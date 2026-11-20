#!/usr/bin/env bash
# Logical backup of the whole database (both schemas) from the running stand.
# Writes backups/expense_platform-<UTC timestamp>.dump (pg_dump custom format)
# and a .sha256 next to it. backups/ is git-ignored; treat dumps as
# confidential (they contain claims and the audit trail).
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
CTX="${KUBE_CONTEXT:-docker-desktop}"
NS="${NS:-expense-platform-local}"
source "$root/scripts/lib.sh"
guard_target "$CTX" "$NS"

mkdir -p "$root/backups"; chmod 700 "$root/backups"
out="$root/backups/expense_platform-$(date -u +%Y%m%dT%H%M%SZ).dump"
# Runs inside the database pod over the local socket; no credentials leave the cluster.
k exec postgres-0 -- pg_dump -U postgres -d expense_platform --format=custom --compress=6 >"$out"
chmod 600 "$out"
shasum -a 256 "$out" | awk '{print $1}' >"$out.sha256"
echo "backup: $out ($(du -h "$out" | cut -f1), sha256 $(cut -c1-16 "$out.sha256")...)"
