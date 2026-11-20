#!/usr/bin/env bash
# Creates the stand's secrets in the namespace if they do not exist yet.
# Values are random, generated here, never written to the repository or to
# disk outside a private temp dir. Re-running keeps existing values, so a
# redeploy keeps working credentials and data.
#
#   scripts/secrets.sh <context> <namespace>
#
# Secret                 Used by                         Contains
# postgres-superuser     postgres, db-bootstrap          superuser password
# db-role-passwords      db-bootstrap                    passwords of the 4 roles
# expenses-migrator-db   expenses-migrate job            DATABASE_URL (DDL role)
# expenses-app-db        expenses                        DATABASE_URL (DML role)
# audit-migrator-db      audit-migrate job               DATABASE_URL (DDL role)
# audit-app-db           audit                           DATABASE_URL (DML role)
# outbox-signing-key     expenses                        HMAC key the worker signs with
# audit-ingest-keys      audit                           accepted HMAC keys (kid:key,...)
set -euo pipefail
CTX="$1"; NS="$2"
source "$(dirname "$0")/lib.sh"

tmp="$(mktemp -d)"; chmod 700 "$tmp"
trap 'rm -rf "$tmp"' EXIT

exists() { k get secret "$1" >/dev/null 2>&1; }
rand() { openssl rand -hex 24; }
get() { k get secret "$1" -o "jsonpath={.data.$2}" | base64 -d; }
# create_from_files <name> <key>=<file>...  (values never appear in argv)
apply_secret() {
  local name="$1"; shift
  local args=()
  for kv in "$@"; do args+=(--from-file="$kv"); done
  kubectl create secret generic "$name" -n "$NS" "${args[@]}" --dry-run=client -o yaml \
    | k label --local -f - app.kubernetes.io/part-of=expense-platform -o yaml \
    | k apply -f - >/dev/null
}

if ! exists postgres-superuser; then
  rand > "$tmp/password"
  apply_secret postgres-superuser "password=$tmp/password"
  echo "created postgres-superuser"
fi

roles="expenses_migrator expenses_app audit_migrator audit_app"
if ! exists db-role-passwords; then
  args=()
  for r in $roles; do rand > "$tmp/$r"; args+=("$r=$tmp/$r"); done
  apply_secret db-role-passwords "${args[@]}"
  echo "created db-role-passwords"
fi

# Connection URLs are derived from the role passwords on every run, so they
# always match what db-bootstrap set in PostgreSQL.
for r in $roles; do
  svc="${r%%_*}"; kind="${r#*_}"
  printf 'postgres://%s:%s@postgres:5432/expense_platform?sslmode=disable' "$r" "$(get db-role-passwords "$r")" > "$tmp/url"
  apply_secret "$svc-$kind-db" "url=$tmp/url"
done

if ! exists outbox-signing-key; then
  rand > "$tmp/key"
  apply_secret outbox-signing-key "key=$tmp/key"
  echo "created outbox-signing-key"
fi
# During a rotation, audit-ingest-keys holds old and new keys
# (docs/operations.md#rotate-the-event-signing-key); by default it trusts k1.
if ! exists audit-ingest-keys; then
  printf 'k1:%s' "$(get outbox-signing-key key)" > "$tmp/keys"
  apply_secret audit-ingest-keys "keys=$tmp/keys"
  echo "created audit-ingest-keys"
fi
echo "secrets ready in $NS"
