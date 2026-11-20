# Shared helpers for the deploy scripts. Source, do not execute.

# Every kubectl call passes --context and -n explicitly, so the current
# context of the shell does not matter. Refuse unknown contexts and any
# namespace other than the stand's own (never touch other workloads).
guard_target() {
  local ctx="${KUBE_CONTEXT:-$1}" ns="$2"
  if ! kubectl config get-contexts -o name | grep -qx "$ctx"; then
    echo "refusing: kube context '$ctx' does not exist" >&2
    exit 1
  fi
  case "$ns" in
    expense-platform-local|expense-platform) ;;
    *) echo "refusing: unexpected namespace '$ns'" >&2; exit 1 ;;
  esac
  echo "target: context=$ctx namespace=$ns"
}

k() { kubectl --context "${KUBE_CONTEXT:-$CTX}" -n "$NS" "$@"; }

step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
