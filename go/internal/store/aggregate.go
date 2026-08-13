package store

import (
	"context"
	"sort"
	"strings"

	"github.com/tscorp/user-usage/internal/db"
)

/*
 * ── 필터 있는 집계 (…WithFilter) ──────────────────────────────────────
 *
 * CONTRACT.md 가 동결한 시그니처(`Totals(ctx)` 처럼 인자 없는 집계)는 **그대로 남긴다.**
 * 필터를 받는 쪽을 `…WithFilter` 로 나란히 두고, 기존 함수는 빈 필터로 부르는 얇은 껍데기가 된다.
 *
 * 왜 인자를 늘리지 않는가: 그 함수들은 이 레포 밖(CONTRACT.md)이 소유한 표면이고, 호출부가
 * 여럿이다. 시그니처를 바꾸면 응답 shape 는 그대로여도 남의 컴파일이 이유 없이 깨진다.
 *
 * **빈 필터는 조건을 하나도 만들지 않는다** — 생성되는 SQL 이 종전과 같은 문자열이다.
 * 그것이 골든 44개 무회귀의 근거다.
 */

// Totals 는 전체 규모다.
func Totals(ctx context.Context) (TotalsResult, error) { return TotalsWithFilter(ctx, Filter{}) }

// TotalsWithFilter 는 필터가 걸린 전체 규모다(sessionWhere 와 같은 한 벌의 규칙).
func TotalsWithFilter(ctx context.Context, f Filter) (TotalsResult, error) {
	var out TotalsResult
	d, err := conn()
	if err != nil {
		return out, err
	}
	sql := "SELECT COUNT(*) n, SUM(input) i, SUM(output) o, SUM(cache_read) cr, SUM(cache_create) cc," +
		" COUNT(DISTINCT username) u, COUNT(DISTINCT machine) m FROM usage_sessions"
	where, args := sessionWhere(f)
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	r, err := d.QueryRow(ctx, sql, args...)
	if err != nil {
		return out, err
	}
	if r == nil {
		return out, nil
	}
	return TotalsResult{
		Sessions:    int(r.Int("n")),
		Input:       r.Int("i"),
		Output:      r.Int("o"),
		CacheRead:   r.Int("cr"),
		CacheCreate: r.Int("cc"),
		Users:       int(r.Int("u")),
		Machines:    int(r.Int("m")),
	}, nil
}

// UsageByDay 는 일별 토큰 사용량이다 — 비용 추세.
// 캐시 읽기를 반드시 따로 돌려준다(합치면 비용이 왜곡된다).
func UsageByDay(ctx context.Context, days int) ([]DayRow, error) {
	return UsageByDayWithFilter(ctx, days, Filter{})
}

// UsageByDayWithFilter 는 필터가 걸린 일별 사용량이다.
func UsageByDayWithFilter(ctx context.Context, days int, f Filter) ([]DayRow, error) {
	d, err := conn()
	if err != nil {
		return nil, err
	}
	lim := clampInt(days, 1, 365, 30)
	// `started_at IS NOT NULL` 은 필터가 아니라 이 집계의 전제다(날짜 칸이 없는 행은 놓을 자리가
	// 없다). 그래서 항상 맨 앞에 두고, 필터는 뒤에 AND 로 붙인다.
	where, args := sessionWhere(f)
	where = append([]string{"started_at IS NOT NULL"}, where...)
	args = append(args, lim)
	rows, err := d.Query(ctx,
		"SELECT substr(started_at,1,10) d, SUM(input) i, SUM(output) o, SUM(cache_read) cr,"+
			" SUM(cache_create) cc, COUNT(*) n FROM usage_sessions"+
			" WHERE "+strings.Join(where, " AND ")+" GROUP BY d ORDER BY d DESC LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	out := make([]DayRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, DayRow{
			Day: r.Str("d"), Input: r.Int("i"), Output: r.Int("o"),
			CacheRead: r.Int("cr"), CacheCreate: r.Int("cc"), Sessions: int(r.Int("n")),
		})
	}
	return out, nil
}

// UsageByUser 는 사람별 사용량이다 — 좌석·비용 배분의 근거.
func UsageByUser(ctx context.Context) ([]UserRow, error) {
	return UsageByUserWithFilter(ctx, Filter{})
}

