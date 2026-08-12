// Package httpapi 는 HTTP 진입점이다 — 라우터·인증·정적 서빙·사용량 라우트를 한곳에 모은다.
//
// ── 이 패키지가 지는 세 가지 책임 ─────────────────────────────────────────────
// 각각을 빠뜨리면 조용한 사고가 된다:
//
//	① 인증.  없으면 사람별 사용량·비용이 무인증으로 열린다. 게이트는 config 가 부팅에서
//	   토큰을 강제하고, 여기가 요청마다 그것을 검사한다.
//	   ⚠ 세션·역할·MFA 체계를 여기에 만들지 않는다. 조회 도구에 인증 체계를 두 벌 만드는
//	     순간 그중 한 벌만 고쳐지는 날이 온다.
//
//	② CSRF 표면 제거.  쿠키 자격증명 + 상태변경은 곧 CSRF 표면이므로 **쿠키는 조회만**
//	   태운다(상태변경은 Authorization 헤더만 인정 → 403). 브라우저는 임의 헤더를 붙일 수
//	   없으니 화면은 자연히 조회 전용이 되고, double-submit 토큰을 둘 이유가 사라진다.
//
//	②' 보고 자격과 열람 자격의 분리.  인테이크의 보고자는 팀원 PC 마다 깔린 수집기다 — 즉 그
//	   토큰은 **팀원 수만큼 복제되어 각자의 디스크에 놓인다.** 그것이 곧 전원의 사용량·비용을
//	   읽는 토큰이기도 하면 사본 하나만 새도 팀 전체가 열린다. 그래서 인테이크 스코프는
//	   `POST /api/usage` **하나만** 연다(그 외 403).
//
//	③ 테넌트 스코프.  pg 백엔드는 매 쿼리에서 tenant.From(ctx) 를 RLS 로 주입한다. 감싸지
//	   않으면 'default' 로 흐르는데, remote 로 남의 DB 를 볼 때 그게 맞는다는 보장이 없다.
//	   그래서 요청 1지점에서 컨텍스트에 실는다.
package httpapi

import (
	"net/http"
	"net/url"

	"github.com/tscorp/user-usage/internal/config"
	"github.com/tscorp/user-usage/internal/org"
	"github.com/tscorp/user-usage/internal/store"
	"github.com/tscorp/user-usage/internal/tenant"
)

// rctx 는 라우트가 요구하는 것만 정확히 담는다.
type rctx struct {
	path  string
	query url.Values
	// scope 를 정직하게 비춘다 — 인테이크 라우트의 자체 게이트가 의미를 갖게 하기 위해서다.
	scope string
	// viewer 는 member 스코프에서 이 요청이 볼 수 있는 유일한 사용자다(RBAC). 빈 문자열이면
	// 제한 없음(admin). 게이트가 member 조회를 이 이름으로 강제한다.
	viewer string
	// self 는 이 요청을 낸 **사람**이다(로그인 세션 또는 개인 열람 토큰). 관리자 토큰(cfg)·
	// 인테이크에는 사람 신원이 없어 빈 문자열이고, 셀프서비스 라우트는 그때 403 을 낸다.
	self string
	// via 는 자격의 출처다(header|cookie|session). 셀프서비스 상태변경을 로그인 세션으로만
	// 좁히는 판정이 이 값을 본다.
	via string
	// keyUser 는 **인제스트 키에 묶인 사용자**다. 비어 있지 않으면 인테이크 귀속에서 무조건
	// 이긴다(동결 ①) — payload.user 도 machine 매핑도 이것을 덮지 못한다.
	keyUser string
}

// memberSelfEndpoints — member(개인) 스코프가 접근할 수 있는 조회 화이트리스트. 전부 user
// 필터를 존중하는 엔드포인트다(게이트가 user=viewer 로 강제하면 자기 것만 나온다). summary·
// leaderboard·seats·teams·dispatch 는 전사/교차 뷰라 여기에 없다 → member 는 403(deny-by-default).
var memberSelfEndpoints = map[string]bool{
	"/api/usage/sessions":     true,
	"/api/usage/series":       true,
	"/api/usage/distribution": true,
	"/api/usage/quality":      true,
}

