# Shared secret of the internal gRPC contract (see internal/platform/auth.go).
resource "random_password" "internal_token" {
  length  = 48
  special = false
}

resource "aws_secretsmanager_secret" "internal_token" {
  name                    = "${local.name}/internal-token"
  recovery_window_in_days = 7
}

resource "aws_secretsmanager_secret_version" "internal_token" {
  secret_id     = aws_secretsmanager_secret.internal_token.id
  secret_string = random_password.internal_token.result
}

resource "random_password" "counselor" {
  length  = 24
  special = false
}

resource "aws_secretsmanager_secret" "counselor_password" {
  name                    = "${local.name}/bootstrap-counselor-password"
  recovery_window_in_days = 7
}

resource "aws_secretsmanager_secret_version" "counselor_password" {
  secret_id     = aws_secretsmanager_secret.counselor_password.id
  secret_string = random_password.counselor.result
}
