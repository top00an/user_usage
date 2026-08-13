/*
Package store 는 사용량 텔레메트리 저장소 — 팀원 PC 가 무엇을 얼마나 썼는지의 관측 계층이다.

네 테이블(스키마 근거와 축 설명은 migrations/pg/0014_usage.sql·0017_usage_series.sql 주석이
단일 출처):

	usage_sessions         세션 단위 토큰 사용량(입력·출력·캐시읽기·캐시생성)
	usage_series           시간 × 모델 버킷 — PK 에 model 이 있다
	usage_counters         축(kind)별 카운터 — tool|bash|slash|skill|agent|mcp|keyword
	usage_recommendations  추천 호출 관측 — 카탈로그 공백 탐지용

품질 판정 신호와 **테이블을 가르는** 것이 이 계층의 전제다. 여기 들어오는 건 관측치이지
판정이 아니다 — 섞으면 "Bash 85회" 같은 관측치가 판정 입력으로 새어 들어간다.

**멱등성이 이 계층의 핵심 계약이다.** 클라이언트 훅은 세션 절대값을 보내고(델타가 아니다),
여기서는 (세션, 축, 키)로 **덮어쓴다**. 누적(+=)을 쓰면 훅이 두 번 돌 때 값이 두 배가 된다.
훅은 실패해도 재시도하는 best-effort 경로라 중복 전송이 정상 동작에 포함된다.
*/
package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/tscorp/user-usage/internal/db"
)

/*
 * 축 화이트리스트. 클라이언트가 보고하는 값이라 서버가 반드시 좁힌다 — 오타 하나가 집계 축을
 * 늘려 화면에 정체불명 행이 생기는 것을 막는다.
 */
var CounterKinds = []string{"tool", "bash", "slash", "skill", "agent", "mcp", "keyword"}

func isCounterKind(k string) bool {
	for _, v := range CounterKinds {
		if v == k {
			return true
		}
	}
	return false
}

const (
	// MaxCountersPerSession 은 한 세션이 보낼 수 있는 카운터 행 수 상한이다.
	// 키워드 축이 사실상 무제한이라 상한이 없으면 세션 하나가 테이블을 채운다.
	// 클라이언트도 자르지만 서버가 최종 방어선이다.
	MaxCountersPerSession = 400
	// MaxSeriesPerSession 은 세션당 시간 버킷 상한이다.
	MaxSeriesPerSession = 200

	keyMax = 120

	// 원행 조회구의 상한. limit 이 이 함수들의 안전장치다 — 상한 없이 부르면 언젠가 전량을
	// 메모리로 끌어온다.
	SessionRowsDefault = 5000
	SessionRowsMax     = 50000
	// 상한이 세션보다 큰 이유: 한 세션이 시간 버킷을 여럿 만든다(실측: 4일짜리 세션 하나가 24개).
	SeriesRowsDefault = 20000
	SeriesRowsMax     = 200000
)

// UnknownModel 은 모델을 모를 때의 키다. 빈 문자열 키를 만들지 않는다.
const UnknownModel = "(미상)"

// ErrNotInitialized 는 Init 전에 저장소를 쓴 것이다.
var ErrNotInitialized = errors.New("store: Init 되지 않았다")

// handle 은 이 패키지가 쓰는 DB 다. Init 이 건다.
//
// 왜 패키지 변수인가: CONTRACT 가 집계 함수를 `Totals(ctx)` 처럼 DB 인자 없이 동결했다.
// 현행 Node 모듈이 모듈 스코프 q() 를 쓰는 것과 같은 모양이고, 이 프로세스에 저장소는 하나다.
var handle db.DB

// conn 은 현재 DB 를 돌려준다. Init 전이면 오류다 — nil 역참조로 죽는 대신 말이 되는 실패를 준다.
func conn() (db.DB, error) {
	if handle == nil {
		return nil, ErrNotInitialized
	}
	return handle, nil
}

// clock 은 시간 의존을 주입 가능하게 둔 자리다. 테스트가 갈아 낀다.
var clock = func() time.Time { return time.Now().UTC() }