// UsageByUserWithFilter 는 필터가 걸린 사람별 사용량이다.
func UsageByUserWithFilter(ctx context.Context, f Filter) ([]UserRow, error) {
	d, err := conn()
	if err != nil {
		return nil, err
	}
	sql := "SELECT COALESCE(NULLIF(username,''),'" + UnknownModel + "') u, SUM(input) i, SUM(output) o," +
		" SUM(cache_read) cr, SUM(cache_create) cc, SUM(turns) t, COUNT(*) n" +
		" FROM usage_sessions"
	where, args := sessionWhere(f)
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	rows, err := d.Query(ctx, sql+" GROUP BY u ORDER BY o DESC", args...)
	if err != nil {
		return nil, err
	}
	out := make([]UserRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, UserRow{
			Username: r.Str("u"), Input: r.Int("i"), Output: r.Int("o"),
			CacheRead: r.Int("cr"), CacheCreate: r.Int("cc"),
			Turns: r.Int("t"), Sessions: int(r.Int("n")),
		})
	}
	return out, nil
}

/*
 * ReporterCoverage — 발신처(머신)별 마지막 보고 시각. "왜 데이터가 없나"를 화면이 답하게 한다.
 *
 * 신수집기(시간 버킷=usage_series)를 보내는 머신 집합도 함께 표시해 품질축 커버리지를 설명한다.
 * machine→username 은 identity 매핑으로 사실상 1:1 이라 대표값 하나(MAX)로 충분하다.
 */
func ReporterCoverage(ctx context.Context) ([]Reporter, error) {
	return ReporterCoverageWithFilter(ctx, Filter{})
}

/*
 * ReporterCoverageWithFilter 는 필터가 걸린 발신처 목록이다.
 *
 * 사용자 축이 여기 닿아야 하는 이유: "이 사람의 PC 들이 언제 마지막으로 보고했나"가 곧
 * 개인 화면의 "왜 내 데이터가 없나"에 대한 답이다. 전사 목록만 주면 그 답을 사람이 직접
 * 골라내야 한다.
 *
 * ⚠ 두 번째 질의(usage_series 를 보내는 머신 집합)에도 **같은 조건**을 건다. 한쪽만 걸면
 *   거른 목록에 안 거른 SendsSeries 가 붙어, 남의 머신이 series 를 보낸다는 사실이 이 사람의
 *   행으로 새어 들어온다.
 */
func ReporterCoverageWithFilter(ctx context.Context, f Filter) ([]Reporter, error) {
	d, err := conn()
	if err != nil {
		return nil, err
	}
	where, args := sessionWhere(f)
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}
	rows, err := d.Query(ctx,
		"SELECT COALESCE(NULLIF(machine,''),'"+UnknownModel+"') m, MAX(COALESCE(username,'')) u,"+
			" COUNT(*) n, MAX(reported_at) last_rep, MAX(started_at) last_start"+
			" FROM usage_sessions"+cond+" GROUP BY m ORDER BY last_rep DESC", args...)
	if err != nil {
		return nil, err
	}
	// usage_series 에는 platform 컬럼이 없다 — 사용자 축만 직접 걸 수 있다(그 표의 username).
	sCond, sArgs := "", []any{}
	if f.Username != "" {
		sCond = " WHERE username = ?"
		sArgs = append(sArgs, f.Username)
	}
	seriesRows, err := d.Query(ctx,
		"SELECT DISTINCT COALESCE(NULLIF(machine,''),'"+UnknownModel+"') m FROM usage_series"+sCond, sArgs...)
	if err != nil {
		return nil, err
	}
	withSeries := make(map[string]bool, len(seriesRows))
	for _, r := range seriesRows {
		withSeries[r.Str("m")] = true
	}
	out := make([]Reporter, 0, len(rows))
	for _, r := range rows {
		m := r.Str("m")
		out = append(out, Reporter{
			Machine: m, Username: r.Str("u"), Sessions: int(r.Int("n")),
			LastReportedAt: r.Str("last_rep"), LastStartedAt: r.Str("last_start"),
			SendsSeries: withSeries[m],
		})
	}
	return out, nil
}

