package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/tscorp/user-usage/internal/db"
	"github.com/tscorp/user-usage/internal/identity"
	"github.com/tscorp/user-usage/internal/intake"
	"github.com/tscorp/user-usage/internal/org"
	"github.com/tscorp/user-usage/internal/store"
	"github.com/tscorp/user-usage/internal/tenant"
)

/*
 * maintDB 는 store·identity 스키마가 걸린 빈 sqlite 다. 정리 명령은 그 표들을 직접 만지므로
 * provDB(org 만 Init)로는 부족하다.
 *
 * identity 를 함께 거는 이유: `cleanup usage-rows` 가 machine_identity(머신→계정 매핑)와
 * usage_audit(감사 로그, 보존 대상)을 판단 대상으로 삼는다 — 표가 없으면 그 판단을 검증할 수 없다.
 */
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
	if err := identity.Init(ctx, d); err != nil {
		t.Fatalf("identity.Init: %v", err)
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
	insertSessionAs(t, ctx, d, id, "u1", "m1", model, in, out)
}

// insertSessionAs 는 귀속(사용자·머신)을 지정해 세션을 넣는다 — 삭제 명령의 범위 검증에 필요하다.
func insertSessionAs(t *testing.T, ctx context.Context, d db.DB, id, username, machine, model string, in, out int64) {
	t.Helper()
	exec(t, ctx, d,
		"INSERT INTO usage_sessions (session_id, machine, username, project, model, platform,"+
			" input, output, cache_read, cache_create, turns, started_at, reported_at)"+
			" VALUES (?,?,?,?,?,'claude',?,?,0,0,1,'2026-08-01T00:00:00.000Z','2026-08-01T00:00:00.000Z')",
		id, nullable(machine), nullable(username), "p1", nullable(model), in, out)
}

// insertCounter 는 축 카운터 한 행이다(usage_counters).
func insertCounter(t *testing.T, ctx context.Context, d db.DB, sid, kind, key, username, machine string, n int64) {
	t.Helper()
	exec(t, ctx, d,
		"INSERT INTO usage_counters (session_id, kind, key, count, day, username, machine)"+
			" VALUES (?,?,?,?,'2026-08-01',?,?)",
		sid, kind, key, n, nullable(username), nullable(machine))
}

// insertRecommendation 은 추천 관측 한 행이다(usage_recommendations — username 만 있고 세션도 머신도 없다).
func insertRecommendation(t *testing.T, ctx context.Context, d db.DB, username string) {
	t.Helper()
	exec(t, ctx, d,
		"INSERT INTO usage_recommendations (goal_tokens, agent, skills, score, source, username, at)"+
			" VALUES ('a b','agent','skill',0.5,'test',?,'2026-08-01T00:00:00.000Z')",
		nullable(username))
}

