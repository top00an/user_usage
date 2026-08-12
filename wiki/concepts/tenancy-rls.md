---
type: concept
tags: [멀티테넌트, rls, postgres, 격리]
updated: 2026-08-12
sources: ["README.md", "docs/VERIFICATION.md", "go/internal/db/rlsguard.go", "docs/PLAN-phase1-multitenant.md"]
---

# 테넌시와 RLS — org 간 격리

## 세 모드

| 모드 | env | 쓰기 | 격리 |
|---|---|---|---|
| local | 기본 | ✅ sqlite | 없음(단일 조직) |
| remote 조회 | `USAGE_DB_MODE=remote` | ❌ **읽기 전용** | RLS(조회만) |
| SaaS | + `USAGE_MULTITENANT=1` | ✅ pg | **RLS** |

핵심 한 줄:

```go
ReadOnly = remote && !MultiTenant
```

remote 읽기 전용 모드에서 인테이크·귀속 교정 쓰기·보존 정리기는 **막힌 게 아니라 존재하지
않는다**(404) → [[auth-scopes]].

> 이 식이 없어서 **멀티테넌트 인테이크가 통째로 막혔던 실결함**이 pg 실측에서 발견돼
> 고쳐졌다(`docs/VERIFICATION.md` §3). 골든 44/44 불변 확인 후 반영.

## sqlite + 멀티테넌트 = 부팅 거부

**경고가 아니라 거부다.** sqlite 에는 RLS 가 없어 여러 org 의 사용량·비용이 한 파일에 섞이는데
**요청은 200 이라 아무도 눈치채지 못한다.** 경고로 끝낼 문제가 아니다 → [[boot-gates]].

## RLS 배선

pg 경로는 매 쿼리에서 `tenant.From(ctx)` 를 RLS 에 주입한다:

```
BEGIN → set_config('app.tenant_id', <tenant>, true) → 본문 → COMMIT
```

`go/CONTRACT.md` 개정 2 가 **반드시 배선할 것** 둘을 못박았다. 둘 다 안 걸면 **조용히 틀린다**
— 요청은 200 이고 화면도 정상으로 보인다:

1. **`db.SetTenantResolver(tenant.From)`** — 안 걸면 pg 의 모든 쿼리가 `default` 테넌트로 흐른다.
2. **RLS 판정은 `Verdict.Rejects()` 로만 분기한다.** `!v.OK` 로 분기하면 터널 미개통(판정 불가)
   에서 부팅이 막혀, "터널을 먼저 뚫는다"는 정상 절차가 부팅 실패로 보인다.

## ⚠ 앱 롤로 붙어라 — 증상이 없는 사고

`DATABASE_URL` 이 **SUPERUSER 또는 BYPASSRLS** 롤이면 RLS 테넌트 격리가 통째로 무력화된다.
**그런데 증상이 없다** — 요청은 200 이고 데이터도 잘 보인다(남의 것까지).

```sql
CREATE ROLE usage_app LOGIN NOSUPERUSER NOBYPASSRLS PASSWORD '…';
```

경고문으로만 남겨 두지 않았다. **서버가 부팅에서 롤을 직접 확인하고 위반이면 거부한다** —
`go/internal/db/rlsguard.go` 의 판정을 `go/cmd/usage-server/main.go` 가 부팅 경로에서 부른다.

```go
type RoleRow struct { Role string; Super bool; BypassRLS bool }
type Verdict struct { OK bool; Inconclusive bool; Message string }
func CheckRLS(row *RoleRow) Verdict   // row == nil → Inconclusive
func Remedy(msg string) string
```

**판정 불가(터널 미개통·DB 다운)는 거부하지 않는다.** 붙지 못한 DB 는 노출도 없고, 여기서
죽이면 정상 절차가 부팅 실패로 보인다. 대신 stderr 로 크게 남긴다 —
**검사가 돌지 않았다는 사실 자체가 기록돼야 한다** → [[honest-uncertainty]].

기동 로그에서 확인:

```
  · DB 롤 확인 — 비-슈퍼·비-BYPASSRLS(RLS 테넌트 격리 성립)     ← 이 줄이 있어야 한다
  ⚠ DB 롤을 확인하지 못했다                                    ← 이 줄이면 격리 미검증
```

## FORCE ROW LEVEL SECURITY

마이그레이션이 `FORCE` 를 건다. 그래서 **테이블 소유자도 RLS 를 탄다** — CI 의 `pg-isolation`
잡에서 앱 롤이 테이블 소유자가 되지만 격리가 여전히 성립하는 이유다 → [[ci-gates]].

⚠ 이 성질이 마이그레이션 하나를 까다롭게 만들었다. `0037_placeholder_model_cleanup.sql` 은
`app.tenant_id` GUC 없이 도는데, FORCE 를 안 풀면 **소유자에게도 한 행이 보이지 않아 오류
없이 0행을 고치고 끝난다**(조용한 미적용). 그래서 트랜잭션 안에서만 잠시 풀고 끝에서
되돌린다 — 그동안 ACCESS EXCLUSIVE 락이 걸리므로 **트래픽이 낮을 때** 돌린다 → [[cleanup]].

## org = tenant

`usage-server org create --name X` 는 **org id 를 그대로 tenant id 로 쓴다**(`org_<hex>`).
키 → tenant 매핑은 해시 조회로 한다.

키 형식은 `uu_ing_<rand>` — **org 식별자를 키에 박지 않는다.** 박으면 키 하나가 조직 이름까지
흘린다(계획 원안은 `uu_ing_<org>_<rand>` 였고, 구현에서 바꿨다) → [[ingest-keys]].

## 실측 (2026-08-09)

docker PostgreSQL 16 + 앱 롤. org 2개를 실제 인제스트 키로 수집:

- 부팅 로그: `DB 롤 확인 — 비-슈퍼·비-BYPASSRLS` + `멀티테넌트 모드`
- A 키 인테이크 → `tenant_id=OrgA`, B 키 → `tenant_id=OrgB` (키가 tenant 를 정확히 결정)
- `set_config('app.tenant_id', OrgA)` 컨텍스트에서 A 세션만 조회, B 는 **0행**. 대칭 확인
- → **org 간 데이터가 절대 섞이지 않음**을 실제 RLS 로 실증

CI 의 `pg-isolation` 잡이 이것을 상시 재검증한다.

## rate limit (쿼터는 아직 없다)

| env | 기본 | 뜻 |
|---|---|---|
| `USAGE_INTAKE_RATE` | 20 | 테넌트별 초당 리필. 음수면 무제한 |
| `USAGE_INTAKE_BURST` | 40 | 토큰버킷 상한 |

⚠ **per-tenant 누적 쿼터는 미구현이다.** rate-limit 은 순간 폭주만 막는다. 한 org 의 누적
사용량 상한이 없어 스토리지·비용이 무제한으로 늘 수 있다 — Phase 1 에서 **유일하게 닫히지
않은 항목** → [[risks]].

## 관련

[[go-db]] · [[boot-gates]] · [[deploy-aws]] · [[ci-gates]] · [[ingest-keys]] · [[risks]]
