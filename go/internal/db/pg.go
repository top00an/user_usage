package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
)

/*
 * PostgreSQL 백엔드 — pgx/v5 위에 아주 얇은 커넥션 보관함을 얹었다.
 *
 * ⚠ 왜 pgxpool(또는 database/sql + pgx stdlib)이 아닌가 — 이건 취향이 아니라 **소유권 제약**이다.
 *   그 둘은 모두 github.com/jackc/puddle/v2 를 끌어오는데, 이 포팅에서 go.mod·go.sum 은 PM
 *   소유다. 오너가 의존을 조용히 늘리면 네 명이 같은 워킹트리에서 부딪힌다. 그래서 이미 잠긴
 *   기본 pgx 만으로 구현한다.
 *   PM 이 go.sum 에 puddle 을 추가해 주면 이 파일은 pgxpool 로 갈아타는 편이 낫다 —
 *   **보관함이 아니라 검증된 풀을 쓰는 쪽이 맞다.** (done 보고서에 남긴다.)
 *
 * 보관함이 하는 일은 셋뿐이다: 동시 커넥션 상한 · 유휴 커넥션 재사용 · 고장난 커넥션 폐기.
 * 대기 큐·헬스체크·수명 관리는 넣지 않았다 — 넣기 시작하면 그게 곧 풀을 다시 쓰는 것이고,
 * 검증되지 않은 풀은 부하가 있을 때만 틀린다.
 */

type pgDB struct {
	url string

	mu     sync.Mutex
	idle   []*pgx.Conn
	closed bool

	// sem 은 동시 커넥션 상한이다. 버퍼 자리를 잡아야 커넥션을 만들 수 있다.
	sem chan struct{}
}

// defaultPoolMax 는 현행 lib/db/pg.js 의 기본값(10)과 같다.
const defaultPoolMax = 10

var poolMax = defaultPoolMax

// SetPoolMax 는 remote 모드의 동시 커넥션 상한을 정한다. Open 전에 부른다.
func SetPoolMax(n int) {
	if n > 0 {
		poolMax = n
	}
}

// pgTenant 는 pg 경로가 매 쿼리에서 읽을 테넌트 해석기다. 기본은 "default".
//
// tenant 패키지를 직접 import 하지 않는 이유: 그 컨텍스트 키가 비공개라 값을 읽으려면
// tenant.From 을 불러야 하는데, 그러면 db 패키지의 단위 테스트가 tenant 를 끌고 온다.
// 대신 부팅에서 한 번 주입한다.
var pgTenant = func(ctx context.Context) string { return "default" }

// SetTenantResolver 는 테넌트 해석기를 건다.
// 부팅에서 `db.SetTenantResolver(tenant.From)` 를 **반드시** 부른다 — 걸지 않으면 모든 쿼리가
// "default" 로 흐르는데, remote 로 남의 DB 를 볼 때 그게 맞는다는 보장이 없다.
func SetTenantResolver(fn func(context.Context) string) {
	if fn != nil {
		pgTenant = fn
	}
}

func openPostgres(_ context.Context, cfg Options) (DB, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, errors.New("db: remote 모드인데 DATABASE_URL 이 비었다")
	}
	// 연결은 지연한다 — 부팅 시점에 터널이 아직 안 뚫렸을 수 있고, 그건 정상 절차다.
	return &pgDB{url: cfg.URL, sem: make(chan struct{}, poolMax)}, nil
}

func (d *pgDB) Dialect() Dialect { return DialectPostgres }

func (d *pgDB) Close() error {
	d.mu.Lock()
	conns := d.idle
	d.idle = nil
	d.closed = true
	d.mu.Unlock()
	for _, c := range conns {
		_ = c.Close(context.Background())
	}
	return nil
}

// acquire 는 커넥션 한 자루를 빌린다. 반드시 release 와 짝을 이룬다.
func (d *pgDB) acquire(ctx context.Context) (*pgx.Conn, error) {
	select {
	case d.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		<-d.sem
		return nil, errors.New("db: 이미 닫힌 백엔드다")
	}
	for len(d.idle) > 0 {
		c := d.idle[len(d.idle)-1]
		d.idle = d.idle[:len(d.idle)-1]
		if !c.IsClosed() {
			d.mu.Unlock()
			return c, nil
		}
		// 죽은 커넥션은 조용히 버린다(네트워크가 끊겼다 붙는 정상 경로다).
	}
	d.mu.Unlock()

	c, err := pgx.Connect(ctx, d.url)
	if err != nil {
		<-d.sem
		return nil, err
	}
	return c, nil
}

