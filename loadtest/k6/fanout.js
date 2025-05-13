// Fan-out benchmark: N students apply to one program, a counselor moves the
// deadline, and we measure how long until every timeline shows the new date.
import { check, fail } from "k6";
import { Trend } from "k6/metrics";
import http from "k6/http";
import { BASE, get, login, post, register, waitFor } from "./lib.js";

const replanned = new Trend("all_timelines_replanned_ms", true);
const N = Number(__ENV.STUDENTS || 300);

export const options = {
  scenarios: { fanout: { executor: "shared-iterations", vus: 1, iterations: 1, maxDuration: "10m" } },
  setupTimeout: "10m",
  thresholds: { checks: ["rate>0.99"] },
};

function iso(daysFromNow) {
  const d = new Date();
  d.setUTCHours(0, 0, 0, 0);
  d.setUTCDate(d.getUTCDate() + daysFromNow);
  return d.toISOString().slice(0, 10);
}

export function setup() {
  const counselor = login(__ENV.COUNSELOR_EMAIL || "counselor@example.com", __ENV.COUNSELOR_PASSWORD || "counselor-demo-password");
  const id = `fanout-${Date.now().toString(36)}`;
  const program = {
    id, university: "Fan-out University", name: "MS Load Testing", degree: "master", field: "Computer Science",
    country: "Canada", city: "Toronto", timezone: "America/Toronto", intake: "Fall 2027",
    deadlines: [{ round: "round_1", date: iso(60) }], tags: [],
    requirements: { minToefl: 0, minIelts: 0, greRequired: false, recommendationsRequired: 1, documents: ["statement_of_purpose", "cv"] },
  };
  let res = post("/api/programs", program, counselor, { name: "setup" });
  if (res.status !== 201) fail(`create program ${res.status} ${res.body}`);
  const created = res.json();
  const students = [];
  for (let i = 0; i < N; i++) {
    const token = register("fanout", i);
    res = post("/api/applications", { programId: id, round: "round_1" }, token, { name: "setup" });
    if (res.status !== 201) fail(`apply ${res.status}`);
    students.push({ token, app: res.json("id") });
  }
  for (const s of students) {
    waitFor(() => get(`/api/applications/${s.app}/timeline`, s.token, { name: "setup" }).status === 200, 15000);
  }
  return { counselor, program: created, students };
}

export default function (data) {
  const newDate = iso(75);
  const updated = Object.assign({}, data.program, { deadlines: [{ round: "round_1", date: newDate }] });
  const start = Date.now();
  const res = http.put(`${BASE}/api/programs/${data.program.id}`, JSON.stringify(updated), {
    headers: { "Content-Type": "application/json", Origin: BASE, Cookie: `wp_session=${data.counselor}` },
  });
  check(res, { "program updated": (r) => r.status === 200 });
  let pending = data.students.slice();
  while (pending.length > 0 && Date.now() - start < 120000) {
    const reqs = pending.map((s) => ["GET", `${BASE}/api/applications/${s.app}/timeline`, null, { headers: { Cookie: `wp_session=${s.token}` }, tags: { name: "poll timeline" } }]);
    const out = http.batch(reqs);
    pending = pending.filter((_, i) => !(out[i].status === 200 && out[i].json("deadline") === newDate));
  }
  const elapsed = Date.now() - start;
  check(pending, { "every timeline re-planned": (p) => p.length === 0 });
  replanned.add(elapsed);
  console.log(`re-planned ${N - pending.length}/${N} timelines in ${elapsed} ms`);
}