// nowISO 는 JS 의 `new Date().toISOString()` 과 **같은 문자열 모양**이다(밀리초 3자리 + Z).
// 골든이 이 값을 그대로 비교하므로 자릿수가 흔들리면 안 된다.
func nowISO() string { return clock().UTC().Format("2006-01-02T15:04:05.000Z07:00") }

/*
 * sqlite DDL. pg 는 migrations/pg/*.sql 이 스키마를 소유하므로 여기서 돌지 않는다
 * (방언마다 소유자가 하나다 — 둘이면 한쪽만 고쳐지는 날이 온다).
 *
 * Node 는 멀티 스테이트먼트 한 덩이를 execMulti 로 넣었지만, 여기서는 문장을 쪼개 둔다.
 * 어느 문장이 실패했는지가 오류에 그대로 남는다.
 */
var sqliteDDL = []string{
	`CREATE TABLE IF NOT EXISTS usage_sessions (
		session_id TEXT PRIMARY KEY,
		machine TEXT,
		username TEXT,
		project TEXT,
		model TEXT,
		-- 이 세션을 만든 도구(claude|codex|gemini). 미지정 보고는 claude 다 — 현행 수집기는
		-- 이 필드를 보내지 않으므로 DEFAULT 가 하위호환의 전부다(platform.go 주석).
		-- pg 는 migrations/pg/0035_platform.sql 이 같은 컬럼을 소유한다(양 방언 동기).
		platform TEXT NOT NULL DEFAULT 'claude',
		input INTEGER NOT NULL DEFAULT 0,
		output INTEGER NOT NULL DEFAULT 0,
		cache_read INTEGER NOT NULL DEFAULT 0,
		cache_create INTEGER NOT NULL DEFAULT 0,
		-- 계단(롱컨텍스트) 분리분 — 위 총량 **중** 롱 구간 요청에서 발생한 몫이다.
		-- 총량 컬럼의 의미는 바뀌지 않는다(여전히 전체 합계). 표준 몫 = 총량 − long.
		-- DEFAULT 0 이 하위호환의 전부다 — 구버전 수집기는 이 값을 안 보내고, 0 이면
		-- 전부 표준 구간이라 비용이 종전과 비트 동일하다.
		-- pg 는 migrations/pg/0036_long_context.sql 이 같은 컬럼을 소유한다(양 방언 동기).
		input_long INTEGER NOT NULL DEFAULT 0,
		output_long INTEGER NOT NULL DEFAULT 0,
		cache_read_long INTEGER NOT NULL DEFAULT 0,
		-- 고속 모드(fast mode) 분리분 — 총량의 부분집합이다(롱과 같은 계약).
		-- 고속은 같은 모델에 2배 단가가 붙는다(Opus 5 고속 $10/$50 vs 표준 $5/$25).
		-- pg 는 migrations/pg/0039_fast_mode.sql 이 같은 컬럼을 소유한다(양 방언 동기).
		input_fast INTEGER NOT NULL DEFAULT 0,
		output_fast INTEGER NOT NULL DEFAULT 0,
		cache_read_fast INTEGER NOT NULL DEFAULT 0,
		cache_create_fast INTEGER NOT NULL DEFAULT 0,
		web_search INTEGER NOT NULL DEFAULT 0,
		web_fetch INTEGER NOT NULL DEFAULT 0,
		turns INTEGER NOT NULL DEFAULT 0,
		started_at TEXT,
		ended_at TEXT,
		reported_at TEXT
	)`,
	`CREATE INDEX IF NOT EXISTS idx_usage_sessions_at ON usage_sessions(started_at)`,
	`CREATE INDEX IF NOT EXISTS idx_usage_sessions_user ON usage_sessions(username)`,

	// 시간 × 모델 버킷 — migrations/pg/0017_usage_series.sql 과 같은 모양이어야 한다.
	// PK 가 (세션, 시간, 모델)인 것이 멱등성의 근거다 — 절대값 UPSERT 로 덮어쓴다.
	`CREATE TABLE IF NOT EXISTS usage_series (
		session_id TEXT NOT NULL,
		hour TEXT NOT NULL,
		model TEXT NOT NULL,
		input INTEGER NOT NULL DEFAULT 0,
		output INTEGER NOT NULL DEFAULT 0,
		cache_read INTEGER NOT NULL DEFAULT 0,
		cache_create INTEGER NOT NULL DEFAULT 0,
		cc_5m INTEGER NOT NULL DEFAULT 0,
		cc_1h INTEGER NOT NULL DEFAULT 0,
		-- 계단(롱컨텍스트) 분리분 — usage_sessions 와 같은 계약이다(총량의 부분집합).
		input_long INTEGER NOT NULL DEFAULT 0,
		output_long INTEGER NOT NULL DEFAULT 0,
		cache_read_long INTEGER NOT NULL DEFAULT 0,
		-- 고속 모드 분리분 — 같은 계약. 세션 표에도 같은 넷이 있다.
		input_fast INTEGER NOT NULL DEFAULT 0,
		output_fast INTEGER NOT NULL DEFAULT 0,
		cache_read_fast INTEGER NOT NULL DEFAULT 0,
		cache_create_fast INTEGER NOT NULL DEFAULT 0,
		turns INTEGER NOT NULL DEFAULT 0,
		tool_errors INTEGER NOT NULL DEFAULT 0,
		stop_max_tokens INTEGER NOT NULL DEFAULT 0,
		stop_refusal INTEGER NOT NULL DEFAULT 0,
		latency_ms_sum INTEGER NOT NULL DEFAULT 0,
		latency_ms_max INTEGER NOT NULL DEFAULT 0,
		latency_turns INTEGER NOT NULL DEFAULT 0,
		username TEXT,
		machine TEXT,
		project TEXT,
		PRIMARY KEY (session_id, hour, model)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_usage_series_hour ON usage_series(hour)`,
	`CREATE INDEX IF NOT EXISTS idx_usage_series_model ON usage_series(model, hour)`,
	`CREATE INDEX IF NOT EXISTS idx_usage_series_user ON usage_series(username, hour)`,

	`CREATE TABLE IF NOT EXISTS usage_counters (
		session_id TEXT NOT NULL,
		kind TEXT NOT NULL,
		key TEXT NOT NULL,
		count INTEGER NOT NULL DEFAULT 0,
		day TEXT,
		username TEXT,
		machine TEXT,
		PRIMARY KEY (session_id, kind, key)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_usage_counters_kind ON usage_counters(kind, key)`,
	`CREATE INDEX IF NOT EXISTS idx_usage_counters_day ON usage_counters(day, kind)`,

	`CREATE TABLE IF NOT EXISTS usage_recommendations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		goal_tokens TEXT,
		agent TEXT,
		skills TEXT,
		score REAL,
		source TEXT,
		username TEXT,
		at TEXT
	)`,
	`CREATE INDEX IF NOT EXISTS idx_usage_reco_at ON usage_recommendations(at)`,
	`CREATE INDEX IF NOT EXISTS idx_usage_reco_score ON usage_recommendations(score)`,

	// 팀 멤버십(사용자→팀) — 팀별 롤업의 매핑. sqlite 는 단일 테넌트라 tenant_id 컬럼이 없다
	// (usage_sessions 와 같은 규율). pg 는 migrations/pg/0031 이 tenant_id + RLS 로 소유한다.
	`CREATE TABLE IF NOT EXISTS team_members (
		username TEXT PRIMARY KEY,
		team TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_team_members_team ON team_members(team)`,

	// 개인 열람 토큰(RBAC) — 한 토큰=한 사용자. 평문은 안 저장(해시만). sqlite 는 단일 테넌트라
	// tenant_id 없음(pg 는 migrations/pg/0032 소유).
	`CREATE TABLE IF NOT EXISTS member_tokens (
		token_hash TEXT PRIMARY KEY,
		username TEXT NOT NULL,
		created_at TEXT NOT NULL,
		revoked_at TEXT
	)`,
	`CREATE INDEX IF NOT EXISTS idx_member_tokens_user ON member_tokens(username)`,

	// 대시보드 사람 계정(ID/PW 로그인) — bcrypt 해시만 저장. sqlite 는 단일 테넌트라 tenant_id
	// 없음(pg 는 migrations/pg/0034 이 tenant_id + RLS 로 소유). 시간 컬럼은 text(RFC3339).
	`CREATE TABLE IF NOT EXISTS auth_users (
		username TEXT PRIMARY KEY,
		password_hash TEXT NOT NULL,
		role TEXT NOT NULL CHECK (role IN ('admin','member')),
		created_at TEXT NOT NULL
	)`,

	// 발급된 세션 — token_hash=sha256(평문). 평문은 쿠키에만 존재한다. expires_at 은 text(RFC3339
	// Zulu)라 사전식 비교가 곧 시간 비교다(migrations/pg/0034 과 같은 규율).
	`CREATE TABLE IF NOT EXISTS auth_sessions (
		token_hash TEXT PRIMARY KEY,
		username TEXT NOT NULL,
		role TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_auth_sessions_expiry ON auth_sessions(expires_at)`,
}

