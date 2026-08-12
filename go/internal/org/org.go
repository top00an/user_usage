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
	"strings"
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

/*
 * KeyUsername 은 **이미 해석에 성공한** 키에 묶인 사용자명을 돌려준다. 묶이지 않은
 * (레거시·org 공용) 키면 빈 문자열이고, 그것이 오류가 아니다 — 하위호환의 정상 경로다.
 *
 * 왜 Resolve 와 따로 있나: 게이트의 해석 지점(httpapi 의 resolveIngestKey)은 "접두사 없는 Bearer 가
 * DB 를 몇 번 때렸는가"를 세는 훅이 걸린 자리라 시그니처가 계약이다(ingestkey_prefix_test.go).
 * 귀속에 필요한 한 컬럼 때문에 그 계약을 흔드는 대신, **인증에 성공한 뒤에만** 도는 조회를
 * 따로 둔다 — 무인증 브루트포스는 여기까지 오지 못하므로 증폭 표면이 늘지 않는다.
 */
func KeyUsername(ctx context.Context, plaintext string) (string, error) {
	if handle == nil {
		return "", ErrNotInit
	}
	row, err := handle.QueryRow(ctx,
		"SELECT username FROM ingest_keys WHERE key_hash=? AND revoked_at IS NULL", HashKey(plaintext))
	if err != nil {
		return "", fmt.Errorf("org: 키 사용자 조회 실패: %w", err)
	}
	if row == nil {
		return "", nil
	}
	return str(row, "username"), nil
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
			`last_used_at TEXT,` +
			// username — 이 키가 묶인 사람. **nullable 이 하위호환의 전부다**: 기존 키는 NULL 이고
			// 그 키의 보고는 종전대로 identity.Resolve 를 탄다(귀속 우선순위 ②→③).
			`username TEXT)`,
		`CREATE INDEX IF NOT EXISTS ingest_keys_org ON ingest_keys(org_id)`,
	}
	for _, s := range stmts {
		if err := d.Exec(ctx, s); err != nil {
			return fmt.Errorf("org: 스키마 보장 실패: %w", err)
		}
	}
	/*
	 * ⚠ 위 `CREATE TABLE IF NOT EXISTS` 는 **기존 표에 새 컬럼을 넣지 않는다.** username 은
	 * 나중에 생긴 컬럼이라, 이 보강이 없으면 옛 DB 로 뜬 서버에서 키 해석 질의가 통째로 실패한다
	 * (= 전 팀원의 보고가 401). store.Init 이 usage_sessions 에 하는 것과 같은 규율이고,
	 * 그 함정의 단일 출처는 store/init_test.go 의 TestInitAddsMissingColumnsToOldDatabase 다.
	 *
	 * 인덱스도 **컬럼 보강 뒤**에 건다 — 옛 DB 에는 그 시점에 컬럼이 없어 CREATE INDEX 가
	 * 부팅을 죽인다(store.Init 의 platform 인덱스가 같은 이유로 여기 있다).
	 */
	if err := ensureColumn(ctx, d, "ingest_keys", "username", "TEXT"); err != nil {
		return err
	}
	if err := d.Exec(ctx,
		"CREATE INDEX IF NOT EXISTS ingest_keys_username ON ingest_keys(username)"); err != nil {
		return fmt.Errorf("org: username 인덱스 생성 실패: %w", err)
	}
	return nil
}

// ensureColumn 은 sqlite 전용 멱등 컬럼 추가다(store.ensureColumn 과 같은 패턴 — 방언이 달라
// 공유하지 않는다). 이미 있으면 아무 일도 하지 않는다.
func ensureColumn(ctx context.Context, d db.DB, table, column, decl string) error {
	rows, err := d.Query(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return fmt.Errorf("org: %s 컬럼 조회 실패: %w", table, err)
	}
	for _, r := range rows {
		if strings.EqualFold(r.Str("name"), column) {
			return nil
		}
	}
	if err := d.Exec(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, decl)); err != nil {
		return fmt.Errorf("org: %s.%s 추가 실패: %w", table, column, err)
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

// IssueKey 는 org 에 새 인제스트 키를 발급한다(사용자에 묶지 않는다 — org 공용). 평문은
// **이 반환값이 유일한 노출 지점**이다 — 저장은 해시만 한다.
func IssueKey(ctx context.Context, d db.DB, orgID string) (string, error) {
	return IssueKeyFor(ctx, d, orgID, "")
}

/*
 * IssueKeyFor 는 org 에 새 인제스트 키를 발급하고 **그 키를 username 에 묶는다**(CLI 경로).
 *
 * username 이 비면 종전과 같은 org 공용 키다(컬럼은 NULL). 묶인 키로 들어온 보고는 인테이크가
 * payload.user·machine 매핑을 타지 않고 이 이름으로 귀속한다 — "그 사용자에게 발급된 키를 실제로
 * 갖고 있음"이 증명된 사실이기 때문이다(귀속 우선순위 ①).
 */
func IssueKeyFor(ctx context.Context, d db.DB, orgID, username string) (string, error) {
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
		"INSERT INTO ingest_keys(key_hash,org_id,created_at,username) VALUES(?,?,?,?)",
		HashKey(plain), orgID, nowUTC(), nullStr(username))
	if err != nil {
		return "", fmt.Errorf("org: 키 저장 실패: %w", err)
	}
	return plain, nil
}

