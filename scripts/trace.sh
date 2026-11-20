#!/usr/bin/env bash
# Follows one request through both services by its request ID.
#   scripts/trace.sh <request-id>
set -euo pipefail
CTX="${KUBE_CONTEXT:-docker-desktop}"
NS="${NS:-expense-platform-local}"
source "$(dirname "$0")/lib.sh"
rid="${1:?request id}"
for app in expenses audit; do
  k logs -l "app.kubernetes.io/name=$app" --tail=-1 --prefix=false 2>/dev/null \
    | grep -F "\"$rid\"" | jq -c '{time, service, msg, route, status, claim_id, event_id, decision, attempt, request_id}|with_entries(select(.value != null))' || true
done | sort
