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
	"github.com/tscorp/user-usage/internal/tenant"
)

// rctx 는 라우트가 요구하는 것만 정확히 담는다.
type rctx struct {
	path  string
	query url.Values
	// scope 를 정직하게 비춘다 — 인테이크 라우트의 자체 게이트가 의미를 갖게 하기 위해서다.
	scope string
}

// route 는 `내가 응답했다` 를 bool 로, 예상 못 한 실패를 error 로 돌려준다.
// error 를 돌려주면 호출부가 원문을 stderr 로만 보내고 클라이언트에는 400 을 낸다.
type route func(http.ResponseWriter, *http.Request, *rctx) (bool, error)

type server struct {
	cfg     config.Config
	routes  []route
	limiter *rateLimiter // 멀티테넌트 인테이크 rate limit. 단일테넌트면 nil(무제한).
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
	s := &server{cfg: cfg}
	// burst<=0 이면 리미터를 만들지 않는다(무제한) — 제로값 설정이 "모든 요청 429"로
	// 뒤집히는 footgun 을 막는다. 프로덕션 기본값은 config.Read 가 20/40 으로 채운다.
	if cfg.MultiTenant && cfg.IntakeBurst > 0 {
		s.limiter = newRateLimiter(cfg.IntakeRate, cfg.IntakeBurst)
	}
	if cfg.ReadOnly {
		// export 는 조회이므로 readOnly 에서도 유효하다(analytics 앞에 둬 admin 이 삼키기 전에 잡는다).
		s.routes = []route{s.routeOTLPExport, s.routeAnalytics, s.readOnlyAdmin}
	} else {
		s.routes = []route{s.routeIntake, s.routeOTLP, s.routeOTLPExport, s.routeAnalytics, s.routeAdmin}
	}
	return s
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

	// /api/* 는 대시보드·인테이크, /v1/* 는 OTLP 수신구. 둘 다 게이트를 탄다.
	if !hasPrefix(p, "/api/") && !hasPrefix(p, "/v1/") {
		sendError(tw, http.StatusNotFound, "not found")
		return
	}

	auth := Authenticate(r, s.cfg)
	// 멀티테넌트(SaaS): cfg 토큰으로 못 뚫렸으면 Bearer 를 org 인제스트 키로 해석한다 —
	// 성공하면 보고(intake) 스코프 + 해석된 tenant. 실패하면 종전대로 401.
	if auth == nil && s.cfg.MultiTenant {
		if bearer := bearerToken(r); bearer != "" {
			if t, _, ok, err := org.Resolve(r.Context(), bearer); err == nil && ok {
				auth = &Auth{Via: ViaHeader, Scope: ScopeIntake, Tenant: t}
			}
		}
	}
	if auth == nil {
		writeJSON(tw, http.StatusUnauthorized, errBody{Error: "unauthorized"},
			map[string]string{"WWW-Authenticate": `Bearer realm="usage"`})
		return
	}
	// 인테이크 토큰/키는 **보고 경로만** 연다(패키지 주석 ②'). 퍼스트파티(/api/usage)와 OTLP(/v1/logs).
	if auth.Scope == ScopeIntake && !(r.Method == http.MethodPost && (p == "/api/usage" || p == "/v1/logs")) {
		sendError(tw, http.StatusForbidden,
			"인테이크 토큰으로는 조회할 수 없습니다 — 열람은 USAGE_ADMIN_TOKEN 을 사용하세요")
		return
	}
	// 쿠키 자격증명으로는 상태변경을 태우지 않는다(패키지 주석 ②).
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
	// 멀티테넌트 인테이크 rate limit — 테넌트별 토큰버킷. 한 org 의 폭주가 남을 굶기지 않게.
	if auth.Scope == ScopeIntake && !s.limiter.allow(tenantID) {
		sendError(tw, http.StatusTooManyRequests, "요청이 너무 잦습니다 — 잠시 후 다시 시도하세요")
		return
	}
	r = r.WithContext(tenant.With(r.Context(), tenantID))
	c := &rctx{path: p, query: r.URL.Query(), scope: auth.Scope}

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
