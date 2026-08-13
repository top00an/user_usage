package store

import (
	"testing"
)

/*
 * ── 사용자 필터 (Filter.Username) ─────────────────────────────────────────
 *
 * '사용 추적' 화면이 한 사람으로 좁힐 때 쓰는 축이다. 이 파일이 못 박는 것은 두 가지다:
 *
 *   ① **격리** — 고른 사람의 값만 돌아온다. 세션 축(sessionWhere)은 이미 그랬지만
 *      카운터 축(usage_counters)과 추천 축(usage_recommendations)은 이번에 붙었다.
 *      이 둘은 세션과 다른 표라, 한쪽만 걸리면 같은 화면의 두 카드가 서로 다른 모집단을
 *      그리면서도 그 사실을 말하지 않는다 — 그것이 이 테스트가 막는 실패다.
 *
 *   ② **무회귀** — 빈 필터는 조건을 하나도 만들지 않는다(= 필터 없는 함수와 같은 값).
 *      골든 44개가 사는 근거이므로 축마다 명시적으로 확인한다.
 */

// 세션 축 — 집계 타일·사용자별·모델별·일별이 모두 이 조건 한 벌을 공유한다.
func TestUserFilterIsolatesSessionAxis(t *testing.T) {
	ctx := fresh(t)
	mustSession(t, ctx, SessionInput{SessionID: "s1", Machine: "pc-1", Username: "kim",
		Model: mOpus, StartedAt: "2026-08-03T09:00:00.000Z",
		Input: 10, Output: 2000, CacheRead: 90000, CacheCreate: 500, Turns: 30})
	mustSession(t, ctx, SessionInput{SessionID: "s2", Machine: "pc-2", Username: "lee",
		Model: mSonnet, StartedAt: "2026-08-02T09:00:00.000Z",
		Input: 5, Output: 100, CacheRead: 7, CacheCreate: 1, Turns: 3})

	kim := Filter{Username: "kim"}

	tot, err := TotalsWithFilter(ctx, kim)
	if err != nil {
		t.Fatal(err)
	}
	if tot.Sessions != 1 || tot.Output != 2000 || tot.CacheRead != 90000 {
		t.Fatalf("kim 만 남아야 한다: %+v", tot)
	}
	// 사람 수·머신 수도 좁혀진다 — '1명 · 1대' 가 화면에 그렇게 나온다.
	if tot.Users != 1 || tot.Machines != 1 {
		t.Fatalf("users/machines 가 안 좁혀졌다: %+v", tot)
	}

	rows, err := UsageByUserWithFilter(ctx, kim)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Username != "kim" {
		t.Fatalf("%+v", rows)
	}

	days, err := UsageByDayWithFilter(ctx, 30, kim)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 || days[0].Day != "2026-08-03" {
		t.Fatalf("lee 의 날짜가 남았다: %+v", days)
	}

	models, err := UsageByModelWithFilter(ctx, kim)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 {
		t.Fatalf("모델 축이 안 좁혀졌다: %+v", models)
	}

	// 무회귀 — 빈 필터는 둘 다 본다.
	if tot2, _ := TotalsWithFilter(ctx, Filter{}); tot2.Sessions != 2 {
		t.Fatalf("빈 필터가 전체를 안 돌려준다: %+v", tot2)
	}
}

/*
 * 카운터 축 — 도구·명령·스킬·에이전트·MCP·키워드 패널의 근거.
 *
 * 이 표에는 username 컬럼이 직접 있어서 세션으로 되짚지 않는다. 그래서 **세션 행이 없는
 * 고아 카운터도 사람 기준으로는 정확히 센다** — 플랫폼 필터가 못 하는 일이다.
 */
