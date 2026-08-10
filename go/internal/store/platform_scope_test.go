package store

import (
	"context"
	"testing"
)

/*
 * platform 축을 **세션 기반이 아닌 집계까지** 밀어 넣은 것의 계약.
 *
 * 여기서 재는 것은 세 가지이고, 셋이 모두 성립해야 이 축이 믿을 만하다:
 *
 *	① 미지정 = 현행 그대로   — 조건을 만들지 않는다(골든 44개 무회귀의 근거)
 *	② 분할    — claude + codex = 전체
 *	③ 배타    — 한쪽 필터에 다른 쪽의 값이 한 톨도 섞이지 않는다
 *
 * ②만 재면 안 된다. 필터가 통째로 무시돼도(=양쪽 다 전체를 돌려줘도) 합은 "전체의 2배"가 되어
 * 눈에 띄지만, 합계가 0 인 축에서는 그것도 통과한다. ③이 그 구멍을 막는다.
 */

// scopeSeed 는 두 플랫폼의 사실을 겹치지 않게 심는다.
//
// 값을 자릿수로 갈라 둔 이유: 합이 맞는지만 보면 두 몫이 뒤바뀌어도 통과한다.
// claude 는 백~천 단위, codex 는 일~십 단위라 어느 쪽이 샜는지 숫자만 보고 안다.
func scopeSeed(t *testing.T) context.Context {
	t.Helper()
	ctx := fresh(t)

	// claude — series 가 있는 세션(①경로) 하나, 없는 세션(②경로) 하나.
	mustSession(t, ctx, SessionInput{
		SessionID: "c1", Username: "alice", Machine: "m1", Model: "claude-opus-4-8", Platform: "claude",
		Input: 1000, Output: 2000, CacheRead: 3000, CacheCreate: 4000, Turns: 10,
		StartedAt:  "2026-08-03T09:00:00.000Z",
		LinesAdded: 100, LinesRemoved: 50, EditsAccepted: 7, EditsRejected: 3,
	})
	mustSeries(t, ctx, SeriesInput{
		SessionID: "c1", Username: "alice", Machine: "m1",
		Rows: []SeriesRow{{Hour: "2026-08-03T09", Model: "claude-opus-4-8",
			Input: 1000, Output: 2000, CacheRead: 3000, CacheCreate: 4000, Turns: 10}},
	})
	mustSession(t, ctx, SessionInput{
		SessionID: "c2", Username: "alice", Machine: "m1", Model: "claude-sonnet-4-5", Platform: "claude",
		Input: 100, Output: 200, CacheRead: 300, CacheCreate: 400, Turns: 5,
		StartedAt:  "2026-08-04T09:00:00.000Z",
		LinesAdded: 10, LinesRemoved: 5, EditsAccepted: 1, EditsRejected: 1,
	})
	mustCounters(t, ctx, CountersInput{
		SessionID: "c1", Username: "alice", Machine: "m1", StartedAt: "2026-08-03T09:00:00.000Z",
		Rows: []CounterRow{
			{Kind: "agent", Key: "backend-engineer", Count: 30},
			{Kind: "skill", Key: "team-design", Count: 20},
		},
	})

	// codex — series 있는 세션 하나.
	mustSession(t, ctx, SessionInput{
		SessionID: "x1", Username: "bob", Machine: "m2", Model: "gpt-5-codex", Platform: "codex",
		Input: 10, Output: 20, CacheRead: 30, CacheCreate: 40, Turns: 2,
		StartedAt:  "2026-08-05T09:00:00.000Z",
		LinesAdded: 1, LinesRemoved: 2, EditsAccepted: 3, EditsRejected: 4,
	})
	mustSeries(t, ctx, SeriesInput{
		SessionID: "x1", Username: "bob", Machine: "m2",
		Rows: []SeriesRow{{Hour: "2026-08-05T09", Model: "gpt-5-codex",
			Input: 10, Output: 20, CacheRead: 30, CacheCreate: 40, Turns: 2}},
	})
	mustCounters(t, ctx, CountersInput{
		SessionID: "x1", Username: "bob", Machine: "m2", StartedAt: "2026-08-05T09:00:00.000Z",
		Rows: []CounterRow{
			{Kind: "agent", Key: "general-purpose", Count: 4},
			{Kind: "skill", Key: "team-design", Count: 1},
		},
	})
	return ctx
}

