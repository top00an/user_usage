package store

import (
	"context"
	"strings"

	"github.com/tscorp/user-usage/internal/db"
)

/*
 * ── 원행 조회구 ───────────────────────────────────────────────────────
 *
 * 왜 집계된 뷰가 아니라 원행인가: 분포(p50/p95/p99)와 비용은 **세션 단위 값**이 있어야 계산된다.
 * SUM 으로 접힌 뒤에는 복원할 수 없다. 그래서 여기서는 접지 않고 넘기고, 접는 일은 호출부가
 * 한다(stats 는 분포, cost 는 비용).
 *
 * ⚠ **limit 이 이 함수들의 안전장치다.** 세션 수가 계속 늘어나므로 상한 없이 부르면 언젠가
 *   전량을 메모리로 끌어온다. 넘칠 때는 **최신 것부터** 자른다(오래된 표본을 버리는 쪽이 분포
 *   왜곡이 적다). 호출부는 반환 길이가 limit 과 같으면 표본이 잘렸다고 보고 화면에 그 사실을
 *   표시해야 한다.
 *
 * 필터는 **값만 바인딩한다** — 컬럼명·정렬축을 문자열로 이어 붙이지 않는다(주입 표면 0).
 */

const sessionSelectCols = "session_id, machine, username, project, model, platform, input, output, cache_read," +
	" cache_create, input_long, output_long, cache_read_long, cache_create_long," +
	" input_fast, output_fast, cache_read_fast, cache_create_fast," +
	" web_search, web_fetch, turns, started_at, ended_at, reported_at"

/*
 * sessionWhere 는 usage_sessions 필터를 한곳에서 만든다(SessionRows·PlatformRollup 공용).
 *
 * 두 벌로 두지 않는 이유: 날짜 경계 규칙(접두 10자 비교)이 미묘해서, 한쪽만 고쳐지면 같은
 * 화면의 두 카드가 서로 다른 하루를 세게 된다.
 *
 * ⚠ **값만 바인딩한다** — 컬럼명·정렬축을 문자열로 이어 붙이지 않으므로 주입 표면이 0 이다.
 */
func sessionWhere(f Filter) ([]string, []any) { return sessionWhereOn(f, "") }

/*
 * sessionWhereOn 은 sessionWhere 와 **같은 규칙**을 별칭 붙은 컬럼으로 만든다.
 *
 * 별칭 인자를 둔 이유: 집계 질의는 usage_sessions 를 `s` 로 별칭 붙여 다른 표와 조인한다
 * (UsageByModel·UsageModelAxis). 거기에 무별칭 조건을 그대로 넣으면 컬럼이 어느 쪽 것인지
 * 방언마다 다르게 해석될 여지가 생긴다. 규칙은 여전히 한 벌이다 — 붙이는 이름만 인자다.
 *
 * alias 는 **이 패키지의 상수 문자열만** 넘긴다(요청 값이 닿지 않는다 — 주입 표면 0).
 */
func sessionWhereOn(f Filter, alias string) ([]string, []any) {
	col := func(name string) string {
		if alias == "" {
			return name
		}
		return alias + "." + name
	}
	var where []string
	var args []any
	/*
	 * 날짜 경계는 **접두 10자 비교**로 한다(UsageByDay 와 같은 관용구).
	 * started_at 이 ISO 라 `started_at <= '2026-08-03'` 은 그날 00:00 이후를 전부 놓친다 —
	 * 하루치를 조용히 잘라먹는 자리라 상한/하한을 같은 방식으로 맞춘다.
	 */
	if isDay(f.From) {
		where = append(where, "substr("+col("started_at")+",1,10) >= ?")
		args = append(args, f.From)
	}
	if isDay(f.To) {
		where = append(where, "substr("+col("started_at")+",1,10) <= ?")
		args = append(args, f.To)
	}
	if f.Username != "" {
		where = append(where, col("username")+" = ?")
		args = append(args, f.Username)
	}
	if f.Model != "" {
		where = append(where, col("model")+" = ?")
		args = append(args, f.Model)
	}
	// 미지정이면 조건 자체가 없다 = 전체(현행과 같은 동작). 이 한 줄이 무회귀의 근거다.
	if f.Platform != "" {
		where = append(where, col("platform")+" = ?")
		args = append(args, f.Platform)
	}
	return where, args
}

