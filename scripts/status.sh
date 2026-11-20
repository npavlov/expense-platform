#!/usr/bin/env bash
# Shows the state of the local stand and probes its public endpoints.
set -uo pipefail
CTX="${KUBE_CONTEXT:-docker-desktop}"
NS="${NS:-expense-platform-local}"
source "$(dirname "$0")/lib.sh"

step "Workloads in $NS ($CTX)"
k get pods,jobs,svc,pvc -o wide 2>/dev/null || { echo "namespace $NS not found"; exit 1; }

step "Endpoints (via scripts/port-forward.sh; 000 = not forwarded or down)"
for e in "expenses http://127.0.0.1:30080/ready" "audit http://127.0.0.1:30081/ready" "devidp http://127.0.0.1:30082/health" "prometheus http://127.0.0.1:30090/-/ready"; do
  name="${e%% *}"; url="${e#* }"
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "$url")"
  printf '  %-10s %-36s %s\n' "$name" "$url" "$code"
done
