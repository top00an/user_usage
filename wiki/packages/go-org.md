---
type: package
tags: [go, org, 인제스트키, 멀티테넌트]
updated: 2026-08-12
sources: ["go/internal/org/org.go", "docs/PLAN-phase1-multitenant.md", "migrations/pg/0030_orgs.sql", "migrations/pg/0038_ingest_key_user.sql"]
---

# `internal/org` — org 와 인제스트 키

조직(= 테넌트)과 인제스트 키의 발급·해석·해지.

```go
func Init(ctx, d db.DB) error
func Resolve(ctx, db, plaintext string) (tenant, orgID string, ok bool)
```

> 계획서는 `ResolveIngestKey` 로 적었지만 실제 이름은 **`org.Resolve`** 다
> (`docs/PLAN-phase1-multitenant.md` S1 의 결과 기록).

## 스키마

```sql
orgs(id, tenant_id, name, created_at, status)
ingest_keys(key_hash, org_id, username, created_at, revoked_at, last_used_at)
```

- `migrations/pg/0030_orgs.sql` — org·키
- `migrations/pg/0038_ingest_key_user.sql` — `username` 결속 → [[attribution]]

**org = tenant.** `org create` 는 org id 를 그대로 tenant id 로 쓴다(`org_<hex>`).

## 키의 성질

| 성질 | 값 |
|---|---|
| 형식 | `uu_ing_<rand>` |
| 저장 | **`sha256` 해시만.** 평문은 발급 시 **1회만** 노출 |
| 스코프 | 보고 전용 — `POST /api/usage` + 수집기 내려받기. 열람은 403 |
| 해지 후 | 401 |
| 회전 | 전용 명령 없음 — **새 키 발급 → 배포 → 옛 키 해지** |

### 왜 키에 org 를 박지 않나

계획 원안은 `uu_ing_<org>_<rand>` 였다. 구현에서 바꾼 이유:

> org 는 해시 조회로 알아내므로 키 문자열에 조직 식별자를 넣을 이유가 없고, 넣으면
> **키 하나가 조직 이름까지 흘린다.**

### 왜 평문을 저장하지 않나

키는 팀원 수만큼 복제되어 각자의 디스크에 놓인다. DB 가 새면 그 전부가 한꺼번에 샌다.
해시만 저장하면 DB 유출이 곧 키 유출이 되지 않는다.

대가는 "다시 볼 수 없다"이고, 그래서 **잃어버리면 재발급**한다 → [[ingest-keys]].

## `username` 결속 (귀속 우선순위 ①)

키에 사람을 묶으면 그 키로 들어온 보고가 **키 주인으로 귀속**된다. 클라이언트가 주장하는
`payload.user` 를 이긴다 → [[attribution]].

`nullable` 이 하위호환의 전부다. **기존 행을 한 줄도 건드리지 않는다** — `NOT NULL` 이나
`DEFAULT` 를 걸면 이미 배포된 모든 키의 귀속이 그날로 바뀐다.

### ⚠ sqlite 컬럼 보강

sqlite 는 `CREATE TABLE IF NOT EXISTS` 가 **기존 표에 컬럼을 넣지 않는다.** 그래서 `Init` 이
`PRAGMA table_info` 로 확인하고 `ALTER TABLE ADD COLUMN` 을 건다.

이 보강이 없으면 옛 DB 로 뜬 서버에서 키 해석 질의가 통째로 실패하고, **증상은 전 팀원의
보고가 401** 이다. 원인이 인증처럼 보이는 자리라 특히 조심할 곳.

## 상수시간 해석

`Resolve` 는 상수시간 비교를 쓴다(S1 의 AC). 키 존재 여부가 타이밍으로 새지 않게.

같은 규율이 응답에도 있다 — **남의 키 해지와 없는 키는 같은 404·같은 문구**다. 갈라 주면
그 차이가 곧 "그 키는 존재한다"는 신호가 된다 → [[auth-scopes]].

## 짝 패키지 — `internal/identity`

→ [[go-identity]]

## 관련

[[ingest-keys]] · [[attribution]] · [[tenancy-rls]] · [[auth-scopes]] · [[go-identity]]
