# alb.tf — 인터넷 대면 ALB(443 HTTPS / 80→443 리다이렉트), ACM TLS, 타깃그룹, 도메인.
#
# 헬스체크 타깃은 /healthz(무인증·무DB 경로). 타깃 타입 ip(Fargate awsvpc).
# ACM 인증서는 DNS 검증. route53_zone_id 를 주면 검증 레코드와 alias 레코드를 자동 생성한다.

# ── ALB 보안그룹: 인터넷 → 443/80 만 ─────────────────────────────────────────
resource "aws_security_group" "alb" {
  name        = "${local.name}-alb"
  description = "ALB - internet inbound 443/80"
  vpc_id      = aws_vpc.main.id
  tags        = { Name = "${local.name}-alb" }
}

resource "aws_security_group_rule" "alb_https_in" {
  type              = "ingress"
  description       = "internet to ALB 443"
  from_port         = 443
  to_port           = 443
  protocol          = "tcp"
  security_group_id = aws_security_group.alb.id
  cidr_blocks       = ["0.0.0.0/0"]
  ipv6_cidr_blocks  = ["::/0"]
}

resource "aws_security_group_rule" "alb_http_in" {
  type              = "ingress"
  description       = "internet to ALB 80 (redirect to 443)"
  from_port         = 80
  to_port           = 80
  protocol          = "tcp"
  security_group_id = aws_security_group.alb.id
  cidr_blocks       = ["0.0.0.0/0"]
  ipv6_cidr_blocks  = ["::/0"]
}

resource "aws_security_group_rule" "alb_egress" {
  type              = "egress"
  description       = "ALB to tasks / outbound"
  from_port         = 0
  to_port           = 0
  protocol          = "-1"
  security_group_id = aws_security_group.alb.id
  cidr_blocks       = ["0.0.0.0/0"]
}

# ── ALB ──────────────────────────────────────────────────────────────────────
resource "aws_lb" "main" {
  name               = local.name
  internal           = false
  load_balancer_type = "application"
  security_groups    = [aws_security_group.alb.id]
  subnets            = aws_subnet.public[*].id

  drop_invalid_header_fields = true
  enable_deletion_protection = false

  tags = { Name = local.name }
}

resource "aws_lb_target_group" "app" {
  name        = local.name
  port        = var.container_port
  protocol    = "HTTP"
  vpc_id      = aws_vpc.main.id
  target_type = "ip"

  health_check {
    path                = "/healthz"
    port                = "traffic-port"
    protocol            = "HTTP"
    matcher             = "200"
    interval            = 30
    timeout             = 5
    healthy_threshold   = 2
    unhealthy_threshold = 3
  }

  deregistration_delay = 30
  tags                 = { Name = local.name }
}

# ── ACM 인증서(DNS 검증) ──────────────────────────────────────────────────────
resource "aws_acm_certificate" "app" {
  domain_name       = var.domain_name
  validation_method = "DNS"

  lifecycle {
    create_before_destroy = true
  }

  tags = { Name = var.domain_name }
}

# route53_zone_id 가 있으면 검증 레코드를 자동으로 심는다.
resource "aws_route53_record" "cert_validation" {
  for_each = var.route53_zone_id == "" ? {} : {
    for dvo in aws_acm_certificate.app.domain_validation_options : dvo.domain_name => {
      name   = dvo.resource_record_name
      type   = dvo.resource_record_type
      record = dvo.resource_record_value
    }
  }

  zone_id = var.route53_zone_id
  name    = each.value.name
  type    = each.value.type
  records = [each.value.record]
  ttl     = 60
}

# route53 를 쓸 때만 검증 완료를 기다린다. 수동 DNS 면 이 리소스를 건너뛰고
# listener 는 (미검증 상태로도 생성되나) 실제 트래픽 전 검증을 끝내야 한다.
resource "aws_acm_certificate_validation" "app" {
  count                   = var.route53_zone_id == "" ? 0 : 1
  certificate_arn         = aws_acm_certificate.app.arn
  validation_record_fqdns = [for r in aws_route53_record.cert_validation : r.fqdn]
}

# ── 리스너 ────────────────────────────────────────────────────────────────────
resource "aws_lb_listener" "https" {
  load_balancer_arn = aws_lb.main.arn
  port              = 443
  protocol          = "HTTPS"
  ssl_policy        = "ELBSecurityPolicy-TLS13-1-2-2021-06"
  certificate_arn   = aws_acm_certificate.app.arn

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.app.arn
  }
}

resource "aws_lb_listener" "http_redirect" {
  load_balancer_arn = aws_lb.main.arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type = "redirect"
    redirect {
      protocol    = "HTTPS"
      port        = "443"
      status_code = "HTTP_301"
    }
  }
}

# route53_zone_id 가 있으면 도메인 A(alias) 레코드를 ALB 로 향하게 한다.
resource "aws_route53_record" "app_alias" {
  count   = var.route53_zone_id == "" ? 0 : 1
  zone_id = var.route53_zone_id
  name    = var.domain_name
  type    = "A"

  alias {
    name                   = aws_lb.main.dns_name
    zone_id                = aws_lb.main.zone_id
    evaluate_target_health = true
  }
}
