// Concurrency + sync benchmark: many students at the same moment start an
// application, tick checklist items and invite a recommender, while the
// timeline service keeps every plan in sync from the event streams.
// Accounts are created in setup(): sign-up is deliberately expensive
// (argon2id, bounded concurrency) and is measured separately.
// "sync_observed_ms" is the time a client waits, after its write returns,
// until GET /timeline reflects it (eventual consistency as users see it).
import { check } from "k6";
import { Counter, Trend } from "k6/metrics";
import { patch, poll, post, programIds, register, waitFor } from "./lib.js";

const syncObserved = new Trend("sync_observed_ms", true);
const conflicts = new Counter("version_conflicts");
const N = Number(__ENV.VUS || 100);

export const options = {
  scenarios: {
    students: { executor: "per-vu-iterations", vus: N, iterations: 1, maxDuration: "5m" },
  },
  setupTimeout: "5m",
  thresholds: { http_req_failed: ["rate<0.01"], checks: ["rate>0.99"] },
  summaryTrendStats: ["avg", "med", "p(90)", "p(95)", "p(99)", "max", "count"],
};

export function setup() {
  const start = Date.now();
  const tokens = [];
  for (let i = 0; i < N; i++) tokens.push(register("writes", i));
  const secs = (Date.now() - start) / 1000;
  console.log(`setup: ${N} sequential sign-ups in ${secs.toFixed(1)} s (${(N / secs).toFixed(1)}/s, argon2id)`);
  return { programs: programIds(30), tokens };
}

function milestone(tl, id) {
  return (tl.milestones || []).find((m) => m.id === id);
}

export default function (data) {
  const token = data.tokens[__VU - 1];
  const programId = data.programs[__VU % data.programs.length];
  let res = post("/api/applications", { programId }, token, { name: "create application" });
  check(res, { "created": (r) => r.status === 201 });
  let app = res.json();

  const t0 = Date.now();
  const ready = waitFor(() => poll(`/api/applications/${app.id}/timeline`, token).status === 200, 15000);
  check(ready, { "timeline planned": (v) => v >= 0 });
  syncObserved.add(Date.now() - t0, { change: "create" });

  for (const item of app.checklist.slice(0, 3)) {
    res = patch(`/api/applications/${app.id}`, { version: app.version, checklist: [{ document: item.document, done: true }] }, token, { name: "tick checklist" });
    if (res.status === 409) conflicts.add(1);
    check(res, { "checklist updated": (r) => r.status === 200 });
    app = res.json();
    const mid = { statement_of_purpose: "gen:sop", cv: "gen:cv", transcript: "gen:transcript", english_scores: "gen:english", gre_scores: "gen:gre", writing_sample: "gen:writing-sample", portfolio: "gen:portfolio" }[item.document];
    const t1 = Date.now();
    const ok = waitFor(() => {
      const r = poll(`/api/applications/${app.id}/timeline`, token);
      const m = r.status === 200 && milestone(r.json(), mid);
      return m && m.done;
    }, 15000);
    check(ok, { "checklist synced": (v) => v >= 0 });
    syncObserved.add(Date.now() - t1, { change: "checklist" });
  }

  res = post(`/api/applications/${app.id}/recommendations`, { name: `Recommender ${__VU}`, email: `rec${__VU}@loadtest.example.com`, waivedAccess: true }, token, { name: "request letter" });
  check(res, { "letter requested": (r) => r.status === 201 });
  const recId = res.json("id");
  const t2 = Date.now();
  const ok = waitFor(() => {
    const r = poll(`/api/applications/${app.id}/timeline`, token);
    return r.status === 200 && !!milestone(r.json(), `gen:rec:${recId}`);
  }, 15000);
  check(ok, { "letter synced": (v) => v >= 0 });
  syncObserved.add(Date.now() - t2, { change: "recommendation" });
}
