#!/usr/bin/env bash
# Five-minute demo against the local stand (make local-up first).
# All people and data are fictional.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
CTX="${KUBE_CONTEXT:-docker-desktop}"
NS="${NS:-expense-platform-local}"
source "$root/scripts/lib.sh"
guard_target "$CTX" "$NS"
API=http://127.0.0.1:30080
AUDIT=http://127.0.0.1:30081
IDP=http://127.0.0.1:30082

curl -s -o /dev/null --max-time 2 "$IDP/health" || "$root/scripts/port-forward.sh" start >/dev/null
say() { printf '\n\033[1;36m▶ %s\033[0m\n' "$*"; }
run() { printf '\033[2m$ %s\033[0m\n' "$*"; }
token() { curl -s -X POST -d "username=$1" "$IDP/token" | jq -r .access_token; }
ALICE=$(token alice); BOB=$(token bob); RITA=$(token rita)
submit() { # token amount description
  curl -s -X POST "$API/v1/claims" -H "Authorization: Bearer $1" -H 'Content-Type: application/json' \
    -H "Idempotency-Key: $(uuidgen)" -d "{\"amount_cents\":$2,\"description\":\"$3\"}"
}
decide() { # token id decision request-id
  curl -s -X POST "$API/v1/claims/$2/decision" -H "Authorization: Bearer $1" -H 'Content-Type: application/json' \
    -H "X-Request-ID: $4" -d "{\"decision\":\"$3\"}"
}
history() { curl -s --max-time 3 -H "Authorization: Bearer $RITA" "$AUDIT/v1/audit/claims/$1/decisions"; }
wait_history() { for _ in $(seq 1 60); do n=$(history "$1" | jq '.decisions | length' 2>/dev/null || echo 0); [[ "$n" -ge 1 ]] && return; sleep 1; done; }

say "1. Alice submits 'Taxi, 12 EUR'"
run "POST /v1/claims {amount_cents: 1200, description: Taxi}  (as alice)"
claim=$(submit "$ALICE" 1200 Taxi); echo "$claim" | jq -c '{id, status, amount_cents, currency}'
ID=$(echo "$claim" | jq -r .id)

say "2. Bob has a valid token and knows the ID, but cannot read the claim"
run "GET /v1/claims/$ID  (as bob)"
curl -s -H "Authorization: Bearer $BOB" "$API/v1/claims/$ID" | jq -c '{status, code}'
run "GET /v1/claims  (as bob)"
curl -s -H "Authorization: Bearer $BOB" "$API/v1/claims" | jq -c "[.items[] | select(.id == \"$ID\")] | {alice_claims_visible_to_bob: length}"

say "3. Rita (reviewer) approves; the history shows exactly one decision"
RID1="demo-$(date +%s)-approve"
run "POST /v1/claims/$ID/decision {decision: approved}  (as rita, X-Request-ID: $RID1)"
decide "$RITA" "$ID" approved "$RID1" | jq -c '{status, decided_by}'
wait_history "$ID"
run "GET /v1/audit/claims/$ID/decisions  (as rita)"
history "$ID" | jq -c '{entries: (.decisions | length), decisions: [.decisions[] | {decision, decided_by, decided_at}]}'

say "4. Audit goes down. A new claim is still decided immediately"
run "kubectl scale deployment/audit --replicas=0"
k scale deployment/audit --replicas=0 >/dev/null
k wait --for=delete pod -l app.kubernetes.io/name=audit --timeout=60s >/dev/null
ID2=$(submit "$ALICE" 4550 "Train ticket" | jq -r .id)
RID2="demo-$(date +%s)-outage"
run "POST /v1/claims/$ID2/decision {decision: approved}  (as rita)"
decide "$RITA" "$ID2" approved "$RID2" | jq -c '{status, decided_by}'
sleep 4
run "scripts/outbox.sh status   # the event waits in the outbox"
"$root/scripts/outbox.sh" status | jq -c '{pending, oldest_age_seconds, items: [.items[] | {claim_id, attempts, last_error}]}'
run "GET /v1/audit/claims/$ID2/decisions"
history "$ID2" >/dev/null 2>&1 && echo "unexpected: audit answered" || echo "audit unavailable (as expected)"

say "5. Audit comes back; the history catches up with the decision"
run "kubectl scale deployment/audit --replicas=1"
k scale deployment/audit --replicas=1 >/dev/null
k rollout status deployment/audit --timeout=120s >/dev/null
# kubectl port-forward can keep pointing at the deleted pod; reconnect.
"$root/scripts/port-forward.sh" start >/dev/null
t0=$(date +%s); wait_history "$ID2"
history "$ID2" | jq -c '{entries: (.decisions | length), decision: .decisions[0].decision}'
echo "caught up $(( $(date +%s) - t0 ))s after audit became ready"
"$root/scripts/outbox.sh" status | jq -c '{pending, oldest_age_seconds}'

say "6. One decision through both services' logs (request ID $RID2)"
"$root/scripts/trace.sh" "$RID2"
echo
echo "Done. Explore further: make status, make e2e, make obs-demo (Prometheus + alert)."
