data "aws_iam_policy_document" "ecs_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
  }
}

# Execution role: pull images, write logs, read the secrets injected at start.
resource "aws_iam_role" "execution" {
  name               = "${local.name}-ecs-execution"
  assume_role_policy = data.aws_iam_policy_document.ecs_assume.json
}

resource "aws_iam_role_policy_attachment" "execution" {
  role       = aws_iam_role.execution.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

data "aws_iam_policy_document" "execution_secrets" {
  statement {
    actions = ["secretsmanager:GetSecretValue"]
    resources = [
      aws_secretsmanager_secret.internal_token.arn,
      aws_secretsmanager_secret.counselor_password.arn,
      aws_secretsmanager_secret.smtp.arn,
    ]
  }
}

resource "aws_iam_role_policy" "execution_secrets" {
  name   = "secrets"
  role   = aws_iam_role.execution.id
  policy = data.aws_iam_policy_document.execution_secrets.json
}

# Task roles: each backend service may only touch its own table (data
# ownership is enforced by IAM, not just convention) and the DAX cluster.
locals {
  table_owner = {
    catalog      = "catalog"
    applications = "applications"
    timeline     = "timeline"
  }
}

resource "aws_iam_role" "task" {
  for_each           = var.services
  name               = "${local.name}-${each.key}"
  assume_role_policy = data.aws_iam_policy_document.ecs_assume.json
}

data "aws_iam_policy_document" "task" {
  for_each = local.table_owner
  statement {
    sid = "OwnTable"
    actions = [
      "dynamodb:GetItem", "dynamodb:BatchGetItem", "dynamodb:Query",
      "dynamodb:PutItem", "dynamodb:UpdateItem", "dynamodb:DeleteItem", "dynamodb:BatchWriteItem",
      "dynamodb:ConditionCheckItem", "dynamodb:DescribeTable",
    ]
    resources = [
      aws_dynamodb_table.service[each.value].arn,
      "${aws_dynamodb_table.service[each.value].arn}/index/*",
    ]
  }
  statement {
    sid = "DAX"
    actions = [
      "dax:GetItem", "dax:BatchGetItem", "dax:Query", "dax:PutItem", "dax:UpdateItem",
      "dax:DeleteItem", "dax:BatchWriteItem", "dax:ConditionCheckItem",
    ]
    resources = [aws_dax_cluster.this.arn]
  }
}

resource "aws_iam_role_policy" "task" {
  for_each = local.table_owner
  name     = "data"
  role     = aws_iam_role.task[each.key].id
  policy   = data.aws_iam_policy_document.task[each.key].json
}
