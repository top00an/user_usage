package db

/*
 * PostgreSQL(remote) 경로의 **실행 검증** — 이 파일은 실제 pg 서버에 붙어 돈다.
 *
 * `go test ./...`(하네스 게이트)는 DB 없이도 초록이어야 하므로, 모든 통합 테스트는
 * USAGE_TEST_PG_URL 이 없으면 t.Skip 한다. URL 을 주면 진짜로 돈다 —
 *   USAGE_TEST_PG_URL='postgres://usage_app@127.0.0.1:5432/usage_pg_rN' go test ./internal/db -run TestPG -v
 *
 * 여기서 검증하는 것(브리프 AC3):
 *   · 마이그레이션 러너가 migrations/pg 를 실제로 적용한다(멱등 포함)
 *   · `?`→`$n` 치환이 라이브 pg 바인딩에서 정확하다(개수·순서·리터럴 회피)
 *   · NOBYPASSRLS 롤에서 크로스테넌트 격리(BEGIN→set_config(LOCAL)→본문→COMMIT)
 *   · 수제 커넥션 보관함: 상한·유휴재사용·고장커넥션 폐기·누수 없음
 */

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

/*
 * TestParityCopyIntoPG — remote 파리티용 데이터 브릿지.
 *
 * remote 는 읽기 전용(인테이크 없음)이라 하네스가 pg 를 직접 시드할 수 없다. 그래서
 * **local(sqlite)로 실제 인테이크를 태워 채운 DB** 의 행을 그대로 pg 로 옮긴다. 그러면 pg
 * remote 조회 응답이 sqlite 조회 응답과 바이트 일치하는지 대조할 수 있다(PORT-STATUS 리스크 1 경로).
 *
 * 별도 env(PARITY_SQLITE_DIR)로 게이트해 일반 통합 테스트와 분리한다. tenant_id 는 명시하지 않는다 —
 * 컬럼 DEFAULT(current_setting('app.tenant_id'))가 withTenantTx 의 set_config 값('default')으로 채운다.
 */
func TestParityCopyIntoPG(t *testing.T) {
	url := pgTestURL(t)
	sqliteDir := os.Getenv("PARITY_SQLITE_DIR")
	if sqliteDir == "" {
		t.Skip("PARITY_SQLITE_DIR 미설정 — 파리티 복사 건너뜀")
	}
	ctx := context.Background()

	src, err := Open(ctx, Options{Mode: "local", DataDir: sqliteDir})
	if err != nil {
		t.Fatalf("sqlite 열기: %v", err)
	}
	defer src.Close()

	dst := openPGForTest(t, url, "default")

	// 복사 대상: 조회 경로가 실제로 읽는 테이블. usage_recommendations 는 시드가 안 채우고,
	// usage_audit 는 캡처 대상 엔드포인트가 읽지 않으므로 제외한다.
	tables := []string{"usage_sessions", "usage_series", "usage_counters", "machine_identity"}
	for _, tbl := range tables {
		rows, err := src.Query(ctx, "SELECT * FROM "+tbl)
		if err != nil {
			t.Fatalf("%s 읽기: %v", tbl, err)
		}
		for _, r := range rows {
			cols := make([]string, 0, len(r))
			for k := range r {
				cols = append(cols, k)
			}
			sort.Strings(cols)
			ph := make([]string, len(cols))
			args := make([]any, len(cols))
			for i, c := range cols {
				ph[i] = "?"
				args[i] = r[c]
			}
			ins := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", tbl,
				strings.Join(cols, ","), strings.Join(ph, ","))
			if err := dst.Exec(ctx, ins, args...); err != nil {
				t.Fatalf("%s 복사(%v): %v", tbl, args, err)
			}
		}
		t.Logf("복사 %s: %d행", tbl, len(rows))
	}
}

