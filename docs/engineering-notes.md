# Engineering notes: bugs found by tests and measurements

Real defects found while building and verifying the project, with root cause
and fix. Each has a regression test unless noted.

## 1. Sync streams cut every 10 seconds

**Symptom.** In the browser tests, a recommendation request took 10–20 s to
show up on the timeline. Logs showed every consumer and the gateway's
live-update stream disconnecting with `DeadlineExceeded` every 10 s.

**Cause.** The gRPC client service config set a 10 s timeout for
`"name": [{}]` — every method, including the long-lived `Subscribe` stream. On
top of that, the consumer's reconnect backoff only reset after a session ended
*without* an error, which never happened, so it grew to 10 s.

**Fix.** A method-specific config entry for `EventStreamService/Subscribe`
without a timeout (a specific entry wins over the default), and backoff reset
after any session that received data. Tests now dial with the production
options and a 300 ms unary deadline; `TestSubscribeOutlivesUnaryDeadline`
fails without the fix.

## 2. No-JavaScript sign-in rejected as cross-site

**Symptom.** The progressive-enhancement test (sign in with JavaScript
disabled; the form posts natively) got `403 bad_origin`.

**Cause.** The site sent `Referrer-Policy: no-referrer`. Under that policy
Chrome sends `Origin: null` for form-post navigations, and the CSRF check
(correctly) refuses `null`.

**Fix.** `Referrer-Policy: same-origin`: nothing leaks cross-site, and
invitation tokens live in the URL fragment, which is never part of a
`Referer` anyway. Covered by the Playwright test.

## 3. Query cache invalidated by unrelated writes

**Symptom.** Measuring the read path, `GET /api/timelines` barely improved
with the cache on; the timeline service's query hit ratio was ~50%.

**Cause.** Query-cache invalidation used one generation counter per table. The
timeline consumers renew their lease (a write to an unrelated item) every 5 s,
which emptied every cached query in the table.

**Fix.** Base-table queries are invalidated only by writes to the same
partition key; index queries only by writes that may touch the index (Puts of
items without index keys, like outbox events and leases, cannot). Hit ratio
went to about 98.5% and the endpoint's median latency dropped 54%.
`TestQueryInvalidationIsScoped`.

## 4. 503s right after a backend restart

**Symptom.** Restarting the timeline container during a load test produced a
handful of 503s from the gateway — after the container was already healthy.

**Cause.** The gateway's gRPC channel was still in reconnect backoff (gRPC's
default grows to two minutes), and the retry policy gave up within ~350 ms.

**Fix.** `waitForReady` for service-to-service calls (bounded by each call's
deadline) and a reconnect backoff capped at 2 s.
`TestCallsWaitForABackendThatIsStillStarting` starts the server 700 ms after
the call and fails without the fix with exactly the production error.

## 5. Sync backlog under a burst

**Symptom.** 100 students making 500 changes within a few seconds: server-side
sync p95 4.5 s.

**Cause.** The consumer applied one event at a time (each is about four
DynamoDB calls), so a burst became a queue.

**Fix.** Events are partitioned by aggregate (application) id across workers;
order is preserved within an application, which is all the planner needs. The
cursor only advances to the watermark (everything before it applied), so a
crash replays from the oldest unfinished event. On the same build and
hardware (1 worker vs 8), server-side sync went from p50 1.33 s / p95 4.1 s to
p50 0.59 s / p95 2.3 s, and re-planning 300 timelines after a deadline change
from 4.2 s to 2.0 s. `TestParallelApplyKeepsPerAggregateOrder`,
`TestCursorNeverPassesAnUnfinishedEvent`, `TestWatermarkFoldsCompletedPrefix`.

## 6. jQuery `$.when` and `$.ajax`

**Symptom.** The dashboard rendered nothing; console: `Cannot read properties
of undefined (reading 'sort')`.

**Cause.** A jqXHR resolves with `(data, statusText, jqXHR)`; `$.when` with
several of them hands each callback an *array*, not the data.

**Fix.** The API helper resolves with the body only. (Found by the first UI
smoke test; covered by every Playwright test that loads the dashboard.)

## 7. Layout shifts and contrast

**Symptom.** Lighthouse (mobile) scored the signed-in pages 76–86 with CLS up
to 0.65, and failed colour contrast.

**Causes and fixes.**

- The header grew when the script filled in the navigation and account menu
  (on phones it wrapped to two lines): fixed height, and a two-row grid on
  narrow screens whose rows exist before the script runs.
- The footer sat in the viewport and was pushed down when data arrived:
  `main` has a viewport minimum height.
- The application page header is a wrapping flex row: with the placeholder
  title "Loading…", the live indicator fit beside it; the real university name
  pushed it to a new line and the page moved 36 px. Found with a
  `PerformanceObserver` for `layout-shift`; fixed by stacking the header on
  narrow screens.
- Skeleton placeholders for the timeline and application cards; the
  application page fetches its data in parallel with the session check.
- The amber "days left" pill was 4.48:1 (needs 4.5:1). The axe browser test
  had not caught it because its test deadlines were never within 14 days; it
  now creates one that is.

## 8. Test data collisions

Re-running the browser tests against the same database failed because test
universities were named after the last six characters of a generated id,
which repeated across runs. Names now use the full id.
