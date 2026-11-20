terraform {
  required_version = ">= 1.9.0, < 2.0.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.60"
    }
  }

  # State stays local on purpose: this is a short-lived personal stand and
  # nothing else reads the state. *.tfstate is git-ignored. For a team, use an
  # S3 backend with encryption, versioning and a lock (see README.md).
}

provider "aws" {
  region = var.region

  default_tags {
    tags = {
      Project   = "expense-platform"
      ManagedBy = "terraform"
      Stand     = var.name
    }
  }
}
