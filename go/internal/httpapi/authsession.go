package httpapi

import (
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/tscorp/user-usage/internal/store"
	"github.com/tscorp/user-usage/internal/tenant"
)

/*
 * 사람 로그인(ID/PW → 세션 쿠키). 신규 /api/auth/* 만 추가하고 기존 토큰/키/멤버토큰 경로는
 * 건드리지 않는다. 이 파일이 지는 것:
 *
 *   POST /api/auth/login   자격 검증 → 세션 발급 → Set-Cookie(usage_sess)
 *   POST /api/auth/logout  세션 무효화 + 쿠키 만료(Max-Age=0)
 *   GET  /api/auth/me       현재 신원(세션 쿠키 또는 기존 Bearer/쿠키)
 *
 * 이 세 라우트는 게이트 **앞**에서 처리된다(server.go) — 로그인/로그아웃은 무자격으로 닿아야
 * 하고 /me 는 스스로 401 을 낸다. 발급된 세션은 게이트가 usage_sess 쿠키로 다시 해석해
 * Auth{Via:session, Scope:role} 로 인식한다(sessionAuth).
 *
 * CSRF: 세션 쿠키는 **HttpOnly + SameSite=Strict 로만** 발급된다(setSessionCookie). 브라우저가
 * 크로스사이트 요청에 싣지 않으므로 CSRF-safe 이고, 그래서 **상태변경을 허용한다** — 화면이
 * 사용자 관리·키 발급을 세션만으로 할 수 있는 근거다(게이트 판정은 server.go 가 소유한다).
 *
 * ⚠ 레거시 usage_tok 쿠키(ViaCookie)는 다르다. 그쪽은 게이트 화면이 document.cookie 로 직접
 *   쓰던 값이라 SameSite 보장이 없어 **조회만** 태운다. 둘을 같은 규칙으로 묶지 말 것.
 */

const (
	// 로그인 rate limit(브루트포스 방어) — IP 당 토큰버킷. 즉시 loginBurst 회, 이후 초당
	// loginRefill 회 리필. 기존 ratelimit.go 의 tokenBucket 을 그대로 재사용한다.
	loginRefill = 0.5 // 초당 리필(2초에 1회)
	loginBurst  = 10  // 초기 버스트 상한
)

// sessionCookieName — 사람 로그인 세션 쿠키. 기존 usage_tok(관리자 토큰 쿠키)과 별개 이름이다.
const sessionCookieName = "usage_sess"

// loginReqBody 는 로그인 요청 본문이다(신뢰 경계 — 반드시 검증 후 안으로 흘린다).
type loginReqBody struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// authUserInfo 는 로그인/me 응답의 사용자 정보다(동결 계약 shape).
type authUserInfo struct {
	Username string `json:"username"`
	Role     string `json:"role"`
	Tenant   string `json:"tenant"`
}

// loginOKBody·loginFailBody — 로그인 응답 shape(동결 계약).
type loginOKBody struct {
	Ok   bool         `json:"ok"`
	User authUserInfo `json:"user"`
}
type loginFailBody struct {
	Ok    bool   `json:"ok"`
	Error string `json:"error"`
}

// loginFailMsg — 실패 문구. 사용자 유무를 구분해 노출하지 않는다(계약 고정 문자열).
const loginFailMsg = "아이디 또는 비밀번호가 올바르지 않습니다"