/*
 * memberSelfKeys — 로그인한 사람이 **자기** 인제스트 키를 관리하는 경로(동결 ②).
 *
 * member 도 써야 하므로 위 조회 화이트리스트와 별도로 둔다. 여기 있는 경로는 상태변경을
 * 허용하되 **로그인 세션(usage_sess)일 때만** 태운다 — 개인 열람 토큰(Bearer)은 이름 그대로
 * 조회 자격이고, 그 토큰에 키 발급 권한까지 얹으면 조회용으로 나눠 준 토큰이 보고 자격을
 * 만들어 내는 자리가 된다.
 *
 * "남의 키는 보이지도 해지되지도 않는다"는 **라우트가** 진다(c.self 로 스코프를 건다) —
 * 게이트는 여기까지 통과시키고, 무엇이 자기 것인지는 소유자를 아는 쪽이 정한다.
 */
var memberSelfKeys = map[string]bool{
	"/api/me/keys":        true,
	"/api/me/keys/revoke": true,
}

// route 는 `내가 응답했다` 를 bool 로, 예상 못 한 실패를 error 로 돌려준다.
// error 를 돌려주면 호출부가 원문을 stderr 로만 보내고 클라이언트에는 400 을 낸다.
type route func(http.ResponseWriter, *http.Request, *rctx) (bool, error)

type server struct {
	cfg     config.Config
	routes  []route
	limiter *rateLimiter // 멀티테넌트 인테이크 rate limit. 단일테넌트면 nil(무제한).
	// loginLimiter 는 사람 로그인(/api/auth/login) 브루트포스 방어용 IP별 rate limit. 항상 켠다.
	loginLimiter *rateLimiter
}

/*
 * New 는 설정에 맞는 핸들러를 만든다.
 *
 * ⚠ **라우트 순서가 계약이다.** analytics 가 admin 보다 앞이어야 한다 — admin 이 /api/usage
 *   접두사를 통째로 소유하고 안 걸리면 404 를 직접 내므로, 뒤로 가면 관측 화면이 통째로
 *   404 가 된다.
 *
 * readOnly(=remote)에서는 인테이크를 **등록하지 않는다.** 빼는 것으로 끝나지 않는다: admin
 * 라우트 안에는 귀속 교정 쓰기(PUT/DELETE /api/usage/identity)가 있어서 그대로 두면 운영 DB 에
 * 쓴다. 그래서 조회 메서드만 통과시키고 나머지는 404 로 끊는다 — 405 가 아니라 404 인 이유는,
 * 이 모드에서 그 엔드포인트는 "지금은 막혔다"가 아니라 **존재하지 않기** 때문이다.
 */
func New(cfg config.Config) http.Handler {
	s := &server{cfg: cfg, loginLimiter: newRateLimiter(loginRefill, loginBurst)}
	// burst<=0 이면 리미터를 만들지 않는다(무제한) — 제로값 설정이 "모든 요청 429"로
	// 뒤집히는 footgun 을 막는다. 프로덕션 기본값은 config.Read 가 20/40 으로 채운다.
	//
	// 모드와 무관하게 만든다: 단일테넌트에서도 인제스트 키가 보고 경로를 열기 때문이다. 다만
	// **무엇에 거는지**는 ServeHTTP 가 정한다 — 멀티테넌트 인테이크 전부 + (모드 무관) 인제스트
	// 키 보고. cfg 인테이크 토큰의 단일테넌트 경로는 종전대로 무제한이다(계약 하네스가 시드를
	// 빠르게 쏘므로 거기에 걸면 골든이 흔들린다).
	if cfg.IntakeBurst > 0 {
		s.limiter = newRateLimiter(cfg.IntakeRate, cfg.IntakeBurst)
	}
	if cfg.ReadOnly {
		s.routes = []route{s.routeAnalytics, s.readOnlyAdmin}
	} else {
		/*
		 * routeOnboarding 은 /api/admin 을 소유하고 **안 걸리면 404 를 직접 낸다** — 그래서
		 * 같은 접두사를 나눠 쓰는 routeAdminUsers(/api/admin/users…)가 반드시 그보다 **앞**이다.
		 * 뒤로 가면 사용자 관리 API 가 통째로 404 가 된다(analytics 가 admin 앞인 것과 같은 규율).
		 *
		 * routeSelfKeys 는 /api/me 를 소유한다 — 누구와도 접두사가 겹치지 않는다.
		 */
		s.routes = []route{
			s.routeIntake, s.routeAnalytics,
			s.routeSelfKeys, s.routeAdminUsers, s.routeOnboarding,
			s.routeAdmin,
		}
	}
	return s
}