/*
 * ── 모델별 집계 ───────────────────────────────────────────────────────
 *
 * ⚠ `usage_sessions.model` 은 모델 축이 **아니다** — 그 세션에서 가장 많이 쓴 모델 1개다
 * (수집기의 turn-argmax). 그것으로 GROUP BY 하면 모델이 섞인 세션의 토큰이 통째로 한 칸에
 * 들어간다. 실측(한 사용자 1,133세션): opus-4-8 행이 output +313,633 을 얻고 fable-5·sonnet-5·
 * opus-5 가 정확히 그만큼 잃었다. **총합은 맞는데 행이 틀린 것**이라 사람에게는 "서버가 더
 * 최신인데 값이 적다"(=유실)로 보였다. 유실이 아니다.
 *
 * 그래서 축을 셋으로 갈라 더한다:
 *
 *	① series 가 있는 세션 → usage_series(PK 에 model 이 있다)의 모델별 **정확한** 값.
 *	   과거분까지 소급 교정된다 — 새 수집이 필요 없다(PruneSeries 는 호출부가 없어 온전하다).
 *	② series 가 **없는** 세션 → 종전대로 세션 최빈 모델에 귀속한다. **버리지 않는다.**
 *	③ ①의 세션 중 series 가 덮지 못한 잔여(시각이 파싱되지 않아 버킷이 안 생긴 턴) → 역시
 *	   최빈 모델에 귀속한다.
 *
 * ②를 버리지 않는 이유: series 커버리지가 사람마다 다르다(실측: 91.3% · 100% · **2.2%**).
 * series 로 갈아타기만 하면 커버리지 2.2% 인 사람의 모델별이 97.8% 사라진다 — 오귀속을
 * 고치면서 더 큰 거짓말을 만드는 셈이다. 그리고 짧은 세션은 대개 단일 모델이라(중앙값 2턴)
 * 그 귀속이 틀렸다는 보장도 없다.
 *
 * ③이 없으면 **총합이 줄어든다.** ①+②+③ = usage_sessions 총합이어야 한다 — 같은 화면의
 * totals·사용자별 카드가 그 값이고, 모델별만 작으면 그게 다시 "유실"로 읽힌다.
 *
 * 그리고 ②③의 몫을 FromSession 으로 **밝힌다**(①은 FromSeries). 밝히지 않으면 근사를
 * 정확한 값으로 위장하는 것이고, 그게 이번 결함의 본질이었다.
 */

// seriesPerSession 은 세션당 series 합계다. ①의 잔여(③)와 커버리지를 같은 정의로 계산하려고
// 한 곳에 둔다 — 두 곳에 쓰면 한쪽만 고쳐지는 날이 온다.
const seriesPerSession = "SELECT session_id sid, SUM(input) i, SUM(output) o," +
	" SUM(cache_read) cr, SUM(cache_create) cc FROM usage_series GROUP BY 1"

func addShare(dst *Share, r db.Row) {
	dst.Input += r.Int("i")
	dst.Output += r.Int("o")
	dst.CacheRead += r.Int("cr")
	dst.CacheCreate += r.Int("cc")
}

// UsageByModel 은 세 경로를 더한 모델별 집계다.
func UsageByModel(ctx context.Context) ([]ModelRow, error) {
	return UsageByModelWithFilter(ctx, Filter{})
}

/*
 * UsageByModelWithFilter 는 필터가 걸린 모델별 집계다.
 *
 * ⚠ **세 경로에 같은 필터를 걸어야 한다.** 한 경로만 걸면 `①+②+③ == Totals` 불변식이 깨져
 *   모델별만 작아지고, 사람에게는 "유실"로 보인다(이 레포에서 실제로 일어났던 결함).
 *   ①은 usage_series 라 platform 컬럼이 없으므로 세션으로 되짚는다(platformSessionCond).
 */