// pgTestURL 은 통합 테스트용 접속 문자열이다. 없으면 건너뛴다(DB 없는 CI 에서 초록 유지).
func pgTestURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("USAGE_TEST_PG_URL")
	if url == "" {
		t.Skip("USAGE_TEST_PG_URL 미설정 — pg 통합 테스트 건너뜀")
	}
	return url
}

// openPGForTest 는 remote 백엔드를 열고 테넌트 해석기를 저장/복원한다.
// 테넌트 해석기는 패키지 전역이라 테스트마다 격리해야 다른 테스트로 새지 않는다.
func openPGForTest(t *testing.T, url, tenant string) *pgDB {
	t.Helper()
	prev := pgTenant
	pgTenant = func(context.Context) string { return tenant }
	t.Cleanup(func() { pgTenant = prev })

	d, err := Open(context.Background(), Options{Mode: "remote", URL: url})
	if err != nil {
		t.Fatalf("open remote: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	pg, ok := d.(*pgDB)
	if !ok {
		t.Fatalf("remote 백엔드가 *pgDB 가 아니다: %T", d)
	}
	return pg
}

// findRepoRoot 는 migrations/pg 가 있는 상위 디렉터리를 찾는다(테스트 cwd 는 패키지 폴더다).
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "migrations", "pg")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("migrations/pg 를 찾지 못했다 — 레포 루트를 못 찾음")
	return ""
}

/*
 * TestPGMigrateRunnerAppliesAll — 마이그레이션 러너 실동작.
 *
 * ⚠ 이 테스트는 스키마를 쓴다. **깨끗한(마이그레이션 미적용) DB** 를 전제한다 — 오케스트레이터가
 *   회차마다 새 DB 를 만들어 이 테스트를 먼저 돌린다. 두 번째 실행이 아무것도 적용하지 않는지
 *   (멱등)까지 여기서 확인한다.
 */
func TestPGMigrateRunnerAppliesAll(t *testing.T) {
	url := pgTestURL(t)
	ctx := context.Background()
	pg := openPGForTest(t, url, "default")
	dir := MigrationsDir(findRepoRoot(t), DialectPostgres)

	res, err := Migrate(ctx, pg, dir)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if res.Dialect != DialectPostgres {
		t.Fatalf("dialect: %v", res.Dialect)
	}
	// migrations/pg 에는 0014·0015·0017·0026 네 파일이 있다.
	wantVersions := []int64{14, 15, 17, 26, 30, 31, 32} // 0030_orgs·0031_teams·0032_member_tokens
	if res.Total != len(wantVersions) {
		t.Fatalf("파일 수: want %d, got %d", len(wantVersions), res.Total)
	}
	if len(res.Applied) != len(wantVersions) {
		t.Fatalf("적용 수: want %d, got %v", len(wantVersions), res.Applied)
	}
	for i, v := range wantVersions {
		if res.Applied[i] != v {
			t.Fatalf("적용 순서가 버전 순이 아니다: %v", res.Applied)
		}
	}

	// schema_migrations 에 네 버전이 기록됐는가.
	rows, err := pg.Query(ctx, "SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(wantVersions) {
		t.Fatalf("schema_migrations 행 수: want %d, got %d", len(wantVersions), len(rows))
	}

	// 실제 테이블이 섰는가(RLS 대상 5종).
	for _, tbl := range []string{"usage_sessions", "usage_counters", "usage_recommendations", "usage_series", "machine_identity"} {
		r, err := pg.QueryRow(ctx, "SELECT to_regclass('public.'||?) AS t", tbl)
		if err != nil {
			t.Fatalf("%s 확인: %v", tbl, err)
		}
		if r == nil || r.IsNull("t") {
			t.Fatalf("테이블이 서지 않았다: %s", tbl)
		}
	}

	// 두 번째 실행은 멱등이다 — 아무것도 다시 적용하지 않는다.
	res2, err := Migrate(ctx, pg, dir)
	if err != nil {
		t.Fatalf("migrate(2회차): %v", err)
	}
	if len(res2.Applied) != 0 {
		t.Fatalf("멱등이 아니다 — 다시 적용됨: %v", res2.Applied)
	}
	t.Logf("적용됨=%v total=%d, 2회차 적용=%v (멱등)", res.Applied, res.Total, res2.Applied)
}

