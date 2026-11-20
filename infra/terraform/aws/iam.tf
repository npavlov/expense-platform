# The instance role allows exactly one thing: registering with Systems
# Manager so an operator can open a Session Manager session or a port-forward
# to the Kubernetes API. No S3, no ECR (images are public on GHCR), no secrets.
# Pods cannot use it either: IMDSv2 with a hop limit of 1 keeps the metadata
# service out of reach from containers.

data "aws_iam_policy_document" "ec2_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "node" {
  name               = "${var.name}-node"
  assume_role_policy = data.aws_iam_policy_document.ec2_assume.json
}

# Minimal Session Manager permissions (a subset of AmazonSSMManagedInstanceCore,
# without Run Command, inventory, patching or S3 access).
data "aws_iam_policy_document" "session_manager" {
  statement {
    sid = "RegisterWithSSM"
    actions = [
      "ssm:UpdateInstanceInformation",
      "ssmmessages:CreateControlChannel",
      "ssmmessages:CreateDataChannel",
      "ssmmessages:OpenControlChannel",
      "ssmmessages:OpenDataChannel",
    ]
    resources = ["*"] # these actions do not support resource-level permissions
  }
}

resource "aws_iam_role_policy" "session_manager" {
  name   = "session-manager"
  role   = aws_iam_role.node.id
  policy = data.aws_iam_policy_document.session_manager.json
}

resource "aws_iam_instance_profile" "node" {
  name = "${var.name}-node"
  role = aws_iam_role.node.name
}

# Data Lifecycle Manager creates and expires the data volume snapshots.
data "aws_iam_policy_document" "dlm_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["dlm.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "dlm" {
  count              = var.snapshot_retention_count > 0 ? 1 : 0
  name               = "${var.name}-dlm"
  assume_role_policy = data.aws_iam_policy_document.dlm_assume.json
}

resource "aws_iam_role_policy_attachment" "dlm" {
  count      = var.snapshot_retention_count > 0 ? 1 : 0
  role       = aws_iam_role.dlm[0].name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSDataLifecycleManagerServiceRole"
}
