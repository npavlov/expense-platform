# Shared helpers for the AWS scripts. Source, do not execute.
# Prerequisites: aws CLI v2 with a short-lived session (aws sso login or
# temporary credentials, never long-lived access keys), the Session Manager
# plugin, Docker (for Terraform), kubectl, jq, curl.
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TF_IMAGE='hashicorp/terraform:1.14.0@sha256:3abcdb56739bf9c61a0290cfd1a2e41ef9c3799c0e6fa7f3c467f883367d3ecb'
STATE="$root/.local/aws"
CTX=expense-platform-aws
NS=expense-platform
export KUBECONFIG="$STATE/kubeconfig"
mkdir -p "$STATE"; chmod 700 "$STATE"

tf_out() { # output name
  docker run --rm -v "$root/infra/terraform/aws:/tf" -w /tf -e TF_DATA_DIR=/tf/.terraform "$TF_IMAGE" output -raw "$1"
}

# Starts a Session Manager port-forward to the Kubernetes API on
# 127.0.0.1:16443 and stops it when the calling script exits.
start_tunnel() {
  local id region
  id="$(tf_out instance_id)"; region="$(docker run --rm -v "$root/infra/terraform/aws:/tf" -w /tf -e TF_DATA_DIR=/tf/.terraform "$TF_IMAGE" output -json | jq -r '.cognito_issuer.value' | cut -d. -f2)"
  aws ssm start-session --region "$region" --target "$id" --document-name AWS-StartPortForwardingSession \
    --parameters portNumber=6443,localPortNumber=16443 >"$STATE/tunnel.log" 2>&1 &
  TUNNEL_PID=$!
  trap 'kill $TUNNEL_PID 2>/dev/null || true' EXIT
  for _ in $(seq 1 30); do nc -z 127.0.0.1 16443 2>/dev/null && return 0; sleep 1; done
  echo "tunnel did not come up; see $STATE/tunnel.log" >&2; return 1
}
