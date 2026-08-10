package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/tscorp/user-usage/internal/store"
	"github.com/tscorp/user-usage/internal/tenant"
)

/*
 * platform 축의 HTTP 표면.
 *
 * 여기서 재는 것은 세 가지다:
 *   ① 인테이크가 선택적 platform 필드를 저장까지 배선하는가(미지정→claude, 미지 값→other)
 *   ② 기존 조회 엔드포인트의 platform= 필터가 **미지정일 때 현행 그대로**인가(골든 무회귀의 근거)
 *   ③ 신규 /api/usage/platforms 가 admin 전용이고 약속한 shape 를 내는가
 */

func seedPlatforms(t *testing.T) {
	t.Helper()
	ctx := tenant.With(t.Context(), "default")
	rows := []store.SessionInput{
		{SessionID: "c1", Username: "alice", Machine: "m1", Model: "claude-opus-4-8", Platform: "claude",
			Input: 1000, Output: 2000, CacheRead: 3000, CacheCreate: 4000, Turns: 5,
			StartedAt: "2026-08-03T09:00:00.000Z"},
		// platform 미지정 — 현행 수집기의 보고. claude 로 채워져야 한다.
		{SessionID: "c2", Username: "alice", Machine: "m1", Model: "claude-opus-4-8",
			Input: 10, Output: 20, Turns: 1, StartedAt: "2026-08-04T09:00:00.000Z"},
		{SessionID: "x1", Username: "bob", Machine: "m2", Model: "gpt-5-codex", Platform: "codex",
			Input: 100, Output: 200, Turns: 2, StartedAt: "2026-08-05T09:00:00.000Z"},
	}
	for _, r := range rows {
		if err := store.SessionUpsert(ctx, r); err != nil {
			t.Fatalf("seed %s: %v", r.SessionID, err)
		}
	}
	if _, err := store.SeriesUpsertN(ctx, store.SeriesInput{
		SessionID: "x1", Username: "bob", Machine: "m2",
		Rows: []store.SeriesRow{{Hour: "2026-08-05T09", Model: "gpt-5-codex", Output: 200, Turns: 2}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SeriesUpsertN(ctx, store.SeriesInput{
		SessionID: "c1", Username: "alice", Machine: "m1",
		Rows: []store.SeriesRow{{Hour: "2026-08-03T09", Model: "claude-opus-4-8", Output: 2000, Turns: 5}},
	}); err != nil {
		t.Fatal(err)
	}
}

// ① 인테이크 — 선택적 필드가 저장까지 간다.
func TestIntakeWiresPlatform(t *testing.T) {
	openDB(t)
	h := New(testCfg(false))

	body := `{"sessions":[
		{"id":"aaaaaaaa-0001","platform":"codex","output":5,"startedAt":"2026-08-03T09:00:00.000Z"},
		{"id":"aaaaaaaa-0002","platform":"grok","output":5,"startedAt":"2026-08-03T09:00:00.000Z"},
		{"id":"aaaaaaaa-0003","output":5,"startedAt":"2026-08-03T09:00:00.000Z"}
	]}`
	rec := do(t, h, http.MethodPost, "/api/usage", body, withIntake)
	if rec.Code != http.StatusOK {
		t.Fatalf("인테이크: %d %s", rec.Code, rec.Body.String())
	}

	want := map[string]string{
		"aaaaaaaa-0001": "codex",
		"aaaaaaaa-0002": store.PlatformOther, // 알 수 없는 값은 거부하지도 claude 로 접지도 않는다
		"aaaaaaaa-0003": store.PlatformDefault,
	}
	ctx := tenant.With(t.Context(), "default")
	rows, err := store.SessionRows(ctx, store.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range rows {
		got[r.SessionID] = r.Platform
	}
	for id, w := range want {
		if got[id] != w {
			t.Fatalf("%s platform = %q (기대 %q)", id, got[id], w)
		}
	}
}

// ③ 신규 엔드포인트 — 응답 shape 와 값.
func TestPlatformsEndpoint(t *testing.T) {
	openDB(t)
	seedPlatforms(t)
	h := New(testCfg(false))

	rec := do(t, h, http.MethodGet, "/api/usage/platforms", "", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Platforms []struct {
			Platform    string  `json:"platform"`
			Sessions    int     `json:"sessions"`
			Input       int64   `json:"input"`
			Output      int64   `json:"output"`
			CacheRead   int64   `json:"cacheRead"`
			CacheCreate int64   `json:"cacheCreate"`
			CostUsd     float64 `json:"costUsd"`
			FirstSeen   string  `json:"firstSeen"`
			LastSeen    string  `json:"lastSeen"`
		} `json:"platforms"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("응답 파싱: %v — %s", err, rec.Body.String())
	}
	if len(body.Platforms) != 2 {
		t.Fatalf("플랫폼 수 = %d: %s", len(body.Platforms), rec.Body.String())
	}
	// 세션 수 내림차순 — claude 2건이 codex 1건보다 앞이다.
	c := body.Platforms[0]
	if c.Platform != "claude" || c.Sessions != 2 {
		t.Fatalf("첫 행: %+v", c)
	}
	if c.Input != 1010 || c.Output != 2020 || c.CacheRead != 3000 || c.CacheCreate != 4000 {
		t.Fatalf("claude 합계: %+v", c)
	}
	if c.FirstSeen != "2026-08-03T09:00:00.000Z" || c.LastSeen != "2026-08-04T09:00:00.000Z" {
		t.Fatalf("claude 관측 경계: %q ~ %q", c.FirstSeen, c.LastSeen)
	}
	if c.CostUsd <= 0 {
		t.Fatalf("단가가 등록된 모델인데 비용이 0 이다: %v", c.CostUsd)
	}
	x := body.Platforms[1]
	if x.Platform != "codex" || x.Sessions != 1 || x.Output != 200 {
		t.Fatalf("codex 행: %+v", x)
	}

	// 필터를 걸면 그 플랫폼만.
	rec = do(t, h, http.MethodGet, "/api/usage/platforms?platform=codex", "", withAdmin)
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Platforms) != 1 || body.Platforms[0].Platform != "codex" {
		t.Fatalf("필터: %s", rec.Body.String())
	}

	// 데이터가 없어도 배열은 [] 다(null 이 아니다 — 화면이 map 을 부르다 죽는다).
	rec = do(t, h, http.MethodGet, "/api/usage/platforms?from=2020-01-01&to=2020-01-02", "", withAdmin)
	if got := rec.Body.String(); got != "{\"platforms\":[]}\n" && got != "{\"platforms\":[]}" {
		t.Fatalf("빈 결과 = %q", got)
	}
}

// ③' admin 스코프 — member 는 403(deny-by-default 화이트리스트에 없다).
func TestPlatformsEndpointIsAdminOnly(t *testing.T) {
	openDB(t)
	seedPlatforms(t)
	ctx := tenant.With(t.Context(), "default")
	tok, err := store.IssueMemberToken(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	h := New(testCfg(false))
	rec := do(t, h, http.MethodGet, "/api/usage/platforms", "",
		func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) })
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member: %d (기대 403)", rec.Code)
	}
}

// ② 필터 — 미지정이면 현행 그대로, 지정하면 갈린다.
func TestPlatformQueryFilterOnExistingEndpoints(t *testing.T) {
	openDB(t)
	seedPlatforms(t)
	h := New(testCfg(false))

	// 세션 목록: 미지정 = 전체 3건.
	rec := do(t, h, http.MethodGet, "/api/usage/sessions?from=2026-08-01&to=2026-08-31&top=500", "", withAdmin)
	var list struct {
		Sessions []struct {
			SessionID string `json:"sessionId"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Sessions) != 3 {
		t.Fatalf("미지정 필터가 전체가 아니다: %d", len(list.Sessions))
	}
	// 응답 shape 에 platform 이 **추가되지 않았다** — 기존 계약(골든 44개)의 근거.
	if _, exists := decode(t, rec)["platforms"]; exists {
		t.Fatal("세션 목록 응답에 새 필드가 생겼다")
	}
	var raw map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &raw)
	first := raw["sessions"].([]any)[0].(map[string]any)
	if _, exists := first["platform"]; exists {
		t.Fatal("세션 DTO 에 platform 필드가 새어 나갔다 — 기존 응답 shape 변경 금지")
	}

	// 지정하면 갈린다.
	rec = do(t, h, http.MethodGet, "/api/usage/sessions?from=2026-08-01&to=2026-08-31&platform=codex", "", withAdmin)
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Sessions) != 1 || list.Sessions[0].SessionID != "x1" {
		t.Fatalf("platform=codex 세션 목록: %s", rec.Body.String())
	}

	/*
	 * 시계열은 **두 테이블**에서 온다 — day 는 세션 행, hour 는 시간 버킷이다. 양쪽 다 걸려야 한다.
	 * 기대값이 다른 것은 필터가 아니라 모집단의 차이다: 세션 행은 input 100 + output 200,
	 * 버킷은 output 200 만 있다(이 축을 섞지 않는 것이 analytics.go 의 규율이다).
	 */
	for target, want := range map[string]float64{
		"/api/usage/series?metric=tokens&interval=day&platform=codex":  300,
		"/api/usage/series?metric=tokens&interval=hour&platform=codex": 200,
	} {
		rec := do(t, h, http.MethodGet, target, "", withAdmin)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", target, rec.Code, rec.Body.String())
		}
		var sr struct {
			Series []struct {
				Total float64 `json:"total"`
			} `json:"series"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &sr); err != nil {
			t.Fatal(err)
		}
		if len(sr.Series) != 1 || sr.Series[0].Total != want {
			t.Fatalf("%s → %s (기대 codex 토큰 %v)", target, rec.Body.String(), want)
		}
	}

	// 품질축(usage_series)도 세션 행으로 되짚어 걸린다.
	rec = do(t, h, http.MethodGet, "/api/usage/quality?platform=codex", "", withAdmin)
	var q struct {
		Turns int64 `json:"turns"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &q); err != nil {
		t.Fatal(err)
	}
	if q.Turns != 2 {
		t.Fatalf("quality platform=codex turns = %d (기대 2)", q.Turns)
	}

	// 리더보드도 걸린다 — 전사 화면이 플랫폼별로 갈린다.
	rec = do(t, h, http.MethodGet, "/api/usage/leaderboard?platform=claude", "", withAdmin)
	var lb struct {
		Users []struct {
			Username string `json:"username"`
		} `json:"users"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &lb); err != nil {
		t.Fatal(err)
	}
	if len(lb.Users) != 1 || lb.Users[0].Username != "alice" {
		t.Fatalf("leaderboard platform=claude: %s", rec.Body.String())
	}
}

// 오타는 400 이다. other 로 접으면 요청과 다른 집합이 조용히 돌아온다.
func TestPlatformFilterRejectsUnknownValue(t *testing.T) {
	openDB(t)
	h := New(testCfg(false))
	for _, target := range []string{
		"/api/usage/sessions?platform=claud",
		"/api/usage/platforms?platform=claud",
		"/api/usage/quality?platform=CLAUDE", // 필터는 정규화하지 않는다 — 대문자도 오타다
	} {
		if rec := do(t, h, http.MethodGet, target, "", withAdmin); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d (기대 400)", target, rec.Code)
		}
	}
}
