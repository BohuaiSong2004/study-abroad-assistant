# Architecture

## Services

| Service | Owns (DynamoDB table) | gRPC API | Consumes |
| --- | --- | --- | --- |
| **catalog** | programs, rounds, requirements (`catalog`) | `CatalogService`, `EventStreamService` | — |
| **applications** | accounts, sessions, applications, recommendations, invitation tokens (`applications`) | `AccountService`, `ApplicationService`, `RecommendationService`, `EventStreamService` | calls `CatalogService.GetProgram` |
| **timeline** | timelines, program projection, cursors, leases (`timeline`) | `TimelineService`, `EventStreamService` | catalog + applications streams |
| **gateway** | nothing | HTTP/JSON, SSE, static UI | timeline stream (live updates) |

Each service owns exactly one table and no service reads another service's
table; in the Terraform the IAM task role of each service can only touch its
own table. The protobuf contracts are in [`proto/`](../proto).

### Internal authentication

The gateway authenticates users (session cookie) and forwards the caller to
services as gRPC metadata (`x-waypoint-user-id`, `x-waypoint-user-role`). The
services accept that metadata only together with a shared internal token
(`INTERNAL_TOKEN`, from Secrets Manager in the Terraform), and enforce
authorization themselves: a student's data lives under `pk=STUDENT#<id>`, so
every query is scoped to the caller structurally. In a larger deployment the
token would be replaced by mTLS identities; the services' checks would not
change.

## Data model (single-table design)

```
catalog       PROGRAM#<id> | META            program (protobuf), version, filter attributes
              gsi1: PROGRAMS | <university>#<name>#<id>      listing / search
applications  USER#<id> | PROFILE            email, name, role, argon2id hash
              EMAIL#<email> | EMAIL          uniqueness guard
              SESSION#<sha256> | SESSION     user, expiry (DynamoDB TTL)
              STUDENT#<sid> | APP#<aid>       application (protobuf), version
              STUDENT#<sid> | PROGRAM#<pid>   one application per program
              STUDENT#<sid> | REC#<aid>#<rid> recommendation (protobuf), status
              RECTOKEN#<sha256> | TOKEN      invitation token -> recommendation
timeline      STUDENT#<sid> | TL#<aid>        timeline + application snapshot, versions
              gsi1: PROGRAM#<pid> | TL#<aid>  re-plan on program change
              PROG#<pid> | META              program projection
              CURSOR#<consumer>, LEASE#<consumer>, DLQ#<consumer>
every table   EVT#<stream> | <seq>            outbox (TTL 30 days)
              EVTHEAD#<stream> | HEAD         sequence counter
```

Entities are stored as protobuf bytes plus the few attributes needed for keys,
indexes, filters and condition expressions. Every mutable entity carries a
`version` used for optimistic concurrency (`ConditionExpression: version = :v`).
Secrets (session tokens, invitation tokens) are stored only as SHA-256 hashes.

## Cross-service sync

### Outbox

`events.Outbox.Commit` writes the state change and its event in **one**
`TransactWriteItems` call, so an event exists if and only if the change
committed. Sequence numbers come from an atomic counter (`UpdateItem ADD`)
taken just before the transaction, which has two consequences:

1. Numbers can commit out of order (A takes 5, B takes 6, B commits first).
2. A failed transaction leaves a hole.

The writer of a failed transaction fills its hole with a *tombstone* (a
conditional put, so it can never overwrite an event whose transaction actually
committed but whose response was lost). For any other hole, the subscriber
waits up to `GapTimeout` (5 s) from first noticing it, then skips it.

### Subscribe

`EventStreamService.Subscribe(after_seq)` streams events strictly in sequence
order. Local commits wake waiting subscribers immediately (in-process
notifier); events written by other replicas are picked up by polling (250 ms).
Heartbeats carry the head sequence number so consumers can report lag.

### Consumers

`events.Consumer`:

- loads its cursor (`CURSOR#<name>`), subscribes after it, reconnects with
  jittered backoff (reset after a healthy session);
