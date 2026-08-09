package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tscorp/user-usage/internal/config"
	"github.com/tscorp/user-usage/internal/db"
	"github.com/tscorp/user-usage/internal/identity"
	"github.com/tscorp/user-usage/internal/store"
	"github.com/tscorp/user-usage/internal/tenant"
)

/*
 * 여기서 재는 것은 **골든이 못 보는 배선**이다.
 *
 * 골든 44개는 local 모드의 응답 본문을 잡는다. 그러나 다음은 그쪽에 안 잡힌다:
 *   · readOnly(remote) 모드의 라우트 등록 — 골든은 local 로만 돈다
 *   · 라우트 순서(analytics 가 admin 앞) — 순서가 뒤집히면 관측 화면이 통째로 404 다
 *   · 예상 못 한 예외의 원문 유출 — 골든에는 그 요청이 없다
 *   · 정적 화이트리스트와 보안 헤더 — 골든은 API 만 본다
 *   · 인테이크 응답의 개수 출처(…UpsertN) — 틀리면 값만 0 이고 200 이다
 */

const (
	testAdmin  = "test-admin-token-0123456789"
	testIntake = "test-intake-token-9876543210"
)

func testCfg(readOnly bool) config.Config {
	mode := "local"
	if readOnly {
		mode = "remote"
	}
	return config.Config{
		Token: testAdmin, IntakeToken: testIntake,
		Mode: mode, Host: "127.0.0.1", Port: 4191, Tenant: "default", ReadOnly: readOnly,
	}
}

