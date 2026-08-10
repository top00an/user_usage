package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/tscorp/user-usage/internal/tenant"
)

/*
 * 대시보드 사람 로그인(ID/PW → 세션 쿠키)의 저장 계층.
 *
 * 두 표(migrations/pg/0034_auth.sql 이 pg 스키마를 소유, sqlite 는 store.go 의 DDL 이 소유):
 *   auth_users     tenant 안의 사람 계정(username, bcrypt password_hash, role)
 *   auth_sessions  발급된 세션(token_hash=sha256(token), expires_at)
 *
 * 규율(기존 인제스트 키·개인 토큰과 동일):
 *   · 비밀번호는 평문 저장 금지 — bcrypt 해시만.
 *   · 세션 토큰은 평문 저장 금지 — sha256 해시만(평문은 쿠키에만 존재).
 *   · tenant 격리는 DB 계층이 tenant.From(ctx) 를 주입해 pg RLS 가 진다(sqlite 는 단일 테넌트).
 */

// SessionTokenPrefix — 세션 토큰 접두사(로그·디버깅에서 다른 토큰과 구분). 평문 토큰의 꼴은
// "uu_sess_" + 32바이트 hex 다.
const SessionTokenPrefix = "uu_sess_"

// AuthRoles — 사람 계정이 가질 수 있는 role 전부. 게이트가 이 값을 스코프로 매핑한다
// (admin=전사 열람, member=자기 데이터만).
var AuthRoles = []string{"admin", "member"}

// isAuthRole 은 role 이 화이트리스트에 있는지 본다. DB CHECK 제약이 최종 방어선이지만, 여기서
// 먼저 걸러 "말이 되는 실패"를 준다(제약 위반 원문이 클라이언트로 흐르지 않게).
func isAuthRole(role string) bool {
	for _, r := range AuthRoles {
		if r == role {
			return true
		}
	}
	return false
}

// sessionTimeFmt — 세션 시간 컬럼(created_at·expires_at)의 문자열 꼴. RFC3339 Zulu 는 고정 폭이라
// 사전식 정렬이 곧 시간 정렬이고, 그래서 `expires_at > ?` 비교가 방언 무관하게 성립한다.
const sessionTimeFmt = time.RFC3339

// AuthUser 는 사람 계정 한 행이다(비밀번호 검증에 password_hash 가 필요하므로 함께 싣는다 —
// 이 타입은 게이트/로그인 경로 안에서만 돈다).
type AuthUser struct {
	Username     string
	Role         string
	PasswordHash string
}

// HashPassword 는 평문 비밀번호를 bcrypt 해시로 접는다(저장·비교는 이 값으로).
func HashPassword(plaintext string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("store: 비밀번호 해시 실패: %w", err)
	}
	return string(h), nil
}

// bcryptDummyHash — 존재하지 않는 사용자에도 bcrypt 검증 1회를 태우기 위한 더미 해시다.
// 사용자 유무가 응답 시간으로 새지 않게 한다(사용자 열거 방지). 아무 비밀번호와도 일치하지 않는다.
var bcryptDummyHash = mustBcrypt("uu_dummy_password_for_constant_time_compare")

func mustBcrypt(s string) []byte {
	h, err := bcrypt.GenerateFromPassword([]byte(s), bcrypt.DefaultCost)
	if err != nil {
		// bcrypt 가 부팅에서 실패하는 환경은 이 서비스가 뜰 자리가 아니다.
		panic("store: 더미 bcrypt 해시 생성 실패: " + err.Error())
	}
	return h
}

// VerifyPassword 는 저장된 bcrypt 해시와 평문을 비교한다. hash 가 비면(=사용자 없음) 더미 해시로
// 검증 1회를 태워 타이밍을 맞추고 항상 false 를 돌려준다 — 사용자 유무를 시간으로 노출하지 않는다.
func VerifyPassword(hash, plaintext string) bool {
	if hash == "" {
		_ = bcrypt.CompareHashAndPassword(bcryptDummyHash, []byte(plaintext))
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext)) == nil
}

// CreateUser 는 tenant(ctx) 안에 사람 계정을 만든다. 이미 있으면 오류다(PK 충돌) — 덮어쓰기는
// 실수로 비밀번호를 바꾸는 자리라 열지 않는다. passwordHash 는 HashPassword 의 결과여야 한다.
func CreateUser(ctx context.Context, username, role, passwordHash string) error {
	d, err := conn()
	if err != nil {
		return err
	}
	if username == "" {
		return fmt.Errorf("store: 사용자 생성에 username 이 필요하다")
	}
	if !isAuthRole(role) {
		return fmt.Errorf("store: 알 수 없는 role %q — admin|member 중 하나여야 한다", role)
	}
	if passwordHash == "" {
		return fmt.Errorf("store: 사용자 생성에 password_hash 가 필요하다")
	}
	at := clock().UTC().Format(sessionTimeFmt)
	// pg 는 tenant_id 가 DEFAULT current_setting 이라 컬럼 목록에서 뺀다(member_tokens 와 같은 규율).
	if err := d.Exec(ctx,
		"INSERT INTO auth_users(username, password_hash, role, created_at) VALUES(?,?,?,?)",
		username, passwordHash, role, at); err != nil {
		return fmt.Errorf("store: 사용자 저장 실패: %w", err)
	}
	return nil
}

