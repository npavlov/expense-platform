#!/usr/bin/env bash
# Integration tests against a throwaway PostgreSQL, set up like production:
#   1. start a clean container
#   2. create roles and schemas with deploy/k8s/base/db/bootstrap.sql (as superuser)
#   3. apply each service's migrations as that service's migrator role
#   4. run each module's tests tagged "integration" as the runtime role
# The container is removed afterwards, also on failure (KEEP_DB=1 keeps it).
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
image="${POSTGRES_IMAGE:-postgres:17.12-alpine@sha256:526f86d93c6ea08fd3f1fe5605650cacc781b2c6013169a0ad8f42c7d3d84f32}"
container="expense-platform-it-$$"
db=expense_platform
services=(expenses audit)

cleanup() {
  if [[ "${KEEP_DB:-}" == 1 ]]; then
    echo "keeping database container $container"
  else
    docker rm -f "$container" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

su_pw="$(openssl rand -hex 16)"
docker run -d --name "$container" \
  -e POSTGRES_PASSWORD="$su_pw" -e POSTGRES_DB="$db" \
  -p 127.0.0.1::5432 "$image" >/dev/null

# The image runs a temporary server during initialization that listens only on
# a Unix socket, so wait until the final server answers over TCP.
ready=0
for _ in $(seq 1 60); do
  if docker exec "$container" psql -h 127.0.0.1 -U postgres -d "$db" -c 'SELECT 1' >/dev/null 2>&1; then
    ready=1; break
  fi
  sleep 1
done
[[ $ready == 1 ]] || { echo "PostgreSQL did not become ready" >&2; docker logs "$container" >&2; exit 1; }

port="$(docker port "$container" 5432/tcp | head -n1 | awk -F: '{print $NF}')"
url() { echo "postgres://$1:$2@127.0.0.1:${port}/${db}?sslmode=disable"; }

# Random per-run passwords (bash 3.2 compatible: pw_<role> variables).
for role in expenses_migrator expenses_app audit_migrator audit_app; do
  printf -v "pw_$role" '%s' "$(openssl rand -hex 16)"
done
pw() { local v="pw_$1"; echo "${!v}"; }

docker exec -i "$container" psql -q -U postgres -d "$db" --single-transaction -v ON_ERROR_STOP=1 -v db="$db" \
  -v expenses_migrator_password="$(pw expenses_migrator)" -v expenses_app_password="$(pw expenses_app)" \
  -v audit_migrator_password="$(pw audit_migrator)" -v audit_app_password="$(pw audit_app)" \
  < "$root/deploy/k8s/base/db/bootstrap.sql"

for s in "${services[@]}"; do
  [[ -d "$root/services/$s/cmd/migrate" ]] || continue
  (cd "$root/services/$s" && DATABASE_URL="$(url "${s}_migrator" "$(pw "${s}_migrator")")" go run ./cmd/migrate up)
done

for s in "${services[@]}"; do
  [[ -d "$root/services/$s" ]] || continue
  echo "== integration tests: $s"
  (cd "$root/services/$s" && \
    TEST_DATABASE_URL="$(url "${s}_app" "$(pw "${s}_app")")" \
    TEST_SUPERUSER_URL="$(url postgres "$su_pw")" \
    go test -tags integration -count=1 -race ${GOTEST_FLAGS:-} ./...)
done
