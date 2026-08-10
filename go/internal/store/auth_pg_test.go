package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/tscorp/user-usage/internal/db"
	"github.com/tscorp/user-usage/internal/tenant"
)

/*
 * 크로스테넌트 격리(RLS) — 실제 pg 에 붙어야만 의미가 있다(sqlite 는 단일 테넌트라 격리 대상이
 * 없다). USAGE_TEST_PG_URL 이 없으면 건너뛴다(DB 없는 게이트에서 초록 유지).
 *
 *   USAGE_TEST_PG_URL='postgres://usage_app@127.0.0.1:5432/usage_pg' go test ./internal/store -run TestPGAuthCrossTenant -v
 *
 * ⚠ URL 은 반드시 **비-슈퍼·비-BYPASSRLS 앱 롤**이어야 한다 — 슈퍼유저로 붙으면 FORCE RLS 조차
 *   무시되어 이 테스트가 (당연히) 실패한다. 그게 곧 운영에서 잡아야 할 오설정이다.
 */
func TestPGAuthCrossTenant(t *testing.T) {
	url := os.Getenv("USAGE_TEST_PG_URL")
	if url == "" {
		t.Skip("USAGE_TEST_PG_URL 미설정 — pg 크로스테넌트 격리 테스트 건너뜀")
	}

	db.SetTenantResolver(tenant.From)
	ctx := context.Background()
	d, err := db.Open(ctx, db.Options{Mode: "remote", URL: url})
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close(); handle = nil })

	// 마이그레이션은 best-effort — 앱 롤에 CREATE 권한이 없으면 운영자가 미리 적용했다고 보고 진행한다.
	if _, err := db.Migrate(ctx, d, "../../../migrations/pg"); err != nil {
		t.Logf("migrate(무시하고 진행 — 사전 적용 가정): %v", err)
	}
	if err := Init(ctx, d); err != nil {
		t.Fatalf("store.Init: %v", err)
	}

	// 재실행 가능하도록 유니크한 사용자명을 쓴다(PK 충돌 방지).
	uname := fmt.Sprintf("alice_%d", time.Now().UnixNano())
	ctxA := tenant.With(ctx, "tenant_a")
	ctxB := tenant.With(ctx, "tenant_b")

	hash, err := HashPassword("pw-in-tenant-a")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if err := CreateUser(ctxA, uname, "admin", hash); err != nil {
		t.Fatalf("CreateUser(A): %v", err)
	}
	tokA, err := CreateSession(ctxA, uname, "admin", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("CreateSession(A): %v", err)
	}

	// tenant_a 에서는 보인다.
	if _, ok, _ := GetUser(ctxA, uname); !ok {
		t.Fatal("tenant_a 가 자기 사용자를 못 본다 — RLS 가 자기 것도 막는다")
	}
	if _, _, _, ok, _ := ResolveSession(ctxA, tokA); !ok {
		t.Fatal("tenant_a 가 자기 세션을 못 본다")
	}

	// tenant_b 에서는 절대 안 보인다(격리).
	if _, ok, _ := GetUser(ctxB, uname); ok {
		t.Fatal("tenant_b 가 tenant_a 의 사용자를 봤다 — 크로스테넌트 격리 실패")
	}
	if _, _, _, ok, _ := ResolveSession(ctxB, tokA); ok {
		t.Fatal("tenant_b 가 tenant_a 의 세션을 해석했다 — 크로스테넌트 격리 실패")
	}
}
