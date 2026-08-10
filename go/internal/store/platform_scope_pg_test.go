package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/tscorp/user-usage/internal/db"
	"github.com/tscorp/user-usage/internal/tenant"
)

/*
 * 플랫폼 필터가 붙은 집계들을 **실제 pg 에서** 돌린다.
 *
 * sqlite 초록이 여기서는 근거가 못 되는 이유가 셋이다:
 *   · 상관 서브쿼리(EXISTS … usage_counters.session_id)의 별칭 해석이 방언마다 갈린다
 *   · 자리표시자(?→$n) 치환이 조건 **순서**대로 붙는지 — 어긋나도 문법 오류가 안 나고 값만 틀린다
 *   · WHERE 가 JOIN·GROUP BY 사이에 끼어드는 자리(UsageByModel ③·UsageModelAxis)
 *
 *   USAGE_TEST_PG_URL='postgres://…' go test ./internal/store -run TestPG -count=1
 *
 * 테넌트를 매 실행 새로 만드는 이유: 카운터 축 집계(TopKeys·ByUser)는 기간으로 좁힐 수 없어
 * 남은 행이 있으면 값이 흔들린다. RLS 가 테넌트로 갈라 주므로 그것을 격리 수단으로 쓴다.
 */
func TestPGPlatformScopedAggregates(t *testing.T) {
	url := os.Getenv("USAGE_TEST_PG_URL")
	if url == "" {
		t.Skip("USAGE_TEST_PG_URL 미설정 — pg 집계 필터 테스트 건너뜀")
	}

	db.SetTenantResolver(tenant.From)
	root := context.Background()
	d, err := db.Open(root, db.Options{Mode: "remote", URL: url})
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close(); handle = nil })
	if _, err := db.Migrate(root, d, "../../../migrations/pg"); err != nil {
		t.Logf("migrate(무시하고 진행 — 사전 적용 가정): %v", err)
	}
	if err := Init(root, d); err != nil {
		t.Fatalf("store.Init: %v", err)
	}

	tag := fmt.Sprintf("scope_%d", time.Now().UnixNano())
	ctx := tenant.With(root, tag)       // 이 실행만의 테넌트 — RLS 가 남의 행을 가린다
	other := tenant.With(root, tag+"x") // 격리 확인용

	// claude 2건(하나는 series 있음) · codex 1건(series 있음). 자릿수를 갈라 섞이면 보이게 한다.
	sessions := []SessionInput{
		{SessionID: "c1", Username: "alice", Machine: "m1", Model: "claude-opus-4-8", Platform: "claude",
			Input: 1000, Output: 2000, CacheRead: 3000, CacheCreate: 4000, Turns: 10,
			StartedAt:  "2026-08-03T09:00:00.000Z",
			LinesAdded: 100, LinesRemoved: 50, EditsAccepted: 7, EditsRejected: 3},
		{SessionID: "c2", Username: "alice", Machine: "m1", Model: "claude-sonnet-4-5", Platform: "claude",
			Input: 100, Output: 200, CacheRead: 300, CacheCreate: 400, Turns: 5,
			StartedAt:  "2026-08-04T09:00:00.000Z",
			LinesAdded: 10, LinesRemoved: 5, EditsAccepted: 1, EditsRejected: 1},
		{SessionID: "x1", Username: "bob", Machine: "m2", Model: "gpt-5-codex", Platform: "codex",
			Input: 10, Output: 20, CacheRead: 30, CacheCreate: 40, Turns: 2,
			StartedAt:  "2026-08-05T09:00:00.000Z",
			LinesAdded: 1, LinesRemoved: 2, EditsAccepted: 3, EditsRejected: 4},
	}
	for _, s := range sessions {
		if err := SessionUpsert(ctx, s); err != nil {
			t.Fatalf("SessionUpsert(%s): %v", s.SessionID, err)
		}
	}
	for _, in := range []SeriesInput{
		{SessionID: "c1", Username: "alice", Machine: "m1", Rows: []SeriesRow{{
			Hour: "2026-08-03T09", Model: "claude-opus-4-8",
			Input: 1000, Output: 2000, CacheRead: 3000, CacheCreate: 4000, Turns: 10}}},
		{SessionID: "x1", Username: "bob", Machine: "m2", Rows: []SeriesRow{{
			Hour: "2026-08-05T09", Model: "gpt-5-codex",
			Input: 10, Output: 20, CacheRead: 30, CacheCreate: 40, Turns: 2}}},
	} {
		if _, err := SeriesUpsertN(ctx, in); err != nil {
			t.Fatalf("SeriesUpsert(%s): %v", in.SessionID, err)
		}
	}
	for _, in := range []CountersInput{
		{SessionID: "c1", Username: "alice", Machine: "m1", StartedAt: "2026-08-03T09:00:00.000Z",
			Rows: []CounterRow{
				{Kind: "agent", Key: "backend-engineer", Count: 30},
				{Kind: "skill", Key: "team-design", Count: 20},
			}},
		{SessionID: "x1", Username: "bob", Machine: "m2", StartedAt: "2026-08-05T09:00:00.000Z",
			Rows: []CounterRow{
				{Kind: "agent", Key: "general-purpose", Count: 4},
				{Kind: "skill", Key: "team-design", Count: 1},
			}},
	} {
		if _, err := CountersUpsertN(ctx, in); err != nil {
			t.Fatalf("CountersUpsert(%s): %v", in.SessionID, err)
		}
	}

	// ① Totals — 분할 + 배타.
	all, err := Totals(ctx)
	if err != nil {
		t.Fatalf("Totals: %v", err)
	}
	c, err := TotalsWithFilter(ctx, Filter{Platform: "claude"})
	if err != nil {
		t.Fatalf("TotalsWithFilter(claude): %v", err)
	}
	x, err := TotalsWithFilter(ctx, Filter{Platform: "codex"})
	if err != nil {
		t.Fatalf("TotalsWithFilter(codex): %v", err)
	}
	if c.Sessions != 2 || c.Output != 2200 || c.CacheCreate != 4400 {
		t.Fatalf("pg claude totals: %+v", c)
	}
	if x.Sessions != 1 || x.Output != 20 || x.CacheCreate != 40 {
		t.Fatalf("pg codex totals: %+v", x)
	}
	if c.Sessions+x.Sessions != all.Sessions || c.Output+x.Output != all.Output {
		t.Fatalf("pg totals 합 != 전체: %+v + %+v != %+v", c, x, all)
	}

	// ② 조건 **순서**가 자리표시자와 어긋나지 않는가 — 여러 축을 한 번에 건다.
	//    어긋나면 문법 오류가 아니라 조용히 0 건이 되거나 엉뚱한 행이 나온다.
	multi, err := TotalsWithFilter(ctx, Filter{
		From: "2026-08-03", To: "2026-08-04", Username: "alice",
		Model: "claude-opus-4-8", Platform: "claude",
	})
	if err != nil {
		t.Fatalf("TotalsWithFilter(다축): %v", err)
	}
	if multi.Sessions != 1 || multi.Output != 2000 {
		t.Fatalf("pg 다축 필터: %+v (기대 c1 한 건)", multi)
	}

	// ③ UsageByModel — 세 경로에 같은 필터가 걸려 불변식이 산다.
	for _, p := range []string{"claude", "codex"} {
		rows, err := UsageByModelWithFilter(ctx, Filter{Platform: p})
		if err != nil {
			t.Fatalf("UsageByModelWithFilter(%s): %v", p, err)
		}
		tot, err := TotalsWithFilter(ctx, Filter{Platform: p})
		if err != nil {
			t.Fatalf("TotalsWithFilter(%s): %v", p, err)
		}
		got := sumAxes(rows)
		if got.Output != tot.Output || got.CacheCreate != tot.CacheCreate {
			t.Fatalf("pg %s: 모델별 합 %+v != Totals %+v", p, got, tot)
		}
	}
	xm, err := UsageByModelWithFilter(ctx, Filter{Platform: "codex"})
	if err != nil {
		t.Fatalf("UsageByModelWithFilter(codex): %v", err)
	}
	// ①(series 정확값) 경로가 실제로 살아 있는지 — 전부 fromSession 이면 필터가 버킷을 통째로 떨궜다.
	if len(xm) != 1 || xm[0].Model != "gpt-5-codex" || xm[0].FromSeries.Output != 20 {
		t.Fatalf("pg codex 모델별: %+v", xm)
	}

	// ④ UsageModelAxis — WHERE 가 LEFT JOIN 과 GROUP BY 사이에 제대로 끼는가.
	ax, err := UsageModelAxisWithFilter(ctx, Filter{Platform: "claude"})
	if err != nil {
		t.Fatalf("UsageModelAxisWithFilter: %v", err)
	}
	if ax.Sessions != 2 || ax.WithSeries != 1 || len(ax.Users) != 1 {
		t.Fatalf("pg claude modelAxis: %+v", ax)
	}

	// ⑤ 카운터 축 — 세션으로 되짚는 상관 서브쿼리가 pg 에서 도는가.
	ck, err := TopKeysWithFilter(ctx, "skill", 20, Filter{Platform: "claude"})
	if err != nil {
		t.Fatalf("TopKeysWithFilter: %v", err)
	}
	xk, err := TopKeysWithFilter(ctx, "skill", 20, Filter{Platform: "codex"})
	if err != nil {
		t.Fatalf("TopKeysWithFilter(codex): %v", err)
	}
	ak, err := TopKeys(ctx, "skill", 20)
	if err != nil {
		t.Fatalf("TopKeys: %v", err)
	}
	// 같은 키(team-design)가 양쪽에 있다 — 되짚기가 안 되면 셋 다 21 로 같아진다.
	if len(ck) != 1 || ck[0].Count != 20 || len(xk) != 1 || xk[0].Count != 1 ||
		len(ak) != 1 || ak[0].Count != 21 {
		t.Fatalf("pg skill 분할: claude %+v · codex %+v · 전체 %+v", ck, xk, ak)
	}
	cu, err := ByUserWithFilter(ctx, "agent", 8, Filter{Platform: "codex"})
	if err != nil {
		t.Fatalf("ByUserWithFilter: %v", err)
	}
	if len(cu) != 1 || cu[0].Username != "bob" || cu[0].Total != 4 {
		t.Fatalf("pg codex ByUser: %+v", cu)
	}

	// ⑥ 일별·사람별·개발 지표.
	cd, err := UsageByDayWithFilter(ctx, 365, Filter{Platform: "codex"})
	if err != nil {
		t.Fatalf("UsageByDayWithFilter: %v", err)
	}
	if len(cd) != 1 || cd[0].Day != "2026-08-05" || cd[0].Output != 20 {
		t.Fatalf("pg codex 일별: %+v", cd)
	}
	cuser, err := UsageByUserWithFilter(ctx, Filter{Platform: "claude"})
	if err != nil {
		t.Fatalf("UsageByUserWithFilter: %v", err)
	}
	if len(cuser) != 1 || cuser[0].Username != "alice" || cuser[0].Output != 2200 {
		t.Fatalf("pg claude 사용자별: %+v", cuser)
	}
	dt, dd, err := DevMetricsWithFilter(ctx, 365, Filter{Platform: "codex"})
	if err != nil {
		t.Fatalf("DevMetricsWithFilter: %v", err)
	}
	if dt.LinesAdded != 1 || dt.LinesRemoved != 2 || dt.EditsAccepted != 3 || dt.EditsRejected != 4 {
		t.Fatalf("pg codex 개발 지표: %+v", dt)
	}
	if len(dd) != 1 || dd[0].Day != "2026-08-05" {
		t.Fatalf("pg codex 개발 지표 일별: %+v", dd)
	}

	/*
	 * ⑦ platform 은 여전히 **격리 축이 아니다.** 새로 만든 필터들이 EXISTS 로 세션 표를
	 *   되짚으므로, 그 서브쿼리가 RLS 를 우회하면 여기서 남의 테넌트 행이 새어 나온다.
	 *   이 확인이 없으면 조회 필터를 늘린 것이 격리 구멍이 되는 자리다.
	 */
	if got, err := TotalsWithFilter(other, Filter{Platform: "claude"}); err != nil {
		t.Fatalf("TotalsWithFilter(다른 테넌트): %v", err)
	} else if got.Sessions != 0 {
		t.Fatalf("크로스테넌트 누수 — 다른 테넌트가 세션 %d건을 봤다: %+v", got.Sessions, got)
	}
	if got, err := TopKeysWithFilter(other, "skill", 20, Filter{Platform: "claude"}); err != nil {
		t.Fatalf("TopKeysWithFilter(다른 테넌트): %v", err)
	} else if len(got) != 0 {
		t.Fatalf("카운터 되짚기가 크로스테넌트로 샌다: %+v", got)
	}
}