// release 는 커넥션을 돌려준다. bad 면 재사용하지 않고 닫는다 —
// 오류가 난 커넥션은 트랜잭션 상태가 어떤지 알 수 없고, 그걸 다시 쓰면 다음 요청이 대신 죽는다.
func (d *pgDB) release(c *pgx.Conn, bad bool) {
	defer func() { <-d.sem }()
	if c == nil {
		return
	}
	if bad || c.IsClosed() {
		_ = c.Close(context.Background())
		return
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		_ = c.Close(context.Background())
		return
	}
	d.idle = append(d.idle, c)
	d.mu.Unlock()
}

/*
 * withTenantTx — 테넌트를 세팅한 커넥션 하나에서 fn 을 트랜잭션으로 돌린다.
 *
 *   BEGIN → SELECT set_config('app.tenant_id', <tenant>, true) → 본문 → COMMIT
 *
 * set_config(..., true) 는 LOCAL 이라 트랜잭션이 끝나면 자동 리셋된다 → 보관함에 돌아간
 * 커넥션이 남의 테넌트를 들고 있는 누수가 없다. 이 한 줄이 RLS 격리의 전부이므로 **모든**
 * 쿼리 경로가 여기를 지난다.
 */
func (d *pgDB) withTenantTx(ctx context.Context, fn func(context.Context, pgx.Tx) error) (err error) {
	if tx, ok := ctx.Value(pgTxKey{}).(pgx.Tx); ok && tx != nil {
		return fn(ctx, tx) // 중첩은 바깥 트랜잭션을 그대로 쓴다(테넌트는 이미 걸려 있다)
	}

	c, err := d.acquire(ctx)
	if err != nil {
		return err
	}
	bad := false
	defer func() { d.release(c, bad) }()

	tx, err := c.Begin(ctx)
	if err != nil {
		bad = true
		return err
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", pgTenant(ctx)); err != nil {
		_ = tx.Rollback(ctx)
		bad = true
		return err
	}
	if err := fn(context.WithValue(ctx, pgTxKey{}, tx), tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		bad = true
		return err
	}
	return nil
}

// pgTxKey 는 컨텍스트에 실린 pgx 트랜잭션의 키다.
type pgTxKey struct{}

func (d *pgDB) Query(ctx context.Context, query string, args ...any) ([]Row, error) {
	text := ToPg(query)
	var out []Row
	err := d.withTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, text, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		out, err = scanPgRows(rows)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (d *pgDB) QueryRow(ctx context.Context, query string, args ...any) (Row, error) {
	out, err := d.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out[0], nil
}

func (d *pgDB) Exec(ctx context.Context, query string, args ...any) error {
	text := ToPg(query)
	return d.withTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, text, args...)
		return err
	})
}

func (d *pgDB) Tx(ctx context.Context, fn func(context.Context) error) error {
	return d.withTenantTx(ctx, func(ctx context.Context, _ pgx.Tx) error { return fn(ctx) })
}

/*
 * ExecRaw 는 멀티 스테이트먼트 원시 실행이다(마이그레이션 러너 전용).
 * 테넌트를 주입하지 않는다 — 스키마 변경은 테넌트 밖의 일이다.
 *
 * 인자가 없는 pgx Exec 는 simple protocol 로 나가고, 그래야 여러 문장과 `DO $$ … $$` 블록이
 * 한 번에 통과한다. 세미콜론으로 쪼개는 방식은 달러 인용부호 안에서 곧바로 틀린다.
 */
func (d *pgDB) ExecRaw(ctx context.Context, sqlText string) error {
	c, err := d.acquire(ctx)
	if err != nil {
		return err
	}
	bad := false
	defer func() { d.release(c, bad) }()
	if _, err := c.Exec(ctx, sqlText); err != nil {
		bad = true
		return fmt.Errorf("db: 원시 실행 실패: %w", err)
	}
	return nil
}

func scanPgRows(rows pgx.Rows) ([]Row, error) {
	fds := rows.FieldDescriptions()
	names := make([]string, len(fds))
	for i, f := range fds {
		names[i] = strings.ToLower(f.Name)
	}
	out := []Row{}
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		r := make(Row, len(names))
		for i, name := range names {
			if i >= len(vals) {
				break
			}
			if b, ok := vals[i].([]byte); ok {
				r[name] = string(b)
				continue
			}
			r[name] = vals[i]
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
