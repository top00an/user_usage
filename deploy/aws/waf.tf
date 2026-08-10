# waf.tf — (선택) ALB 앞 WAFv2 rate-based 규칙. enable_waf 로 on/off.
#
# 단순한 IP 단위 rate limit 한 개. 인제스트/열람 엔드포인트에 대한 러프한 폭주 방지선이다.
# 세밀한 인증은 앱의 토큰 게이트가 담당하고, WAF 는 그 앞의 얇은 방어막이다.

resource "aws_wafv2_web_acl" "main" {
  count = var.enable_waf ? 1 : 0

  name        = "${local.name}-acl"
  description = "ALB rate-based protection"
  scope       = "REGIONAL"

  default_action {
    allow {}
  }

  rule {
    name     = "rate-limit"
    priority = 1

    action {
      block {}
    }

    statement {
      rate_based_statement {
        limit              = var.waf_rate_limit
        aggregate_key_type = "IP"
      }
    }

    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "${local.name}-rate-limit"
      sampled_requests_enabled   = true
    }
  }

  visibility_config {
    cloudwatch_metrics_enabled = true
    metric_name                = "${local.name}-acl"
    sampled_requests_enabled   = true
  }
}

resource "aws_wafv2_web_acl_association" "alb" {
  count        = var.enable_waf ? 1 : 0
  resource_arn = aws_lb.main.arn
  web_acl_arn  = aws_wafv2_web_acl.main[0].arn
}