/*
 * platformSessionCond 는 **세션이 아닌 표**(usage_series·usage_counters)의 행을 세션으로 되짚어
 * 플랫폼으로 거른다.
 *
 * 그 표들에는 platform 컬럼이 없다(같은 사실을 두 테이블에 적지 않는다 — platform.go 주석).
 * 이 조건을 빼면 `platform=codex` 요청이 조용히 전 플랫폼을 돌려주는데, 그게 이 축에서 가장
 * 나쁜 실패다(요청과 다른 모집단을 요청한 이름으로 보여준다).
 *
 * childSessionCol 은 그 표의 session_id 참조식이다 — 바깥 질의가 별칭을 쓰면 별칭째로 준다.
 * 별칭을 `ps` 로 고정한 이유: 호출 지점들이 이미 `s`·`x` 를 쓰고 있어 이름이 겹치면 상관
 * 서브쿼리가 바깥 행을 보게 된다(문법 오류도 안 나고 값만 틀린다).
 *
 * ⚠ **플랫폼만 본다.** 기간·사용자 축까지 여기서 걸면 usage_series 의 hour·username 으로 이미
 *   거른 것과 이중으로 걸려, series·quality 의 현행 모집단이 조용히 달라진다(골든 44개).
 */
func platformSessionCond(f Filter, childSessionCol string) (string, []any) {
	if f.Platform == "" {
		return "", nil
	}
	return "EXISTS (SELECT 1 FROM usage_sessions ps WHERE ps.session_id = " + childSessionCol +
		" AND ps.platform = ?)", []any{f.Platform}
}

// seriesPlatformCond 는 시간 버킷(usage_series)을 플랫폼으로 거르는 조건이다.
func seriesPlatformCond(f Filter) (string, []any) {
	return platformSessionCond(f, "usage_series.session_id")
}

/*
 * counterPlatformCond 는 축 카운터(usage_counters)를 같은 규율로 거른다.
 *
 * 세션 행이 없는 **고아 카운터**는 필터가 걸리면 빠진다. 그것이 맞다 — 귀속을 모르는 것을
 * 특정 플랫폼의 사실로 지어내지 않는다. 미지정일 때는 종전대로 센다(조건 자체가 없다).
 */
func counterPlatformCond(f Filter) (string, []any) {
	return platformSessionCond(f, "usage_counters.session_id")
}

/*
 * ── 사용자 축 조건 ────────────────────────────────────────────────────────
 *
 * 플랫폼과 달리 **세션으로 되짚지 않는다.** usage_counters·usage_recommendations 에는
 * username 컬럼이 직접 있다(스키마 참조 — 같은 사실을 두 번 적는 것이 아니라, 이 표들은
 * 세션 없이도 사람에게 귀속된다). 그래서 EXISTS 서브쿼리가 필요 없고, 세션 행이 사라진
 * 고아 카운터도 사람 기준으로는 정확히 센다.
 *
 * ⚠ 이것을 platformSessionCond 안에 넣지 않는 이유는 그 함수 주석이 이미 말한다: 거기에
 *   사용자 축을 얹으면 usage_series 의 username 으로 이미 거른 것과 이중으로 걸려
 *   series·quality 의 현행 모집단이 조용히 달라진다(골든 44개). 규칙은 나란히 둔다.
 *
 * col 은 **이 패키지의 상수 문자열만** 넘긴다(요청 값이 닿지 않는다 — 주입 표면 0).
 */