// openDB 는 테스트마다 빈 sqlite 를 열고 스키마를 보장한다.
// store·identity 는 패키지 전역 핸들을 쓰므로 이 테스트들은 **직렬**로 돈다(t.Parallel 금지).
func openDB(t *testing.T) db.DB {
	t.Helper()
	ctx := tenant.With(context.Background(), "default")
	d, err := db.Open(ctx, db.Options{Mode: "local", DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := store.Init(ctx, d); err != nil {
		t.Fatalf("store.Init: %v", err)
	}
	if err := identity.Init(ctx, d); err != nil {
		t.Fatalf("identity.Init: %v", err)
	}
	return d
}

type reqOpt func(*http.Request)

func withAdmin(r *http.Request)  { r.Header.Set("Authorization", "Bearer "+testAdmin) }
func withIntake(r *http.Request) { r.Header.Set("Authorization", "Bearer "+testIntake) }
func withCookie(r *http.Request) { r.Header.Set("Cookie", "usage_tok="+testAdmin) }

func do(t *testing.T, h http.Handler, method, target string, body string, opts ...reqOpt) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	for _, o := range opts {
		o(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("응답이 JSON 이 아니다 (%d): %q", w.Code, w.Body.String())
	}
	return m
}

/* ── 게이트 위: /healthz ─────────────────────────────────────────────── */

func TestHealthzNeedsNoTokenAndNoDB(t *testing.T) {
	// DB 를 열지 않는다 — 무DB 라는 사실 자체를 잰다.
	h := New(testCfg(false))
	w := do(t, h, http.MethodGet, "/healthz", "")
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != contentTypeJSON {
		t.Fatalf("content-type = %q", got)
	}
	if decode(t, w)["status"] != "ok" {
		t.Fatalf("body = %s", w.Body.String())
	}
}

/* ── 인증 ────────────────────────────────────────────────────────────── */

func TestNoTokenIs401WithChallenge(t *testing.T) {
	openDB(t)
	h := New(testCfg(false))
	w := do(t, h, http.MethodGet, "/api/usage/summary", "")
	if w.Code != 401 {
		t.Fatalf("status = %d", w.Code)
	}
	if got := w.Header().Get("WWW-Authenticate"); got != `Bearer realm="usage"` {
		t.Fatalf("WWW-Authenticate = %q — 화면이 이 헤더로 토큰 입력을 안내한다", got)
	}
	if decode(t, w)["error"] != "unauthorized" {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestWrongHeaderDoesNotFallBackToCookie(t *testing.T) {
	// 폴백이 있으면 게이트가 흐려진다 — 헤더를 틀리게 보낸 요청이 쿠키로 통과하는 자리가 생긴다.
	openDB(t)
	h := New(testCfg(false))
	w := do(t, h, http.MethodGet, "/api/usage/summary", "", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer 0000000000000000000000")
		withCookie(r)
	})
	if w.Code != 401 {
		t.Fatalf("status = %d — 틀린 헤더가 쿠키로 흘렀다", w.Code)
	}
}

func TestIntakeTokenIsNotAcceptedViaCookie(t *testing.T) {
	/*
	 * 인테이크 토큰의 보고자는 수집기이지 브라우저가 아니다. 쿠키로 받아 주면 브라우저를 꾀어
	 * 임의 사용량을 밀어 넣는 자리가 생긴다.
	 */
	openDB(t)
	h := New(testCfg(false))
	w := do(t, h, http.MethodPost, "/api/usage", `{"sessions":[]}`, func(r *http.Request) {
		r.Header.Set("Cookie", "usage_tok="+testIntake)
	})
	if w.Code != 401 {
		t.Fatalf("status = %d — 인테이크 토큰이 쿠키로 통과했다", w.Code)
	}
}

func TestIntakeScopeOpensOnlyPostUsage(t *testing.T) {
	openDB(t)
	h := New(testCfg(false))

	w := do(t, h, http.MethodGet, "/api/usage/summary", "", withIntake)
	if w.Code != 403 {
		t.Fatalf("조회 status = %d, want 403", w.Code)
	}
	if !strings.Contains(decode(t, w)["error"].(string), "USAGE_ADMIN_TOKEN") {
		t.Fatalf("403 문구가 무엇을 써야 하는지 알려주지 않는다: %s", w.Body.String())
	}
	// 그 하나는 열려 있어야 한다 — 게이트가 과하게 잠기면 보고가 통째로 멈춘다.
	w = do(t, h, http.MethodPost, "/api/usage", `{"sessions":[]}`, withIntake)
	if w.Code != 200 {
		t.Fatalf("POST /api/usage status = %d, want 200", w.Code)
	}
}

func TestCookieCredentialCannotMutate(t *testing.T) {
	// 브라우저는 임의 헤더를 붙일 수 없으니 화면은 자연히 조회 전용이 되고, CSRF 표면이 안 생긴다.
	openDB(t)
	h := New(testCfg(false))
	w := do(t, h, http.MethodDelete, "/api/usage/identity?machine=host-a", "", withCookie)
	if w.Code != 403 {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if !strings.Contains(decode(t, w)["error"].(string), "Authorization") {
		t.Fatalf("403 문구에 대안이 없다: %s", w.Body.String())
	}
}

func TestCookieCredentialCanRead(t *testing.T) {
	// 게이트가 과하게 잠기지 않았는지 — 쿠키는 조회를 태워야 한다.
	openDB(t)
	h := New(testCfg(false))
	w := do(t, h, http.MethodGet, "/api/usage/summary?days=365&top=3", "", withCookie)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
}

func TestCookieValueIsPercentDecoded(t *testing.T) {
	// 클라이언트가 encodeURIComponent 로 넣으므로 디코딩이 없으면 특수문자 토큰이 조용히 안 맞는다.
	openDB(t)
	cfg := testCfg(false)
	cfg.Token = "tok/en with space+plus"
	h := New(cfg)
	w := do(t, h, http.MethodGet, "/api/usage/summary?days=1", "", func(r *http.Request) {
		r.Header.Set("Cookie", "usage_tok=tok%2Fen%20with%20space+plus")
	})
	if w.Code != 200 {
		t.Fatalf("status = %d — 쿠키 디코딩이 다르다: %s", w.Code, w.Body.String())
	}
}

/* ── 라우트 순서는 계약이다 ──────────────────────────────────────────── */

func TestAnalyticsIsRegisteredBeforeAdmin(t *testing.T) {
	/*
	 * admin 이 /api/usage 접두사를 통째로 소유하고 안 걸리면 404 를 직접 낸다. 순서가 뒤집히면
	 * 관측 화면이 **통째로** 404 가 된다 — 그런데 서버는 멀쩡하고 로그도 조용하다.
	 */
	openDB(t)
	h := New(testCfg(false))
	for _, p := range []string{
		"/api/usage/series?interval=day",
		"/api/usage/distribution",
		"/api/usage/sessions",
		"/api/usage/quality",
		"/api/usage/coverage",
		"/api/usage/leaderboard",
		"/api/usage/dispatch",
	} {
		w := do(t, h, http.MethodGet, p, "", withAdmin)
		if w.Code != 200 {
			t.Fatalf("%s → %d (analytics 가 admin 뒤에 있으면 404 가 된다): %s", p, w.Code, w.Body.String())
		}
	}
}

func TestUnknownUsagePathIs404FromPrefixOwner(t *testing.T) {
	openDB(t)
	h := New(testCfg(false))
	w := do(t, h, http.MethodGet, "/api/usage/nope", "", withAdmin)
	if w.Code != 404 || decode(t, w)["error"] != "not found" {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestUnknownSessionIs404WithItsOwnMessage(t *testing.T) {
	openDB(t)
	h := New(testCfg(false))
	w := do(t, h, http.MethodGet, "/api/usage/sessions/does-not-exist", "", withAdmin)
	if w.Code != 404 || decode(t, w)["error"] != "없는 세션" {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

/* ── readOnly(remote) 모드 ───────────────────────────────────────────── */

func TestReadOnlyDoesNotRegisterIntake(t *testing.T) {
	openDB(t)
	h := New(testCfg(true))
	w := do(t, h, http.MethodPost, "/api/usage", `{"sessions":[]}`, withAdmin)
	if w.Code != 404 {
		t.Fatalf("status = %d, want 404 — remote 모드에 인테이크가 등록됐다", w.Code)
	}
}

func TestReadOnlyMutationsAre404NotMethodNotAllowed(t *testing.T) {
	/*
	 * 405 가 아니라 404 인 이유: 이 모드에서 그 엔드포인트는 "지금은 막혔다"가 아니라
	 * **존재하지 않는다.** 405 로 내면 "권한을 올리면 쓸 수 있다"로 읽힌다.
	 */
	openDB(t)
	h := New(testCfg(true))
	for _, m := range []string{http.MethodPut, http.MethodPost, http.MethodDelete} {
		w := do(t, h, m, "/api/usage/identity?machine=host-a", `{"machine":"host-a","username":"x"}`, withAdmin)
		if w.Code != 404 {
			t.Fatalf("%s → %d, want 404", m, w.Code)
		}
	}
}

func TestReadOnlyStillServesReads(t *testing.T) {
	openDB(t)
	h := New(testCfg(true))
	for _, p := range []string{"/api/usage/summary", "/api/usage/identity", "/api/usage/series"} {
		w := do(t, h, http.MethodGet, p, "", withAdmin)
		if w.Code != 200 {
			t.Fatalf("%s → %d, want 200: %s", p, w.Code, w.Body.String())
		}
	}
}

/* ── 인테이크 응답의 개수 출처 ───────────────────────────────────────── */

func TestIntakeResponseCountsWhatWasStored(t *testing.T) {
	/*
	 * 개수를 …UpsertN 이 아니라 error-only 함수에서 얻으면 값이 전부 0 인데 요청은 200 이다.
	 * 화면은 "보고됨"으로 보이고 아무 에러도 안 난다 — 그래서 여기서 잰다.
	 */
	openDB(t)
	h := New(testCfg(false))
	body := `{"user":"alice","machine":"host-a","sessions":[{
	  "sessionId":"S-count-0001","model":"claude-sonnet-5","project":"p",
	  "input":10,"output":20,"cacheRead":30,"cacheCreate":40,"turns":3,
	  "startedAt":"2026-08-02T03:00:00.000Z",
	  "counters":{"tool":{"Read":5,"Edit":2}},
	  "series":[{"hour":"2026-08-02T03","model":"claude-sonnet-5","input":10,"output":20,"turns":3}]
	}]}`
	w := do(t, h, http.MethodPost, "/api/usage", body, withAdmin)
	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	got := decode(t, w)
	for k, want := range map[string]float64{"sessions": 1, "counters": 2, "buckets": 1} {
		if got[k] != want {
			t.Fatalf("%s = %v, want %v (…UpsertN 을 부르지 않았다) — 전체: %s", k, got[k], want, w.Body.String())
		}
	}
	if got["ok"] != true {
		t.Fatalf("ok = %v", got["ok"])
	}
}

func TestIntakeIsIdempotent(t *testing.T) {
	// 수집기는 실패 시 재시도하는 best-effort 경로라 중복 전송이 정상 동작에 포함된다.
	openDB(t)
	h := New(testCfg(false))
	body := `{"user":"a","machine":"m","sessions":[{"sessionId":"S1-fixture-001","model":"claude-sonnet-5",
	  "input":100,"turns":2,"startedAt":"2026-08-02T03:00:00.000Z","counters":{"tool":{"Read":5}}}]}`
	do(t, h, http.MethodPost, "/api/usage", body, withAdmin)
	do(t, h, http.MethodPost, "/api/usage", body, withAdmin)

	w := do(t, h, http.MethodGet, "/api/usage/summary?days=365", "", withAdmin)
	totals := decode(t, w)["totals"].(map[string]any)
	if totals["input"] != float64(100) {
		t.Fatalf("input = %v, want 100 — 두 번째 보고가 누적됐다(upsert 가 아니다)", totals["input"])
	}
	if totals["sessions"] != float64(1) {
		t.Fatalf("sessions = %v, want 1", totals["sessions"])
	}
}

func TestIntakeSurvivesBrokenBody(t *testing.T) {
	// 여기서 400 을 내면 그 사람 사용량이 통째로 사라진다. 0건 응답으로 접는다.
	openDB(t)
	h := New(testCfg(false))
	for _, body := range []string{`{`, `[]`, `null`, `{"sessions":"nope"}`} {
		w := do(t, h, http.MethodPost, "/api/usage", body, withAdmin)
		if w.Code != 200 {
			t.Fatalf("body=%q → %d, want 200", body, w.Code)
		}
		if decode(t, w)["sessions"] != float64(0) {
			t.Fatalf("body=%q → %s", body, w.Body.String())
		}
	}
}

/* ── 응답 shape: nil 이 null 로 새지 않는다 ──────────────────────────── */

func TestEmptyCollectionsAreEmptyNotNull(t *testing.T) {
	/*
	 * nil 슬라이스는 JSON 에서 null 로 나가고 화면은 `arr.map` 을 부르다 죽는다 —
	 * 그리고 그건 **데이터가 없는 테넌트에서만** 나타난다.
	 */
	openDB(t)
	h := New(testCfg(false))

	raw := do(t, h, http.MethodGet, "/api/usage/summary", "", withAdmin).Body.String()
	for _, needle := range []string{`"byDay":[]`, `"byUser":[]`, `"byModel":[]`, `"gaps":[]`, `"tool":[]`} {
		if !strings.Contains(raw, needle) {
			t.Fatalf("summary 에 %s 가 없다: %s", needle, raw)
		}
	}
	raw = do(t, h, http.MethodGet, "/api/usage/series?interval=day", "", withAdmin).Body.String()
	for _, needle := range []string{`"series":[]`, `"unpriced":[]`, `"groupBy":[]`} {
		if !strings.Contains(raw, needle) {
			t.Fatalf("series 에 %s 가 없다: %s", needle, raw)
		}
	}
	raw = do(t, h, http.MethodGet, "/api/usage/identity", "", withAdmin).Body.String()
	for _, needle := range []string{`"items":[]`, `"unmapped":[]`} {
		if !strings.Contains(raw, needle) {
			t.Fatalf("identity 에 %s 가 없다: %s", needle, raw)
		}
	}
}

func TestSeriesKeyIsEmptyObjectWhenUngrouped(t *testing.T) {
	// 화면이 key 를 객체로 훑는다. null 이면 그 자리에서 죽는다.
	openDB(t)
	h := New(testCfg(false))
	body := `{"user":"a","machine":"m","sessions":[{"sessionId":"S1-fixture-001","model":"claude-sonnet-5",
	  "input":100,"turns":2,"startedAt":"2026-08-02T03:00:00.000Z"}]}`
	do(t, h, http.MethodPost, "/api/usage", body, withAdmin)
	raw := do(t, h, http.MethodGet,
		"/api/usage/series?from=2026-08-01&to=2026-08-03&interval=day", "", withAdmin).Body.String()
	if !strings.Contains(raw, `"key":{}`) {
		t.Fatalf(`"key":{} 가 없다: %s`, raw)
	}
}

func TestSessionDetailCountersIsObjectNotNull(t *testing.T) {
	openDB(t)
	h := New(testCfg(false))
	body := `{"user":"a","machine":"m","sessions":[{"sessionId":"S1-fixture-001","model":"claude-sonnet-5",
	  "turns":0,"startedAt":"2026-08-02T03:00:00.000Z"}]}`
	do(t, h, http.MethodPost, "/api/usage", body, withAdmin)
	raw := do(t, h, http.MethodGet, "/api/usage/sessions/S1-fixture-001", "", withAdmin).Body.String()
	if !strings.Contains(raw, `"counters":{}`) || !strings.Contains(raw, `"series":[]`) {
		t.Fatalf("counters/series 가 null 로 나갔다: %s", raw)
	}
}

/* ── 검증 400 은 안내를 남긴다 ───────────────────────────────────────── */

func TestValidationErrorsKeepTheirMessage(t *testing.T) {
	openDB(t)
	h := New(testCfg(false))
	cases := map[string]string{
		"/api/usage/sessions?from=2026-8-1": "from 은 YYYY-MM-DD 형식이어야 합니다",
		"/api/usage/series?metric=vibes":    "metric 은 cost|tokens|sessions|turns 중 하나입니다",
		"/api/usage/series?interval=minute": "interval 은 day|hour|week 중 하나입니다",
		"/api/usage/sessions?sort=nope":     "정렬축은 cost|cacheRead|output|turns|startedAt 중 하나입니다",
		"/api/usage/series?group_by=zodiac": "알 수 없는 그룹 축: zodiac",
	}
	for target, want := range cases {
		w := do(t, h, http.MethodGet, target, "", withAdmin)
		if w.Code != 400 {
			t.Fatalf("%s → %d, want 400", target, w.Code)
		}
		if got := decode(t, w)["error"]; got != want {
			t.Fatalf("%s → %q, want %q", target, got, want)
		}
	}
}

func TestGroupByAcceptsAtMostThreeAxes(t *testing.T) {
	openDB(t)
	h := New(testCfg(false))
	w := do(t, h, http.MethodGet, "/api/usage/series?group_by=user,model,project,machine", "", withAdmin)
	if w.Code != 400 || decode(t, w)["error"] != "그룹 축은 최대 3개입니다" {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

/* ── 예상 못 한 예외: 원문을 클라이언트로 보내지 않는다 ─────────────── */

func TestUnexpectedFailureIsRedacted(t *testing.T) {
	/*
	 * 여기로 오는 것은 대개 DB 드라이버 에러다 — 테이블·컬럼명, 제약 이름, 때로는 접속 정보
	 * 조각을 문장에 담는다. 클라이언트에는 `400 {"error":"bad request"}` 만 간다.
	 */
	ctx := tenant.With(context.Background(), "default")
	d, err := db.Open(ctx, db.Options{Mode: "local", DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := store.Init(ctx, d); err != nil {
		t.Fatalf("store.Init: %v", err)
	}
	if err := identity.Init(ctx, d); err != nil {
		t.Fatalf("identity.Init: %v", err)
	}
	// 핸들을 닫아 조회가 드라이버 에러를 내게 만든다.
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	h := New(testCfg(false))
	w := do(t, h, http.MethodGet, "/api/usage/summary", "", withAdmin)
	if w.Code != 400 {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if got := decode(t, w)["error"]; got != "bad request" {
		t.Fatalf("error = %q — 원문이 새고 있다", got)
	}
	body := w.Body.String()
	for _, leak := range []string{"sql", "SELECT", "usage_sessions", "database"} {
		if strings.Contains(body, leak) {
			t.Fatalf("응답에 드라이버 원문 조각(%q)이 있다: %s", leak, body)
		}
	}
}

/* ── 정적 서빙 ───────────────────────────────────────────────────────── */

func TestStaticWhitelistServesShellWithSecurityHeaders(t *testing.T) {
	h := New(testCfg(false)) // 무인증이다 — DB 도 필요 없다
	w := do(t, h, http.MethodGet, "/", "")
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	want := map[string]string{
		"Content-Type":            "text/html; charset=utf-8",
		"X-Frame-Options":         "DENY",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "same-origin",
		"Content-Security-Policy": csp,
		"Cache-Control":           "no-cache",
	}
	for k, v := range want {
		if got := w.Header().Get(k); got != v {
			t.Fatalf("%s = %q, want %q", k, got, v)
		}
	}
	if w.Body.Len() == 0 {
		t.Fatal("본문이 비었다")
	}
}

/*
 * ⚠ 셸 자산 열거(구 TestStaticWhitelistCoversEveryShellAsset)와 HEAD 테스트(구
 *   TestHeadOnStaticSendsHeadersWithoutBody)는 **static_test.go 로 옮겼다.**
 *
 *   두 테스트는 `/app.js` · `/js/core.js` · `/views/*.js` 를 손으로 열거했다 — 구 바닐라
 *   프런트(public/)의 파일 목록이다. webroot/ 가 Next 정적 export 로 교체되면서 그 URL 들은
 *   더 이상 존재하지 않고(파일명이 콘텐츠 해시다), 손으로 적은 목록은 빌드마다 깨진다.
 *
 *   같은 보장은 static_test.go 가 **생성된 표에 대해** 더 강하게 잰다:
 *     · TestWhitelistIsExactlyTheEmbeddedSetPlusRoot — 표 == 임베드된 파일 집합
 *     · TestIndexHTMLReferencesOnlyEmbeddedAssets    — 셸이 가리키는 자산이 전부 200
 *     · TestHeadOnStaticSendsHeadersWithoutBody      — HEAD(본문 없음 · Content-Length 있음)
 *   그리고 구 URL 들이 이제 404 라는 것 자체를 TestPathsOutsideTheWhitelistAre404JSON 이 잡는다.
 *
 *   (go-embed 오너가 옮겼다. server_test.go 는 go-http 소유라 이 주석 외에는 손대지 않았다.)
 */

func TestStaticRejectsAnythingOutsideTheWhitelist(t *testing.T) {
	/*
	 * 경로 화이트리스트라 탈출이라는 문제 **자체가 성립하지 않는다.** 그래도 잰다 —
	 * 나중에 누가 디렉터리 서빙으로 바꾸면 이 테스트가 먼저 빨개진다.
	 */
	h := New(testCfg(false))
	for _, p := range []string{
		"/views/../app.js",
		"/views/%2e%2e/app.js",
		"/views/sub/dir.js",
		"/views/Upper.js",
		"/js/../../server.js",
		"/package.json",
		"/webroot/app.js",
	} {
		w := do(t, h, http.MethodGet, p, "")
		if w.Code == 200 {
			t.Fatalf("%s 가 200 으로 서빙됐다", p)
		}
	}
}

func TestNonAPIUnknownPathIs404JSON(t *testing.T) {
	h := New(testCfg(false))
	w := do(t, h, http.MethodGet, "/definitely-not-a-thing", "")
	if w.Code != 404 {
		t.Fatalf("status = %d", w.Code)
	}
	if decode(t, w)["error"] != "not found" {
		t.Fatalf("body = %s", w.Body.String())
	}
}

/* ── 보존 정책이 응답에 실린다 ───────────────────────────────────────── */

func TestRetentionPolicyIsInTheResponse(t *testing.T) {
	// 데이터가 언젠가 사라진다는 사실은 보는 사람이 알아야 한다(추세가 끊기는 이유).
	openDB(t)

	off := New(testCfg(false))
	raw := do(t, off, http.MethodGet, "/api/usage/summary", "", withAdmin).Body.String()
	if !strings.Contains(raw, `"keywordDays":null`) {
		t.Fatalf("무기한 보관이 null 로 안 나갔다: %s", raw)
	}

	cfg := testCfg(false)
	d := 30
	cfg.KeywordRetentionDays = &d
	raw = do(t, New(cfg), http.MethodGet, "/api/usage/summary", "", withAdmin).Body.String()
	if !strings.Contains(raw, `"keywordDays":30`) {
		t.Fatalf("보존일이 안 실렸다: %s", raw)
	}
}

/* ── 귀속 교정 ───────────────────────────────────────────────────────── */

func TestIdentitySetRestampsPastRowsAndRejectsEmptyUsername(t *testing.T) {
	openDB(t)
	h := New(testCfg(false))
	body := `{"user":"osname","machine":"host-x","sessions":[{"sessionId":"S1-fixture-001",
	  "model":"claude-sonnet-5","input":10,"turns":1,"startedAt":"2026-08-02T03:00:00.000Z",
	  "counters":{"tool":{"Read":1}}}]}`
	do(t, h, http.MethodPost, "/api/usage", body, withAdmin)

	w := do(t, h, http.MethodPut, "/api/usage/identity",
		`{"machine":"host-x","username":"realname","note":"n"}`, withAdmin)
	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	got := decode(t, w)
	if got["ok"] != true || got["username"] != "realname" {
		t.Fatalf("body = %s", w.Body.String())
	}
	moved := got["moved"].(map[string]any)
	if moved["sessions"] != float64(1) {
		t.Fatalf("소급 재스탬프가 안 돌았다: %s", w.Body.String())
	}

	// 빈 username 은 거부한다 — 실수로 귀속을 지우지 못하게. 400 이지만 **안내 문구를 남긴다.**
	w = do(t, h, http.MethodPut, "/api/usage/identity", `{"machine":"host-x","username":"  "}`, withAdmin)
	if w.Code != 400 {
		t.Fatalf("빈 username status = %d, want 400", w.Code)
	}
	if msg, _ := decode(t, w)["error"].(string); msg == "bad request" || msg == "" {
		t.Fatalf("검증 400 의 안내가 지워졌다: %s", w.Body.String())
	}
}

/* ── JS Number() 의미론 ──────────────────────────────────────────────── */

func TestNumOrMatchesJavaScriptFalsyRules(t *testing.T) {
	// 현행 라우트가 `Number(x) || 기본값` 으로 읽는다. 0 과 NaN 이 둘 다 기본값으로 떨어진다.
	cases := []struct {
		raw  string
		want float64
	}{
		{"", 30}, {"abc", 30}, {"0", 30}, {"365", 365}, {" 12 ", 12}, {"12.9", 12}, {"-3", -3},
	}
	for _, c := range cases {
		if got := numOr(c.raw, 30); got != c.want {
			t.Fatalf("numOr(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}
