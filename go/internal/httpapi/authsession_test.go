package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tscorp/user-usage/internal/config"
	"github.com/tscorp/user-usage/internal/store"
	"github.com/tscorp/user-usage/internal/tenant"
)

// authCfg 는 세션 TTL 이 설정된 테스트 cfg 다(testCfg 는 TTL 이 0 이라 세션이 즉시 만료된다).
func authCfg() config.Config {
	c := testCfg(false)
	c.SessionTTL = time.Hour
	return c
}

// seedUser 는 tenant "default" 에 사람 계정을 만든다.
func seedUser(t *testing.T, username, role, password string) {
	t.Helper()
	ctx := tenant.With(t.Context(), "default")
	hash, err := store.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if err := store.CreateUser(ctx, username, role, hash); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
}

// login 은 로그인해서 세션 쿠키 값을 돌려준다(성공 가정).
func login(t *testing.T, h http.Handler, username, password string) string {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/api/auth/login",
		`{"username":"`+username+`","password":"`+password+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login %s: %d %s", username, rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "usage_sess" {
			return c.Value
		}
	}
	t.Fatalf("로그인 응답에 usage_sess 쿠키가 없다: %s", rec.Header().Get("Set-Cookie"))
	return ""
}

func sessionCookie(tok string) reqOpt {
	return func(r *http.Request) { r.Header.Set("Cookie", "usage_sess="+tok) }
}

