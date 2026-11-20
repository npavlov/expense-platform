#!/usr/bin/env bash
# Local access to the stand, bound to 127.0.0.1 only.
#
#   scripts/port-forward.sh start   # background, reconnects after pod restarts
#   scripts/port-forward.sh stop
#
#   http://127.0.0.1:30080  expenses API
#   http://127.0.0.1:30081  audit history API
#   http://127.0.0.1:30082  devidp (local tokens)
#   http://127.0.0.1:30090  Prometheus (observability profile only)
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
CTX="${KUBE_CONTEXT:-docker-desktop}"
NS=expense-platform-local
dir="$root/.local/port-forward"
forwards="expenses:30080:8080 audit:30081:8080 devidp:30082:8080 prometheus:30090:9090"

stop() {
  for f in "$dir"/*.pid; do
    [[ -e "$f" ]] || continue
    pid="$(cat "$f")"
    # Kill the supervisor loop and its kubectl child.
    pkill -P "$pid" 2>/dev/null || true
    kill "$pid" 2>/dev/null || true
    rm -f "$f"
  done
}

start() {
  stop
  mkdir -p "$dir"
  for f in $forwards; do
    IFS=: read -r svc local remote <<<"$f"
    kubectl --context "$CTX" -n "$NS" get svc "$svc" >/dev/null 2>&1 || continue
    nohup bash -c "while true; do
        kubectl --context '$CTX' -n '$NS' port-forward --address 127.0.0.1 svc/$svc $local:$remote
        sleep 1
      done" >"$dir/$svc.log" 2>&1 &
    echo $! >"$dir/$svc.pid"
    echo "  http://127.0.0.1:$local -> $svc:$remote"
  done
  # Wait until the forwards accept connections.
  for _ in $(seq 1 20); do
    curl -s -o /dev/null --max-time 1 http://127.0.0.1:30082/health && return 0
    sleep 0.5
  done
  echo "port-forward did not come up; see $dir/*.log" >&2
  return 1
}

case "${1:-}" in
  start) start ;;
  stop) stop; echo "port-forwards stopped" ;;
  *) echo "usage: $0 start|stop" >&2; exit 2 ;;
esac
