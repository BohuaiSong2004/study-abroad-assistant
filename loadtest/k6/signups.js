// Sequential sign-ups through the gateway (docs/performance.md, section 5):
// one client registering accounts back to back for 20 s. Each sign-up hashes
// a password with argon2id and counts against the shared per-address limit,
// so run the stack with REGISTER_LIMIT_PER_IP and AUTH_RATE_PER_MIN raised
// (docker compose sets the former; set AUTH_RATE_PER_MIN=100000 for the gateway).
import { check } from "k6";
import { post } from "./lib.js";

export const options = { vus: 1, duration: __ENV.DURATION || "20s" };

export default function () {
  const email = `signup-${__VU}-${__ITER}-${Date.now().toString(36)}@loadtest.example.com`;
  const res = post("/api/auth/register", { name: "Load", email, password: "correct horse battery" }, null, { name: "register" });
  check(res, { registered: (r) => r.status === 200 });
}