func TestLoginSuccessSetsSessionCookieAndMeWorks(t *testing.T) {
	openDB(t)
	seedUser(t, "alice", "admin", "correct horse battery")
	h := New(authCfg())

	rec := do(t, h, http.MethodPost, "/api/auth/login",
		`{"username":"alice","password":"correct horse battery"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	got := decode(t, rec)
	if got["ok"] != true {
		t.Fatalf("ok = %v", got["ok"])
	}
	user := got["user"].(map[string]any)
	if user["username"] != "alice" || user["role"] != "admin" || user["tenant"] != "default" {
		t.Fatalf("user = %v", user)
	}

	// 쿠키 속성 검증.
	var sess *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "usage_sess" {
			sess = c
		}
	}
	if sess == nil {
		t.Fatal("usage_sess 쿠키가 없다")
	}
	if !sess.HttpOnly {
		t.Fatal("HttpOnly 가 아니다")
	}
	if sess.SameSite != http.SameSiteStrictMode {
		t.Fatalf("SameSite = %v, want Strict", sess.SameSite)
	}
	if sess.Path != "/" {
		t.Fatalf("Path = %q", sess.Path)
	}
	if sess.MaxAge != int(time.Hour.Seconds()) {
		t.Fatalf("Max-Age = %d, want %d", sess.MaxAge, int(time.Hour.Seconds()))
	}
	if sess.Secure {
		t.Fatal("평문 http 요청인데 Secure 가 붙었다")
	}

	// /me 가 세션으로 신원을 돌려준다.
	rec = do(t, h, http.MethodGet, "/api/auth/me", "", sessionCookie(sess.Value))
	if rec.Code != http.StatusOK {
		t.Fatalf("me status = %d: %s", rec.Code, rec.Body.String())
	}
	me := decode(t, rec)
	if me["username"] != "alice" || me["role"] != "admin" || me["tenant"] != "default" {
		t.Fatalf("me = %v", me)
	}
}

func TestLoginSecureWhenForwardedHTTPS(t *testing.T) {
	openDB(t)
	seedUser(t, "alice", "admin", "pw-pw-pw-pw")
	h := New(authCfg())
	rec := do(t, h, http.MethodPost, "/api/auth/login",
		`{"username":"alice","password":"pw-pw-pw-pw"}`,
		func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "https") })
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "usage_sess" && !c.Secure {
			t.Fatal("X-Forwarded-Proto=https 인데 Secure 가 없다")
		}
	}
}

func TestLoginWrongPasswordAndUnknownUserAreIndistinguishable(t *testing.T) {
	openDB(t)
	seedUser(t, "alice", "admin", "right-password-here")
	h := New(authCfg())

	wrong := do(t, h, http.MethodPost, "/api/auth/login",
		`{"username":"alice","password":"nope-nope-nope"}`)
	ghost := do(t, h, http.MethodPost, "/api/auth/login",
		`{"username":"ghost","password":"nope-nope-nope"}`)

	for _, rec := range []struct {
		label string
		code  int
		body  string
	}{
		{"wrong-pw", wrong.Code, wrong.Body.String()},
		{"unknown-user", ghost.Code, ghost.Body.String()},
	} {
		if rec.code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want 401", rec.label, rec.code)
		}
		if !strings.Contains(rec.body, "아이디 또는 비밀번호가 올바르지 않습니다") {
			t.Fatalf("%s: 실패 문구가 계약과 다르다: %s", rec.label, rec.body)
		}
	}
	// 두 실패 응답 본문이 완전히 같아야 한다(사용자 유무 노출 금지).
	if wrong.Body.String() != ghost.Body.String() {
		t.Fatalf("실패 응답이 사용자 유무를 구분한다:\n wrong=%s\n ghost=%s",
			wrong.Body.String(), ghost.Body.String())
	}
	// 실패에는 세션 쿠키가 붙지 않는다.
	if len(wrong.Result().Cookies()) != 0 {
		t.Fatal("실패 응답에 쿠키가 붙었다")
	}
}

func TestLogoutInvalidatesSessionAndExpiresCookie(t *testing.T) {
	openDB(t)
	seedUser(t, "alice", "admin", "the-password-value")
	h := New(authCfg())
	tok := login(t, h, "alice", "the-password-value")

	// 세션은 살아 있다.
	if rec := do(t, h, http.MethodGet, "/api/auth/me", "", sessionCookie(tok)); rec.Code != 200 {
		t.Fatalf("로그아웃 전 me = %d", rec.Code)
	}
	rec := do(t, h, http.MethodPost, "/api/auth/logout", "", sessionCookie(tok))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d, want 204", rec.Code)
	}
	// Max-Age=0 으로 쿠키를 만료시킨다.
	foundExpiry := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == "usage_sess" && c.MaxAge < 0 {
			foundExpiry = true
		}
	}
	if !foundExpiry {
		t.Fatalf("로그아웃이 쿠키를 만료시키지 않았다: %q", rec.Header().Get("Set-Cookie"))
	}
	// 세션이 무효화됐다.
	if rec := do(t, h, http.MethodGet, "/api/auth/me", "", sessionCookie(tok)); rec.Code != 401 {
		t.Fatalf("로그아웃 후 me = %d, want 401", rec.Code)
	}
}

func TestMeUnauthenticatedIs401(t *testing.T) {
	openDB(t)
	h := New(authCfg())
	if rec := do(t, h, http.MethodGet, "/api/auth/me", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("me = %d, want 401", rec.Code)
	}
}

func TestMeAcceptsExistingAdminBearer(t *testing.T) {
	openDB(t)
	h := New(authCfg())
	rec := do(t, h, http.MethodGet, "/api/auth/me", "", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("me(admin bearer) = %d", rec.Code)
	}
	if decode(t, rec)["role"] != "admin" {
		t.Fatalf("role = %v", decode(t, rec)["role"])
	}
}

func TestSessionCookieRecognizedByGate(t *testing.T) {
	openDB(t)
	seedUser(t, "boss", "admin", "admin-pw-value-1")
	h := New(authCfg())
	tok := login(t, h, "boss", "admin-pw-value-1")

	// admin 세션은 전사 조회를 태운다.
	if rec := do(t, h, http.MethodGet, "/api/usage/summary?days=365", "", sessionCookie(tok)); rec.Code != 200 {
		t.Fatalf("admin 세션 summary = %d: %s", rec.Code, rec.Body.String())
	}
	// 쿠키 자격이므로 상태변경은 403(CSRF 표면 제거) — usage_tok 규칙과 동일.
	if rec := do(t, h, http.MethodDelete, "/api/usage/identity?machine=host-a", "", sessionCookie(tok)); rec.Code != 403 {
		t.Fatalf("admin 세션 mutation = %d, want 403", rec.Code)
	}
}

func TestMemberSessionIsSelfScoped(t *testing.T) {
	ctx := tenant.With(t.Context(), "default")
	openDB(t)
	seedUser(t, "alice", "member", "alice-password-1")

	day := time.Now().UTC().Format("2006-01-02")
	for _, s := range []struct{ sid, user string }{{"alice-1", "alice"}, {"bob-1", "bob"}} {
		if err := store.SessionUpsert(ctx, store.SessionInput{
			SessionID: s.sid, Username: s.user, Machine: "m", Model: "claude-opus-4-8",
			Input: 10, Turns: 1, StartedAt: day + "T10:00:00.000Z", EndedAt: day + "T11:00:00.000Z",
		}); err != nil {
			t.Fatalf("seed %s: %v", s.sid, err)
		}
	}

	h := New(authCfg())
	tok := login(t, h, "alice", "alice-password-1")

	// ?user=bob 을 보내도 alice 것만 보인다.
	rec := do(t, h, http.MethodGet, "/api/usage/sessions?user=bob&limit=1000", "", sessionCookie(tok))
	if rec.Code != 200 {
		t.Fatalf("member 세션 sessions = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "alice-1") || strings.Contains(body, "bob-1") {
		t.Fatalf("member 세션 격리 실패: %s", body)
	}
	// 전사 뷰는 403.
	if rec := do(t, h, http.MethodGet, "/api/usage/leaderboard", "", sessionCookie(tok)); rec.Code != 403 {
		t.Fatalf("member 세션 leaderboard = %d, want 403", rec.Code)
	}
}

func TestLoginRateLimited(t *testing.T) {
	openDB(t)
	seedUser(t, "alice", "admin", "the-real-password")
	h := New(authCfg())
	// loginBurst(=10) 회까지 401(자격 실패), 그 다음부터 429.
	var got429 bool
	for i := 0; i < loginBurst+3; i++ {
		rec := do(t, h, http.MethodPost, "/api/auth/login",
			`{"username":"alice","password":"wrong-attempt"}`)
		if rec.Code == http.StatusTooManyRequests {
			got429 = true
			break
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("시도 %d: status = %d (401 또는 429 기대)", i, rec.Code)
		}
	}
	if !got429 {
		t.Fatal("브루트포스가 rate limit 에 걸리지 않았다")
	}
}
