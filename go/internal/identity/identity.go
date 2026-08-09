/*
Package identity 는 머신 → 계정 매핑 — **신원의 권위를 서버로 옮기는** 계층이다.

배경(스키마 주석 migrations/pg/0015 가 단일 출처): 팀원 PC 가 보고하는 사용자명은 기본적으로
OS 계정명이라 팀에서 쓰는 계정명과 어긋날 수 있다. 어긋난 자리를 고치려고 수집기를 재배포하고
그 PC 가 재설치하는 방식은 반복 비용이 크고, 실제로 같은 누락을 세 번 반복했다.

이 모듈이 있으면 관리자가 화면에서 한 줄 고치는 것으로 끝난다. 클라이언트는 건드리지 않는다.

적용 시점은 **쓰기(인테이크)** 다. 읽기 시점 조인을 쓰면 집계 쿼리를 전부 고쳐야 하고, 새 화면이
생길 때마다 조인을 빠뜨릴 자리가 늘어난다. 인테이크는 한 곳이라 빠뜨릴 자리가 없다.
대신 매핑을 새로 걸 때 **과거 행을 함께 재스탬프**해 소급 적용한다(restamp).
*/
package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tscorp/user-usage/internal/db"
)

const (
	machineMax = 200
	userMax    = 200
	noteMax    = 500
)

// Mapping 은 매핑 한 줄이다 — 관리 화면용.
type Mapping struct {
	Machine   string
	Username  string
	Note      string
	UpdatedBy string
	UpdatedAt string
}

// SetInput 은 매핑 설정 입력이다.
type SetInput struct {
	Machine  string
	Username string
	Note     string
	Actor    string
}

// Moved 는 소급 재스탬프로 옮겨진 행 수다.
type Moved struct {
	Sessions int
	Counters int
}

// SetResult 는 매핑 설정 결과다.
type SetResult struct {
	Machine  string
	Username string
	Moved    Moved
}

// Unmapped 한 머신 한 줄 — 관리 화면이 "고쳐야 할 후보"를 먼저 보여줄 수 있게.
type UnmappedMachine struct {
	Machine  string
	Username string
	Sessions int
}

// ErrMachineRequired·ErrUsernameRequired 는 빈 값 거부다.
//
// ⚠ 빈 username 을 거부하는 것이 이 계층의 계약이다 — 실수로 귀속을 지우지 못하게.
// 빈 값을 통과시키면 과거 행 수천 개의 username 이 한 번에 지워지고, 되돌릴 원본이 남지 않는다.
var (
	ErrMachineRequired  = errors.New("identity: machine 이 필요합니다")
	ErrUsernameRequired = errors.New("identity: username 이 필요합니다")
	ErrNotInitialized   = errors.New("identity: Init 되지 않았다")
)

var handle db.DB

func conn() (db.DB, error) {
	if handle == nil {
		return nil, ErrNotInitialized
	}
	return handle, nil
}

var clock = func() time.Time { return time.Now().UTC() }

func nowISO() string { return clock().UTC().Format("2006-01-02T15:04:05.000Z07:00") }

var sqliteDDL = []string{
	`CREATE TABLE IF NOT EXISTS machine_identity (
		machine TEXT PRIMARY KEY,
		username TEXT NOT NULL,
		note TEXT,
		updated_by TEXT,
		updated_at TEXT
	)`,
	`CREATE INDEX IF NOT EXISTS idx_machine_identity_user ON machine_identity(username)`,
}

// Init 은 매핑 표를 이 DB 에 건다. pg 는 스키마가 migrations 소유라 아무것도 하지 않는다.
func Init(ctx context.Context, d db.DB) error {
	if d == nil {
		return errors.New("identity: DB 가 nil 이다")
	}
	handle = d
	if d.Dialect() != db.DialectSQLite {
		return nil
	}
	for _, stmt := range sqliteDDL {
		if err := d.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("identity: DDL 실패: %w", err)
		}
	}
	return AuditInit(ctx, d)
}