func UsageByModelWithFilter(ctx context.Context, f Filter) ([]ModelRow, error) {
	d, err := conn()
	if err != nil {
		return nil, err
	}

	/*
	 * ① series 의 모델별 정확값. 세션 행이 없는 **고아 버킷은 제외한다** — 그것까지 더하면
	 * 총합이 usage_sessions 를 넘어서 이 화면 안에서 두 카드가 어긋난다. 라이브에 실재하는
	 * 조건이다(인테이크가 세션 행만 실패하고 버킷은 들어가는 자리).
	 */
	exactWhere := []string{"EXISTS (SELECT 1 FROM usage_sessions s WHERE s.session_id = x.session_id)"}
	var exactArgs []any
	if cond, cargs := platformSessionCond(f, "x.session_id"); cond != "" {
		exactWhere = append(exactWhere, cond)
		exactArgs = append(exactArgs, cargs...)
	}
	exact, err := d.Query(ctx,
		"SELECT COALESCE(NULLIF(x.model,''),'"+UnknownModel+"') m, SUM(x.input) i, SUM(x.output) o,"+
			" SUM(x.cache_read) cr, SUM(x.cache_create) cc, COUNT(DISTINCT x.session_id) n"+
			" FROM usage_series x"+
			" WHERE "+strings.Join(exactWhere, " AND ")+
			" GROUP BY 1", exactArgs...)
	if err != nil {
		return nil, err
	}

	// ② series 가 없는 세션 — 종전 질의 그대로. 이 몫이 곧 "세션 최빈 모델 기준" 이다.
	sw, sargs := sessionWhereOn(f, "s")
	fbWhere := append([]string{
		"NOT EXISTS (SELECT 1 FROM usage_series x WHERE x.session_id = s.session_id)",
	}, sw...)
	fallback, err := d.Query(ctx,
		"SELECT COALESCE(NULLIF(s.model,''),'"+UnknownModel+"') m, SUM(s.input) i, SUM(s.output) o,"+
			" SUM(s.cache_read) cr, SUM(s.cache_create) cc, COUNT(*) n"+
			" FROM usage_sessions s"+
			" WHERE "+strings.Join(fbWhere, " AND ")+
			" GROUP BY 1", sargs...)
	if err != nil {
		return nil, err
	}

	/*
	 * ③ 잔여 = 세션 행 − series 합. 축마다 CASE 로 0 에서 끊는다(음수 금지) —
	 * GREATEST/MAX(a,b) 는 방언이 갈려 쓰지 않는다. 음수가 나오는 세션(series 가 세션 행보다
	 * 큰 경우)은 정상이 아니므로 UsageModelAxis().OverSessions 로 따로 센다. 조용히 덮지 않는다.
	 */
	resSQL := "SELECT COALESCE(NULLIF(s.model,''),'" + UnknownModel + "') m," +
		" SUM(CASE WHEN s.input > x.i THEN s.input - x.i ELSE 0 END) i," +
		" SUM(CASE WHEN s.output > x.o THEN s.output - x.o ELSE 0 END) o," +
		" SUM(CASE WHEN s.cache_read > x.cr THEN s.cache_read - x.cr ELSE 0 END) cr," +
		" SUM(CASE WHEN s.cache_create > x.cc THEN s.cache_create - x.cc ELSE 0 END) cc" +
		" FROM usage_sessions s" +
		" JOIN (" + seriesPerSession + ") x ON x.sid = s.session_id"
	if len(sw) > 0 {
		resSQL += " WHERE " + strings.Join(sw, " AND ")
	}
	residual, err := d.Query(ctx, resSQL+" GROUP BY 1", sargs...)
	if err != nil {
		return nil, err
	}

	order := []string{}
	out := map[string]*ModelRow{}
	at := func(m string) *ModelRow {
		if m == "" {
			m = UnknownModel
		}
		if row, ok := out[m]; ok {
			return row
		}
		row := &ModelRow{Model: m}
		out[m] = row
		order = append(order, m)
		return row
	}

	for _, r := range exact {
		row := at(r.Str("m"))
		addShare(&row.FromSeries, r)
		row.FromSeries.Sessions += int(r.Int("n"))
	}
	for _, r := range fallback {
		row := at(r.Str("m"))
		addShare(&row.FromSession, r)
		row.FromSession.Sessions += int(r.Int("n"))
	}
	for _, r := range residual {
		// 세션 수는 더하지 않는다 — 이 세션들은 ①에서 이미 세었다(같은 세션을 두 번 세면 안 된다).
		addShare(&at(r.Str("m")).FromSession, r)
	}

	rows := make([]ModelRow, 0, len(order))
	for _, m := range order {
		r := out[m]
		r.Input = r.FromSeries.Input + r.FromSession.Input
		r.Output = r.FromSeries.Output + r.FromSession.Output
		r.CacheRead = r.FromSeries.CacheRead + r.FromSession.CacheRead
		r.CacheCreate = r.FromSeries.CacheCreate + r.FromSession.CacheCreate
		r.Sessions = r.FromSeries.Sessions + r.FromSession.Sessions
		rows = append(rows, *r)
	}
	// 출력 내림차순, 동률은 모델명 오름차순 — 화면이 흔들리면 안 되므로 결정론이다.
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Output != rows[j].Output {
			return rows[i].Output > rows[j].Output
		}
		return rows[i].Model < rows[j].Model
	})
	return rows, nil
}

