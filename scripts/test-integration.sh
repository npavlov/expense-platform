#!/usr/bin/env bash
# Runs integration tests of a service against a throwaway PostgreSQL:
# start a container, apply migrations, run tests tagged "integration",
# and remove the container even if a step fails.
set -euo pipefail

service="${1:?usage: test-integration.sh <service>}"
image="${POSTGRES_IMAGE:-postgres:17-alpine}"
container="${service}-it-$$"
root="$(cd "$(dirname "$0")/.." && pwd)"

cleanup() { docker rm -f "$container" >/dev/null 2>&1 || true; }
trap cleanup EXIT

docker run -d --name "$container" \
  -e POSTGRES_USER=test -e POSTGRES_PASSWORD=test -e POSTGRES_DB=test \
  -p 127.0.0.1::5432 "$image" >/dev/null

# pg_isready inside the container can succeed during the init phase,
# so wait until the server answers a real query over TCP.
for _ in $(seq 1 30); do
  if docker exec "$container" psql -h 127.0.0.1 -U test -d test -c 'SELECT 1' >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

port="$(docker port "$container" 5432/tcp | head -n1 | awk -F: '{print $NF}')"
url="postgres://test:test@127.0.0.1:${port}/test?sslmode=disable"

cd "$root/services/$service"
DATABASE_URL="$url" go run ./cmd/migrate up
TEST_DATABASE_URL="$url" go test -tags integration -count=1 -race ./...
