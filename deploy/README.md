# AWS 배포 (Terraform IaC)

Claude Code 사용량 관측 SaaS 를 AWS 에 **공개 HTTPS SaaS**(공개 HTTPS + 인제스트 키 인증)로 올리는
Terraform 구성이다. 코드 위치: [`deploy/aws/`](./aws/).

플랫폼: 기존 `Dockerfile` 재사용 → **ECR → ECS Fargate → ALB(ACM TLS) → RDS PostgreSQL**.
앱 태스크와 RDS 는 프라이빗 서브넷, 인터넷 대면은 ALB 뿐이다. 비밀 3종은 Secrets Manager 로만 주입한다.

```
인터넷 ──443/80──▶ ALB (ACM TLS, 80→443)
                     │  /healthz 헬스체크 (무인증·무DB)
                     ▼
              ECS Fargate 태스크  (프라이빗 서브넷, 포트 4191)
                 │ env: USAGE_DB_MODE=remote, USAGE_MULTITENANT=1
                 │ secrets: DATABASE_URL / USAGE_ADMIN_TOKEN / USAGE_INTAKE_TOKEN
                 ▼
              RDS PostgreSQL (프라이빗, 암호화, 자동 백업)
```

> ⚠ **멀티테넌트 필수**: 이 구성은 태스크에 `USAGE_DB_MODE=remote` + `USAGE_MULTITENANT=1` 을 준다.
> remote 인데 MULTITENANT 를 켜지 않으면 앱이 **읽기 전용**이 되어 인제스트(수집)가 막힌다
> (`go/internal/config/config.go:225-226`). org 격리는 DB 의 RLS(`app.tenant_id`)가 강제한다.

---

## 0. 사전 준비 (계정 부트스트랩 — 수동)

Terraform 이 만들지 않는, 계정에 한 번만 해두는 것들:

1. **AWS 자격증명**: `aws configure` 또는 환경변수(`AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`/`AWS_REGION`).
   apply 에 필요한 권한: VPC·EC2·ELB·ECS·ECR·RDS·IAM·SecretsManager·CloudWatch·WAFv2·(선택)Route53.
2. **도메인**: 서비스에 붙일 도메인(예 `usage.example.com`)을 준비한다.
   Route53 호스팅 영역이 있으면 `route53_zone_id` 를 넘겨 ACM DNS 검증·A 레코드를 자동화할 수 있다.
3. **(권장) 원격 상태 백엔드**: 팀 운영이면 S3+DynamoDB 상태 백엔드를 만들고
   [`aws/versions.tf`](./aws/versions.tf) 의 `backend "s3"` 블록을 열어 채운다.
   지금은 로컬 상태로 둔다(개인 검증용).
4. **도구**: `terraform >= 1.5`, `aws` CLI, 이미지 빌드용 `docker`(+ 기존 `scripts/build.sh` 의존성).

---

## 1. 입력 변수

필수는 **`region`, `domain_name`** 둘뿐이다. 나머지는 기본값이 있다.
전체 목록·기본값은 [`aws/variables.tf`](./aws/variables.tf), 예시는
[`aws/terraform.tfvars.example`](./aws/terraform.tfvars.example) 참조.

| 변수 | 필수 | 기본 | 설명 |
|---|---|---|---|
| `region` | ✅ | — | 배포 리전 |
| `domain_name` | ✅ | — | 공개 도메인(ACM/ALB) |
| `route53_zone_id` |  | `""` | 주면 ACM 검증·A레코드 자동 생성. 비우면 수동 DNS |
| `image_tag` |  | `latest` | ECR 이미지 태그 |
| `desired_count` |  | `2` | 상시 태스크 수 |
| `task_cpu`/`task_memory` |  | `512`/`1024` | Fargate 크기 |
| `db_instance_class` |  | `db.t4g.micro` | RDS 클래스 |
| `db_multi_az` |  | `false` | RDS 대기 인스턴스 |
| `db_deletion_protection` |  | `true` | RDS 삭제 보호 |
| `db_skip_final_snapshot` |  | `false` | destroy 시 최종 스냅샷 생략 |
| `single_nat_gateway` |  | `true` | NAT 1개(비용↓) vs AZ별(가용성↑) |
| `enable_waf` |  | `true` | ALB rate-based WAF on/off |
| `waf_rate_limit` |  | `2000` | 5분/IP 요청 상한 |
| `admin_token_override` |  | `""`(자동생성) | 열람 토큰 직접 지정(16자+) |
| `intake_token_override` |  | `""`(자동생성) | 인제스트 토큰 직접 지정(16자+) |

