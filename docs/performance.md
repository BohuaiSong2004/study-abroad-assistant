# Performance

Everything here was measured on **one machine**, so the numbers describe this
code on this hardware, not a production deployment:

- 2 vCPU Intel Xeon @ 2.10 GHz, 7.8 GB RAM, Linux
- services in Docker Compose (`docker compose up --build`), DynamoDB Local
  (`amazon/dynamodb-local`, in memory) in Docker, k6 1.8 on the same machine
- commit `54bb840` (November 2026)

**Not measured:** Amazon DAX or real DynamoDB (there is no AWS deployment), and
real user traffic. "Cache" below is the in-process DAX-style read-through cache
(`CACHE_MODE=local`) against DynamoDB Local. DAX would add a network hop
(sub-millisecond in-VPC) but serve a shared cache; DynamoDB proper has higher
latency than DynamoDB Local on localhost, which tends to make a cache *more*
valuable, not less. Treat the percentages as a property of this setup.

Runs vary by roughly ±10–15% between repetitions on this shared VM.

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

Reproduce: `docker compose up -d --build && docker compose run --rm seed -synthetic 1000 && ./loadtest/compare-cache.sh`

### Low load: 50 requests/s (open loop, far below capacity)

Latency without CPU contention, i.e. the cache's own effect.

| Endpoint | off: median / p95 | local: median / p95 | Median change |
| --- | --- | --- | --- |
| GET timeline | 3.24 / 7.09 ms | 1.51 / 3.98 ms | −53% |
| GET program | 2.93 / 5.34 ms | 1.36 / 2.57 ms | −54% |
| GET timelines (list) | 3.85 / 8.30 ms | 1.76 / 5.89 ms | −54% |
| Search programs | 12.18 / 42.1 ms | 1.68 / 11.4 ms | −86% |
| GET applications (consistent, control) | 3.61 / 7.93 ms | 3.64 / 6.00 ms | ±0% |

The control endpoint does not change, as it should not: the difference on the
other rows is the cache.

### Saturated: 20 concurrent clients, no think time (closed loop)

| | off | local |
| --- | --- | --- |
| Throughput (all endpoints) | 359 req/s | 1,659 req/s (4.6×) |
| GET timeline median / p95 | 29.8 / 62.1 ms | 9.2 / 23.6 ms (−69% / −62%) |
| GET program median / p95 | 30.8 / 58.3 ms | 8.3 / 22.5 ms (−73% / −61%) |
| Search median / p95 | 93.2 / 404 ms | 8.8 / 24.9 ms (−91% / −94%) |
| GET applications (control) median | 30.1 ms | 11.8 ms |

Under saturation the control also gets faster: the cache frees CPU that
DynamoDB Local (a JVM on the same two cores) was using, so part of these
numbers is reduced contention rather than cache hits.

Cache hit ratios after the two `local` runs (from
`waypoint_ddb_cache_requests_total`): timeline items 99.1%, timeline queries
98.5%, catalog items 99.3%, catalog queries 99.1%.

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
| 5 students (25 changes), low load | 22.5 / 49.3 ms | 58 / 70 ms | 0 |
| 100 students at once (500 changes in ~10 s) | 997 ms / 2.35 s | 0.89 / 1.62 s | 0 |

The burst row is a 2-vCPU machine running k6, five services and DynamoDB Local
while 100 clients poll: latency there is queueing.

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
date. **300/300 re-planned in 1.99 s and 2.25 s** (two runs), including the
time to poll 300 timelines per round.

## 5. Sign-up capacity

Accounts are created with argon2id (19 MiB memory, t = 2), with at most 4
hashes in flight per applications replica. Sequential sign-ups through the
gateway: **24–26 per second** on this machine. A burst of 100 simultaneous
sign-ups queues behind the hashing limit and some requests reach the gateway's
5 s backend deadline (504) — by design, the service sheds load instead of
running out of memory. The load tests therefore create accounts up front and
run with `AUTH_RATE_PER_MIN` raised (the per-IP limiter otherwise stops one
machine from creating hundreds of accounts a minute).

## 6. Lighthouse

Lighthouse 13 (default mobile emulation, simulated throttling) against the
Docker stack; signed-in pages with a session cookie:

| Page | Performance | Accessibility | Best practices | SEO | CLS |
| --- | --- | --- | --- | --- | --- |
| `/` | 100 | 100 | 100 | 100 | 0 |
| `/programs.html` | 99 | 100 | 100 | 100 | 0 |
| `/dashboard.html` | 97 | 100 | 100 | 100 | 0.10 |
| `/application.html` | 99 | 100 | 100 | 100 | 0 |

The first run scored 76–86 on the signed-in pages (CLS up to 0.65) and found a
colour-contrast failure; the fixes are in the
[engineering notes](engineering-notes.md#7-layout-shifts-and-contrast).

## Reproducing everything

```sh
docker compose up -d --build
docker compose run --rm seed -synthetic 1000
./loadtest/compare-cache.sh                                   # section 1
go run ./loadtest/cachebench                                  # section 2
AUTH_RATE_PER_MIN=100000 docker compose up -d gateway
k6 run loadtest/k6/writes.js                                  # section 3
VUS=5 k6 run loadtest/k6/writes.js
k6 run loadtest/k6/fanout.js                                  # section 4
# server-side sync latency (timeline admin port is on the compose network):
docker run --rm --network waypoint_default curlimages/curl -s http://timeline:9103/metrics \
  | python3 loadtest/histogram.py - waypoint_sync_latency_seconds
```