- applies events with `Workers` goroutines, **partitioned by aggregate id**: two
  events about the same application always go to the same worker in stream
  order; different applications proceed in parallel;
- persists only the **watermark** — the highest sequence number such that it
  and everything before it is applied — so a crash replays from the oldest
  unfinished event, never skipping one;
- retries transient handler errors with backoff (blocking that partition, by
  design), and parks permanent errors (malformed payloads) in a dead-letter item;
- holds a DynamoDB **lease** so one replica of the timeline service consumes a
  given stream at a time (correctness does not depend on it; it avoids
  duplicated work).

Handlers are idempotent: a timeline records the application version and the
program version it was planned from, and ignores events that do not carry a
newer one. Replaying a whole stream from zero changes nothing (tested).

### Operating the sync

`SyncAdminService` (callers with the internal `service` role) and the
`waypointctl` CLI cover the two repairs an operator needs:

- **Dead letters.** `waypointctl dlq list` shows parked events with their
  error; after fixing the cause (e.g. the referenced program now exists),
  `waypointctl dlq replay CONSUMER SEQ` runs the handler again and removes the
  item on success, or records the new error and keeps it. The counselor page
  and `waypointctl status` show a parked-event count per stream.
- **Planner fixes.** Rewinding a cursor would not help, because the handlers
  skip events they already applied. `waypointctl resync [-student ID]`
  re-plans each timeline from the application snapshot it stores and the
  current program, and writes only the ones whose plan changes (students'
  own milestones are kept).

### Cross-stream ordering

The catalog and applications streams are independent, so:

- an application can reference a program the catalog consumer has not
  projected yet — the timeline service then fetches it with `GetProgram`;
- a program change can race with a timeline being created — re-planning finds
  timelines through an eventually consistent index, so it runs again a few
  seconds later (`RecheckDelay`) to catch late arrivals.

### Live updates

The timeline service publishes `TimelineChanged` on its own outbox. Each
gateway replica holds one `Subscribe(from_tail)` stream and fans events out to
the affected student's browsers over Server-Sent Events. After a reconnect it
sends `resync` so pages reload state they may have missed.

## Caching (DAX)

`internal/ddb.API` is the subset of DynamoDB operations the services use.
`*dynamodb.Client`, `*dax.Dax` and `*ddb.CachedAPI` all implement it, so the
repositories never know which one they have.

`CachedAPI` (`CACHE_MODE=local`) mirrors DAX semantics:

- **item cache** for eventually consistent `GetItem`/`BatchGetItem`, including
  negative results (short TTL);
- **query cache** for eventually consistent `Query`, invalidated by writes to
  the same partition (base-table queries) or by writes that may touch the index
  (index queries);
- `ConsistentRead=true` always bypasses the cache — the same contract as DAX,
  which passes strongly consistent reads through to DynamoDB.

Two details make it safe for read-your-writes in one process: misses are filled
with a strongly consistent read, and a fill is only stored if no write bumped
the key's generation counter while it was in flight (the "slow reader caches a
value a writer just replaced" race; there is a test for it). Concurrent misses
for the same key share one read (singleflight).

Which reads may be cached is a per-query decision: a student's own application
list uses strongly consistent queries (they must see the application they just
created); sessions and invitation tokens are always read consistently (logout
and link revocation must be immediate); program and timeline reads, catalog
search and agenda queries go through the cache.

With several replicas each has its own cache, so each replica also tails its
own service's outbox (`Outbox.Watch`) and drops the keys every event changed.
Writes from other replicas are visible within one outbox poll interval
(250 ms) instead of the item TTL. Event handlers read the program projection
with strong consistency, so a replica that has just taken over the consumer
lease never re-plans with a cached, older program.

In production (`CACHE_MODE=dax`) the DAX cluster is a shared, write-through
item cache; the watcher is not started. DAX's contract is weaker than the
local cache's in one way that matters: its **query cache is not invalidated
by writes**, only by its TTL. Nothing correctness-relevant depends on query
invalidation:

- writers (event handlers) read with strong consistency or the raw client;
- a page reloading after a live update asks for `?fresh=1`, which becomes a
  consistent read;