/*
 * UsageModelAxis — 모델 축의 커버리지. "이 표의 어디까지가 정확한 값인가"를 화면이 말하게 하는 근거.
 *
 * 사람별로 내보내는 이유: 커버리지가 사람마다 갈리고(낮은 사람은 2.2%), 그 사람의 모델별 값이
 * 사실상 전부 세션 최빈 모델 기준이라는 것은 **그 사람 행을 볼 때** 알아야 하는 사실이다.
 *
 * **원인은 말하지 않는다.** 커버리지가 낮다는 사실과 그것이 뜻하는 것만 낸다 — 낮은 이유는
 * 그 PC 쪽 사실이고 서버는 모른다.
 *
 * username 은 usage_sessions 쪽을 쓴다. usage_series.username 은 identity 재스탬프가 건드리지
 * 않아 매핑 후 갈릴 수 있다 — 세션 행을 기준으로 잡아야 그 결함에 오염되지 않는다.
 */
func UsageModelAxis(ctx context.Context) (ModelAxis, error) {
	return UsageModelAxisWithFilter(ctx, Filter{})
}

// UsageModelAxisWithFilter 는 필터가 걸린 모델 축 커버리지다.
func UsageModelAxisWithFilter(ctx context.Context, f Filter) (ModelAxis, error) {
	out := ModelAxis{Users: []ModelAxisUser{}}
	d, err := conn()
	if err != nil {
		return out, err
	}
	// 커버리지는 **세션 기준**이라 필터도 세션에 건다(x 는 그 세션의 버킷 합일 뿐이다).
	where, args := sessionWhereOn(f, "s")
	axisWhere := ""
	if len(where) > 0 {
		axisWhere = " WHERE " + strings.Join(where, " AND ")
	}
	rows, err := d.Query(ctx,
		"SELECT COALESCE(NULLIF(s.username,''),'"+UnknownModel+"') u, COUNT(*) n,"+
			" SUM(CASE WHEN x.sid IS NULL THEN 0 ELSE 1 END) w,"+
			" SUM(CASE WHEN x.sid IS NOT NULL AND (s.input < x.i OR s.output < x.o"+
			" OR s.cache_read < x.cr OR s.cache_create < x.cc) THEN 1 ELSE 0 END) ov,"+
			/*
			 * series 가 **왜** 없는지의 근거. 시각이 없어 버킷에 못 올린 턴 수를 수집기가 보낸다.
			 * 커버리지가 낮은 것만 보면 사람은 "보고가 안 온다"로 읽는다 — 보고는 오고 있고
			 * 시각이 없을 뿐이다. 그 구분이 없으면 엉뚱한 데를 판다.
			 * NULL(구버전 수집기)은 세지 않는다 — 모르는 것을 0 으로 접으면 "시각 문제 없음"이 된다.
			 */
			" SUM(COALESCE(s.no_ts_turns,0)) nts,"+
			" SUM(CASE WHEN s.no_ts_turns IS NULL THEN 1 ELSE 0 END) nts_unknown"+
			" FROM usage_sessions s"+
			" LEFT JOIN ("+seriesPerSession+") x ON x.sid = s.session_id"+
			axisWhere+
			" GROUP BY 1 ORDER BY 2 DESC, 1 DESC", args...)
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		u := ModelAxisUser{
			Username:    r.Str("u"),
			Sessions:    int(r.Int("n")),
			WithSeries:  int(r.Int("w")),
			NoTsTurns:   r.Int("nts"),
			NoTsUnknown: int(r.Int("nts_unknown")),
		}
		out.Users = append(out.Users, u)
		out.Sessions += u.Sessions
		out.WithSeries += u.WithSeries
		// series 합이 세션 행보다 큰 세션 수. 정상이면 0 이다(수집기는 같은 실행에서 두 값을
		// 같은 원본으로 만든다). >0 이면 그만큼이 ③에서 0 으로 끊겼다는 뜻 — 조용히 두지 않는다.
		out.OverSessions += int(r.Int("ov"))
	}
	return out, nil
}

