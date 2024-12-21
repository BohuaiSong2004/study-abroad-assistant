# Performance

Everything here was measured on **one machine**, so the numbers describe this
code on this hardware, not a production deployment:

- 2 vCPU Intel Xeon @ 2.10 GHz, 7.8 GB RAM, Linux (a shared VM)
- services in Docker Compose (`docker compose up --build`, internal gRPC over
  mutual TLS), DynamoDB Local (`amazon/dynamodb-local`, in memory, via
  `loadtest/docker-compose.inmemory.yml`; the default stack persists to disk) in Docker,
  k6 1.8 on the same machine
- sections 1 and 3 to 6 re-measured on 23 November 2026 with the services as
  of commit `b175ed9` (unchanged through `8741090`); section 2 and the
  parallel-apply comparison were measured at `54bb840` on 22 November (their
  code paths have not changed since)

**Not measured:** Amazon DAX or real DynamoDB (there is no AWS deployment), and
real user traffic. "Cache" below is the in-process DAX-style read-through cache
(`CACHE_MODE=local`) against DynamoDB Local. DAX would add a network hop
(sub-millisecond in-VPC) but serve a shared cache; DynamoDB proper has higher
latency than DynamoDB Local on localhost, which tends to make a cache *more*
valuable, not less. Treat the percentages as a property of this setup.

Runs vary by roughly ±10–15% between repetitions on this shared VM, and by
more between days: on 23 November the same commit (`54bb840`) was about twice
as slow under the sync burst as on 22 November (section 3), so compare rows
measured on the same day, not across days.

## 1. Read path: cache off vs on (k6, end to end)

`loadtest/k6/reads.js` signs up 20 students with 3 applications each, then
replays a read mix through the gateway:

| Endpoint | Share | Backing read |
| --- | --- | --- |
| `GET /api/applications/{id}/timeline` | 35% | timeline `GetItem` |
| `GET /api/programs/{id}` (popular programs more often) | 25% | catalog `GetItem` |
| `GET /api/programs?q=…` | 15% | catalog index `Query` + filters (1,038 programs) |
| `GET /api/timelines` | 15% | timeline base-table `Query` |
| `GET /api/applications` | 10% | **strongly consistent** `Query`: never cached — the control |

Reproduce: `export COMPOSE_FILE=docker-compose.yml:loadtest/docker-compose.inmemory.yml && docker compose up -d --build && docker compose run --rm seed -synthetic 1000 && ./loadtest/compare-cache.sh`

### Low load: 50 requests/s (open loop, far below capacity)

Latency without CPU contention, i.e. the cache's own effect.

| Endpoint | off: median / p95 | local: median / p95 | Median change |
| --- | --- | --- | --- |
| GET timeline | 3.63 / 7.65 ms | 1.93 / 6.24 ms | −47% |
| GET program | 3.29 / 6.10 ms | 1.61 / 2.81 ms | −51% |
| GET timelines (list) | 4.34 / 9.27 ms | 2.44 / 8.07 ms | −44% |
| Search programs | 10.49 / 46.7 ms | 2.07 / 14.1 ms | −80% |
| GET applications (consistent, control) | 3.93 / 8.39 ms | 4.26 / 9.44 ms | +8% (noise) |

The control endpoint stays within run-to-run noise, as it should: the
difference on the other rows is the cache.

### Saturated: 20 concurrent clients, no think time (closed loop)

| | off | local |
| --- | --- | --- |
| Throughput (all endpoints) | 287 req/s | 1,277 req/s (4.4×) |
| GET timeline median / p95 | 36.2 / 90.7 ms | 12.6 / 32.1 ms (−65% / −65%) |
| GET program median / p95 | 36.2 / 72.8 ms | 9.9 / 27.7 ms (−73% / −62%) |
| Search median / p95 | 97.1 / 474 ms | 10.7 / 30.5 ms (−89% / −94%) |
| GET applications (control) median | 37.0 ms | 14.7 ms |

Under saturation the control also gets faster: the cache frees CPU that
DynamoDB Local (a JVM on the same two cores) was using, so part of these
numbers is reduced contention rather than cache hits.

On 22 November, before compose used mutual TLS, the same mix ran at 359 vs
1,659 req/s (4.6×); every internal call now also pays for TLS. Cache hit
ratios measured then (from `waypoint_ddb_cache_requests_total`): timeline
items 99.1%, timeline queries 98.5%, catalog items 99.3%, catalog queries
99.1%.

## 2. Store level: `GetItem` with and without the cache

`go run ./loadtest/cachebench -items 2000 -reads 20000 -concurrency 8` writes
2,000 program items to a fresh table and replays the same Zipf-distributed key
sequence (s = 1.1) through the raw client and through `CachedAPI`:

| | p50 | p95 | p99 | mean | reads/s |
| --- | --- | --- | --- | --- | --- |
| DynamoDB Local | 2.861 ms | 6.420 ms | 11.472 ms | 3.320 ms | 2,409 |
| read-through cache | 0.004 ms | 2.971 ms | 6.045 ms | 0.328 ms | 24,261 |