func TestUserFilterIsolatesCounterAxis(t *testing.T) {
	ctx := fresh(t)
	seed := func(sid, user string, rows ...CounterRow) {
		mustCounters(t, ctx, CountersInput{SessionID: sid, Username: user, Machine: "m",
			StartedAt: "2026-08-03T00:00:00.000Z", Rows: rows})
	}
	seed("s1", "kim",
		CounterRow{Kind: "agent", Key: "backend-engineer", Count: 25},
		CounterRow{Kind: "agent", Key: "qa-engineer", Count: 16})
	seed("s2", "lee",
		CounterRow{Kind: "agent", Key: "general-purpose", Count: 100})

	kim := Filter{Username: "kim"}

	top, err := TopKeysWithFilter(ctx, "agent", 20, kim)
	if err != nil {
		t.Fatal(err)
	}
	// lee 의 general-purpose 100 이 1위로 남아 있으면 필터가 안 걸린 것이다.
	if len(top) != 2 {
		t.Fatalf("kim 의 두 축만 남아야 한다: %+v", top)
	}
	for _, r := range top {
		if r.Key == "general-purpose" {
			t.Fatalf("다른 사람의 축이 남았다: %+v", top)
		}
	}
	if top[0].Key != "backend-engineer" || top[0].Count != 25 {
		t.Fatalf("정렬/합계: %+v", top[0])
	}

	byU, err := ByUserWithFilter(ctx, "agent", 0, kim)
	if err != nil {
		t.Fatal(err)
	}
	if len(byU) != 1 || byU[0].Username != "kim" || byU[0].Total != 41 {
		t.Fatalf("사람별 활용이 한 사람으로 좁혀지지 않았다: %+v", byU)
	}

	// 무회귀 — 빈 필터는 전체 합과 두 사람을 본다.
	all, _ := TopKeysWithFilter(ctx, "agent", 20, Filter{})
	if len(all) != 3 {
		t.Fatalf("빈 필터가 전체를 안 돌려준다: %+v", all)
	}
	if allU, _ := ByUserWithFilter(ctx, "agent", 0, Filter{}); len(allU) != 2 {
		t.Fatalf("빈 필터 사람별: %+v", allU)
	}
}

/*
 * 추천 축 — 카탈로그 공백. 이 표에는 session_id 가 없어 플랫폼으로는 못 걸리지만
 * username 은 있어서 사람으로는 걸린다. 그 비대칭을 여기서 못 박는다.
 */
func TestUserFilterIsolatesRecommendationAxis(t *testing.T) {
	ctx := fresh(t)
	add := func(user string, score float64, tokens ...string) {
		if err := RecommendationAdd(ctx, RecoInput{GoalTokens: tokens, Score: score,
			Source: "mcp", Username: user}); err != nil {
			t.Fatal(err)
		}
	}
	add("kim", 0, "쿠버네티스", "비용")
	add("kim", 0, "쿠버네티스", "모니터링")
	add("lee", 0, "안드로이드", "빌드")
	add("lee", 0, "안드로이드", "서명")
	add("lee", 5, "잘맞은", "목표")

	kim := Filter{Username: "kim"}

	gaps, err := RecommendationGapsAtWithFilter(ctx, 1, 20, kim)
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 1 || gaps[0].Token != "쿠버네티스" || gaps[0].Count != 2 {
		t.Fatalf("kim 의 공백만 남아야 한다: %+v", gaps)
	}

	sum, err := RecommendationSummaryWithFilter(ctx, kim)
	if err != nil {
		t.Fatal(err)
	}
	if sum != (RecoSummary{Total: 2, Miss: 2}) {
		t.Fatalf("kim 기준 요약: %+v", sum)
	}

	// 무회귀 — 빈 필터는 종전과 같다(두 사람의 공백 2개, 전체 5건 중 실패 4건).
	allGaps, _ := RecommendationGapsAtWithFilter(ctx, 1, 20, Filter{})
	if len(allGaps) != 2 {
		t.Fatalf("빈 필터 공백: %+v", allGaps)
	}
	if allSum, _ := RecommendationSummaryWithFilter(ctx, Filter{}); allSum != (RecoSummary{Total: 5, Miss: 4}) {
		t.Fatalf("빈 필터 요약: %+v", allSum)
	}
	// 시그니처가 동결된 예전 함수도 같은 값이어야 한다(껍데기가 빈 필터로 부른다).
	if legacy, _ := RecommendationSummary(ctx); legacy != (RecoSummary{Total: 5, Miss: 4}) {
		t.Fatalf("동결 시그니처가 갈렸다: %+v", legacy)
	}
}