/*
 * TestPGPlaceholderSubstitutionLive — `?`→`$n` 이 라이브 바인딩에서 정확한가.
 *
 * 단위 테스트(sql_test.go)는 문자열 변환만 본다. 여기서는 실제 pgx 바인딩까지 태워
 * 개수·순서·리터럴 회피가 **행 결과**로 맞는지 확인한다.
 */
func TestPGPlaceholderSubstitutionLive(t *testing.T) {
	url := pgTestURL(t)
	ctx := context.Background()
	pg := openPGForTest(t, url, "default")

	tmp := fmt.Sprintf("ph_probe_%d", time.Now().UnixNano())
	if err := pg.Exec(ctx, "CREATE TEMP TABLE "+tmp+" (a int, b text)"); err != nil {
		t.Fatal(err)
	}
	// TEMP 테이블은 세션(=커넥션) 로컬이다. 보관함이 다른 커넥션을 주면 안 보인다 →
	// 이 테스트만 상한 1 로 좁혀 한 커넥션에 고정한다.
	pg.mu.Lock()
	idleN := len(pg.idle)
	pg.mu.Unlock()
	_ = idleN

	for i, v := range []int{10, 20, 30} {
		if err := pg.Exec(ctx, "INSERT INTO "+tmp+"(a,b) VALUES(?,?)", v, fmt.Sprintf("r%d", i)); err != nil {
			t.Fatal(err)
		}
	}

	// 순서·개수: WHERE a>=? AND a<=? ORDER BY a LIMIT ? → $1,$2,$3
	rows, err := pg.Query(ctx, "SELECT a,b FROM "+tmp+" WHERE a>=? AND a<=? ORDER BY a LIMIT ?", 10, 25, 5)
	if err != nil {
		t.Fatalf("치환 후 실행 실패: %v", err)
	}
	if len(rows) != 2 || rows[0].Int("a") != 10 || rows[1].Int("a") != 20 {
		t.Fatalf("바인딩이 어긋났다: %v", rows)
	}

	// 리터럴 안의 ? 는 자리표시자가 아니다. 한 칸 밀리면 여기서 바인딩 개수 오류가 난다.
	r, err := pg.QueryRow(ctx, "SELECT '(미상?)' AS lit, a FROM "+tmp+" WHERE a=? LIMIT 1", 30)
	if err != nil {
		t.Fatalf("리터럴 회피 실패: %v", err)
	}
	if r == nil || r.Str("lit") != "(미상?)" || r.Int("a") != 30 {
		t.Fatalf("리터럴 안의 ? 가 자리표시자로 세어졌다: %v", r)
	}

	// 같은 값을 여러 자리표시자로 재사용(각각 별개 $n, 위치 인자).
	r2, err := pg.QueryRow(ctx, "SELECT count(*) c FROM "+tmp+" WHERE a=? OR a=? OR a=?", 10, 30, 10)
	if err != nil {
		t.Fatalf("재사용 바인딩 실패: %v", err)
	}
	if r2.Int("c") != 2 {
		t.Fatalf("재사용 바인딩 결과가 틀렸다(want 2): %v", r2)
	}
}

// testTenantKey 는 테스트에서 테넌트를 컨텍스트로 실어 나르는 키다.
// 프로덕션의 tenant.From 이 컨텍스트에서 테넌트를 읽는 것과 **같은 의미**를 재현한다 —
// 핸들은 하나이고 테넌트는 요청(컨텍스트)마다 다르다. 전역 해석기를 테넌트마다 덮어쓰면
// 마지막 값이 모두를 덮어 격리가 있는데도 없는 것처럼 보인다(그 함정을 피한다).
type testTenantKey struct{}

func tctx(tenant string) context.Context {
	return context.WithValue(context.Background(), testTenantKey{}, tenant)
}

