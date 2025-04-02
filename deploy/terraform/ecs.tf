resource "aws_ecs_cluster" "this" {
  name = local.name
  setting {
    name  = "containerInsights"
    value = "enabled"
  }
}

resource "aws_security_group" "tasks" {
  name        = "${local.name}-tasks"
  description = "ECS tasks: gateway from the ALB, gRPC between services"
  vpc_id      = aws_vpc.this.id
  tags        = { Name = "${local.name}-tasks" }
}

resource "aws_vpc_security_group_ingress_rule" "tasks_from_alb" {
  security_group_id            = aws_security_group.tasks.id
  referenced_security_group_id = aws_security_group.alb.id
  ip_protocol                  = "tcp"
  from_port                    = 8080
  to_port                      = 8080
}

resource "aws_vpc_security_group_ingress_rule" "tasks_grpc" {
  security_group_id            = aws_security_group.tasks.id
  referenced_security_group_id = aws_security_group.tasks.id
  ip_protocol                  = "tcp"
  from_port                    = 9091
  to_port                      = 9093
  description                  = "gRPC between services"
}

resource "aws_vpc_security_group_egress_rule" "tasks_all" {
  security_group_id = aws_security_group.tasks.id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
}

# Private DNS for client-side load balancing: each service gets an A record
# per task (multivalue), and the Go clients dial "dns:///<svc>.<ns>:<port>"
# with round_robin, re-resolving as tasks come and go.
resource "aws_service_discovery_private_dns_namespace" "this" {
  name = "${var.environment}.waypoint.internal"
  vpc  = aws_vpc.this.id
}

resource "aws_service_discovery_service" "backend" {
  for_each = local.grpc_ports
  name     = each.key
  dns_config {
    namespace_id   = aws_service_discovery_private_dns_namespace.this.id
    routing_policy = "MULTIVALUE"
    dns_records {
      type = "A"
      ttl  = 10
    }
  }
  # ECS reports task health to Cloud Map (custom health check).
  health_check_custom_config {}
}

resource "aws_cloudwatch_log_group" "service" {
  for_each          = var.services
  name              = "/ecs/${local.name}/${each.key}"
  retention_in_days = 30
}

locals {
  grpc_ports = { catalog = 9091, applications = 9092, timeline = 9093 }
  ns         = aws_service_discovery_private_dns_namespace.this.name
  public_url = var.public_url != "" ? var.public_url : "http://${aws_lb.this.dns_name}"

  common_env = {
    ENVIRONMENT        = "production"
    AWS_REGION         = var.region
    TABLE_PREFIX       = "${local.name}-"
    AUTO_CREATE_TABLES = "false"
    CACHE_MODE         = "dax"
    DAX_ENDPOINT       = local.dax_endpoint
    CATALOG_ADDR       = "dns:///catalog.${local.ns}:9091"
    APPLICATIONS_ADDR  = "dns:///applications.${local.ns}:9092"
    TIMELINE_ADDR      = "dns:///timeline.${local.ns}:9093"
    LOG_FORMAT         = "json"
  }

  service_env = {
    gateway = {
      HTTP_ADDR       = ":8080"
      ADMIN_ADDR      = ":8081"
      PUBLIC_URL      = local.public_url
      TRUSTED_PROXIES = var.vpc_cidr # the ALB's addresses
    }
    catalog = { GRPC_ADDR = ":9091", ADMIN_ADDR = ":9101" }
    applications = {
      GRPC_ADDR  = ":9092", ADMIN_ADDR = ":9102", BOOTSTRAP_COUNSELOR_EMAIL = var.bootstrap_counselor_email
      PUBLIC_URL = local.public_url, MAIL_MODE = "smtp", SMTP_ADDR = local.smtp_addr, MAIL_FROM = "Waypoint <${var.mail_from_address}>"
    }
    timeline = { GRPC_ADDR = ":9093", ADMIN_ADDR = ":9103" }
  }

  service_secrets = {
    gateway  = [{ name = "INTERNAL_TOKEN", valueFrom = aws_secretsmanager_secret.internal_token.arn }]
    catalog  = [{ name = "INTERNAL_TOKEN", valueFrom = aws_secretsmanager_secret.internal_token.arn }]
    timeline = [{ name = "INTERNAL_TOKEN", valueFrom = aws_secretsmanager_secret.internal_token.arn }]
    applications = [
      { name = "INTERNAL_TOKEN", valueFrom = aws_secretsmanager_secret.internal_token.arn },
      { name = "BOOTSTRAP_COUNSELOR_PASSWORD", valueFrom = aws_secretsmanager_secret.counselor_password.arn },
      { name = "SMTP_USERNAME", valueFrom = "${aws_secretsmanager_secret.smtp.arn}:username::" },
      { name = "SMTP_PASSWORD", valueFrom = "${aws_secretsmanager_secret.smtp.arn}:password::" },
    ]
  }
}

