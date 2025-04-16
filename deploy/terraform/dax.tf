# DAX cluster in front of the three tables. Services run with CACHE_MODE=dax:
# the aws-dax-go-v2 client implements the same interface as the DynamoDB
# client (see internal/ddb), so the cache sits below the repositories.
# Reads that must be fresh use ConsistentRead, which DAX passes through.

resource "aws_dax_subnet_group" "this" {
  name       = local.name
  subnet_ids = aws_subnet.private[*].id
}

resource "aws_dax_parameter_group" "this" {
  name = local.name
  parameters {
    name  = "record-ttl-millis"
    value = tostring(var.dax_item_ttl_ms)
  }
  parameters {
    name  = "query-ttl-millis"
    value = tostring(var.dax_query_ttl_ms)
  }
}

resource "aws_security_group" "dax" {
  name        = "${local.name}-dax"
  description = "DAX: accepts TLS connections from ECS tasks"
  vpc_id      = aws_vpc.this.id
  tags        = { Name = "${local.name}-dax" }
}

resource "aws_vpc_security_group_ingress_rule" "dax_from_tasks" {
  security_group_id            = aws_security_group.dax.id
  referenced_security_group_id = aws_security_group.tasks.id
  ip_protocol                  = "tcp"
  from_port                    = 9111
  to_port                      = 9111
  description                  = "DAX TLS endpoint"
}

data "aws_iam_policy_document" "dax_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["dax.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "dax" {
  name               = "${local.name}-dax"
  assume_role_policy = data.aws_iam_policy_document.dax_assume.json
}

data "aws_iam_policy_document" "dax_tables" {
  statement {
    actions = [
      "dynamodb:GetItem", "dynamodb:BatchGetItem", "dynamodb:Query", "dynamodb:Scan",
      "dynamodb:PutItem", "dynamodb:UpdateItem", "dynamodb:DeleteItem", "dynamodb:BatchWriteItem",
      "dynamodb:ConditionCheckItem", "dynamodb:DescribeTable",
    ]
    resources = flatten([for t in aws_dynamodb_table.service : [t.arn, "${t.arn}/index/*"]])
  }
}

resource "aws_iam_role_policy" "dax_tables" {
  name   = "tables"
  role   = aws_iam_role.dax.id
  policy = data.aws_iam_policy_document.dax_tables.json
}

resource "aws_dax_cluster" "this" {
  cluster_name                     = local.name
  iam_role_arn                     = aws_iam_role.dax.arn
  node_type                        = var.dax_node_type
  replication_factor               = var.dax_nodes
  availability_zones               = slice(local.azs, 0, min(var.dax_nodes, var.az_count))
  subnet_group_name                = aws_dax_subnet_group.this.name
  parameter_group_name             = aws_dax_parameter_group.this.name
  security_group_ids               = [aws_security_group.dax.id]
  cluster_endpoint_encryption_type = "TLS"
  maintenance_window               = "sun:05:00-sun:06:00"

  server_side_encryption {
    enabled = true
  }
}

locals {
  dax_endpoint = "daxs://${aws_dax_cluster.this.cluster_address}:${aws_dax_cluster.this.port}"
}
