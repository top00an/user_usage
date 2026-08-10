# versions.tf — Terraform / provider 핀과 AWS provider 설정.
#
# 이 구성은 자격증명 없이 `terraform init -backend=false && terraform validate` 로만 검증한다.
# 실제 plan/apply 에는 AWS 자격증명(환경변수 또는 프로필)이 필요하다.
#
# 원격 상태(운영 권장): 아래 backend "s3" 블록을 열고 값을 채운 뒤 `terraform init` 하라.
# 지금은 로컬 상태로 둔다(README 의 "상태 백엔드" 참조).

terraform {
  required_version = ">= 1.5"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.5"
    }
  }

  # backend "s3" {
  #   bucket         = "<your-tf-state-bucket>"
  #   key            = "user-usage/aws/terraform.tfstate"
  #   region         = "<your-region>"
  #   dynamodb_table = "<your-tf-lock-table>"
  #   encrypt        = true
  # }
}

provider "aws" {
  region = var.region

  # validate 를 자격증명 없이 통과시키기 위한 스킵 플래그. apply 시에는 실제 자격증명이 있어야 한다.
  skip_credentials_validation = true
  skip_requesting_account_id  = true
  skip_metadata_api_check     = true

  default_tags {
    tags = {
      Project     = var.project
      Environment = var.environment
      ManagedBy   = "terraform"
    }
  }
}