resource "aws_ecs_task_definition" "service" {
  for_each                 = var.services
  family                   = "${local.name}-${each.key}"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = each.value.cpu
  memory                   = each.value.memory
  execution_role_arn       = aws_iam_role.execution.arn
  task_role_arn            = aws_iam_role.task[each.key].arn
  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }

  container_definitions = jsonencode([{
    name                   = each.key
    image                  = "${aws_ecr_repository.service[each.key].repository_url}:${var.image_tag}"
    essential              = true
    readonlyRootFilesystem = true
    portMappings           = each.key == "gateway" ? [{ containerPort = 8080, protocol = "tcp" }] : [{ containerPort = local.grpc_ports[each.key], protocol = "tcp" }]
    environment            = [for k, v in merge(local.common_env, local.service_env[each.key]) : { name = k, value = tostring(v) } if v != ""]
    secrets                = local.service_secrets[each.key]
    healthCheck = {
      command     = ["/app", "healthcheck"]
      interval    = 10
      timeout     = 3
      retries     = 3
      startPeriod = 10
    }
    logConfiguration = {
      logDriver = "awslogs"
      options = {
        awslogs-group         = aws_cloudwatch_log_group.service[each.key].name
        awslogs-region        = var.region
        awslogs-stream-prefix = each.key
      }
    }
    stopTimeout = 30
  }])
}

resource "aws_ecs_service" "service" {
  for_each                           = var.services
  name                               = each.key
  cluster                            = aws_ecs_cluster.this.id
  task_definition                    = aws_ecs_task_definition.service[each.key].arn
  desired_count                      = each.value.desired
  launch_type                        = "FARGATE"
  deployment_minimum_healthy_percent = 100
  deployment_maximum_percent         = 200
  enable_execute_command             = false
  propagate_tags                     = "SERVICE"

  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  network_configuration {
    subnets          = aws_subnet.private[*].id
    security_groups  = [aws_security_group.tasks.id]
    assign_public_ip = false
  }

  dynamic "load_balancer" {
    for_each = each.key == "gateway" ? [1] : []
    content {
      target_group_arn = aws_lb_target_group.gateway.arn
      container_name   = "gateway"
      container_port   = 8080
    }
  }

  dynamic "service_registries" {
    for_each = contains(keys(local.grpc_ports), each.key) ? [1] : []
    content {
      registry_arn = aws_service_discovery_service.backend[each.key].arn
    }
  }

  lifecycle {
    ignore_changes = [desired_count] # owned by autoscaling
  }
}

resource "aws_appautoscaling_target" "service" {
  for_each           = var.services
  service_namespace  = "ecs"
  resource_id        = "service/${aws_ecs_cluster.this.name}/${aws_ecs_service.service[each.key].name}"
  scalable_dimension = "ecs:service:DesiredCount"
  min_capacity       = each.value.desired
  max_capacity       = each.value.max
}

resource "aws_appautoscaling_policy" "cpu" {
  for_each           = var.services
  name               = "${each.key}-cpu"
  policy_type        = "TargetTrackingScaling"
  service_namespace  = aws_appautoscaling_target.service[each.key].service_namespace
  resource_id        = aws_appautoscaling_target.service[each.key].resource_id
  scalable_dimension = aws_appautoscaling_target.service[each.key].scalable_dimension
  target_tracking_scaling_policy_configuration {
    target_value       = 60
    scale_in_cooldown  = 120
    scale_out_cooldown = 60
    predefined_metric_specification {
      predefined_metric_type = "ECSServiceAverageCPUUtilization"
    }
  }
}
