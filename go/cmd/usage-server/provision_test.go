package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/tscorp/user-usage/internal/db"
	"github.com/tscorp/user-usage/internal/org"
	"github.com/tscorp/user-usage/internal/store"
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

// provOrg 는 org 를 하나 만들고 그 id 를 돌려준다. CreateOrg 는 tenant_id 를 org id 와 같은
// 값으로 두므로(org=tenant 1:1) 반환값은 그 org 의 tenant 이기도 하다.
func provOrg(t *testing.T, ctx context.Context, d db.DB) string {
	t.Helper()
	var out bytes.Buffer
	if rc := orgCmd(ctx, d, &out, []string{"create", "--name", "Acme"}); rc != 0 {
		t.Fatalf("org create rc=%d out=%s", rc, out.String())
	}
	for _, tok := range strings.Fields(out.String()) {
		if strings.HasPrefix(tok, "id=") {
			return strings.TrimPrefix(tok, "id=")
		}
	}
	t.Fatalf("org id 파싱 실패: %q", out.String())
	return ""
}

// captureStderr 는 fn 이 os.Stderr 로 낸 것을 모은다. 거부 사유가 **실제로 사람에게 보이는지**
// 재는 유일한 방법이다 — keyCmd 는 안내를 out 이 아니라 stderr 로 낸다(이 파일의 기존 규율).
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	fn()
	os.Stderr = orig
	_ = w.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	_ = r.Close()
	return string(b)
}

// 하위호환 — `--user` 없이 부르면 **종전과 같은 org 공용 키**다. 이미 배포된 호출 형태
// (install.sh·운영 문서·손으로 치는 명령)가 그대로 돌아야 한다.
func TestProvisionKeyIssueWithoutUserStaysOrgWide(t *testing.T) {
	ctx, d := provDB(t)
	orgID := provOrg(t, ctx, d)

	var out bytes.Buffer
	if rc := keyCmd(ctx, d, &out, []string{"issue", "--org", orgID}); rc != 0 {
		t.Fatalf("key issue rc=%d out=%s", rc, out.String())
	}
	key := strings.TrimSpace(out.String())
	if !strings.HasPrefix(key, org.KeyPrefix) {
		t.Fatalf("발급 키 접두사 없음: %q", key)
	}

	rk, ok, err := org.ResolveIngestKeyDetail(ctx, d, key)
	if err != nil || !ok {
		t.Fatalf("발급 키 해석 실패: ok=%v err=%v", ok, err)
	}
	if rk.Username != "" {
		t.Fatalf("플래그 없이 발급했는데 사용자에 묶였다: username=%q — 하위호환이 깨졌다", rk.Username)
	}
	if rk.OrgID != orgID {
		t.Fatalf("org 가 다르다: %q (기대 %q)", rk.OrgID, orgID)
	}
}

// `--user` 로 발급한 키는 그 사람에 묶인다(귀속 우선순위 ①의 재료).
func TestProvisionKeyIssueBindsUser(t *testing.T) {
	ctx, d := provDB(t)
	orgID := provOrg(t, ctx, d)

	// auth_users 는 tenant 로 격리된다(pg RLS) — 사용자는 그 **org 의 tenant** 에 만든다.
	otx := tenant.With(ctx, orgID)
	if err := store.Init(otx, d); err != nil {
		t.Fatalf("store.Init: %v", err)
	}
	hash, err := store.HashPassword("pw-12345678")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if err := store.CreateUser(otx, "amy", "member", hash); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	var out bytes.Buffer
	if rc := keyCmd(ctx, d, &out, []string{"issue", "--org", orgID, "--user", "amy"}); rc != 0 {
		t.Fatalf("key issue --user rc=%d out=%s", rc, out.String())
	}
	// 평문은 stdout 에 **한 줄**이고 그것이 유일한 노출이다.
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("stdout 이 한 줄이 아니다: %q", out.String())
	}
	key := lines[0]
	if !strings.HasPrefix(key, org.KeyPrefix) {
		t.Fatalf("발급 키 접두사 없음: %q", key)
	}

	rk, ok, err := org.ResolveIngestKeyDetail(ctx, d, key)
	if err != nil || !ok {
		t.Fatalf("발급 키 해석 실패: ok=%v err=%v", ok, err)
	}
	if rk.Username != "amy" {
		t.Fatalf("키가 사용자에 묶이지 않았다: username=%q (기대 amy)", rk.Username)
	}
}