/*
 * 없는 사용자는 **빈 집계**다 — 오류가 아니다.
 *
 * 사용자명은 자유 문자열이라 400 을 낼 허용목록이 없다(플랫폼과 다른 점). 없는 이름에
 * 전체를 돌려주면 요청과 다른 모집단이 요청한 이름으로 조용히 돌아온다 — 그것이 최악이다.
 */
func TestUserFilterUnknownUserReturnsEmptyNotAll(t *testing.T) {
	ctx := fresh(t)
	mustSession(t, ctx, SessionInput{SessionID: "s1", Machine: "pc-1", Username: "kim",
		StartedAt: "2026-08-03T09:00:00.000Z", Output: 2000})
	mustCounters(t, ctx, CountersInput{SessionID: "s1", Username: "kim", Machine: "pc-1",
		StartedAt: "2026-08-03T00:00:00.000Z",
		Rows:      []CounterRow{{Kind: "agent", Key: "backend-engineer", Count: 3}}})

	ghost := Filter{Username: "없는사람"}

	if tot, err := TotalsWithFilter(ctx, ghost); err != nil || tot.Sessions != 0 {
		t.Fatalf("tot=%+v err=%v", tot, err)
	}
	if rows, err := TopKeysWithFilter(ctx, "agent", 20, ghost); err != nil || len(rows) != 0 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if rows, err := ByUserWithFilter(ctx, "agent", 0, ghost); err != nil || len(rows) != 0 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if sum, err := RecommendationSummaryWithFilter(ctx, ghost); err != nil || sum != (RecoSummary{}) {
		t.Fatalf("sum=%+v err=%v", sum, err)
	}
}

/*
 * 사용자 필터가 **series·quality 의 모집단을 건드리지 않는다.**
 *
 * platformSessionCond 주석이 경고하는 자리다: 사용자 축을 그 공용 조건에 얹으면
 * usage_series 의 username 으로 이미 거른 것과 이중으로 걸려 다른 화면이 조용히 달라진다.
 * 규칙을 나란히 둔 것이 실제로 지켜졌는지 확인한다.
 */
func TestUserFilterDoesNotTouchSeriesPopulation(t *testing.T) {
	ctx := fresh(t)
	/*
	 * **세션 행을 일부러 만들지 않는다** — 시간 버킷만 있는 고아 series 다.
	 *
	 * 이것이 이 테스트의 칼날이다. SeriesRows 는 usage_series.username 을 직접 보므로
	 * (rows.go) 세션 행이 없어도 사람 기준으로 정확히 돌려준다. 만약 누군가 사용자 축을
	 * 공용 조건(platformSessionCond)에 얹으면 EXISTS 서브쿼리가 세션 행을 요구하게 되어
	 * 이 행들이 **조용히 0건으로 사라진다**. 그때 이 테스트가 먼저 깨진다.
	 */
	mustSeries(t, ctx, SeriesInput{SessionID: "orphan-1", Username: "kim", Machine: "pc-1",
		Rows: []SeriesRow{{Hour: "2026-08-03T09", Model: mOpus, Output: 10, Turns: 1}}})
	mustSeries(t, ctx, SeriesInput{SessionID: "orphan-2", Username: "lee", Machine: "pc-2",
		Rows: []SeriesRow{{Hour: "2026-08-03T09", Model: mOpus, Output: 20, Turns: 1}}})

	all, err := SeriesRows(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("빈 필터가 고아 버킷 2개를 안 준다: %+v", all)
	}

	mine, err := SeriesRows(ctx, Filter{Username: "kim"})
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 1 {
		t.Fatalf("사용자 필터가 고아 버킷을 잃었다(공용 조건에 사용자 축이 얹혔나?): %+v", mine)
	}
	if mine[0].Output != 10 {
		t.Fatalf("다른 사람의 버킷이 왔다: %+v", mine[0])
	}
}
