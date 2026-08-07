// Package db 는 이 프로젝트가 DB 에 닿는 **유일한 문**이다.
//
// 호출부(store·identity)는 드라이버를 모른 채 DB 인터페이스만 쓴다. 그래서 Options.Mode 하나로
// 로컬 sqlite 파일과 원격 PostgreSQL 을 바꿔 낄 수 있다.
//
// 자리표시자 규칙: **SQL 본문은 방언 무관하게 `?` 를 쓴다.** pg 드라이버가 로드 시 `$n` 으로
// 옮긴다(ToPg). 이 규칙을 어기면 SQL 을 백엔드마다 두 벌 쓰게 되고, 그 순간 한 벌만 고쳐지는
// 날이 온다.
package db

import (
	"context"
	"fmt"
)

// Dialect 는 백엔드 방언이다. UPSERT 충돌 대상처럼 방언이 갈리는 자리에서 호출부가 읽는다.
type Dialect string

const (
	DialectSQLite   Dialect = "sqlite"
	DialectPostgres Dialect = "postgres"
)

// Row 는 컬럼명 → 값 맵이다. Node 의 q().all()/get() 과 같은 계약.
//
// **키는 항상 소문자다.** pg 는 따옴표 없는 별칭을 소문자로 접고 sqlite 는 쓴 그대로 돌려주므로,
// 양쪽 드라이버가 여기서 소문자로 정규화한다. 이게 없으면 같은 SQL 이 방언마다 다른 키를 주고
// 그 차이가 조용한 0 으로 나타난다(Node 가 `r.ntsunknown ?? r.ntsUnknown` 을 써야 했던 이유).
type Row map[string]any

// DB 는 동결 계약이다. 여기에 없는 것은 드라이버 사정이고 호출부가 알 필요가 없다.
type DB interface {
	Query(ctx context.Context, sql string, args ...any) ([]Row, error)
	// QueryRow 는 행이 없으면 (nil, nil) 이다 — "없음"은 오류가 아니다.
	QueryRow(ctx context.Context, sql string, args ...any) (Row, error)
	Exec(ctx context.Context, sql string, args ...any) error
	// Tx 는 fn 에 넘기는 컨텍스트에 트랜잭션을 실어 준다. 그 컨텍스트로 부른 Query/Exec 는
	// 같은 트랜잭션에서 돈다. 중첩은 금지다(안쪽 Tx 는 바깥 트랜잭션을 그대로 쓴다).
	Tx(ctx context.Context, fn func(context.Context) error) error
	Dialect() Dialect
	Close() error
}

// Options 는 Open 의 입력이다. 값 검증(모드 오타·remote 인데 URL 없음)은 config 가 소유하고,
// 여기서는 마지막 방어선으로만 거른다.
type Options struct {
	Mode    string // "local" | "remote"
	DataDir string // local 전용
	URL     string // remote 전용
}

// Open 은 모드에 맞는 백엔드를 연다.
func Open(ctx context.Context, cfg Options) (DB, error) {
	switch cfg.Mode {
	case "", "local":
		return openSQLite(ctx, cfg)
	case "remote":
		return openPostgres(ctx, cfg)
	default:
		// local 로 조용히 접지 않는다 — 오타가 "로컬 파일에 쓰고 있었다"로 끝나면 안 된다.
		return nil, fmt.Errorf("db: 알 수 없는 모드 %q (local|remote)", cfg.Mode)
	}
}

// ── Row 접근 헬퍼 ─────────────────────────────────────────────────────────
//
// 드라이버마다 정수를 int64 로 줄 수도, float64 나 문자열로 줄 수도 있다(pg 의 int8 파서,
// sqlite 의 SUM 결과). 그 차이를 호출부가 매번 판별하면 한 자리를 빠뜨리는 날이 오고,
// 그때 값은 0 이 되어 조용히 틀린다. 그래서 변환을 여기 한 곳에 둔다.

// Int 는 컬럼을 int64 로 읽는다. 없거나 NULL 이거나 숫자가 아니면 0 이다(Node 의 `Number(x)||0`).
func (r Row) Int(col string) int64 {
	switch v := r[col].(type) {
	case nil:
		return 0
	case int64:
		return v
	case int32:
		return int64(v)
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case float32:
		return int64(v)
	case bool:
		if v {
			return 1
		}
		return 0
	case []byte:
		return parseInt(string(v))
	case string:
		return parseInt(v)
	default:
		return 0
	}
}

// Float 는 컬럼을 float64 로 읽는다. score(real) 처럼 정수가 아닌 축에 쓴다.
func (r Row) Float(col string) float64 {
	switch v := r[col].(type) {
	case nil:
		return 0
	case float64:
		return v
	case float32:
		return float64(v)
	case int64:
		return float64(v)
	case int32:
		return float64(v)
	case int:
		return float64(v)
	case []byte:
		return parseFloat(string(v))
	case string:
		return parseFloat(v)
	default:
		return 0
	}
}

// Str 은 컬럼을 문자열로 읽는다. NULL 은 빈 문자열이다 — 호출부가 "" 와 NULL 을 갈라야 하면
// IsNull 을 함께 쓴다.
func (r Row) Str(col string) string {
	switch v := r[col].(type) {
	case nil:
		return ""
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return fmt.Sprint(v)
	}
}

// IsNull 은 컬럼이 없거나 NULL 인지 본다.
//
// 이 구분이 이 시스템에서 실제로 의미가 있다: usage_sessions.no_ts_turns 의 NULL 은
// "구버전 수집기라 모른다" 이고 0 은 "전 턴에 시각이 있었다" 다. 뭉치면 화면이 구버전 PC 를
// "정상" 으로 단정한다.
func (r Row) IsNull(col string) bool {
	v, ok := r[col]
	return !ok || v == nil
}

// Bool 은 pg 의 boolean 컬럼을 읽는다. 드라이버가 문자열 't'/'f' 로 줄 수도 있어 함께 받는다.
func (r Row) Bool(col string) bool {
	switch v := r[col].(type) {
	case bool:
		return v
	case string:
		return v == "t" || v == "true"
	case []byte:
		s := string(v)
		return s == "t" || s == "true"
	default:
		return false
	}
}
