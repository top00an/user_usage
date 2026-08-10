# variables.tf — 모든 입력변수. 비밀값은 여기에 기본값으로 넣지 않는다.
#
# 필수로 채워야 하는 것: region, domain_name.
# 나머지는 합리적 기본값이 있다. 비밀(DB 접속·토큰)은 terraform 이 생성하거나(random),
# 선택적 override 변수로만 받는다 — terraform.tfvars.example 참조.

variable "project" {
  description = "리소스 이름 접두사 겸 태그."
  type        = string
  default     = "user-usage"
}

variable "environment" {
  description = "환경 태그(prod/staging 등)."
  type        = string
  default     = "prod"
}

variable "region" {
  description = "배포 AWS 리전(예: ap-northeast-2)."
  type        = string
}

# ── 네트워크 ──────────────────────────────────────────────────────────────
variable "vpc_cidr" {
  description = "VPC CIDR."
  type        = string
  default     = "10.20.0.0/16"
}

variable "az_count" {
  description = "사용할 가용영역 수(퍼블릭/프라이빗 서브넷 각 AZ 1개)."
  type        = number
  default     = 2

  validation {
    condition     = var.az_count >= 2
    error_message = "고가용성을 위해 최소 2개 AZ 가 필요하다(ALB 요건)."
  }
}

variable "single_nat_gateway" {
  description = "true 면 NAT Gateway 를 1개만 둔다(비용 절감). false 면 AZ 마다 1개(가용성)."
  type        = bool
  default     = true
}

# ── 엣지 / TLS ────────────────────────────────────────────────────────────
variable "domain_name" {
  description = "서비스 공개 도메인(ACM 인증서·ALB 대상). 예: usage.example.com"
  type        = string
}

variable "route53_zone_id" {
  description = "도메인의 Route53 호스팅 영역 ID. 주면 ACM DNS 검증 레코드와 A(alias) 레코드를 자동 생성한다. 비우면 검증 레코드를 outputs 로 내보내 수동 생성하게 한다."
  type        = string
  default     = ""
}

# ── 컴퓨트 ────────────────────────────────────────────────────────────────
variable "image_tag" {
  description = "ECR 에 푸시한 이미지 태그. 서비스가 이 태그를 당긴다."
  type        = string
  default     = "latest"
}

variable "container_port" {
  description = "컨테이너 리스닝 포트(Dockerfile EXPOSE 4191)."
  type        = number
  default     = 4191
}

variable "task_cpu" {
  description = "Fargate 태스크 CPU 단위(256=0.25 vCPU)."
  type        = number
  default     = 512
}

variable "task_memory" {
  description = "Fargate 태스크 메모리(MiB)."
  type        = number
  default     = 1024
}

variable "desired_count" {
  description = "ECS 서비스 상시 태스크 수."
  type        = number
  default     = 2
}

variable "log_retention_days" {
  description = "CloudWatch Logs 보존 일수."
  type        = number
  default     = 30
}

# ── 데이터 / RDS ──────────────────────────────────────────────────────────
variable "db_engine_version" {
  description = "RDS PostgreSQL 엔진 버전."
  type        = string
  default     = "16.4"
}

variable "db_instance_class" {
  description = "RDS 인스턴스 클래스."
  type        = string
  default     = "db.t4g.micro"
}

variable "db_allocated_storage" {
  description = "RDS 초기 스토리지(GiB)."
  type        = number
  default     = 20
}

variable "db_max_allocated_storage" {
  description = "스토리지 자동확장 상한(GiB). 0 이면 자동확장 끔."
  type        = number
  default     = 100
}

variable "db_name" {
  description = "생성할 데이터베이스 이름."
  type        = string
  default     = "usage"
}

variable "db_master_username" {
  description = "RDS 마스터 사용자명(마이그레이션·롤 프로비저닝용). 앱은 이 계정으로 붙지 않는다."
  type        = string
  default     = "usage_admin"
}

variable "db_app_username" {
  description = "앱이 붙는 전용 롤 이름(NOSUPERUSER·NOBYPASSRLS 로 부트스트랩에서 생성). DATABASE_URL 시크릿에 박힌다."
  type        = string
  default     = "usage_app"
}

variable "db_backup_retention_days" {
  description = "RDS 자동 백업 보존 일수."
  type        = number
  default     = 7
}

variable "db_multi_az" {
  description = "RDS Multi-AZ 대기 인스턴스 사용 여부."
  type        = bool
  default     = false
}

variable "db_deletion_protection" {
  description = "RDS 삭제 보호. 운영은 true 권장."
  type        = bool
  default     = true
}

variable "db_skip_final_snapshot" {
  description = "destroy 시 최종 스냅샷 생략 여부. 운영은 false 권장."
  type        = bool
  default     = false
}

# ── 시크릿 override(선택) ─────────────────────────────────────────────────
# 비우면 terraform 이 random 으로 생성한다(Secrets Manager 에서 조회). 값을 주면 그 값을 쓴다.
# 실제 값은 tfvars 에 커밋하지 말 것 — CI 변수·-var 파일(gitignore)·환경변수로만 넘긴다.
variable "admin_token_override" {
  description = "USAGE_ADMIN_TOKEN 을 직접 지정(최소 16자). 비우면 자동 생성."
  type        = string
  default     = ""
  sensitive   = true

  validation {
    condition     = var.admin_token_override == "" || length(var.admin_token_override) >= 16
    error_message = "admin_token_override 는 비우거나 16자 이상이어야 한다."
  }
}

variable "intake_token_override" {
  description = "USAGE_INTAKE_TOKEN 을 직접 지정(최소 16자, admin 과 달라야 함). 비우면 자동 생성."
  type        = string
  default     = ""
  sensitive   = true

  validation {
    condition     = var.intake_token_override == "" || length(var.intake_token_override) >= 16
    error_message = "intake_token_override 는 비우거나 16자 이상이어야 한다."
  }
}

# ── WAF ───────────────────────────────────────────────────────────────────
variable "enable_waf" {
  description = "ALB 앞에 WAFv2(rate-based) 를 붙일지."
  type        = bool
  default     = true
}

variable "waf_rate_limit" {
  description = "WAF rate-based 규칙: 5분 창당 IP 요청 상한."
  type        = number
  default     = 2000
}
