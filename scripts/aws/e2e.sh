#!/usr/bin/env bash
# Runs the end-to-end suite against the AWS stand with Cognito tokens.
set -euo pipefail
source "$(dirname "$0")/common.sh"
start_tunnel
host="$(tf_out hostname)"
export E2E_EXPENSES_URL="https://$host" E2E_AUDIT_URL="https://$host"
for u in alice bob rita victor; do export "E2E_TOKEN_$(echo "$u" | tr a-z A-Z)=$("$root/scripts/aws/token.sh" "$u")"; done
export E2E_DISRUPTIVE=1 E2E_KUBE_CONTEXT="$CTX" E2E_NAMESPACE="$NS"
cd "$root/tests" && go test -tags e2e -count=1 -v ./e2e/
