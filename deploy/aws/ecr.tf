# ecr.tf — 앱 이미지 레지스트리.
#
# 기존 Dockerfile 로 빌드한 이미지를 여기에 푸시한다(README 의 build/push 절차 참조).
# 서비스는 var.image_tag 태그를 당긴다.

resource "aws_ecr_repository" "app" {
  name                 = local.name
  image_tag_mutability = "MUTABLE"
  force_delete         = false

  image_scanning_configuration {
    scan_on_push = true
  }

  encryption_configuration {
    encryption_type = "AES256"
  }
}

# 오래된 이미지를 정리해 스토리지 비용을 억제한다(최근 10개만 유지).
resource "aws_ecr_lifecycle_policy" "app" {
  repository = aws_ecr_repository.app.name

  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "최근 10개 이미지만 보관"
      selection = {
        tagStatus   = "any"
        countType   = "imageCountMoreThan"
        countNumber = 10
      }
      action = { type = "expire" }
    }]
  })
}
