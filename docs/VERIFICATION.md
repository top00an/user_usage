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

> **원천 격리(2026-08-10 보강).** 이 테스트는 합성 픽스처만 읽어야 하므로 나머지 원천을 전부
> 끈다: `-codex-dir "" -gemini-dir "" -antigravity-dir ""`. `-gemini-dir ""` 가 빠져 있어서
> **실제 `~/.gemini` 세션이 있는 머신에서는 그 세션이 함께 올라가 세션 수 단정이 흔들렸다.**
> 실측으로 확인한 차이(합성 Gemini 홈 1세션 기준):
>
> ```
> -gemini-dir <경로>  → [gemini] …/tmp — 세션 1개 · 바뀐 세션 1개
> -gemini-dir ""      → (gemini 원천이 아예 스캔되지 않음)
> ```

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

## 4. 온보딩·키 스코프 실측 (2026-08-10)

로컬 sqlite 서버 + CLI 프로비저닝으로 **원커맨드 연동 전 경로**를 실측했다.

```sh
usage-server org create --name "Acme"        # → org 생성됨: id=org_… tenant=org_…
usage-server key issue  --org org_…          # → uu_ing_…            (평문 1회)
usage-server user add -tenant default -username ops-admin -role admin
usage-server key revoke --key uu_ing_…       # → 해지됨
```

응답 코드(실측):

| 요청 | 유효 키 | 해지된 키 | 관리자 토큰 |
|---|---|---|---|
| `GET /healthz` | 200(무인증) | — | — |
| `GET /install.sh` | 200 `text/x-shellscript`(무인증) | — | — |
| `GET /api/agent/collector?os=darwin&arch=arm64` | **200** | **401** | 200 |
| `POST /api/usage` | **200** | **401** | 200 |
| `GET /api/usage/summary` | **403**(보고 전용) | **401** | **200** |

키 없이 다운로드 시도, 위조 키(`uu_ing_bogus`) 둘 다 **401**.

**최초 관리자 부트스트랩**: `USAGE_BOOTSTRAP_ADMIN_USER`+`_PASSWORD` 로 기동 →
`· 최초 관리자 생성: tenant=default username=ops-admin role=admin` 출력 →
`POST /api/auth/login` 200 + `GET /api/auth/me` 200, 틀린 비밀번호 401.

## 5. 원커맨드 설치기 실측 (2026-08-10)

격리한 `HOME` 에 **기존 `SessionEnd` 훅과 `theme` 가 있는 `settings.json`** 을 두고
`scripts/install.sh` 를 실행했다.

| 확인 | 결과 |
|---|---|
| 기존 훅·`theme` 보존 | 그대로 남음(우리 훅이 뒤에 추가됨) |
| 재실행 멱등 | 2회 실행 후에도 우리 훅 그룹 **1개**(전체 2개 = 남의 것 1 + 우리 것 1) |
| `settings.json` 내 평문 키 | **0건** — 토큰은 `config.env` 에만 |
| `config.env` 권한 | `-rw-------`(600) |
| 덮기 전 백업 | `settings.json.bak` 생성 |
| 백필 | `연동 완료 ✓ — 0 세션 전송`(빈 픽스처라 0) |

⚠ **`.bak` 은 "직전 상태"이지 "최초 원본"이 아니다** — 실측 확인. 2회 실행 후의 `.bak` 에는
이미 우리 훅이 들어 있다. 완전한 제거는 `.bak` 복구가 아니라 jq 로 우리 훅 그룹만 빼는 쪽이
맞다(절차는 [`OPERATIONS.md`](OPERATIONS.md) §6-1). 그 제거 후 남의 훅과 `theme` 가 보존되는
것까지 실측했다.

## 상시 게이트 (CI)

`.github/workflows/ci.yml`:
- `go test ./...`(백엔드 — 2026-08-10 기준 **12 패키지**)·collector 단위·web 테스트
- 빌드 + **임베드 드리프트 0** + **contract:verify 골든 44/44**
- **collector e2e**(위 2)
- **pg-isolation** 잡(postgres 서비스 + 앱 롤로 크로스테넌트 격리)

⚠ pg-isolation 잡은 CI 첫 실행에서 role/grant 셋업을 1회 확인할 것(로컬은 docker 실측 완료).

⚠ `ci.yml` 의 스텝 이름이 아직 "백엔드 11 패키지"다 — 실제는 12 다. 표시 문구일 뿐 게이트
동작(`go test ./...`)에는 영향이 없지만, 세는 값이 문구와 어긋나 있다.
