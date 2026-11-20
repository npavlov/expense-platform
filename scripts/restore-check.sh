#!/usr/bin/env bash
# Restore drill: restores a dump into a separate, clean PostgreSQL (a throwaway
# container, not the stand), reads one decided claim together with its audit
# history, compares it with the live stand, and reports the restore time.
# The live database is only read.
#
#   scripts/restore-check.sh [backups/<file>.dump]   (default: newest dump)
#   RTO_TARGET_SECONDS=1800                          target to compare against
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
CTX="${KUBE_CONTEXT:-docker-desktop}"
NS="${NS:-expense-platform-local}"
source "$root/scripts/lib.sh"
guard_target "$CTX" "$NS"
IMAGE='postgres:17.12-alpine@sha256:526f86d93c6ea08fd3f1fe5605650cacc781b2c6013169a0ad8f42c7d3d84f32'

dump="${1:-$(ls -t "$root"/backups/*.dump 2>/dev/null | head -1)}"
[[ -f "$dump" ]] || { echo "no dump found; run make backup first" >&2; exit 1; }
if [[ -f "$dump.sha256" ]] && [[ "$(shasum -a 256 "$dump" | awk '{print $1}')" != "$(cat "$dump.sha256")" ]]; then
  echo "checksum mismatch for $dump" >&2; exit 1
fi

live() { k exec postgres-0 -- psql -U postgres -d expense_platform -At -c "$1"; }
counts="SELECT (SELECT count(*) FROM expenses.claims) || '/' || (SELECT count(*) FROM audit.decisions)"
live_before="$(live "$counts")"

container="expense-platform-restore-$$"
trap 'docker rm -f "$container" >/dev/null 2>&1 || true' EXIT
step "Restore $(basename "$dump") into a clean PostgreSQL"
start=$(date +%s)
docker run -d --name "$container" -e POSTGRES_PASSWORD="$(openssl rand -hex 16)" -e POSTGRES_DB=restore_check "$IMAGE" >/dev/null
until docker exec "$container" psql -h 127.0.0.1 -U postgres -d restore_check -c 'SELECT 1' >/dev/null 2>&1; do sleep 1; done
# Ownership and grants refer to roles that do not exist in the drill database.
docker exec -i "$container" pg_restore -U postgres -d restore_check --no-owner --no-acl --exit-on-error <"$dump"
elapsed=$(( $(date +%s) - start ))

restored() { docker exec "$container" psql -U postgres -d restore_check -At -F ' | ' -c "$1"; }
claim="${CLAIM_ID:-$(restored "SELECT c.id FROM expenses.claims c JOIN audit.decisions d ON d.claim_id = c.id ORDER BY c.decided_at DESC LIMIT 1")}"
[[ -n "$claim" ]] || { echo "the dump has no decided claim with history; run make demo first" >&2; exit 1; }

q_claim="SELECT id, amount_cents, status, decided_by, decided_at FROM expenses.claims WHERE id = '$claim'"
q_hist="SELECT event_id, decision, decided_by, amount_cents FROM audit.decisions WHERE claim_id = '$claim' ORDER BY decided_at"
step "Claim $claim in the restored database"
echo "claim:   $(restored "$q_claim")"
restored "$q_hist" | sed 's/^/history: /'

step "Compare with the live stand"
fail=0
[[ "$(restored "$q_claim")" == "$(live "$q_claim" | tr '|' '\n' | paste -sd'|' - | sed 's/|/ | /g')" ]] && echo "  claim matches live" || { echo "  claim DIFFERS from live"; fail=1; }
[[ "$(restored "$q_hist")" == "$(live "$q_hist" | sed 's/|/ | /g')" ]] && echo "  history matches live" || { echo "  history DIFFERS from live"; fail=1; }
live_after="$(live "$counts")"
[[ "$live_before" == "$live_after" ]] && echo "  live data untouched (claims/decisions: $live_after)" || { echo "  live counts changed during the drill: $live_before -> $live_after (new traffic?)"; }
echo "  restored claims/decisions: $(restored "$counts")"

target="${RTO_TARGET_SECONDS:-1800}"
step "Result"
echo "restore took ${elapsed}s (target RTO ${target}s, includes starting PostgreSQL)"
[[ $elapsed -le $target ]] || { echo "restore slower than the RTO target"; fail=1; }
exit $fail
