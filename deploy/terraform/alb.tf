resource "aws_security_group" "alb" {
  name        = "${local.name}-alb"
  description = "Public load balancer"
  vpc_id      = aws_vpc.this.id
  tags        = { Name = "${local.name}-alb" }
}

resource "aws_vpc_security_group_ingress_rule" "alb_http" {
  #checkov:skip=CKV_AWS_260:Port 80 serves the site only until a certificate is configured; with certificate_arn it only redirects to HTTPS
  security_group_id = aws_security_group.alb.id
  description       = "HTTP from the internet (redirects to HTTPS when a certificate is set)"
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "tcp"
  from_port         = 80
  to_port           = 80
}

resource "aws_vpc_security_group_ingress_rule" "alb_https" {
  count             = var.certificate_arn == "" ? 0 : 1
  security_group_id = aws_security_group.alb.id
  description       = "HTTPS from the internet"
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
}

resource "aws_vpc_security_group_egress_rule" "alb_to_gateway" {
  security_group_id            = aws_security_group.alb.id
  description                  = "To the gateway tasks"
  referenced_security_group_id = aws_security_group.tasks.id
  ip_protocol                  = "tcp"
  from_port                    = 8080
  to_port                      = 8080
}

resource "aws_lb" "this" {
  #checkov:skip=CKV_AWS_91:Access logs need an S3 bucket with an ELB delivery policy; request logs already come from the gateway (structured JSON in CloudWatch)
  #checkov:skip=CKV2_AWS_76:The WebACL has KnownBadInputs (Log4j); the check also demands AnonymousIpList, which blocks VPN and hosting addresses and would lock out students abroad, many of whom (e.g. in mainland China) browse through VPNs
  #checkov:skip=CKV2_AWS_20:HTTP redirects to HTTPS whenever certificate_arn is set (see aws_lb_listener.http); without a certificate there is no HTTPS to redirect to
  name                       = local.name
  enable_deletion_protection = var.deletion_protection
  load_balancer_type         = "application"
  subnets                    = aws_subnet.public[*].id
  security_groups            = [aws_security_group.alb.id]
  drop_invalid_header_fields = true
  # Server-Sent Events connections stay open; the gateway sends a keep-alive
  # comment every 20 s, well inside this timeout.
  idle_timeout = 120
}

resource "aws_lb_target_group" "gateway" {
  #checkov:skip=CKV_AWS_378:TLS terminates at the ALB; ALB to task traffic stays inside the VPC on private subnets
  name                 = "${local.name}-gw"
  port                 = 8080
  protocol             = "HTTP"
  target_type          = "ip"
  vpc_id               = aws_vpc.this.id
  deregistration_delay = 30
  health_check {
    path                = "/healthz"
    matcher             = "200"
    interval            = 15
    healthy_threshold   = 2
    unhealthy_threshold = 3
  }
}

resource "aws_lb_listener" "http" {
  #checkov:skip=CKV_AWS_2:Forwards only when no certificate is configured; otherwise this listener only redirects to HTTPS
  #checkov:skip=CKV_AWS_103:Plain HTTP listener (see above); the HTTPS listener uses ELBSecurityPolicy-TLS13-1-2-2021-06
  load_balancer_arn = aws_lb.this.arn
  port              = 80
  protocol          = "HTTP"

  dynamic "default_action" {
    for_each = var.certificate_arn == "" ? [1] : []
    content {
      type             = "forward"
      target_group_arn = aws_lb_target_group.gateway.arn
    }
  }
  dynamic "default_action" {
    for_each = var.certificate_arn == "" ? [] : [1]
    content {
      type = "redirect"
      redirect {
        port        = "443"
        protocol    = "HTTPS"
        status_code = "HTTP_301"
      }
    }
  }
}

resource "aws_lb_listener" "https" {
  count             = var.certificate_arn == "" ? 0 : 1
  load_balancer_arn = aws_lb.this.arn
  port              = 443
  protocol          = "HTTPS"
  ssl_policy        = "ELBSecurityPolicy-TLS13-1-2-2021-06"
  certificate_arn   = var.certificate_arn
  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.gateway.arn
  }
}
