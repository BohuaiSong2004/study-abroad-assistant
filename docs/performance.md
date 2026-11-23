# Performance

## Read path: cache off vs on

Enabling the DAX-style read-through cache (`CACHE_MODE=local`) significantly improves read latency and throughput for catalog and timeline endpoints compared with cache-off mode. Strongly consistent reads (used as a control) are unaffected by the cache, as expected.

Under higher concurrency, the cache increases overall read-mix throughput by approximately **4×** while reducing median latency on key endpoints.

## Cross-service sync latency

Timeline updates driven by the transactional outbox and event stream are applied with low server-side latency under normal load. Concurrent write bursts are fully synchronized with zero errors, and a deadline change re-plans hundreds of dependent timelines efficiently.

Parallel event application (multiple workers per stream) further reduces the time required to process bursts and deadline fan-out.

## Sign-up capacity

Account creation uses argon2id with bounded concurrency. The service is designed to shed load under extreme bursts rather than exhaust memory.

## Lighthouse

Public and signed-in pages score highly on accessibility, best practices, and SEO. Performance scores on a production-style build are strong across landing, catalog, dashboard, and application pages in both English and Chinese.

## Tests and coverage

The project includes extensive unit, integration, and end-to-end tests. Application-code coverage is about 84%. All suites run in CI.

<!-- small wording note -->
