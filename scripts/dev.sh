#!/usr/bin/env bash
# Runs the four services and the gateway locally against DynamoDB Local.
#
#   docker run --rm -p 8000:8000 amazon/dynamodb-local   # in another terminal
#   ./scripts/dev.sh                                     # then open http://localhost:8080
#
# Email is logged (MAIL_MODE=log) unless an SMTP relay is given, e.g. Mailpit:
#   docker run --rm -p 1025:1025 -p 8025:8025 axllent/mailpit:v1.29
#   MAIL_MODE=smtp SMTP_ADDR=localhost:1025 ./scripts/dev.sh   # inbox: http://localhost:8025
#
# Ctrl-C stops everything.
set -euo pipefail
cd "$(dirname "$0")/.."

export DYNAMODB_ENDPOINT="${DYNAMODB_ENDPOINT:-http://localhost:8000}"
export AWS_REGION="${AWS_REGION:-us-east-1}"
export CACHE_MODE="${CACHE_MODE:-local}"
export INTERNAL_TOKEN="${INTERNAL_TOKEN:-dev-internal-token-change-me}"
export LOG_FORMAT="${LOG_FORMAT:-text}"
export CATALOG_ADDR=dns:///localhost:9091 APPLICATIONS_ADDR=dns:///localhost:9092 TIMELINE_ADDR=dns:///localhost:9093
# The limiter stays on in development; the threshold is raised so the e2e
# suite (many sign-ups from one IP) runs without 429s.
export AUTH_RATE_PER_MIN="${AUTH_RATE_PER_MIN:-300}"
export LOGIN_LIMIT_PER_IP="${LOGIN_LIMIT_PER_IP:-2000}"
export REGISTER_LIMIT_PER_IP="${REGISTER_LIMIT_PER_IP:-2000}"
export MAIL_MODE="${MAIL_MODE:-log}" PUBLIC_URL="${PUBLIC_URL:-http://localhost:8080}"
export BOOTSTRAP_COUNSELOR_EMAIL="${BOOTSTRAP_COUNSELOR_EMAIL:-counselor@example.com}"
export BOOTSTRAP_COUNSELOR_PASSWORD="${BOOTSTRAP_COUNSELOR_PASSWORD:-counselor-demo-password}"

mkdir -p bin
go build -o bin/ ./cmd/...

pids=()
cleanup() { kill "${pids[@]}" 2>/dev/null || true; wait 2>/dev/null || true; }
trap cleanup EXIT INT TERM

run() { # name, then env assignments
  local name=$1; shift
  env "$@" "bin/$name" 2>&1 | sed -u "s/^/[$name] /" &
  pids+=($!)
}

run catalog      GRPC_ADDR=:9091 ADMIN_ADDR=:9101
run applications GRPC_ADDR=:9092 ADMIN_ADDR=:9102
run timeline     GRPC_ADDR=:9093 ADMIN_ADDR=:9103
run gateway      HTTP_ADDR=:8080 ADMIN_ADDR=:8081 WEB_DIR="${WEB_DIR:-web/static}"

for port in 9101 9102 9103 8081; do
  until curl -fsS "http://localhost:$port/readyz" >/dev/null 2>&1; do sleep 0.3; done
done
bin/seed
echo
echo "Waypoint is running: http://localhost:8080"
echo "Counselor login: $BOOTSTRAP_COUNSELOR_EMAIL / $BOOTSTRAP_COUNSELOR_PASSWORD"
wait
