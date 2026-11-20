#!/usr/bin/env bash
# Prints a Cognito access token for a demo user:  scripts/aws/token.sh alice
set -euo pipefail
source "$(dirname "$0")/common.sh"
user="${1:?user}"
source "$STATE/users.env"
pwvar="PASSWORD_$(echo "$user" | tr a-z A-Z)"
aws cognito-idp initiate-auth --region "$(tf_out cognito_issuer | cut -d. -f2)" --client-id "$(tf_out cognito_client_id)" \
  --auth-flow USER_PASSWORD_AUTH --auth-parameters "USERNAME=$user,PASSWORD=${!pwvar}" \
  --query 'AuthenticationResult.AccessToken' --output text
