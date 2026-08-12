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

/*
 * ★ `CREATE TABLE IF NOT EXISTS` 는 **기존 표에 새 컬럼을 넣지 않는다.**
 *
 * username 은 나중에 생긴 컬럼이라, 이 보강이 없으면 옛 DB 로 뜬 서버에서 키 해석 질의가
 * 통째로 실패한다 — 증상은 "전 팀원의 보고가 401" 이고, 원인은 여기서만 보인다.
 * 이 레포는 그 함정을 이미 안다(store/init_test.go 의 TestInitAddsMissingColumnsToOldDatabase).
 */
func TestInitAddsUsernameColumnToOldDatabase(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, db.Options{Mode: "local", DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close(); handle = nil })

	// username 이 없던 시절의 스키마를 손으로 만든다(0030_orgs.sql 그대로).
	for _, stmt := range []string{
		`CREATE TABLE orgs (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, name TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active', created_at TEXT NOT NULL)`,
		`CREATE TABLE ingest_keys (key_hash TEXT PRIMARY KEY, org_id TEXT NOT NULL,
			created_at TEXT NOT NULL, revoked_at TEXT, last_used_at TEXT)`,
	} {
		if err := d.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	// 옛 스키마에 이미 들어 있던 키 — 보강 뒤에도 그대로 살아 있어야 한다.
	o, err := insertOldOrg(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	oldKey := KeyPrefix + "legacydeadbeef"
	if err := d.Exec(ctx, "INSERT INTO ingest_keys(key_hash,org_id,created_at) VALUES(?,?,?)",
		HashKey(oldKey), o, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}

	if err := Init(ctx, d); err != nil {
		t.Fatalf("옛 DB 에서 Init 이 죽었다: %v", err)
	}
	cols := map[string]bool{}
	rows, err := d.Query(ctx, "PRAGMA table_info(ingest_keys)")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		cols[r.Str("name")] = true
	}
	if !cols["username"] {
		t.Fatalf("username 컬럼이 보강되지 않았다: %v", cols)
	}
	// 그리고 그 컬럼을 읽는 질의가 실제로 돈다 — 옛 키는 username 이 NULL 이라 하위호환 경로다.
	rk, ok, err := ResolveIngestKeyDetail(ctx, d, oldKey)
	if err != nil || !ok {
		t.Fatalf("옛 키 해석이 깨졌다: ok=%v err=%v", ok, err)
	}
	if rk.Username != "" {
		t.Fatalf("옛 키에 username 이 붙었다: %q", rk.Username)
	}
	// 보강 뒤 새로 발급한 키는 사람에 묶인다.
	newKey, err := IssueKeyFor(ctx, d, o, "amy")
	if err != nil {
		t.Fatal(err)
	}
	if rk, ok, _ := ResolveIngestKeyDetail(ctx, d, newKey); !ok || rk.Username != "amy" {
		t.Fatalf("보강 뒤 결속이 안 된다: ok=%v %+v", ok, rk)
	}
	// 멱등 — 두 번째 Init 도 죽지 않는다.
	if err := Init(ctx, d); err != nil {
		t.Fatalf("두 번째 Init: %v", err)
	}
}

// insertOldOrg 는 위 테스트가 옛 스키마에 org 한 줄을 넣는 헬퍼다(CreateOrg 와 같은 모양).
func insertOldOrg(ctx context.Context, d db.DB) (string, error) {
	id, err := newID("org_")
	if err != nil {
		return "", err
	}
	return id, d.Exec(ctx, "INSERT INTO orgs(id,tenant_id,name,status,created_at) VALUES(?,?,?,?,?)",
		id, "default", "old", "active", "2026-01-01T00:00:00Z")
}

