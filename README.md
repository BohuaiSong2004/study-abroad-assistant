# Waypoint — Study Abroad Application Assistant

[![CI](https://github.com/BohuaiSong2004/study-abroad-assistant/actions/workflows/ci.yml/badge.svg)](https://github.com/BohuaiSong2004/study-abroad-assistant/actions/workflows/ci.yml)

Waypoint helps prospective students plan applications to programs abroad. A
student picks programs and rounds; Waypoint turns each program's deadline and
requirements into a dated plan (documents, test scores, recommendation letters,
submission) and **keeps every plan in sync across services** when a counselor
changes a deadline, a document is ticked off, or a recommender submits a letter.

## Screenshots

| Student dashboard | Application timeline |
| --- | --- |
| ![Dashboard with three applications, progress and the 30-day agenda](docs/screenshots/dashboard.png) | ![Generated timeline with overdue items, a recommendation letter and a custom milestone](docs/screenshots/application.png) |
| **The same timeline in Chinese** | **Counselor catalog and sync monitor** |
| ![Timeline rendered in Simplified Chinese](docs/screenshots/application-zh.png) | ![Program catalog with the timeline sync panel](docs/screenshots/counselor.png) |

Landing page and phone layout: [landing](docs/screenshots/landing.png) · [mobile](docs/screenshots/mobile.png).

## What it does

- **Programs catalog** — search by university, field, country and degree; each
  program has rounds (deadlines in the program's own time zone) and requirements.
- **Synced application timelines** — milestones are generated from the round
  deadline (e.g. ask recommenders six weeks out, send test scores a month out)
  and re-planned automatically when anything they depend on changes. Students
  can add their own milestones; they survive re-planning.
- **Recommendation letters** — students invite recommenders with a secret link
  (no account needed); letters the student waived access to are never shown to
  them. Timelines update when letters arrive or a recommender declines.
- **Live updates** — open pages update over Server-Sent Events when a timeline
  changes in the backend (for example, a counselor edits a deadline).
- **XML export** — timelines and recommendation packets download as XML that
  validates against published XSD schemas ([`/schemas/timeline-v1.xsd`](internal/xmlexport/schemas/timeline-v1.xsd),
  [`/schemas/packet-v1.xsd`](internal/xmlexport/schemas/packet-v1.xsd)).
- **Counselor console** — edit programs and rounds; watch the sync streams.
- **English and Simplified Chinese** — the UI follows the browser's language
  or the header switch, including the milestones and warnings the planner
  generates, dates, fees and API error messages.

## Architecture

```mermaid
flowchart LR
  B[Browser<br/>HTML5 · CSS3 · jQuery] -- JSON / SSE --> G[gateway<br/>sessions · CSRF · rate limits<br/>XML export]
  G -- gRPC --> C[catalog]
  G -- gRPC --> A[applications<br/>accounts · letters]
  G -- gRPC --> T[timeline]
  A -- gRPC GetProgram --> C
  C -. "EventStream.Subscribe<br/>(catalog outbox)" .-> T
  A -. "EventStream.Subscribe<br/>(applications outbox)" .-> T
  T -. "EventStream.Subscribe<br/>(timeline outbox)" .-> G
  C --- DC[(DynamoDB<br/>catalog)]
  A --- DA[(DynamoDB<br/>applications)]
  T --- DT[(DynamoDB<br/>timeline)]
```

- **Go gRPC services**, each owning one DynamoDB table (single-table design).
  Data crosses service boundaries only through gRPC calls and event streams.
- **Cross-service sync**: every write commits its domain event to a
  **transactional outbox** in the same DynamoDB transaction. Consumers tail it
  with a server-streaming `Subscribe` RPC, keep a durable cursor, apply events
  **in parallel per aggregate** with a watermark cursor, and are idempotent
  (version checks), so a crash or redelivery never corrupts a timeline.
- **Caching**: services read through a narrow `ddb.API` interface implemented by
  the AWS SDK client, the **DAX** client (`aws-dax-go-v2`, production) and an
  in-process **DAX-style read-through cache** (local/dev), selected with
  `CACHE_MODE=off|local|dax`. Because DAX's query cache ignores writes, CI
  also runs the system and browser tests with `CACHE_MODE=daxsim` (the local
  cache with DAX's contract); reads that must reflect a change use strongly
  consistent reads, which DAX passes through.

Details: [docs/architecture.md](docs/architecture.md) ·
Measurements: [docs/performance.md](docs/performance.md) ·
Bugs found along the way: [docs/engineering-notes.md](docs/engineering-notes.md)

## Quick start (Docker)

```sh
docker compose up --build
```

Open <http://localhost:8080>, create a student account, or sign in as the
counselor: `counselor@example.com` / `counselor-demo-password`.

The stack: DynamoDB Local, the three services, the gateway, a one-shot job
that seeds ~40 sample programs, and [Mailpit](https://mailpit.axllent.org),
which catches the invitation and password-reset emails: open its inbox at
<http://localhost:8025>.

Data lives in Docker volumes and survives restarts and `docker compose down`;
`docker compose down -v` starts from scratch. On every start the seed job
adds missing sample programs and moves a sample whose rounds have all passed
to the next intake, and the certs job renews internal TLS certificates before
they expire, so a stack started again months later still works.

## Development without Docker for the services

Requirements: Go 1.26+, and DynamoDB Local (Docker or the AWS jar).

```sh
docker run --rm -p 8000:8000 amazon/dynamodb-local   # terminal 1
./scripts/dev.sh                                     # terminal 2: builds, runs, seeds
```

`dev.sh` serves the UI from `web/static` on disk, so HTML/CSS/JS changes show
up on reload.

## Tests

| Suite | What | Run |
| --- | --- | --- |
| Go unit + integration | 91 test functions: planner rules, cache races and cross-replica coherence, DAX semantics, outbox gaps and tombstones, consumer resume/watermark/leases/dead letters, full services over in-memory gRPC, shared rate limits and lockout, password reset, mutual TLS, gateway HTTP (CSRF, SSE, XML, language negotiation), Chinese dictionary completeness | `DYNAMODB_ENDPOINT=http://localhost:8000 go test -race ./...` |
| XML schemas | Every export validated with `xmllint` against the XSDs | part of the Go tests (needs `libxml2-utils`) |
| Browser (Playwright) | 20 end-to-end tests on Chromium, Firefox and WebKit: live sync, no-JavaScript sign-in, lockout, emails read from Mailpit, Chinese UI, axe accessibility, mobile layout | `cd e2e && npm ci && npx playwright install chromium && npx playwright test --project=desktop --project=mobile` (stack running; `E2E_MAILPIT_URL=http://localhost:8025` for the email tests) |
| Load (k6) | cache on/off, concurrent writes, deadline fan-out | see [docs/performance.md](docs/performance.md) |

Application-code coverage is about 84% (`go test -coverpkg=./internal/...`).
CI runs lint (golangci-lint, `buf lint`, generated-code check); the Go suites
against DynamoDB Local, and the system tests again with the cache off and with
DAX semantics; Playwright per browser against the dev-mode stack (and with
`CACHE_MODE=daxsim`); the Docker images with a compose smoke test, a check
that internal gRPC refuses plaintext, and Playwright against compose; and
`terraform validate`, tflint and Checkov.

## Performance (measured locally)

All on one 2-vCPU VM, services in Docker against DynamoDB Local; full method
and raw numbers in [docs/performance.md](docs/performance.md).

| | `CACHE_MODE=off` | `CACHE_MODE=local` |
| --- | --- | --- |
| `GET /timeline` median, 50 req/s | 3.63 ms | 1.93 ms (−47%) |
| `GET /programs/{id}` median, 50 req/s | 3.29 ms | 1.61 ms (−51%) |
| Program search median, 50 req/s | 10.5 ms | 2.1 ms (−80%) |
| Strongly consistent list (never cached, control) | 3.93 ms | 4.26 ms |
| Read mix throughput, 20 concurrent clients | 287 req/s | 1,277 req/s (4.4×) |
| Cache hit ratio (timeline / catalog items) | — | 99% / 99% |

Sync: a change is applied to the timeline in **38–72 ms median** at low load
(three runs); a burst of 100 students making 500 changes is fully synced with
0 errors (client-observed p50 1.7–2.3 s, the same range as the previous build
on the same day); a deadline change re-plans **300 timelines in 2.7–3.4 s**.
Lighthouse (mobile): accessibility, best practices and SEO 100 on every page,
in English and Chinese; performance 88–100 depending on page and run.

## Operations

```bash
# dev mode: go run ./cmd/waypointctl …   compose: docker compose run --rm ctl …
waypointctl status                          # cursor, lag, leader, parked events per stream
waypointctl dlq list                        # events that could not be applied, with the error
waypointctl dlq replay timeline.applications 42
waypointctl resync [-student usr_…]         # re-plan after a planner fix (writes only changed plans)
```

Every service also exposes `/metrics` (Prometheus), `/healthz` and
`/readyz` on its admin port.

## Project layout

```
proto/                 gRPC API definitions (buf)       gen/  generated Go code
cmd/                   catalog, applications, timeline, gateway, seed, waypointctl (ops CLI), devcerts
internal/
  catalog/ applications/ timeline/   service implementations
  events/              outbox, Subscribe server, consumer, leases, watermark
  ddb/                 DynamoDB/DAX client interface, tables, read-through cache
  gateway/             HTTP API, sessions, CSRF, SSE, static assets
  xmlexport/           XML encoders + XSD schemas
  platform/            config, logging, gRPC interceptors, internal auth
web/static/            HTML5/CSS3/jQuery UI
e2e/                   Playwright tests        loadtest/  k6 scripts, benchmarks
deploy/terraform/      AWS: DynamoDB, DAX, ECS Fargate, ALB, Cloud Map
```

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `DYNAMODB_ENDPOINT` | (AWS) | e.g. `http://localhost:8000` for DynamoDB Local |
| `CACHE_MODE` | `local` | `off`, `local` (in-process cache), `daxsim` (local cache with DAX semantics, for tests) or `dax` |
| `DAX_ENDPOINT` | | `daxs://…` cluster endpoint when `CACHE_MODE=dax` |
| `INTERNAL_TOKEN` | dev value | shared secret between gateway and services (required in production) |
| `GRPC_TLS_CA` / `_CERT` / `_KEY` | | mutual TLS for internal gRPC (set in docker compose; all three or none) |
| `PUBLIC_URL` | `http://localhost:8080` | origin used for invitation links and the CSRF check |
| `TRUSTED_PROXIES` | | CIDRs whose `X-Forwarded-For` is trusted (the load balancer) |
| `AUTH_RATE_PER_MIN` | `20` | gateway (per replica): sign-in/sign-up attempts per client IP per minute |
| `LOGIN_LIMIT_PER_IP` | `60` | applications (shared): sign-in attempts per address per sliding 10 minutes |
| `LOGIN_FAILURES_PER_ACCOUNT` | `10` | applications (shared): failed sign-ins per account per sliding 15 minutes before lockout |
| `REGISTER_LIMIT_PER_IP` | `20` | applications (shared): sign-ups per address per sliding hour |
| `CONSUMER_WORKERS` | `8` | parallel event appliers per stream (per-aggregate order kept) |
| `BOOTSTRAP_COUNSELOR_EMAIL` / `_PASSWORD` | | creates the first counselor account |
| `MAIL_MODE` | `log` | applications: `log` (development; refused in production) or `smtp` |
| `SMTP_ADDR` / `SMTP_USERNAME` / `SMTP_PASSWORD` | | SMTP relay (Mailpit in compose, SES in the Terraform) |
| `MAIL_FROM` | `Waypoint <no-reply@waypoint.local>` | sender of invitations and password resets |

## Known limitations

- Program search filters the listing index in DynamoDB, which is fine for a
  catalog of a few thousand programs; a much larger catalog would use a search
  engine.
