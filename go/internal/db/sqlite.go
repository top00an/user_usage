package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // 순수 Go 드라이버 — CGO 가 필요 없다(배포가 단일 바이너리로 끝난다).
)

// txKey 는 컨텍스트에 실린 트랜잭션 핸들의 키다. 비공개라 밖에서 만들 수 없다.
type txKey struct{}

// sqliteQueryer 는 *sql.DB 와 *sql.Tx 의 공통 표면이다.
type sqliteQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type sqliteDB struct {
	db   *sql.DB
	file string
}

// MemoryDataDir 를 DataDir 로 주면 파일 대신 메모리 DB 를 연다(테스트 전용).
// 파일 경로와 섞이지 않게 실제 디렉터리로 쓰일 수 없는 이름을 골랐다.
const MemoryDataDir = ":memory:"

func openSQLite(ctx context.Context, cfg Options) (DB, error) {
	dsn := ""
	file := ""
	if cfg.DataDir == MemoryDataDir {
		// 공유 캐시 메모리 DB — database/sql 의 커넥션 재생성에도 같은 DB 를 본다.
		dsn = "file::memory:?cache=shared"
		file = MemoryDataDir
	} else {
		dir := cfg.DataDir
		if dir == "" {
			dir = "data"
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return nil, fmt.Errorf("db: 데이터 디렉터리 경로 해석 실패: %w", err)
		}
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return nil, fmt.Errorf("db: 데이터 디렉터리 생성 실패: %w", err)
		}
		file = filepath.Join(abs, "usage.db")
		dsn = file
	}

	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: sqlite 열기 실패: %w", err)
	}
	/*
	 * 커넥션 하나로 고정한다.
	 *
	 * sqlite 는 단일 파일 단일 라이터다. 풀을 여러 개로 두면 같은 프로세스 안에서 스스로
	 * SQLITE_BUSY 를 만들고, 그 실패는 부하가 있을 때만 나타나 재현이 어렵다. 이 서비스의
	 * 로컬 모드는 팀 하나가 쓰는 관측 서버라 동시성으로 얻을 것이 없다.
	 */
	sdb.SetMaxOpenConns(1)

	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL;",
		// 같은 파일을 여는 커넥션이 둘 이상일 수 있다(서버 + 점검용 CLI). 즉시 실패 대신 대기.
		"PRAGMA busy_timeout = 5000;",
	} {
		if _, err := sdb.ExecContext(ctx, pragma); err != nil {
			_ = sdb.Close()
			return nil, fmt.Errorf("db: sqlite pragma 실패(%s): %w", pragma, err)
		}
	}
	return &sqliteDB{db: sdb, file: file}, nil
}

func (d *sqliteDB) Dialect() Dialect { return DialectSQLite }
func (d *sqliteDB) Close() error     { return d.db.Close() }

func (d *sqliteDB) conn(ctx context.Context) sqliteQueryer {
	if tx, ok := ctx.Value(txKey{}).(*sql.Tx); ok && tx != nil {
		return tx
	}
	return d.db
}

func (d *sqliteDB) Query(ctx context.Context, query string, args ...any) ([]Row, error) {
	rows, err := d.conn(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRows(rows)
}

func (d *sqliteDB) QueryRow(ctx context.Context, query string, args ...any) (Row, error) {
	out, err := d.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out[0], nil
}

func (d *sqliteDB) Exec(ctx context.Context, query string, args ...any) error {
	_, err := d.conn(ctx).ExecContext(ctx, query, args...)
	return err
}

func (d *sqliteDB) Tx(ctx context.Context, fn func(context.Context) error) error {
	// 중첩은 바깥 트랜잭션을 그대로 쓴다 — sqlite 는 커넥션이 하나라 새 BEGIN 이 곧 오류다.
	if tx, ok := ctx.Value(txKey{}).(*sql.Tx); ok && tx != nil {
		return fn(ctx)
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(context.WithValue(ctx, txKey{}, tx)); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ExecRaw 는 멀티 스테이트먼트 원시 실행이다(스키마 DDL·마이그레이션 러너 전용).
// 자리표시자 치환도, 테넌트 주입도 하지 않는다.
func (d *sqliteDB) ExecRaw(ctx context.Context, sqlText string) error {
	_, err := d.db.ExecContext(ctx, sqlText)
	return err
}

// scanRows 는 *sql.Rows 를 Row 슬라이스로 옮긴다. 컬럼명은 소문자로 접는다.
func scanRows(rows *sql.Rows) ([]Row, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	lower := make([]string, len(cols))
	for i, c := range cols {
		lower[i] = strings.ToLower(c)
	}
	out := []Row{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		r := make(Row, len(cols))
		for i, name := range lower {
			// []byte 는 다음 Scan 에서 재사용될 수 있는 버퍼다 — 문자열로 복사해 둔다.
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
