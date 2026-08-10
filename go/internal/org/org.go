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
	"errors"
	"fmt"
	"time"

	"github.com/tscorp/user-usage/internal/db"
)

// ErrNotInit — 온보딩/멀티테넌트 서브시스템이 초기화되지 않았다(org.Init 미호출). 전역 핸들이
// 없으면 관리자 키 API 가 이 오류를 돌려주고, 게이트 핸들러가 이를 503 으로 접는다(400 으로
// 접으면 "잘못된 요청"으로 보여 원인이 흐려진다).
var ErrNotInit = errors.New("org: Init 되지 않았다 — 온보딩 서브시스템이 초기화되지 않았다")

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

// handle 은 게이트(httpapi)가 요청마다 db 를 넘기지 않고 키를 해석하도록 두는 패키지 전역
// 핸들이다. store·identity 와 같은 관례. Init 에서 세팅된다.
var handle db.DB

// Resolve 는 전역 핸들로 인제스트 키를 해석한다(게이트용). Init 전이면 ok=false·err.
func Resolve(ctx context.Context, plaintext string) (tenant, orgID string, ok bool, err error) {
	if handle == nil {
		return "", "", false, fmt.Errorf("org: Init 되지 않았다")
	}
	return ResolveIngestKey(ctx, handle, plaintext)
}