// TopKeys 는 축별 상위 키다 — 명령·키워드·자산 사용 순위.
func TopKeys(ctx context.Context, kind string, limit int) ([]KeyRow, error) {
	return TopKeysWithFilter(ctx, kind, limit, Filter{})
}

/*
 * TopKeysWithFilter 는 필터가 걸린 축별 상위 키다.
 *
 * ⚠ usage_counters 에는 세션의 축(model·started_at)이 없다. 그래서 이 함수가 보는 것은
 *   **Filter.Platform 과 Filter.Username 뿐**이다. 플랫폼은 세션 행으로 되짚어 만들고
 *   (counterPlatformCond), 사용자는 이 표의 username 컬럼을 직접 본다(counterUserCond).
 *   기간·모델까지 여기서 흉내내면 세션 축과 카운터 축에 서로 다른 기간 규칙이 두 벌 생긴다 —
 *   이 화면(summary)이 기간을 노출하는 자리는 byDay 하나이고 그쪽은 세션 표가 소유한다.
 */
func TopKeysWithFilter(ctx context.Context, kind string, limit int, f Filter) ([]KeyRow, error) {
	d, err := conn()
	if err != nil {
		return nil, err
	}
	if !isCounterKind(kind) {
		return []KeyRow{}, nil
	}
	lim := clampInt(limit, 1, 200, 20)
	sql := "SELECT key, SUM(count) c, COUNT(DISTINCT session_id) s, COUNT(DISTINCT username) u" +
		" FROM usage_counters WHERE kind=?"
	args := []any{kind}
	if cond, cargs := counterPlatformCond(f); cond != "" {
		sql += " AND " + cond
		args = append(args, cargs...)
	}
	if cond, cargs := counterUserCond(f); cond != "" {
		sql += " AND " + cond
		args = append(args, cargs...)
	}
	args = append(args, lim)
	rows, err := d.Query(ctx, sql+" GROUP BY key ORDER BY c DESC LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	out := make([]KeyRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, KeyRow{
			Key: r.Str("key"), Count: r.Int("c"),
			Sessions: int(r.Int("s")), Users: int(r.Int("u")),
		})
	}
	return out, nil
}

/*
 * ByUser — 사용자별 축 집계. "누가 어떤 서브에이전트·스킬을 쓰는가".
 *
 * 왜 필요한가(실측): 같은 지침이 모든 세션에 실리는데 실제 사용은 사람마다 갈렸다.
 *
 *	사용자 A  backend-engineer 25 · qa-engineer 16 · frontend-engineer 14 · security-reviewer 10
 *	사용자 B  general-purpose 25 · Explore 10 · **역할 에이전트 0**
 *
 * 지침만 있고 측정이 없어 아무도 몰랐다. TopKeys 는 전체 합이라 이 차이를 보여주지 못한다.
 * 강제하지 않는다 — 보이면 사람이 판단한다.
 */
func ByUser(ctx context.Context, kind string, limit int) ([]UserKeys, error) {
	return ByUserWithFilter(ctx, kind, limit, Filter{})
}

