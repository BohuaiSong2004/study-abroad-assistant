output "url" {
  description = "Public URL of the site."
  value       = local.public_url
}

output "load_balancer_dns" {
  value = aws_lb.this.dns_name
}

output "dax_endpoint" {
  description = "Value of DAX_ENDPOINT used by the services."
  value       = local.dax_endpoint
}

output "tables" {
  value = { for k, t in aws_dynamodb_table.service : k => t.name }
}

output "ecr_repositories" {
  value = { for k, r in aws_ecr_repository.service : k => r.repository_url }
}

output "bootstrap_counselor_password_secret" {
  description = "Secrets Manager secret holding the bootstrap counselor's password."
  value       = aws_secretsmanager_secret.counselor_password.name
}
