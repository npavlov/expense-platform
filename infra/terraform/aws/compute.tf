# Ubuntu 24.04 LTS arm64, resolved at plan time from Canonical's public SSM
# parameter. Pin ami_id in a tfvars file if you need a reproducible image.
data "aws_ssm_parameter" "ubuntu" {
  name = "/aws/service/canonical/ubuntu/server/24.04/stable/current/arm64/hvm/ebs-gp3/ami-id"
}

resource "aws_eip" "node" {
  domain = "vpc"
  tags   = { Name = var.name }
}

locals {
  hostname = var.domain_name != "" ? var.domain_name : "${replace(aws_eip.node.public_ip, ".", "-")}.sslip.io"
}

# Separate encrypted volume for everything stateful (k3s datastore, PostgreSQL,
# ACME account and certificates). Replacing the instance re-attaches it, and
# k3s comes back with the same cluster state (see docs/aws.md).
resource "aws_ebs_volume" "data" {
  availability_zone = aws_subnet.public.availability_zone
  size              = var.data_volume_gb
  type              = "gp3"
  encrypted         = true
  tags              = { Name = "${var.name}-data", Backup = var.name }
}

resource "aws_instance" "node" {
  ami                    = data.aws_ssm_parameter.ubuntu.value
  instance_type          = var.instance_type
  subnet_id              = aws_subnet.public.id
  vpc_security_group_ids = [aws_security_group.node.id]
  iam_instance_profile   = aws_iam_instance_profile.node.name
  monitoring             = false # detailed monitoring costs extra
  ebs_optimized          = true

  metadata_options {
    http_endpoint               = "enabled"
    http_tokens                 = "required" # IMDSv2 only
    http_put_response_hop_limit = 1          # containers are one hop further: no IMDS for pods
    instance_metadata_tags      = "disabled"
  }

  root_block_device {
    volume_type           = "gp3"
    volume_size           = var.root_volume_gb
    encrypted             = true
    delete_on_termination = true
  }

  user_data = templatefile("${path.module}/templates/cloud-init.yaml.tftpl", {
    k3s_version        = var.k3s_version
    k3s_install_sha256 = var.k3s_install_sha256
    data_volume_id     = replace(aws_ebs_volume.data.id, "-", "")
    hostname           = local.hostname
    acme_email         = var.acme_email
    acme_server        = var.acme_staging ? "https://acme-staging-v02.api.letsencrypt.org/directory" : "https://acme-v02.api.letsencrypt.org/directory"
  })
  user_data_replace_on_change = true

  tags = { Name = var.name }

  lifecycle {
    ignore_changes = [ami] # a new AMI release must not replace a running stand
  }
}

resource "aws_volume_attachment" "data" {
  device_name = "/dev/sdf"
  volume_id   = aws_ebs_volume.data.id
  instance_id = aws_instance.node.id
}

resource "aws_eip_association" "node" {
  instance_id   = aws_instance.node.id
  allocation_id = aws_eip.node.id
}

resource "aws_dlm_lifecycle_policy" "data" {
  count              = var.snapshot_retention_count > 0 ? 1 : 0
  description        = "${var.name} daily data volume snapshots"
  execution_role_arn = aws_iam_role.dlm[0].arn
  state              = "ENABLED"

  policy_details {
    resource_types = ["VOLUME"]
    target_tags    = { Backup = var.name }

    schedule {
      name      = "daily"
      copy_tags = true
      create_rule {
        interval      = 24
        interval_unit = "HOURS"
        times         = ["03:00"]
      }
      retain_rule {
        count = var.snapshot_retention_count
      }
      tags_to_add = { SnapshotOf = "${var.name}-data" }
    }
  }
}