Hit ratio 91.9% (each key's first read misses). With one reader instead of
eight, DynamoDB Local answers in 0.74 ms median and the cache in 3 µs.

## 3. Cross-service sync latency

Server-side latency is `waypoint_sync_latency_seconds`: from the event's
commit in the source service to "applied" in the timeline service (quantiles
interpolated from the histogram with `loadtest/histogram.py`). Client-observed
latency is how long a client waits, after its write returns, until
`GET /timeline` reflects it (polling every 50 ms, so it includes up to 50 ms
of polling granularity).

`loadtest/k6/writes.js`: each student starts an application, ticks three
checklist items and invites a recommender (5 changes), waiting each time until
the timeline reflects the change.

| Scenario | Server p50 / p95 | Client p50 / p95 | Errors |
| --- | --- | --- | --- |
| 5 students (25 changes), low load, three runs | 38–72 / 79–99 ms | 63–69 / 77–123 ms | 0 |
| 100 students at once (500 changes), three runs | 1.79–2.66 s / 4.28–4.97 s | 1.70–2.34 s / 2.85–3.92 s | 0 |
| same burst, commit `54bb840`, same day, two runs | 2.25–2.76 s / 4.85–5.54 s | 2.06–2.36 s / 3.71–4.21 s | 0 |

The burst rows are a 2-vCPU machine running k6, five services and DynamoDB
Local while 100 clients poll every 50 ms: latency there is queueing. On 22
November `54bb840` measured 0.89 / 1.62 s client-side for this burst; the
same build was much slower on the 23rd, which is why the current code is
compared with it on the same day. Since then, sync handlers read the program
projection with strong consistency and every replica tails its own outbox
(cache coherence); under the same burst on the same day the two builds land
in overlapping ranges, so these changes cost nothing measurable here.

### Parallel apply (same build, `CONSUMER_WORKERS` / `REPLAN_PARALLELISM`)

| | 1 worker (sequential) | 8 workers |
| --- | --- | --- |
| 100-student burst, client p50 / p95 | 2.02 / 2.89 s | 0.92 / 1.88 s |
| Deadline change → 300 timelines re-planned | 4.18 s | 1.99 s |

Before the change, the consumer applied one event at a time; see
[engineering notes](engineering-notes.md#5-sync-backlog-under-a-burst).

## 4. Deadline fan-out

`loadtest/k6/fanout.js`: 300 students apply to the same program; a counselor
moves its deadline; the script polls all 300 timelines until each shows the new
date. **300/300 re-planned in 2.68 s and 3.35 s** (two runs on 23 November),
including the time to poll 300 timelines per round. On 22 November the same
test took 1.99 s and 2.25 s; see section 3 for the day-to-day difference.

## 5. Sign-up capacity

Accounts are created with argon2id (19 MiB memory, t = 2), with at most 4
hashes in flight per applications replica. Sequential sign-ups through the
gateway (`k6 run loadtest/k6/signups.js`): **18.1 and 18.5 per second** in two
runs on 23 November, against 19–20.5 for `54bb840` on the same day; each
sign-up now also updates the shared per-address limit in DynamoDB. A burst of 100 simultaneous
sign-ups queues behind the hashing limit and some requests reach the gateway's
5 s backend deadline (504) — by design, the service sheds load instead of
running out of memory. The load tests therefore create accounts up front and
run with `AUTH_RATE_PER_MIN` raised and the shared per-address limits raised
(docker compose does the latter), since one machine creating hundreds of
accounts a minute is exactly what the limits stop.

## 6. Lighthouse

Lighthouse 13 (default mobile emulation, simulated throttling) against the
Docker stack; signed-in pages with a session cookie:

| Page | Performance | Accessibility | Best practices | SEO | CLS |
| --- | --- | --- | --- | --- | --- |
| `/` | 100 | 100 | 100 | 100 | 0 |
| `/programs.html` | 96 | 100 | 100 | 100 | 0 |
| `/dashboard.html` (1 application) | 95 | 100 | 100 | 100 | 0.005 |
| `/dashboard.html` (3 applications, first visit, two runs) | 88–94 | 100 | 100 | 100 | 0.108 |
| `/application.html` | 98 | 100 | 100 | 100 | 0 |
| `/dashboard.html`, Chinese | 97 | 100 | 100 | 100 | 0.009 |
| `/application.html`, Chinese | 95 | 100 | 100 | 100 | 0 |

Performance scores move by several points between runs on this VM
(simulated throttling scales with available CPU). Lighthouse starts with an
empty profile, so the dashboard draws one placeholder card: a first visit
with three applications still shifts (0.108, as before). A returning
student's browser draws as many placeholders as they had last time, which
Lighthouse cannot show.

The first run scored 76–86 on the signed-in pages (CLS up to 0.65) and found a
colour-contrast failure; the fixes are in the
[engineering notes](engineering-notes.md#7-layout-shifts-and-contrast).

## Reproducing everything

```sh
export COMPOSE_FILE=docker-compose.yml:loadtest/docker-compose.inmemory.yml   # in-memory DynamoDB Local
docker compose up -d --build
docker compose run --rm seed -synthetic 1000
./loadtest/compare-cache.sh                                   # section 1
go run ./loadtest/cachebench                                  # section 2
AUTH_RATE_PER_MIN=100000 docker compose up -d gateway
k6 run loadtest/k6/writes.js                                  # section 3
VUS=5 k6 run loadtest/k6/writes.js
k6 run loadtest/k6/fanout.js                                  # section 4
k6 run loadtest/k6/signups.js                                 # section 5
# server-side sync latency (timeline admin port is on the compose network):
docker run --rm --network waypoint_default curlimages/curl -s http://timeline:9103/metrics \
  | python3 loadtest/histogram.py - waypoint_sync_latency_seconds
```
