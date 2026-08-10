# outputs.tf — apply 후 필요한 값들.

output "alb_dns_name" {
  description = "ALB DNS 이름. route53 를 안 쓸 때 도메인 CNAME/A 를 여기로 향하게 한다."
  value       = aws_lb.main.dns_name
}

output "alb_zone_id" {
  description = "ALB 호스티드 존 ID(수동 alias 레코드용)."
  value       = aws_lb.main.zone_id
}

output "service_url" {
  description = "공개 서비스 URL."
  value       = "https://${var.domain_name}"
}

output "ecr_repository_url" {
  description = "이미지 push 대상."
  value       = aws_ecr_repository.app.repository_url
}

output "ecs_cluster_name" {
  description = "ECS 클러스터 이름."
  value       = aws_ecs_cluster.main.name
}

output "ecs_service_name" {
  description = "ECS 서비스 이름."
  value       = aws_ecs_service.app.name
}

output "task_definition_family" {
  description = "태스크 정의 family(마이그레이션 one-off task 실행 시 참조)."
  value       = aws_ecs_task_definition.app.family
}

output "private_subnet_ids" {
  description = "프라이빗 서브넷(one-off 마이그레이션 태스크 실행에 사용)."
  value       = aws_subnet.private[*].id
}

output "tasks_security_group_id" {
  description = "앱 태스크 SG(one-off 마이그레이션 태스크에 재사용 — RDS 5432 인바운드가 이 SG 를 허용)."
  value       = aws_security_group.tasks.id
}

output "rds_endpoint" {
  description = "RDS 엔드포인트(host:port)."
  value       = aws_db_instance.main.endpoint
}

output "rds_master_user_secret_arn" {
  description = "AWS 가 관리하는 RDS 마스터 자격 시크릿 ARN(마이그레이션·롤 생성에 사용)."
  value       = try(aws_db_instance.main.master_user_secret[0].secret_arn, null)
}

output "secret_database_url_arn" {
  description = "DATABASE_URL 시크릿 ARN."
  value       = aws_secretsmanager_secret.database_url.arn
}

output "secret_admin_token_arn" {
  description = "USAGE_ADMIN_TOKEN 시크릿 ARN(값 조회: get-secret-value)."
  value       = aws_secretsmanager_secret.admin_token.arn
}

output "secret_intake_token_arn" {
  description = "USAGE_INTAKE_TOKEN 시크릿 ARN(수집기 배포용, 값 조회: get-secret-value)."
  value       = aws_secretsmanager_secret.intake_token.arn
}

# ACM DNS 검증 레코드 — route53_zone_id 를 안 줬을 때 수동으로 심어야 하는 값.
output "acm_certificate_arn" {
  description = "ACM 인증서 ARN."
  value       = aws_acm_certificate.app.arn
}

output "acm_validation_records" {
  description = "route53 를 안 쓸 때 도메인 DNS 에 직접 추가할 ACM 검증 레코드(name/type/value)."
  value = [
    for dvo in aws_acm_certificate.app.domain_validation_options : {
      name  = dvo.resource_record_name
      type  = dvo.resource_record_type
      value = dvo.resource_record_value
    }
  ]
}