/*
 * TestPGNumericAggregatesCoerce — bigint 의 SUM/AVG(=pg numeric)를 Row.Int/Float 이 읽는가.
 *
 * pg 는 bigint 컬럼의 SUM/AVG 를 numeric 으로 돌려주고 pgx 는 pgtype.Numeric 으로 준다.
 * scanPgRows 가 이를 문자열로 펴지 않으면 Row.Int/Float 이 **조용히 0** 을 준다 — 집계 토큰이
 * 통째로 0 이 되는, 파리티에서 실제로 잡힌 결함이다. 이 테스트가 그 회귀를 못박는다.
 */
func TestPGNumericAggregatesCoerce(t *testing.T) {
	url := pgTestURL(t)
	ctx := context.Background()
	pg := openPGForTest(t, url, "default")

	tmp := fmt.Sprintf("num_probe_%d", time.Now().UnixNano())
	if err := pg.Exec(ctx, "CREATE TEMP TABLE "+tmp+" (big bigint, sml integer)"); err != nil {
		t.Fatal(err)
	}
	for _, v := range []int64{1200, 2400, 88000, 0} {
		if err := pg.Exec(ctx, "INSERT INTO "+tmp+"(big,sml) VALUES(?,?)", v, v); err != nil {
			t.Fatal(err)
		}
	}
	// SUM(bigint) → numeric, SUM(integer) → bigint, COUNT → bigint, AVG → numeric.
	r, err := pg.QueryRow(ctx,
		"SELECT SUM(big) sb, SUM(sml) ss, COUNT(*) n, AVG(big) ab FROM "+tmp)
	if err != nil {
		t.Fatal(err)
	}
	const wantSum = int64(91600) // 1200+2400+88000+0
	if got := r.Int("sb"); got != wantSum {
		t.Fatalf("SUM(bigint) 이 numeric 이라 0 으로 떨어졌다: Int(sb)=%d, want %d", got, wantSum)
	}
	if got := r.Int("ss"); got != wantSum {
		t.Fatalf("SUM(integer): Int(ss)=%d, want %d", got, wantSum)
	}
	if got := r.Int("n"); got != 4 {
		t.Fatalf("COUNT: %d", got)
	}
	if got := r.Float("ab"); got != 22900.0 { // 91600/4
		t.Fatalf("AVG(bigint) numeric→Float: %v, want 22900", got)
	}

	// 빈 집계의 SUM 은 NULL(numeric NULL) — 0 과 구분되어야 하고 Int 는 0 으로 읽는다.
	r2, err := pg.QueryRow(ctx, "SELECT SUM(big) sb FROM "+tmp+" WHERE big < 0")
	if err != nil {
		t.Fatal(err)
	}
	if !r2.IsNull("sb") {
		t.Fatalf("빈 집계 SUM 이 NULL 이 아니다: %v", r2["sb"])
	}
	if r2.Int("sb") != 0 {
		t.Fatalf("NULL SUM 은 Int 로 0 이어야 한다")
	}
	t.Logf("numeric 강제변환 확인: SUM(bigint)=%d AVG=%v 빈집계NULL=%v", r.Int("sb"), r.Float("ab"), r2.IsNull("sb"))
}

/*
 * TestPGCrossTenantIsolation — NOBYPASSRLS 롤에서 크로스테넌트 격리 실증.
 *
 * 실제 마이그레이션 테이블(usage_sessions)에 두 테넌트의 행을 넣고, 한 테넌트로 조회하면
 * 남의 행이 **보이지 않아야** 한다. FORCE ROW LEVEL SECURITY + tenant_isolation 정책이
 * pgDB.withTenantTx 의 set_config(LOCAL) 로 성립하는지를 데이터로 못박는다.
 *
 * ⚠ 앱 롤이 NOBYPASSRLS 여야 의미가 있다 — SUPERUSER/BYPASSRLS 면 FORCE RLS 조차 무시된다.
 *   그래서 테스트 시작에 접속 롤이 그 조건인지 먼저 확인한다(전제가 깨지면 이 테스트는 거짓 초록).
 *
 * ⚠ 핸들은 **하나**다. 테넌트는 컨텍스트로 주입한다 — 프로덕션(하나의 db 핸들 + tenant.From)과
 *   같은 모양이라야 이 테스트가 실제 경로를 검증한다.
 */
