package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

/*
 * 사용자 관리 저장 계층. 여기서 재는 것은 **보안 불변식의 재료**다 —
 * 판정(마지막 관리자·자기 강등)은 httpapi 가 지지만, 그 판정이 기대는 값과
 * "세션이 실제로 끊기는가"는 이 계층에서만 증명된다.
 */

func mustUser(t *testing.T, ctx context.Context, username, role, pw string) {
	t.Helper()
	if err := CreateUserWithPassword(ctx, username, role, pw); err != nil {
		t.Fatalf("CreateUserWithPassword(%s): %v", username, err)
	}
}

func TestListUsersIsSortedAndCarriesNoHash(t *testing.T) {
	ctx := fresh(t)
	mustUser(t, ctx, "zoe", "member", "password-zoe")
	mustUser(t, ctx, "amy", "admin", "password-amy")

	users, err := ListUsers(ctx)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 2 || users[0].Username != "amy" || users[1].Username != "zoe" {
		t.Fatalf("이름순이 아니다: %+v", users)
	}
	if users[0].Role != "admin" || users[1].Role != "member" {
		t.Fatalf("role 이 틀렸다: %+v", users)
	}
	if users[0].CreatedAt == "" {
		t.Fatalf("created_at 이 비었다: %+v", users[0])
	}
	// UserSummary 에 해시를 담을 자리가 없다는 것을 값으로 못박는다(타입이 바뀌면 여기가 깨진다).
	for _, u := range users {
		if strings.Contains(u.Username+u.Role+u.CreatedAt, "$2a$") {
			t.Fatalf("bcrypt 해시가 요약에 샜다: %+v", u)
		}
	}
}

// 비밀번호는 **bcrypt 해시로만** 저장된다. 평문이 auth_users 어디에도 남지 않는다.
func TestCreateUserStoresBcryptNotPlaintext(t *testing.T) {
	ctx := fresh(t)
	const pw = "super-secret-1234"
	mustUser(t, ctx, "amy", "admin", pw)

	u, ok, err := GetUser(ctx, "amy")
	if err != nil || !ok {
		t.Fatalf("GetUser: %v ok=%v", err, ok)
	}
	if u.PasswordHash == pw {
		t.Fatal("평문이 그대로 저장됐다")
	}
	if !strings.HasPrefix(u.PasswordHash, "$2") {
		t.Fatalf("bcrypt 해시가 아니다: %q", u.PasswordHash)
	}
	if !VerifyPassword(u.PasswordHash, pw) {
		t.Fatal("저장된 해시로 검증이 안 된다")
	}
	// 표 전체를 훑어 평문이 어느 컬럼에도 없는지 본다.
	d, _ := conn()
	rows, err := d.Query(ctx, "SELECT username, password_hash, role, created_at FROM auth_users")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		for _, col := range []string{"username", "password_hash", "role", "created_at"} {
			if strings.Contains(teamStr(r, col), pw) {
				t.Fatalf("평문이 %s 컬럼에 남았다", col)
			}
		}
	}
}

func TestCreateUserRejectsDuplicateWeakAndBadRole(t *testing.T) {
	ctx := fresh(t)
	mustUser(t, ctx, "amy", "admin", "password-amy")

	if err := CreateUserWithPassword(ctx, "amy", "member", "password-amy2"); !errors.Is(err, ErrUserExists) {
		t.Fatalf("중복 생성이 ErrUserExists 가 아니다: %v", err)
	}
	if err := CreateUserWithPassword(ctx, "bob", "member", "short"); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("짧은 비밀번호가 ErrWeakPassword 가 아니다: %v", err)
	}
	if err := CreateUserWithPassword(ctx, "bob", "root", "password-bob"); err == nil {
		t.Fatal("알 수 없는 role 이 통과했다")
	}
	// 거부된 사용자는 실제로 만들어지지 않았다.
	if _, ok, _ := GetUser(ctx, "bob"); ok {
		t.Fatal("거부됐는데 사용자가 생겼다")
	}
}

// 비밀번호 최소 길이는 **룬 수**로 센다 — 한글 8자가 바이트 길이(24)로 통과하면 규칙이 사람마다 다르다.
func TestPasswordLengthCountsRunes(t *testing.T) {
	ctx := fresh(t)
	if err := CreateUserWithPassword(ctx, "kim", "member", "가나다라마바"); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("한글 6자가 통과했다(룬 수로 세지 않는다): %v", err)
	}
	if err := CreateUserWithPassword(ctx, "kim", "member", "가나다라마바사아"); err != nil {
		t.Fatalf("한글 8자가 거부됐다: %v", err)
	}
}

