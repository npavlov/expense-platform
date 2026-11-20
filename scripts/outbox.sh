#!/usr/bin/env bash
# Operator access to the expenses outbox admin endpoint, which listens on
# 127.0.0.1:9091 inside the pod and is reachable only via port-forward.
#
#   scripts/outbox.sh status              undelivered events, oldest first
#   scripts/outbox.sh parked              events waiting for an operator
#   scripts/outbox.sh requeue <event_id>  make a parked event due again
set -euo pipefail
CTX="${KUBE_CONTEXT:-docker-desktop}"
NS="${NS:-expense-platform-local}"
source "$(dirname "$0")/lib.sh"
port=39091

pod="$(k get pod -l app.kubernetes.io/name=expenses -o jsonpath='{.items[0].metadata.name}')"
k port-forward --address 127.0.0.1 "pod/$pod" "$port:9091" >/dev/null 2>&1 &
pf=$!
trap 'kill $pf 2>/dev/null' EXIT
for _ in $(seq 1 20); do curl -s -o /dev/null "http://127.0.0.1:$port/admin/outbox" && break; sleep 0.25; done

case "${1:-status}" in
  status)  curl -s "http://127.0.0.1:$port/admin/outbox?state=undelivered" | jq . ;;
  parked)  curl -s "http://127.0.0.1:$port/admin/outbox?state=parked" | jq . ;;
  requeue) curl -s -X POST "http://127.0.0.1:$port/admin/outbox/${2:?event id}/requeue" | jq . ;;
  *) echo "usage: $0 status|parked|requeue <event_id>" >&2; exit 2 ;;
esac
