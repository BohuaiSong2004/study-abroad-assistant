resource "aws_ecr_repository" "service" {
  #checkov:skip=CKV_AWS_136:Encrypted with the AWS-managed key; a customer-managed KMS key is warranted for cross-account or key-policy separation requirements, not here
  for_each             = toset(["gateway", "catalog", "applications", "timeline", "seed"])
  name                 = "${local.name}/${each.key}"
  image_tag_mutability = "IMMUTABLE"
  force_delete         = false
  image_scanning_configuration {
    scan_on_push = true
  }
  encryption_configuration {
    encryption_type = "AES256"
  }
}

resource "aws_ecr_lifecycle_policy" "service" {
  for_each   = aws_ecr_repository.service
  repository = each.value.name
  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "Keep the 30 most recent images"
      selection    = { tagStatus = "any", countType = "imageCountMoreThan", countNumber = 30 }
      action       = { type = "expire" }
    }]
  })
}
