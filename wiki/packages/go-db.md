---
type: package
tags: [go, db, sqlite, postgres, rls]
updated: 2026-08-12
sources: ["go/internal/db/", "go/CONTRACT.md", "PORT-STATUS.md"]
---

# `internal/db` — sqlite | PostgreSQL 어댑터

한 인터페이스로 두 방언을 덮고, 마이그레이션 러너와 RLS 가드를 담는다.

```go
type Dialect string
const (DialectSQLite Dialect = "sqlite"; DialectPostgres Dialect = "postgres")

type Row map[string]any

type DB interface {
    Query(ctx, sql string, args ...any) ([]Row, error)
    QueryRow(ctx, sql string, args ...any) (Row, error)  // 없으면 (nil, nil)
    Exec(ctx, sql string, args ...any) error
    Tx(ctx, fn func(context.Context) error) error
    Dialect() Dialect
    Close() error
}

func Open(ctx, cfg Options) (DB, error)
type Options struct { Mode, DataDir, URL string }
```

## 자리표시자 규칙

**SQL 본문에 `?` 로 쓰고 pg 드라이버가 `$n` 으로 치환한다.** 그리고 **문자열 리터럴 안의 `?`
는 건드리지 않는다**(`sql.go`).

이 규칙을 어기면 SQL 을 백엔드마다 두 벌 쓰게 되고, 그 순간 **한 벌만 고쳐지는 날이 온다.**

## RLS 주입

pg 경로는 매 쿼리에서 `tenant.From(ctx)` 를 주입한다:

```
BEGIN → set_config('app.tenant_id', <tenant>, true) → 본문 → COMMIT
```

`db.SetTenantResolver(tenant.From)` 을 [[go-httpapi]] 가 반드시 배선해야 한다. 안 걸면
**모든 쿼리가 `default` 테넌트로 흐르고 요청은 200 이다** → [[tenancy-rls]].

## `rlsguard.go`

```go
type RoleRow struct { Role string; Super bool; BypassRLS bool }
type Verdict struct { OK bool; Inconclusive bool; Message string }

func CheckRLS(row *RoleRow) Verdict   // row == nil → Inconclusive
func Remedy(msg string) string
```

**판정 불가(터널 미개통·DB 다운)는 거부하지 않는다.** 붙지 못한 DB 는 노출도 없고, 여기서
죽이면 "터널을 먼저 뚫는다"는 정상 절차가 부팅 실패로 보인다. 대신 stderr 로 크게 남긴다.

호출부는 `go/cmd/usage-server/rlsgate.go` 이고, 반드시 `Verdict.Rejects()` 로 분기한다
→ [[boot-gates]].

프로브는 `pg_roles` 만 읽는다 — **remote 부팅은 원격 DB 에 아무것도 쓰지 않는다.**

## `migrate.go` — 자동 실행 경로에 없다

`migrations/pg/*.sql` 을 번호 순으로 적용하는 러너. **부팅에 걸려 있지 않다** — 되돌리기
어려운 작업은 사람이 명시적으로 돌린다.

번호에 공백이 있다(0014·0015·0017·0026 …). 의도된 것 — 기존 DB 의 `schema_migrations` 와
대조할 수 있어야 하고, 러너는 없는 번호를 신경 쓰지 않는다.

## pg 커넥션 풀 — 손으로 만들었다

`pgxpool` 도 `pgx/v5/stdlib` 도 `github.com/jackc/puddle/v2` 를 요구하는데, 포팅 웨이브 당시
`go.mod`/`go.sum` 오너십 때문에 추가하지 못했다. 기본 `pgx` 위에 **동시 상한 · 유휴 재사용 ·
고장 커넥션 폐기**만 하는 얇은 보관함을 두었다(`USAGE_PG_POOL_MAX`, 기본 10).

**전환 조건:** pg 실측에서 커넥션 고갈·누수가 관측되면 `puddle/v2` 를 넣고 `pgxpool` 로
바꾼다. `DB` 인터페이스가 안 변하므로 **`internal/db/pg.go` 한 파일만** 고치면 된다.

> 쓰지 않는 의존성을 미리 넣으면 `go mod tidy` 가 그것을 지우거나 사람이 그 불일치를 계속
> 설명해야 한다 — 그래서 지금은 그대로 둔다(`go/CONTRACT.md` 개정 2).

→ [[risks]]

## pg 전용으로 잡힌 버그 하나

`SUM/AVG(bigint)` → `numeric` 을 **조용히 0 으로 떨구던 스캔 결함**(`db/pg.go`).
2026-08-09 의 pg 실측에서 발견·수정됐다. sqlite 로만 돌던 동안에는 드러나지 않았다
→ [[node-to-go-port]].

## 테스트

| | 조건 |
|---|---|
| `sqlite_test.go` · `sql_test.go` · `rlsguard_test.go` · `migrate_test.go` | 상시 |
| `pg_test.go` (`-run PG`) | `USAGE_TEST_PG_URL` 이 있을 때만. 없으면 skip |

pg 통합 테스트는 앱 롤로 **크로스테넌트 격리 · `?→$n` · 커넥션 풀**을 실측한다.
CI 의 `pg-isolation` 잡이 이것을 돌린다 → [[ci-gates]].

```bash
USAGE_TEST_PG_URL=postgres://usage_app:<pw>@127.0.0.1:5432/<db> \
  go test ./go/internal/db/ -run PG -v
```

## 짝 패키지 — `internal/tenant`

Node 의 `AsyncLocalStorage` 를 `context.Context` 로 옮긴 것. Go 에서는 이쪽이 더 자연스럽다 —
암묵 전파가 아니라 **명시 인자**라 "감싸는 것을 잊었다"가 컴파일에서 잡힌다.

```go
func With(ctx context.Context, tenant string) context.Context
func From(ctx context.Context) string   // 없으면 "default"
```

컨텍스트 키는 외부에서 만들 수 없게 **비공개 타입**이다.

## 관련

[[tenancy-rls]] · [[boot-gates]] · [[go-store]] · [[ci-gates]] · [[risks]]