// serveAuth 는 /api/auth/* 를 라우팅한다(게이트 앞에서 호출됨).
func (s *server) serveAuth(w http.ResponseWriter, r *http.Request, p string) {
	switch p {
	case "/api/auth/login":
		if r.Method != http.MethodPost {
			sendError(w, http.StatusMethodNotAllowed, "POST 만 허용됩니다")
			return
		}
		s.handleLogin(w, r)
	case "/api/auth/logout":
		if r.Method != http.MethodPost {
			sendError(w, http.StatusMethodNotAllowed, "POST 만 허용됩니다")
			return
		}
		s.handleLogout(w, r)
	case "/api/auth/me":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			sendError(w, http.StatusMethodNotAllowed, "GET 만 허용됩니다")
			return
		}
		s.handleMe(w, r)
	default:
		sendError(w, http.StatusNotFound, "not found")
	}
}

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	// 브루트포스 방어 — IP 당 rate limit. 리미터가 없으면(구성 실패) 통과시키지 않고 열어 둔다
	// (allow 가 nil 리시버에서 true 를 돌려주므로 안전하다).
	if !s.loginLimiter.allow(clientIP(r, s.cfg.TrustedProxyCount)) {
		sendError(w, http.StatusTooManyRequests, "로그인 시도가 너무 잦습니다 — 잠시 후 다시 시도하세요")
		return
	}

	var body loginReqBody
	// 본문 파싱 실패는 자격 실패와 같게 다룬다(형식으로 사용자 유무·존재를 유추하지 못하게).
	if err := decodeJSONBody(r, &body); err != nil {
		sendLoginFail(w)
		return
	}
	username := strings.TrimSpace(body.Username)

	sctx := tenant.With(r.Context(), s.cfg.Tenant)
	// 로그인 시 만료 세션을 lazy 정리(주기 워커 없이도 표가 무한정 커지지 않게). best-effort.
	_ = store.PurgeExpiredSessions(sctx)

	user, _, err := store.GetUser(sctx, username)
	if err != nil {
		// DB 오류의 원문을 클라이언트로 보내지 않는다 — stderr 로만. 응답은 일반 실패로 접는다.
		logf("로그인 사용자 조회 실패: %v", err)
		sendLoginFail(w)
		return
	}
	// user 가 없으면 PasswordHash 가 "" 이고 VerifyPassword 가 더미 해시로 타이밍을 맞춘 뒤 false 다
	// — 사용자 유무가 응답 시간으로 새지 않는다.
	if !store.VerifyPassword(user.PasswordHash, body.Password) {
		sendLoginFail(w)
		return
	}

	exp := time.Now().Add(s.cfg.SessionTTL)
	tok, err := store.CreateSession(sctx, user.Username, user.Role, exp)
	if err != nil {
		logf("세션 발급 실패: %v", err)
		sendError(w, http.StatusInternalServerError, "세션을 발급하지 못했습니다")
		return
	}
	setSessionCookie(w, r, tok, s.cfg.SessionTTL)
	writeJSON(w, http.StatusOK, loginOKBody{
		Ok:   true,
		User: authUserInfo{Username: user.Username, Role: user.Role, Tenant: s.cfg.Tenant},
	}, nil)
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// 세션 무효화(best-effort — 쿠키가 없거나 이미 만료여도 204).
	if raw, ok := parseCookies(r)[sessionCookieName]; ok && raw != "" {
		sctx := tenant.With(r.Context(), s.cfg.Tenant)
		if err := store.DeleteSession(sctx, raw); err != nil {
			logf("세션 삭제 실패(로그아웃): %v", err)
		}
	}
	clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleMe(w http.ResponseWriter, r *http.Request) {
	// ① 사람 로그인 세션(usage_sess).
	if a := s.sessionAuth(r); a != nil {
		writeJSON(w, http.StatusOK, authUserInfo{Username: a.Username, Role: a.Scope, Tenant: a.Tenant}, nil)
		return
	}
	// ② 기존 Bearer/쿠키 자격(관리자 토큰). intake 는 사람이 아니므로 /me 로는 신원을 주지 않는다.
	if a := Authenticate(r, s.cfg); a != nil && a.Scope == ScopeAdmin {
		writeJSON(w, http.StatusOK, authUserInfo{Username: "", Role: "admin", Tenant: s.cfg.Tenant}, nil)
		return
	}
	// ③ 개인 열람 토큰(member Bearer). 게이트와 같은 해석(r.Context()).
	if bearer := bearerToken(r); bearer != "" {
		if u, ok, err := store.ResolveMemberToken(r.Context(), bearer); err == nil && ok {
			writeJSON(w, http.StatusOK, authUserInfo{Username: u, Role: "member", Tenant: s.cfg.Tenant}, nil)
			return
		}
	}
	writeJSON(w, http.StatusUnauthorized, errBody{Error: "unauthorized"},
		map[string]string{"WWW-Authenticate": `Bearer realm="usage"`})
}