// ResolvedKey 는 인제스트 키 해석 결과다. Username 은 **키에 묶인 사람**이고, 레거시·org 공용
// 키에서는 빈 문자열이다(그 경우 귀속은 종전대로 identity 매핑 → payload 를 탄다).
type ResolvedKey struct {
	Tenant   string
	OrgID    string
	Username string
}

// ResolveIngestKey 는 평문 인제스트 키를 (tenant, orgID) 로 해석한다. 해지됐거나 없거나 org 가
// 비활성이면 ok=false. err 은 조회 자체가 실패한 경우에만(키 불일치는 err 이 아니라 ok=false).
//
// 키에 묶인 username 까지 필요하면 ResolveIngestKeyDetail 을 쓴다 — 이 함수는 기존 호출부
// (프로비저닝 CLI·게이트)의 시그니처를 그대로 유지하기 위한 얇은 래퍼다.
func ResolveIngestKey(ctx context.Context, d db.DB, plaintext string) (tenant, orgID string, ok bool, err error) {
	rk, ok, err := ResolveIngestKeyDetail(ctx, d, plaintext)
	return rk.Tenant, rk.OrgID, ok, err
}

// ResolveIngestKeyDetail 은 평문 키를 (tenant, orgID, username) 으로 해석한다. 판정 규칙은
// ResolveIngestKey 와 같다 — 해지·미지·비활성 org 는 ok=false.
func ResolveIngestKeyDetail(ctx context.Context, d db.DB, plaintext string) (ResolvedKey, bool, error) {
	row, err := d.QueryRow(ctx,
		"SELECT o.tenant_id AS tenant_id, k.org_id AS org_id, k.username AS username"+
			" FROM ingest_keys k JOIN orgs o ON o.id = k.org_id"+
			" WHERE k.key_hash = ? AND k.revoked_at IS NULL AND o.status = 'active'",
		HashKey(plaintext))
	if err != nil {
		return ResolvedKey{}, false, fmt.Errorf("org: 키 해석 실패: %w", err)
	}
	if row == nil {
		return ResolvedKey{}, false, nil
	}
	rk := ResolvedKey{
		Tenant:   str(row, "tenant_id"),
		OrgID:    str(row, "org_id"),
		Username: str(row, "username"),
	}
	if rk.Tenant == "" || rk.OrgID == "" {
		return ResolvedKey{}, false, nil
	}
	// last_used_at 갱신은 베스트에포트 — 실패해도 해석은 성립한다(관측용 필드).
	_ = d.Exec(ctx, "UPDATE ingest_keys SET last_used_at=? WHERE key_hash=?", nowUTC(), HashKey(plaintext))
	return rk, true, nil
}

// nullStr 은 빈 문자열을 SQL NULL 로 접는다 — "묶이지 않은 키"는 빈 문자열이 아니라 NULL 이다
// (identity.nullStr 과 같은 관례).
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
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
	// Username 은 이 키가 묶인 사람이다(빈 문자열이면 org 공용 키).
	Username string
}

// KeyInfo 는 목록 항목이다. **평문은 절대 담지 않는다** — Masked 만 노출한다.
type KeyInfo struct {
	ID        string
	Masked    string // KeyPrefix + '…' + key_hash 뒤 4자(사람이 행을 구분하는 용도)
	CreatedAt string
	RevokedAt string // 빈 문자열이면 미해지
	// Username 은 이 키가 묶인 사람이다. 빈 문자열이면 org 공용(레거시) 키다 — 그 키의 보고는
	// 종전대로 machine 매핑 → payload 를 탄다.
	Username string
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

// IssueForTenant 는 tenant 의 org 를 보장(없으면 생성)하고 새 인제스트 키를 발급한다(사용자에
// 묶지 않는다 — org 공용). 평문은 반환값에서만 노출된다.
func IssueForTenant(ctx context.Context, tenant, name string) (KeyIssued, error) {
	return IssueForTenantUser(ctx, tenant, name, "")
}

// IssueForTenantUser 는 IssueForTenant 와 같되 발급한 키를 **username 에 묶는다**(셀프서비스·
// 관리자 대리발급이 쓰는 자리). username 이 비면 종전과 같은 org 공용 키다.
func IssueForTenantUser(ctx context.Context, tenant, name, username string) (KeyIssued, error) {
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
		"INSERT INTO ingest_keys(key_hash,org_id,created_at,username) VALUES(?,?,?,?)",
		hash, orgID, created, nullStr(username)); err != nil {
		return KeyIssued{}, fmt.Errorf("org: 키 저장 실패: %w", err)
	}
	return KeyIssued{Plain: plain, ID: hash, CreatedAt: created, Username: username}, nil
}

