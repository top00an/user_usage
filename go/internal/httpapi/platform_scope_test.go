package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/tscorp/user-usage/internal/store"
	"github.com/tscorp/user-usage/internal/tenant"
)

/*
 * platform= 을 **받지 않던** 다섯 엔드포인트의 계약: summary · dispatch · seats · teams · dev.
 *
 * 전에는 파라미터를 붙여도 에러 없이 무시하고 전체를 돌려줬다. 그게 이 축에서 가장 나쁜
 * 실패다 — 요청과 다른 모집단이 **요청한 이름으로** 돌아오고, 화면은 그 사실을 알 길이 없다.
 * (프런트가 "전체 플랫폼 기준" 배지로 덮어 뒀지만 그건 증상 가림이지 해결이 아니었다.)
 *
 * 그래서 셋을 잰다:
 *	① 미지정 = 현행 그대로(골든 44개 무회귀)
 *	② claude + codex = 전체
 *	③ 서로 겹치지 않는다
 */

// scopeDays 는 seats·teams 의 기본 윈도우(30일) 안이면서 서로 다른 날이다.
// 그 두 엔드포인트는 from/to 를 쿼리로 받지 않고 **오늘 기준**으로 만들므로, 시드가 창 밖으로
// 나가면 필터가 아니라 날짜 때문에 0 이 나와 테스트가 거짓으로 통과한다.
func scopeDays(t *testing.T) (string, string) {
	t.Helper()
	today := time.Now().UTC().Format("2006-01-02")
	return shiftDayLocal(today, -3), shiftDayLocal(today, -2)
}

