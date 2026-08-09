package db

import (
	"context"
	"errors"
	"testing"
)

// openTemp 는 테스트용 sqlite 백엔드를 연다. 파일이라 WAL·PRAGMA 경로까지 실제로 지난다.
func openTemp(t *testing.T) DB {
	t.Helper()
	d, err := Open(context.Background(), Options{Mode: "local", DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestSQLiteRoundTrip(t *testing.T) {
	ctx := context.Background()
	d := openTemp(t)

	if d.Dialect() != DialectSQLite {
		t.Fatalf("dialect: %v", d.Dialect())
	}
	if err := d.Exec(ctx, "CREATE TABLE t(k TEXT PRIMARY KEY, n INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if err := d.Exec(ctx, "INSERT INTO t(k,n) VALUES(?,?)", "a", 3); err != nil {
		t.Fatal(err)
	}
	rows, err := d.Query(ctx, "SELECT k, n FROM t WHERE k=?", "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Str("k") != "a" || rows[0].Int("n") != 3 {
		t.Fatalf("rows=%v", rows)
	}
}

// 행이 없으면 (nil, nil) 이다 — "없음"은 오류가 아니다.
func TestSQLiteQueryRowMissingIsNilNil(t *testing.T) {
	ctx := context.Background()
	d := openTemp(t)
	if err := d.Exec(ctx, "CREATE TABLE t(k TEXT)"); err != nil {
		t.Fatal(err)
	}
	r, err := d.QueryRow(ctx, "SELECT k FROM t WHERE k=?", "nope")
	if err != nil {
		t.Fatalf("없는 행이 오류가 됐다: %v", err)
	}
	if r != nil {
		t.Fatalf("want nil row, got %v", r)
	}
}

// 컬럼 키는 소문자로 정규화된다 — 방언마다 다른 키를 주면 그 차이가 조용한 0 으로 나타난다.
func TestSQLiteColumnKeysAreLowercased(t *testing.T) {
	ctx := context.Background()
	d := openTemp(t)
	if err := d.Exec(ctx, "CREATE TABLE t(k TEXT)"); err != nil {
		t.Fatal(err)
	}
	if err := d.Exec(ctx, "INSERT INTO t(k) VALUES('x')"); err != nil {
		t.Fatal(err)
	}
	rows, err := d.Query(ctx, "SELECT k AS ntsUnknown FROM t")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rows[0]["ntsunknown"]; !ok {
		t.Fatalf("소문자 키가 없다: %v", rows[0])
	}
}

func TestSQLiteTxCommits(t *testing.T) {
	ctx := context.Background()
	d := openTemp(t)
	if err := d.Exec(ctx, "CREATE TABLE t(n INTEGER)"); err != nil {
		t.Fatal(err)
	}
	err := d.Tx(ctx, func(ctx context.Context) error {
		if err := d.Exec(ctx, "INSERT INTO t(n) VALUES(1)"); err != nil {
			return err
		}
		return d.Exec(ctx, "INSERT INTO t(n) VALUES(2)")
	})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := d.QueryRow(ctx, "SELECT COUNT(*) c FROM t")
	if r.Int("c") != 2 {
		t.Fatalf("커밋이 안 됐다: %v", r)
	}
}

func TestSQLiteTxRollsBackOnError(t *testing.T) {
	ctx := context.Background()
	d := openTemp(t)
	if err := d.Exec(ctx, "CREATE TABLE t(n INTEGER)"); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	err := d.Tx(ctx, func(ctx context.Context) error {
		if err := d.Exec(ctx, "INSERT INTO t(n) VALUES(1)"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("오류가 전달되지 않았다: %v", err)
	}
	r, _ := d.QueryRow(ctx, "SELECT COUNT(*) c FROM t")
	if r.Int("c") != 0 {
		t.Fatalf("롤백이 안 됐다: %v", r)
	}
}

// 중첩 Tx 는 바깥 트랜잭션을 그대로 쓴다 — sqlite 는 커넥션이 하나라 새 BEGIN 이 곧 오류다.
func TestSQLiteNestedTxReusesOuter(t *testing.T) {
	ctx := context.Background()
	d := openTemp(t)
	if err := d.Exec(ctx, "CREATE TABLE t(n INTEGER)"); err != nil {
		t.Fatal(err)
	}
	err := d.Tx(ctx, func(ctx context.Context) error {
		return d.Tx(ctx, func(ctx context.Context) error {
			return d.Exec(ctx, "INSERT INTO t(n) VALUES(1)")
		})
	})
	if err != nil {
		t.Fatalf("중첩 Tx 가 깨졌다: %v", err)
	}
	r, _ := d.QueryRow(ctx, "SELECT COUNT(*) c FROM t")
	if r.Int("c") != 1 {
		t.Fatalf("want 1, got %v", r)
	}
}

// 모드 오타는 local 로 조용히 접지 않는다.
func TestOpenRejectsUnknownMode(t *testing.T) {
	if _, err := Open(context.Background(), Options{Mode: "locl"}); err == nil {
		t.Fatal("오타를 통과시켰다 — 로컬 파일에 쓰고 있었다로 끝나면 안 된다")
	}
}

func TestOpenRemoteRequiresURL(t *testing.T) {
	if _, err := Open(context.Background(), Options{Mode: "remote"}); err == nil {
		t.Fatal("URL 없는 remote 를 통과시켰다")
	}
}

// sqlite 는 단일 테넌트라 RLS 판정 대상이 아니다(위반으로 세면 로컬 부팅이 막힌다).
func TestProbeRLSOnSQLiteIsOK(t *testing.T) {
	v := ProbeRLS(context.Background(), openTemp(t))
	if !v.OK || v.Rejects() {
		t.Fatalf("sqlite 가 위반으로 판정됐다: %+v", v)
	}
}