// Init 은 저장소를 이 DB 에 건다. sqlite 는 DDL 을 직접 걸어 **멱등하게** 초기화하고,
// pg 는 아무것도 하지 않는다(스키마는 migrations 소유이고 자동 실행 경로에 올리지 않는다).
func Init(ctx context.Context, d db.DB) error {
	if d == nil {
		return errors.New("store: DB 가 nil 이다")
	}
	handle = d
	if d.Dialect() != db.DialectSQLite {
		return nil
	}
	for _, stmt := range sqliteDDL {
		if err := d.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("store: DDL 실패(%.60s…): %w", stmt, err)
		}
	}
	/*
	 * 기존 DB 에 컬럼을 멱등 추가한다 — CREATE TABLE IF NOT EXISTS 는 **기존 테이블에 새 컬럼을
	 * 안 넣는다.** 이 두 컬럼은 나중에 생겼고, 없으면 관련 질의가 통째로 실패한다.
	 */
	if err := ensureColumn(ctx, d, "usage_sessions", "ended_at", "TEXT"); err != nil {
		return err
	}
	// pg 는 migrations/pg/0026_usage_no_ts_turns.sql 이 같은 컬럼을 소유한다(양 방언 동기).
	if err := ensureColumn(ctx, d, "usage_sessions", "no_ts_turns", "INTEGER"); err != nil {
		return err
	}
	// 개발 지표 컬럼(LOC·편집결정). pg 는 migrations/pg/0033_dev_metrics.sql 이 소유.
	for _, col := range []string{"lines_added", "lines_removed", "edits_accepted", "edits_rejected"} {
		if err := ensureColumn(ctx, d, "usage_sessions", col, "INTEGER"); err != nil {
			return err
		}
	}
	/*
	 * platform 축. pg 는 migrations/pg/0035_platform.sql 이 소유한다.
	 *
	 * NOT NULL DEFAULT 로 추가하므로 **기존 행이 전부 claude 로 채워진다** — 그것이 맞는 사실이다
	 * (지금까지 들어온 보고는 전부 Claude Code 수집기의 것이다).
	 */
	if err := ensureColumn(ctx, d, "usage_sessions", "platform", "TEXT NOT NULL DEFAULT 'claude'"); err != nil {
		return err
	}
	/*
	 * ⚠ 인덱스는 sqliteDDL 이 아니라 **여기**에 있다. 위 DDL 은 ensureColumn 보다 먼저 도는데,
	 * 옛 DB 에는 그 시점에 platform 컬럼이 아직 없어서 CREATE INDEX 가 통째로 실패한다
	 * (그러면 부팅이 죽는다). 컬럼 보강 뒤에 거는 것이 순서상 유일하게 맞는 자리다.
	 */
	if err := d.Exec(ctx,
		"CREATE INDEX IF NOT EXISTS idx_usage_sessions_platform ON usage_sessions(platform)"); err != nil {
		return fmt.Errorf("store: platform 인덱스 생성 실패: %w", err)
	}
	/*
	 * 계단(롱컨텍스트) 분리분. pg 는 migrations/pg/0036_long_context.sql 이 소유한다.
	 *
	 * **두 표 모두**에 넣는다. 한쪽만 넣으면 그 표를 근거로 삼는 화면만 계단을 안 타고, 같은
	 * 데이터의 비용이 화면마다 달라진다(세션 뷰 ↔ 시간 뷰).
	 *
	 * NOT NULL DEFAULT 0 으로 추가하므로 기존 행은 전부 "전부 표준 구간"이 된다 — 그것이 맞는
	 * 사실이다. 지금까지의 보고에는 롱 분리분이 아예 없었고, 여기서 NULL 을 허용하면
	 * "모른다"가 생겨 총량과 분리분의 관계(표준 = 총량 − long)가 깨진다.
	 */
	for _, table := range []string{"usage_sessions", "usage_series"} {
		for _, col := range []string{
			"input_long", "output_long", "cache_read_long",
			// 고속 모드 분리분(0039). 기존 DB 에 없으면 여기서 붙는다 — DEFAULT 0 이라
			// 붙는 순간의 기존 행은 "전부 표준 속도"가 되고 비용이 종전과 같다.
			"input_fast", "output_fast", "cache_read_fast", "cache_create_fast",
		} {
			if err := ensureColumn(ctx, d, table, col, "INTEGER NOT NULL DEFAULT 0"); err != nil {
				return err
			}
		}
	}
	return nil
}

