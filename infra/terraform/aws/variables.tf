variable "region" {
  description = "AWS region. Prices in docs/cost.md are for eu-central-1."
  type        = string
  default     = "eu-central-1"
}

variable "name" {
  description = "Name prefix for every resource."
  type        = string
  default     = "expense-platform"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{2,30}$", var.name))
    error_message = "Use 3-31 lowercase letters, digits and dashes."
  }
}

variable "instance_type" {
  description = "Graviton (arm64) instance. t4g.small (2 GiB) fits k3s, PostgreSQL and both services; t4g.medium leaves headroom."
  type        = string
  default     = "t4g.small"

  validation {
    condition     = can(regex("^t4g\\.", var.instance_type))
    error_message = "The AMI and images are arm64; use a t4g instance."
  }
}

variable "k3s_version" {
  description = "Pinned k3s release."
  type        = string
  default     = "v1.36.6+k3s1"
}

variable "k3s_install_sha256" {
  description = "SHA-256 of install.sh at the pinned k3s tag; cloud-init refuses a different script."
  type        = string
  default     = "46177d4c99440b4c0311b67233823a8e8a2fc09693f6c89af1a7161e152fbfad"
}

variable "domain_name" {
  description = "Public hostname of the API. Empty: <elastic-ip>.sslip.io, which needs no DNS setup. With your own domain, create an A record to the Elastic IP before the first request."
  type        = string
  default     = ""
}

variable "acme_email" {
  description = "Contact e-mail for Let's Encrypt (expiry notices)."
  type        = string
}

variable "acme_staging" {
  description = "Use the Let's Encrypt staging CA (untrusted certificates, generous rate limits) while experimenting."
  type        = bool
  default     = false
}

variable "allowed_ingress_cidrs" {
  description = "Sources allowed to reach ports 80 and 443. Narrow to your own IP for a private demo."
  type        = list(string)
  default     = ["0.0.0.0/0"]
}

variable "root_volume_gb" {
  description = "Root volume (OS, container images)."
  type        = number
  default     = 20
}

variable "data_volume_gb" {
  description = "Data volume: k3s state, PostgreSQL, certificates. Survives instance replacement."
  type        = number
  default     = 10
}

variable "snapshot_retention_count" {
  description = "Daily snapshots of the data volume to keep (0 disables the snapshot policy)."
  type        = number
  default     = 3
}

variable "token_validity_minutes" {
  description = "Lifetime of Cognito access tokens."
  type        = number
  default     = 15

  validation {
    condition     = var.token_validity_minutes >= 5 && var.token_validity_minutes <= 60
    error_message = "Keep access tokens short-lived: 5 to 60 minutes."
  }
}
