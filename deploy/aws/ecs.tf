# ecs.tf — ECS Fargate 클러스터/서비스, 태스크 정의, IAM, 태스크 SG, 로그.
#
# 태스크는 프라이빗 서브넷에서 돈다(퍼블릭 IP 없음, NAT 로 아웃바운드). ALB SG 에서만 4191 인바운드.
# 비밀 3종은 평문 env 가 아니라 `secrets`(Secrets Manager)로 주입한다.
#
# 멀티테넌트 SaaS: USAGE_DB_MODE=remote + USAGE_MULTITENANT=1 이라야 인제스트가 열린다
# (config.go:225-226 — remote 인데 MULTITENANT 안 켜면 ReadOnly 라 수집이 막힌다).

# ── 태스크 보안그룹: ALB → 4191 만 ────────────────────────────────────────────
resource "aws_security_group" "tasks" {
  name        = "${local.name}-tasks"
  description = "ECS tasks - container port inbound from ALB only"
  vpc_id      = aws_vpc.main.id
  tags        = { Name = "${local.name}-tasks" }
}

resource "aws_security_group_rule" "tasks_from_alb" {
  type                     = "ingress"
  description              = "ALB to task container port"
  from_port                = var.container_port
  to_port                  = var.container_port
  protocol                 = "tcp"
  security_group_id        = aws_security_group.tasks.id
  source_security_group_id = aws_security_group.alb.id
}

resource "aws_security_group_rule" "tasks_egress" {
  type              = "egress"
  description       = "task outbound (ECR pull / Secrets / RDS / CloudWatch)"
  from_port         = 0
  to_port           = 0
  protocol          = "-1"
  security_group_id = aws_security_group.tasks.id
  cidr_blocks       = ["0.0.0.0/0"]
}

# ── 로그 ──────────────────────────────────────────────────────────────────────
resource "aws_cloudwatch_log_group" "app" {
  name              = "/ecs/${local.name}"
  retention_in_days = var.log_retention_days
}

# ── IAM: 실행 역할(이미지 pull·시크릿 조회·로그) ─────────────────────────────
data "aws_iam_policy_document" "ecs_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "execution" {
  name               = "${local.name}-exec"
  assume_role_policy = data.aws_iam_policy_document.ecs_assume.json
}

resource "aws_iam_role_policy_attachment" "execution_managed" {
  role       = aws_iam_role.execution.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

# 실행 역할이 우리 시크릿 3종만 읽도록 최소권한 인라인 정책(리소스 스코프).
data "aws_iam_policy_document" "exec_secrets" {
  statement {
    sid     = "ReadAppSecrets"
    actions = ["secretsmanager:GetSecretValue"]
    resources = [
      aws_secretsmanager_secret.database_url.arn,
      aws_secretsmanager_secret.admin_token.arn,
      aws_secretsmanager_secret.intake_token.arn,
    ]
  }
}

resource "aws_iam_role_policy" "exec_secrets" {
  name   = "${local.name}-exec-secrets"
  role   = aws_iam_role.execution.id
  policy = data.aws_iam_policy_document.exec_secrets.json
}

# ── IAM: 태스크 역할(앱 런타임 — 현재 추가 AWS 권한 불필요) ───────────────────
resource "aws_iam_role" "task" {
  name               = "${local.name}-task"
  assume_role_policy = data.aws_iam_policy_document.ecs_assume.json
}

# ── 클러스터 ──────────────────────────────────────────────────────────────────
resource "aws_ecs_cluster" "main" {
  name = local.name

  setting {
    name  = "containerInsights"
    value = "enabled"
  }
}

# ── 태스크 정의 ────────────────────────────────────────────────────────────────
resource "aws_ecs_task_definition" "app" {
  family                   = local.name
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.task_cpu
  memory                   = var.task_memory
  execution_role_arn       = aws_iam_role.execution.arn
  task_role_arn            = aws_iam_role.task.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }

  container_definitions = jsonencode([{
    name      = "app"
    image     = "${aws_ecr_repository.app.repository_url}:${var.image_tag}"
    essential = true

    portMappings = [{
      containerPort = var.container_port
      protocol      = "tcp"
    }]

    environment = [
      { name = "USAGE_HOST", value = "0.0.0.0" },
      { name = "USAGE_PORT", value = tostring(var.container_port) },
      { name = "USAGE_DB_MODE", value = "remote" },
      { name = "USAGE_MULTITENANT", value = "1" },
    ]

    secrets = [
      { name = "DATABASE_URL", valueFrom = aws_secretsmanager_secret.database_url.arn },
      { name = "USAGE_ADMIN_TOKEN", valueFrom = aws_secretsmanager_secret.admin_token.arn },
      { name = "USAGE_INTAKE_TOKEN", valueFrom = aws_secretsmanager_secret.intake_token.arn },
    ]

    logConfiguration = {
      logDriver = "awslogs"
      options = {
        "awslogs-group"         = aws_cloudwatch_log_group.app.name
        "awslogs-region"        = var.region
        "awslogs-stream-prefix" = "app"
      }
    }

    healthCheck = {
      command     = ["CMD-SHELL", "wget -q -O /dev/null http://127.0.0.1:${var.container_port}/healthz || exit 1"]
      interval    = 30
      timeout     = 5
      retries     = 3
      startPeriod = 10
    }
  }])
}

# ── 서비스 ────────────────────────────────────────────────────────────────────
resource "aws_ecs_service" "app" {
  name            = local.name
  cluster         = aws_ecs_cluster.main.id
  task_definition = aws_ecs_task_definition.app.arn
  desired_count   = var.desired_count
  launch_type     = "FARGATE"

  # 첫 apply 때 이미지가 아직 없거나 마이그레이션 전이라 태스크가 안 뜰 수 있다.
  # apply 가 steady state 를 기다리다 막히지 않게 한다(README 의 배포 순서 참조).
  wait_for_steady_state = false

  network_configuration {
    subnets          = aws_subnet.private[*].id
    security_groups  = [aws_security_group.tasks.id]
    assign_public_ip = false
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.app.arn
    container_name   = "app"
    container_port   = var.container_port
  }

  # 배포 실패 시 자동 롤백(되돌릴 수 있는 배포).
  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  health_check_grace_period_seconds = 60

  depends_on = [aws_lb_listener.https]

  lifecycle {
    # CI 가 새 태스크 정의로 배포를 밀 때 terraform 이 되돌리지 않도록.
    ignore_changes = [task_definition]
  }
}