// ensureColumn 은 sqlite 전용 멱등 컬럼 추가다.
func ensureColumn(ctx context.Context, d db.DB, table, column, decl string) error {
	rows, err := d.Query(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return fmt.Errorf("store: %s 컬럼 조회 실패: %w", table, err)
	}
	for _, r := range rows {
		if strings.EqualFold(r.Str("name"), column) {
			return nil
		}
	}
	if err := d.Exec(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, decl)); err != nil {
		return fmt.Errorf("store: %s.%s 추가 실패: %w", table, column, err)
	}
	return nil
}

// ── 작은 순수 헬퍼 ────────────────────────────────────────────────────

// clip 은 문자열을 n 자로 자른다.
//
// **룬 단위**다. JS 의 slice 는 UTF-16 코드 단위를 세는데, 이 레포의 키는 한글·ASCII 라
// (BMP 문자) 룬 수와 코드 단위 수가 같다. 바이트로 자르면 한글이 중간에서 잘려 깨진다.
func clip(s string, n int) string {
	if len(s) <= n { // 바이트 길이가 이미 짧으면 룬 수는 그보다 작거나 같다
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// clipTrim 은 앞뒤 공백을 떼고 자른다(identity 계열의 clip 과 같은 규칙).
func clipTrim(s string, n int) string { return clip(strings.TrimSpace(s), n) }

// nonNeg 는 음수를 0 으로 접는다(Node 의 int(): 유한하고 0 초과일 때만 그 값).
func nonNeg(v int64) int64 {
	if v > 0 {
		return v
	}
	return 0
}

// nullStr 은 빈 문자열을 SQL NULL 로 넘긴다.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

var dayPrefixRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}`)
var dayExactRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
var hourRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}$`)