func userCondOn(f Filter, col string) (string, []any) {
	if f.Username == "" {
		return "", nil
	}
	return col + " = ?", []any{f.Username}
}

// counterUserCond 는 축 카운터(usage_counters)를 사람으로 거른다.
func counterUserCond(f Filter) (string, []any) {
	return userCondOn(f, "usage_counters.username")
}

// recoUserCond 는 추천 관측(usage_recommendations)을 사람으로 거른다.
func recoUserCond(f Filter) (string, []any) {
	return userCondOn(f, "usage_recommendations.username")
}

// strOrNil 은 빈 값을 nil 로 남긴다 — 안 보낸 값을 "" 로 지어내지 않는다.
func strOrNil(r db.Row, col string) *string {
	if r.IsNull(col) {
		return nil
	}
	s := r.Str(col)
	if s == "" {
		return nil
	}
	return &s
}

func mapSession(r db.Row) Session {
	return Session{
		SessionID:   r.Str("session_id"),
		Machine:     r.Str("machine"),
		Username:    r.Str("username"),
		Project:     r.Str("project"),
		Model:       r.Str("model"),
		Platform:    r.Str("platform"),
		Input:       r.Int("input"),
		Output:      r.Int("output"),
		CacheRead:   r.Int("cache_read"),
		CacheCreate: r.Int("cache_create"),
		// 계단 분리분 — 비용 계산이 이 몫만 상위 단가로 매긴다. 안 올리면 이 화면만 계단을
		// 안 타고, 그 사실이 어디에도 표시되지 않는다.
		InputLong:       r.Int("input_long"),
		CacheCreateLong: r.Int("cache_create_long"),
		InputFast:       r.Int("input_fast"),
		OutputFast:      r.Int("output_fast"),
		CacheReadFast:   r.Int("cache_read_fast"),
		CacheCreateFast: r.Int("cache_create_fast"),
		OutputLong:      r.Int("output_long"),
		CacheReadLong:   r.Int("cache_read_long"),
		WebSearch:       r.Int("web_search"),
		WebFetch:        r.Int("web_fetch"),
		Turns:           r.Int("turns"),
		StartedAt:       strOrNil(r, "started_at"),
		EndedAt:         strOrNil(r, "ended_at"),
		ReportedAt:      strOrNil(r, "reported_at"),
	}
}

// SessionRows 는 세션 원행이다 — 분포·비용·드릴다운이 공유하는 단일 조회구.
func SessionRows(ctx context.Context, f Filter) ([]Session, error) {
	d, err := conn()
	if err != nil {
		return nil, err
	}
	lim := clampInt(f.Limit, 1, SessionRowsMax, SessionRowsDefault)

	where, args := sessionWhere(f)
	sql := "SELECT " + sessionSelectCols + " FROM usage_sessions"
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	sql += " ORDER BY started_at DESC LIMIT ?"
	args = append(args, lim)

	rows, err := d.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	out := make([]Session, 0, len(rows))
	for _, r := range rows {
		out = append(out, mapSession(r))
	}
	return out, nil
}

// SessionByID 는 세션 하나다. 드릴다운이 전량을 끌어와 훑지 않게 하는 자리다.
// 없으면 (nil, nil).
func SessionByID(ctx context.Context, sessionID string) (*Session, error) {
	d, err := conn()
	if err != nil {
		return nil, err
	}
	if sessionID == "" {
		return nil, nil
	}
	r, err := d.QueryRow(ctx,
		"SELECT "+sessionSelectCols+" FROM usage_sessions WHERE session_id = ?", sessionID)
	if err != nil || r == nil {
		return nil, err
	}
	s := mapSession(r)
	return &s, nil
}

const seriesSelectCols = "session_id, hour, model, input, output, cache_read, cache_create," +
	" input_long, output_long, cache_read_long, cache_create_long," +
	" input_fast, output_fast, cache_read_fast, cache_create_fast," +
	" cc_5m, cc_1h, turns, tool_errors, stop_max_tokens, stop_refusal," +
	" latency_ms_sum, latency_ms_max, latency_turns, username, machine, project"

