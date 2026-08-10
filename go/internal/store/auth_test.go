package store

import (
	"testing"
	"time"
)

func mustHash(t *testing.T, pw string) string {
	t.Helper()
	h, err := HashPassword(pw)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	return h
}

func TestAuthUserCreateAndGet(t *testing.T) {
	ctx := fresh(t)
	if err := CreateUser(ctx, "alice", "admin", mustHash(t, "s3cret-pw")); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	u, ok, err := GetUser(ctx, "alice")
	if err != nil || !ok {
		t.Fatalf("GetUser: ok=%v err=%v", ok, err)
	}
	if u.Username != "alice" || u.Role != "admin" {
		t.Fatalf("user = %+v", u)
	}
	if !VerifyPassword(u.PasswordHash, "s3cret-pw") {
		t.Fatal("올바른 비밀번호가 검증에 실패했다")
	}
	if VerifyPassword(u.PasswordHash, "wrong") {
		t.Fatal("틀린 비밀번호가 검증을 통과했다")
	}
	// 평문이 저장되지 않았음을 확인 — hash 는 평문과 달라야 한다.
	if u.PasswordHash == "s3cret-pw" || u.PasswordHash == "" {
		t.Fatalf("password_hash 가 평문이거나 비었다: %q", u.PasswordHash)
	}
}

func TestAuthGetMissingUserAndDummyCompare(t *testing.T) {
	ctx := fresh(t)
	_, ok, err := GetUser(ctx, "ghost")
	if err != nil {
		t.Fatalf("GetUser err: %v", err)
	}
	if ok {
		t.Fatal("없는 사용자가 ok=true 로 나왔다")
	}
	// 사용자 없음(빈 해시)에서도 항상 false — 사용자 유무를 노출하지 않는다.
	if VerifyPassword("", "anything") {
		t.Fatal("빈 해시(사용자 없음)가 검증을 통과했다")
	}
}

func TestAuthCreateUserRejectsBadRole(t *testing.T) {
	ctx := fresh(t)
	if err := CreateUser(ctx, "x", "root", mustHash(t, "pw")); err == nil {
		t.Fatal("알 수 없는 role 이 통과했다")
	}
	if err := CreateUser(ctx, "", "admin", mustHash(t, "pw")); err == nil {
		t.Fatal("빈 username 이 통과했다")
	}
}

func TestAuthSessionLifecycle(t *testing.T) {
	ctx := fresh(t)
	tok, err := CreateSession(ctx, "alice", "member", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if len(tok) < len(SessionTokenPrefix)+16 || tok[:len(SessionTokenPrefix)] != SessionTokenPrefix {
		t.Fatalf("토큰 꼴이 이상하다: %q", tok)
	}
	ten, u, role, ok, err := ResolveSession(ctx, tok)
	if err != nil || !ok {
		t.Fatalf("ResolveSession: ok=%v err=%v", ok, err)
	}
	if u != "alice" || role != "member" {
		t.Fatalf("resolved = %s/%s", u, role)
	}
	if ten == "" {
		t.Fatalf("tenant 가 비었다")
	}
	// 로그아웃(삭제) 후에는 해석되지 않는다.
	if err := DeleteSession(ctx, tok); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, _, _, ok, _ := ResolveSession(ctx, tok); ok {
		t.Fatal("삭제된 세션이 여전히 해석된다")
	}
}

func TestAuthSessionExpiry(t *testing.T) {
	ctx := fresh(t)
	at := freezeClock(t, "2026-08-10T00:00:00Z")
	tok, err := CreateSession(ctx, "bob", "admin", at.Add(time.Hour))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, _, _, ok, _ := ResolveSession(ctx, tok); !ok {
		t.Fatal("만료 전 세션이 해석되지 않는다")
	}
	// 시계를 만료 이후로 옮긴다.
	freezeClock(t, "2026-08-10T02:00:00Z")
	if _, _, _, ok, _ := ResolveSession(ctx, tok); ok {
		t.Fatal("만료된 세션이 해석된다")
	}
}

func TestAuthPurgeExpiredSessions(t *testing.T) {
	ctx := fresh(t)
	at := freezeClock(t, "2026-08-10T00:00:00Z")
	expired, _ := CreateSession(ctx, "a", "member", at.Add(-time.Minute)) // 이미 만료
	valid, _ := CreateSession(ctx, "b", "member", at.Add(time.Hour))
	if err := PurgeExpiredSessions(ctx); err != nil {
		t.Fatalf("PurgeExpiredSessions: %v", err)
	}
	if _, _, _, ok, _ := ResolveSession(ctx, expired); ok {
		t.Fatal("만료 세션이 정리되지 않았다")
	}
	if _, _, _, ok, _ := ResolveSession(ctx, valid); !ok {
		t.Fatal("유효 세션이 잘못 정리됐다")
	}
}

func TestAuthHasAnyUser(t *testing.T) {
	ctx := fresh(t)
	if has, err := HasAnyUser(ctx); err != nil || has {
		t.Fatalf("빈 DB 에서 HasAnyUser=%v err=%v", has, err)
	}
	if err := CreateUser(ctx, "alice", "admin", mustHash(t, "pw")); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if has, err := HasAnyUser(ctx); err != nil || !has {
		t.Fatalf("사용자 생성 후 HasAnyUser=%v err=%v", has, err)
	}
}