// ByUserWithFilter 는 필터가 걸린 사용자별 축 집계다(dispatch 화면의 근거).
// TopKeysWithFilter 와 같은 제약 — 카운터 축에서 볼 수 있는 것은 Platform·Username 뿐이다.
//
// Username 이 걸리면 결과는 **그 사람 한 줄**로 좁혀진다(사람별 비교가 아니라 그 사람의 내역).
// 그것이 이 필터의 요점이다 — 전사 비교는 필터를 비우면 그대로 돌아온다.
func ByUserWithFilter(ctx context.Context, kind string, limit int, f Filter) ([]UserKeys, error) {
	d, err := conn()
	if err != nil {
		return nil, err
	}
	if !isCounterKind(kind) {
		return []UserKeys{}, nil
	}
	lim := clampInt(limit, 1, 50, 8)
	sql := "SELECT COALESCE(NULLIF(username,''),'" + UnknownModel + "') u, key, SUM(count) c" +
		" FROM usage_counters WHERE kind=?"
	args := []any{kind}
	if cond, cargs := counterPlatformCond(f); cond != "" {
		sql += " AND " + cond
		args = append(args, cargs...)
	}
	if cond, cargs := counterUserCond(f); cond != "" {
		sql += " AND " + cond
		args = append(args, cargs...)
	}
	rows, err := d.Query(ctx, sql+" GROUP BY 1, key ORDER BY 1, c DESC", args...)
	if err != nil {
		return nil, err
	}
	order := []string{}
	byU := map[string][]KeyCount{}
	for _, r := range rows {
		u := r.Str("u")
		if _, ok := byU[u]; !ok {
			order = append(order, u)
		}
		byU[u] = append(byU[u], KeyCount{Key: r.Str("key"), Count: r.Int("c")})
	}
	out := make([]UserKeys, 0, len(order))
	for _, u := range order {
		items := byU[u]
		var total int64
		for _, it := range items {
			total += it.Count
		}
		if len(items) > lim {
			items = items[:lim]
		}
		out = append(out, UserKeys{Username: u, Total: total, Items: items})
	}
	// 안정 정렬이라 총합이 같으면 SQL 이 준 순서(사용자명 오름차순)가 유지된다.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Total > out[j].Total })
	return out, nil
}

/*
 * 조사 제거 — 키워드 축과 추천 공백 토큰이 **같은 어휘**를 쓰게 하는 정규화.
 *
 * 규칙이 클라이언트 수집기에도 있는 이유: 훅은 팀원 PC 에서 도는 별도 프로세스라 서버 코드를
 * import 할 수 없다. 한쪽만 고치면 "키워드 상위"와 "추천 공백"이 다른 어휘로 갈려 나란히 못 놓는다.
 */
var particles = []string{
	"으로부터", "에게서", "으로", "에서", "부터", "까지", "에게", "한테", "보다", "처럼", "마다",
	"이라", "라는", "라고", "이며", "와의", "과의", "들이", "들을", "들은",
	"을", "를", "이", "가", "은", "는", "에", "의", "로", "와", "과", "도", "만",
}

// 3글자 토큰에는 '이'·'가'를 떼지 않는다 — 명사의 끝소리와 구분이 안 된다('고양이'→'고양',
// '어린이'→'어린' 같은 오절단). 4글자 이상은 어간이 충분히 남아 안전하다.
var particlesShort = func() []string {
	out := make([]string, 0, len(particles))
	for _, p := range particles {
		if p != "이" && p != "가" {
			out = append(out, p)
		}
	}
	return out
}()

func isAllHangul(r []rune) bool {
	for _, c := range r {
		if c < '가' || c > '힣' {
			return false
		}
	}
	return true
}

func stripParticle(t string) string {
	r := []rune(t)
	if len(r) < 3 || !isAllHangul(r) {
		return t
	}
	list := particlesShort
	if len(r) >= 4 {
		list = particles
	}
	for _, p := range list {
		pr := []rune(p)
		if len(r)-len(pr) >= 2 && strings.HasSuffix(t, p) {
			return string(r[:len(r)-len(pr)])
		}
	}
	return t
}

/*
 * RecommendationGaps — 점수가 낮았던 목표의 토큰 빈도.
 *
 * 이 목록이 학습 플랫폼 고도화의 실질 입력이다. 매칭이 약한 목표들이 같은 토큰을 공유하면
 * 그 토큰이 곧 카탈로그가 덮지 못하는 주제다.
 */
func RecommendationGaps(ctx context.Context, limit int) ([]Gap, error) {
	return RecommendationGapsAt(ctx, 1, limit)
}