func mapBucket(r db.Row) Bucket {
	return Bucket{
		SessionID:   r.Str("session_id"),
		Hour:        r.Str("hour"),
		Model:       r.Str("model"),
		Input:       r.Int("input"),
		Output:      r.Int("output"),
		CacheRead:   r.Int("cache_read"),
		CacheCreate: r.Int("cache_create"),
		// cost 가 기대하는 이름으로 올린다 — TTL 분해가 있으면 그쪽이 정확히 가격을 매긴다.
		CacheCreate5m: r.Int("cc_5m"),
		CacheCreate1h: r.Int("cc_1h"),
		// 계단 분리분 — 시간 뷰의 비용이 이 몫을 상위 단가로 매긴다.
		InputLong:       r.Int("input_long"),
		CacheCreateLong: r.Int("cache_create_long"),
		InputFast:       r.Int("input_fast"),
		OutputFast:      r.Int("output_fast"),
		CacheReadFast:   r.Int("cache_read_fast"),
		CacheCreateFast: r.Int("cache_create_fast"),
		OutputLong:      r.Int("output_long"),
		CacheReadLong:   r.Int("cache_read_long"),
		Turns:           r.Int("turns"),
		ToolErrors:      r.Int("tool_errors"),
		StopMaxTokens:   r.Int("stop_max_tokens"),
		StopRefusal:     r.Int("stop_refusal"),
		LatencyMsSum:    r.Int("latency_ms_sum"),
		LatencyMsMax:    r.Int("latency_ms_max"),
		LatencyTurns:    r.Int("latency_turns"),
		Username:        r.Str("username"),
		Machine:         r.Str("machine"),
		Project:         r.Str("project"),
	}
}

/*
 * SeriesRows — 시간 버킷 조회. 시간 시계열의 원자료.
 *
 * ⚠ 경계 비교가 SessionRows 와 **다르다**. hour 는 'YYYY-MM-DDTHH' 라 앞 10자가 곧 날짜이므로
 * substr(hour,1,10) 으로 날짜 경계를 잡는다. SessionRows 의 관용구를 컬럼 이름만 바꿔 옮기면
 * 안 되는 자리다.
 */
func SeriesRows(ctx context.Context, f Filter) ([]Bucket, error) {
	d, err := conn()
	if err != nil {
		return nil, err
	}
	lim := clampInt(f.Limit, 1, SeriesRowsMax, SeriesRowsDefault)

	var where []string
	var args []any
	if isDay(f.From) {
		where = append(where, "substr(hour,1,10) >= ?")
		args = append(args, f.From)
	}
	if isDay(f.To) {
		where = append(where, "substr(hour,1,10) <= ?")
		args = append(args, f.To)
	}
	if f.Username != "" {
		where = append(where, "username = ?")
		args = append(args, f.Username)
	}
	if f.Model != "" {
		where = append(where, "model = ?")
		args = append(args, f.Model)
	}
	if cond, cargs := seriesPlatformCond(f); cond != "" {
		where = append(where, cond)
		args = append(args, cargs...)
	}
	sql := "SELECT " + seriesSelectCols + " FROM usage_series"
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	sql += " ORDER BY hour DESC LIMIT ?"
	args = append(args, lim)

	rows, err := d.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	out := make([]Bucket, 0, len(rows))
	for _, r := range rows {
		out = append(out, mapBucket(r))
	}
	return out, nil
}

// SeriesOf 는 한 세션의 시간 버킷이다 — 드릴다운에서 "이 세션이 몇 시에 무엇을 썼나".
func SeriesOf(ctx context.Context, sessionID string) ([]Bucket, error) {
	d, err := conn()
	if err != nil {
		return nil, err
	}
	if sessionID == "" {
		return []Bucket{}, nil
	}
	rows, err := d.Query(ctx,
		"SELECT "+seriesSelectCols+" FROM usage_series WHERE session_id = ? ORDER BY hour ASC, model ASC",
		sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]Bucket, 0, len(rows))
	for _, r := range rows {
		out = append(out, mapBucket(r))
	}
	return out, nil
}

