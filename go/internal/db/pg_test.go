package db

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

/*
 * pg 경로의 **실행** 검증. 살아 있는 PostgreSQL 이 있어야 밟히므로 기본은 건너뛴다 —
 * `go test ./...` 가 클러스터 없는 머신에서 빨개지면 아무도 안 돌리게 된다.
 *
 *	USAGE_TEST_PG_URL=postgres://usage_app:…@127.0.0.1:5432/usage_pg_r1 go test ./internal/db -run PGLive -v
 *
 * 여기서 검증하는 것은 코드 리뷰로는 못 잡는 넷이다:
 *	① migrate 러너가 migrations/pg/*.sql 을 실제로 적용하는가(ExecRaw 의 멀티 스테이트먼트·달러 인용)
 *	② RLS 테넌트 주입이 실제로 격리를 세우는가(NOBYPASSRLS 롤로 남의 행이 안 보이는가)
 *	③ `?`→`$n` 치환이 실제 드라이버에서 통하는가(개수·순서·재사용)
 *	④ 수제 커넥션 보관함이 부하에서 고갈·누수 없이 도는가
 */

func liveURL(t *testing.T) string {
	t.Helper()
	u := strings.TrimSpace(os.Getenv("USAGE_TEST_PG_URL"))
	if u == "" {
		t.Skip("USAGE_TEST_PG_URL 이 없다 — 살아 있는 pg 없이는 실행 검증이 불가능하다")
	}
	return u
}