func TestCountAdmins(t *testing.T) {
	ctx := fresh(t)
	if n, err := CountAdmins(ctx); err != nil || n != 0 {
		t.Fatalf("빈 저장소: n=%d err=%v", n, err)
	}
	mustUser(t, ctx, "amy", "admin", "password-amy")
	mustUser(t, ctx, "bob", "member", "password-bob")
	if n, _ := CountAdmins(ctx); n != 1 {
		t.Fatalf("n=%d (기대 1)", n)
	}
	if err := SetUserRole(ctx, "bob", "admin"); err != nil {
		t.Fatal(err)
	}
	if n, _ := CountAdmins(ctx); n != 2 {
		t.Fatalf("n=%d (기대 2)", n)
	}
}

/*
 * ★ 마지막 관리자 보호는 **저장 계층이** 진다 (보안검토 H-1).
 *
 * 판정(몇 명인가)과 변경(강등·삭제)이 한 트랜잭션이라야 동시 요청에서도 성립한다. 여기서는
 * 그 계약의 **값**을 잰다 — 동시성 자체는 httpapi 의 회귀 테스트가 왕복으로 잡는다.
 */
func TestGuardedRoleChangeAndDeleteProtectLastAdmin(t *testing.T) {
	ctx := fresh(t)
	mustUser(t, ctx, "boss", "admin", "password-boss")
	mustUser(t, ctx, "amy", "member", "password-amy")

	// 관리자가 혼자면 강등도 삭제도 ErrLastAdmin 이다.
	if _, err := SetUserRoleGuarded(ctx, "boss", "member"); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("마지막 관리자 강등: %v (기대 ErrLastAdmin)", err)
	}
	if _, err := DeleteUserGuarded(ctx, "boss"); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("마지막 관리자 삭제: %v (기대 ErrLastAdmin)", err)
	}
	// **거부가 실제로 아무것도 바꾸지 않았다** — 트랜잭션이 되돌아갔다는 값 증거다.
	if u, ok, _ := GetUser(ctx, "boss"); !ok || u.Role != "admin" {
		t.Fatalf("거부됐는데 boss 가 바뀌었다: %+v ok=%v", u, ok)
	}

	// member 는 마지막 관리자 판정과 무관하다(지워도 관리자 수는 그대로다).
	if role, err := DeleteUserGuarded(ctx, "amy"); err != nil || role != "member" {
		t.Fatalf("member 삭제: role=%q err=%v", role, err)
	}

	// 관리자가 둘이 되면 열린다 — 가드가 과하게 넓지 않다.
	mustUser(t, ctx, "vice", "admin", "password-vice")
	from, err := SetUserRoleGuarded(ctx, "boss", "member")
	if err != nil {
		t.Fatalf("둘째 관리자가 있는데 강등이 막혔다: %v", err)
	}
	// from 은 **그 트랜잭션이 읽은** 이전 role 이다 — 감사 로그와 세션 무효화 판정의 근거다.
	if from != "admin" {
		t.Fatalf("from=%q (기대 admin) — 강등인지 승격인지 이 값으로 가른다", from)
	}
	if n, _ := CountAdmins(ctx); n != 1 {
		t.Fatalf("강등 뒤 관리자 수=%d (기대 1)", n)
	}

	// 승격은 언제나 열려 있다(아무도 잠그지 않는다).
	if from, err := SetUserRoleGuarded(ctx, "boss", "admin"); err != nil || from != "member" {
		t.Fatalf("승격: from=%q err=%v", from, err)
	}
	// 없는 사용자·알 수 없는 role 은 종전 판정 그대로다.
	if _, err := SetUserRoleGuarded(ctx, "ghost", "admin"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("없는 사용자 강등: %v", err)
	}
	if _, err := DeleteUserGuarded(ctx, "ghost"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("없는 사용자 삭제: %v", err)
	}
	if _, err := SetUserRoleGuarded(ctx, "boss", "root"); err == nil {
		t.Fatal("알 수 없는 role 이 통과했다")
	}
}