// 없는 사용자에 묶으려 하면 **거부하고 이유를 말한다.** 조용히 만들면 오타 하나가 영원히
// 아무에게도 귀속되지 않는 키를 낳는다 — 그 보고는 유령 사용자로 쌓인다.
func TestProvisionKeyIssueRejectsUnknownUser(t *testing.T) {
	ctx, d := provDB(t)
	orgID := provOrg(t, ctx, d)

	var out bytes.Buffer
	var rc int
	stderr := captureStderr(t, func() {
		rc = keyCmd(ctx, d, &out, []string{"issue", "--org", orgID, "--user", "amyy"})
	})
	if rc == 0 {
		t.Fatalf("없는 사용자에 묶는 발급이 성공했다: rc=0 out=%q", out.String())
	}
	if s := out.String(); s != "" {
		t.Fatalf("거부했는데 stdout 에 무언가 나갔다: %q — 키가 새 나갔을 수 있다", s)
	}
	// 이유를 말해야 한다: 어떤 이름이 문제인지 + 다음에 무엇을 할지.
	if !strings.Contains(stderr, "amyy") {
		t.Fatalf("거부 사유에 문제의 이름이 없다: %q", stderr)
	}
	if !strings.Contains(stderr, "user add") {
		t.Fatalf("거부 사유가 다음 할 일을 안 알려 준다: %q", stderr)
	}
	// 그리고 **키를 만들지 않았어야 한다.**
	keys, err := org.ListKeys(ctx, orgID)
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("거부했는데 키가 %d개 생겼다", len(keys))
	}
}

/*
 * ★ CLI `user add` 도 비밀번호 최소 길이를 강제한다 (보안검토 M-3).
 *
 * API 는 store.MinPasswordLen(8)을 강제하는데 CLI 는 빈 문자열만 막고 store.HashPassword →
 * store.CreateUser 를 직접 불러 **길이 검사를 건너뛰었다**. store/users.go:48 이 "서버가 마지막
 * 방어선이다"라고 적은 그 규율이 CLI 에서만 서지 않았다.
 *
 * 이 명령이 하필 **H-1 의 복구 경로**다(OPERATIONS.md 9-3: 잠겼으면 `user add` 로 관리자를 하나
 * 더 만들라). 급히 복구하는 사람이 가장 약한 문을 쓰게 되는 조합이라 H-1 과 같이 간다.
 */
func TestUserAddEnforcesMinPasswordLength(t *testing.T) {
	ctx, d := provDB(t)
	if err := store.Init(ctx, d); err != nil {
		t.Fatalf("store.Init: %v", err)
	}
	var out bytes.Buffer

	// 검토자가 실제로 통과시킨 그 명령 — 1자 비밀번호로 **관리자**를 만들었다.
	if rc := userAdd(d, &out, []string{
		"-tenant", tenant.Default, "-username", "weakcli", "-role", "admin", "-password", "a",
	}); rc == 0 {
		t.Fatalf("1자 비밀번호로 관리자가 만들어졌다 (rc=0, out=%q)", out.String())
	}
	// 거부가 실제로 아무것도 만들지 않았다 — rc!=0 인데 계정이 남아 있으면 더 나쁘다.
	if _, ok, err := store.GetUser(ctx, "weakcli"); err != nil {
		t.Fatalf("GetUser: %v", err)
	} else if ok {
		t.Fatal("거부됐는데 계정이 생겼다")
	}

	// 경계 바로 아래(7자)도 막힌다 — MinPasswordLen 을 실제로 보는지 값으로 잰다.
	out.Reset()
	if rc := userAdd(d, &out, []string{
		"-tenant", tenant.Default, "-username", "weak7", "-role", "member", "-password", "seven77",
	}); rc == 0 {
		t.Fatal("7자 비밀번호가 통과했다")
	}

	// 8자는 통과하고, 저장된 것은 bcrypt 해시다(평문이 아니다).
	out.Reset()
	if rc := userAdd(d, &out, []string{
		"-tenant", tenant.Default, "-username", "okcli", "-role", "admin", "-password", "eight888",
	}); rc != 0 {
		t.Fatalf("8자 비밀번호가 거부됐다: rc=%d out=%q", rc, out.String())
	}
	u, ok, err := store.GetUser(ctx, "okcli")
	if err != nil || !ok {
		t.Fatalf("GetUser: %v ok=%v", err, ok)
	}
	if !strings.HasPrefix(u.PasswordHash, "$2") || !store.VerifyPassword(u.PasswordHash, "eight888") {
		t.Fatalf("bcrypt 해시로 저장되지 않았다: %q", u.PasswordHash)
	}
	// 비밀번호는 출력에 절대 실리지 않는다.
	if strings.Contains(out.String(), "eight888") {
		t.Fatalf("출력에 평문이 실렸다: %q", out.String())
	}
	// 중복도 store 가 한 자리에서 막는다(CLI 가 따로 판정하지 않는다).
	out.Reset()
	if rc := userAdd(d, &out, []string{
		"-tenant", tenant.Default, "-username", "okcli", "-role", "member", "-password", "another8",
	}); rc == 0 {
		t.Fatal("중복 사용자가 만들어졌다")
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
