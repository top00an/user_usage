package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// MemberTokenPrefix — 개인 열람 토큰 접두사(로그·설정에서 다른 토큰과 구분).
const MemberTokenPrefix = "uu_view_"

// hashToken 은 평문 토큰을 저장용 sha256 다이제스트로 접는다(조회·비교는 이 값으로).
func hashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// IssueMemberToken 은 username 에 개인 열람 토큰을 발급한다. 평문은 이 반환값이 유일한 노출.
func IssueMemberToken(ctx context.Context, username string) (string, error) {
	d, err := conn()
	if err != nil {
		return "", err
	}
	if username == "" {
		return "", fmt.Errorf("store: member 토큰 발급에 username 이 필요하다")
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("store: member 토큰 생성 실패: %w", err)
	}
	plain := MemberTokenPrefix + hex.EncodeToString(b)
	at := time.Now().UTC().Format(time.RFC3339)
	// pg 는 tenant_id 가 DEFAULT current_setting 이라 컬럼 목록에서 뺀다(방언 공통).
	if err := d.Exec(ctx,
		"INSERT INTO member_tokens(token_hash, username, created_at) VALUES(?,?,?)",
		hashToken(plain), username, at); err != nil {
		return "", fmt.Errorf("store: member 토큰 저장 실패: %w", err)
	}
	return plain, nil
}

// ResolveMemberToken 은 평문 토큰을 username 으로 해석한다. 해지/미존재면 ok=false.
// err 은 조회 자체 실패에만(불일치는 ok=false).
func ResolveMemberToken(ctx context.Context, plaintext string) (username string, ok bool, err error) {
	d, err := conn()
	if err != nil {
		return "", false, err
	}
	row, err := d.QueryRow(ctx,
		"SELECT username FROM member_tokens WHERE token_hash=? AND revoked_at IS NULL",
		hashToken(plaintext))
	if err != nil {
		return "", false, fmt.Errorf("store: member 토큰 해석 실패: %w", err)
	}
	if row == nil {
		return "", false, nil
	}
	u := teamStr(row, "username")
	if u == "" {
		return "", false, nil
	}
	return u, true, nil
}

// RevokeMemberToken 은 평문 토큰을 해지한다(멱등).
func RevokeMemberToken(ctx context.Context, plaintext string) error {
	d, err := conn()
	if err != nil {
		return err
	}
	at := time.Now().UTC().Format(time.RFC3339)
	if err := d.Exec(ctx,
		"UPDATE member_tokens SET revoked_at=? WHERE token_hash=? AND revoked_at IS NULL",
		at, hashToken(plaintext)); err != nil {
		return fmt.Errorf("store: member 토큰 해지 실패: %w", err)
	}
	return nil
}

// MemberToken 은 목록 조회용 한 행이다(토큰 해시는 노출하지 않는다).
type MemberToken struct {
	Username string
	Revoked  bool
}

// ListMemberTokens 는 발급된 개인 토큰 목록이다(username 순, 평문·해시 미노출).
func ListMemberTokens(ctx context.Context) ([]MemberToken, error) {
	d, err := conn()
	if err != nil {
		return nil, err
	}
	rows, err := d.Query(ctx, "SELECT username, revoked_at FROM member_tokens ORDER BY username")
	if err != nil {
		return nil, err
	}
	out := make([]MemberToken, 0, len(rows))
	for _, r := range rows {
		out = append(out, MemberToken{
			Username: teamStr(r, "username"),
			Revoked:  teamStr(r, "revoked_at") != "",
		})
	}
	return out, nil
}