/*
 * SeriesQualityTotals — 품질축 집계. usage_series 전량 SUM(행 제한 없이).
 *
 * 도구 오류·거부·최대토큰중단·지연을 기간 전체로 합산한다. 신수집기만 시간 버킷을 보내므로
 * SessionsWithSeries 로 커버리지를 함께 내보내 화면이 "이 지표는 세션 N/M 만 덮는다"고
 * 정직하게 말하게 한다.
 */
func SeriesQualityTotals(ctx context.Context, f Filter) (QualityTotals, error) {
	var out QualityTotals
	d, err := conn()
	if err != nil {
		return out, err
	}
	var where []string
	var args []any
	if isDay(f.From) {
		where = append(where, "substr(hour,1,10) >= ?")
		args = append(args, f.From)
	}
	if isDay(f.To) {
		where = append(where, "substr(hour,1,10) <= ?")
		args = append(args, f.To)
	}
	if f.Username != "" {
		where = append(where, "username = ?")
		args = append(args, f.Username)
	}
	if cond, cargs := seriesPlatformCond(f); cond != "" {
		where = append(where, cond)
		args = append(args, cargs...)
	}
	w := ""
	if len(where) > 0 {
		w = " WHERE " + strings.Join(where, " AND ")
	}
	r, err := d.QueryRow(ctx,
		"SELECT COUNT(DISTINCT session_id) sess, COALESCE(SUM(turns),0) turns,"+
			" COALESCE(SUM(tool_errors),0) te, COALESCE(SUM(stop_max_tokens),0) smt,"+
			" COALESCE(SUM(stop_refusal),0) ref, COALESCE(SUM(latency_ms_sum),0) lsum,"+
			" COALESCE(MAX(latency_ms_max),0) lmax, COALESCE(SUM(latency_turns),0) lturns"+
			" FROM usage_series"+w, args...)
	if err != nil || r == nil {
		return out, err
	}
	return QualityTotals{
		SessionsWithSeries: int(r.Int("sess")),
		Turns:              r.Int("turns"),
		ToolErrors:         r.Int("te"),
		StopMaxTokens:      r.Int("smt"),
		StopRefusal:        r.Int("ref"),
		LatencyMsSum:       r.Int("lsum"),
		LatencyMsMax:       r.Int("lmax"),
		LatencyTurns:       r.Int("lturns"),
	}, nil
}

// CountersOf 는 한 세션의 축별 카운터다 — 드릴다운(세션 상세)의 본문.
func CountersOf(ctx context.Context, sessionID string, kinds []string) (map[string][]KeyCount, error) {
	out := map[string][]KeyCount{}
	d, err := conn()
	if err != nil {
		return out, err
	}
	if kinds == nil {
		kinds = CounterKinds
	}
	allow := make([]string, 0, len(kinds))
	for _, k := range kinds {
		if isCounterKind(k) {
			allow = append(allow, k)
		}
	}
	if sessionID == "" || len(allow) == 0 {
		return out, nil
	}
	marks := make([]string, len(allow))
	args := make([]any, 0, len(allow)+1)
	args = append(args, sessionID)
	for i, k := range allow {
		marks[i] = "?"
		args = append(args, k)
	}
	rows, err := d.Query(ctx,
		"SELECT kind, key, count FROM usage_counters WHERE session_id = ? AND kind IN ("+
			strings.Join(marks, ",")+") ORDER BY count DESC", args...)
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		kind := r.Str("kind")
		out[kind] = append(out[kind], KeyCount{Key: r.Str("key"), Count: r.Int("count")})
	}
	return out, nil
}
