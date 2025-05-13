#!/usr/bin/env bash
# Reproduces the read-path numbers in docs/performance.md: runs the k6 read
# mix against the docker compose stack with CACHE_MODE=off and =local, both
# saturating (20 VUs, closed loop) and at a fixed 50 req/s (open loop).
#
#   docker compose up -d --build && docker compose run --rm seed -synthetic 1000
#   ./loadtest/compare-cache.sh
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p loadtest/results

wait_healthy() {
  for _ in $(seq 1 90); do
    [ "$(docker compose ps --format '{{.Health}}' | grep -c '^healthy$')" -ge 4 ] && return
    sleep 2
  done
  echo "stack not healthy" >&2; exit 1
}

for mode in off local; do
  CACHE_MODE=$mode docker compose up -d catalog applications timeline gateway >/dev/null
  wait_healthy
  sleep 3
  echo "== CACHE_MODE=$mode, 20 VUs closed loop"
  k6 run --quiet --summary-export "loadtest/results/reads-$mode.json" loadtest/k6/reads.js | grep -E '\{ name:|http_reqs\.\.'
  echo "== CACHE_MODE=$mode, 50 req/s open loop"
  RATE=50 k6 run --quiet --summary-export "loadtest/results/reads-$mode-50rps.json" loadtest/k6/reads.js | grep -E '\{ name:|http_reqs\.\.'
done
