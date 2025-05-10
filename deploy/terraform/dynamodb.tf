# Mirrors internal/ddb/tables.go: every service owns one single-table-design
# table with pk/sk, one overloaded GSI and a TTL attribute.
locals {
  tables = toset(["catalog", "applications", "timeline"])
}

resource "aws_dynamodb_table" "service" {
  #checkov:skip=CKV_AWS_119:Encrypted with the AWS-managed key; a customer-managed KMS key is warranted for cross-account or key-policy separation requirements, not here
  for_each                    = local.tables
  name                        = "${local.name}-${each.key}"
  billing_mode                = "PAY_PER_REQUEST"
  hash_key                    = "pk"
  range_key                   = "sk"
  deletion_protection_enabled = true

  attribute {
    name = "pk"
    type = "S"
  }
  attribute {
    name = "sk"
    type = "S"
  }
  attribute {
    name = "gsi1pk"
    type = "S"
  }
  attribute {
    name = "gsi1sk"
    type = "S"
  }

  global_secondary_index {
    name            = "gsi1"
    projection_type = "ALL"
    key_schema {
      attribute_name = "gsi1pk"
      key_type       = "HASH"
    }
    key_schema {
      attribute_name = "gsi1sk"
      key_type       = "RANGE"
    }
  }

  ttl {
    attribute_name = "ttl"
    enabled        = true
  }

  point_in_time_recovery {
    enabled = true
  }

  server_side_encryption {
    enabled = true
  }
}
