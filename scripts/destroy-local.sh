#!/usr/bin/env bash
# DELETES the local stand including the PostgreSQL volume and all secrets.
# Requires CONFIRM=delete-local-data. Images in Docker stay; see docs/operations.md.
set -euo pipefail
CTX=docker-desktop
NS=expense-platform-local
source "$(dirname "$0")/lib.sh"
guard_target "$CTX" "$NS"
if [[ "${CONFIRM:-}" != delete-local-data ]]; then
  echo "This deletes namespace $NS with its database volume and secrets."
  echo "Run: make local-destroy CONFIRM=delete-local-data"
  exit 1
fi
kubectl --context "$CTX" delete namespace "$NS" --wait=true
echo "deleted $NS"
