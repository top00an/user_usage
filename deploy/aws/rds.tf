# rds.tf — RDS PostgreSQL(프라이빗, 암호화, 자동 백업) + 전용 보안그룹.
#
# 인터넷 인바운드 0. 오직 앱 태스크 SG(ecs.tf) 에서 5432 만 받는다.
# 마스터 비밀번호는 Secrets Manager 로 자동 관리(manage_master_user_password)한다 — 상태·tfvars 에
# 평문 마스터 비밀번호가 남지 않는다. 앱은 마스터가 아니라 usage_app 롤로 붙는다(RLS 강제).

resource "aws_db_subnet_group" "main" {
  name       = "${local.name}-db"
  subnet_ids = aws_subnet.private[*].id
  tags       = { Name = "${local.name}-db" }
}

resource "aws_security_group" "rds" {
  name        = "${local.name}-rds"
  description = "RDS Postgres - inbound 5432 from app tasks only"
  vpc_id      = aws_vpc.main.id

  tags = { Name = "${local.name}-rds" }
}

resource "aws_security_group_rule" "rds_from_tasks" {
  type                     = "ingress"
  description              = "app tasks to RDS 5432"
  from_port                = 5432
  to_port                  = 5432
  protocol                 = "tcp"
  security_group_id        = aws_security_group.rds.id
  source_security_group_id = aws_security_group.tasks.id
}

# RDS 아웃바운드는 필요 없지만, 일부 확장/모니터링 위해 열어둔다.
resource "aws_security_group_rule" "rds_egress" {
  type              = "egress"
  from_port         = 0
  to_port           = 0
  protocol          = "-1"
  security_group_id = aws_security_group.rds.id
  cidr_blocks       = ["0.0.0.0/0"]
}

resource "aws_db_instance" "main" {
  identifier     = "${local.name}-pg"
  engine         = "postgres"
  engine_version = var.db_engine_version
  instance_class = var.db_instance_class

  allocated_storage     = var.db_allocated_storage
  max_allocated_storage = var.db_max_allocated_storage > 0 ? var.db_max_allocated_storage : null
  storage_type          = "gp3"
  storage_encrypted     = true

  db_name  = var.db_name
  username = var.db_master_username
  # 마스터 비밀번호를 AWS 관리 시크릿으로. password 인자를 쓰지 않는다.
  manage_master_user_password = true

  db_subnet_group_name   = aws_db_subnet_group.main.name
  vpc_security_group_ids = [aws_security_group.rds.id]
  publicly_accessible    = false
  multi_az               = var.db_multi_az

  backup_retention_period = var.db_backup_retention_days
  copy_tags_to_snapshot   = true

  deletion_protection       = var.db_deletion_protection
  skip_final_snapshot       = var.db_skip_final_snapshot
  final_snapshot_identifier = var.db_skip_final_snapshot ? null : "${local.name}-pg-final"

  auto_minor_version_upgrade = true

  tags = { Name = "${local.name}-pg" }
}
