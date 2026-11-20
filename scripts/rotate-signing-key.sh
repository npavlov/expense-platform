#!/usr/bin/env bash
# Rotates the outbox event signing key without losing or rejecting events:
#   1. audit accepts old and new key
#   2. expenses signs with the new key
#   3. after the outbox has drained, audit drops the old key
set -euo pipefail
CTX="${KUBE_CONTEXT:-docker-desktop}"
NS="${NS:-expense-platform-local}"
source "$(dirname "$0")/lib.sh"
guard_target "$CTX" "$NS"

tmp="$(mktemp -d)"; chmod 700 "$tmp"; trap 'rm -rf "$tmp"' EXIT
get() { k get secret "$1" -o "jsonpath={.data.$2}" | base64 -d; }
put() { # name key=file...
  local name="$1"; shift; local args=()
  for kv in "$@"; do args+=(--from-file="$kv"); done
  kubectl create secret generic "$name" -n "$NS" "${args[@]}" --dry-run=client -o yaml \
    | k label --local -f - app.kubernetes.io/part-of=expense-platform -o yaml | k apply -f - >/dev/null
}

old_id="$(get outbox-signing-key id 2>/dev/null || echo k1)"; get outbox-signing-key key > "$tmp/old"
new_id="k$(( ${old_id#k} + 1 ))"; openssl rand -hex 24 > "$tmp/new"; printf %s "$new_id" > "$tmp/id"
echo "rotating $old_id -> $new_id"

step "1. audit trusts $old_id and $new_id"
printf '%s:%s,%s:%s' "$old_id" "$(cat "$tmp/old")" "$new_id" "$(cat "$tmp/new")" > "$tmp/keys"
put audit-ingest-keys "keys=$tmp/keys"
k rollout restart deployment/audit >/dev/null && k rollout status deployment/audit --timeout=120s

step "2. expenses signs with $new_id"
put outbox-signing-key "id=$tmp/id" "key=$tmp/new"
k rollout restart deployment/expenses >/dev/null && k rollout status deployment/expenses --timeout=120s

step "3. wait for the outbox to drain, then retire $old_id"
for _ in $(seq 1 60); do
  pending="$("$(dirname "$0")/outbox.sh" status | jq '.pending + .parked')"
  [[ "$pending" == 0 ]] && break; sleep 2
done
[[ "$pending" == 0 ]] || { echo "outbox not drained ($pending); keeping both keys" >&2; exit 1; }
printf '%s:%s' "$new_id" "$(cat "$tmp/new")" > "$tmp/keys"
put audit-ingest-keys "keys=$tmp/keys"
k rollout restart deployment/audit >/dev/null && k rollout status deployment/audit --timeout=120s
echo "rotation complete: active key $new_id"