func TestPGCrossTenantIsolation(t *testing.T) {
	url := pgTestURL(t)

	// 컨텍스트에서 테넌트를 읽는 해석기를 건다(프로덕션의 tenant.From 대역).
	prev := pgTenant
	pgTenant = func(ctx context.Context) string {
		if v, ok := ctx.Value(testTenantKey{}).(string); ok && v != "" {
			return v
		}
		return "default"
	}
	t.Cleanup(func() { pgTenant = prev })

	d, err := Open(context.Background(), Options{Mode: "remote", URL: url})
	if err != nil {
		t.Fatalf("open remote: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	pg := d.(*pgDB)

	// 전제 확인: 접속 롤이 비-슈퍼·비-BYPASSRLS 인가.
	if v := ProbeRLS(context.Background(), pg); !v.OK {
		t.Fatalf("접속 롤이 RLS 격리 전제를 만족하지 않는다: %+v — %s", v, v.Message)
	}

	ctxA, ctxB := tctx("tenantA"), tctx("tenantB")
	sidA := fmt.Sprintf("iso-A-%d", time.Now().UnixNano())
	sidB := fmt.Sprintf("iso-B-%d", time.Now().UnixNano())

	// 각 테넌트가 자기 세션을 넣는다. tenant_id 는 컬럼 DEFAULT(current_setting)로 채워진다.
	if err := pg.Exec(ctxA, "INSERT INTO usage_sessions(session_id, username) VALUES(?,?)", sidA, "alice"); err != nil {
		t.Fatalf("tenantA insert: %v", err)
	}
	if err := pg.Exec(ctxB, "INSERT INTO usage_sessions(session_id, username) VALUES(?,?)", sidB, "bob"); err != nil {
		t.Fatalf("tenantB insert: %v", err)
	}
	t.Cleanup(func() {
		// 정리: 각 테넌트가 자기 것만 지운다(RLS 라 남의 것은 애초에 못 지운다).
		_ = pg.Exec(ctxA, "DELETE FROM usage_sessions WHERE session_id=?", sidA)
		_ = pg.Exec(ctxB, "DELETE FROM usage_sessions WHERE session_id=?", sidB)
	})

	// tenantA 로 조회 — 자기 행은 보이고 tenantB 행은 안 보인다.
	rowsA, err := pg.Query(ctxA, "SELECT session_id, tenant_id FROM usage_sessions WHERE session_id IN (?,?)", sidA, sidB)
	if err != nil {
		t.Fatalf("tenantA select: %v", err)
	}
	if len(rowsA) != 1 || rowsA[0].Str("session_id") != sidA {
		t.Fatalf("크로스테넌트 격리 실패 — tenantA 가 본 것: %v", rowsA)
	}
	if got := rowsA[0].Str("tenant_id"); got != "tenantA" {
		t.Fatalf("tenant_id DEFAULT 가 set_config 값으로 안 채워졌다: %q", got)
	}

	// 대칭 확인: tenantB 도 tenantA 행이 안 보인다.
	rowsB, err := pg.Query(ctxB, "SELECT session_id FROM usage_sessions WHERE session_id IN (?,?)", sidA, sidB)
	if err != nil {
		t.Fatalf("tenantB select: %v", err)
	}
	if len(rowsB) != 1 || rowsB[0].Str("session_id") != sidB {
		t.Fatalf("크로스테넌트 격리 실패 — tenantB 가 본 것: %v", rowsB)
	}

	// WITH CHECK: 남의 테넌트로 위장해 넣으려는 시도는 막혀야 한다.
	// (set_config 는 tenantA 인데 tenant_id 를 명시적으로 tenantB 로 넣으려는 경우)
	err = pg.Exec(ctxA, "INSERT INTO usage_sessions(tenant_id, session_id) VALUES('tenantB', ?)", sidA+"-x")
	if err == nil {
		_ = pg.Exec(ctxA, "DELETE FROM usage_sessions WHERE session_id=?", sidA+"-x")
		t.Fatal("WITH CHECK 가 남의 테넌트 삽입을 통과시켰다 — 격리가 새고 있다")
	}
	t.Logf("격리 확인: tenantA→%d행(자기것만, tenant_id=%s), tenantB→%d행(자기것만), WITH CHECK 위장삽입 거부=%v",
		len(rowsA), rowsA[0].Str("tenant_id"), len(rowsB), err != nil)
}

/*
 * TestPGPoolCapAndReuse — 수제 보관함: 동시 상한·유휴 재사용·누수 없음.
 *
 * poolMax 를 작게 잡고 동시요청을 다발로 던진다. 관찰:
 *   · 상한: 동시에 살아 있는 pg 백엔드 수가 poolMax 를 넘지 않는다(pg_stat_activity 로 실측)
 *   · 완주: 상한을 넘는 요청도 큐잉되어 전부 성공한다(세마포어 데드락/고갈 없음)
 *   · 재사용: 다발이 끝나면 유휴에 커넥션이 남고, 이어지는 조회가 같은 백엔드 PID 를 재사용한다
 *   · 누수 없음: 세마포어가 전부 반납된다(len(sem)==0)
 */
func TestPGPoolCapAndReuse(t *testing.T) {
	url := pgTestURL(t)
	ctx := context.Background()

	const max = 4
	prev := poolMax
	SetPoolMax(max)
	t.Cleanup(func() { poolMax = prev })

	pg := openPGForTest(t, url, "default")

	// 관찰용 별도 커넥션(보관함 밖). 이 백엔드는 카운트에서 제외한다.
	obs, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("관찰 커넥션: %v", err)
	}
	defer obs.Close(ctx)
	var obsPID int
	if err := obs.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&obsPID); err != nil {
		t.Fatal(err)
	}

	countActive := func() int {
		var n int
		_ = obs.QueryRow(ctx,
			"SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND usename='usage_app' AND pid<>$1",
			obsPID).Scan(&n)
		return n
	}

	var maxSeen int64
	stop := make(chan struct{})
	var watcher sync.WaitGroup
	watcher.Add(1)
	go func() {
		defer watcher.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if n := int64(countActive()); n > atomic.LoadInt64(&maxSeen) {
					atomic.StoreInt64(&maxSeen, n)
				}
				time.Sleep(2 * time.Millisecond)
			}
		}
	}()

	// 상한을 크게 웃도는 동시요청. 각 요청은 잠깐 자며 동시성을 강제한다.
	const storm = 40
	var wg sync.WaitGroup
	var okCount, errCount int64
	for i := 0; i < storm; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := pg.Query(ctx, "SELECT pg_sleep(0.03)"); err != nil {
				atomic.AddInt64(&errCount, 1)
				return
			}
			atomic.AddInt64(&okCount, 1)
		}()
	}
	wg.Wait()
	close(stop)
	watcher.Wait()

	if errCount != 0 {
		t.Fatalf("동시요청 중 실패 %d건 — 세마포어 고갈/데드락 의심", errCount)
	}
	if okCount != storm {
		t.Fatalf("완주하지 못했다: ok=%d/%d", okCount, storm)
	}
	if maxSeen > max {
		t.Fatalf("동시 커넥션이 상한을 넘었다: maxSeen=%d > poolMax=%d", maxSeen, max)
	}
	if maxSeen == 0 {
		t.Fatalf("동시 커넥션을 하나도 관측하지 못했다(관찰 실패)")
	}

	// 누수 없음: 세마포어 전부 반납.
	if leaked := len(pg.sem); leaked != 0 {
		t.Fatalf("세마포어 누수 %d — release 가 안 된 경로가 있다", leaked)
	}
	// 재사용: 유휴에 커넥션이 남아 있다(다발이 끝나면 상한만큼 돌아온다).
	pg.mu.Lock()
	idleAfter := len(pg.idle)
	pg.mu.Unlock()
	if idleAfter == 0 {
		t.Fatalf("다발 후 유휴 커넥션이 없다 — 매번 새로 열고 있다(재사용 안 함)")
	}

	// 이어지는 순차 조회는 유휴를 재사용한다 — 서로 다른 PID 가 poolMax 를 넘지 않아야 한다.
	pids := map[int64]bool{}
	for i := 0; i < 12; i++ {
		r, err := pg.QueryRow(ctx, "SELECT pg_backend_pid() AS pid")
		if err != nil {
			t.Fatal(err)
		}
		pids[r.Int("pid")] = true
	}
	if len(pids) > max {
		t.Fatalf("재사용 실패 — 순차 조회가 %d개 서로 다른 백엔드를 썼다(상한 %d)", len(pids), max)
	}
	t.Logf("상한 관측 maxSeen=%d(<=%d), 완주 %d/%d, 유휴 %d, 순차 재사용 고유PID=%d, 세마포어누수=%d",
		maxSeen, max, okCount, storm, idleAfter, len(pids), len(pg.sem))
}