### 비밀 취급

**DB 접속문자열·admin 토큰·intake 토큰 3종은 tfvars 에 넣지 않는다.**
기본적으로 Terraform 이 `random_password` 로 생성해 Secrets Manager 에 넣는다
([`aws/secrets.tf`](./aws/secrets.tf)). RDS 마스터 비밀번호는 RDS 가 별도 관리 시크릿으로 자동 생성한다
(`manage_master_user_password`). 굳이 토큰을 직접 지정하려면 `terraform.tfvars` 가 아니라
환경변수로만 넘긴다:

```bash
export TF_VAR_admin_token_override='...'   # 16자 이상
export TF_VAR_intake_token_override='...'  # admin 과 다른 값
```

---

## 2. 배포 절차 (apply)

이미지가 없으면 태스크가 뜨지 않으므로 **인프라 먼저 → 이미지 push → 마이그레이션 → 태스크 기동** 순서다.
서비스는 `wait_for_steady_state=false` 라 apply 자체는 태스크 정상화를 기다리지 않고 끝난다.

### 2-1. 인프라 생성

```bash
cd deploy/aws
cp terraform.tfvars.example terraform.tfvars   # region·domain_name 채우기
terraform init
terraform plan -out tfplan
terraform apply tfplan
```

주요 output:

```bash
terraform output ecr_repository_url          # 이미지 push 대상
terraform output alb_dns_name                # 수동 DNS 시 도메인을 여기로
terraform output acm_validation_records      # 수동 DNS 시 ACM 검증 레코드
terraform output rds_endpoint
terraform output rds_master_user_secret_arn  # 마이그레이션용 마스터 자격
terraform output service_url
```

### 2-2. 도메인·인증서 검증

- `route53_zone_id` 를 **줬으면**: 검증 레코드와 도메인 A(alias) 레코드가 자동 생성되고
  `aws_acm_certificate_validation` 이 검증 완료까지 기다린다. 추가 작업 없음.
- **안 줬으면(수동)**: `terraform output acm_validation_records` 의 name/type/value 를 도메인 DNS 에
  CNAME 으로 추가해 ACM 검증을 통과시키고, `alb_dns_name` 으로 서비스 도메인의 A(ALIAS)/CNAME 을 향하게 한다.
  검증이 끝나야 443 리스너가 실제 TLS 를 서빙한다.

### 2-3. 이미지 빌드 & push (기존 Dockerfile 재사용)

기존 `Dockerfile` / `scripts/build.sh` 를 그대로 쓴다(앱 소스·Dockerfile 변경 없음).

```bash
REPO=$(cd deploy/aws && terraform output -raw ecr_repository_url)
ACCOUNT_REGISTRY=${REPO%/*}                              # <acct>.dkr.ecr.<region>.amazonaws.com
REGION=$(echo "$ACCOUNT_REGISTRY" | cut -d. -f4)         # 레지스트리 호스트에서 리전 추출

# ECR 로그인
aws ecr get-login-password --region "$REGION" | docker login --username AWS --password-stdin "$ACCOUNT_REGISTRY"

# 리포 루트에서 빌드(멀티스테이지: web build → go:embed → 정적 바이너리)
docker build -t "$REPO:latest" .
docker push "$REPO:latest"
```

> 이미지 태그를 `latest` 외로 쓰면 `image_tag` 변수를 맞추고 다시 apply 한다.

