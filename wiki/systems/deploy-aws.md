---
type: system
tags: [배포, aws, terraform, 인프라]
updated: 2026-08-12
sources: ["deploy/README.md", "deploy/aws/", "Dockerfile", "docker-compose.yml"]
---

# 배포 — Docker · AWS(ECS Fargate)

배포 산출물은 [[usage-server]] 단일 바이너리다. 그것을 어디에 놓느냐가 세 가지.

## ① 네이티브

```bash
bash scripts/build.sh
USAGE_ADMIN_TOKEN=… USAGE_DATA_DIR=./data ./go/usage-server
```

## ② Docker

```bash
cp .env.example .env      # USAGE_ADMIN_TOKEN 을 채운다
docker compose up -d      # http://127.0.0.1:4191
```

포트를 **`127.0.0.1` 에 묶어 두었다.** `"4191:4191"` 로 쓰면 도커가 호스트 방화벽을 우회해
LAN 전체에 열린다 — 사람별 사용량·비용 화면에 그 기본값을 쓰지 않는다.

⚠ **런타임 이미지가 `migrations/` 를 담지 않는다.** remote(pg) 모드에서 마이그레이션 러너가
파일을 찾게 하려면 COPY 추가가 필요하다. remote 는 사전 마이그레이션 전제라 조회만 하면
무해할 수 있으나 **확인이 필요한 항목** → [[risks]].

## ③ AWS — ECS Fargate + ALB + RDS

Terraform 구성: `deploy/aws/`. 기존 `Dockerfile` 재사용.

```
인터넷 ──443/80──▶ ALB (ACM TLS, 80→443)
                     │  /healthz 헬스체크 (무인증·무DB)
                     ▼
              ECS Fargate 태스크  (프라이빗 서브넷, 포트 4191)
                 │ env:     USAGE_DB_MODE=remote, USAGE_MULTITENANT=1
                 │ secrets: DATABASE_URL / ADMIN_TOKEN / INTAKE_TOKEN / BOOTSTRAP_ADMIN_PASSWORD
                 ▼
              RDS PostgreSQL (프라이빗, 암호화, 자동 백업)
```

앱 태스크와 RDS 는 프라이빗 서브넷, 인터넷 대면은 **ALB 뿐**이다.

### ⚠ 멀티테넌트가 필수다

이 구성은 `USAGE_DB_MODE=remote` + `USAGE_MULTITENANT=1` 을 함께 준다. remote 인데
MULTITENANT 를 켜지 않으면 앱이 **읽기 전용**이 되어 인제스트가 막힌다
(`ReadOnly = remote && !MultiTenant`) → [[tenancy-rls]].

### 비밀 취급

**DB 접속문자열·admin 토큰·intake 토큰·최초 관리자 비밀번호 4종은 tfvars 에 넣지 않는다.**
Terraform 이 `random_password` 로 생성해 Secrets Manager 에 넣고 ECS `secrets` 로 주입한다.
RDS 마스터 비밀번호는 RDS 가 별도 관리 시크릿으로 자동 생성한다(`manage_master_user_password`).

직접 지정하려면 tfvars 가 아니라 환경변수로:

```bash
export TF_VAR_admin_token_override='...'   # 16자 이상
export TF_VAR_intake_token_override='...'  # admin 과 다른 값 (같으면 부팅 거부 → [[boot-gates]])
```

### `trusted_proxy_count` — 로그인 rate-limiter 의 전제

로그인 rate-limiter 는 클라이언트 IP 별로 버킷을 나눈다. ALB 뒤에서는 소켓 상대 IP 가 항상
ALB 라 그대로 두면 **전체 로그인 시도가 단일 버킷으로 붕괴**한다 — 한 명이 나머지를 잠글 수
있다. 앱은 `USAGE_TRUSTED_PROXY_COUNT` 만큼 `X-Forwarded-For` 우측에서 벗겨 낸 값을 실제
클라이언트 IP 로 쓴다. **ALB 는 정확히 1홉이라 기본값 `1`.** CloudFront 를 더 두면 올린다.

이 신뢰가 안전한 이유: **앱 태스크가 ALB 를 통해서만 접근 가능**하다. tasks SG 인바운드는
ALB SG 한 곳만 허용하고, 태스크는 프라이빗 서브넷·퍼블릭 IP 없음이라 클라이언트가 ALB 를
우회해 XFF 를 위조 주입할 경로가 없다.

### 배포 순서

이미지가 없으면 태스크가 뜨지 않으므로 **인프라 → 이미지 push → 마이그레이션 → 태스크 기동**.

1. `terraform apply`
2. 도메인·ACM 검증 (`route53_zone_id` 를 주면 자동, 안 주면 CNAME 수동 추가)
3. `docker build` → ECR push
4. **DB 마이그레이션** — RDS 는 프라이빗이라 배스천/SSM 포트포워딩/VPN 에서 psql 로.
   컨테이너 이미지에 psql 이 없어 **앱 태스크로는 못 돌린다.**
   마스터 자격(`rds_master_user_secret_arn`)으로 `migrations/pg/*.sql` 을 **번호 순**으로.
5. 앱 롤(`usage_app`, NOSUPERUSER·NOBYPASSRLS) 생성 + GRANT

### 주요 변수

필수는 **`region`·`domain_name`** 둘뿐. 기본값이 있는 것 중 눈여겨볼 것:

| 변수 | 기본 | 의미 |
|---|---|---|
| `desired_count` | 2 | 상시 태스크 수 |
| `db_multi_az` / `db_deletion_protection` | false / true | RDS 가용성·보호 |
| `single_nat_gateway` | true | NAT 1개(비용↓) vs AZ별(가용성↑) |
| `enable_waf` / `waf_rate_limit` | true / 2000 | ALB rate-based WAF (5분/IP) |
| `session_ttl` | 12h | 로그인 세션 수명 |
| `trusted_proxy_count` | 1 | 위 참조 |

## 되돌리기

- **배포 롤백** → 이전 이미지 태그로 서비스 롤백. 앱은 스키마를 자동으로 바꾸지 않으므로
  (마이그레이션 수동) **앱만 되돌리는 것이 안전하다.**
- **마이그레이션 롤백** → **자동 경로가 없다.** down 마이그레이션이 없으므로 스냅샷/PITR
  복구가 유일하다. **적용 전에 백업을 확인하라.**

## 관련

[[usage-server]] · [[tenancy-rls]] · [[boot-gates]] · [[runbook]] · [[risks]]
