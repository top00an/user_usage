package httpapi

import (
	"net/http"
	"testing"
)

/*
 * ── 사용자 스코프 (?user=) ────────────────────────────────────────────────
 *
 * '사용 추적' 화면이 한 사람으로 좁힐 때 summary·dispatch 에 같은 값을 싣는다.
 * 픽스처는 플랫폼 스코프 테스트와 공유한다(seedScope): alice 2세션 · bob 1세션.
 *
 * 이 파일이 못 박는 것:
 *   ① 미지정 = 전체 (골든 44개가 사는 근거)
 *   ② 배타 — 고른 사람의 값만. 자릿수가 갈려 있어 섞이면 즉시 보인다.
 *   ③ 한 화면의 두 조회(summary·dispatch)가 **같은 모집단**을 본다.
 *   ④ 없는 사람은 빈 집계이고 200 이다 — 400 도, 전체도 아니다.
 */

func TestSummaryUserScope(t *testing.T) {
	openDB(t)
	seedScope(t)
	h := New(testCfg(false))

	var all, a, b summaryProbe
	getJSON(t, h, "/api/usage/summary", &all)
	getJSON(t, h, "/api/usage/summary?user=alice", &a)
	getJSON(t, h, "/api/usage/summary?user=bob", &b)

	// ① 미지정 = 전체(3세션). 이 기본값이 바뀌면 골든이 통째로 깨진다.
	if all.Totals.Sessions != 3 {
		t.Fatalf("미지정이 전체가 아니다: %+v", all.Totals)
	}

	// ② 배타 — alice 는 c1+c2 = 2세션, output 2000+200.
	if a.Totals.Sessions != 2 || a.Totals.Output != 2200 || a.Totals.Input != 1100 {
		t.Fatalf("alice totals: %+v", a.Totals)
	}
	if b.Totals.Sessions != 1 || b.Totals.Output != 20 || b.Totals.Input != 10 {
		t.Fatalf("bob totals: %+v", b.Totals)
	}

	// byUser 는 고른 사람 한 줄로 좁혀진다 — 화면이 '1명'이라고 말하는 근거.
	if len(a.ByUser) != 1 || a.ByUser[0].Username != "alice" {
		t.Fatalf("byUser 가 안 좁혀졌다: %+v", a.ByUser)
	}
	if len(b.ByUser) != 1 || b.ByUser[0].Username != "bob" {
		t.Fatalf("byUser: %+v", b.ByUser)
	}

	/*
	 * ③ 카운터 축(top)도 같이 좁혀진다. 세션 표와 다른 표라 한쪽만 걸리면
	 *   같은 화면의 두 카드가 서로 다른 모집단을 그리면서 그 사실을 말하지 않는다.
	 */
	if len(a.Top.Agent) != 1 || a.Top.Agent[0].Key != "backend-engineer" || a.Top.Agent[0].Count != 30 {
		t.Fatalf("alice 축: %+v", a.Top.Agent)
	}
	if len(b.Top.Agent) != 1 || b.Top.Agent[0].Key != "general-purpose" || b.Top.Agent[0].Count != 4 {
		t.Fatalf("bob 축: %+v", b.Top.Agent)
	}
	// team-design 스킬은 두 사람이 **모두** 쓴다(20 대 1). 합계 21 이 남아 있으면 안 걸린 것이다.
	if len(a.Top.Skill) != 1 || a.Top.Skill[0].Count != 20 {
		t.Fatalf("alice 스킬 축이 전사 합계다: %+v", a.Top.Skill)
	}
	if len(b.Top.Skill) != 1 || b.Top.Skill[0].Count != 1 {
		t.Fatalf("bob 스킬 축: %+v", b.Top.Skill)
	}
}

// dispatch(사람별 활용)도 같은 축을 싣는다 — summary 와 한 화면이므로 갈리면 안 된다.
func TestDispatchUserScope(t *testing.T) {
	openDB(t)
	seedScope(t)
	h := New(testCfg(false))

	type userRow struct {
		Username string `json:"username"`
		Total    int64  `json:"total"`
	}
	var all, a struct {
		Agents []userRow `json:"agents"`
		Skills []userRow `json:"skills"`
	}
	getJSON(t, h, "/api/usage/dispatch", &all)
	getJSON(t, h, "/api/usage/dispatch?user=alice", &a)

	if len(all.Agents) != 2 || len(all.Skills) != 2 {
		t.Fatalf("미지정이 두 사람을 안 준다: agents=%+v skills=%+v", all.Agents, all.Skills)
	}
	if len(a.Agents) != 1 || a.Agents[0].Username != "alice" || a.Agents[0].Total != 30 {
		t.Fatalf("alice 로 안 좁혀졌다: %+v", a.Agents)
	}
	// 스킬 축도 함께 좁혀진다 — 한쪽만 걸리면 같은 카드 안에서 모집단이 갈린다.
	if len(a.Skills) != 1 || a.Skills[0].Username != "alice" || a.Skills[0].Total != 20 {
		t.Fatalf("스킬 축이 안 좁혀졌다: %+v", a.Skills)
	}
}

/*
 * ④ 없는 사람 — 빈 집계 200 이다.
 *
 * platform 과 달리 허용목록이 없어 400 을 낼 근거가 없다(사용자명은 자유 문자열).
 * 전체를 돌려주는 것이 최악이다: 요청과 다른 모집단이 요청한 이름으로 조용히 돌아온다.
 */
func TestSummaryUnknownUserIsEmptyNotAll(t *testing.T) {
	openDB(t)
	seedScope(t)
	h := New(testCfg(false))

	rec := do(t, h, http.MethodGet, "/api/usage/summary?user=%EC%97%86%EB%8A%94%EC%82%AC%EB%9E%8C", "", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("없는 사용자에 %d 를 냈다 — 200 + 빈 집계여야 한다: %s", rec.Code, rec.Body.String())
	}
	var ghost summaryProbe
	getJSON(t, h, "/api/usage/summary?user=%EC%97%86%EB%8A%94%EC%82%AC%EB%9E%8C", &ghost)
	if ghost.Totals.Sessions != 0 || ghost.Totals.Output != 0 {
		t.Fatalf("없는 사용자가 전체를 받았다: %+v", ghost.Totals)
	}
	if len(ghost.ByUser) != 0 || len(ghost.Top.Agent) != 0 {
		t.Fatalf("없는 사용자에 행이 있다: byUser=%+v agent=%+v", ghost.ByUser, ghost.Top.Agent)
	}
}