// Init 은 org·ingest_keys 스키마를 보장하고(멱등) 전역 핸들을 세팅한다. 양 방언 공통 문법이다 —
// pg 는 migrations 가 스키마를 소유하지만, 이 DDL 은 IF NOT EXISTS 라 remote 에서도 안전하게 무시된다.
func Init(ctx context.Context, d db.DB) error {
	handle = d
	// pg 는 스키마를 migrations 가 소유한다(store·identity 와 같은 규칙). 앱 롤은 CREATE 권한이
	// 없을 수 있으므로 DDL 을 걸지 않는다 — orgs·ingest_keys 는 migrations/pg 가 만든다.
	if d.Dialect() == db.DialectPostgres {
		return nil
	}
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

// ListOrgs 는 등록된 org 를 이름순으로 돌려준다(프로비저닝 CLI 용).
func ListOrgs(ctx context.Context, d db.DB) ([]Org, error) {
	rows, err := d.Query(ctx, "SELECT id, tenant_id, name, status FROM orgs ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("org: 목록 조회 실패: %w", err)
	}
	out := make([]Org, 0, len(rows))
	for _, r := range rows {
		out = append(out, Org{
			ID: str(r, "id"), TenantID: str(r, "tenant_id"),
			Name: str(r, "name"), Status: str(r, "status"),
		})
	}
	return out, nil
}

// RevokeKey 는 평문 키를 해지한다(멱등 — 이미 해지/없음이어도 오류 아님).
func RevokeKey(ctx context.Context, d db.DB, plaintext string) error {
	if err := d.Exec(ctx, "UPDATE ingest_keys SET revoked_at=? WHERE key_hash=? AND revoked_at IS NULL",
		nowUTC(), HashKey(plaintext)); err != nil {
		return fmt.Errorf("org: 키 해지 실패: %w", err)
	}
	return nil
}

/*
 * ── 대시보드 온보딩 헬퍼(전역 핸들 사용) ─────────────────────────────────────
 *
 * 관리자 대시보드가 요청마다 db 를 넘기지 않고 키를 발급/목록/해지하도록, Resolve 와 같은
 * 관례로 전역 handle 위에서 도는 함수들을 둔다. 셋 다 handle==nil(Init 전)이면 ErrNotInit.
 *
 * 키 식별자(ID)는 **key_hash 그 자체**다: ingest_keys 의 PK 이고, sha256 이라 목록·해지에서
 * 키를 안정적으로 가리키면서도 평문을 재구성하지 못한다(선상 이미지 저항성 — 해시를 알아도
 * 인증에 못 쓴다). 평문은 발급 응답에서 1회만 노출된다.
 */

// KeyIssued 는 발급 결과다. Plain 은 **이 값이 유일한 노출 지점**이다(저장은 해시만).
type KeyIssued struct {
	Plain     string // 평문 인제스트 키(uu_ing_…) — 1회만
	ID        string // key_hash — 목록·해지에서 키를 가리키는 안정 식별자
	CreatedAt string
}

// KeyInfo 는 목록 항목이다. **평문은 절대 담지 않는다** — Masked 만 노출한다.
type KeyInfo struct {
	ID        string
	Masked    string // KeyPrefix + '…' + key_hash 뒤 4자(사람이 행을 구분하는 용도)
	CreatedAt string
	RevokedAt string // 빈 문자열이면 미해지
}

// maskKey 는 목록 표시용 마스크다. 평문을 저장하지 않으므로 안정 재료인 key_hash 뒤 4자를 쓴다
// (앞 접두사 + 뒤 4자). 평문의 뒤 4자가 아니다 — 평문은 어디에도 남지 않는다.
func maskKey(keyHash string) string {
	tail := keyHash
	if len(tail) > 4 {
		tail = tail[len(tail)-4:]
	}
	return KeyPrefix + "…" + tail
}

// ensureOrgForTenant 는 tenant 에 대응하는 active org 를 찾고, 없으면 그 tenant 로 하나 만든다.
// CreateOrg 와 달리 tenant_id 를 **인자로 받은 tenant** 로 둔다 — 단일테넌트 기본배포(default)의
// 요청 tenant 가 그대로 org 의 tenant 가 되어야 RLS·해석이 일관된다.
func ensureOrgForTenant(ctx context.Context, d db.DB, tenant, name string) (string, error) {
	row, err := d.QueryRow(ctx,
		"SELECT id FROM orgs WHERE tenant_id=? AND status='active' ORDER BY created_at LIMIT 1", tenant)
	if err != nil {
		return "", fmt.Errorf("org: tenant org 조회 실패: %w", err)
	}
	if row != nil {
		if id := str(row, "id"); id != "" {
			return id, nil
		}
	}
	id, err := newID("org_")
	if err != nil {
		return "", err
	}
	if err := d.Exec(ctx,
		"INSERT INTO orgs(id,tenant_id,name,status,created_at) VALUES(?,?,?,?,?)",
		id, tenant, name, "active", nowUTC()); err != nil {
		return "", fmt.Errorf("org: tenant org 생성 실패: %w", err)
	}
	return id, nil
}

// IssueForTenant 는 tenant 의 org 를 보장(없으면 생성)하고 새 인제스트 키를 발급한다.
// 평문은 반환값에서만 노출된다.
func IssueForTenant(ctx context.Context, tenant, name string) (KeyIssued, error) {
	if handle == nil {
		return KeyIssued{}, ErrNotInit
	}
	orgID, err := ensureOrgForTenant(ctx, handle, tenant, name)
	if err != nil {
		return KeyIssued{}, err
	}
	plain, err := newPlainKey()
	if err != nil {
		return KeyIssued{}, err
	}
	hash := HashKey(plain)
	created := nowUTC()
	if err := handle.Exec(ctx,
		"INSERT INTO ingest_keys(key_hash,org_id,created_at) VALUES(?,?,?)",
		hash, orgID, created); err != nil {
		return KeyIssued{}, fmt.Errorf("org: 키 저장 실패: %w", err)
	}
	return KeyIssued{Plain: plain, ID: hash, CreatedAt: created}, nil
}

// ListKeys 는 tenant 소유 org 들의 인제스트 키를 발급순으로 돌려준다(평문 미포함).
func ListKeys(ctx context.Context, tenant string) ([]KeyInfo, error) {
	if handle == nil {
		return nil, ErrNotInit
	}
	rows, err := handle.Query(ctx,
		"SELECT k.key_hash AS key_hash, k.created_at AS created_at, k.revoked_at AS revoked_at"+
			" FROM ingest_keys k JOIN orgs o ON o.id = k.org_id"+
			" WHERE o.tenant_id = ? ORDER BY k.created_at", tenant)
	if err != nil {
		return nil, fmt.Errorf("org: 키 목록 조회 실패: %w", err)
	}
	out := make([]KeyInfo, 0, len(rows))
	for _, r := range rows {
		h := str(r, "key_hash")
		out = append(out, KeyInfo{
			ID: h, Masked: maskKey(h),
			CreatedAt: str(r, "created_at"), RevokedAt: str(r, "revoked_at"),
		})
	}
	return out, nil
}

// RevokeByID 는 key_hash(=ID)로 키를 해지한다(멱등). **tenant 로 스코프**를 걸어 한 tenant 의
// 관리자가 남의 tenant 키를 해지하지 못하게 한다(pg RLS 위의 방어선 한 겹 더).
func RevokeByID(ctx context.Context, tenant, id string) error {
	if handle == nil {
		return ErrNotInit
	}
	if err := handle.Exec(ctx,
		"UPDATE ingest_keys SET revoked_at=?"+
			" WHERE key_hash=? AND revoked_at IS NULL"+
			" AND org_id IN (SELECT id FROM orgs WHERE tenant_id=?)",
		nowUTC(), id, tenant); err != nil {
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