/*
 * resolveIngestKey 는 httpapi 가 인제스트 키를 **DB 로 해석하는 유일한 지점**이다(게이트의
 * 인테이크 해석 + 수집기 다운로드). 값이 org.Resolve 그대로인데도 변수로 두는 이유는 하나다:
 * 아래 접두사 게이트가 실제로 DB 를 건드리지 않는지는 "몇 번 호출됐는가"로만 증명되고,
 * 테스트가 그것을 세려면 가로챌 자리가 필요하다(ingestkey_prefix_test.go).
 */
var resolveIngestKey = org.Resolve

/*
 * ingestKeyUsername 은 **해석에 성공한** 키에 묶인 사용자를 읽는다(귀속 우선순위 ①의 재료).
 *
 * 왜 resolveIngestKey 와 합치지 않았나: 위 훅은 "접두사 없는 Bearer 가 DB 를 몇 번 때렸는가"를
 * 세는 자리라 시그니처가 계약이다(ingestkey_prefix_test.go). 그리고 이 조회는 **인증 성공 뒤**
 * 에만 돈다 — 무인증 브루트포스는 여기까지 못 오므로 그 증폭 표면은 늘지 않는다.
 */
var ingestKeyUsername = org.KeyUsername

/*
 * ingestKeyAuth 는 해석된 인제스트 키를 이 배포의 자격으로 접는다. 두 모드가 갈리는 **유일한**
 * 지점이라 여기 한 곳에 둔다:
 *
 *   · 멀티테넌트 — 키가 가리키는 tenant 를 그대로 싣는다. RLS 가 그 값으로 격리를 강제한다.
 *   · 단일테넌트 — tenant 를 비워 둔다(게이트가 cfg.Tenant 를 쓴다). 키를 따라가면 sqlite 처럼
 *     RLS 가 없는 배포에서 org 별 tenant 가 조용히 생기는데, 저장 계층이 그것을 보지 않으므로
 *     보고는 남의 tenant 로 들어가고 대시보드에서는 사라진 것처럼 보인다.
 */
// keyUser 는 그 키에 묶인 사용자다(없으면 빈 문자열 — 레거시·org 공용 키). 모드와 무관하게
// 싣는다: 귀속은 테넌트 격리와 다른 축이고, 단일테넌트에서도 "누가 보고했는가"는 같은 문제다.
func (s *server) ingestKeyAuth(keyTenant, keyUser string) *Auth {
	a := &Auth{Via: ViaHeader, Scope: ScopeIntake, IngestKey: true, Username: keyUser}
	if s.cfg.MultiTenant {
		a.Tenant = keyTenant
	}
	return a
}

