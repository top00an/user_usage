// Package org 는 멀티테넌트 SaaS 의 org·인제스트 키 모델이다.
//
// 한 배포에 여러 org 가 격리 수용되는 코어: 각 org 는 하나의 tenant_id 를 갖고(RLS 의
// app.tenant_id 가 그 값을 본다), 인제스트 키가 그 org 로 매핑된다. 수집기·훅은 org 별 키로
// 보고하고, 게이트가 키 → tenant 로 해석해 컨텍스트에 실어 준다(server.go). 그때부터 기존
// RLS 가 격리를 강제한다.
//
// 키는 **평문으로 저장하지 않는다.** sha256(key) 만 남기고, 발급 시 1회만 평문을 돌려준다 —
// DB 가 유출돼도 키가 새지 않게. 해석은 해시로 조회하고 해지 여부를 확인한다.
package org

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/tscorp/user-usage/internal/db"
)

// KeyPrefix — 인제스트 키의 접두사. 로그·설정에서 이 문자열이 보이면 "이건 인제스트 키"임을
// 사람이 바로 안다(다른 토큰과 섞이지 않게).
const KeyPrefix = "uu_ing_"

// Org 는 한 테넌트 경계다. TenantID 가 RLS 의 app.tenant_id 로 주입된다.
type Org struct {
	ID       string
	TenantID string
	Name     string
	Status   string
}

// Init 은 org·ingest_keys 스키마를 보장한다(멱등). 양 방언 공통 문법이다 — pg 는
// migrations 가 스키마를 소유하지만, 이 DDL 은 IF NOT EXISTS 라 remote 에서도 안전하게 무시된다.
func Init(ctx context.Context, d db.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS orgs (` +
			`id TEXT PRIMARY KEY,` +
			`tenant_id TEXT NOT NULL,` +
			`name TEXT NOT NULL,` +
			`status TEXT NOT NULL DEFAULT 'active',` +
			`created_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS ingest_keys (` +
			`key_hash TEXT PRIMARY KEY,` +
			`org_id TEXT NOT NULL,` +
			`created_at TEXT NOT NULL,` +
			`revoked_at TEXT,` +
			`last_used_at TEXT)`,
		`CREATE INDEX IF NOT EXISTS ingest_keys_org ON ingest_keys(org_id)`,
	}
	for _, s := range stmts {
		if err := d.Exec(ctx, s); err != nil {
			return fmt.Errorf("org: 스키마 보장 실패: %w", err)
		}
	}
	return nil
}

// HashKey 는 평문 키를 저장용 다이제스트로 접는다. 조회·비교는 언제나 이 값으로 한다.
func HashKey(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// newPlainKey 는 KeyPrefix + 24바이트 무작위(hex)를 돌려준다.
func newPlainKey() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("org: 키 생성 실패: %w", err)
	}
	return KeyPrefix + hex.EncodeToString(b), nil
}

func newID(prefix string) (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("org: id 생성 실패: %w", err)
	}
	return prefix + hex.EncodeToString(b), nil
}

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }

// CreateOrg 는 새 org 를 만든다. tenant_id 는 org id 와 같은 값으로 둔다(org=tenant 1:1).
func CreateOrg(ctx context.Context, d db.DB, name string) (Org, error) {
	id, err := newID("org_")
	if err != nil {
		return Org{}, err
	}
	o := Org{ID: id, TenantID: id, Name: name, Status: "active"}
	err = d.Exec(ctx,
		"INSERT INTO orgs(id,tenant_id,name,status,created_at) VALUES(?,?,?,?,?)",
		o.ID, o.TenantID, o.Name, o.Status, nowUTC())
	if err != nil {
		return Org{}, fmt.Errorf("org: 생성 실패: %w", err)
	}
	return o, nil
}

// IssueKey 는 org 에 새 인제스트 키를 발급한다. 평문은 **이 반환값이 유일한 노출 지점**이다 —
// 저장은 해시만 한다.
func IssueKey(ctx context.Context, d db.DB, orgID string) (string, error) {
	row, err := d.QueryRow(ctx, "SELECT id FROM orgs WHERE id=? AND status='active'", orgID)
	if err != nil {
		return "", fmt.Errorf("org: 키 발급 중 org 조회 실패: %w", err)
	}
	if row == nil {
		return "", fmt.Errorf("org: 알 수 없는(또는 비활성) org %q", orgID)
	}
	plain, err := newPlainKey()
	if err != nil {
		return "", err
	}
	err = d.Exec(ctx,
		"INSERT INTO ingest_keys(key_hash,org_id,created_at) VALUES(?,?,?)",
		HashKey(plain), orgID, nowUTC())
	if err != nil {
		return "", fmt.Errorf("org: 키 저장 실패: %w", err)
	}
	return plain, nil
}

// ResolveIngestKey 는 평문 인제스트 키를 (tenant, orgID) 로 해석한다. 해지됐거나 없거나 org 가
// 비활성이면 ok=false. err 은 조회 자체가 실패한 경우에만(키 불일치는 err 이 아니라 ok=false).
func ResolveIngestKey(ctx context.Context, d db.DB, plaintext string) (tenant, orgID string, ok bool, err error) {
	row, err := d.QueryRow(ctx,
		"SELECT o.tenant_id AS tenant_id, k.org_id AS org_id"+
			" FROM ingest_keys k JOIN orgs o ON o.id = k.org_id"+
			" WHERE k.key_hash = ? AND k.revoked_at IS NULL AND o.status = 'active'",
		HashKey(plaintext))
	if err != nil {
		return "", "", false, fmt.Errorf("org: 키 해석 실패: %w", err)
	}
	if row == nil {
		return "", "", false, nil
	}
	tenant = str(row, "tenant_id")
	orgID = str(row, "org_id")
	if tenant == "" || orgID == "" {
		return "", "", false, nil
	}
	// last_used_at 갱신은 베스트에포트 — 실패해도 해석은 성립한다(관측용 필드).
	_ = d.Exec(ctx, "UPDATE ingest_keys SET last_used_at=? WHERE key_hash=?", nowUTC(), HashKey(plaintext))
	return tenant, orgID, true, nil
}

// RevokeKey 는 평문 키를 해지한다(멱등 — 이미 해지/없음이어도 오류 아님).
func RevokeKey(ctx context.Context, d db.DB, plaintext string) error {
	if err := d.Exec(ctx, "UPDATE ingest_keys SET revoked_at=? WHERE key_hash=? AND revoked_at IS NULL",
		nowUTC(), HashKey(plaintext)); err != nil {
		return fmt.Errorf("org: 키 해지 실패: %w", err)
	}
	return nil
}

// str 은 드라이버가 문자열을 string 으로도 []byte 로도 줄 수 있어 양쪽을 흡수한다.
func str(r db.Row, col string) string {
	switch v := r[col].(type) {
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return ""
	}
}