- the dashboard compares each timeline's `applicationVersion` with the
  (consistently read) application and re-reads once past the cache when a
  timeline is missing or older;
- XML exports always read consistently.

`CACHE_MODE=daxsim` runs the local cache with DAX's contract, and CI runs the
system tests and the browser tests under it. It caught the dashboard case:
a student returning to the dashboard after creating an application saw
"Planning timeline…" until the query TTL expired.

## Gateway security

- Sessions: random 256-bit tokens in an `HttpOnly; SameSite=Lax` cookie
  (`Secure` with https), stored hashed, 7-day expiry, revoked on logout.
- CSRF: unsafe requests must come from an allowed `Origin` (or carry
  `Sec-Fetch-Site` other than `cross-site`); JSON endpoints require
  `Content-Type: application/json`.
- Passwords: argon2id (19 MiB, t=2), with bounded hashing concurrency so a
  burst of sign-ins cannot exhaust memory; unknown emails still pay one hash
  (no account enumeration by timing).
- Rate limits in two layers. The gateway keeps an in-memory token bucket per
  client IP on sign-in, sign-up and invitation endpoints; it is cheap and
  protects one replica's CPU from argon2 floods. The authoritative limits are
  shared across replicas in DynamoDB (sliding-window estimate over two
  fixed-window `ADD` counters with a TTL, hashed keys) in the applications service: sign-ins and sign-ups per
  address, and a **per-account lockout** after repeated failed sign-ins,
  which per-address limits cannot give against a distributed guessing
  attack. The lockout is counted for unknown emails too (no enumeration) and
  checked before the password. The gateway forwards the address in gRPC
  metadata (trusted only with the internal token) and turns the service's
  `RetryInfo` into `Retry-After`. `X-Forwarded-For` is only trusted from
  configured proxies. Invitation endpoints keep only the per-replica limit:
  tokens are 256-bit random, so guessing is not a threat the shared limit
  would address.
- Invitation and password-reset tokens travel in the URL fragment
  (`#token=`), which browsers never send to servers, so they stay out of
  access logs and `Referer` headers. Only their SHA-256 is stored.
- Email (SMTP: Mailpit locally, SES in the Terraform) is sent by the
  applications service while handling the request, not by an outbox worker:
  the raw token exists only in that request, and outbox events are readable
  by other services, so it is never persisted. A delivery failure never fails
  the request (`email_sent=false`; the student can share the link). Subjects
  carry user-supplied names, so header values are stripped of line breaks;
  invitation emails are capped per student per day so the feature cannot be
  used to mail arbitrary addresses at volume.
- Password reset: the response is identical whether or not the email has an
  account, and everything that depends on it (lookup, token write, email)
  runs after the response, so timing does not tell either. Links last 30
  minutes, work once, and are limited per address and per email. A reset
  bumps the account's session epoch, which ends every existing session (the
  session lookup reads the profile with strong consistency; gateways cache a
  session for at most 5 seconds), and lifts a sign-in lockout.
- Strict CSP (`script-src 'self'`, no inline scripts or styles), `nosniff`,
  `frame-ancestors 'none'`, `Referrer-Policy: same-origin`.
- Forms post natively before JavaScript loads (progressive enhancement), so an
  early click never falls back to a GET with the password in the URL.

## Front end

Plain HTML5, CSS3 (custom properties, grid, dark mode via
`prefers-color-scheme`) and jQuery 3.7. Assets are fingerprinted at startup
(`?v=<hash>`, cached for a year) and gzip-compressed once; HTML is always
revalidated with an ETag. Interactive controls start disabled and are enabled
when their handlers are attached.

## Deployment (Terraform, not applied)

ECS Fargate services in private subnets behind an ALB; Cloud Map private DNS for
gRPC discovery (clients dial `dns:///` with round-robin); DAX cluster with a TLS
endpoint and encryption at rest; DynamoDB on-demand tables with PITR; secrets in
Secrets Manager; per-service IAM roles. See [deploy/terraform](../deploy/terraform).
