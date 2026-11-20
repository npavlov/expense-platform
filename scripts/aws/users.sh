#!/usr/bin/env bash
# Creates the four fictional demo users in Cognito with random passwords and
# their groups. Passwords are stored only in .local/aws/users.env (mode 600).
set -euo pipefail
source "$(dirname "$0")/common.sh"
pool="$(tf_out cognito_user_pool_id)"; region="$(tf_out cognito_issuer | cut -d. -f2)"
umask 077
: >"$STATE/users.env"
for spec in alice:employees bob:employees rita:employees,reviewers victor:reviewers; do
  user="${spec%%:*}"; groups="${spec#*:}"
  pw="$(openssl rand -base64 24 | tr -d '/+=')Aa1!"
  aws cognito-idp admin-create-user --region "$region" --user-pool-id "$pool" --username "$user" \
    --message-action SUPPRESS >/dev/null 2>&1 || true
  aws cognito-idp admin-set-user-password --region "$region" --user-pool-id "$pool" --username "$user" \
    --password "$pw" --permanent
  for g in ${groups//,/ }; do
    aws cognito-idp admin-add-user-to-group --region "$region" --user-pool-id "$pool" --username "$user" --group-name "$g"
  done
  echo "PASSWORD_$(echo "$user" | tr a-z A-Z)=$pw" >>"$STATE/users.env"
  echo "user $user in $groups"
done
echo "passwords: $STATE/users.env"