func TestTotalsPlatformScope(t *testing.T) {
	ctx := scopeSeed(t)

	all, err := Totals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// ① 미지정 = 현행. WithFilter(빈 필터) 도 같은 값이어야 한다 — 기존 함수가 새 함수의
	// 얇은 껍데기라는 사실을 여기서 못 박는다.
	same, err := TotalsWithFilter(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if same != all {
		t.Fatalf("빈 필터가 현행과 다르다: %+v vs %+v", same, all)
	}

	c, err := TotalsWithFilter(ctx, Filter{Platform: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	x, err := TotalsWithFilter(ctx, Filter{Platform: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	// ③ 배타 — 자릿수가 갈려 있어 섞이면 즉시 보인다.
	if c.Sessions != 2 || c.Input != 1100 || c.Output != 2200 || c.CacheRead != 3300 || c.CacheCreate != 4400 {
		t.Fatalf("claude totals: %+v", c)
	}
	if x.Sessions != 1 || x.Input != 10 || x.Output != 20 || x.CacheRead != 30 || x.CacheCreate != 40 {
		t.Fatalf("codex totals: %+v", x)
	}
	// ② 분할
	if c.Sessions+x.Sessions != all.Sessions || c.Input+x.Input != all.Input ||
		c.Output+x.Output != all.Output || c.CacheRead+x.CacheRead != all.CacheRead ||
		c.CacheCreate+x.CacheCreate != all.CacheCreate {
		t.Fatalf("합이 전체와 다르다: %+v + %+v != %+v", c, x, all)
	}
	// 사용자·머신은 distinct 라 합이 전체와 같을 이유가 없다(각 플랫폼이 다른 사람이면 같지만,
	// 겹치면 작아진다). 여기서는 사람이 갈려 있으므로 같아야 한다.
	if c.Users+x.Users != all.Users {
		t.Fatalf("사용자 수 분할: %d + %d != %d", c.Users, x.Users, all.Users)
	}
}

func TestUsageByDayPlatformScope(t *testing.T) {
	ctx := scopeSeed(t)

	all, err := UsageByDay(ctx, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("전체 일수 = %d (%+v)", len(all), all)
	}
	c, err := UsageByDayWithFilter(ctx, 30, Filter{Platform: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	x, err := UsageByDayWithFilter(ctx, 30, Filter{Platform: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if len(c) != 2 || len(x) != 1 {
		t.Fatalf("일별 분할: claude %d일 · codex %d일", len(c), len(x))
	}
	if x[0].Day != "2026-08-05" || x[0].Output != 20 {
		t.Fatalf("codex 일별: %+v", x)
	}
	var sum int64
	for _, r := range append(append([]DayRow{}, c...), x...) {
		sum += r.Output
	}
	var allSum int64
	for _, r := range all {
		allSum += r.Output
	}
	if sum != allSum {
		t.Fatalf("일별 output 합 %d != 전체 %d", sum, allSum)
	}
}

func TestUsageByUserPlatformScope(t *testing.T) {
	ctx := scopeSeed(t)

	c, err := UsageByUserWithFilter(ctx, Filter{Platform: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if len(c) != 1 || c[0].Username != "alice" || c[0].Output != 2200 {
		t.Fatalf("claude 사용자별: %+v", c)
	}
	x, err := UsageByUserWithFilter(ctx, Filter{Platform: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if len(x) != 1 || x[0].Username != "bob" || x[0].Output != 20 {
		t.Fatalf("codex 사용자별: %+v", x)
	}
	all, err := UsageByUser(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("미지정이 전체가 아니다: %+v", all)
	}
}

/*
 * 모델별은 세 경로의 합이라 필터가 한 경로만 타면 **총합이 조용히 줄어든다.**
 * 그래서 여기서는 행 값이 아니라 **불변식**(①+②+③ == 그 플랫폼의 Totals)을 잰다.
 */
func TestUsageByModelPlatformScopeKeepsInvariant(t *testing.T) {
	ctx := scopeSeed(t)

	for _, p := range []string{"claude", "codex"} {
		rows, err := UsageByModelWithFilter(ctx, Filter{Platform: p})
		if err != nil {
			t.Fatal(err)
		}
		tot, err := TotalsWithFilter(ctx, Filter{Platform: p})
		if err != nil {
			t.Fatal(err)
		}
		got := sumAxes(rows)
		if got.Input != tot.Input || got.Output != tot.Output ||
			got.CacheRead != tot.CacheRead || got.CacheCreate != tot.CacheCreate {
			t.Fatalf("%s: 모델별 합 %+v != Totals %+v — 사람에게는 '유실'로 보인다", p, got, tot)
		}
	}

	// 배타 — codex 필터에 claude 모델 행이 없다.
	x, err := UsageByModelWithFilter(ctx, Filter{Platform: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if len(x) != 1 || x[0].Model != "gpt-5-codex" {
		t.Fatalf("codex 모델 행: %+v", x)
	}
	// ①(series) 경로를 실제로 탔는지 — 전부 fromSession 이면 필터가 series 를 통째로 떨어뜨린 것이다.
	if x[0].FromSeries.Output != 20 {
		t.Fatalf("codex 정확값 경로가 비었다: %+v", x[0])
	}

	c, err := UsageByModelWithFilter(ctx, Filter{Platform: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if len(c) != 2 {
		t.Fatalf("claude 모델 행 수 = %d (%+v)", len(c), c)
	}
	// series 없는 세션(c2)의 몫은 fromSession 으로 남아야 한다 — 버리면 총합이 준다.
	if modelRow(t, c, "claude-sonnet-4-5").FromSession.Output != 200 {
		t.Fatalf("series 없는 세션의 몫이 사라졌다: %+v", c)
	}

	// 미지정은 현행 그대로.
	all, err := UsageByModel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("미지정 모델 행 수 = %d (%+v)", len(all), all)
	}
}

func TestUsageModelAxisPlatformScope(t *testing.T) {
	ctx := scopeSeed(t)

	c, err := UsageModelAxisWithFilter(ctx, Filter{Platform: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Sessions != 2 || c.WithSeries != 1 {
		t.Fatalf("claude 커버리지: %+v", c)
	}
	if len(c.Users) != 1 || c.Users[0].Username != "alice" {
		t.Fatalf("claude 사용자: %+v", c.Users)
	}
	x, err := UsageModelAxisWithFilter(ctx, Filter{Platform: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if x.Sessions != 1 || x.WithSeries != 1 {
		t.Fatalf("codex 커버리지: %+v", x)
	}
	all, err := UsageModelAxis(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c.Sessions+x.Sessions != all.Sessions || c.WithSeries+x.WithSeries != all.WithSeries {
		t.Fatalf("커버리지 합이 전체와 다르다: %+v + %+v != %+v", c, x, all)
	}
}

/*
 * 카운터 축(usage_counters)에는 platform 컬럼이 없다 — 세션으로 되짚어야 한다.
 * 되짚지 않으면 `platform=codex` 가 claude 의 서브에이전트 사용까지 얹어 돌려준다.
 */
func TestCounterAggregatesPlatformScope(t *testing.T) {
	ctx := scopeSeed(t)

	c, err := TopKeysWithFilter(ctx, "agent", 20, Filter{Platform: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if len(c) != 1 || c[0].Key != "backend-engineer" || c[0].Count != 30 {
		t.Fatalf("claude agent 상위: %+v", c)
	}
	x, err := TopKeysWithFilter(ctx, "agent", 20, Filter{Platform: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if len(x) != 1 || x[0].Key != "general-purpose" || x[0].Count != 4 {
		t.Fatalf("codex agent 상위: %+v", x)
	}
	all, err := TopKeys(ctx, "agent", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("미지정 agent 상위가 전체가 아니다: %+v", all)
	}

	// 같은 키가 양쪽에 있어도 갈려야 한다(team-design 은 claude 20 · codex 1).
	cs, err := TopKeysWithFilter(ctx, "skill", 20, Filter{Platform: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || cs[0].Count != 20 {
		t.Fatalf("claude skill: %+v", cs)
	}
	xs, err := TopKeysWithFilter(ctx, "skill", 20, Filter{Platform: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if len(xs) != 1 || xs[0].Count != 1 {
		t.Fatalf("codex skill: %+v", xs)
	}
	as, err := TopKeys(ctx, "skill", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(as) != 1 || as[0].Count != 21 {
		t.Fatalf("미지정 skill 합: %+v", as)
	}

	// ByUser(dispatch 의 근거)도 같은 규율.
	cu, err := ByUserWithFilter(ctx, "agent", 8, Filter{Platform: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cu) != 1 || cu[0].Username != "alice" || cu[0].Total != 30 {
		t.Fatalf("claude ByUser: %+v", cu)
	}
	xu, err := ByUserWithFilter(ctx, "agent", 8, Filter{Platform: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if len(xu) != 1 || xu[0].Username != "bob" || xu[0].Total != 4 {
		t.Fatalf("codex ByUser: %+v", xu)
	}
	au, err := ByUser(ctx, "agent", 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(au) != 2 {
		t.Fatalf("미지정 ByUser 가 전체가 아니다: %+v", au)
	}
}

func TestDevMetricsPlatformScope(t *testing.T) {
	ctx := scopeSeed(t)

	allTot, allDays, err := DevMetrics(ctx, 30)
	if err != nil {
		t.Fatal(err)
	}
	if allTot.LinesAdded != 111 || allTot.EditsRejected != 8 {
		t.Fatalf("전체 개발 지표: %+v", allTot)
	}
	if len(allDays) != 3 {
		t.Fatalf("전체 일수 = %d", len(allDays))
	}

	c, cDays, err := DevMetricsWithFilter(ctx, 30, Filter{Platform: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if c.LinesAdded != 110 || c.LinesRemoved != 55 || c.EditsAccepted != 8 || c.EditsRejected != 4 {
		t.Fatalf("claude 개발 지표: %+v", c)
	}
	if len(cDays) != 2 {
		t.Fatalf("claude 일수 = %d", len(cDays))
	}
	x, xDays, err := DevMetricsWithFilter(ctx, 30, Filter{Platform: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if x.LinesAdded != 1 || x.LinesRemoved != 2 || x.EditsAccepted != 3 || x.EditsRejected != 4 {
		t.Fatalf("codex 개발 지표: %+v", x)
	}
	if len(xDays) != 1 {
		t.Fatalf("codex 일수 = %d", len(xDays))
	}
	if c.LinesAdded+x.LinesAdded != allTot.LinesAdded ||
		c.LinesRemoved+x.LinesRemoved != allTot.LinesRemoved ||
		c.EditsAccepted+x.EditsAccepted != allTot.EditsAccepted ||
		c.EditsRejected+x.EditsRejected != allTot.EditsRejected {
		t.Fatalf("개발 지표 합이 전체와 다르다: %+v + %+v != %+v", c, x, allTot)
	}
}

/*
 * 고아 행 방어 — 세션 행이 없는 카운터·버킷은 플랫폼을 알 수 없다.
 *
 * 미지정일 때는 종전대로 **센다**(현행 동작이고 골든이 그 위에 있다). 필터가 걸리면 빠진다 —
 * 귀속을 모르는 것을 특정 플랫폼의 사실로 지어내지 않는다.
 */
func TestOrphanRowsAreNotAttributedToAPlatform(t *testing.T) {
	ctx := fresh(t)
	mustSession(t, ctx, SessionInput{
		SessionID: "c1", Username: "alice", Platform: "claude", Output: 1, Turns: 1,
		StartedAt: "2026-08-03T09:00:00.000Z",
	})
	// 세션 행이 없는 카운터(인테이크가 세션만 실패한 자리 — 라이브에 실재한다).
	mustCounters(t, ctx, CountersInput{
		SessionID: "ghost", Username: "ghost", StartedAt: "2026-08-03T09:00:00.000Z",
		Rows: []CounterRow{{Kind: "agent", Key: "backend-engineer", Count: 99}},
	})

	all, err := TopKeys(ctx, "agent", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Count != 99 {
		t.Fatalf("미지정이 고아 행을 떨어뜨렸다 — 현행 동작이 바뀌었다: %+v", all)
	}
	c, err := TopKeysWithFilter(ctx, "agent", 20, Filter{Platform: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if len(c) != 0 {
		t.Fatalf("고아 행이 claude 로 귀속됐다: %+v", c)
	}
}