func liveDB(t *testing.T) DB {
	t.Helper()
	d, err := Open(context.Background(), Options{Mode: "remote", URL: liveURL(t)})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// withTenant 는 테스트용 테넌트 해석기를 갈아 끼운다. 패키지 변수라 되돌려 놓는다.
func withTenant(t *testing.T, name string) {
	t.Helper()
	prev := pgTenant
	pgTenant = func(context.Context) string { return name }
	t.Cleanup(func() { pgTenant = prev })
}

/* ── ① 마이그레이션 러너 ─────────────────────────────────────────────── */

func TestPGLiveMigrateAppliesRepoMigrations(t *testing.T) {
	ctx := context.Background()
	d := liveDB(t)
	dir := strings.TrimSpace(os.Getenv("USAGE_TEST_PG_MIGRATIONS"))
	if dir == "" {
		dir = MigrationsDir("../../..", DialectPostgres)
	}

	res, err := Migrate(ctx, d, dir)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Logf("dialect=%s total=%d applied=%v", res.Dialect, res.Total, res.Applied)
	if res.Total == 0 {
		t.Fatalf("migrations/pg 에서 파일을 하나도 못 찾았다(dir=%s)", dir)
	}

	// 두 번째 실행은 아무것도 적용하지 않는다(멱등).
	res2, err := Migrate(ctx, d, dir)
	if err != nil {
		t.Fatalf("Migrate(재실행): %v", err)
	}
	if len(res2.Applied) != 0 {
		t.Fatalf("멱등하지 않다 — 재적용됨: %v", res2.Applied)
	}

	// 러너가 실제로 스키마를 세웠는지 확인한다. 러너가 조용히 아무것도 안 하는 경로를 막는다.
	for _, tbl := range []string{"usage_sessions", "usage_series", "usage_counters", "usage_recommendations", "machine_identity"} {
		row, err := d.QueryRow(ctx,
			"SELECT count(*) AS n FROM information_schema.tables WHERE table_schema='public' AND table_name=?", tbl)
		if err != nil {
			t.Fatalf("%s 확인 실패: %v", tbl, err)
		}
		if row.Int("n") != 1 {
			t.Fatalf("%s 가 없다 — 러너가 적용하지 않았다", tbl)
		}
	}
	// 0017·0026 이 붙인 후행 컬럼까지 확인한다(파일 하나가 통째로 건너뛰어도 테이블은 있다).
	for _, col := range []string{"ended_at", "no_ts_turns"} {
		row, err := d.QueryRow(ctx,
			"SELECT count(*) AS n FROM information_schema.columns WHERE table_name='usage_sessions' AND column_name=?", col)
		if err != nil {
			t.Fatalf("%s 확인 실패: %v", col, err)
		}
		if row.Int("n") != 1 {
			t.Fatalf("usage_sessions.%s 가 없다", col)
		}
	}
	// RLS 가 ENABLE + FORCE 로 서 있는지. 하나라도 빠지면 격리가 조용히 무너진다.
	rows, err := d.Query(ctx,
		"SELECT relname, relrowsecurity, relforcerowsecurity FROM pg_class WHERE relname IN "+
			"('usage_sessions','usage_series','usage_counters','usage_recommendations','machine_identity')")
	if err != nil {
		t.Fatalf("RLS 확인 실패: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("테이블 5개를 기대했다: %d", len(rows))
	}
	for _, r := range rows {
		if !r.Bool("relrowsecurity") || !r.Bool("relforcerowsecurity") {
			t.Fatalf("%s: ENABLE/FORCE RLS 가 안 서 있다 (%v/%v)",
				r.Str("relname"), r["relrowsecurity"], r["relforcerowsecurity"])
		}
	}
}

/* ── ② RLS 테넌트 격리 ──────────────────────────────────────────────── */

// 이 테스트가 이 파일의 존재 이유다: 테넌트 주입이 실패하면 요청은 200 이고 화면도 정상인데
// 남의 테넌트 행이 섞인다. 증상이 없는 사고라 실측으로만 잡힌다.
func TestPGLiveTenantIsolation(t *testing.T) {
	ctx := context.Background()
	d := liveDB(t)

	// 롤 전제부터 확인한다 — 슈퍼·BYPASSRLS 롤이면 아래 격리 주장이 전부 무의미하다.
	if v := ProbeRLS(ctx, d); !v.OK {
		t.Fatalf("앱 롤 전제 실패: %s", v.Message)
	}

	const (
		tA = "pgtest-tenant-a"
		tB = "pgtest-tenant-b"
	)
	sid := fmt.Sprintf("rls-%d", time.Now().UnixNano())

	// A 테넌트로 한 행 쓴다. tenant_id 는 컬럼 DEFAULT(current_setting) 가 채운다.
	withTenant(t, tA)
	if err := d.Exec(ctx,
		"INSERT INTO usage_sessions(session_id, machine, username, turns) VALUES(?,?,?,?)",
		sid, "host-a", "alice", 3); err != nil {
		t.Fatalf("A 쓰기 실패: %v", err)
	}
	t.Cleanup(func() {
		withTenantRaw(tA)
		_ = d.Exec(context.Background(), "DELETE FROM usage_sessions WHERE session_id = ?", sid)
	})

	// A 로는 보인다.
	rows, err := d.Query(ctx, "SELECT session_id, tenant_id FROM usage_sessions WHERE session_id = ?", sid)
	if err != nil {
		t.Fatalf("A 조회 실패: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("A 테넌트가 자기 행을 못 본다: %d", len(rows))
	}
	if got := rows[0].Str("tenant_id"); got != tA {
		t.Fatalf("tenant_id DEFAULT 가 주입값을 안 받았다: %q (원한 값 %q) — set_config 가 안 걸렸다", got, tA)
	}

	// B 로는 **안 보여야** 한다.
	withTenant(t, tB)
	rows, err = d.Query(ctx, "SELECT session_id FROM usage_sessions WHERE session_id = ?", sid)
	if err != nil {
		t.Fatalf("B 조회 실패: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("크로스테넌트 누수 — B 가 A 의 행 %d건을 봤다", len(rows))
	}

	// B 가 A 의 행을 지우지도 못해야 한다(정책의 USING 이 DELETE 에도 걸린다).
	if err := d.Exec(ctx, "DELETE FROM usage_sessions WHERE session_id = ?", sid); err != nil {
		t.Fatalf("B DELETE 실행 실패: %v", err)
	}
	withTenant(t, tA)
	rows, err = d.Query(ctx, "SELECT session_id FROM usage_sessions WHERE session_id = ?", sid)
	if err != nil {
		t.Fatalf("A 재조회 실패: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("B 가 A 의 행을 지웠다 — 정책이 DELETE 를 막지 않는다")
	}
}

// withTenantRaw 는 Cleanup 처럼 t.Cleanup 밖에서 테넌트를 바꿔야 할 때 쓴다.
func withTenantRaw(name string) { pgTenant = func(context.Context) string { return name } }

// 트랜잭션이 끝나면 테넌트 설정이 남지 않아야 한다 — 보관함에 돌아간 커넥션이 남의 테넌트를
// 들고 있으면 다음 요청이 조용히 그 테넌트로 흐른다. set_config(..., true) 의 LOCAL 성질 확인.
func TestPGLiveTenantDoesNotLeakBetweenCheckouts(t *testing.T) {
	ctx := context.Background()
	d := liveDB(t)

	withTenant(t, "pgtest-leak-a")
	if _, err := d.QueryRow(ctx, "SELECT 1 AS x"); err != nil {
		t.Fatalf("첫 왕복 실패: %v", err)
	}

	// 같은 커넥션이 재사용되도록 상한을 좁히지 않아도 유휴 스택 top 이 방금 그 커넥션이다.
	withTenant(t, "pgtest-leak-b")
	row, err := d.QueryRow(ctx, "SELECT current_setting('app.tenant_id', true) AS t")
	if err != nil {
		t.Fatalf("둘째 왕복 실패: %v", err)
	}
	if got := row.Str("t"); got != "pgtest-leak-b" {
		t.Fatalf("테넌트가 새로 안 걸렸다: %q", got)
	}
}

/* ── ③ `?` → `$n` 치환 ──────────────────────────────────────────────── */

// 치환은 문법이 아니라 **바인딩**의 문제다. 개수가 어긋나면 드라이버가 소리를 내지만,
// 순서가 어긋나면 오류 없이 틀린 값이 나온다. 그래서 값을 되받아 확인한다.
func TestPGLivePlaceholderRewriteBindsInOrder(t *testing.T) {
	ctx := context.Background()
	d := liveDB(t)
	withTenant(t, "pgtest-ph")

	row, err := d.QueryRow(ctx, "SELECT ? AS a, ? AS b, ? AS c", "one", "two", "three")
	if err != nil {
		t.Fatalf("3개 바인딩 실패: %v", err)
	}
	if row.Str("a") != "one" || row.Str("b") != "two" || row.Str("c") != "three" {
		t.Fatalf("순서가 어긋났다: %v", row)
	}

	// 문자열 리터럴 안의 `?` 는 자리표시자가 아니다. 세면 뒤 인자가 한 칸씩 밀린다.
	row, err = d.QueryRow(ctx, "SELECT '왜?' AS lit, ? AS a, ? AS b", "x", "y")
	if err != nil {
		t.Fatalf("리터럴 물음표에서 실패: %v", err)
	}
	if row.Str("lit") != "왜?" || row.Str("a") != "x" || row.Str("b") != "y" {
		t.Fatalf("리터럴 안 물음표를 자리표시자로 셌다: %v", row)
	}

	// 홑따옴표 두 번(SQL 표준 이스케이프)도 상태가 맞아야 한다.
	row, err = d.QueryRow(ctx, "SELECT 'it''s ok' AS lit, ? AS a", "z")
	if err != nil {
		t.Fatalf("이스케이프 리터럴에서 실패: %v", err)
	}
	if row.Str("lit") != "it's ok" || row.Str("a") != "z" {
		t.Fatalf("이스케이프 리터럴 처리가 틀렸다: %v", row)
	}
}

// 같은 인자를 여러 번 쓰는 SQL — 현행 규칙은 **재사용이 아니라 매번 새 번호**다.
// (`?` 하나가 인자 하나에 대응한다. 호출부가 같은 값을 두 번 넘긴다.)
// 이 규칙이 조용히 바뀌면 뒤 인자가 전부 한 칸씩 밀린다.
func TestPGLivePlaceholderNumbersEachQuestionMark(t *testing.T) {
	ctx := context.Background()
	d := liveDB(t)
	withTenant(t, "pgtest-ph2")

	const q = "SELECT ? AS a, ? AS b WHERE ? <> ''"
	if got, want := ToPg(q), "SELECT $1 AS a, $2 AS b WHERE $3 <> ''"; got != want {
		t.Fatalf("ToPg 규칙이 바뀌었다:\n got %q\nwant %q", got, want)
	}
	row, err := d.QueryRow(ctx, q, "a", "b", "c")
	if err != nil {
		t.Fatalf("실행 실패: %v", err)
	}
	if row.Str("a") != "a" || row.Str("b") != "b" {
		t.Fatalf("바인딩이 틀렸다: %v", row)
	}
}

/* ── ④ 커넥션 보관함 ────────────────────────────────────────────────── */

// 동시 상한을 넘지 않고, 유휴를 재사용하고, 누수가 없어야 한다.
func TestPGLivePoolRespectsLimitAndReuses(t *testing.T) {
	ctx := context.Background()

	prev := poolMax
	poolMax = 4
	defer func() { poolMax = prev }()

	d, err := Open(ctx, Options{Mode: "remote", URL: liveURL(t)})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = d.Close() }()
	pg := d.(*pgDB)
	withTenant(t, "pgtest-pool")

	const workers, iters = 32, 20
	var wg sync.WaitGroup
	errs := make(chan error, workers*iters)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				// `?::int` — 자리표시자만 있는 SELECT 목록은 pg 가 타입을 추론하지 못한다
				// (실제 쿼리는 컬럼 비교라 추론이 되지만 여기는 문맥이 없다).
				row, err := d.QueryRow(ctx, "SELECT pg_backend_pid() AS pid, ?::int AS marker", w)
				if err != nil {
					errs <- err
					return
				}
				if row.Int("marker") != int64(w) {
					errs <- fmt.Errorf("worker %d 가 marker %d 를 받았다 — 바인딩이 섞였다", w, row.Int("marker"))
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("부하 중 실패: %v", e)
	}

	// 서버 쪽에서 이 DB 로 열린 백엔드 수를 센다 — 상한을 넘겼으면 여기서 드러난다.
	row, err := d.QueryRow(ctx,
		"SELECT count(*) AS n FROM pg_stat_activity WHERE datname = current_database() AND pid <> pg_backend_pid()")
	if err != nil {
		t.Fatalf("백엔드 수 조회 실패: %v", err)
	}
	// 조회 자신이 커넥션 하나를 쓰므로 n+1 이 실제 보유 수다.
	if n := row.Int("n") + 1; n > int64(poolMax) {
		t.Fatalf("동시 상한 %d 를 넘겼다: 서버에 %d개", poolMax, n)
	}

	pg.mu.Lock()
	idle := len(pg.idle)
	pg.mu.Unlock()
	if idle == 0 {
		t.Fatal("유휴 커넥션이 하나도 없다 — 재사용이 아니라 매번 새로 열고 있다")
	}
	if idle > poolMax {
		t.Fatalf("유휴가 상한보다 많다(누수): %d > %d", idle, poolMax)
	}
	t.Logf("부하 후 유휴 %d개(상한 %d) — 세마포어 잔여 %d", idle, poolMax, len(pg.sem))
	if len(pg.sem) != 0 {
		t.Fatalf("세마포어가 %d칸 안 돌아왔다 — release 누락(고갈로 이어진다)", len(pg.sem))
	}
}

/*
 * 고장난 커넥션은 폐기해야 한다 — 재사용하면 **다음 요청이 대신 죽는다.**
 *
 * 이 시나리오가 운영에서 흔한 경로다: SSH 터널이 끊겼다 붙거나, DBA 가 유휴 백엔드를
 * 정리하거나, 유휴 타임아웃이 걸린다. 그때 보관함에 남은 커넥션은 클라이언트 쪽에서
 * "닫힘"으로 보이지 않는다(소켓을 읽기 전까지는 모른다).
 */
func TestPGLivePoolSurvivesServerSideKill(t *testing.T) {
	ctx := context.Background()
	url := liveURL(t)

	victimDB, err := Open(ctx, Options{Mode: "remote", URL: url})
	if err != nil {
		t.Fatalf("Open(victim): %v", err)
	}
	defer func() { _ = victimDB.Close() }()
	killerDB, err := Open(ctx, Options{Mode: "remote", URL: url})
	if err != nil {
		t.Fatalf("Open(killer): %v", err)
	}
	defer func() { _ = killerDB.Close() }()
	withTenant(t, "pgtest-broken")

	// 커넥션 하나를 예열해 유휴로 돌려놓고, 그 백엔드 pid 를 기억한다.
	row, err := victimDB.QueryRow(ctx, "SELECT pg_backend_pid() AS pid")
	if err != nil {
		t.Fatalf("예열 실패: %v", err)
	}
	pid := row.Int("pid")
	if pid == 0 {
		t.Fatal("pid 를 못 읽었다")
	}

	// **다른 커넥션**에서 그 백엔드를 죽인다. 유휴 커넥션이 서버 쪽에서만 끊긴 상태가 된다.
	if _, err := killerDB.QueryRow(ctx, "SELECT pg_terminate_backend(?) AS ok", pid); err != nil {
		t.Fatalf("terminate 실패: %v", err)
	}

	// 죽은 커넥션이 보관함 top 에 있는 상태에서의 다음 요청. 성공해야 한다.
	got, err := victimDB.QueryRow(ctx, "SELECT 42 AS x")
	if err != nil {
		t.Fatalf("고장 커넥션을 폐기하지 못했다 — 다음 요청이 대신 죽었다: %v", err)
	}
	if got.Int("x") != 42 {
		t.Fatalf("응답이 이상하다: %v", got)
	}

	pg := victimDB.(*pgDB)
	pg.mu.Lock()
	for _, c := range pg.idle {
		if c.IsClosed() {
			pg.mu.Unlock()
			t.Fatal("닫힌 커넥션이 보관함에 남아 있다")
		}
	}
	pg.mu.Unlock()
	if len(pg.sem) != 0 {
		t.Fatalf("세마포어가 %d칸 안 돌아왔다: %d", len(pg.sem), len(pg.sem))
	}
}