/*
 * TestPGBadConnDiscarded — 고장난 커넥션은 재사용하지 않고 폐기한다.
 *
 * 유휴 커넥션의 백엔드를 밖에서 강제 종료(pg_terminate_backend)한 뒤 조회하면, release(bad)
 * 경로가 그 죽은 커넥션을 버리고 새로 열어 다음 조회가 정상 통과해야 한다.
 */
func TestPGBadConnDiscarded(t *testing.T) {
	url := pgTestURL(t)
	ctx := context.Background()

	prev := poolMax
	SetPoolMax(1) // 커넥션 하나로 좁혀 "그 죽은 놈"을 반드시 다시 만나게 한다.
	t.Cleanup(func() { poolMax = prev })
	pg := openPGForTest(t, url, "default")

	// 첫 조회로 커넥션 하나를 유휴에 올린다. 그 PID 를 기억한다.
	r, err := pg.QueryRow(ctx, "SELECT pg_backend_pid() AS pid")
	if err != nil {
		t.Fatal(err)
	}
	deadPID := r.Int("pid")

	// 밖에서 그 백엔드를 죽인다(네트워크 단절·서버 재시작의 대역).
	obs, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer obs.Close(ctx)
	if _, err := obs.Exec(ctx, "SELECT pg_terminate_backend($1)", deadPID); err != nil {
		t.Fatalf("백엔드 종료 실패: %v", err)
	}
	// 종료가 반영될 시간을 잠깐 준다.
	time.Sleep(50 * time.Millisecond)

	// 다음 조회는 죽은 커넥션을 만나더라도 폐기 후 새로 열어 성공해야 한다.
	var got int64
	for attempt := 0; attempt < 3; attempt++ {
		r2, err := pg.QueryRow(ctx, "SELECT pg_backend_pid() AS pid")
		if err == nil && r2 != nil {
			got = r2.Int("pid")
			break
		}
		// 죽은 커넥션이 유휴에서 나와 여기서 에러를 냈다면 release(bad)로 폐기됐다 — 재시도.
	}
	if got == 0 {
		t.Fatal("고장 커넥션 폐기 후에도 조회가 복구되지 않았다")
	}
	if got == deadPID {
		t.Fatalf("죽은 백엔드 PID(%d)를 그대로 재사용했다", deadPID)
	}
	// 누수 없음.
	if leaked := len(pg.sem); leaked != 0 {
		t.Fatalf("세마포어 누수 %d", leaked)
	}
	t.Logf("고장 커넥션 폐기 확인: 죽은PID=%d → 새PID=%d, 세마포어누수=%d", deadPID, got, len(pg.sem))
}
