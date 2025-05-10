# Shared secret of the internal gRPC contract (see internal/platform/auth.go).
resource "random_password" "internal_token" {
  length  = 48
  special = false
}

resource "aws_secretsmanager_secret" "internal_token" {
  #checkov:skip=CKV_AWS_149:Encrypted with the AWS-managed key; a customer-managed KMS key is warranted for cross-account or key-policy separation requirements, not here
  #checkov:skip=CKV2_AWS_57:Rotating it needs a coordinated restart of every task; rotation is a documented manual runbook step, not a Lambda
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
  #checkov:skip=CKV_AWS_149:Encrypted with the AWS-managed key; a customer-managed KMS key is warranted for cross-account or key-policy separation requirements, not here
  #checkov:skip=CKV2_AWS_57:Used once, to create the first counselor account; the counselor changes the password afterwards
  name                    = "${local.name}/bootstrap-counselor-password"
  recovery_window_in_days = 7
}

resource "aws_secretsmanager_secret_version" "counselor_password" {
  secret_id     = aws_secretsmanager_secret.counselor_password.id
  secret_string = random_password.counselor.result
}
