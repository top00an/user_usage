# secrets.tf — Secrets Manager 3종(DB 접속문자열·admin 토큰·intake 토큰).
#
# 평문 env 금지: 세 값 모두 여기서 관리하고 ECS 태스크에 `secrets` 로 주입한다(ecs.tf).
# 기본은 terraform 이 random 으로 생성한다 — tfvars 에 실제 비밀을 커밋할 일이 없다.
# override 변수를 주면 그 값을 쓴다(CI 변수/환경변수로만 넘길 것).
#
# 조회 방법(생성값 확인):
#   aws secretsmanager get-secret-value --secret-id <name> --query SecretString --output text
#
# ⚠ RDS 마스터 비밀번호는 여기 없다 — rds.tf 가 manage_master_user_password 로 별도 관리 시크릿을
#   자동 생성한다. 앱은 마스터가 아니라 db_app_username 롤(NOBYPASSRLS)로 붙는다.

resource "random_password" "db_app" {
  length  = 32
  special = false # DATABASE_URL 에 그대로 들어가므로 URL 안전 문자만.
}

resource "random_password" "admin_token" {
  length  = 40
  special = false
}

resource "random_password" "intake_token" {
  length  = 40
  special = false
}

locals {
  admin_token  = var.admin_token_override != "" ? var.admin_token_override : random_password.admin_token.result
  intake_token = var.intake_token_override != "" ? var.intake_token_override : random_password.intake_token.result

  # 앱 전용 롤로 붙는 접속 문자열. sslmode=require 로 RDS TLS 를 강제한다.
  database_url = format(
    "postgres://%s:%s@%s:%d/%s?sslmode=require",
    var.db_app_username,
    random_password.db_app.result,
    aws_db_instance.main.address,
    aws_db_instance.main.port,
    var.db_name,
  )
}

# ── DATABASE_URL ────────────────────────────────────────────────────────────
resource "aws_secretsmanager_secret" "database_url" {
  name        = "${local.name}/database-url"
  description = "앱 전용 롤(usage_app) PostgreSQL 접속 문자열."
}

resource "aws_secretsmanager_secret_version" "database_url" {
  secret_id     = aws_secretsmanager_secret.database_url.id
  secret_string = local.database_url
}

# ── USAGE_ADMIN_TOKEN ───────────────────────────────────────────────────────
resource "aws_secretsmanager_secret" "admin_token" {
  name        = "${local.name}/admin-token"
  description = "대시보드 열람 토큰(전사)."
}

resource "aws_secretsmanager_secret_version" "admin_token" {
  secret_id     = aws_secretsmanager_secret.admin_token.id
  secret_string = local.admin_token
}

# ── USAGE_INTAKE_TOKEN ──────────────────────────────────────────────────────
resource "aws_secretsmanager_secret" "intake_token" {
  name        = "${local.name}/intake-token"
  description = "수집기 배포용 보고 전용 토큰(POST /api/usage 만 허용)."
}

resource "aws_secretsmanager_secret_version" "intake_token" {
  secret_id     = aws_secretsmanager_secret.intake_token.id
  secret_string = local.intake_token
}