// GetUser 는 tenant(ctx) 안의 사용자를 돌려준다. 없으면 ok=false(오류 아님).
func GetUser(ctx context.Context, username string) (AuthUser, bool, error) {
	d, err := conn()
	if err != nil {
		return AuthUser{}, false, err
	}
	row, err := d.QueryRow(ctx,
		"SELECT username, password_hash, role FROM auth_users WHERE username=?", username)
	if err != nil {
		return AuthUser{}, false, fmt.Errorf("store: 사용자 조회 실패: %w", err)
	}
	if row == nil {
		return AuthUser{}, false, nil
	}
	u := AuthUser{
		Username:     teamStr(row, "username"),
		Role:         teamStr(row, "role"),
		PasswordHash: teamStr(row, "password_hash"),
	}
	if u.Username == "" {
		return AuthUser{}, false, nil
	}
	return u, true, nil
}

// HasAnyUser 는 tenant(ctx) 에 사용자가 하나라도 있는지 본다(부트스트랩 판정용).
func HasAnyUser(ctx context.Context) (bool, error) {
	d, err := conn()
	if err != nil {
		return false, err
	}
	row, err := d.QueryRow(ctx, "SELECT username FROM auth_users LIMIT 1")
	if err != nil {
		return false, fmt.Errorf("store: 사용자 존재 확인 실패: %w", err)
	}
	return row != nil, nil
}

// CreateSession 은 사용자에게 세션을 발급한다. 평문 토큰은 이 반환값이 유일한 노출이고, DB 에는
// sha256 해시만 저장한다. expiresAt 은 이 세션이 만료되는 시각이다(UTC).
func CreateSession(ctx context.Context, username, role string, expiresAt time.Time) (string, error) {
	d, err := conn()
	if err != nil {
		return "", err
	}
	if username == "" {
		return "", fmt.Errorf("store: 세션 발급에 username 이 필요하다")
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("store: 세션 토큰 생성 실패: %w", err)
	}
	plain := SessionTokenPrefix + hex.EncodeToString(b)
	now := clock().UTC().Format(sessionTimeFmt)
	exp := expiresAt.UTC().Format(sessionTimeFmt)
	// pg 는 tenant_id 가 DEFAULT current_setting 이라 컬럼 목록에서 뺀다.
	if err := d.Exec(ctx,
		"INSERT INTO auth_sessions(token_hash, username, role, expires_at, created_at) VALUES(?,?,?,?,?)",
		hashToken(plain), username, role, exp, now); err != nil {
		return "", fmt.Errorf("store: 세션 저장 실패: %w", err)
	}
	return plain, nil
}

// ResolveSession 은 평문 세션 토큰을 (tenant, username, role) 로 해석한다. 만료됐거나 없으면
// ok=false(오류 아님). 만료 판정은 조회에서 함께 한다 — expires_at > now 인 행만 돌려준다.
//
// tenant 는 이 조회가 돈 컨텍스트의 테넌트다(pg 는 RLS 가 그 값으로 행을 좁혔으므로 곧 세션의
// 소유 테넌트다). 호출부가 tenant.With 로 감싼 값과 같다.
func ResolveSession(ctx context.Context, plaintext string) (tenantID, username, role string, ok bool, err error) {
	d, err := conn()
	if err != nil {
		return "", "", "", false, err
	}
	now := clock().UTC().Format(sessionTimeFmt)
	row, err := d.QueryRow(ctx,
		"SELECT username, role FROM auth_sessions WHERE token_hash=? AND expires_at > ?",
		hashToken(plaintext), now)
	if err != nil {
		return "", "", "", false, fmt.Errorf("store: 세션 해석 실패: %w", err)
	}
	if row == nil {
		return "", "", "", false, nil
	}
	u := teamStr(row, "username")
	r := teamStr(row, "role")
	if u == "" || r == "" {
		return "", "", "", false, nil
	}
	return tenant.From(ctx), u, r, true, nil
}

// DeleteSession 은 세션을 무효화한다(로그아웃 — 멱등). 없어도 오류가 아니다.
func DeleteSession(ctx context.Context, plaintext string) error {
	d, err := conn()
	if err != nil {
		return err
	}
	if err := d.Exec(ctx, "DELETE FROM auth_sessions WHERE token_hash=?", hashToken(plaintext)); err != nil {
		return fmt.Errorf("store: 세션 삭제 실패: %w", err)
	}
	return nil
}

// PurgeExpiredSessions 는 만료된 세션을 정리한다(로그인/조회 시 lazy 또는 주기 호출). 멱등.
func PurgeExpiredSessions(ctx context.Context) error {
	d, err := conn()
	if err != nil {
		return err
	}
	now := clock().UTC().Format(sessionTimeFmt)
	if err := d.Exec(ctx, "DELETE FROM auth_sessions WHERE expires_at <= ?", now); err != nil {
		return fmt.Errorf("store: 만료 세션 정리 실패: %w", err)
	}
	return nil
}
