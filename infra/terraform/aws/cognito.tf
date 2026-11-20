# Users sign in to Cognito; the services verify the access tokens it issues.
# Users and their group memberships are created by the operator
# (scripts/aws-users.sh), never in Terraform: passwords do not belong in state.

resource "aws_cognito_user_pool" "this" {
  name                = var.name
  user_pool_tier      = "LITE"
  deletion_protection = "INACTIVE" # a temporary stand must be destroyable

  admin_create_user_config {
    allow_admin_create_user_only = true # no self sign-up
  }

  username_configuration {
    case_sensitive = false
  }

  password_policy {
    minimum_length                   = 14
    require_lowercase                = true
    require_uppercase                = true
    require_numbers                  = true
    require_symbols                  = true
    temporary_password_validity_days = 1
  }

  mfa_configuration = "OPTIONAL"
  software_token_mfa_configuration {
    enabled = true
  }

  account_recovery_setting {
    recovery_mechanism {
      name     = "admin_only"
      priority = 1
    }
  }
}

resource "aws_cognito_user_group" "employees" {
  name         = "employees"
  user_pool_id = aws_cognito_user_pool.this.id
  description  = "May submit and view their own claims"
}

resource "aws_cognito_user_group" "reviewers" {
  name         = "reviewers"
  user_pool_id = aws_cognito_user_pool.this.id
  description  = "May decide other people's claims and read the decision history"
}

# Public client for the terminal demo (no client secret). SRP keeps the
# password off the wire; USER_PASSWORD_AUTH is allowed for the AWS CLI demo
# and goes to Cognito over TLS only.
resource "aws_cognito_user_pool_client" "cli" {
  name            = "${var.name}-cli"
  user_pool_id    = aws_cognito_user_pool.this.id
  generate_secret = false

  explicit_auth_flows = [
    "ALLOW_USER_SRP_AUTH",
    "ALLOW_USER_PASSWORD_AUTH",
    "ALLOW_REFRESH_TOKEN_AUTH",
  ]

  access_token_validity  = var.token_validity_minutes
  id_token_validity      = var.token_validity_minutes
  refresh_token_validity = 1
  token_validity_units {
    access_token  = "minutes"
    id_token      = "minutes"
    refresh_token = "days"
  }

  prevent_user_existence_errors = "ENABLED"
  enable_token_revocation       = true
}
