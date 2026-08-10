package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/tscorp/user-usage/internal/config"
	"github.com/tscorp/user-usage/internal/org"
	"github.com/tscorp/user-usage/internal/tenant"
)

// Phase1 S2 — 멀티테넌트 게이트: 인테이크가 단일 토큰이 아니라 org 인제스트 키로 인증되고,
// 키가 해석한 tenant 가 요청에 실린다. (진짜 격리는 pg RLS 로, db.TestPGCrossTenantIsolation 가 실측.)
func multiTenantCfg() config.Config {
	return config.Config{
		Token: testAdmin, IntakeToken: "", // 순수 키 모드(단일 인테이크 토큰 없음)
		Mode: "local", Host: "127.0.0.1", Port: 4191, Tenant: "default", MultiTenant: true,
		IntakeRate: 20, IntakeBurst: 40, // 프로덕션 기본값(config.Read 와 동일)
	}
}

func TestMultiTenantIntakeGate(t *testing.T) {
	ctx := tenant.With(context.Background(), "default")
	d := openDB(t) // store·identity 전역 핸들 세팅
	if err := org.Init(ctx, d); err != nil {
		t.Fatalf("org.Init: %v", err)
	}
	o, err := org.CreateOrg(ctx, d, "Acme")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	key, err := org.IssueKey(ctx, d, o.ID)
	if err != nil {
		t.Fatalf("IssueKey: %v", err)
	}

	h := New(multiTenantCfg())
	withKey := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+key) }

	// ① 유효 인제스트 키로 보고 → 200
	if rec := do(t, h, http.MethodPost, "/api/usage", `{"sessions":[]}`, withKey); rec.Code != http.StatusOK {
		t.Fatalf("유효 키 인테이크: code=%d body=%s", rec.Code, rec.Body.String())
	}

	// ② 미지 키로 보고 → 401 (게이트가 못 뚫는다)
	badKey := func(r *http.Request) { r.Header.Set("Authorization", "Bearer uu_ing_deadbeef") }
	if rec := do(t, h, http.MethodPost, "/api/usage", `{"sessions":[]}`, badKey); rec.Code != http.StatusUnauthorized {
		t.Fatalf("미지 키: code=%d (기대 401)", rec.Code)
	}

	// ③ 인제스트 키로 조회 시도 → 403 (보고 스코프는 열람 불가)
	if rec := do(t, h, http.MethodGet, "/api/usage/summary?days=30", "", withKey); rec.Code != http.StatusForbidden {
		t.Fatalf("키로 조회: code=%d (기대 403)", rec.Code)
	}

	// ④ 해지된 키 → 401
	if err := org.RevokeKey(ctx, d, key); err != nil {
		t.Fatalf("RevokeKey: %v", err)
	}
	if rec := do(t, h, http.MethodPost, "/api/usage", `{"sessions":[]}`, withKey); rec.Code != http.StatusUnauthorized {
		t.Fatalf("해지 키: code=%d (기대 401)", rec.Code)
	}
}

/*
 * 단일테넌트(기본 배포)에서도 인제스트 키가 **보고 경로 하나**를 연다.
 *
 * 왜 필요한가: 온보딩 UI(POST /api/admin/keys)는 모드와 무관하게 키를 발급하고, install.sh 는
 * 그 키 하나로 ② 수집기 다운로드와 ⑥ 백필 업로드를 모두 한다. 키가 다운로드만 열고 업로드를
 * 열지 않으면 원커맨드 설치가 **반쪽에서 401** 로 끝난다(운영자 화면에는 원인이 안 보인다).
 */
