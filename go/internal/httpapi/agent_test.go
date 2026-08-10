package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/tscorp/user-usage/internal/org"
	"github.com/tscorp/user-usage/internal/tenant"
)

// install.sh — 무인증 서빙. Content-Type 과 본문(임베드된 스크립트 그대로)을 잡는다.
func TestServeInstallScript(t *testing.T) {
	h := New(testCfg(false))

	rec := do(t, h, http.MethodGet, "/install.sh", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("install.sh: code=%d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/x-shellscript" {
		t.Fatalf("Content-Type=%q (기대 text/x-shellscript)", ct)
	}
	if rec.Body.Len() == 0 || rec.Body.String() != string(installScript) {
		t.Fatalf("install.sh 본문이 임베드 스크립트와 다르다(len=%d)", rec.Body.Len())
	}
	// 상태변경 메서드는 405.
	if rec := do(t, h, http.MethodPost, "/install.sh", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("install.sh POST: code=%d (기대 405)", rec.Code)
	}
}

// collector — 인제스트 키 필수. 키 없음/틀림 401, 미지원 플랫폼 404, 지원 플랫폼은 200 또는 503
// (임베드 바이너리 유무는 빌드 환경마다 다르다 — 여기서는 인증·플랫폼 판정을 통과했음을 본다).
func TestServeCollector(t *testing.T) {
	ctx := tenant.With(context.Background(), "default")
	d := openDB(t)
	if err := org.Init(ctx, d); err != nil {
		t.Fatalf("org.Init: %v", err)
	}
	issued, err := org.IssueForTenant(ctx, "default", "default")
	if err != nil {
		t.Fatalf("IssueForTenant: %v", err)
	}
	h := New(testCfg(false))
	withKey := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+issued.Plain) }

	// 키 없음 → 401.
	if rec := do(t, h, http.MethodGet, "/api/agent/collector?os=linux&arch=amd64", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("키 없음: code=%d (기대 401)", rec.Code)
	}
	// 틀린 키 → 401.
	badKey := func(r *http.Request) { r.Header.Set("Authorization", "Bearer uu_ing_deadbeef") }
	if rec := do(t, h, http.MethodGet, "/api/agent/collector?os=linux&arch=amd64", "", badKey); rec.Code != http.StatusUnauthorized {
		t.Fatalf("틀린 키: code=%d (기대 401)", rec.Code)
	}
	// 유효 키 + 미지원 플랫폼 → 404.
	if rec := do(t, h, http.MethodGet, "/api/agent/collector?os=plan9&arch=foo", "", withKey); rec.Code != http.StatusNotFound {
		t.Fatalf("미지원 플랫폼: code=%d (기대 404)", rec.Code)
	}
	// 유효 키 + os/arch 누락 → 404.
	if rec := do(t, h, http.MethodGet, "/api/agent/collector", "", withKey); rec.Code != http.StatusNotFound {
		t.Fatalf("플랫폼 누락: code=%d (기대 404)", rec.Code)
	}
	// 유효 키 + 지원 플랫폼 → 200(바이너리 임베드됨) 또는 503(빌드 전). 401/404 면 배선 오류다.
	rec := do(t, h, http.MethodGet, "/api/agent/collector?os=linux&arch=amd64", "", withKey)
	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("지원 플랫폼(헤더 키): code=%d (기대 200 또는 503)", rec.Code)
	}
	if rec.Code == http.StatusOK {
		if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
			t.Fatalf("바이너리 Content-Type=%q (기대 application/octet-stream)", ct)
		}
	}
	// ?key= 쿼리 파라미터 인증 경로도 같은 판정을 통과한다.
	rec = do(t, h, http.MethodGet, "/api/agent/collector?os=linux&arch=amd64&key="+issued.Plain, "")
	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("지원 플랫폼(?key=): code=%d (기대 200 또는 503)", rec.Code)
	}
}

/*
 * 수집기 다운로드는 **보고 자격이면 충분하다** — 바이너리에는 테넌트 데이터가 없고, 이 엔드포인트의
 * 목적은 "오픈 바이너리 CDN 이 되지 않는 것" 하나다. 이미 보고할 수 있는 자격(cfg 인테이크 토큰)과
 * 관리 자격(admin 토큰)은 인제스트 키보다 강하므로 여기서 거절할 이유가 없다. 거절하면 종전
 * 단일 토큰 배포는 원커맨드 설치를 ②에서 401 로 못 넘긴다.
 *
 * 단 cfg 토큰은 **헤더로만** 받는다 — ?key= 는 액세스 로그·리퍼러에 남는 자리라 관리 토큰을
 * 태울 곳이 아니다.
 */
func TestServeCollectorAcceptsAdminAndIntakeTokens(t *testing.T) {
	ctx := tenant.With(context.Background(), "default")
	d := openDB(t)
	if err := org.Init(ctx, d); err != nil {
		t.Fatalf("org.Init: %v", err)
	}
	h := New(testCfg(false))
	const target = "/api/agent/collector?os=linux&arch=amd64"

	// admin 토큰(헤더) → 인증 통과(200 또는 바이너리 미빌드 503).
	if rec := do(t, h, http.MethodGet, target, "", withAdmin); rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("admin 토큰 다운로드: code=%d (기대 200 또는 503)", rec.Code)
	}
	// 인테이크 토큰(헤더) → 인증 통과.
	if rec := do(t, h, http.MethodGet, target, "", withIntake); rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("인테이크 토큰 다운로드: code=%d (기대 200 또는 503)", rec.Code)
	}
	// cfg 토큰을 ?key= 로 → 401(쿼리에는 인제스트 키만).
	if rec := do(t, h, http.MethodGet, target+"&key="+testAdmin, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("?key=admin토큰: code=%d (기대 401)", rec.Code)
	}
	// 틀린 Bearer 는 여전히 401(org 키로도 해석 안 된다).
	badTok := func(r *http.Request) { r.Header.Set("Authorization", "Bearer nope-not-a-token") }
	if rec := do(t, h, http.MethodGet, target, "", badTok); rec.Code != http.StatusUnauthorized {
		t.Fatalf("틀린 Bearer: code=%d (기대 401)", rec.Code)
	}
}