// clip 은 앞뒤 공백을 떼고 n 자(룬)로 자른다.
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

/*
 * Resolve 는 이 머신의 귀속 계정을 돌려준다. 매핑이 없으면 빈 문자열이다
 * (호출부가 클라이언트 값을 그대로 쓴다).
 *
 * 인테이크는 절대 실패로 무너지면 안 되므로 **조회 실패도 빈 값으로 접는다.** 매핑을 못 읽은 것이
 * 사용량 보고 전체를 버리는 이유가 될 수는 없다 — 그 경우 클라이언트가 보낸 이름으로 저장되고,
 * 나중에 매핑이 복구되면 restamp 가 소급해 고친다.
 *
 * claimed 는 클라이언트가 보고한 이름이다. 매핑이 있으면 **매핑이 이긴다.**
 */
func Resolve(ctx context.Context, machine, claimed string) (string, error) {
	d, err := conn()
	if err != nil {
		return claimed, err
	}
	m := clip(machine, machineMax)
	if m == "" {
		return claimed, nil
	}
	r, err := d.QueryRow(ctx, "SELECT username FROM machine_identity WHERE machine=?", m)
	if err != nil || r == nil {
		return claimed, nil
	}
	if u := r.Str("username"); u != "" {
		return u, nil
	}
	return claimed, nil
}

// List 는 전체 매핑이다 — 관리 화면용.
func List(ctx context.Context) ([]Mapping, error) {
	d, err := conn()
	if err != nil {
		return nil, err
	}
	rows, err := d.Query(ctx,
		"SELECT machine, username, note, updated_by, updated_at FROM machine_identity ORDER BY machine")
	if err != nil {
		return nil, err
	}
	out := make([]Mapping, 0, len(rows))
	for _, r := range rows {
		out = append(out, Mapping{
			Machine: r.Str("machine"), Username: r.Str("username"), Note: r.Str("note"),
			UpdatedBy: r.Str("updated_by"), UpdatedAt: r.Str("updated_at"),
		})
	}
	return out, nil
}

/*
 * Restamp 는 이미 쌓인 행을 새 귀속으로 옮긴다.
 *
 * 이게 없으면 매핑을 걸어도 화면의 과거 데이터는 옛 이름으로 남아, 같은 사람이 두 줄로 보인다.
 * 머신으로 범위를 좁히므로 다른 사람의 행은 건드리지 않는다.
 *
 * 옮긴 행 수를 세는 방법: DB 인터페이스의 Exec 은 영향 행 수를 돌려주지 않는다(동결 계약이다).
 * 그래서 **같은 트랜잭션 안에서** 같은 WHERE 로 먼저 세고 나서 UPDATE 한다 — 트랜잭션 밖에서
 * 세면 그 사이 인테이크가 행을 더해 수가 어긋난다.
 */
func Restamp(ctx context.Context, machine, username string) (Moved, error) {
	var moved Moved
	d, err := conn()
	if err != nil {
		return moved, err
	}
	m := clip(machine, machineMax)
	u := clip(username, userMax)
	if m == "" || u == "" {
		return moved, nil
	}

	err = d.Tx(ctx, func(ctx context.Context) error {
		for _, t := range []struct {
			table string
			dst   *int
		}{{"usage_sessions", &moved.Sessions}, {"usage_counters", &moved.Counters}} {
			where := " WHERE machine=? AND (username IS NULL OR username<>?)"
			r, err := d.QueryRow(ctx, "SELECT COUNT(*) c FROM "+t.table+where, m, u)
			if err != nil {
				// best-effort — 매핑 저장 자체는 성공시킨다.
				continue
			}
			if r != nil {
				*t.dst = int(r.Int("c"))
			}
			if err := d.Exec(ctx, "UPDATE "+t.table+" SET username=?"+where, u, m, u); err != nil {
				*t.dst = 0
				continue
			}
		}
		return nil
	})
	return moved, err
}

