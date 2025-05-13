// Read-path benchmark: run once with CACHE_MODE=off and once with
// CACHE_MODE=local and compare the per-endpoint latencies (tag "name").
import { check } from "k6";
import { get, programIds, register, post, waitFor } from "./lib.js";

export const options = {
  // Default: closed loop, 20 VUs with no think time (saturates the box).
  // RATE=50: open loop at a fixed request rate, far below capacity, which
  // measures latency without CPU contention.
  scenarios: __ENV.RATE
    ? { reads: { executor: "constant-arrival-rate", rate: Number(__ENV.RATE), timeUnit: "1s", duration: __ENV.DURATION || "30s", preAllocatedVUs: 20 } }
    : { reads: { executor: "constant-vus", vus: Number(__ENV.VUS || 20), duration: __ENV.DURATION || "30s" } },
  // Per-endpoint thresholds also make k6 report each endpoint separately.
  thresholds: {
    http_req_failed: ["rate<0.01"],
    checks: ["rate>0.99"],
    "http_req_duration{name:GET timeline}": ["p(95)<1000"],
    "http_req_duration{name:GET program}": ["p(95)<1000"],
    "http_req_duration{name:search programs}": ["p(95)<2000"],
    "http_req_duration{name:GET timelines}": ["p(95)<1000"],
    "http_req_duration{name:GET applications (consistent)}": ["p(95)<1000"],
  },
  summaryTrendStats: ["avg", "med", "p(90)", "p(95)", "p(99)", "count"],
  setupTimeout: "120s",
};

export function setup() {
  const programs = programIds(30);
  const users = [];
  for (let i = 0; i < 20; i++) {
    const token = register("reads", i);
    const apps = [];
    for (let k = 0; k < 3; k++) {
      const res = post("/api/applications", { programId: programs[(i * 3 + k) % programs.length] }, token, { name: "setup" });
      check(res, { "app created": (r) => r.status === 201 });
      apps.push(res.json("id"));
    }
    users.push({ token, apps });
  }
  // Timelines are created asynchronously; wait until all exist.
  for (const u of users) {
    for (const a of u.apps) {
      waitFor(() => get(`/api/applications/${a}/timeline`, u.token, { name: "setup" }).status === 200, 10000);
    }
  }
  return { users, programs, terms: ["computer", "data", "business", "canada", "robotics", "finance", "london", "health"] };
}

export default function (data) {
  const u = data.users[(__VU - 1) % data.users.length];
  const r = Math.random();
  let res;
  if (r < 0.35) {
    res = get(`/api/applications/${u.apps[Math.floor(Math.random() * u.apps.length)]}/timeline`, u.token, { name: "GET timeline" });
  } else if (r < 0.6) {
    // Popular programs are read far more often (catalog pages, recommender invites).
    const idx = Math.floor(Math.pow(Math.random(), 2) * data.programs.length);
    res = get(`/api/programs/${data.programs[idx]}`, null, { name: "GET program" });
  } else if (r < 0.75) {
    res = get(`/api/programs?q=${data.terms[Math.floor(Math.random() * data.terms.length)]}`, null, { name: "search programs" });
  } else if (r < 0.9) {
    res = get(`/api/timelines`, u.token, { name: "GET timelines" });
  } else {
    // Strongly consistent by design (read-your-writes): never cached.
    res = get(`/api/applications`, u.token, { name: "GET applications (consistent)" });
  }
  check(res, { "status 200": (x) => x.status === 200 });
}