func (s *server) readOnlyAdmin(w http.ResponseWriter, r *http.Request, c *rctx) (bool, error) {
	if !hasPrefix(c.path, "/api/usage") {
		return false, nil
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		sendError(w, http.StatusNotFound, "not found")
		return true, nil
	}
	return s.routeAdmin(w, r, c)
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tw := &trackingWriter{ResponseWriter: w}
	/*
	 * 핸들러 밖으로 새는 거부가 프로세스를 죽이지 않게 하는 마지막 안전망.
	 * 원문은 stderr 로만 간다 — 대개 DB 드라이버 에러라 테이블·컬럼명이 문장에 실린다.
	 */
	defer func() {
		if rec := recover(); rec != nil {
			if tw.wrote {
				return // 이미 응답이 나갔다 — 여기서 또 쓰면 프로토콜이 깨진다
			}
			failRequest(tw, rec, r.URL.EscapedPath())
		}
	}()

	/*
	 * EscapedPath 를 쓴다(Path 아님). Node 의 URL.pathname 은 퍼센트 인코딩을 **풀지 않고**
	 * 라우팅에 쓰므로, 디코딩된 경로로 맞추면 `%2E%2E` 류가 정적 화이트리스트나 세션 id
	 * 정규식을 다르게 통과한다. 경계 판정은 같은 문자열 위에서 돌아야 한다.
	 */
	p := r.URL.EscapedPath()

	// 무인증·무DB — 기동 확인용. 데이터가 없으므로 게이트 **위**에 둔다.
	if p == "/healthz" {
		writeJSON(tw, http.StatusOK, map[string]string{"status": "ok"}, nil)
		return
	}

	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		if abs, ok := resolveStatic(p); ok {
			// 셸·뷰는 무인증이다. 데이터는 전부 /api/* 로 오고 그쪽에 게이트가 있다 —
			// 화면 껍데기를 가리면 "토큰을 어디에 넣어야 하는가"를 안내할 자리가 사라진다.
			serveFile(tw, r, abs)
			return
		}
	}

	// /install.sh — 무인증 부트스트랩(게이트 앞). 키가 인증을 대신한다. /api/ 가 아니라
	// 아래 404 게이트에 걸리기 전에 잡는다.
	if p == "/install.sh" {
		serveInstallScript(tw, r)
		return
	}
	// /api/agent/collector — 인제스트 키 자체 검증(게이트 앞). 게이트의 인테이크 규칙이 이 GET
	// 조회를 막으므로(그 스코프는 POST 보고만) 여기서 키를 직접 검증한다.
	if p == "/api/agent/collector" {
		s.serveCollector(tw, r)
		return
	}

	// 데이터 표면은 /api/* 하나뿐이다(대시보드·인테이크) — 전부 아래 게이트를 탄다.
	if !hasPrefix(p, "/api/") {
		sendError(tw, http.StatusNotFound, "not found")
		return
	}

	// 사람 로그인(ID/PW → 세션 쿠키)은 게이트 **앞**이다 — 로그인/로그아웃은 무자격으로 닿아야
	// 하고 /me 는 스스로 401 을 낸다. 발급된 세션은 아래 게이트가 usage_sess 쿠키로 다시 인식한다.
	if hasPrefix(p, "/api/auth/") {
		s.serveAuth(tw, r, p)
		return
	}

	auth := Authenticate(r, s.cfg)
	// 사람 로그인 세션(usage_sess 쿠키) — cfg 토큰/쿠키로 못 뚫렸으면 세션으로 해석한다.
	// 성공하면 세션의 role 이 스코프가 된다(admin=전사 열람, member=자기 데이터만).
	if auth == nil {
		auth = s.sessionAuth(r)
	}
	// cfg 토큰으로 못 뚫렸으면 Bearer 를 org 인제스트 키로 해석한다 — 성공하면 보고(intake)
	// 스코프. 실패하면 종전대로 401.
	//
	// **모드와 무관하게** 해석한다. 온보딩 UI(POST /api/admin/keys)는 모드를 보지 않고 키를
	// 발급하고, install.sh 는 그 키 하나로 수집기 다운로드와 백필 업로드를 모두 한다 —
	// 단일테넌트에서 여기를 닫아 두면 다운로드만 되고 업로드가 401 인 반쪽 설치가 된다.
	//
	// ⚠ 단 **접두사(org.KeyPrefix)로 시작하는 Bearer 만** 해석에 태운다. 이 해석은 sha256 +
	//   DB PK 조회이고, 아래 rate limit 은 **인증에 성공한 뒤**에 걸려 이 경로를 막지 못한다 —
	//   접두사를 안 보면 무인증 브루트포스가 시도마다 DB 를 때리는 증폭 표면이 된다. 접두사 없는
	//   Bearer(오타·남의 토큰·아래 member 열람 토큰)는 여기서 DB 를 아예 건드리지 않는다.
	//
	//   이 판정은 **"발급된 모든 키가 KeyPrefix 를 갖는다"는 불변식**에만 기대고 있다. 접두사
	//   없이 발급하는 경로가 하나라도 생기면 유효한 키가 조용히 401 이 되고, 증상은 여기가 아니라
	//   발급부에서 나온다 — org.TestIssuedKeysAlwaysCarryPrefix 가 그 불변식을 못박는 유일한
	//   안전장치다. 접두사 문자열은 org 것을 그대로 쓴다(복제하면 한쪽만 바뀌는 날이 온다).
	if auth == nil {
		if bearer := bearerToken(r); hasPrefix(bearer, org.KeyPrefix) {
			t, _, ok, err := resolveIngestKey(r.Context(), bearer)
			if err != nil {
				// 조회 실패·미초기화. 응답은 아래에서 401 로 접고(서버 상태 미노출) 원인은
				// stderr 로만 남긴다. 미지·해지 키는 err 이 아니라 ok=false 라 여기 오지 않는다.
				logf("인제스트 키 해석 실패: %v", err)
			}
			if err == nil && ok {
				/*
				 * 키에 묶인 사용자를 함께 싣는다(귀속 우선순위 ①). 조회가 실패하면 **묶이지
				 * 않은 것으로 접는다** — 여기서 401 을 내면 DB 딸꾹질 하나로 전 팀원의 보고가
				 * 사라지고, 종전(키에 사용자가 없던 시절) 동작으로 떨어지는 쪽이 안전하다.
				 * 사실이 조용히 사라지지는 않게 stderr 에 남긴다.
				 */
				keyUser, uerr := ingestKeyUsername(r.Context(), bearer)
				if uerr != nil {
					logf("인제스트 키 사용자 조회 실패(귀속은 종전 경로로 접는다): %v", uerr)
					keyUser = ""
				}
				auth = s.ingestKeyAuth(t, keyUser)
			}
		}
	}
	// 개인 열람 토큰(RBAC): admin/intake/인제스트키로 못 뚫렸으면 Bearer 를 member 토큰으로
	// 해석한다. 성공하면 member 스코프 + 그 사용자. (member 토큰은 조회 자격이라 헤더로만 받는다.)
	if auth == nil {
		if bearer := bearerToken(r); bearer != "" {
			if u, ok, err := store.ResolveMemberToken(r.Context(), bearer); err == nil && ok {
				auth = &Auth{Via: ViaHeader, Scope: ScopeMember, Username: u}
			}
		}
	}
	if auth == nil {
		writeJSON(tw, http.StatusUnauthorized, errBody{Error: "unauthorized"},
			map[string]string{"WWW-Authenticate": `Bearer realm="usage"`})
		return
	}
	// member(개인) 스코프 정책: 조회 전용 + self 화이트리스트 + user=본인 강제(deny-by-default).
	if auth.Scope == ScopeMember {
		mutating := r.Method != http.MethodGet && r.Method != http.MethodHead
		switch {
		case memberSelfKeys[p]:
			/*
			 * 셀프서비스 키 관리(동결 ②) — member 도 자기 키를 발급·해지할 수 있어야 한다.
			 * 다만 **상태변경은 로그인 세션(usage_sess)만** 태운다. 개인 열람 토큰은 조회
			 * 자격이고(패키지 주석 ②'), 그것으로 키를 발급할 수 있으면 나눠 준 조회 토큰이
			 * 보고 자격을 찍어 내는 자리가 된다.
			 */
			if mutating && auth.Via != ViaSession {
				sendError(tw, http.StatusForbidden,
					"개인 열람 토큰으로는 상태변경을 할 수 없습니다 — 로그인 후 다시 시도하세요")
				return
			}
		case mutating:
			sendError(tw, http.StatusForbidden, "개인 열람 토큰으로는 상태변경을 할 수 없습니다")
			return
		case !memberSelfEndpoints[p]:
			sendError(tw, http.StatusForbidden,
				"개인 열람 토큰은 자기 데이터만 볼 수 있습니다 — 전사·팀 화면은 관리자 토큰이 필요합니다")
			return
		}
	}
	// 인테이크 토큰/키는 **보고 경로만** 연다(패키지 주석 ②') — 퍼스트파티 `POST /api/usage` 하나뿐이다.
	if auth.Scope == ScopeIntake && !(r.Method == http.MethodPost && p == "/api/usage") {
		sendError(tw, http.StatusForbidden,
			"인테이크 토큰으로는 조회할 수 없습니다 — 열람은 USAGE_ADMIN_TOKEN 을 사용하세요")
		return
	}
	// 레거시 usage_tok 쿠키(ViaCookie)로는 상태변경을 태우지 않는다(패키지 주석 ②) — SameSite 보장이
	// 없어 CSRF 표면이 되므로 조회만 연다. 사람 로그인 세션(ViaSession, usage_sess)은 항상
	// SameSite=Strict+HttpOnly 로 발급되므로(authsession.go) 브라우저가 크로스사이트 요청에 안 실어
	// 보낸다 — CSRF-safe 라 상태변경을 허용한다(보안검토 L-4: SameSite=Strict 로 실용적 충분).
	if r.Method != http.MethodGet && r.Method != http.MethodHead && auth.Via == ViaCookie {
		sendError(tw, http.StatusForbidden,
			"쿠키 인증으로는 상태변경을 할 수 없습니다 — Authorization: Bearer 를 사용하세요")
		return
	}

	// ③ 테넌트 스코프 — 요청 1지점에서 감싼다. pg 의 모든 쿼리가 이 값을 RLS 로 받는다.
	// 멀티테넌트 모드에서 인제스트 키가 해석한 tenant 가 있으면 그것을, 없으면 cfg.Tenant.
	tenantID := s.cfg.Tenant
	if auth.Tenant != "" {
		tenantID = auth.Tenant
	}
	// 인테이크 rate limit — 테넌트별 토큰버킷. 멀티테넌트에서는 한 org 의 폭주가 남을 굶기지
	// 않게, 단일테넌트에서는 복제된 인제스트 키 사본 하나가 무한정 쓰지 못하게 건다.
	// (단일테넌트의 cfg 인테이크 토큰 경로는 제외 — 종전 무제한 그대로다.)
	if auth.Scope == ScopeIntake && (s.cfg.MultiTenant || auth.IngestKey) && !s.limiter.allow(tenantID) {
		sendError(tw, http.StatusTooManyRequests, "요청이 너무 잦습니다 — 잠시 후 다시 시도하세요")
		return
	}
	r = r.WithContext(tenant.With(r.Context(), tenantID))
	c := &rctx{path: p, query: r.URL.Query(), scope: auth.Scope, via: auth.Via}
	// 사람 신원. 인테이크는 사람이 아니므로 여기 싣지 않는다 — 그쪽의 Username 은 키에 묶인
	// 귀속 대상이지 "요청한 사람"이 아니고, 둘을 같은 칸에 두면 셀프서비스 라우트가
	// 인제스트 키를 로그인으로 착각한다.
	if auth.Scope != ScopeIntake {
		c.self = auth.Username
	} else {
		c.keyUser = auth.Username
	}
	// member(RBAC): user 필터를 **본인 이름으로 덮어쓴다.** 클라이언트가 ?user=남 을 보내도
	// 자기 것만 나온다 — 화이트리스트 엔드포인트가 전부 user 필터를 존중하므로 이 한 줄이
	// "자기 데이터만" 을 강제한다(위 게이트가 교차 뷰는 이미 403 으로 막았다).
	if auth.Scope == ScopeMember {
		c.viewer = auth.Username
		c.query.Set("user", auth.Username)
	}

	for _, rt := range s.routes {
		handled, err := rt(tw, r, c)
		if err != nil {
			if !tw.wrote {
				failRequest(tw, err, p)
			}
			return
		}
		if handled {
			return
		}
	}
	sendError(tw, http.StatusNotFound, "not found")
}

// trackingWriter 는 "응답이 이미 나갔는가"를 안다. Node 의 res.headersSent 와 같은 역할이고,
// 그것 없이 오류 경로에서 두 번 쓰면 클라이언트가 깨진 응답을 받는다.
type trackingWriter struct {
	http.ResponseWriter
	wrote bool
}

func (t *trackingWriter) WriteHeader(code int) {
	if t.wrote {
		return
	}
	t.wrote = true
	t.ResponseWriter.WriteHeader(code)
}

func (t *trackingWriter) Write(b []byte) (int, error) {
	t.wrote = true
	return t.ResponseWriter.Write(b)
}
