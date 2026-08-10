package org

import (
	"context"
	"strings"
	"testing"

	"github.com/tscorp/user-usage/internal/db"
)

func openTestDB(t *testing.T) db.DB {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, db.Options{Mode: "local", DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := Init(ctx, d); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return d
}

// 발급 → 해석 → 해지 후 거부. Phase1 S1 의 핵심 계약.
func TestIssueResolveRevoke(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	o, err := CreateOrg(ctx, d, "Acme")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	if o.TenantID != o.ID || o.ID == "" {
		t.Fatalf("org id/tenant 이상: %+v", o)
	}

	key, err := IssueKey(ctx, d, o.ID)
	if err != nil {
		t.Fatalf("IssueKey: %v", err)
	}
	if !strings.HasPrefix(key, KeyPrefix) {
		t.Fatalf("키 접두사 없음: %q", key)
	}

	tenant, orgID, ok, err := ResolveIngestKey(ctx, d, key)
	if err != nil || !ok {
		t.Fatalf("Resolve 실패: ok=%v err=%v", ok, err)
	}
	if tenant != o.TenantID || orgID != o.ID {
		t.Fatalf("해석 불일치: tenant=%q org=%q (want %q/%q)", tenant, orgID, o.TenantID, o.ID)
	}

	if err := RevokeKey(ctx, d, key); err != nil {
		t.Fatalf("RevokeKey: %v", err)
	}
	_, _, ok, err = ResolveIngestKey(ctx, d, key)
	if err != nil {
		t.Fatalf("해지 후 Resolve err: %v", err)
	}
	if ok {
		t.Fatal("해지된 키가 해석됐다 — 격리 구멍")
	}
}

/*
 * 발급 경로는 **반드시** KeyPrefix 로만 평문을 만든다.
 *
 * 왜 이걸 따로 못박는가: 게이트(httpapi/server.go)가 "접두사가 없는 Bearer 는 키가 아니다"로
 * 판단해 DB 해석을 건너뛴다(무인증 브루트포스가 DB 를 때리지 못하게). 그 최적화는 이 불변식
 * 하나에 통째로 얹혀 있다 — 접두사 없이 발급되는 경로가 하나라도 생기면 **유효한 키인데 401**
 * 이 조용히 생기고, 증상은 게이트가 아니라 발급부에서 나온다. 이 테스트가 그 유일한 안전장치다.
 *
 * 발급 진입점이 늘어나면 여기에 같이 넣는다.
 */
func TestIssuedKeysAlwaysCarryPrefix(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	o, err := CreateOrg(ctx, d, "Acme")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	issuers := map[string]func() (string, error){
		"IssueKey": func() (string, error) { return IssueKey(ctx, d, o.ID) },
		"IssueForTenant": func() (string, error) {
			k, err := IssueForTenant(ctx, "tenant-a", "Tenant A")
			return k.Plain, err
		},
	}
	// 무작위 재료라 1회 통과가 우연일 수 있다 — 발급 경로마다 여러 번 돌려 못박는다.
	for name, issue := range issuers {
		for i := 0; i < 16; i++ {
			key, err := issue()
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if !strings.HasPrefix(key, KeyPrefix) {
				t.Fatalf("%s 가 접두사 %q 없이 발급했다: %q — 게이트가 이 키를 401 로 접는다",
					name, KeyPrefix, key)
			}
			// 접두사만 있고 무작위 몸통이 없으면 그것도 결함이다.
			if len(key) <= len(KeyPrefix) {
				t.Fatalf("%s 가 접두사뿐인 키를 발급했다: %q", name, key)
			}
		}
	}
}

// 잘못된 키·빈 키는 err 이 아니라 ok=false.
func TestResolveRejectsUnknown(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	for _, bad := range []string{"", "uu_ing_deadbeef", "not-a-key"} {
		_, _, ok, err := ResolveIngestKey(ctx, d, bad)
		if err != nil {
			t.Fatalf("bad=%q 에서 err(기대 없음): %v", bad, err)
		}
		if ok {
			t.Fatalf("bad=%q 가 해석됐다", bad)
		}
	}
}

// 두 org 의 키는 서로 다른 tenant 로 해석된다(격리의 뿌리).
func TestKeysMapToDistinctTenants(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	a, _ := CreateOrg(ctx, d, "A")
	b, _ := CreateOrg(ctx, d, "B")
	ka, _ := IssueKey(ctx, d, a.ID)
	kb, _ := IssueKey(ctx, d, b.ID)
	ta, _, _, _ := ResolveIngestKey(ctx, d, ka)
	tb, _, _, _ := ResolveIngestKey(ctx, d, kb)
	if ta == tb {
		t.Fatalf("두 org 가 같은 tenant 로 해석됐다: %q", ta)
	}
}

// 평문 키는 저장되지 않는다 — 해시만.
func TestKeyStoredHashedNotPlaintext(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	o, _ := CreateOrg(ctx, d, "Acme")
	key, _ := IssueKey(ctx, d, o.ID)

	rows, err := d.Query(ctx, "SELECT key_hash FROM ingest_keys")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	for _, r := range rows {
		if got := str(r, "key_hash"); got == key {
			t.Fatal("평문 키가 그대로 저장됐다")
		} else if got != HashKey(key) {
			t.Fatalf("저장된 해시가 HashKey 와 다르다: %q", got)
		}
	}
}
