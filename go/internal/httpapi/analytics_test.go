package httpapi

import (
	"testing"
)

/*
 * 세션 응답이 **버리던** 필드의 계약.
 *
 * 수집기는 platform 을 보내고 저장 계층도 갖고 있는데 `/api/usage/sessions` 와
 * `/api/usage/sessions/{id}` 응답에는 없었다. 대시보드는 `/api/usage/platforms` 로 집계는
 * 보여주면서 **드릴다운에서는 어느 도구의 세션인지 말하지 못했다** — 같은 화면이 한쪽에서는
 * 아는 사실을 다른 쪽에서는 모른다고 하는 상태다.
 *
 * 여기서 재는 것은 셋이다:
 *	① 목록·상세 **양쪽**에 실린다(한쪽만 실으면 드릴다운에서 값이 사라진다)
 *	② 저장 계층의 정규화를 **우회하지 않는다** — 미지정 보고는 claude 로 보인다
 *	③ 기존 필드를 하나도 잃지 않는다(추가만 한다 — 화면이 그것에 의존한다)
 */

// sessionProbe 는 응답의 **일부만** 푼다. 기존 필드 몇을 함께 두는 이유는 ③ 때문이다 —
// 추가하다 기존 키 이름이 바뀌면 여기서 0/빈값으로 잡힌다.
type sessionProbe struct {
	SessionID string  `json:"sessionId"`
	Username  string  `json:"username"`
	Model     string  `json:"model"`
	Platform  string  `json:"platform"`
	Input     int64   `json:"input"`
	Output    int64   `json:"output"`
	Turns     int64   `json:"turns"`
	StartedAt *string `json:"startedAt"`
}

type sessionsProbe struct {
	Sessions []sessionProbe `json:"sessions"`
	Sort     string         `json:"sort"`
}

type sessionDetailProbe struct {
	Session sessionProbe `json:"session"`
}

// bySessionID 는 목록을 id 로 색인한다 — 정렬축에 의존하지 않게.
func bySessionID(rows []sessionProbe) map[string]sessionProbe {
	m := make(map[string]sessionProbe, len(rows))
	for _, r := range rows {
		m[r.SessionID] = r
	}
	return m
}

/*
 * ① + ② 목록.
 *
 * seedScope 의 c2 는 **platform 을 안 보낸 세션**이다(현행 수집기의 보고). 그것이 응답에서
 * claude 로 보여야 한다 — 빈 문자열이 새어 나오면 화면이 "플랫폼 미상" 이라는 없는 범주를
 * 그리게 되고, 그 순간 세션 드릴다운과 /api/usage/platforms 의 모집단이 갈린다.
 */
func TestSessionListCarriesPlatform(t *testing.T) {
	openDB(t)
	seedScope(t)
	h := New(testCfg(false))

	var got sessionsProbe
	getJSON(t, h, "/api/usage/sessions", &got)
	if len(got.Sessions) != 3 {
		t.Fatalf("세션 수 = %d (%+v)", len(got.Sessions), got.Sessions)
	}

	idx := bySessionID(got.Sessions)
	for id, want := range map[string]string{"c1": "claude", "x1": "codex", "c2": "claude"} {
		row, ok := idx[id]
		if !ok {
			t.Fatalf("세션 %s 이 목록에 없다: %+v", id, got.Sessions)
		}
		if row.Platform != want {
			t.Errorf("%s.platform = %q, want %q", id, row.Platform, want)
		}
	}

	// ③ 기존 필드가 그대로다.
	if c1 := idx["c1"]; c1.Username != "alice" || c1.Model != "claude-opus-4-8" ||
		c1.Input != 1000 || c1.Output != 2000 || c1.Turns != 10 || c1.StartedAt == nil {
		t.Errorf("기존 필드가 흔들렸다: %+v", c1)
	}
}

// ① 상세. 목록에만 실으면 드릴다운을 열자마자 값이 사라진다.
func TestSessionDetailCarriesPlatform(t *testing.T) {
	openDB(t)
	seedScope(t)
	h := New(testCfg(false))

	for id, want := range map[string]string{"c1": "claude", "x1": "codex", "c2": "claude"} {
		var got sessionDetailProbe
		getJSON(t, h, "/api/usage/sessions/"+id, &got)
		if got.Session.SessionID != id {
			t.Fatalf("상세 %s: sessionId = %q", id, got.Session.SessionID)
		}
		if got.Session.Platform != want {
			t.Errorf("상세 %s.platform = %q, want %q", id, got.Session.Platform, want)
		}
	}
}

/*
 * 목록과 상세가 **같은 세션에 같은 값**을 말한다.
 *
 * 두 응답이 서로 다른 조립부를 지나므로(toSessionDTO 를 공유하지만 호출 경로가 다르다)
 * 한쪽만 고치는 실수가 실재한다. 그 어긋남은 화면을 두 번 열어 봐야만 보인다.
 */
func TestSessionListAndDetailAgreeOnPlatform(t *testing.T) {
	openDB(t)
	seedScope(t)
	h := New(testCfg(false))

	var list sessionsProbe
	getJSON(t, h, "/api/usage/sessions", &list)
	for _, row := range list.Sessions {
		var detail sessionDetailProbe
		getJSON(t, h, "/api/usage/sessions/"+row.SessionID, &detail)
		if detail.Session.Platform != row.Platform {
			t.Errorf("%s: 목록 %q != 상세 %q",
				row.SessionID, row.Platform, detail.Session.Platform)
		}
	}
}

/*
 * platform 필터와 응답 필드가 **같은 사실**을 말한다.
 *
 * `?platform=codex` 로 좁힌 목록의 모든 행이 codex 여야 한다. 필터는 저장 계층 컬럼을 보고
 * 응답 필드는 DTO 가 만드는데, 둘이 갈리면 화면이 "codex 로 걸렀는데 claude 세션이 보인다"는
 * 상태가 된다 — 이 축에서 가장 나쁜 실패다(요청과 다른 모집단이 요청한 이름으로 돌아온다).
 */
func TestSessionListPlatformFieldMatchesFilter(t *testing.T) {
	openDB(t)
	seedScope(t)
	h := New(testCfg(false))

	for _, want := range []string{"claude", "codex"} {
		var got sessionsProbe
		getJSON(t, h, "/api/usage/sessions?platform="+want, &got)
		if len(got.Sessions) == 0 {
			t.Fatalf("platform=%s 결과가 비었다", want)
		}
		for _, row := range got.Sessions {
			if row.Platform != want {
				t.Errorf("platform=%s 로 걸렀는데 %s.platform = %q", want, row.SessionID, row.Platform)
			}
		}
	}
}
