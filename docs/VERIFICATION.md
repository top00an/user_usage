# 검증 기록 — 제품 구동 · 연동 수집

이 제품이 실제로 구동되고, **연동 시 데이터가 제대로 수집되는지**를 실측한 기록. 각 항목은
재현 명령과 실측 결과를 함께 둔다. 자동화된 것은 `go test`/CI 로 상시 재검증된다.

## 1. 실데이터 E2E (수동 실측, 2026-08-09)

실제 `~/.claude/projects`(143 세션) 중 소량을 수집기로 로컬 서버에 올려 대시보드 반영을 확인.

```sh
bash scripts/build.sh
go -C collector build -o /tmp/usage-collector ./cmd/usage-collector
# 빈 서버 기동(admin/intake 토큰) 후:
USAGE_INTAKE_TOKEN=<intake> /tmp/usage-collector -server http://127.0.0.1:PORT -state <state> -limit 5
```

결과:
- BEFORE `sessions=0` → AFTER `sessions=5, 카운터 391, 버킷 27` (토큰 4축 집계 반영).
- 재실행(`-all`) 후 값 **불변** — 서버 세션 UPSERT 멱등 확인.

## 2. 합성 E2E (자동, 재현 가능)

합성 트랜스크립트(멀티모델·캐시·존없는 타임스탬프) → 서버 바이너리 기동 → 수집 → 조회 검증.
`collector/e2e_test.go`. 바이너리 경로가 주어질 때만 돈다(없으면 skip). CI 가 상시 실행.

```sh
bash scripts/build.sh
go -C collector build -o /tmp/usage-collector ./cmd/usage-collector
USAGE_E2E_SERVER_BIN="$PWD/go/usage-server" USAGE_E2E_COLLECTOR_BIN=/tmp/usage-collector \
  go -C collector test -run E2E -v
```

검증 내용: BEFORE 0 → AFTER 2 세션, 멱등(`-all` 재전송 후 input 불변), 세션 목록에 두 세션 노출.

## 3. 멀티테넌트 격리 실측 (pg RLS, 2026-08-09)

docker PostgreSQL 16 + 앱 롤(NOSUPERUSER·NOBYPASSRLS). org 2개를 실제 인제스트 키로 수집해
크로스테넌트가 **차단**되는지 실측.

```sh
docker run -d --name uu-pg -e POSTGRES_PASSWORD=postgres -p 15433:5432 postgres:16
# 롤·DB·migrations/pg/*.sql·GRANT 세팅(ci.yml pg-isolation 참조)
# org create ×2, key issue ×2 (remote), 멀티테넌트 서버 기동:
USAGE_MULTITENANT=1 USAGE_DB_MODE=remote DATABASE_URL=... ./go/usage-server
```

결과:
- 부팅 로그: `DB 롤 확인 — 비-슈퍼·비-BYPASSRLS(RLS 테넌트 격리 성립)` + `멀티테넌트 모드`.
- A 키 인테이크 → `tenant_id=OrgA`, B 키 → `tenant_id=OrgB` (키가 tenant 를 정확히 결정).
- `set_config('app.tenant_id', OrgA)` 컨텍스트에서 A 세션만 조회, B 는 **0행**. 대칭 확인.
- → **org 간 데이터가 절대 섞이지 않음**을 실제 RLS 로 실증.

> 이 실측 중 실결함 1건을 발견·수정: `remote` 모드가 무조건 `ReadOnly` 라 **멀티테넌트 인테이크
> (pg 에 쓰기)가 막혔다.** `ReadOnly = remote && !MultiTenant` 로 고쳐, SaaS 는 pg 에 쓰되 격리는
> RLS 가 지도록 했다(부팅 프로브가 NOBYPASSRLS 를 강제). 골든 44/44 불변 확인.

## 상시 게이트 (CI)

`.github/workflows/ci.yml`:
- `go test ./...`(백엔드 13패키지)·collector 단위·web 테스트
- 빌드 + **임베드 드리프트 0** + **contract:verify 골든 44/44**
- **collector e2e**(위 2)
- **pg-isolation** 잡(postgres 서비스 + 앱 롤로 크로스테넌트 격리)

⚠ pg-isolation 잡은 CI 첫 실행에서 role/grant 셋업을 1회 확인할 것(로컬은 docker 실측 완료).