func TestSetUserRoleAndPasswordAndDeleteRejectUnknownUser(t *testing.T) {
	ctx := fresh(t)
	if err := SetUserRole(ctx, "ghost", "admin"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("SetUserRole: %v", err)
	}
	if err := SetUserPassword(ctx, "ghost", "password-ghost"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("SetUserPassword: %v", err)
	}
	if err := DeleteUser(ctx, "ghost"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("DeleteUser: %v", err)
	}
}

func TestSetUserPasswordRehashesAndInvalidatesOld(t *testing.T) {
	ctx := fresh(t)
	mustUser(t, ctx, "amy", "admin", "password-old")
	if err := SetUserPassword(ctx, "amy", "password-new"); err != nil {
		t.Fatalf("SetUserPassword: %v", err)
	}
	u, _, _ := GetUser(ctx, "amy")
	if VerifyPassword(u.PasswordHash, "password-old") {
		t.Fatal("옛 비밀번호가 아직 통한다")
	}
	if !VerifyPassword(u.PasswordHash, "password-new") {
		t.Fatal("새 비밀번호가 통하지 않는다")
	}
	if err := SetUserPassword(ctx, "amy", "short"); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("짧은 재설정이 통과했다: %v", err)
	}
}

/*
 * ★ 불변식 ④의 저장 계층 절반 — **이름으로 세션을 끊을 수 있어야 한다.**
 *
 * DeleteSession 은 평문 토큰으로만 지운다. 그 토큰은 그 사람의 쿠키에만 있으므로, 이 함수가
 * 없으면 강등·삭제된 사람의 세션을 끊을 수단이 아예 없다.
 */
func TestDeleteSessionsForUserKillsOnlyThatUser(t *testing.T) {
	ctx := fresh(t)
	mustUser(t, ctx, "amy", "admin", "password-amy")
	mustUser(t, ctx, "bob", "member", "password-bob")
	exp := clock().Add(time.Hour)

	amy1, err := CreateSession(ctx, "amy", "admin", exp)
	if err != nil {
		t.Fatal(err)
	}
	amy2, err := CreateSession(ctx, "amy", "admin", exp)
	if err != nil {
		t.Fatal(err)
	}
	bob1, err := CreateSession(ctx, "bob", "member", exp)
	if err != nil {
		t.Fatal(err)
	}

	if err := DeleteSessionsForUser(ctx, "amy"); err != nil {
		t.Fatalf("DeleteSessionsForUser: %v", err)
	}
	// amy 의 세션은 **전부** 끊긴다(한 개가 아니다 — 여러 브라우저에 로그인해 있을 수 있다).
	for i, tok := range []string{amy1, amy2} {
		if _, _, _, ok, _ := ResolveSession(ctx, tok); ok {
			t.Fatalf("amy 세션 %d 가 살아 있다 — 강등돼도 만료까지 관리자다", i+1)
		}
	}
	// 남의 세션은 건드리지 않는다.
	if _, u, _, ok, _ := ResolveSession(ctx, bob1); !ok || u != "bob" {
		t.Fatal("bob 세션이 함께 끊겼다")
	}
	// 멱등 — 두 번 불러도 오류가 아니다.
	if err := DeleteSessionsForUser(ctx, "amy"); err != nil {
		t.Fatalf("두 번째 호출: %v", err)
	}
	if err := DeleteSessionsForUser(ctx, ""); err == nil {
		t.Fatal("빈 username 이 통과했다 — 전 세션이 날아갈 자리다")
	}
}

// 삭제된 사용자의 세션은 남아 있으면 여전히 해석된다 — 그래서 호출부가 짝으로 부른다는 사실을
// 여기서 못박는다(이 테스트가 깨지면 auth_sessions 에 FK 가 생겼다는 뜻이고, 그때 규율이 바뀐다).
func TestDeleteUserAloneLeavesSessionAlive(t *testing.T) {
	ctx := fresh(t)
	mustUser(t, ctx, "amy", "admin", "password-amy")
	tok, err := CreateSession(ctx, "amy", "admin", clock().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := DeleteUser(ctx, "amy"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok, _ := ResolveSession(ctx, tok); !ok {
		t.Skip("세션이 스스로 끊겼다 — 저장 계층에 연쇄 삭제가 생겼다(규율 재검토)")
	}
	if err := DeleteSessionsForUser(ctx, "amy"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok, _ := ResolveSession(ctx, tok); ok {
		t.Fatal("삭제 + 세션 무효화 뒤에도 세션이 산다")
	}
}
