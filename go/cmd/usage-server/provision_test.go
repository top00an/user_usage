package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/tscorp/user-usage/internal/db"
	"github.com/tscorp/user-usage/internal/org"
	"github.com/tscorp/user-usage/internal/tenant"
)

func provDB(t *testing.T) (context.Context, db.DB) {
	t.Helper()
	ctx := tenant.With(context.Background(), tenant.Default)
	d, err := db.Open(ctx, db.Options{Mode: "local", DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := org.Init(ctx, d); err != nil {
		t.Fatalf("org.Init: %v", err)
	}
	return ctx, d
}

// Phase1 S3 — 프로비저닝 CLI: org 생성 → 키 발급 → 해석 → 해지. 실제 발급 키가 게이트에서
// 통할 값이어야 한다.
func TestProvisionOrgAndKey(t *testing.T) {
	ctx, d := provDB(t)
	var out bytes.Buffer

	if rc := orgCmd(ctx, d, &out, []string{"create", "--name", "Acme"}); rc != 0 {
		t.Fatalf("org create rc=%d out=%s", rc, out.String())
	}
	var orgID string
	for _, tok := range strings.Fields(out.String()) {
		if strings.HasPrefix(tok, "id=") {
			orgID = strings.TrimPrefix(tok, "id=")
		}
	}
	if orgID == "" {
		t.Fatalf("org id 파싱 실패: %q", out.String())
	}

	out.Reset()
	if rc := orgCmd(ctx, d, &out, []string{"list"}); rc != 0 || !strings.Contains(out.String(), "Acme") {
		t.Fatalf("org list rc=%d out=%s", rc, out.String())
	}

	out.Reset()
	if rc := keyCmd(ctx, d, &out, []string{"issue", "--org", orgID}); rc != 0 {
		t.Fatalf("key issue rc=%d out=%s", rc, out.String())
	}
	key := strings.TrimSpace(out.String())
	if !strings.HasPrefix(key, org.KeyPrefix) {
		t.Fatalf("발급 키 접두사 없음: %q", key)
	}

	tn, oid, ok, err := org.ResolveIngestKey(ctx, d, key)
	if err != nil || !ok || oid != orgID || tn == "" {
		t.Fatalf("발급 키 해석 실패: ok=%v err=%v oid=%q tn=%q", ok, err, oid, tn)
	}

	out.Reset()
	if rc := keyCmd(ctx, d, &out, []string{"revoke", "--key", key}); rc != 0 {
		t.Fatalf("key revoke rc=%d out=%s", rc, out.String())
	}
	if _, _, ok, _ := org.ResolveIngestKey(ctx, d, key); ok {
		t.Fatal("해지 후에도 키가 해석됐다")
	}
}

func TestProvisionErrors(t *testing.T) {
	ctx, d := provDB(t)
	var out bytes.Buffer
	if rc := orgCmd(ctx, d, &out, []string{"create"}); rc == 0 {
		t.Fatal("--name 없이 성공하면 안 된다")
	}
	if rc := keyCmd(ctx, d, &out, []string{"issue", "--org", "org_nope"}); rc == 0 {
		t.Fatal("없는 org 에 키 발급이 성공하면 안 된다")
	}
}
