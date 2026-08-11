package main

import (
	"bytes"
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/tscorp/user-usage/internal/db"
	"github.com/tscorp/user-usage/internal/intake"
	"github.com/tscorp/user-usage/internal/store"
	"github.com/tscorp/user-usage/internal/tenant"
)

// maintDB 는 store 스키마가 걸린 빈 sqlite 다. 정리 명령은 store 표를 직접 만지므로
// provDB(org 만 Init)로는 부족하다.
func maintDB(t *testing.T) (context.Context, db.DB) {
	t.Helper()
	ctx := tenant.With(context.Background(), tenant.Default)
	d, err := db.Open(ctx, db.Options{Mode: "local", DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := store.Init(ctx, d); err != nil {
		t.Fatalf("store.Init: %v", err)
	}
	return ctx, d
}

func exec(t *testing.T, ctx context.Context, d db.DB, sql string, args ...any) {
	t.Helper()
	if err := d.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %.60s…: %v", sql, err)
	}
}

// insertSession 은 세션 행 하나를 raw 로 넣는다. 인테이크를 통하면 자리표시자가 이미 접히므로
// (그게 앞 웨이브의 수정이다) **정리 대상 데이터를 만들 수 없다.**
func insertSession(t *testing.T, ctx context.Context, d db.DB, id, model string, in, out int64) {
	t.Helper()
	exec(t, ctx, d,
		"INSERT INTO usage_sessions (session_id, machine, username, project, model, platform,"+
			" input, output, cache_read, cache_create, turns, started_at, reported_at)"+
			" VALUES (?,?,?,?,?,'claude',?,?,0,0,1,'2026-08-01T00:00:00.000Z','2026-08-01T00:00:00.000Z')",
		id, "m1", "u1", "p1", nullable(model), in, out)
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// seriesRow 는 테스트가 넣고 되읽는 버킷이다. 병합 검증이 컬럼 단위라 전 컬럼을 들고 있다.
type seriesRow struct {
	Input, Output, CacheRead, CacheCreate    int64
	CC5m, CC1h                               int64
	InputLong, OutputLong, CacheReadLong     int64
	Turns, ToolErrors                        int64
	StopMaxTokens, StopRefusal               int64
	LatencyMsSum, LatencyMsMax, LatencyTurns int64
	Username, Machine, Project               string
}

func insertSeries(t *testing.T, ctx context.Context, d db.DB, sid, hour, model string, r seriesRow) {
	t.Helper()
	exec(t, ctx, d,
		"INSERT INTO usage_series (session_id, hour, model, input, output, cache_read, cache_create,"+
			" cc_5m, cc_1h, input_long, output_long, cache_read_long, turns, tool_errors,"+
			" stop_max_tokens, stop_refusal, latency_ms_sum, latency_ms_max, latency_turns,"+
			" username, machine, project)"+
			" VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
		sid, hour, model, r.Input, r.Output, r.CacheRead, r.CacheCreate,
		r.CC5m, r.CC1h, r.InputLong, r.OutputLong, r.CacheReadLong, r.Turns, r.ToolErrors,
		r.StopMaxTokens, r.StopRefusal, r.LatencyMsSum, r.LatencyMsMax, r.LatencyTurns,
		nullable(r.Username), nullable(r.Machine), nullable(r.Project))
}

func readSeries(t *testing.T, ctx context.Context, d db.DB, sid, hour, model string) (seriesRow, bool) {
	t.Helper()
	row, err := d.QueryRow(ctx,
		"SELECT * FROM usage_series WHERE session_id=? AND hour=? AND model=?", sid, hour, model)
	if err != nil {
		t.Fatalf("readSeries: %v", err)
	}
	if row == nil {
		return seriesRow{}, false
	}
	return seriesRow{
		Input: row.Int("input"), Output: row.Int("output"),
		CacheRead: row.Int("cache_read"), CacheCreate: row.Int("cache_create"),
		CC5m: row.Int("cc_5m"), CC1h: row.Int("cc_1h"),
		InputLong: row.Int("input_long"), OutputLong: row.Int("output_long"),
		CacheReadLong: row.Int("cache_read_long"),
		Turns:         row.Int("turns"), ToolErrors: row.Int("tool_errors"),
		StopMaxTokens: row.Int("stop_max_tokens"), StopRefusal: row.Int("stop_refusal"),
		LatencyMsSum: row.Int("latency_ms_sum"), LatencyMsMax: row.Int("latency_ms_max"),
		LatencyTurns: row.Int("latency_turns"),
		Username:     row.Str("username"), Machine: row.Str("machine"), Project: row.Str("project"),
	}, true
}

func countRows(t *testing.T, ctx context.Context, d db.DB, sql string, args ...any) int {
	t.Helper()
	r, err := d.QueryRow(ctx, sql, args...)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if r == nil {
		return 0
	}
	return int(r.Int("c"))
}

/*
 * ① 판정 규칙은 **한 벌**이어야 한다.
 *
 * 자리표시자 판정의 단일 출처는 internal/intake 의 normModel 이다. 이 파일은 internal/** 을
 * 고칠 수 없어 같은 정규식을 들고 있으므로, 두 판정이 갈리지 않는 것을 intake 의 공개
 * 표면(NormSession)을 통해 **행동으로** 못 박는다. 한쪽만 고치면 여기서 깨진다.
 */
func TestPlaceholderRuleMatchesIntake(t *testing.T) {
	cases := []string{
		"<synthetic>", "<none>", "<unknown>", "<>",
		"claude-opus-4-8", "", "(미상)", "a<b>c", "<a><b>", "<half", "half>",
		"gpt-5.6", "<synthetic> ", " <synthetic>",
	}
	for _, m := range cases {
		raw := map[string]any{"sessionId": "parity-session-01", "model": m}
		s, ok := intake.NormSession(raw)
		if !ok {
			t.Fatalf("NormSession(%q) 이 거부됐다 — 표본이 잘못됐다", m)
		}
		// intake 는 자리표시자를 빈 값으로 접는다(세션은 NULL). 접혔다 == 자리표시자다.
		intakeSaysPlaceholder := s.Model == nil && m != ""
		if got := isPlaceholderModel(m); got != intakeSaysPlaceholder {
			t.Errorf("판정이 갈렸다 model=%q: maintenance=%v intake=%v", m, got, intakeSaysPlaceholder)
		}
	}
}

// ② --dry-run(기본)은 **아무것도 바꾸지 않는다.**
func TestCleanupDryRunChangesNothing(t *testing.T) {
	ctx, d := maintDB(t)
	insertSession(t, ctx, d, "s1", "<synthetic>", 0, 0)
	insertSeries(t, ctx, d, "s1", "2026-08-01T03", "<synthetic>", seriesRow{Turns: 4, ToolErrors: 1})

	rep, err := cleanPlaceholderModels(ctx, d, false)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if rep.Applied {
		t.Fatal("dry-run 인데 Applied 가 참이다")
	}
	if rep.Sessions != 1 || rep.SeriesRenamed != 1 || rep.SeriesMerged != 0 {
		t.Fatalf("계획이 틀렸다: %+v", rep)
	}
	// DB 는 그대로여야 한다.
	if n := countRows(t, ctx, d,
		"SELECT COUNT(*) c FROM usage_sessions WHERE model='<synthetic>'"); n != 1 {
		t.Fatalf("dry-run 이 세션을 건드렸다: %d", n)
	}
	if n := countRows(t, ctx, d,
		"SELECT COUNT(*) c FROM usage_series WHERE model='<synthetic>'"); n != 1 {
		t.Fatalf("dry-run 이 버킷을 건드렸다: %d", n)
	}
}

// ③ 기본 정리 — 세션은 NULL, 버킷은 '(미상)'. 그 두 규칙은 이 레포에 이미 한 벌씩 있다.
func TestCleanupRewritesLabels(t *testing.T) {
	ctx, d := maintDB(t)
	insertSession(t, ctx, d, "s1", "<synthetic>", 10, 20)
	insertSession(t, ctx, d, "s2", "claude-opus-4-8", 5, 5)
	insertSeries(t, ctx, d, "s1", "2026-08-01T03", "<synthetic>", seriesRow{Turns: 4})
	insertSeries(t, ctx, d, "s2", "2026-08-01T03", "claude-opus-4-8", seriesRow{Input: 5, Output: 5, Turns: 1})

	rep, err := cleanPlaceholderModels(ctx, d, true)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !rep.Applied || rep.Sessions != 1 || rep.SeriesRenamed != 1 || rep.SeriesMerged != 0 {
		t.Fatalf("보고가 틀렸다: %+v", rep)
	}
	if n := countRows(t, ctx, d, "SELECT COUNT(*) c FROM usage_sessions WHERE model IS NULL"); n != 1 {
		t.Fatalf("세션 model 이 NULL 로 돌아가지 않았다: %d", n)
	}
	if _, ok := readSeries(t, ctx, d, "s1", "2026-08-01T03", store.UnknownModel); !ok {
		t.Fatalf("버킷이 %s 로 바뀌지 않았다", store.UnknownModel)
	}
	// 멀쩡한 모델은 건드리지 않는다.
	if n := countRows(t, ctx, d,
		"SELECT COUNT(*) c FROM usage_sessions WHERE model='claude-opus-4-8'"); n != 1 {
		t.Fatal("정상 모델 세션이 바뀌었다")
	}
	// 세션 수·버킷 수는 그대로 — 라벨만 바꾼다.
	if n := countRows(t, ctx, d, "SELECT COUNT(*) c FROM usage_sessions"); n != 2 {
		t.Fatalf("세션이 사라졌다: %d", n)
	}
	if n := countRows(t, ctx, d, "SELECT COUNT(*) c FROM usage_series"); n != 2 {
		t.Fatalf("버킷이 사라졌다: %d", n)
	}
}

/*
 * ④ PK 충돌 병합 — usage_series 는 (session, hour, model) 이 PK 라 라벨만 바꾸면 충돌한다.
 *
 * 컬럼마다 결합 방식이 다르다: 대부분 합이지만 latency_ms_max 는 MAX 이고, 라벨 컬럼
 * (username·machine·project)은 기존 값을 유지한다. **컬럼 단위로** 못 박는다 — 한 컬럼만
 * 틀리면 그 축의 숫자가 조용히 줄어든다.
 */
func TestCleanupSeriesMergeCombinesPerColumn(t *testing.T) {
	ctx, d := maintDB(t)
	insertSession(t, ctx, d, "s1", "", 0, 0)

	// 이미 있는 (미상) 버킷.
	insertSeries(t, ctx, d, "s1", "2026-08-01T03", store.UnknownModel, seriesRow{
		Input: 100, Output: 200, CacheRead: 300, CacheCreate: 400,
		CC5m: 40, CC1h: 360,
		InputLong: 10, OutputLong: 20, CacheReadLong: 30,
		Turns: 7, ToolErrors: 2, StopMaxTokens: 1, StopRefusal: 3,
		LatencyMsSum: 5000, LatencyMsMax: 900, LatencyTurns: 6,
		Username: "u1", Machine: "m1", Project: "p1",
	})
	// 같은 시각의 자리표시자 버킷 — 토큰은 0 이지만 카운터는 0 이 아니다.
	insertSeries(t, ctx, d, "s1", "2026-08-01T03", "<synthetic>", seriesRow{
		Turns: 4, ToolErrors: 5, StopMaxTokens: 2, StopRefusal: 1,
		LatencyMsSum: 1234, LatencyMsMax: 4321, LatencyTurns: 3,
		Username: "other", Machine: "other", Project: "other",
	})

	rep, err := cleanPlaceholderModels(ctx, d, true)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if rep.SeriesMerged != 1 || rep.SeriesRenamed != 0 {
		t.Fatalf("병합으로 세지 않았다: %+v", rep)
	}
	if n := countRows(t, ctx, d, "SELECT COUNT(*) c FROM usage_series"); n != 1 {
		t.Fatalf("병합 후 버킷이 1개여야 한다: %d", n)
	}
	got, ok := readSeries(t, ctx, d, "s1", "2026-08-01T03", store.UnknownModel)
	if !ok {
		t.Fatal("병합 대상 버킷이 없다")
	}
	want := seriesRow{
		Input: 100, Output: 200, CacheRead: 300, CacheCreate: 400,
		CC5m: 40, CC1h: 360,
		InputLong: 10, OutputLong: 20, CacheReadLong: 30,
		Turns: 11, ToolErrors: 7, StopMaxTokens: 3, StopRefusal: 4,
		LatencyMsSum: 6234,
		LatencyMsMax: 4321, // 합이 아니라 MAX 다
		LatencyTurns: 9,
		Username:     "u1", Machine: "m1", Project: "p1", // 기존 라벨 유지
	}
	if got != want {
		t.Fatalf("병합 결과가 다르다\n got=%+v\nwant=%+v", got, want)
	}
}

// ⑤ 같은 (세션, 시각)에 자리표시자가 **둘** — 첫 값이 (미상)이 되고 둘째가 거기에 합류한다.
func TestCleanupMergesTwoPlaceholdersIntoOne(t *testing.T) {
	ctx, d := maintDB(t)
	insertSession(t, ctx, d, "s1", "", 0, 0)
	insertSeries(t, ctx, d, "s1", "2026-08-01T03", "<synthetic>", seriesRow{Turns: 2, LatencyMsMax: 10})
	insertSeries(t, ctx, d, "s1", "2026-08-01T03", "<none>", seriesRow{Turns: 3, LatencyMsMax: 50})

	rep, err := cleanPlaceholderModels(ctx, d, true)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if rep.SeriesRenamed+rep.SeriesMerged != 2 {
		t.Fatalf("두 행을 다루지 않았다: %+v", rep)
	}
	if n := countRows(t, ctx, d, "SELECT COUNT(*) c FROM usage_series"); n != 1 {
		t.Fatalf("한 버킷으로 합쳐지지 않았다: %d", n)
	}
	got, _ := readSeries(t, ctx, d, "s1", "2026-08-01T03", store.UnknownModel)
	if got.Turns != 5 || got.LatencyMsMax != 50 {
		t.Fatalf("합산/MAX 가 틀렸다: %+v", got)
	}
}

// ⑥ 멱등 — 두 번째 실행은 0행이다.
func TestCleanupIdempotent(t *testing.T) {
	ctx, d := maintDB(t)
	insertSession(t, ctx, d, "s1", "<synthetic>", 0, 0)
	insertSeries(t, ctx, d, "s1", "2026-08-01T03", "<synthetic>", seriesRow{Turns: 4})
	insertSeries(t, ctx, d, "s1", "2026-08-01T04", store.UnknownModel, seriesRow{Turns: 1})
	insertSeries(t, ctx, d, "s1", "2026-08-01T04", "<synthetic>", seriesRow{Turns: 2})

	first, err := cleanPlaceholderModels(ctx, d, true)
	if err != nil {
		t.Fatalf("1회차: %v", err)
	}
	if first.Total() == 0 {
		t.Fatal("1회차가 아무것도 하지 않았다 — 표본이 잘못됐다")
	}
	second, err := cleanPlaceholderModels(ctx, d, true)
	if err != nil {
		t.Fatalf("2회차: %v", err)
	}
	if second.Total() != 0 {
		t.Fatalf("2회차가 0행이 아니다: %+v", second)
	}
	// dry-run 도 0행이어야 한다(계획이 비어야 "할 일 없음"이 화면에 뜬다).
	plan, err := cleanPlaceholderModels(ctx, d, false)
	if err != nil {
		t.Fatalf("2회차 dry-run: %v", err)
	}
	if plan.Total() != 0 {
		t.Fatalf("정리 후 dry-run 이 0행이 아니다: %+v", plan)
	}
}

/*
 * ⑦ `①+②+③ == Totals` 불변식 — 정리 **전후 모두** 성립해야 한다.
 *
 * 이 명령은 모델 라벨만 바꾸므로 숫자가 움직이면 안 된다. 모델별 표가 세션 총합과 어긋나면
 * 사람에게는 "유실"로 보인다(이 레포에서 실제로 있었던 결함).
 */
func TestCleanupPreservesModelAxisInvariant(t *testing.T) {
	ctx, d := maintDB(t)
	// series 가 있는 세션(①+③)과 없는 세션(②)을 섞는다.
	insertSession(t, ctx, d, "s1", "<synthetic>", 1000, 2000)
	insertSeries(t, ctx, d, "s1", "2026-08-01T03", "<synthetic>", seriesRow{Turns: 4})
	insertSeries(t, ctx, d, "s1", "2026-08-01T03", store.UnknownModel, seriesRow{Input: 100, Output: 200, Turns: 1})
	insertSeries(t, ctx, d, "s1", "2026-08-01T04", "claude-opus-4-8", seriesRow{Input: 400, Output: 800, Turns: 2})
	insertSession(t, ctx, d, "s2", "<none>", 50, 60) // series 없음 → ②
	insertSession(t, ctx, d, "s3", "gpt-5.6", 7, 8)  // series 없음 → ②

	before := modelAxisSum(t, ctx)
	if _, err := cleanPlaceholderModels(ctx, d, true); err != nil {
		t.Fatalf("apply: %v", err)
	}
	after := modelAxisSum(t, ctx)

	if before != after {
		t.Fatalf("정리가 숫자를 움직였다\nbefore=%+v\n after=%+v", before, after)
	}
	// 정리 후 모델 축에 자리표시자 라벨이 남아 있으면 안 된다.
	rows, err := store.UsageByModel(ctx)
	if err != nil {
		t.Fatalf("UsageByModel: %v", err)
	}
	for _, r := range rows {
		if isPlaceholderModel(r.Model) {
			t.Fatalf("모델 축에 자리표시자가 남았다: %q", r.Model)
		}
	}
}

type axisSum struct {
	Input, Output, CacheRead, CacheCreate int64
	TotalsMatch                           bool
}

// modelAxisSum 은 모델별 합과 Totals 를 대조한다. 두 값이 같아야 불변식이 성립한 것이다.
func modelAxisSum(t *testing.T, ctx context.Context) axisSum {
	t.Helper()
	rows, err := store.UsageByModel(ctx)
	if err != nil {
		t.Fatalf("UsageByModel: %v", err)
	}
	var s axisSum
	for _, r := range rows {
		s.Input += r.Input
		s.Output += r.Output
		s.CacheRead += r.CacheRead
		s.CacheCreate += r.CacheCreate
	}
	tot, err := store.Totals(ctx)
	if err != nil {
		t.Fatalf("Totals: %v", err)
	}
	s.TotalsMatch = s.Input == tot.Input && s.Output == tot.Output &&
		s.CacheRead == tot.CacheRead && s.CacheCreate == tot.CacheCreate
	if !s.TotalsMatch {
		t.Fatalf("①+②+③ != Totals: axis=%+v totals=%+v", s, tot)
	}
	return s
}

/*
 * ⑧ 스키마 드리프트 방어 — usage_series 의 **모든** 컬럼이 병합 규칙에 배정돼 있어야 한다.
 *
 * 컬럼이 하나 늘었는데 여기 배정을 빠뜨리면, 병합된 버킷에서 그 축이 조용히 사라진다.
 * 그건 테스트 없이는 아무도 못 잡는 종류의 유실이라 스키마를 직접 물어본다.
 */
func TestSeriesMergeCoversEveryColumn(t *testing.T) {
	ctx, d := maintDB(t)
	rows, err := d.Query(ctx, "PRAGMA table_info(usage_series)")
	if err != nil {
		t.Fatalf("PRAGMA: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("usage_series 컬럼을 읽지 못했다")
	}
	assigned := map[string]string{}
	add := func(kind string, cols []string) {
		for _, c := range cols {
			if prev, dup := assigned[c]; dup {
				t.Fatalf("컬럼 %q 가 %s 와 %s 에 중복 배정됐다", c, prev, kind)
			}
			assigned[c] = kind
		}
	}
	add("PK", seriesKeyCols)
	add("SUM", seriesSumCols)
	add("MAX", seriesMaxCols)
	add("KEEP", seriesKeepCols)

	var missing []string
	for _, r := range rows {
		name := strings.ToLower(r.Str("name"))
		if _, ok := assigned[name]; !ok {
			missing = append(missing, name)
		}
		delete(assigned, name)
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("병합 규칙에 배정되지 않은 컬럼: %v — maintenance.go 의 series*Cols 를 갱신하라", missing)
	}
	if len(assigned) > 0 {
		var extra []string
		for c := range assigned {
			extra = append(extra, c)
		}
		sort.Strings(extra)
		t.Fatalf("스키마에 없는 컬럼이 배정돼 있다: %v", extra)
	}
}

// ⑨ CLI 표면 — 기본은 dry-run, --apply 만 실제로 바꾼다. 인자 오류는 2 다.
func TestCleanupCmd(t *testing.T) {
	ctx, d := maintDB(t)
	insertSession(t, ctx, d, "s1", "<synthetic>", 0, 0)
	insertSeries(t, ctx, d, "s1", "2026-08-01T03", "<synthetic>", seriesRow{Turns: 4})

	var out bytes.Buffer
	if rc := cleanupCmd(ctx, d, &out, []string{"placeholder-models"}); rc != 0 {
		t.Fatalf("dry-run rc=%d out=%s", rc, out.String())
	}
	if !strings.Contains(out.String(), "dry-run") || !strings.Contains(out.String(), "--apply") {
		t.Fatalf("dry-run 안내가 없다: %s", out.String())
	}
	if n := countRows(t, ctx, d, "SELECT COUNT(*) c FROM usage_series WHERE model='<synthetic>'"); n != 1 {
		t.Fatal("dry-run 이 DB 를 바꿨다")
	}

	out.Reset()
	if rc := cleanupCmd(ctx, d, &out, []string{"placeholder-models", "--apply"}); rc != 0 {
		t.Fatalf("apply rc=%d out=%s", rc, out.String())
	}
	if n := countRows(t, ctx, d, "SELECT COUNT(*) c FROM usage_series WHERE model='<synthetic>'"); n != 0 {
		t.Fatal("--apply 가 아무것도 바꾸지 않았다")
	}

	out.Reset()
	if rc := cleanupCmd(ctx, d, &out, nil); rc != 2 {
		t.Fatalf("서브커맨드 없이 rc=%d 여야 한다(2)", rc)
	}
	if rc := cleanupCmd(ctx, d, &out, []string{"nope"}); rc != 2 {
		t.Fatalf("모르는 서브커맨드 rc=%d 여야 한다(2)", rc)
	}
}

// ⑩ 정리 대상이 없으면 그렇게 말한다 — 빈 DB 에서 오류가 나면 안 된다.
func TestCleanupOnCleanDB(t *testing.T) {
	ctx, d := maintDB(t)
	var out bytes.Buffer
	if rc := cleanupCmd(ctx, d, &out, []string{"placeholder-models", "--apply"}); rc != 0 {
		t.Fatalf("rc=%d out=%s", rc, out.String())
	}
	if !strings.Contains(out.String(), "정리할 행이 없다") {
		t.Fatalf("빈 결과 안내가 없다: %s", out.String())
	}
}
