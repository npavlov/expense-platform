# One public subnet, no NAT gateway: the only instance has a public IP and
# needs outbound access for packages, images, Let's Encrypt and Cognito keys.
# 10.80.0.0/16 does not overlap the k3s pod (10.42/16) and service (10.43/16) ranges.

data "aws_availability_zones" "available" {
  state = "available"
}

resource "aws_vpc" "this" {
  cidr_block           = "10.80.0.0/16"
  enable_dns_support   = true
  enable_dns_hostnames = true
  tags                 = { Name = var.name }
}

# Deny everything on the default security group so nothing can use it by accident.
resource "aws_default_security_group" "this" {
  vpc_id = aws_vpc.this.id
  tags   = { Name = "${var.name}-default-deny" }
}

resource "aws_internet_gateway" "this" {
  vpc_id = aws_vpc.this.id
  tags   = { Name = var.name }
}

resource "aws_subnet" "public" {
  vpc_id                  = aws_vpc.this.id
  cidr_block              = "10.80.1.0/24"
  availability_zone       = data.aws_availability_zones.available.names[0]
  map_public_ip_on_launch = false # the Elastic IP is attached explicitly
  tags                    = { Name = "${var.name}-public" }
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.this.id
  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.this.id
  }
  tags = { Name = "${var.name}-public" }
}

resource "aws_route_table_association" "public" {
  subnet_id      = aws_subnet.public.id
  route_table_id = aws_route_table.public.id
}

resource "aws_security_group" "node" {
  name        = "${var.name}-node"
  description = "k3s node: HTTP/HTTPS in, HTTP/HTTPS out. No SSH, no Kubernetes API."
  vpc_id      = aws_vpc.this.id
  tags        = { Name = "${var.name}-node" }
}

# 443: the public API (Traefik ingress, TLS).
resource "aws_vpc_security_group_ingress_rule" "https" {
  for_each          = toset(var.allowed_ingress_cidrs)
  security_group_id = aws_security_group.node.id
  description       = "Public API over TLS"
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
  cidr_ipv4         = each.value
}

# 80: Let's Encrypt HTTP-01 challenge; everything else is redirected to 443.
resource "aws_vpc_security_group_ingress_rule" "http" {
  for_each          = toset(var.allowed_ingress_cidrs)
  security_group_id = aws_security_group.node.id
  description       = "ACME HTTP-01 challenge and redirect to HTTPS"
  ip_protocol       = "tcp"
  from_port         = 80
  to_port           = 80
  cidr_ipv4         = each.value
}

# Outbound: HTTPS (SSM, images, Cognito JWKS, Let's Encrypt, k3s download) and
# HTTP (Ubuntu package mirrors). DNS and NTP use the VPC resolver and the
# Amazon Time Sync link-local address, which security groups do not filter.
resource "aws_vpc_security_group_egress_rule" "https" {
  security_group_id = aws_security_group.node.id
  description       = "HTTPS to the internet"
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
  cidr_ipv4         = "0.0.0.0/0"
}

resource "aws_vpc_security_group_egress_rule" "http" {
  security_group_id = aws_security_group.node.id
  description       = "HTTP to package mirrors"
  ip_protocol       = "tcp"
  from_port         = 80
  to_port           = 80
  cidr_ipv4         = "0.0.0.0/0"
}