// ListKeys 는 tenant 소유 org 들의 인제스트 키를 발급순으로 돌려준다(평문 미포함). **관리자용**
// 전체 현황이다 — 셀프서비스 목록은 ListKeysForUser 를 쓴다.
func ListKeys(ctx context.Context, tenant string) ([]KeyInfo, error) {
	return listKeys(ctx, tenant, "")
}

/*
 * ListKeysForUser 는 tenant 안에서 **그 사용자에게 묶인 키만** 돌려준다(셀프서비스 목록).
 *
 * username 이 비면 아무것도 돌려주지 않는다 — 빈 이름이 "묶이지 않은 키 전부"로 흘러 남의
 * 키가 목록에 뜨는 자리를 만들지 않기 위해서다(동결 ②: 남의 키는 보이지도 않는다).
 */
func ListKeysForUser(ctx context.Context, tenant, username string) ([]KeyInfo, error) {
	if username == "" {
		return []KeyInfo{}, nil
	}
	return listKeys(ctx, tenant, username)
}

func listKeys(ctx context.Context, tenant, username string) ([]KeyInfo, error) {
	if handle == nil {
		return nil, ErrNotInit
	}
	q := "SELECT k.key_hash AS key_hash, k.created_at AS created_at," +
		" k.revoked_at AS revoked_at, k.username AS username" +
		" FROM ingest_keys k JOIN orgs o ON o.id = k.org_id" +
		" WHERE o.tenant_id = ?"
	args := []any{tenant}
	if username != "" {
		q += " AND k.username = ?"
		args = append(args, username)
	}
	q += " ORDER BY k.created_at"
	rows, err := handle.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("org: 키 목록 조회 실패: %w", err)
	}
	out := make([]KeyInfo, 0, len(rows))
	for _, r := range rows {
		h := str(r, "key_hash")
		out = append(out, KeyInfo{
			ID: h, Masked: maskKey(h),
			CreatedAt: str(r, "created_at"), RevokedAt: str(r, "revoked_at"),
			Username: str(r, "username"),
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

/*
 * RevokeByIDForUser 는 **자기 키만** 해지한다(셀프서비스). owned=false 는 "그 tenant 안에서
 * 그 사용자에게 묶인 그 id 의 키가 없다"는 뜻이다 — 남의 키든 없는 키든 결과가 같아야 한다.
 * 호출부가 둘을 다른 상태코드로 갈라 주면 그 차이가 곧 "그 키는 존재한다"는 신호가 된다.
 *
 * 소유 판정을 UPDATE 의 WHERE 에 맡기지 않고 먼저 SELECT 하는 이유: db.DB 에 영향 행 수를
 * 돌려주는 자리가 없어(Exec 은 error 만) UPDATE 만으로는 "해지됐는가"를 알 수 없다. 이미
 * 해지된 키를 다시 해지해도 owned=true 다(멱등).
 */
func RevokeByIDForUser(ctx context.Context, tenant, username, id string) (owned bool, err error) {
	if handle == nil {
		return false, ErrNotInit
	}
	if username == "" || id == "" {
		return false, nil
	}
	row, err := handle.QueryRow(ctx,
		"SELECT k.key_hash AS key_hash FROM ingest_keys k JOIN orgs o ON o.id = k.org_id"+
			" WHERE k.key_hash=? AND k.username=? AND o.tenant_id=?", id, username, tenant)
	if err != nil {
		return false, fmt.Errorf("org: 키 소유 확인 실패: %w", err)
	}
	if row == nil {
		return false, nil
	}
	if err := handle.Exec(ctx,
		"UPDATE ingest_keys SET revoked_at=? WHERE key_hash=? AND revoked_at IS NULL",
		nowUTC(), id); err != nil {
		return true, fmt.Errorf("org: 키 해지 실패: %w", err)
	}
	return true, nil
}

/*
 * ── 사람 단위 키 정리 (보안검토 M-1) ─────────────────────────────────────────
 *
 * 세션은 이름으로 끊을 수 있는데(store.DeleteSessionsForUser) 키는 그럴 수 없던 것이 M-1 이다.
 * 계정을 지워도 그 사람의 결속 키는 `revoked_at IS NULL` 로 살아남아 POST /api/usage 를 계속
 * 통과했고, **삭제된 이름으로 귀속을 계속 만들었다.** 퇴사 처리와 침해 대응이 반쪽이었다는 뜻이다.
 *
 * 두 함수의 역할이 다르다:
 *   · RevokeAllForUser        — 거둔다 (계정 삭제)
 *   · CountActiveKeysForUser  — 세기만 한다 (비밀번호 재설정)
 *
 * 왜 재설정은 거두지 않는가: 재설정의 이유 절반은 단순 분실이고, 거기서 키까지 조용히 죽이면
 * 그 사람의 수집기가 말없이 멈춘다(응답에는 아무 신호가 없다 — 이 레포가 가장 비싸다고 한
 * 사고 유형 그대로다). 대신 살아 있는 키 수를 응답에 실어 **화면이 "이 사람 키 N개가 아직
 * 살아 있습니다 — 회전하시겠습니까"를 띄울 근거**를 준다. 침해 대응은 그 안내를 보고 사람이
 * 명시적으로 해지한다.
 */

// CountActiveKeysForUser 는 tenant 안에서 그 사용자에게 묶인 **미해지** 키 수다.
// username 이 비면 0 이다 — 빈 이름이 "묶이지 않은 키 전부"로 흐르는 자리를 만들지 않는다
// (ListKeysForUser 와 같은 규율).
func CountActiveKeysForUser(ctx context.Context, tenant, username string) (int, error) {
	if handle == nil {
		return 0, ErrNotInit
	}
	if username == "" {
		return 0, nil
	}
	rows, err := handle.Query(ctx, activeKeysForUserQuery, username, tenant)
	if err != nil {
		return 0, fmt.Errorf("org: 살아 있는 키 조회 실패: %w", err)
	}
	return len(rows), nil
}

/*
 * RevokeAllForUser 는 tenant 안에서 그 사용자에게 묶인 **미해지 키를 전부** 해지하고 해지한
 * 개수를 돌려준다(멱등 — 두 번 불러도 두 번째는 0 이다).
 *
 * 세고 나서 지우는 두 문장을 한 트랜잭션에 넣는 이유는 두 가지다: ①db.DB 의 Exec 이 영향 행 수를
 * 돌려주지 않아 "몇 개를 거뒀는가"를 세는 문장이 따로 필요하고(org.go:468 이 같은 제약을 적어 뒀다)
 * ②그 사이에 셀프서비스 발급이 끼어들면 응답의 개수가 실제와 달라진다. 응답에 실릴 수라 틀리면
 * 안 된다 — 그 수가 곧 "무엇을 거뒀는가"의 증거다.
 *
 * tenant 스코프를 UPDATE 에도 그대로 건다 — 한 tenant 의 관리자가 동명이인의 남의 tenant 키를
 * 거두지 못하게(pg RLS 위의 방어선 한 겹 더, RevokeByID 와 같은 규율).
 */
func RevokeAllForUser(ctx context.Context, tenant, username string) (int, error) {
	if handle == nil {
		return 0, ErrNotInit
	}
	if username == "" || tenant == "" {
		return 0, nil
	}
	n := 0
	err := handle.Tx(ctx, func(ctx context.Context) error {
		rows, err := handle.Query(ctx, activeKeysForUserQuery, username, tenant)
		if err != nil {
			return fmt.Errorf("org: 해지 대상 키 조회 실패: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		if err := handle.Exec(ctx,
			"UPDATE ingest_keys SET revoked_at=?"+
				" WHERE username=? AND revoked_at IS NULL"+
				" AND org_id IN (SELECT id FROM orgs WHERE tenant_id=?)",
			nowUTC(), username, tenant); err != nil {
			return fmt.Errorf("org: 사용자 키 해지 실패: %w", err)
		}
		n = len(rows)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// activeKeysForUserQuery — 그 사용자에게 묶인 미해지 키. 세는 쪽과 거두는 쪽이 **같은 조건**을
// 봐야 응답의 개수가 실제로 거둔 수와 일치한다. 그래서 문자열을 한 곳에 둔다.
const activeKeysForUserQuery = "SELECT k.key_hash AS key_hash FROM ingest_keys k" +
	" JOIN orgs o ON o.id = k.org_id" +
	" WHERE k.username = ? AND k.revoked_at IS NULL AND o.tenant_id = ?"

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