### 2-4. DB 마이그레이션 & 앱 롤 (RDS 는 프라이빗)

RDS 는 인터넷에 열려 있지 않다. **RDS 5432 에 도달 가능한 위치**(같은 VPC 의 배스천/SSM 포트포워딩,
또는 VPN)에서 psql 로 아래를 순서대로 실행한다. 컨테이너 이미지에는 psql 이 없으므로 앱 태스크로는
못 돌린다.

**① 마스터 자격 조회**(RDS 가 자동 생성한 시크릿에서):

```bash
MSECRET=$(cd deploy/aws && terraform output -raw rds_master_user_secret_arn)
aws secretsmanager get-secret-value --secret-id "$MSECRET" \
  --query SecretString --output text        # {"username":"usage_admin","password":"..."}
RDS_HOST=$(cd deploy/aws && terraform output -raw rds_endpoint | cut -d: -f1)
```

**② 스키마 마이그레이션 적용**(마스터로 접속, 파일명 오름차순):

```bash
# migrations/pg/*.sql 를 번호 순서대로. 예:
for f in $(ls migrations/pg/*.sql | sort); do
  echo "applying $f"
  PGPASSWORD='<master-password>' psql "host=$RDS_HOST port=5432 dbname=usage user=usage_admin sslmode=require" -v ON_ERROR_STOP=1 -f "$f"
done
```

**③ 앱 전용 롤 생성 + 권한 부여**(가장 중요 — RLS 격리의 근거).
앱은 마스터가 아니라 **`usage_app` 롤**로 붙는다. 이 롤은 반드시 **`NOSUPERUSER NOBYPASSRLS`** 여야 한다.
앱은 부팅 시 이 속성을 **직접 확인하고 위반이면 기동을 거부한다**(`.env.example:50-53`).
비밀번호는 `DATABASE_URL` 시크릿에 박힌 값과 같아야 한다:

```bash
# DATABASE_URL 시크릿에서 usage_app 비밀번호를 꺼낸다
DBURL=$(aws secretsmanager get-secret-value \
  --secret-id "$(cd deploy/aws && terraform output -raw secret_database_url_arn)" \
  --query SecretString --output text)
# postgres://usage_app:<PW>@host:5432/usage?sslmode=require 에서 <PW> 추출
APP_PW=$(echo "$DBURL" | sed -E 's#^postgres://usage_app:([^@]+)@.*#\1#')
```

```sql
-- 마스터로 접속해 실행
CREATE ROLE usage_app WITH LOGIN PASSWORD '<APP_PW>'
  NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE;

GRANT CONNECT ON DATABASE usage TO usage_app;
GRANT USAGE ON SCHEMA public TO usage_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO usage_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO usage_app;
-- 이후 마이그레이션이 만들 표에도 기본 권한이 붙도록
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO usage_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO usage_app;
```

> **RLS 주의**: `usage_*`·`member_tokens` 표는 `FORCE ROW LEVEL SECURITY` + `tenant_isolation` 정책으로
> `app.tenant_id` GUC 에 묶인다(예: `migrations/pg/0032_member_tokens.sql`). `orgs`·`ingest_keys` 는
> 인제스트 키→tenant 라우팅 표라 RLS 를 걸지 않는다(순환 방지). **절대 슈퍼유저/BYPASSRLS 롤로 앱을
> 붙이지 말 것** — 요청은 200 이 나오는데 테넌트 격리가 통째로 무력화된다(무증상 사고).

**④ 서비스 재기동**(이미지·마이그레이션이 준비된 뒤 새 배포로 태스크를 띄운다):

```bash
CLUSTER=$(cd deploy/aws && terraform output -raw ecs_cluster_name)
SERVICE=$(cd deploy/aws && terraform output -raw ecs_service_name)
aws ecs update-service --cluster "$CLUSTER" --service "$SERVICE" --force-new-deployment
```

### 2-5. 검증

