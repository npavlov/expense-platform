output "api_url" {
  description = "Public API base URL."
  value       = "https://${local.hostname}"
}

output "hostname" {
  value = local.hostname
}

output "public_ip" {
  value = aws_eip.node.public_ip
}

output "instance_id" {
  value = aws_instance.node.id
}

output "data_volume_id" {
  value = aws_ebs_volume.data.id
}

output "cognito_user_pool_id" {
  value = aws_cognito_user_pool.this.id
}

output "cognito_client_id" {
  value = aws_cognito_user_pool_client.cli.id
}

output "cognito_issuer" {
  description = "Value for AUTH_ISSUER."
  value       = "https://cognito-idp.${var.region}.amazonaws.com/${aws_cognito_user_pool.this.id}"
}

output "kube_api_tunnel" {
  description = "Port-forward to the Kubernetes API through Session Manager (no open port)."
  value       = "aws ssm start-session --region ${var.region} --target ${aws_instance.node.id} --document-name AWS-StartPortForwardingSession --parameters portNumber=6443,localPortNumber=16443"
}
