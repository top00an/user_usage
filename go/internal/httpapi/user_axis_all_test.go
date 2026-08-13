package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/tscorp/user-usage/internal/store"
	"github.com/tscorp/user-usage/internal/tenant"
)

// getBody 는 admin 으로 조회해 200 을 확인하고 **본문 문자열**을 돌려준다.
// 응답 shape 가 엔드포인트마다 달라 구조체로 풀 수 없으므로, "갈리는가"는 본문 비교로 잰다.
func getBody(t *testing.T, h http.Handler, target string) string {
	t.Helper()
	rec := do(t, h, http.MethodGet, target, "", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", target, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

/*
 * ── 유저 축은 **모든** 조회에 닿는다 ──────────────────────────────────────
 *
 * 요구: "비용·토큰 모든 차트를 연동된 사용자별로 각자 볼 수 있어야 한다. 전체 통합은 기본이고
 * 세부 내역도 필요하다."
 *
 * 그래서 이 파일은 엔드포인트를 **열거해서** 전부 확인한다. 새 조회를 추가하면서 user 축을
 * 잊는 사고를 막는 것이 요점이다 — platform 축에서 실제로 그 사고가 났고(summary·dispatch·
 * seats·teams·dev 가 축을 조용히 무시했다), 같은 실수를 유저 축에서 반복하지 않기 위해
 * 여기서 목록으로 못 박는다.
 *
 * 2026-08-13 감사 시점에 user 를 무시하던 넷: coverage · seats · teams · dev. 이제 전부 받는다.
 */

// seedTwoUsersThreePlatforms 는 **한 PC 가 여러 플랫폼을 쓰는** 상황을 만든다.
//
// pc-b 하나가 codex 와 gemini 를 모두 보낸다 — platform 은 머신이 아니라 **세션**에 붙으므로
// 이것이 정상 경로다(store.go 의 platform 컬럼 주석). 한 사람이 PC 한 대로 세 도구를 쓰는 것이
// 실제 사용 형태이고, 그때 플랫폼별·사용자별이 동시에 갈려야 한다.
func seedTwoUsersThreePlatforms(t *testing.T) {
	t.Helper()
	ctx := tenant.With(t.Context(), "default")
	for _, s := range []store.SessionInput{
		{SessionID: "alice-claude-001", Machine: "pc-a", Username: "alice",
			Model: "claude-opus-5", Platform: "claude", StartedAt: "2026-08-12T09:00:00.000Z",
			Input: 1000, Output: 2000, CacheRead: 3000, CacheCreate: 400, Turns: 10,
			LinesAdded: 500, LinesRemoved: 120, EditsAccepted: 9, EditsRejected: 1},
		// 같은 PC(pc-b)로 두 플랫폼 — 이것이 이 픽스처의 요점이다.
		{SessionID: "bob-codex-0001", Machine: "pc-b", Username: "bob",
			Model: "gpt-5.6-terra", Platform: "codex", StartedAt: "2026-08-12T09:00:00.000Z",
			Input: 10, Output: 20, CacheRead: 30, CacheCreate: 4, Turns: 2,
			LinesAdded: 7, LinesRemoved: 2, EditsAccepted: 1},
		{SessionID: "bob-gemini-0001", Machine: "pc-b", Username: "bob",
			Model: "gemini-3.6-flash", Platform: "gemini", StartedAt: "2026-08-12T11:00:00.000Z",
			Input: 5, Output: 7, CacheRead: 9, CacheCreate: 1, Turns: 1},
	} {
		if err := store.SessionUpsert(ctx, s); err != nil {
			t.Fatalf("seed %s: %v", s.SessionID, err)
		}
	}

	/*
	 * 카운터도 심는다 — dispatch·축 패널의 근거는 usage_counters 이고, 세션만 심으면 그
	 * 엔드포인트는 **양쪽 다 빈 응답**이라 "user 축이 무시된다"로 오판한다(실제로 그 함정을
	 * 밟았다. dev 의 LOC 와 같은 종류의 오탐이다).
	 */
	for _, c := range []store.CountersInput{
		{SessionID: "alice-claude-001", Username: "alice", Machine: "pc-a",
			StartedAt: "2026-08-12T09:00:00.000Z", Rows: []store.CounterRow{
				{Kind: "agent", Key: "backend-engineer", Count: 30},
				{Kind: "skill", Key: "team-design", Count: 20},
			}},
		{SessionID: "bob-codex-0001", Username: "bob", Machine: "pc-b",
			StartedAt: "2026-08-12T09:00:00.000Z", Rows: []store.CounterRow{
				{Kind: "agent", Key: "general-purpose", Count: 4},
				{Kind: "skill", Key: "team-design", Count: 1},
			}},
	} {
		if _, err := store.CountersUpsertN(ctx, c); err != nil {
			t.Fatalf("seed counters %s: %v", c.SessionID, err)
		}
	}
}

/*
 * 한 PC 가 여러 플랫폼을 써도 각각으로 갈리고, 사용자로 좁히면 그 사람의 플랫폼만 남는다.
 *
 * 이것이 안 되면 "codex·gpt·gemini 를 한 PC 에서 같이 쓴다"는 실제 사용 형태에서 화면이
 * 한 도구만 보여 주거나 셋을 뭉쳐 버린다.
 */
func TestPlatformsSplitPerMachineAndUser(t *testing.T) {
	openDB(t)
	seedTwoUsersThreePlatforms(t)
	h := New(testCfg(false))

	type row struct {
		Platform string `json:"platform"`
		Sessions int    `json:"sessions"`
	}
	var all, forBob struct {
		Platforms []row `json:"platforms"`
	}
	getJSON(t, h, "/api/usage/platforms", &all)
	getJSON(t, h, "/api/usage/platforms?user=bob", &forBob)

	got := map[string]int{}
	for _, r := range all.Platforms {
		got[r.Platform] = r.Sessions
	}
	for _, p := range []string{"claude", "codex", "gemini"} {
		if got[p] != 1 {
			t.Errorf("전체: %s 세션 %d (기대 1) — 한 PC 의 여러 플랫폼이 뭉쳐졌나: %+v", p, got[p], all.Platforms)
		}
	}

	// bob 은 PC 한 대(pc-b)로 codex·gemini 둘을 썼다 — 둘 다 남고 claude 는 빠져야 한다.
	gotBob := map[string]int{}
	for _, r := range forBob.Platforms {
		gotBob[r.Platform] = r.Sessions
	}
	if len(forBob.Platforms) != 2 || gotBob["codex"] != 1 || gotBob["gemini"] != 1 {
		t.Fatalf("bob 의 플랫폼: %+v (기대 codex 1 · gemini 1)", forBob.Platforms)
	}
	if _, leaked := gotBob["claude"]; leaked {
		t.Errorf("bob 에 alice 의 claude 가 새어 들어갔다: %+v", forBob.Platforms)
	}
}

/*
 * 유저 축을 **받는지** 엔드포인트마다 확인한다.
 *
 * 판정은 "alice 응답 != bob 응답"이다. 값이 같으면 축이 무시된 것이다 — 두 사람의 데이터를
 * 자릿수까지 다르게 심어 두었으므로(위 픽스처) 갈리면 반드시 다르다.
 */
func TestEveryUsageEndpointAcceptsUserAxis(t *testing.T) {
	openDB(t)
	seedTwoUsersThreePlatforms(t)
	h := New(testCfg(false))

	for _, ep := range []struct{ name, path string }{
		{"summary", "/api/usage/summary"},
		{"dispatch", "/api/usage/dispatch"},
		{"platforms", "/api/usage/platforms"},
		{"sessions", "/api/usage/sessions?sort=cost&top=25"},
		{"distribution", "/api/usage/distribution"},
		{"quality", "/api/usage/quality"},
		{"leaderboard", "/api/usage/leaderboard"},
		{"series", "/api/usage/series?metric=cost&interval=day"},
		// 2026-08-13 감사에서 배선한 넷.
		{"coverage", "/api/usage/coverage"},
		{"seats", "/api/usage/seats?days=30"},
		{"teams", "/api/usage/teams?days=30"},
		{"dev", "/api/usage/dev?days=30"},
	} {
		sep := "?"
		if strings.Contains(ep.path, "?") {
			sep = "&"
		}
		a := getBody(t, h, ep.path+sep+"user=alice")
		b := getBody(t, h, ep.path+sep+"user=bob")
		if a == b {
			t.Errorf("%s: user 축이 무시된다 — alice 와 bob 응답이 같다\n  %s", ep.name, a)
		}
		// 미지정은 전체다(둘 중 어느 쪽과도 같지 않아야 한다 — 합계이므로).
		all := getBody(t, h, ep.path)
		if all == a || all == b {
			t.Errorf("%s: 미지정이 한 사람 응답과 같다 — 전체 집계가 아니다", ep.name)
		}
	}
}

/*
 * dev 축은 **LOC·편집** 이 있어야 갈리는 것이 보인다. 토큰만 심으면 전 응답이 0 이라
 * "안 갈린다"로 오판한다(실제로 이 함정을 밟았다). 그래서 합이 맞는지 숫자로 확인한다.
 */
func TestDevMetricsSplitByUserWithRealLOC(t *testing.T) {
	openDB(t)
	seedTwoUsersThreePlatforms(t)
	h := New(testCfg(false))

	type totals struct {
		LinesAdded    int64 `json:"linesAdded"`
		LinesRemoved  int64 `json:"linesRemoved"`
		EditsAccepted int64 `json:"editsAccepted"`
	}
	var all, a, b struct {
		Totals totals `json:"totals"`
	}
	getJSON(t, h, "/api/usage/dev?days=30", &all)
	getJSON(t, h, "/api/usage/dev?days=30&user=alice", &a)
	getJSON(t, h, "/api/usage/dev?days=30&user=bob", &b)

	if a.Totals.LinesAdded != 500 || b.Totals.LinesAdded != 7 {
		t.Fatalf("유저별 LOC: alice=%d bob=%d (기대 500 · 7)", a.Totals.LinesAdded, b.Totals.LinesAdded)
	}
	// 부분의 합이 전체여야 한다 — 한쪽이 빠지거나 겹치면 여기서 깨진다.
	if all.Totals.LinesAdded != a.Totals.LinesAdded+b.Totals.LinesAdded {
		t.Errorf("합이 안 맞는다: 전체 %d != %d + %d",
			all.Totals.LinesAdded, a.Totals.LinesAdded, b.Totals.LinesAdded)
	}
	if all.Totals.EditsAccepted != a.Totals.EditsAccepted+b.Totals.EditsAccepted {
		t.Errorf("편집 합이 안 맞는다: %d != %d + %d",
			all.Totals.EditsAccepted, a.Totals.EditsAccepted, b.Totals.EditsAccepted)
	}
}