// RecommendationGapsAt 는 임계 점수를 명시한다.
//
// 임계값을 인자로 빼는 이유: 점수 분포는 카탈로그 크기에 따라 달라져서 고정 상수로 두면
// 카탈로그가 커질 때 조용히 아무것도 안 잡는다.
func RecommendationGapsAt(ctx context.Context, maxScore float64, limit int) ([]Gap, error) {
	return RecommendationGapsAtWithFilter(ctx, maxScore, limit, Filter{})
}

/*
 * RecommendationGapsAtWithFilter 는 사람으로 좁힌 공백 목록이다.
 *
 * ⚠ 이 표가 보는 축은 **Username 하나뿐이다.** usage_recommendations 에는 session_id 가 없어
 *   (스키마 참조) 플랫폼·모델·기간으로 되짚을 근거가 존재하지 않는다 — 그래서 Filter 의 나머지
 *   필드는 여기서 조용히 무시된다. 이것은 값이 0 인 것과 다르다: 축이 없는 것을 0 으로 접으면
 *   "그 플랫폼에는 공백이 없었다"는 **없는 사실**을 만든다(usage.go 의 같은 주석과 한 벌).
 *
 *   반면 username 컬럼은 있다. 그래서 사용자 필터는 이 축까지 정직하게 닿는다.
 */
func RecommendationGapsAtWithFilter(ctx context.Context, maxScore float64, limit int, f Filter) ([]Gap, error) {
	d, err := conn()
	if err != nil {
		return nil, err
	}
	sql := "SELECT goal_tokens FROM usage_recommendations WHERE score <= ? AND goal_tokens IS NOT NULL"
	args := []any{maxScore}
	if cond, cargs := recoUserCond(f); cond != "" {
		sql += " AND " + cond
		args = append(args, cargs...)
	}
	// LIMIT 은 조건 뒤에 온다 — 앞에 두면 필터가 "최근 2000건 중 그 사람 것"만 보게 되어
	// 활동이 적은 사람의 공백이 조용히 사라진다.
	rows, err := d.Query(ctx, sql+" ORDER BY id DESC LIMIT 2000", args...)
	if err != nil {
		return nil, err
	}
	order := []string{}
	freq := map[string]int{}
	for _, r := range rows {
		for _, raw := range strings.Fields(r.Str("goal_tokens")) {
			t := stripParticle(raw)
			if len([]rune(t)) < 2 {
				continue
			}
			if _, ok := freq[t]; !ok {
				order = append(order, t)
			}
			freq[t]++
		}
	}
	gaps := make([]Gap, 0, len(order))
	for _, t := range order {
		// 1회성 목표는 공백의 증거가 못 된다.
		if freq[t] >= 2 {
			gaps = append(gaps, Gap{Token: t, Count: freq[t]})
		}
	}
	sort.SliceStable(gaps, func(i, j int) bool { return gaps[i].Count > gaps[j].Count })
	if lim := clampInt(limit, 1, 100, 20); len(gaps) > lim {
		gaps = gaps[:lim]
	}
	return gaps, nil
}

// RecommendationSummary 는 추천 전환 요약이다 — 몇 건 중 몇 건이 매칭 실패였나.
func RecommendationSummary(ctx context.Context) (RecoSummary, error) {
	return RecommendationSummaryWithFilter(ctx, Filter{})
}

// RecommendationSummaryWithFilter 는 사람으로 좁힌 전환 요약이다.
// RecommendationGapsAtWithFilter 와 같은 제약 — 이 표에서 볼 수 있는 축은 Username 뿐이다.
func RecommendationSummaryWithFilter(ctx context.Context, f Filter) (RecoSummary, error) {
	var out RecoSummary
	d, err := conn()
	if err != nil {
		return out, err
	}
	sql := "SELECT COUNT(*) n, SUM(CASE WHEN score <= 0 THEN 1 ELSE 0 END) miss FROM usage_recommendations"
	var args []any
	if cond, cargs := recoUserCond(f); cond != "" {
		sql += " WHERE " + cond
		args = append(args, cargs...)
	}
	r, err := d.QueryRow(ctx, sql, args...)
	if err != nil || r == nil {
		return out, err
	}
	return RecoSummary{Total: int(r.Int("n")), Miss: int(r.Int("miss"))}, nil
}
