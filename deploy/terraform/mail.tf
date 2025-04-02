# Transactional email (recommendation invitations, password resets) through
# Amazon SES's SMTP interface. The applications service refuses to log
# emails in production, so this is required, not optional.
#
# SES SMTP credentials are an IAM user's access key with a derived password
# (ses_smtp_password_v4). The key ends up in Terraform state, which must be
# stored encrypted (e.g. an S3 backend with SSE and restricted access).
# New SES accounts start in the sandbox: until production access is granted,
# mail is only delivered to verified addresses.

locals {
  smtp_addr = var.smtp_addr != "" ? var.smtp_addr : "email-smtp.${var.region}.amazonaws.com:587"
}

resource "aws_sesv2_email_identity" "sender" {
  count          = var.mail_domain != "" ? 1 : 0
  email_identity = var.mail_domain
}

resource "aws_iam_user" "smtp" {
  name = "${local.name}-smtp"
  path = "/system/"
}

data "aws_iam_policy_document" "smtp_send" {
  statement {
    actions   = ["ses:SendRawEmail"]
    resources = ["*"]
    condition {
      test     = "StringEquals"
      variable = "ses:FromAddress"
      values   = [var.mail_from_address]
    }
  }
}

resource "aws_iam_user_policy" "smtp_send" {
  name   = "send-from-${local.name}"
  user   = aws_iam_user.smtp.name
  policy = data.aws_iam_policy_document.smtp_send.json
}

resource "aws_iam_access_key" "smtp" {
  user = aws_iam_user.smtp.name
}

resource "aws_secretsmanager_secret" "smtp" {
  name                    = "${local.name}/smtp"
  recovery_window_in_days = 7
}

resource "aws_secretsmanager_secret_version" "smtp" {
  secret_id = aws_secretsmanager_secret.smtp.id
  secret_string = jsonencode({
    username = aws_iam_access_key.smtp.id
    password = aws_iam_access_key.smtp.ses_smtp_password_v4
  })
}