```bash
curl -fsS "https://<domain>/healthz"     # 200 (무인증·무DB)
# 열람 토큰은 Secrets Manager 에서
aws secretsmanager get-secret-value --secret-id "$(terraform output -raw secret_admin_token_arn)" --query SecretString --output text
```

수집기(collector)에는 **intake 토큰만** 배포한다:

```bash
aws secretsmanager get-secret-value --secret-id "$(terraform output -raw secret_intake_token_arn)" --query SecretString --output text
```

---

## 3. 관측 · 로그

- ECS 태스크 로그: CloudWatch Logs 로그그룹 **`/ecs/<project>-<env>`**([`aws/ecs.tf`](./aws/ecs.tf)).
- ECS Container Insights 활성(클러스터 메트릭).
- 배포 실패 시 **자동 롤백**: 서비스에 `deployment_circuit_breaker { rollback = true }`.

---

## 4. 롤백 · 파기

### 애플리케이션 롤백(이전 이미지로)
새 이미지가 나쁘면 이전 태그로 되돌린다. 배포 서킷 브레이커가 헬스 실패 시 자동 롤백하지만,
수동으로도:

```bash
# 이전에 잘 돌던 태그로 새 배포
aws ecs update-service --cluster "$CLUSTER" --service "$SERVICE" \
  --task-definition <이전-taskdef-arn> --force-new-deployment
```

### 인프라 변경 롤백
`terraform plan` 으로 diff 를 먼저 보고 apply. 상태 백엔드(S3)를 쓰면 버전 관리로 되돌릴 수 있다.

### 전체 파기(destroy)
**되돌릴 수 없는 동작이다 — RDS 데이터가 사라진다. 반드시 사람이 확인 후 실행.**

```bash
cd deploy/aws
terraform destroy
```

주의:
- `db_deletion_protection=true`(기본)면 RDS 가 지워지지 않는다. 파기하려면 먼저
  `-var db_deletion_protection=false` 로 apply 한 뒤 destroy.
- `db_skip_final_snapshot=false`(기본)면 destroy 시 `<name>-pg-final` 최종 스냅샷을 남긴다.
- `aws_ecr_repository.force_delete=false` 라 이미지가 남아 있으면 ECR 삭제가 막힌다(의도된 안전장치).
- Secrets Manager 시크릿은 즉시 삭제되지 않고 복구 대기기간이 있다.

---

## 5. 비용 메모(대략, 리전·환율에 따라 다름)

| 항목 | 절감 레버 |
|---|---|
| NAT Gateway | `single_nat_gateway=true`(기본) — AZ별로 두면 시간당 요금이 배로 |
| ALB | 상시 과금. 트래픽 적으면 가장 큰 고정비 중 하나 |
| RDS | `db.t4g.micro` + `db_multi_az=false`(기본). 운영 안정성 필요 시 Multi-AZ 로 |
| Fargate | `task_cpu/memory`·`desired_count` 로 조절 |
| ECR | lifecycle policy 로 최근 10개만 보관 |

---

## 파일 맵 ([`deploy/aws/`](./aws/))

| 파일 | 관심사 |
|---|---|
| `versions.tf` | Terraform/provider 핀, provider 설정, (주석)백엔드 |
| `variables.tf` | 입력 변수 |
| `network.tf` | VPC, 서브넷(2AZ), IGW, NAT, 라우팅 |
| `alb.tf` | ALB, ALB SG, 타깃그룹(/healthz), ACM, 리스너(443/80→443), Route53 |
| `ecr.tf` | ECR 리포지토리 + lifecycle |
| `ecs.tf` | 클러스터, 태스크 SG, 로그그룹, IAM(exec/task), 태스크 정의, 서비스 |
| `rds.tf` | RDS Postgres, RDS SG, 서브넷 그룹 |
| `secrets.tf` | Secrets Manager 3종 + 값 생성 |
| `waf.tf` | (선택) WAFv2 rate-based + ALB 연결 |
| `outputs.tf` | apply 후 참조값 |
| `terraform.tfvars.example` | 입력 예시(실값 금지) |