// sessionAuth 는 usage_sess 쿠키를 Auth 로 해석한다(게이트·/me 공용). 없거나 만료면 nil.
//
// tenant 는 cfg.Tenant 로 고정한다 — 로그인 본문에 tenant 가 없으므로 이 배포의 단일 테넌트
// 안에서 세션을 발급/해석한다. pg 는 이 값으로 RLS 를 걸어 크로스테넌트 세션이 새지 않는다.
func (s *server) sessionAuth(r *http.Request) *Auth {
	raw, ok := parseCookies(r)[sessionCookieName]
	if !ok || raw == "" {
		return nil
	}
	sctx := tenant.With(r.Context(), s.cfg.Tenant)
	t, u, role, ok, err := store.ResolveSession(sctx, raw)
	if err != nil || !ok {
		return nil
	}
	// role 은 admin|member 만 인정한다 — 알 수 없는 값이 admin 경로로 흘러 들어가는 자리를 막는다.
	if role != ScopeAdmin && role != ScopeMember {
		return nil
	}
	return &Auth{Via: ViaSession, Scope: role, Tenant: t, Username: u}
}

// setSessionCookie 는 세션 쿠키를 발급한다. https(또는 X-Forwarded-Proto=https)면 Secure 를 붙인다.
func setSessionCookie(w http.ResponseWriter, r *http.Request, token string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   isHTTPS(r),
		MaxAge:   int(ttl.Seconds()),
	})
}

// clearSessionCookie 는 세션 쿠키를 만료시킨다(Max-Age=0). MaxAge<0 이 net/http 에서 "Max-Age=0".
func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   isHTTPS(r),
		MaxAge:   -1,
	})
}

func sendLoginFail(w http.ResponseWriter) {
	writeJSON(w, http.StatusUnauthorized, loginFailBody{Ok: false, Error: loginFailMsg}, nil)
}

// isHTTPS 는 요청이 https 로 들어왔는지 본다(직결 TLS 또는 리버스 프록시의 X-Forwarded-Proto).
func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

/*
 * clientIP 는 rate limit 키로 쓸 실클라이언트 IP 다.
 *
 * trustedProxyCount==0(기본): RemoteAddr 만 쓴다. XFF 는 완전히 무시한다 — 스푸핑 가능한
 * 헤더를 신뢰하지 않는 현행 동작 그대로다(루프백/터널 뒤 직결 배포).
 *
 * trustedProxyCount==N>0: 서버 앞에 신뢰 프록시가 N홉 있다는 선언이다(예: ALB 단독=1).
 * X-Forwarded-For 를 콤마 분리·trim 한 리스트에서 실클라이언트 = parts[len-N] 을 뽑는다.
 * 이게 없으면 ALB 뒤에서 모든 요청이 ALB IP 라는 **단일 버킷**으로 붕괴해 로그인 rate-limit 이
 * 무력화된다(무인증 로그인 DoS). 단 XFF 는 여전히 위조 가능하므로 아래 가드로 fail-safe 한다:
 *
 *   · 헤더 없음/빈값                → RemoteAddr 폴백
 *   · len(parts) < N(위조·오설정)   → RemoteAddr 폴백(공유버킷보다 안전)
 *   · 뽑은 값이 유효 IP 가 아님      → RemoteAddr 폴백
 *
 * 폴백은 절대 XFF 를 신뢰하지 않는다 — 최악의 경우에도 현행(count=0) 수준의 안전성을 유지한다.
 */
func clientIP(r *http.Request, trustedProxyCount int) string {
	remote := remoteHost(r)
	if trustedProxyCount <= 0 {
		return remote
	}
	xff := r.Header.Get("X-Forwarded-For")
	if strings.TrimSpace(xff) == "" {
		return remote
	}
	parts := strings.Split(xff, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	if len(parts) < trustedProxyCount {
		return remote
	}
	candidate := parts[len(parts)-trustedProxyCount]
	if net.ParseIP(candidate) == nil {
		return remote
	}
	return candidate
}

// remoteHost 는 RemoteAddr 의 host(포트 제거)다. 파싱 실패면 원문 그대로.
func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