func seedScope(t *testing.T) {
	t.Helper()
	ctx := tenant.With(t.Context(), "default")
	dayC, dayX := scopeDays(t)

	// claude — alice. series 있는 세션 하나 + 없는 세션 하나(양쪽 모델 귀속 경로).
	sessions := []store.SessionInput{
		{SessionID: "c1", Username: "alice", Machine: "m1", Model: "claude-opus-4-8", Platform: "claude",
			Input: 1000, Output: 2000, CacheRead: 3000, CacheCreate: 4000, Turns: 10,
			StartedAt:  dayC + "T09:00:00.000Z",
			LinesAdded: 100, LinesRemoved: 50, EditsAccepted: 7, EditsRejected: 3},
		// platform 미지정 — 현행 수집기의 보고. claude 로 채워진다.
		{SessionID: "c2", Username: "alice", Machine: "m1", Model: "claude-sonnet-4-5",
			Input: 100, Output: 200, CacheRead: 300, CacheCreate: 400, Turns: 5,
			StartedAt:  dayC + "T10:00:00.000Z",
			LinesAdded: 10, LinesRemoved: 5, EditsAccepted: 1, EditsRejected: 1},
		// codex — bob.
		{SessionID: "x1", Username: "bob", Machine: "m2", Model: "gpt-5-codex", Platform: "codex",
			Input: 10, Output: 20, CacheRead: 30, CacheCreate: 40, Turns: 2,
			StartedAt:  dayX + "T09:00:00.000Z",
			LinesAdded: 1, LinesRemoved: 2, EditsAccepted: 3, EditsRejected: 4},
	}
	for _, s := range sessions {
		if err := store.SessionUpsert(ctx, s); err != nil {
			t.Fatalf("seed %s: %v", s.SessionID, err)
		}
	}
	if _, err := store.SeriesUpsertN(ctx, store.SeriesInput{
		SessionID: "c1", Username: "alice", Machine: "m1",
		Rows: []store.SeriesRow{{Hour: dayC + "T09", Model: "claude-opus-4-8",
			Input: 1000, Output: 2000, CacheRead: 3000, CacheCreate: 4000, Turns: 10}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SeriesUpsertN(ctx, store.SeriesInput{
		SessionID: "x1", Username: "bob", Machine: "m2",
		Rows: []store.SeriesRow{{Hour: dayX + "T09", Model: "gpt-5-codex",
			Input: 10, Output: 20, CacheRead: 30, CacheCreate: 40, Turns: 2}},
	}); err != nil {
		t.Fatal(err)
	}

	// dispatch 의 근거 — 서브에이전트·스킬 축.
	counters := []store.CountersInput{
		{SessionID: "c1", Username: "alice", Machine: "m1", StartedAt: dayC + "T09:00:00.000Z",
			Rows: []store.CounterRow{
				{Kind: "agent", Key: "backend-engineer", Count: 30},
				{Kind: "skill", Key: "team-design", Count: 20},
			}},
		{SessionID: "x1", Username: "bob", Machine: "m2", StartedAt: dayX + "T09:00:00.000Z",
			Rows: []store.CounterRow{
				{Kind: "agent", Key: "general-purpose", Count: 4},
				{Kind: "skill", Key: "team-design", Count: 1},
			}},
	}
	for _, c := range counters {
		if _, err := store.CountersUpsertN(ctx, c); err != nil {
			t.Fatalf("seed counters %s: %v", c.SessionID, err)
		}
	}

	// teams 의 근거 — 사람이 서로 다른 팀에 있어야 롤업이 갈리는 게 보인다.
	for user, team := range map[string]string{"alice": "플랫폼", "bob": "프로덕트"} {
		if err := store.AssignTeam(ctx, user, team); err != nil {
			t.Fatalf("seed team %s: %v", user, err)
		}
	}
}

// getJSON 은 admin 으로 조회해 200 을 확인하고 본문을 dst 로 푼다.
func getJSON(t *testing.T, h http.Handler, target string, dst any) {
	t.Helper()
	rec := do(t, h, http.MethodGet, target, "", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", target, rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("%s 응답 파싱: %v — %s", target, err, rec.Body.String())
	}
}

/* ── summary ─────────────────────────────────────────────────────────── */

type summaryProbe struct {
	Totals struct {
		Sessions    int   `json:"sessions"`
		Input       int64 `json:"input"`
		Output      int64 `json:"output"`
		CacheRead   int64 `json:"cacheRead"`
		CacheCreate int64 `json:"cacheCreate"`
	} `json:"totals"`
	ByDay []struct {
		Day    string `json:"day"`
		Output int64  `json:"output"`
	} `json:"byDay"`
	ByUser []struct {
		Username string `json:"username"`
		Output   int64  `json:"output"`
	} `json:"byUser"`
	ByModel []struct {
		Model  string `json:"model"`
		Output int64  `json:"output"`
	} `json:"byModel"`
	ModelAxis struct {
		Sessions   int `json:"sessions"`
		WithSeries int `json:"withSeries"`
	} `json:"modelAxis"`
	Top struct {
		Agent []struct {
			Key   string `json:"key"`
			Count int64  `json:"count"`
		} `json:"agent"`
		Skill []struct {
			Key   string `json:"key"`
			Count int64  `json:"count"`
		} `json:"skill"`
	} `json:"top"`
}

func TestSummaryPlatformScope(t *testing.T) {
	openDB(t)
	seedScope(t)
	h := New(testCfg(false))

	var all, c, x summaryProbe
	getJSON(t, h, "/api/usage/summary", &all)
	getJSON(t, h, "/api/usage/summary?platform=claude", &c)
	getJSON(t, h, "/api/usage/summary?platform=codex", &x)

	// ① 미지정 = 전체
	if all.Totals.Sessions != 3 || all.Totals.Output != 2220 {
		t.Fatalf("미지정 totals 가 전체가 아니다: %+v", all.Totals)
	}
	// ③ 배타 — 자릿수가 갈려 있어 섞이면 즉시 보인다.
	if c.Totals.Sessions != 2 || c.Totals.Input != 1100 || c.Totals.Output != 2200 ||
		c.Totals.CacheRead != 3300 || c.Totals.CacheCreate != 4400 {
		t.Fatalf("claude totals: %+v", c.Totals)
	}
	if x.Totals.Sessions != 1 || x.Totals.Input != 10 || x.Totals.Output != 20 ||
		x.Totals.CacheRead != 30 || x.Totals.CacheCreate != 40 {
		t.Fatalf("codex totals: %+v", x.Totals)
	}
	// ② 분할
	if c.Totals.Sessions+x.Totals.Sessions != all.Totals.Sessions ||
		c.Totals.Output+x.Totals.Output != all.Totals.Output ||
		c.Totals.Input+x.Totals.Input != all.Totals.Input {
		t.Fatalf("summary totals 합이 전체와 다르다: %+v + %+v != %+v", c.Totals, x.Totals, all.Totals)
	}

	// 하위 카드도 전부 같은 모집단이어야 한다 — 하나라도 안 걸리면 같은 화면 안에서
	// 두 카드가 서로 다른 플랫폼을 그린다.
	if len(c.ByUser) != 1 || c.ByUser[0].Username != "alice" {
		t.Fatalf("claude byUser: %+v", c.ByUser)
	}
	if len(x.ByUser) != 1 || x.ByUser[0].Username != "bob" {
		t.Fatalf("codex byUser: %+v", x.ByUser)
	}
	if len(c.ByModel) != 2 {
		t.Fatalf("claude byModel: %+v", c.ByModel)
	}
	if len(x.ByModel) != 1 || x.ByModel[0].Model != "gpt-5-codex" || x.ByModel[0].Output != 20 {
		t.Fatalf("codex byModel: %+v", x.ByModel)
	}
	// 모델별 합 == 그 플랫폼의 totals. 깨지면 모델별만 작아져 사람에게 "유실"로 보인다.
	for _, probe := range []summaryProbe{c, x} {
		var sum int64
		for _, m := range probe.ByModel {
			sum += m.Output
		}
		if sum != probe.Totals.Output {
			t.Fatalf("모델별 합 %d != totals %d (%+v)", sum, probe.Totals.Output, probe.ByModel)
		}
	}
	if c.ModelAxis.Sessions != 2 || c.ModelAxis.WithSeries != 1 {
		t.Fatalf("claude modelAxis: %+v", c.ModelAxis)
	}
	if x.ModelAxis.Sessions != 1 || x.ModelAxis.WithSeries != 1 {
		t.Fatalf("codex modelAxis: %+v", x.ModelAxis)
	}
	if len(c.ByDay) != 1 || len(x.ByDay) != 1 || c.ByDay[0].Day == x.ByDay[0].Day {
		t.Fatalf("byDay 분할: claude %+v · codex %+v", c.ByDay, x.ByDay)
	}

	// 상위 키(usage_counters)도 세션으로 되짚어 갈린다.
	if len(c.Top.Agent) != 1 || c.Top.Agent[0].Key != "backend-engineer" || c.Top.Agent[0].Count != 30 {
		t.Fatalf("claude top.agent: %+v", c.Top.Agent)
	}
	if len(x.Top.Agent) != 1 || x.Top.Agent[0].Key != "general-purpose" || x.Top.Agent[0].Count != 4 {
		t.Fatalf("codex top.agent: %+v", x.Top.Agent)
	}
	// 같은 키가 양쪽에 있어도 갈린다(team-design: claude 20 · codex 1 · 전체 21).
	if len(c.Top.Skill) != 1 || c.Top.Skill[0].Count != 20 {
		t.Fatalf("claude top.skill: %+v", c.Top.Skill)
	}
	if len(x.Top.Skill) != 1 || x.Top.Skill[0].Count != 1 {
		t.Fatalf("codex top.skill: %+v", x.Top.Skill)
	}
	if len(all.Top.Skill) != 1 || all.Top.Skill[0].Count != 21 {
		t.Fatalf("미지정 top.skill 합: %+v", all.Top.Skill)
	}
}

/* ── dispatch ────────────────────────────────────────────────────────── */

func TestDispatchPlatformScope(t *testing.T) {
	openDB(t)
	seedScope(t)
	h := New(testCfg(false))

	type probe struct {
		Agents []struct {
			Username string `json:"username"`
			Total    int64  `json:"total"`
			Generic  int64  `json:"generic"`
			Role     int64  `json:"role"`
		} `json:"agents"`
		Skills []struct {
			Username string `json:"username"`
			Total    int64  `json:"total"`
		} `json:"skills"`
	}
	var all, c, x probe
	getJSON(t, h, "/api/usage/dispatch", &all)
	getJSON(t, h, "/api/usage/dispatch?platform=claude", &c)
	getJSON(t, h, "/api/usage/dispatch?platform=codex", &x)

	if len(all.Agents) != 2 {
		t.Fatalf("미지정 dispatch 가 전체가 아니다: %+v", all.Agents)
	}
	// alice 는 역할 에이전트만, bob 은 general-purpose 만 — 이 화면이 보려던 바로 그 차이가
	// 플랫폼별로도 갈려야 한다.
	if len(c.Agents) != 1 || c.Agents[0].Username != "alice" || c.Agents[0].Role != 30 || c.Agents[0].Generic != 0 {
		t.Fatalf("claude dispatch: %+v", c.Agents)
	}
	if len(x.Agents) != 1 || x.Agents[0].Username != "bob" || x.Agents[0].Generic != 4 || x.Agents[0].Role != 0 {
		t.Fatalf("codex dispatch: %+v", x.Agents)
	}
	var sum int64
	for _, a := range append(append([]struct {
		Username string `json:"username"`
		Total    int64  `json:"total"`
		Generic  int64  `json:"generic"`
		Role     int64  `json:"role"`
	}{}, c.Agents...), x.Agents...) {
		sum += a.Total
	}
	var allSum int64
	for _, a := range all.Agents {
		allSum += a.Total
	}
	if sum != allSum {
		t.Fatalf("dispatch 합 %d != 전체 %d", sum, allSum)
	}
	if len(c.Skills) != 1 || c.Skills[0].Total != 20 || len(x.Skills) != 1 || x.Skills[0].Total != 1 {
		t.Fatalf("dispatch skills 분할: %+v · %+v", c.Skills, x.Skills)
	}
}

/* ── seats ───────────────────────────────────────────────────────────── */

func TestSeatsPlatformScope(t *testing.T) {
	openDB(t)
	seedScope(t)
	h := New(testCfg(false))

	var all, c, x seatsResponse
	getJSON(t, h, "/api/usage/seats", &all)
	getJSON(t, h, "/api/usage/seats?platform=claude", &c)
	getJSON(t, h, "/api/usage/seats?platform=codex", &x)

	if all.Summary.ActiveSeats != 2 {
		t.Fatalf("미지정 활성 좌석 = %d (기대 2)", all.Summary.ActiveSeats)
	}
	if c.Summary.ActiveSeats != 1 || len(c.Seats) != 1 || c.Seats[0].Username != "alice" {
		t.Fatalf("claude seats: %+v", c.Seats)
	}
	if x.Summary.ActiveSeats != 1 || len(x.Seats) != 1 || x.Seats[0].Username != "bob" {
		t.Fatalf("codex seats: %+v", x.Seats)
	}
	if c.Seats[0].Sessions != 2 || x.Seats[0].Sessions != 1 {
		t.Fatalf("좌석 세션 수: claude %d · codex %d", c.Seats[0].Sessions, x.Seats[0].Sessions)
	}
	if c.Seats[0].Tokens != 11000 || x.Seats[0].Tokens != 100 {
		t.Fatalf("좌석 토큰: claude %d · codex %d", c.Seats[0].Tokens, x.Seats[0].Tokens)
	}
	// 비용도 합이 맞아야 한다 — 단가 계산은 세션별이라 필터가 걸리면 선형으로 갈린다.
	if got, want := c.Summary.TotalUsd+x.Summary.TotalUsd, all.Summary.TotalUsd; !almostEqual(got, want) {
		t.Fatalf("seats 비용 합 %v != 전체 %v", got, want)
	}
	// 창(from/to)은 필터와 무관하게 같아야 한다 — 플랫폼이 기간을 바꾸면 비교가 무의미해진다.
	if c.From != all.From || c.To != all.To || c.PrevFrom != all.PrevFrom {
		t.Fatalf("필터가 기간을 바꿨다: %+v vs %+v", c, all)
	}
}

/* ── teams ───────────────────────────────────────────────────────────── */

func TestTeamsPlatformScope(t *testing.T) {
	openDB(t)
	seedScope(t)
	h := New(testCfg(false))

	var all, c, x teamsResponse
	getJSON(t, h, "/api/usage/teams", &all)
	getJSON(t, h, "/api/usage/teams?platform=claude", &c)
	getJSON(t, h, "/api/usage/teams?platform=codex", &x)

	if len(all.Teams) != 2 {
		t.Fatalf("미지정 팀 수 = %d (%+v)", len(all.Teams), all.Teams)
	}
	if len(c.Teams) != 1 || c.Teams[0].Team != "플랫폼" || c.Teams[0].Sessions != 2 {
		t.Fatalf("claude teams: %+v", c.Teams)
	}
	if len(x.Teams) != 1 || x.Teams[0].Team != "프로덕트" || x.Teams[0].Sessions != 1 {
		t.Fatalf("codex teams: %+v", x.Teams)
	}
	if c.Teams[0].Tokens != 11000 || x.Teams[0].Tokens != 100 {
		t.Fatalf("팀 토큰: claude %d · codex %d", c.Teams[0].Tokens, x.Teams[0].Tokens)
	}
	var sum, allSum int64
	for _, tm := range append(append([]teamRowDTO{}, c.Teams...), x.Teams...) {
		sum += tm.Tokens
	}
	for _, tm := range all.Teams {
		allSum += tm.Tokens
	}
	if sum != allSum {
		t.Fatalf("팀 토큰 합 %d != 전체 %d", sum, allSum)
	}
}

/* ── dev ─────────────────────────────────────────────────────────────── */

type devProbe struct {
	Totals struct {
		LinesAdded    int64 `json:"linesAdded"`
		LinesRemoved  int64 `json:"linesRemoved"`
		EditsAccepted int64 `json:"editsAccepted"`
		EditsRejected int64 `json:"editsRejected"`
	} `json:"totals"`
	ByDay []struct {
		Day        string `json:"day"`
		LinesAdded int64  `json:"linesAdded"`
	} `json:"byDay"`
}

func TestDevPlatformScope(t *testing.T) {
	openDB(t)
	seedScope(t)
	h := New(testCfg(false))

	var all, c, x devProbe
	getJSON(t, h, "/api/usage/dev", &all)
	getJSON(t, h, "/api/usage/dev?platform=claude", &c)
	getJSON(t, h, "/api/usage/dev?platform=codex", &x)

	if all.Totals.LinesAdded != 111 || all.Totals.EditsRejected != 8 {
		t.Fatalf("미지정 dev: %+v", all.Totals)
	}
	if c.Totals.LinesAdded != 110 || c.Totals.LinesRemoved != 55 ||
		c.Totals.EditsAccepted != 8 || c.Totals.EditsRejected != 4 {
		t.Fatalf("claude dev: %+v", c.Totals)
	}
	if x.Totals.LinesAdded != 1 || x.Totals.LinesRemoved != 2 ||
		x.Totals.EditsAccepted != 3 || x.Totals.EditsRejected != 4 {
		t.Fatalf("codex dev: %+v", x.Totals)
	}
	if c.Totals.LinesAdded+x.Totals.LinesAdded != all.Totals.LinesAdded ||
		c.Totals.EditsRejected+x.Totals.EditsRejected != all.Totals.EditsRejected {
		t.Fatalf("dev 합이 전체와 다르다: %+v + %+v != %+v", c.Totals, x.Totals, all.Totals)
	}
	if len(c.ByDay) != 1 || len(x.ByDay) != 1 || c.ByDay[0].Day == x.ByDay[0].Day {
		t.Fatalf("dev byDay 분할: %+v · %+v", c.ByDay, x.ByDay)
	}
}

/* ── 잘못된 값 ───────────────────────────────────────────────────────── */

/*
 * 오타는 400 이다. 다섯 엔드포인트 전부 — 여기서 조용히 무시하면 이번 수정 이전과 똑같이
 * "요청과 다른 모집단이 요청한 이름으로" 돌아온다. 대문자도 오타다(필터는 정규화하지 않는다).
 */
func TestPlatformFilterRejectsUnknownValueOnRollupEndpoints(t *testing.T) {
	openDB(t)
	h := New(testCfg(false))
	for _, target := range []string{
		"/api/usage/summary?platform=claud",
		"/api/usage/dispatch?platform=claud",
		"/api/usage/seats?platform=claud",
		"/api/usage/teams?platform=claud",
		"/api/usage/dev?platform=claud",
		"/api/usage/summary?platform=CLAUDE",
		"/api/usage/dev?platform=",
	} {
		rec := do(t, h, http.MethodGet, target, "", withAdmin)
		want := http.StatusBadRequest
		if target == "/api/usage/dev?platform=" {
			want = http.StatusOK // 빈 값은 미지정이다 — 조건을 걸지 않는다
		}
		if rec.Code != want {
			t.Fatalf("%s: %d (기대 %d) — %s", target, rec.Code, want, rec.Body.String())
		}
	}
}

/*
 * 모르는 경로는 잘못된 platform 이 붙어도 **404 다.**
 *
 * 400 을 내면 "그 엔드포인트는 있는데 파라미터가 틀렸다"는 뜻이 되어, 없는 경로의 존재를
 * 알려주는 셈이 된다. 검증 순서가 경로 판정보다 앞서면 조용히 그렇게 된다.
 */
func TestUnknownPathStays404EvenWithBadPlatform(t *testing.T) {
	openDB(t)
	h := New(testCfg(false))
	rec := do(t, h, http.MethodGet, "/api/usage/nope?platform=claud", "", withAdmin)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d (기대 404) — %s", rec.Code, rec.Body.String())
	}
}

func almostEqual(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}
