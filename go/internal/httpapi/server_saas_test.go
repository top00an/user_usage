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

// 멀티테넌트 모드가 꺼져 있으면(기본) 인제스트 키는 아무것도 열지 않는다 — 종전 단일 토큰 경로만.
func TestKeyIgnoredWhenSingleTenant(t *testing.T) {
	ctx := tenant.With(context.Background(), "default")
	d := openDB(t)
	if err := org.Init(ctx, d); err != nil {
		t.Fatalf("org.Init: %v", err)
	}
	o, _ := org.CreateOrg(ctx, d, "Acme")
	key, _ := org.IssueKey(ctx, d, o.ID)

	h := New(testCfg(false)) // MultiTenant=false
	withKey := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+key) }
	if rec := do(t, h, http.MethodPost, "/api/usage", `{"sessions":[]}`, withKey); rec.Code != http.StatusUnauthorized {
		t.Fatalf("싱글테넌트에서 키가 통과됨: code=%d (기대 401)", rec.Code)
	}
}