// insertMapping 은 머신→계정 매핑 한 행이다(machine_identity).
func insertMapping(t *testing.T, ctx context.Context, d db.DB, machine, username string) {
	t.Helper()
	exec(t, ctx, d,
		"INSERT INTO machine_identity (machine, username, note, updated_by, updated_at)"+
			" VALUES (?,?,'테스트','tester','2026-08-01T00:00:00.000Z')",
		machine, username)
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

/*
 * ── cleanup usage-rows — 수집된 사용량 행 삭제 ──────────────────────────
 *
 * placeholder-models 와 달리 이 명령은 **행을 지운다.** 그래서 검증의 무게가 다르다:
 * "지워졌나"보다 **"지우지 말아야 할 것이 남았나"** 와 **"중간에 끊겨도 반쪽이 안 되나"** 다.
 */

// purgeSeed 는 두 사람(amy·bob)의 사용량과 **세션 행이 없는 고아 행**까지 심는다.
//
// 고아 행이 표본에 있는 이유: 인테이크가 세션 행만 실패하고 버킷은 들어가는 자리가 실재한다
// (store/aggregate.go 의 ① 주석). 그 행도 화면에 계정명·머신명을 남기므로 삭제 대상이다.
func purgeSeed(t *testing.T, ctx context.Context, d db.DB) {
	t.Helper()
	// amy — series 가 있는 세션 하나(a1)와 없는 세션 하나(a2).
	insertSessionAs(t, ctx, d, "a1", "amy", "pc-amy", "claude-opus-4-8", 1000, 2000)
	insertSeries(t, ctx, d, "a1", "2026-08-01T03", "claude-opus-4-8", seriesRow{
		Input: 400, Output: 800, Turns: 2, Username: "amy", Machine: "pc-amy", Project: "p1"})
	insertSeries(t, ctx, d, "a1", "2026-08-01T04", store.UnknownModel, seriesRow{
		Input: 100, Output: 200, Turns: 1, Username: "amy", Machine: "pc-amy", Project: "p1"})
	insertCounter(t, ctx, d, "a1", "tool", "Read", "amy", "pc-amy", 5)
	insertCounter(t, ctx, d, "a1", "bash", "ls", "amy", "pc-amy", 3)
	insertSessionAs(t, ctx, d, "a2", "amy", "pc-amy", "claude-opus-4-8", 50, 60)
	insertCounter(t, ctx, d, "a2", "skill", "x", "amy", "pc-amy", 1)
	insertRecommendation(t, ctx, d, "amy")
	insertMapping(t, ctx, d, "pc-amy", "amy")

	// bob — 건드리면 안 되는 쪽.
	insertSessionAs(t, ctx, d, "b1", "bob", "pc-bob", "gpt-5.6", 7, 8)
	insertSeries(t, ctx, d, "b1", "2026-08-01T03", "gpt-5.6", seriesRow{
		Input: 7, Output: 8, Turns: 1, Username: "bob", Machine: "pc-bob", Project: "p1"})
	insertCounter(t, ctx, d, "b1", "tool", "Write", "bob", "pc-bob", 2)
	insertRecommendation(t, ctx, d, "bob")
	insertMapping(t, ctx, d, "pc-bob", "bob")

	// 고아 행 — 세션 행이 없다.
	insertSeries(t, ctx, d, "orph-amy", "2026-08-01T05", "claude-opus-4-8", seriesRow{
		Turns: 1, Username: "amy", Machine: "pc-amy"})
	insertCounter(t, ctx, d, "orph-amy", "tool", "Grep", "amy", "pc-amy", 1)
	insertSeries(t, ctx, d, "orph-bob", "2026-08-01T05", "gpt-5.6", seriesRow{
		Turns: 1, Username: "bob", Machine: "pc-bob"})
}

// tableCounts 는 검증에 쓰는 표별 행 수 묶음이다.
type tableCounts struct{ Sessions, Series, Counters, Recos, Mappings, Audit int }

func snapshotCounts(t *testing.T, ctx context.Context, d db.DB) tableCounts {
	t.Helper()
	n := func(sql string) int { return countRows(t, ctx, d, sql) }
	return tableCounts{
		Sessions: n("SELECT COUNT(*) c FROM usage_sessions"),
		Series:   n("SELECT COUNT(*) c FROM usage_series"),
		Counters: n("SELECT COUNT(*) c FROM usage_counters"),
		Recos:    n("SELECT COUNT(*) c FROM usage_recommendations"),
		Mappings: n("SELECT COUNT(*) c FROM machine_identity"),
		Audit:    n("SELECT COUNT(*) c FROM usage_audit"),
	}
}

// purgeTableRows 는 보고에서 표 하나의 행 수를 꺼낸다(없는 표는 -1 — 오타를 0 으로 위장하지 않는다).
func purgeTableRows(r purgeReport, table string) int {
	for _, tb := range r.Tables {
		if tb.Table == table {
			return tb.Total()
		}
	}
	return -1
}

// ⑪ dry-run(기본)은 **아무것도 지우지 않는다** — 그러면서 표별 행 수는 정확해야 한다.
func TestPurgeDryRunDeletesNothing(t *testing.T) {
	ctx, d := maintDB(t)
	purgeSeed(t, ctx, d)
	before := snapshotCounts(t, ctx, d)

	rep, err := purgeUsageRows(ctx, d, purgeOptions{Scope: purgeByUser, Target: "amy"})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if rep.Applied {
		t.Fatal("dry-run 인데 Applied 가 참이다")
	}
	want := map[string]int{
		"usage_sessions":        2, // a1 · a2
		"usage_series":          3, // a1 의 2 + 고아 1
		"usage_counters":        4, // a1 의 2 + a2 의 1 + 고아 1
		"usage_recommendations": 1,
		"machine_identity":      1,
	}
	for table, n := range want {
		if got := purgeTableRows(rep, table); got != n {
			t.Errorf("%s 계획이 틀렸다: got=%d want=%d", table, got, n)
		}
	}
	if rep.Total() != 11 {
		t.Errorf("합계가 틀렸다: got=%d want=11", rep.Total())
	}
	if after := snapshotCounts(t, ctx, d); after != before {
		t.Fatalf("dry-run 이 DB 를 바꿨다\nbefore=%+v\n after=%+v", before, after)
	}
}

// ⑫ --apply 는 계획한 그대로 지운다 — dry-run 과 apply 의 보고가 같아야 "미리 보고 그대로 실행"이 성립한다.
func TestPurgeApplyMatchesPlan(t *testing.T) {
	ctx, d := maintDB(t)
	purgeSeed(t, ctx, d)

	plan, err := purgeUsageRows(ctx, d, purgeOptions{Scope: purgeByUser, Target: "amy"})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	done, err := purgeUsageRows(ctx, d, purgeOptions{Scope: purgeByUser, Target: "amy", Apply: true})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !done.Applied {
		t.Fatal("apply 인데 Applied 가 거짓이다")
	}
	plan.Applied = true // Applied 만 다르면 나머지는 같아야 한다
	if fmt.Sprintf("%+v", plan) != fmt.Sprintf("%+v", done) {
		t.Fatalf("계획과 결과가 다르다\nplan=%+v\ndone=%+v", plan, done)
	}
	// amy 의 이름이 어느 표에도 남지 않아야 한다.
	for _, q := range []string{
		"SELECT COUNT(*) c FROM usage_sessions WHERE username='amy'",
		"SELECT COUNT(*) c FROM usage_series WHERE username='amy'",
		"SELECT COUNT(*) c FROM usage_counters WHERE username='amy'",
		"SELECT COUNT(*) c FROM usage_recommendations WHERE username='amy'",
		"SELECT COUNT(*) c FROM machine_identity WHERE username='amy'",
		// 세션이 지워졌는데 자식 행만 남는 것(고아 발생)도 없어야 한다.
		"SELECT COUNT(*) c FROM usage_series WHERE session_id IN ('a1','a2')",
		"SELECT COUNT(*) c FROM usage_counters WHERE session_id IN ('a1','a2')",
	} {
		if n := countRows(t, ctx, d, q); n != 0 {
			t.Errorf("amy 의 행이 남았다(%d): %s", n, q)
		}
	}
}

// ⑬ **다른 사용자의 행은 건드리지 않는다.** 이 명령이 지켜야 할 가장 중요한 성질이다.
func TestPurgeLeavesOtherUsersAlone(t *testing.T) {
	ctx, d := maintDB(t)
	purgeSeed(t, ctx, d)

	if _, err := purgeUsageRows(ctx, d,
		purgeOptions{Scope: purgeByUser, Target: "amy", Apply: true}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// bob 은 세션 1 · series 2(b1 + 고아) · counters 1 · reco 1 · mapping 1 이 그대로다.
	got := snapshotCounts(t, ctx, d)
	want := tableCounts{Sessions: 1, Series: 2, Counters: 1, Recos: 1, Mappings: 1}
	if got != want {
		t.Fatalf("bob 의 행이 변했다\n got=%+v\nwant=%+v", got, want)
	}
	for _, q := range []string{
		"SELECT COUNT(*) c FROM usage_sessions WHERE username='bob'",
		"SELECT COUNT(*) c FROM usage_series WHERE username='bob' AND session_id='b1'",
		"SELECT COUNT(*) c FROM usage_series WHERE session_id='orph-bob'",
		"SELECT COUNT(*) c FROM usage_counters WHERE username='bob'",
		"SELECT COUNT(*) c FROM usage_recommendations WHERE username='bob'",
		"SELECT COUNT(*) c FROM machine_identity WHERE username='bob'",
	} {
		if n := countRows(t, ctx, d, q); n != 1 {
			t.Errorf("bob 의 행이 %d 개다(1 이어야 한다): %s", n, q)
		}
	}
}

/*
 * ⑭ **한 트랜잭션** — 중간에 끊기면 부분 삭제가 남지 않는다.
 *
 * 왜 결함을 주입하나: 표 다섯을 지우는 도중 실패를 자연스럽게 만들 수단이 없다. 그런데 이 성질은
 * 사고가 났을 때만 관측되는 성질이라, 검증하지 않으면 "세션은 없는데 카운터가 남은" 상태를
 * 아무도 못 잡는다 — 그 상태는 화면에서 진단이 거의 불가능하다(고아 버킷은 집계에서 빠진다).
 */
func TestPurgeIsOneTransaction(t *testing.T) {
	ctx, d := maintDB(t)
	purgeSeed(t, ctx, d)
	before := snapshotCounts(t, ctx, d)

	boom := errors.New("주입된 결함")
	var touched []string
	dirty := tableCounts{}
	_, err := purgeUsageRows(ctx, d, purgeOptions{
		Scope: purgeByUser, Target: "amy", Apply: true,
		faultBeforeDelete: func(txCtx context.Context, table string) error {
			touched = append(touched, table)
			// 셋째 표에서 끊는다 — 앞의 둘은 이미 지워진 상태여야 롤백이 의미를 갖는다.
			if len(touched) < 3 {
				return nil
			}
			// **트랜잭션 안에서** 본다. 이 확인이 없으면 삭제가 아예 안 일어나도 아래
			// before==after 가 성립해 테스트가 공허하게 통과한다.
			dirty = snapshotCounts(t, txCtx, d)
			return boom
		},
	})
	if !errors.Is(err, boom) {
		t.Fatalf("주입한 결함이 전파되지 않았다: %v", err)
	}
	if len(touched) < 3 {
		t.Fatalf("표본이 잘못됐다 — 표 셋에 닿지 못했다: %v", touched)
	}
	// 끊긴 시점에는 usage_series·usage_counters 가 이미 줄어 있어야 한다(= 반쪽 상태였다).
	if dirty.Series != before.Series-3 || dirty.Counters != before.Counters-4 {
		t.Fatalf("끊긴 시점이 반쪽 상태가 아니었다 — 롤백 검증이 공허하다\nbefore=%+v\ndirty=%+v",
			before, dirty)
	}
	if after := snapshotCounts(t, ctx, d); after != before {
		t.Fatalf("중간 실패인데 부분 삭제가 남았다\nbefore=%+v\n after=%+v", before, after)
	}
}

// ⑮ 멱등 — 두 번째 실행은 0행이고, 그 뒤 dry-run 도 0행이다.
func TestPurgeIdempotent(t *testing.T) {
	ctx, d := maintDB(t)
	purgeSeed(t, ctx, d)

	first, err := purgeUsageRows(ctx, d, purgeOptions{Scope: purgeByUser, Target: "amy", Apply: true})
	if err != nil {
		t.Fatalf("1회차: %v", err)
	}
	if first.Total() == 0 {
		t.Fatal("1회차가 아무것도 지우지 않았다 — 표본이 잘못됐다")
	}
	second, err := purgeUsageRows(ctx, d, purgeOptions{Scope: purgeByUser, Target: "amy", Apply: true})
	if err != nil {
		t.Fatalf("2회차: %v", err)
	}
	if second.Total() != 0 {
		t.Fatalf("2회차가 0행이 아니다: %+v", second)
	}
	plan, err := purgeUsageRows(ctx, d, purgeOptions{Scope: purgeByUser, Target: "amy"})
	if err != nil {
		t.Fatalf("2회차 dry-run: %v", err)
	}
	if plan.Total() != 0 {
		t.Fatalf("삭제 후 dry-run 이 0행이 아니다: %+v", plan)
	}
}

/*
 * ⑯ `①+②+③ == Totals` 불변식은 **삭제 뒤에도** 성립한다.
 *
 * placeholder-models 는 숫자를 안 움직이므로 "before == after" 를 봤지만, 이 명령은 숫자를
 * 줄인다. 그러니 볼 것은 "줄었는가"가 아니라 **모델 축 합과 Totals 가 여전히 일치하는가** 다.
 * 세션만 지우고 버킷을 남기면(또는 반대면) 바로 여기서 갈린다.
 */
func TestPurgePreservesModelAxisInvariant(t *testing.T) {
	ctx, d := maintDB(t)
	purgeSeed(t, ctx, d)

	before := modelAxisSum(t, ctx) // 불변식이 심은 직후에도 성립함을 먼저 확인한다
	if _, err := purgeUsageRows(ctx, d,
		purgeOptions{Scope: purgeByUser, Target: "amy", Apply: true}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	after := modelAxisSum(t, ctx) // modelAxisSum 이 내부에서 ①+②+③ == Totals 를 못 박는다

	if after == before {
		t.Fatal("삭제했는데 합이 그대로다 — 표본이나 삭제 범위가 잘못됐다")
	}
	// 남은 것은 bob 의 몫뿐이어야 한다(세션 7/8 · series 7/8 → ① 이 7/8, ③ 은 0).
	if after.Input != 7 || after.Output != 8 {
		t.Fatalf("남은 합이 bob 의 몫이 아니다: %+v", after)
	}
}

// ⑰ 머신 단위 — 계정이 붙지 않은(username NULL) 행은 **머신으로만** 지목할 수 있다.
func TestPurgeByMachine(t *testing.T) {
	ctx, d := maintDB(t)
	// 귀속이 비어 있는 세션 — --user 로는 손잡이가 없다.
	insertSessionAs(t, ctx, d, "n1", "", "pc-orphan", "claude-opus-4-8", 10, 20)
	insertSeries(t, ctx, d, "n1", "2026-08-01T03", "claude-opus-4-8", seriesRow{
		Input: 10, Output: 20, Turns: 1, Machine: "pc-orphan"})
	insertCounter(t, ctx, d, "n1", "tool", "Read", "", "pc-orphan", 2)
	insertSessionAs(t, ctx, d, "b1", "bob", "pc-bob", "gpt-5.6", 7, 8)

	rep, err := purgeUsageRows(ctx, d,
		purgeOptions{Scope: purgeByMachine, Target: "pc-orphan", Apply: true})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := purgeTableRows(rep, "usage_sessions"); got != 1 {
		t.Errorf("세션 1행이어야 한다: %d", got)
	}
	if got := purgeTableRows(rep, "usage_series"); got != 1 {
		t.Errorf("버킷 1행이어야 한다: %d", got)
	}
	if got := purgeTableRows(rep, "usage_counters"); got != 1 {
		t.Errorf("카운터 1행이어야 한다: %d", got)
	}
	// usage_recommendations 는 머신 컬럼이 없다 — 조용히 0 이 아니라 **건너뛴 이유**를 말해야 한다.
	var skipped string
	for _, tb := range rep.Tables {
		if tb.Table == "usage_recommendations" {
			skipped = tb.Skipped
		}
	}
	if skipped == "" {
		t.Error("usage_recommendations 를 건너뛴 이유가 보고에 없다")
	}
	if n := countRows(t, ctx, d, "SELECT COUNT(*) c FROM usage_sessions"); n != 1 {
		t.Fatalf("bob 의 세션까지 지웠다: %d", n)
	}
}

/*
 * ⑱ 자식 표의 귀속이 낡아 있어도 빠뜨리지 않는다.
 *
 * 실재하는 드리프트다: identity.Restamp 는 usage_sessions·usage_counters 만 재스탬프하고
 * **usage_series 는 건드리지 않는다.** 그래서 세션은 amy 인데 그 세션의 버킷은 옛 OS 계정명을
 * 지닌 행이 남는다. 이름으로만 좁히면 그 버킷이 그대로 남아 화면에 옛 이름이 계속 보인다 —
 * 그래서 **세션으로 되짚는** 조건이 계약이다.
 */
func TestPurgeCatchesStaleChildAttribution(t *testing.T) {
	ctx, d := maintDB(t)
	insertSessionAs(t, ctx, d, "a1", "amy", "pc-amy", "claude-opus-4-8", 100, 200)
	// 버킷의 username 은 재스탬프 전의 옛 이름이다.
	insertSeries(t, ctx, d, "a1", "2026-08-01T03", "claude-opus-4-8", seriesRow{
		Input: 100, Output: 200, Turns: 1, Username: "sh.ahn", Machine: "pc-amy"})
	insertCounter(t, ctx, d, "a1", "tool", "Read", "", "pc-amy", 1) // username NULL

	if _, err := purgeUsageRows(ctx, d,
		purgeOptions{Scope: purgeByUser, Target: "amy", Apply: true}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if n := countRows(t, ctx, d, "SELECT COUNT(*) c FROM usage_series"); n != 0 {
		t.Fatalf("옛 이름을 지닌 버킷이 남았다: %d", n)
	}
	if n := countRows(t, ctx, d, "SELECT COUNT(*) c FROM usage_counters"); n != 0 {
		t.Fatalf("귀속이 빈 카운터가 남았다: %d", n)
	}
}

/*
 * ⑱' 반대 방향 — 낡은 이름으로 지목해도 **남의 살아 있는 세션**에서 행을 뽑아 가지 않는다.
 *
 * 자식 표의 byName 조건을 고아로 한정하지 않으면 여기서 갈린다: 버킷의 username 이 옛
 * OS 계정명(sh.ahn)인데 그 세션은 amy 의 것이므로, 한정 없이 지우면 amy 의 세션에 버킷 없는
 * 구멍이 생긴다 — 세션 총합은 남는데 시간·모델 축에서만 사라지는, 진단이 어려운 손상이다.
 *
 * 낡은 이름을 정리하는 수단은 삭제가 아니라 **귀속 교정**이다(README 의 (a)).
 */
func TestPurgeByStaleNameSparesLiveSessionsOfOthers(t *testing.T) {
	ctx, d := maintDB(t)
	insertSessionAs(t, ctx, d, "a1", "amy", "pc-amy", "claude-opus-4-8", 100, 200)
	insertSeries(t, ctx, d, "a1", "2026-08-01T03", "claude-opus-4-8", seriesRow{
		Input: 100, Output: 200, Turns: 1, Username: "sh.ahn", Machine: "pc-amy"})
	insertCounter(t, ctx, d, "a1", "tool", "Read", "sh.ahn", "pc-amy", 1)

	rep, err := purgeUsageRows(ctx, d,
		purgeOptions{Scope: purgeByUser, Target: "sh.ahn", Apply: true})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if rep.Total() != 0 {
		t.Fatalf("남의 살아 있는 세션의 행을 지웠다: %+v", rep)
	}
	got := snapshotCounts(t, ctx, d)
	want := tableCounts{Sessions: 1, Series: 1, Counters: 1}
	if got != want {
		t.Fatalf("amy 의 세션이 손상됐다\n got=%+v\nwant=%+v", got, want)
	}
}

/*
 * ⑲ **감사 로그와 계정·자격 표는 남긴다.**
 *
 * usage_audit: 이 레포는 그 표를 "어제 보던 이름이 왜 오늘 다른가에 답하는 표"라며 기한을 두지
 * 않기로 했다(identity/audit.go ③). 삭제의 부수효과로 그 근거를 함께 지우면, 방금 지운 이유를
 * 나중에 아무도 답할 수 없다.
 * 계정·자격 표: 계정과 인제스트 키 회수는 사용자 관리 API 가 소유한다(docs §9). 사용량 행을
 * 지우는 명령이 계정까지 조용히 지우면 두 소유자가 같은 표를 각자 지우게 된다.
 */
func TestPurgeKeepsAuditAndAccountTables(t *testing.T) {
	ctx, d := maintDB(t)
	purgeSeed(t, ctx, d)
	exec(t, ctx, d,
		"INSERT INTO usage_audit (at, actor, action, target, detail)"+
			" VALUES ('2026-08-01T00:00:00.000Z','ops','identity.set','pc-amy','{\"username\":\"amy\"}')")
	exec(t, ctx, d, "INSERT INTO team_members (username, team) VALUES ('amy','플랫폼')")
	exec(t, ctx, d,
		"INSERT INTO member_tokens (token_hash, username, created_at)"+
			" VALUES ('h1','amy','2026-08-01T00:00:00.000Z')")
	exec(t, ctx, d,
		"INSERT INTO auth_users (username, password_hash, role, created_at)"+
			" VALUES ('amy','x','member','2026-08-01T00:00:00.000Z')")
	exec(t, ctx, d,
		"INSERT INTO auth_sessions (token_hash, username, role, expires_at, created_at)"+
			" VALUES ('s1','amy','member','2099-01-01T00:00:00.000Z','2026-08-01T00:00:00.000Z')")

	if _, err := purgeUsageRows(ctx, d,
		purgeOptions{Scope: purgeByUser, Target: "amy", Apply: true}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	for _, q := range []string{
		"SELECT COUNT(*) c FROM usage_audit",
		"SELECT COUNT(*) c FROM team_members WHERE username='amy'",
		"SELECT COUNT(*) c FROM member_tokens WHERE username='amy'",
		"SELECT COUNT(*) c FROM auth_users WHERE username='amy'",
		"SELECT COUNT(*) c FROM auth_sessions WHERE username='amy'",
	} {
		if n := countRows(t, ctx, d, q); n != 1 {
			t.Errorf("남겨야 할 행이 사라졌다(%d): %s", n, q)
		}
	}
}

/*
 * ⑳ 스키마 드리프트 방어 — 귀속 컬럼(username·machine)을 가진 **모든** 표가 "지운다"와
 * "남긴다" 중 하나로 분류돼 있어야 한다.
 *
 * 표가 하나 늘었는데 여기 분류를 빠뜨리면, 그 표에 계정명·머신명이 계속 남아 이 명령이 목적을
 * 달성하지 못한다. 그건 삭제를 돌려본 사람에게는 "지웠는데 화면에 이름이 남아 있다"로 보이고,
 * 원인을 찾을 단서가 없다 — 그래서 스키마를 직접 물어본다.
 */
func TestPurgeClassifiesEveryIdentityTable(t *testing.T) {
	ctx, d := maintDB(t)
	// ingest_keys·orgs 까지 있어야 분류 누락을 실제로 잡는다.
	if err := org.Init(ctx, d); err != nil {
		t.Fatalf("org.Init: %v", err)
	}

	classified := map[string]string{}
	for _, sel := range purgeSelections(purgeByUser) {
		classified[sel.table] = "지운다"
	}
	for _, tb := range purgeKeepTables {
		if prev, dup := classified[tb]; dup {
			t.Fatalf("표 %q 가 %s 와 남긴다 에 중복 분류됐다", tb, prev)
		}
		classified[tb] = "남긴다"
	}

	tables, err := d.Query(ctx,
		"SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}
	if len(tables) == 0 {
		t.Fatal("표 목록을 읽지 못했다")
	}
	var missing []string
	seen := map[string]bool{}
	for _, tr := range tables {
		name := tr.Str("name")
		cols, err := d.Query(ctx, "PRAGMA table_info("+name+")")
		if err != nil {
			t.Fatalf("PRAGMA %s: %v", name, err)
		}
		hasIdentity := false
		for _, c := range cols {
			switch strings.ToLower(c.Str("name")) {
			case "username", "machine":
				hasIdentity = true
			}
		}
		if !hasIdentity {
			continue
		}
		seen[name] = true
		if _, ok := classified[name]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("귀속 컬럼을 가졌는데 분류되지 않은 표: %v — "+
			"maintenance.go 의 purgeSelections 또는 purgeKeepTables 를 갱신하라", missing)
	}
	var extra []string
	for name := range classified {
		if !seen[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	if len(extra) > 0 {
		t.Fatalf("스키마에 없거나 귀속 컬럼이 없는 표가 분류돼 있다: %v", extra)
	}
}

// ㉑ CLI 표면 — 기본은 dry-run, 대상 지목은 정확히 하나, 인자 오류는 2 다.
func TestPurgeCmd(t *testing.T) {
	ctx, d := maintDB(t)
	purgeSeed(t, ctx, d)
	var out bytes.Buffer

	// 대상을 지목하지 않으면 거부한다 — "전부 지운다"로 접히면 안 된다.
	if rc := cleanupCmd(ctx, d, &out, []string{"usage-rows"}); rc != 2 {
		t.Fatalf("대상 없이 rc=%d 여야 한다(2) out=%s", rc, out.String())
	}
	// 둘을 동시에 주는 것도 거부한다 — 무엇을 기준으로 지웠는지가 모호해진다.
	if rc := cleanupCmd(ctx, d, &out, []string{"usage-rows", "--user", "amy", "--machine", "pc-amy"}); rc != 2 {
		t.Fatalf("둘 다 주면 rc=%d 여야 한다(2)", rc)
	}
	// 공백만 있는 대상도 거부한다.
	if rc := cleanupCmd(ctx, d, &out, []string{"usage-rows", "--user", "   "}); rc != 2 {
		t.Fatalf("공백 대상 rc=%d 여야 한다(2)", rc)
	}
	if n := countRows(t, ctx, d, "SELECT COUNT(*) c FROM usage_sessions"); n != 3 {
		t.Fatalf("거부 경로가 DB 를 바꿨다: %d", n)
	}

	out.Reset()
	if rc := cleanupCmd(ctx, d, &out, []string{"usage-rows", "--user", "amy"}); rc != 0 {
		t.Fatalf("dry-run rc=%d out=%s", rc, out.String())
	}
	for _, want := range []string{"dry-run", "되돌릴 수 없다", "username=amy", "usage_sessions", "--apply"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("dry-run 출력에 %q 가 없다:\n%s", want, out.String())
		}
	}
	if n := countRows(t, ctx, d, "SELECT COUNT(*) c FROM usage_sessions"); n != 3 {
		t.Fatalf("dry-run 이 DB 를 바꿨다: %d", n)
	}

	out.Reset()
	if rc := cleanupCmd(ctx, d, &out, []string{"usage-rows", "--user", "amy", "--apply"}); rc != 0 {
		t.Fatalf("apply rc=%d out=%s", rc, out.String())
	}
	if n := countRows(t, ctx, d, "SELECT COUNT(*) c FROM usage_sessions WHERE username='amy'"); n != 0 {
		t.Fatalf("--apply 가 아무것도 지우지 않았다: %d", n)
	}
	if !strings.Contains(out.String(), "실제로 지웠다") {
		t.Errorf("apply 출력이 실행을 밝히지 않는다:\n%s", out.String())
	}
}

/*
 * ㉒ 빈 대상은 **함수 층에서도** 거부다.
 *
 * CLI 가 이미 걸러 주지만(㉑) 그 검사에 의존하면 이 함수를 다른 자리에서 부르는 날 —
 * 예를 들어 나중에 관리 API 가 붙는 날 — 빈 문자열이 "전부 삭제"로 통과한다. 되돌릴 수 없는
 * 동작의 방어선은 호출부가 아니라 그 동작 자체에 있어야 한다(identity.Set 과 같은 규율).
 */
func TestPurgeRejectsEmptyTarget(t *testing.T) {
	ctx, d := maintDB(t)
	purgeSeed(t, ctx, d)
	before := snapshotCounts(t, ctx, d)

	for _, target := range []string{"", "   ", "\t\n"} {
		for _, apply := range []bool{false, true} {
			_, err := purgeUsageRows(ctx, d,
				purgeOptions{Scope: purgeByUser, Target: target, Apply: apply})
			if !errors.Is(err, errPurgeTargetRequired) {
				t.Fatalf("빈 대상(%q · apply=%v)이 거부되지 않았다: %v", target, apply, err)
			}
		}
	}
	if after := snapshotCounts(t, ctx, d); after != before {
		t.Fatalf("거부 경로가 DB 를 바꿨다\nbefore=%+v\n after=%+v", before, after)
	}
}

// ㉓ 지울 것이 없으면 그렇게 말한다 — 빈 DB·없는 이름에서 오류가 나면 안 된다.
func TestPurgeOnCleanDB(t *testing.T) {
	ctx, d := maintDB(t)
	var out bytes.Buffer
	if rc := cleanupCmd(ctx, d, &out, []string{"usage-rows", "--user", "nobody", "--apply"}); rc != 0 {
		t.Fatalf("rc=%d out=%s", rc, out.String())
	}
	if !strings.Contains(out.String(), "지울 행이 없다") {
		t.Fatalf("빈 결과 안내가 없다: %s", out.String())
	}
}
