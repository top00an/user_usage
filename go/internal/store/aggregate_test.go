package store

import (
	"testing"
)

// 사람·모델·일별로 나뉘고 **캐시읽기가 입력과 분리된다**(합치면 비용이 왜곡된다).
func TestAggregatesSplitUserModelDayAndKeepCacheSeparate(t *testing.T) {
	ctx := fresh(t)
	mustSession(t, ctx, SessionInput{SessionID: "s1", Machine: "pc-1", Username: "user-a",
		Model: mOpus, StartedAt: "2026-08-03T09:00:00.000Z",
		Input: 10, Output: 2000, CacheRead: 90000, CacheCreate: 500, Turns: 30})
	mustSession(t, ctx, SessionInput{SessionID: "s2", Machine: "pc-2", Username: "kim",
		Model: mSonnet, StartedAt: "2026-08-02T09:00:00.000Z",
		Input: 5, Output: 100, CacheRead: 7, CacheCreate: 1, Turns: 3})

	byUser, err := UsageByUser(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(byUser) != 2 {
		t.Fatalf("사용자 수 %d", len(byUser))
	}
	var a UserRow
	for _, r := range byUser {
		if r.Username == "user-a" {
			a = r
		}
	}
	if a.CacheRead != 90000 {
		t.Fatalf("cacheRead=%d", a.CacheRead)
	}
	if a.Input != 10 {
		t.Fatalf("캐시읽기가 입력에 합산되면 비용이 왜곡된다: input=%d", a.Input)
	}
	// 출력 내림차순 정렬이다.
	if byUser[0].Username != "user-a" {
		t.Fatalf("정렬이 출력 내림차순이 아니다: %+v", byUser)
	}

	models, _ := UsageByModel(ctx)
	if len(models) != 2 {
		t.Fatalf("모델 수 %d", len(models))
	}
	days, err := UsageByDay(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 2 || days[0].Day != "2026-08-03" || days[1].Day != "2026-08-02" {
		t.Fatalf("일별이 최신순이 아니다: %+v", days)
	}
}

// 사용자명이 비면 '(미상)' 으로 모인다 — 빈 키를 만들지 않는다.
func TestUsageByUserUnknownBucket(t *testing.T) {
	ctx := fresh(t)
	mustSession(t, ctx, SessionInput{SessionID: "s", StartedAt: "2026-08-03T09:00:00.000Z", Output: 5})
	rows, _ := UsageByUser(ctx)
	if len(rows) != 1 || rows[0].Username != UnknownModel {
		t.Fatalf("%+v", rows)
	}
}

/*
 * 클램프 규칙은 경계에서 갈린다 — Node 의 `Math.max(lo, Math.min(hi, v || dflt))` 를 그대로 옮겼다.
 * 0 은 기본값으로 가고, 음수는 기본값이 아니라 **하한**으로 간다.
 */
func TestClampIntMatchesNodeSemantics(t *testing.T) {
	for _, tc := range []struct{ v, lo, hi, dflt, want int }{
		{0, 1, 365, 30, 30},    // 0 → 기본값
		{-5, 1, 365, 30, 1},    // 음수 → 하한(기본값이 아니다)
		{500, 1, 365, 30, 365}, // 상한
		{7, 1, 365, 30, 7},
		{0, 1, 200, 20, 20},
		{0, 1, 50, 8, 8},
	} {
		if got := clampInt(tc.v, tc.lo, tc.hi, tc.dflt); got != tc.want {
			t.Errorf("clampInt(%d,%d,%d,%d) want %d, got %d", tc.v, tc.lo, tc.hi, tc.dflt, tc.want, got)
		}
	}
}

func TestUsageByDayClampsRange(t *testing.T) {
	ctx := fresh(t)
	for i := 0; i < 3; i++ {
		mustSession(t, ctx, SessionInput{
			SessionID: string(rune('a' + i)),
			StartedAt: []string{"2026-08-01", "2026-08-02", "2026-08-03"}[i] + "T09:00:00.000Z",
			Output:    10,
		})
	}
	// 음수는 하한 1 로 접힌다 — 기본값 30 으로 되돌아가지 않는다.
	rows, _ := UsageByDay(ctx, -5)
	if len(rows) != 1 {
		t.Fatalf("음수 days 가 하한으로 접히지 않았다: %d", len(rows))
	}
}

// 모르는 축은 빈 결과다(오류가 아니다) — 화면에 정체불명 행이 생기지 않는다.
func TestTopKeysAndByUserRejectUnknownKind(t *testing.T) {
	ctx := fresh(t)
	if rows, err := TopKeys(ctx, "bogus", 10); err != nil || len(rows) != 0 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if rows, err := ByUser(ctx, "bogus", 10); err != nil || len(rows) != 0 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}

/*
 * ByUser — "누가 어떤 서브에이전트·스킬을 쓰는가". TopKeys 는 전체 합이라 이 차이를 못 보여준다.
 */
func TestByUserSplitsPerUserAndCapsItems(t *testing.T) {
	ctx := fresh(t)
	seed := func(sid, user string, rows ...CounterRow) {
		mustCounters(t, ctx, CountersInput{SessionID: sid, Username: user, Machine: "m",
			StartedAt: "2026-08-03T00:00:00.000Z", Rows: rows})
	}
	seed("s1", "user-a",
		CounterRow{Kind: "agent", Key: "backend-engineer", Count: 25},
		CounterRow{Kind: "agent", Key: "qa-engineer", Count: 16})
	seed("s2", "user-b",
		CounterRow{Kind: "agent", Key: "general-purpose", Count: 25},
		CounterRow{Kind: "agent", Key: "Explore", Count: 10})

	rows, err := ByUser(ctx, "agent", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("%+v", rows)
	}
	// 총합 내림차순 — user-a 41 > user-b 35
	if rows[0].Username != "user-a" || rows[0].Total != 41 {
		t.Fatalf("정렬/총합이 틀렸다: %+v", rows[0])
	}
	if len(rows[0].Items) != 1 {
		t.Fatalf("limit 이 안 걸렸다: %+v", rows[0].Items)
	}
	// Total 은 자르기 **전** 전체 합이다 — 자른 뒤로 세면 화면이 과소 보고한다.
	if rows[1].Total != 35 {
		t.Fatalf("user-b total=%d", rows[1].Total)
	}
}

// 발신처별 커버리지 — series 를 보내는 머신을 구분한다.
func TestReporterCoverage(t *testing.T) {
	ctx := fresh(t)
	mustSession(t, ctx, SessionInput{SessionID: "s1", Machine: "pc-new", Username: "u1",
		StartedAt: "2026-08-03T09:00:00.000Z", Output: 1})
	mustSeries(t, ctx, SeriesInput{SessionID: "s1", Machine: "pc-new", Username: "u1",
		Rows: []SeriesRow{{Hour: "2026-08-03T09", Model: mOpus, Output: 1}}})
	mustSession(t, ctx, SessionInput{SessionID: "s2", Machine: "pc-old", Username: "u2",
		StartedAt: "2026-08-03T08:00:00.000Z", Output: 1})

	rows, err := ReporterCoverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range rows {
		got[r.Machine] = r.SendsSeries
	}
	if !got["pc-new"] || got["pc-old"] {
		t.Fatalf("series 발신 여부가 틀렸다: %v", got)
	}
	if len(rows) != 2 {
		t.Fatalf("%+v", rows)
	}
}

/*
 * 추천 공백 — 매칭이 약한 목표의 **반복** 토큰만 올라온다.
 * 1회성 목표는 공백의 증거가 못 된다.
 */
func TestRecommendationGaps(t *testing.T) {
	ctx := fresh(t)
	add := func(score float64, tokens ...string) {
		if err := RecommendationAdd(ctx, RecoInput{GoalTokens: tokens, Score: score, Source: "mcp"}); err != nil {
			t.Fatal(err)
		}
	}
	add(0, "쿠버네티스", "비용")
	add(0, "쿠버네티스", "모니터링")
	add(0, "일회성단어")

	gaps, err := RecommendationGaps(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 1 || gaps[0].Token != "쿠버네티스" || gaps[0].Count != 2 {
		t.Fatalf("%+v", gaps)
	}
}

func TestRecommendationGapsSkipWellMatched(t *testing.T) {
	ctx := fresh(t)
	for _, tk := range [][]string{{"보안", "리뷰"}, {"보안", "점검"}} {
		if err := RecommendationAdd(ctx, RecoInput{GoalTokens: tk, Agent: "security-reviewer",
			Score: 9, Source: "mcp"}); err != nil {
			t.Fatal(err)
		}
	}
	gaps, _ := RecommendationGaps(ctx, 0)
	if len(gaps) != 0 {
		t.Fatalf("매칭이 잘 된 목표가 공백에 들어갔다: %+v", gaps)
	}
}

/*
 * 조사를 뗀 어휘로 묶인다 — 클라이언트 키워드 축과 같은 규칙.
 * '스코프로'/'스코프를' 이 따로 세어지면 상위권이 흩어져 공백이 드러나지 않는다.
 */
func TestRecommendationGapsStripParticles(t *testing.T) {
	ctx := fresh(t)
	for _, tk := range []string{"스코프로", "스코프를"} {
		if err := RecommendationAdd(ctx, RecoInput{GoalTokens: []string{tk}, Score: 0}); err != nil {
			t.Fatal(err)
		}
	}
	gaps, _ := RecommendationGaps(ctx, 0)
	if len(gaps) != 1 || gaps[0].Token != "스코프" || gaps[0].Count != 2 {
		t.Fatalf("%+v", gaps)
	}
}

/*
 * 3글자 토큰에는 '이'·'가'를 떼지 않는다 — '고양이'→'고양' 같은 오절단을 막는다.
 * 4글자 이상은 어간이 충분히 남아 안전하다.
 */
func TestStripParticleShortTokensKeepIGa(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"고양이", "고양이"},
		{"어린이", "어린이"},
		{"스코프로", "스코프"},
		{"스코프를", "스코프"},
		{"프로젝트에서", "프로젝트"},
		{"abc", "abc"},     // 한글이 아니면 건드리지 않는다
		{"가", "가"},         // 너무 짧다
		{"쿠버네티스", "쿠버네티스"}, // 뗄 조사가 없다
	} {
		if got := stripParticle(tc.in); got != tc.want {
			t.Errorf("stripParticle(%q) want %q, got %q", tc.in, tc.want, got)
		}
	}
}

// 요약은 **점수 0 만** 실패로 센다.
func TestRecommendationSummary(t *testing.T) {
	ctx := fresh(t)
	_ = RecommendationAdd(ctx, RecoInput{GoalTokens: []string{"a"}, Score: 0})
	_ = RecommendationAdd(ctx, RecoInput{GoalTokens: []string{"b"}, Score: 3})
	s, err := RecommendationSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s != (RecoSummary{Total: 2, Miss: 1}) {
		t.Fatalf("%+v", s)
	}
}

// 빈 저장소에서도 집계가 성립한다(0 이지 오류가 아니다).
func TestAggregatesOnEmptyStore(t *testing.T) {
	ctx := fresh(t)
	if tot, err := Totals(ctx); err != nil || tot.Sessions != 0 {
		t.Fatalf("tot=%+v err=%v", tot, err)
	}
	if s, err := RecommendationSummary(ctx); err != nil || s != (RecoSummary{}) {
		t.Fatalf("s=%+v err=%v", s, err)
	}
	for _, f := range []func() (int, error){
		func() (int, error) { r, e := UsageByDay(ctx, 30); return len(r), e },
		func() (int, error) { r, e := UsageByUser(ctx); return len(r), e },
		func() (int, error) { r, e := ReporterCoverage(ctx); return len(r), e },
		func() (int, error) { r, e := TopKeys(ctx, "bash", 20); return len(r), e },
		func() (int, error) { r, e := SessionRows(ctx, Filter{}); return len(r), e },
		func() (int, error) { r, e := SeriesRows(ctx, Filter{}); return len(r), e },
	} {
		n, err := f()
		if err != nil || n != 0 {
			t.Fatalf("n=%d err=%v", n, err)
		}
	}
}

// Init 전에 쓰면 nil 역참조로 죽는 대신 말이 되는 오류를 준다.
func TestUninitializedStoreErrors(t *testing.T) {
	prev := handle
	handle = nil
	defer func() { handle = prev }()
	if _, err := Totals(t.Context()); err == nil {
		t.Fatal("Init 전인데 통과했다")
	}
}
