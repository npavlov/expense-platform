#!/usr/bin/env bash
# Bring up the AWS stand from the last published release.
set -euo pipefail
umask 077
source "$(dirname "$0")/common.sh"
export KUBE_CONTEXT="$CTX"

for cmd in aws session-manager-plugin docker kubectl jq curl openssl nc; do
  command -v "$cmd" >/dev/null || { echo "Missing command: $cmd" >&2; exit 1; }
done

# The initial signed release from workflow run 45273242500.
export EXPENSES_IMAGE="${EXPENSES_IMAGE:-ghcr.io/npavlov/expense-platform-expenses@sha256:6af714878784691d7705bf8f24bc2a6eb12bc2b45f346e6298603d1c34b382f9}"
export AUDIT_IMAGE="${AUDIT_IMAGE:-ghcr.io/npavlov/expense-platform-audit@sha256:9491baa5f523f44d90a41052427335abedeac73c325620ef99d31cebdc7856e8}"

tf_dir="$root/infra/terraform/aws"
[[ -f "$tf_dir/terraform.tfvars" ]] || { echo "Configure infra/terraform/aws/terraform.tfvars first" >&2; exit 1; }
# Anonymous inspection matches the EC2 registry access model.
scratch=$(mktemp -d)
plan=
trap 'rm -rf "$scratch"; if [[ -n "$plan" ]]; then rm -f "$tf_dir/$plan"; fi' EXIT
for image in "$EXPENSES_IMAGE" "$AUDIT_IMAGE"; do
  [[ "$image" =~ @sha256:[0-9a-f]{64}$ ]] || { echo "Expected an image digest: $image" >&2; exit 1; }
  echo "Checking $image"
  docker --config "$scratch" manifest inspect "$image" |
    jq -e 'any(.manifests[]; .platform.os == "linux" and .platform.architecture == "arm64")' >/dev/null
done
tf() {
  aws configure export-credentials --format env-no-export |
    docker run --rm --env-file /dev/stdin -v "$tf_dir:/tf" -w /tf "$TF_IMAGE" "$@"
}
tf init -input=false
plan=".terraform/aws-up-$$.tfplan"
result=0
tf plan -input=false -detailed-exitcode -out="$plan" || result=$?
case "$result" in
  0) echo "AWS infrastructure is up to date." ;;
  2)
    printf 'Apply the AWS changes shown above? Type yes: '
    read -r answer && [[ "$answer" == yes ]] || { echo "Cancelled."; exit 1; }
    tf apply -input=false "$plan" ;;
  *) exit "$result" ;;
esac

id="$(tf_out instance_id)"
issuer="$(tf_out cognito_issuer)"
region="$(cut -d. -f2 <<< "$issuer")"
echo "Waiting for $id in $region to register with Session Manager"
for attempt in {1..60}; do
  status=$(aws ssm describe-instance-information --region "$region" --filters "Key=InstanceIds,Values=$id" \
    --query 'InstanceInformationList[0].PingStatus' --output text)
  [[ "$status" == Online ]] && break
  sleep 5
done
[[ "$status" == Online ]] || { echo "SSM did not come online" >&2; exit 1; }

echo "Waiting for k3s to issue its kubeconfig"
for attempt in {1..60}; do
  if "$root/scripts/aws/kubeconfig.sh"; then
    "$root/scripts/aws/deploy.sh"
    exit 0
  fi
  sleep 5
done
echo "k3s did not provide a kubeconfig; inspect cloud-init on $id" >&2
exit 1
