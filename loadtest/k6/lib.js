import http from "k6/http";
import { check, fail, sleep } from "k6";

export const BASE = __ENV.BASE_URL || "http://localhost:8080";
const JSON_HEADERS = { "Content-Type": "application/json", Origin: BASE };

export function post(path, body, token, tags) {
  const headers = Object.assign({}, JSON_HEADERS);
  if (token) headers.Cookie = `wp_session=${token}`;
  return http.post(`${BASE}${path}`, JSON.stringify(body), { headers, tags });
}

export function patch(path, body, token, tags) {
  return http.patch(`${BASE}${path}`, JSON.stringify(body), { headers: Object.assign({ Cookie: `wp_session=${token}` }, JSON_HEADERS), tags });
}

export function get(path, token, tags) {
  return http.get(`${BASE}${path}`, { headers: token ? { Cookie: `wp_session=${token}` } : {}, tags });
}

// A brand-new application's timeline is created asynchronously, so 404
// while polling for it is expected, not a failure.
const pollStatuses = http.expectedStatuses(200, 404);
export function poll(path, token) {
  return http.get(`${BASE}${path}`, { headers: { Cookie: `wp_session=${token}` }, tags: { name: "poll timeline" }, responseCallback: pollStatuses });
}

export function register(prefix, i) {
  const email = `${prefix}-${Date.now().toString(36)}-${i}@loadtest.example.com`;
  const res = post("/api/auth/register", { name: `Load ${i}`, email, password: "correct horse battery" }, null, { name: "register" });
  if (!check(res, { "registered": (r) => r.status === 200 })) fail(`register ${res.status} ${res.body}`);
  return res.cookies.wp_session[0].value;
}

export function login(email, password) {
  const res = post("/api/auth/login", { email, password }, null, { name: "login" });
  if (res.status !== 200) fail(`login ${res.status} ${res.body}`);
  return res.cookies.wp_session[0].value;
}

export function programIds(n) {
  const res = get(`/api/programs?pageSize=100`);
  const ids = res.json("programs").map((p) => p.id);
  if (ids.length < n) fail(`need ${n} programs, catalog has ${ids.length}`);
  return ids;
}

// waitFor polls fn every intervalMs (default 50 ms) until it returns true.
export function waitFor(fn, timeoutMs, intervalMs) {
  const start = Date.now();
  while (Date.now() - start < timeoutMs) {
    if (fn()) return Date.now() - start;
    sleep((intervalMs || 50) / 1000);
  }
  return -1;
}
