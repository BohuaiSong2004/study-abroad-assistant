variable "region" {
  description = "AWS region."
  type        = string
  default     = "us-east-1"
}

variable "environment" {
  description = "Deployment name, used in resource names (e.g. staging, prod)."
  type        = string
  default     = "staging"
  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,15}$", var.environment))
    error_message = "environment must be 2-16 lowercase letters, digits or dashes."
  }
}

variable "vpc_cidr" {
  description = "CIDR block of the VPC."
  type        = string
  default     = "10.40.0.0/16"
}

variable "az_count" {
  description = "Availability zones to spread subnets, tasks and DAX nodes across."
  type        = number
  default     = 3
  validation {
    condition     = var.az_count >= 2 && var.az_count <= 3
    error_message = "az_count must be 2 or 3."
  }
}

variable "image_tag" {
  description = "Image tag deployed for every service (pushed to the ECR repositories this stack creates)."
  type        = string
  default     = "latest"
}

variable "public_url" {
  description = "Public origin of the site (https://...). Defaults to the load balancer's DNS name over HTTP."
  type        = string
  default     = ""
}

variable "certificate_arn" {
  description = "ACM certificate for HTTPS on the load balancer. Without it the ALB serves HTTP only."
  type        = string
  default     = ""
}

variable "dax_node_type" {
  description = "DAX node type."
  type        = string
  default     = "dax.t3.small"
}

variable "dax_nodes" {
  description = "DAX nodes (1 primary + replicas). Use 3 across AZs for production."
  type        = number
  default     = 3
}

variable "dax_item_ttl_ms" {
  description = "DAX item cache TTL; mirrors CACHE_ITEM_TTL of the local cache (5 minutes)."
  type        = number
  default     = 300000
}

variable "dax_query_ttl_ms" {
  description = "DAX query cache TTL; mirrors CACHE_QUERY_TTL of the local cache (10 seconds)."
  type        = number
  default     = 10000
}

variable "services" {
  description = "Per-service Fargate sizing and replica counts."
  type = map(object({
    cpu     = number
    memory  = number
    desired = number
    max     = number
  }))
  default = {
    gateway      = { cpu = 256, memory = 512, desired = 2, max = 6 }
    catalog      = { cpu = 256, memory = 512, desired = 2, max = 4 }
    applications = { cpu = 512, memory = 1024, desired = 2, max = 6 } # argon2id hashing
    timeline     = { cpu = 256, memory = 512, desired = 2, max = 4 }
  }
}

variable "bootstrap_counselor_email" {
  description = "Creates this counselor account on first start (password in Secrets Manager). Empty to skip."
  type        = string
  default     = ""
}

variable "mail_from_address" {
  description = "Sender address for transactional email; must be (or belong to a domain) verified in SES."
  type        = string
  default     = "no-reply@example.com"
}

variable "mail_domain" {
  description = "Domain to verify as an SES identity (DKIM records must then be published). Empty skips it, e.g. when the identity is managed elsewhere."
  type        = string
  default     = ""
}

variable "smtp_addr" {
  description = "SMTP relay host:port. Defaults to the SES SMTP endpoint of var.region."
  type        = string
  default     = ""
}