// dayOf 는 ISO 시각에서 날짜를 뽑는다. 형식이 아니면 오늘이다.
func dayOf(iso string) string {
	if dayPrefixRe.MatchString(iso) {
		return iso[:10]
	}
	return nowISO()[:10]
}

// isDay 는 'YYYY-MM-DD' 인지 본다. 형식이 아닌 값은 필터에서 **무시한다** —
// 경계가 깨진 채로 조회되느니 없는 편이 낫다.
func isDay(s string) bool { return dayExactRe.MatchString(s) }

// clampInt 는 Node 의 `Math.max(lo, Math.min(hi, Math.floor(Number(v)) || dflt))` 를 그대로 옮긴다.
//
// ⚠ `|| dflt` 자리가 미묘하다. v==0 이면 기본값으로 가고, v<0 이면 기본값이 아니라 **lo** 로 간다.
// "같은 뜻의 다른 식"으로 바꾸면 경계에서 갈린다.
func clampInt(v, lo, hi, dflt int) int {
	if v == 0 {
		v = dflt
	}
	if v > hi {
		v = hi
	}
	if v < lo {
		v = lo
	}
	return v
}

// conflictTarget 은 방언별 UPSERT 충돌 대상이다.
// pg 는 PK 에 tenant_id 가 들어 있어 목록이 다르다(migrations 가 단일 출처).
func conflictTarget(d db.DB, sqliteCols, pgCols string) string {
	if d.Dialect() == db.DialectPostgres {
		return pgCols
	}
	return sqliteCols
}