// Set 은 매핑을 걸고(신규·갱신) 과거 행을 소급 적용한다.
func Set(ctx context.Context, in SetInput) (SetResult, error) {
	var out SetResult
	d, err := conn()
	if err != nil {
		return out, err
	}
	m := clip(in.Machine, machineMax)
	u := clip(in.Username, userMax)
	if m == "" {
		return out, ErrMachineRequired
	}
	// 공백만 있는 값도 여기서 걸린다(clip 이 TrimSpace 한다) — 실수로 귀속을 지우지 못하게.
	if u == "" {
		return out, ErrUsernameRequired
	}

	conflict := "(machine)"
	if d.Dialect() == db.DialectPostgres {
		conflict = "(tenant_id, machine)"
	}
	err = d.Exec(ctx,
		"INSERT INTO machine_identity(machine,username,note,updated_by,updated_at) VALUES(?,?,?,?,?)"+
			" ON CONFLICT "+conflict+" DO UPDATE SET username=excluded.username, note=excluded.note,"+
			" updated_by=excluded.updated_by, updated_at=excluded.updated_at",
		m, u, nullStr(clip(in.Note, noteMax)), nullStr(clip(in.Actor, userMax)), nowISO())
	if err != nil {
		return out, err
	}
	moved, err := Restamp(ctx, m, u)
	if err != nil {
		return SetResult{Machine: m, Username: u}, err
	}
	return SetResult{Machine: m, Username: u, Moved: moved}, nil
}

/*
 * Remove 는 매핑을 해제한다. 과거 행은 **되돌리지 않는다.**
 *
 * 되돌릴 원래 값이 남아 있지 않고(재스탬프가 덮었다), 해제의 의도는 보통 "앞으로는 클라이언트
 * 값을 쓰겠다"이지 "과거를 옛 이름으로 되돌리겠다"가 아니다. 되돌리려면 다른 매핑을 걸면 된다.
 */
func Remove(ctx context.Context, machine string) (bool, error) {
	d, err := conn()
	if err != nil {
		return false, err
	}
	m := clip(machine, machineMax)
	if m == "" {
		return false, nil
	}
	// Exec 이 영향 행 수를 주지 않으므로 있었는지 먼저 본다(같은 트랜잭션 안에서).
	existed := false
	err = d.Tx(ctx, func(ctx context.Context) error {
		r, err := d.QueryRow(ctx, "SELECT 1 one FROM machine_identity WHERE machine=?", m)
		if err != nil {
			return err
		}
		existed = r != nil
		return d.Exec(ctx, "DELETE FROM machine_identity WHERE machine=?", m)
	})
	if err != nil {
		return false, err
	}
	return existed, nil
}

// Unmapped 는 아직 매핑이 없는 머신들이다 — 사용량에 등장한 머신 중 매핑 표에 없는 것들.
func Unmapped(ctx context.Context) ([]UnmappedMachine, error) {
	d, err := conn()
	if err != nil {
		return nil, err
	}
	rows, err := d.Query(ctx,
		"SELECT s.machine, MIN(s.username) username, COUNT(*) sessions FROM usage_sessions s"+
			" LEFT JOIN machine_identity m ON m.machine = s.machine"+
			" WHERE s.machine IS NOT NULL AND m.machine IS NULL"+
			" GROUP BY s.machine ORDER BY sessions DESC, s.machine DESC")
	if err != nil {
		return nil, err
	}
	out := make([]UnmappedMachine, 0, len(rows))
	for _, r := range rows {
		u := r.Str("username")
		if u == "" {
			u = "(미상)"
		}
		out = append(out, UnmappedMachine{
			Machine: r.Str("machine"), Username: u, Sessions: int(r.Int("sessions")),
		})
	}
	return out, nil
}
