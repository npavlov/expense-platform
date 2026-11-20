#!/usr/bin/env bash
# Shows the outbox backlog graph and the OutboxDeliveryStalled alert while
# audit is stopped. Requires the observability profile:
#   make local-up OBSERVABILITY=1
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
CTX="${KUBE_CONTEXT:-docker-desktop}"
NS="${NS:-expense-platform-local}"
source "$root/scripts/lib.sh"
guard_target "$CTX" "$NS"
PROM=http://127.0.0.1:30090
k get deployment prometheus >/dev/null 2>&1 || { echo "Prometheus not deployed: make local-up OBSERVABILITY=1" >&2; exit 1; }
curl -s -o /dev/null --max-time 2 "$PROM/-/ready" || "$root/scripts/port-forward.sh" start >/dev/null

alert_state() { curl -s "$PROM/api/v1/alerts" | jq -r '[.data.alerts[] | select(.labels.alertname=="OutboxDeliveryStalled") | .state] | first // "inactive"'; }
age() { curl -s "$PROM/api/v1/query" --data-urlencode 'query=max(outbox_oldest_undelivered_age_seconds)' | jq -r '.data.result[0].value[1] // "n/a"'; }
tok() { curl -s -X POST -d "username=$1" http://127.0.0.1:30082/token | jq -r .access_token; }

step "Graph (open in a browser)"
echo "$PROM/graph?g0.expr=max(outbox_oldest_undelivered_age_seconds)&g0.tab=0&g0.range_input=15m"
echo "$PROM/alerts"

step "Stop audit and decide a claim"
k scale deployment/audit --replicas=0 >/dev/null
k wait --for=delete pod -l app.kubernetes.io/name=audit --timeout=60s >/dev/null
trap 'k scale deployment/audit --replicas=1 >/dev/null' EXIT
A=$(tok alice); R=$(tok rita)
id=$(curl -s -X POST http://127.0.0.1:30080/v1/claims -H "Authorization: Bearer $A" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $(uuidgen)" -d '{"amount_cents":3200,"description":"Airport shuttle"}' | jq -r .id)
curl -s -X POST "http://127.0.0.1:30080/v1/claims/$id/decision" -H "Authorization: Bearer $R" \
  -H 'Content-Type: application/json' -d '{"decision":"approved"}' | jq -c '{status}'

step "Wait for OutboxDeliveryStalled (age > 30s for 15s)"
start=$(date +%s); state=inactive
while [[ "$state" != firing ]]; do
  sleep 5; state=$(alert_state)
  printf '  t+%3ss  oldest undelivered age=%ss  alert=%s\n' $(( $(date +%s) - start )) "$(age)" "$state"
  (( $(date +%s) - start > 180 )) && { echo "alert did not fire within 180s" >&2; exit 1; }
done

step "Start audit; the backlog drains and the alert resolves"
k scale deployment/audit --replicas=1 >/dev/null
k rollout status deployment/audit --timeout=120s >/dev/null
while [[ "$(alert_state)" != inactive ]]; do
  sleep 5; printf '  oldest undelivered age=%ss  alert=%s\n' "$(age)" "$(alert_state)"
done
echo "alert resolved; outbox drained"