// 키 결속 — 발급 시 묶은 이름이 해석에 그대로 나온다. 묶지 않으면 빈 문자열(하위호환).
func TestIssueBindsUsername(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	o, _ := CreateOrg(ctx, d, "Acme")

	bound, err := IssueKeyFor(ctx, d, o.ID, "amy")
	if err != nil {
		t.Fatal(err)
	}
	free, err := IssueKey(ctx, d, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rk, ok, _ := ResolveIngestKeyDetail(ctx, d, bound); !ok || rk.Username != "amy" {
		t.Fatalf("묶인 키: ok=%v %+v", ok, rk)
	}
	if rk, ok, _ := ResolveIngestKeyDetail(ctx, d, free); !ok || rk.Username != "" {
		t.Fatalf("안 묶인 키: ok=%v %+v", ok, rk)
	}
	// 전역 핸들 경로(게이트가 쓰는 자리)도 같은 답을 준다.
	if u, err := KeyUsername(ctx, bound); err != nil || u != "amy" {
		t.Fatalf("KeyUsername(bound)=%q err=%v", u, err)
	}
	if u, err := KeyUsername(ctx, free); err != nil || u != "" {
		t.Fatalf("KeyUsername(free)=%q err=%v", u, err)
	}
	// 해지된 키는 사용자도 돌려주지 않는다 — 해지 뒤에 귀속이 살아남으면 안 된다.
	if err := RevokeKey(ctx, d, bound); err != nil {
		t.Fatal(err)
	}
	if u, err := KeyUsername(ctx, bound); err != nil || u != "" {
		t.Fatalf("해지 뒤 KeyUsername=%q err=%v", u, err)
	}
}

/*
 * ★ 동결 ② — 셀프서비스 목록·해지는 **자기 키만** 본다.
 *
 * 그리고 ⑥ 테넌트 스코프: 다른 tenant 의 키는 같은 이름의 사용자라도 보이지 않고 해지되지 않는다.
 */
func TestSelfScopedKeyListAndRevoke(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	amy, err := IssueForTenantUser(ctx, "t-a", "Tenant A", "amy")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := IssueForTenantUser(ctx, "t-a", "Tenant A", "bob")
	if err != nil {
		t.Fatal(err)
	}
	// 다른 tenant 의 **같은 이름** 사용자.
	otherAmy, err := IssueForTenantUser(ctx, "t-b", "Tenant B", "amy")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := IssueForTenant(ctx, "t-a", "Tenant A"); err != nil { // 공용 키 한 장
		t.Fatal(err)
	}

	got, err := ListKeysForUser(ctx, "t-a", "amy")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != amy.ID || got[0].Username != "amy" {
		t.Fatalf("amy 의 목록: %+v (기대 amy 키 1장)", got)
	}
	// 관리자 전체 현황은 tenant 안의 3장(amy·bob·공용)을 본다 — 남의 tenant 것은 없다.
	all, err := ListKeys(ctx, "t-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("t-a 전체 현황 %d장 (기대 3): %+v", len(all), all)
	}
	for _, k := range all {
		if k.ID == otherAmy.ID {
			t.Fatal("다른 tenant 의 키가 현황에 샜다")
		}
	}
	// 빈 이름으로는 아무것도 안 나온다 — 공용 키가 "주인 없는 사람"에게 흘러가지 않는다.
	if got, _ := ListKeysForUser(ctx, "t-a", ""); len(got) != 0 {
		t.Fatalf("빈 이름 목록: %+v", got)
	}

	// 남의 키는 해지되지 않는다(owned=false) — 그리고 **실제로 살아 있다.**
	if owned, err := RevokeByIDForUser(ctx, "t-a", "amy", bob.ID); err != nil || owned {
		t.Fatalf("남의 키 해지: owned=%v err=%v", owned, err)
	}
	if rk, ok, _ := ResolveIngestKeyDetail(ctx, d, bob.Plain); !ok || rk.Username != "bob" {
		t.Fatal("남의 해지 시도로 bob 의 키가 죽었다")
	}
	// 다른 tenant 의 같은 이름도 안 된다.
	if owned, err := RevokeByIDForUser(ctx, "t-a", "amy", otherAmy.ID); err != nil || owned {
		t.Fatalf("크로스테넌트 해지: owned=%v err=%v", owned, err)
	}
	if rk, ok, _ := ResolveIngestKeyDetail(ctx, d, otherAmy.Plain); !ok || rk.Username != "amy" {
		t.Fatal("크로스테넌트 해지가 통했다 — 테넌트 스코프가 뚫렸다")
	}
	// 없는 키도 owned=false — 남의 키와 구분되지 않는다.
	if owned, _ := RevokeByIDForUser(ctx, "t-a", "amy", "nosuchhash"); owned {
		t.Fatal("없는 키가 owned=true")
	}

	// 자기 키는 해지된다. 멱등이다.
	if owned, err := RevokeByIDForUser(ctx, "t-a", "amy", amy.ID); err != nil || !owned {
		t.Fatalf("자기 키 해지: owned=%v err=%v", owned, err)
	}
	if _, ok, _ := ResolveIngestKeyDetail(ctx, d, amy.Plain); ok {
		t.Fatal("해지했는데 아직 해석된다")
	}
	if owned, err := RevokeByIDForUser(ctx, "t-a", "amy", amy.ID); err != nil || !owned {
		t.Fatalf("두 번째 해지(멱등): owned=%v err=%v", owned, err)
	}
}

// 관리자 해지도 tenant 로 스코프된다(기존 계약의 회귀 방지).
func TestRevokeByIDIsTenantScoped(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	other, err := IssueForTenantUser(ctx, "t-b", "Tenant B", "bob")
	if err != nil {
		t.Fatal(err)
	}
	if err := RevokeByID(ctx, "t-a", other.ID); err != nil {
		t.Fatalf("RevokeByID: %v", err)
	}
	if _, ok, _ := ResolveIngestKeyDetail(ctx, d, other.Plain); !ok {
		t.Fatal("t-a 관리자가 t-b 의 키를 해지했다")
	}
}

/*
 * ★ 사람 단위 키 해지 — 퇴사 처리가 자격을 **전부** 거둔다 (보안검토 M-1).
 *
 * 여기서 잰다: ①거둔 개수가 실제와 맞는가 ②남의 키·남의 tenant 키가 휩쓸리지 않는가
 * ③이미 해지된 키를 두 번 세지 않는가(멱등) ④세기만 하는 쪽은 아무것도 바꾸지 않는가.
 */
func TestRevokeAllForUserRevokesOnlyThatPersonsLiveKeys(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	k1, err := IssueForTenantUser(ctx, "t-a", "Tenant A", "leaver")
	if err != nil {
		t.Fatal(err)
	}
	k2, err := IssueForTenantUser(ctx, "t-a", "Tenant A", "leaver")
	if err != nil {
		t.Fatal(err)
	}
	mine, err := IssueForTenantUser(ctx, "t-a", "Tenant A", "stayer") // 같은 tenant, 다른 사람
	if err != nil {
		t.Fatal(err)
	}
	shared, err := IssueForTenant(ctx, "t-a", "Tenant A") // org 공용(레거시) 키 — 아무에게도 안 묶였다
	if err != nil {
		t.Fatal(err)
	}
	crossTenant, err := IssueForTenantUser(ctx, "t-b", "Tenant B", "leaver") // 동명이인, 남의 tenant
	if err != nil {
		t.Fatal(err)
	}

	// 세기만 하는 쪽은 정확히 2 이고, **아무것도 바꾸지 않는다**.
	if n, err := CountActiveKeysForUser(ctx, "t-a", "leaver"); err != nil || n != 2 {
		t.Fatalf("CountActiveKeysForUser=%d err=%v (기대 2)", n, err)
	}
	if _, ok, _ := ResolveIngestKeyDetail(ctx, d, k1.Plain); !ok {
		t.Fatal("세기만 했는데 키가 죽었다")
	}

	n, err := RevokeAllForUser(ctx, "t-a", "leaver")
	if err != nil {
		t.Fatalf("RevokeAllForUser: %v", err)
	}
	if n != 2 {
		t.Fatalf("해지 개수=%d (기대 2) — 이 수가 곧 '무엇을 거뒀는가'의 증거다", n)
	}
	for i, k := range []string{k1.Plain, k2.Plain} {
		if _, ok, _ := ResolveIngestKeyDetail(ctx, d, k); ok {
			t.Fatalf("leaver 의 키 %d 가 아직 산다 — 삭제된 이름으로 보고가 계속된다", i+1)
		}
	}
	// 남의 키·공용 키·남의 tenant 동명이인 키는 전부 멀쩡하다.
	for name, k := range map[string]string{
		"같은 tenant 다른 사람": mine.Plain,
		"org 공용(레거시)":     shared.Plain,
		"남의 tenant 동명이인":  crossTenant.Plain,
	} {
		if _, ok, _ := ResolveIngestKeyDetail(ctx, d, k); !ok {
			t.Fatalf("%s 키가 함께 죽었다 — 퇴사자 하나가 남의 수집기를 멈춘다", name)
		}
	}

	// 멱등 — 두 번째는 0 이다(이미 해지된 것을 다시 세지 않는다).
	if n, err := RevokeAllForUser(ctx, "t-a", "leaver"); err != nil || n != 0 {
		t.Fatalf("두 번째 호출=%d err=%v (기대 0)", n, err)
	}
	if n, err := CountActiveKeysForUser(ctx, "t-a", "leaver"); err != nil || n != 0 {
		t.Fatalf("해지 뒤 살아 있는 키=%d (기대 0)", n)
	}

	// 빈 이름은 아무것도 건드리지 않는다 — 빈 값이 "전부"로 흐르면 tenant 의 키가 통째로 죽는다.
	if n, err := RevokeAllForUser(ctx, "t-a", ""); err != nil || n != 0 {
		t.Fatalf("빈 username: n=%d err=%v", n, err)
	}
	if n, err := CountActiveKeysForUser(ctx, "t-a", ""); err != nil || n != 0 {
		t.Fatalf("빈 username 세기: n=%d err=%v", n, err)
	}
	if _, ok, _ := ResolveIngestKeyDetail(ctx, d, mine.Plain); !ok {
		t.Fatal("빈 username 이 남의 키를 지웠다")
	}
}
