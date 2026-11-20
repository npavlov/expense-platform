#!/usr/bin/env bash
# Fetches the k3s admin kubeconfig through an interactive Session Manager
# session (not Run Command, so it is not kept in the SSM command history) and
# stores it in .local/aws/kubeconfig (mode 600, git-ignored), pointed at the
# local end of the Session Manager tunnel. Treat it as a root credential.
set -euo pipefail
source "$(dirname "$0")/common.sh"
id="$(tf_out instance_id)"
region="$(tf_out cognito_issuer | cut -d. -f2)"
raw="$(aws ssm start-session --region "$region" --target "$id" --document-name AWS-StartInteractiveCommand \
  --parameters command="sudo cat /etc/rancher/k3s/k3s.yaml")"
umask 077
printf '%s\n' "$raw" | sed -n '/^apiVersion:/,$p' | tr -d '\r' \
  | sed -e 's#https://127.0.0.1:6443#https://127.0.0.1:16443#' -e "s/: default$/: $CTX/" -e "s/^current-context: .*/current-context: $CTX/" \
  >"$KUBECONFIG"
grep -q 'client-key-data' "$KUBECONFIG" || { echo "kubeconfig not received" >&2; rm -f "$KUBECONFIG"; exit 1; }
echo "kubeconfig written to $KUBECONFIG (context $CTX)"
