package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/tscorp/user-usage/internal/org"
	"github.com/tscorp/user-usage/internal/tenant"
)

// 관리자 인제스트 키 API — 발급/목록/해지의 shape·스코프·평문 비노출을 잡는다. 골든은 이
// 엔드포인트를 커버하지 않으므로(신규) 배선은 여기서만 검증된다.
func initOrg(t *testing.T) {
	t.Helper()
	ctx := tenant.With(context.Background(), "default")
	d := openDB(t) // store·identity 전역 핸들 세팅
	if err := org.Init(ctx, d); err != nil {
		t.Fatalf("org.Init: %v", err)
	}
}

func TestAdminKeysIssueListRevoke(t *testing.T) {
	initOrg(t)
	h := New(testCfg(false))

	// ① 발급 — Bearer 관리자 토큰(상태변경이라 쿠키·세션은 게이트가 막는다).
	rec := do(t, h, http.MethodPost, "/api/admin/keys", "", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("발급: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var issued keyIssueResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil {
		t.Fatalf("발급 응답 파싱: %v", err)
	}
	if !strings.HasPrefix(issued.Key, org.KeyPrefix) {
		t.Fatalf("발급 key 접두사: %q", issued.Key)
	}
	if issued.ID == "" || issued.CreatedAt == "" {
		t.Fatalf("발급 id/createdAt 비어있음: %+v", issued)
	}

	// ② 목록 — 평문 절대 미포함, 마스크만. 방금 발급한 id 가 보인다.
	rec = do(t, h, http.MethodGet, "/api/admin/keys", "", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("목록: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); strings.Contains(body, issued.Key) {
		t.Fatalf("목록 응답에 평문 키가 들어있다: %s", body)
	}
	var list keyListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("목록 응답 파싱: %v", err)
	}
	if len(list.Keys) != 1 {
		t.Fatalf("목록 길이=%d (기대 1)", len(list.Keys))
	}
	k := list.Keys[0]
	if k.ID != issued.ID {
		t.Fatalf("목록 id=%q, 발급 id=%q", k.ID, issued.ID)
	}
	if !strings.HasPrefix(k.Masked, org.KeyPrefix+"…") {
		t.Fatalf("마스크 형식: %q", k.Masked)
	}
	if k.RevokedAt != nil {
		t.Fatalf("미해지 키인데 revokedAt=%v (기대 null)", *k.RevokedAt)
	}

	// ③ 해지 — 204, 이후 목록에서 revokedAt 채워짐.
	rec = do(t, h, http.MethodPost, "/api/admin/keys/revoke", `{"id":"`+issued.ID+`"}`, withAdmin)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("해지: code=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/api/admin/keys", "", withAdmin)
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("해지 후 목록 파싱: %v", err)
	}
	if len(list.Keys) != 1 || list.Keys[0].RevokedAt == nil {
		t.Fatalf("해지 후 revokedAt 미채움: %+v", list.Keys)
	}
}

func TestAdminKeysScopeAndCookie(t *testing.T) {
	initOrg(t)
	h := New(testCfg(false))

	// 인테이크 토큰으로 발급 시도 → 게이트가 403(보고 스코프는 관리 API 를 못 연다).
	if rec := do(t, h, http.MethodPost, "/api/admin/keys", "", withIntake); rec.Code != http.StatusForbidden {
		t.Fatalf("인테이크 발급: code=%d (기대 403)", rec.Code)
	}
	// 쿠키(관리자) 로 발급 시도 → 게이트가 403(쿠키 자격은 상태변경 불가 — CSRF 표면 제거).
	if rec := do(t, h, http.MethodPost, "/api/admin/keys", "", withCookie); rec.Code != http.StatusForbidden {
		t.Fatalf("쿠키 발급: code=%d (기대 403)", rec.Code)
	}
	// 쿠키(관리자) 로 목록 조회 → 200(조회는 쿠키 자격이 태운다).
	if rec := do(t, h, http.MethodGet, "/api/admin/keys", "", withCookie); rec.Code != http.StatusOK {
		t.Fatalf("쿠키 목록: code=%d (기대 200)", rec.Code)
	}
	// 무자격 → 401.
	if rec := do(t, h, http.MethodGet, "/api/admin/keys", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("무자격 목록: code=%d (기대 401)", rec.Code)
	}
}

func TestSessionCookieCanMutateAdminKeys(t *testing.T) {
	initOrg(t)
	seedUser(t, "boss", "admin", "admin-pw-value-1")
	h := New(authCfg())
	tok := login(t, h, "boss", "admin-pw-value-1")

	// ① 세션 쿠키(admin)로 발급 → 200. usage_sess 는 항상 SameSite=Strict+HttpOnly 라 CSRF-safe 이므로
	//    상태변경을 태운다(레거시 usage_tok 쿠키와 다르다).
	rec := do(t, h, http.MethodPost, "/api/admin/keys", "", sessionCookie(tok))
	if rec.Code != http.StatusOK {
		t.Fatalf("세션 발급: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var issued keyIssueResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil {
		t.Fatalf("발급 응답 파싱: %v", err)
	}
	if issued.ID == "" {
		t.Fatalf("발급 id 비어있음: %+v", issued)
	}

	// ② 세션 쿠키로 해지 → 204.
	rec = do(t, h, http.MethodPost, "/api/admin/keys/revoke", `{"id":"`+issued.ID+`"}`, sessionCookie(tok))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("세션 해지: code=%d body=%s", rec.Code, rec.Body.String())
	}

	// ③ 세션 쿠키 GET 조회는 계속 200(회귀 금지).
	if rec := do(t, h, http.MethodGet, "/api/admin/keys", "", sessionCookie(tok)); rec.Code != http.StatusOK {
		t.Fatalf("세션 목록: code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminKeysBadRequests(t *testing.T) {
	initOrg(t)
	h := New(testCfg(false))

	// id 없는 해지 → 400.
	if rec := do(t, h, http.MethodPost, "/api/admin/keys/revoke", `{}`, withAdmin); rec.Code != http.StatusBadRequest {
		t.Fatalf("빈 해지: code=%d (기대 400)", rec.Code)
	}
	// /api/admin 하위 미지 경로 → 404(접두사 주인이 직접 낸다).
	if rec := do(t, h, http.MethodGet, "/api/admin/nope", "", withAdmin); rec.Code != http.StatusNotFound {
		t.Fatalf("미지 admin 경로: code=%d (기대 404)", rec.Code)
	}
}