func TestSingleTenantIngestKeyIntake(t *testing.T) {
	ctx := tenant.With(context.Background(), "default")
	d := openDB(t)
	if err := org.Init(ctx, d); err != nil {
		t.Fatalf("org.Init: %v", err)
	}
	issued, err := org.IssueForTenant(ctx, "default", "default")
	if err != nil {
		t.Fatalf("IssueForTenant: %v", err)
	}

	h := New(testCfg(false)) // MultiTenant=false
	withKey := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+issued.Plain) }

	// ① 유효 인제스트 키로 보고 → 200
	if rec := do(t, h, http.MethodPost, "/api/usage", `{"sessions":[]}`, withKey); rec.Code != http.StatusOK {
		t.Fatalf("단일테넌트 키 인테이크: code=%d body=%s", rec.Code, rec.Body.String())
	}
	// ② 미지 키 → 401
	badKey := func(r *http.Request) { r.Header.Set("Authorization", "Bearer uu_ing_deadbeef") }
	if rec := do(t, h, http.MethodPost, "/api/usage", `{"sessions":[]}`, badKey); rec.Code != http.StatusUnauthorized {
		t.Fatalf("미지 키: code=%d (기대 401)", rec.Code)
	}
	// ③ 인제스트 키로 조회 → 403 (보고 스코프는 열람 불가 — 모드와 무관한 규율)
	if rec := do(t, h, http.MethodGet, "/api/usage/summary?days=30", "", withKey); rec.Code != http.StatusForbidden {
		t.Fatalf("키로 조회: code=%d (기대 403)", rec.Code)
	}
	// ④ 해지된 키 → 즉시 401
	if err := org.RevokeByID(ctx, "default", issued.ID); err != nil {
		t.Fatalf("RevokeByID: %v", err)
	}
	if rec := do(t, h, http.MethodPost, "/api/usage", `{"sessions":[]}`, withKey); rec.Code != http.StatusUnauthorized {
		t.Fatalf("해지 키: code=%d (기대 401)", rec.Code)
	}
}

// probeTenant 는 게이트가 컨텍스트에 실은 tenant 를 가로채 본다. 라우트 앞에 끼워 넣고
// handled=false 를 돌려주므로 뒤의 실제 라우트는 그대로 돈다.
func probeTenant(s *server, seen *string) {
	probe := func(_ http.ResponseWriter, r *http.Request, _ *rctx) (bool, error) {
		*seen = tenant.From(r.Context())
		return false, nil
	}
	s.routes = append([]route{probe}, s.routes...)
}

/*
 * 단일테넌트에서 키가 해석돼도 **tenant 는 배포 기본(cfg.Tenant)으로 고정**한다.
 * 키가 가리키는 tenant 를 따라가면 단일테넌트 배포(sqlite, RLS 없음)에서 조용히 여러 tenant 가
 * 생기고, 저장 계층은 그것을 격리하지 않으므로 조회가 뒤섞인다.
 */
func TestSingleTenantIngestKeyPinsDeploymentTenant(t *testing.T) {
	ctx := tenant.With(context.Background(), "default")
	d := openDB(t)
	if err := org.Init(ctx, d); err != nil {
		t.Fatalf("org.Init: %v", err)
	}
	// CreateOrg 는 tenant_id 를 org id 로 둔다 — 즉 "default" 가 아닌 tenant 의 키다.
	o, err := org.CreateOrg(ctx, d, "Acme")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	if o.TenantID == "default" {
		t.Fatalf("전제 위반: 남의 tenant 키여야 한다 (tenant=%q)", o.TenantID)
	}
	key, err := org.IssueKey(ctx, d, o.ID)
	if err != nil {
		t.Fatalf("IssueKey: %v", err)
	}

	s := New(testCfg(false)).(*server)
	var seen string
	probeTenant(s, &seen)
	withKey := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+key) }
	if rec := do(t, s, http.MethodPost, "/api/usage", `{"sessions":[]}`, withKey); rec.Code != http.StatusOK {
		t.Fatalf("보고: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if seen != "default" {
		t.Fatalf("단일테넌트인데 요청 tenant=%q (기대 cfg.Tenant=default) — 조용한 다중 테넌트", seen)
	}
}

// 멀티테넌트는 종전 그대로 — 키가 가리키는 tenant 를 싣는다(무회귀 잠금).
func TestMultiTenantIngestKeyKeepsKeyTenant(t *testing.T) {
	ctx := tenant.With(context.Background(), "default")
	d := openDB(t)
	if err := org.Init(ctx, d); err != nil {
		t.Fatalf("org.Init: %v", err)
	}
	o, _ := org.CreateOrg(ctx, d, "Acme")
	key, _ := org.IssueKey(ctx, d, o.ID)

	s := New(multiTenantCfg()).(*server)
	var seen string
	probeTenant(s, &seen)
	withKey := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+key) }
	if rec := do(t, s, http.MethodPost, "/api/usage", `{"sessions":[]}`, withKey); rec.Code != http.StatusOK {
		t.Fatalf("보고: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if seen != o.TenantID {
		t.Fatalf("멀티테넌트 요청 tenant=%q (기대 %q) — 격리 회귀", seen, o.TenantID)
	}
}
