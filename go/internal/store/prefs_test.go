package store

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tscorp/user-usage/internal/db"
	"github.com/tscorp/user-usage/internal/tenant"
)

/*
 * 대시보드 레이아웃 저장 계층.
 *
 * 이 계층이 지는 것은 둘뿐이다 — **한 사람에 한 행**(덮어쓰기)과 **남의 행이 섞이지 않는 것**.
 * 좌표 검증은 여기가 아니라 httpapi/prefs.go 가 진다(그쪽 테스트가 잡는다).
 */

const layoutA = `[{"id":"cost","x":0,"y":0,"w":6,"h":4}]`

func mustPutLayout(t *testing.T, ctx context.Context, user, raw string) time.Time {
	t.Helper()
	at, err := PutDashboardLayout(ctx, user, []byte(raw))
	if err != nil {
		t.Fatalf("PutDashboardLayout(%s): %v", user, err)
	}
	return at
}

// 저장한 적이 없으면 ok=false 다. **빈 배열이 아니다** — "저장 안 함"과 "빈 배열을 저장함"은
// 다른 사실이고, 화면은 전자에서만 기본 배치로 떨어져야 한다.
func TestDashboardLayoutMissingIsNotEmpty(t *testing.T) {
	ctx := fresh(t)

	raw, at, ok, err := GetDashboardLayout(ctx, "amy")
	if err != nil {
		t.Fatalf("GetDashboardLayout: %v", err)
	}
	if ok {
		t.Fatalf("저장한 적이 없는데 ok=true (raw=%q)", raw)
	}
	if raw != nil {
		t.Fatalf("미저장인데 raw 가 비어 있지 않다: %q", raw)
	}
	if !at.IsZero() {
		t.Fatalf("미저장인데 updatedAt 이 있다: %v", at)
	}
}

// PUT → GET 왕복이 값을 그대로 보존한다(바이트 단위). 저장 계층은 해석하지 않는다.
func TestDashboardLayoutRoundTrip(t *testing.T) {
	ctx := fresh(t)
	at := freezeClock(t, "2026-08-14T09:00:00Z")

	const raw = `[{"id":"cost","x":0,"y":0,"w":6,"h":4},{"id":"tokens","x":6,"y":0,"w":6,"h":4}]`
	saved := mustPutLayout(t, ctx, "amy", raw)
	if !saved.Equal(at) {
		t.Fatalf("Put 가 돌려준 시각=%v, 기대=%v", saved, at)
	}

	got, gotAt, ok, err := GetDashboardLayout(ctx, "amy")
	if err != nil || !ok {
		t.Fatalf("GetDashboardLayout: err=%v ok=%v", err, ok)
	}
	if string(got) != raw {
		t.Fatalf("왕복에서 값이 바뀌었다:\n 저장 %s\n 조회 %s", raw, got)
	}
	if !gotAt.Equal(at) {
		t.Fatalf("updatedAt=%v, 기대=%v", gotAt, at)
	}
}

// 같은 사람이 다시 저장하면 **덮어쓴다**(행이 늘지 않는다). 누적되면 어느 것이 지금 배치인지
// 알 수 없고, PK 가 그것을 막는다는 사실을 값으로 못박는다.
func TestDashboardLayoutPutOverwrites(t *testing.T) {
	ctx := fresh(t)
	freezeClock(t, "2026-08-14T09:00:00Z")
	mustPutLayout(t, ctx, "amy", layoutA)

	second := freezeClock(t, "2026-08-14T10:30:00Z")
	const raw2 = `[{"id":"cost","x":6,"y":2,"w":6,"h":8}]`
	mustPutLayout(t, ctx, "amy", raw2)

	got, at, ok, err := GetDashboardLayout(ctx, "amy")
	if err != nil || !ok {
		t.Fatalf("GetDashboardLayout: err=%v ok=%v", err, ok)
	}
	if string(got) != raw2 {
		t.Fatalf("덮어쓰기가 안 됐다: %s", got)
	}
	if !at.Equal(second) {
		t.Fatalf("updatedAt 이 갱신되지 않았다: %v", at)
	}

	rows, err := handle.Query(ctx, "SELECT username FROM user_dashboard_layout")
	if err != nil {
		t.Fatalf("행 수 조회: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("행이 %d개다 — 사람당 한 행이어야 한다", len(rows))
	}
}

// **남의 배치가 섞이지 않는다.** 이 한 줄이 깨지면 로그인한 사람이 남의 화면을 본다.
func TestDashboardLayoutIsPerUser(t *testing.T) {
	ctx := fresh(t)
	mustPutLayout(t, ctx, "amy", layoutA)

	if _, _, ok, err := GetDashboardLayout(ctx, "bob"); err != nil || ok {
		t.Fatalf("bob 이 amy 의 배치를 봤다: ok=%v err=%v", ok, err)
	}

	const bobRaw = `[{"id":"tokens","x":0,"y":0,"w":12,"h":6}]`
	mustPutLayout(t, ctx, "bob", bobRaw)

	amy, _, ok, err := GetDashboardLayout(ctx, "amy")
	if err != nil || !ok {
		t.Fatalf("amy: err=%v ok=%v", err, ok)
	}
	if string(amy) != layoutA {
		t.Fatalf("bob 의 저장이 amy 의 배치를 덮었다: %s", amy)
	}
}

// 삭제는 그 사람 것만 지우고 **멱등**이다(없는 것을 지워도 오류가 아니다 — 초기화 버튼을
// 두 번 눌렀다고 500 이 나면 안 된다).
func TestDashboardLayoutDeleteIsScopedAndIdempotent(t *testing.T) {
	ctx := fresh(t)
	mustPutLayout(t, ctx, "amy", layoutA)
	mustPutLayout(t, ctx, "bob", layoutA)

	if err := DeleteDashboardLayout(ctx, "amy"); err != nil {
		t.Fatalf("DeleteDashboardLayout: %v", err)
	}
	if _, _, ok, _ := GetDashboardLayout(ctx, "amy"); ok {
		t.Fatal("삭제 후에도 amy 의 배치가 남아 있다")
	}
	if _, _, ok, _ := GetDashboardLayout(ctx, "bob"); !ok {
		t.Fatal("amy 를 지웠는데 bob 의 배치가 사라졌다")
	}
	// 두 번째 삭제 — 없는 것을 지운다.
	if err := DeleteDashboardLayout(ctx, "amy"); err != nil {
		t.Fatalf("멱등이어야 하는데 두 번째 삭제가 실패했다: %v", err)
	}
}

// 빈 username 은 **거부**한다. 통과시키면 사람 신원이 없는 자격(관리자 토큰)의 실수 한 번이
// ""라는 유령 사용자의 행을 만들고, 그 행은 아무에게도 보이지 않은 채 남는다.
func TestDashboardLayoutRejectsEmptyUsername(t *testing.T) {
	ctx := fresh(t)

	if _, err := PutDashboardLayout(ctx, "  ", []byte(layoutA)); err == nil {
		t.Fatal("빈 username 저장이 통과했다")
	}
	if _, _, ok, err := GetDashboardLayout(ctx, ""); err == nil && ok {
		t.Fatal("빈 username 조회가 행을 돌려줬다")
	}
	if err := DeleteDashboardLayout(ctx, ""); err == nil {
		t.Fatal("빈 username 삭제가 통과했다")
	}
}

// 빈 본문은 저장하지 않는다 — jsonb 컬럼에 빈 문자열은 애초에 들어갈 수 없고(pg 는 파싱 오류),
// sqlite 는 조용히 받아 준다. 두 방언이 갈리기 전에 저장 계층에서 끊는다.
func TestDashboardLayoutRejectsEmptyPayload(t *testing.T) {
	ctx := fresh(t)
	if _, err := PutDashboardLayout(ctx, "amy", nil); err == nil {
		t.Fatal("빈 본문 저장이 통과했다")
	}
	if _, err := PutDashboardLayout(ctx, "amy", []byte("   ")); err == nil {
		t.Fatal("공백뿐인 본문 저장이 통과했다")
	}
}

/*
 * pg 전용 — 실제 PostgreSQL 에 붙어야만 의미가 있는 두 가지를 잰다(sqlite 로는 증명 불가):
 *
 *   ① jsonb·text 컬럼의 왕복. pg 는 jsonb 를 드라이버가 **디코드해서** 돌려주므로 그대로
 *      읽으면 문자열이 아니다 — prefs.go 의 CAST(layout AS text) 가 그 차이를 없앤다.
 *      이 테스트가 없으면 그 한 줄이 빠져도 sqlite 게이트는 초록이다.
 *   ② 크로스테넌트 격리(RLS). 0041 의 정책이 실제로 걸렸는지는 여기서만 증명된다.
 *
 * USAGE_TEST_PG_URL 이 없으면 건너뛴다(DB 없는 게이트에서 초록 유지) — auth_pg_test.go 와 같은 규율.
 * ⚠ URL 은 반드시 비-슈퍼·비-BYPASSRLS 앱 롤이어야 한다(슈퍼유저는 FORCE RLS 도 무시한다).
 */
func TestPGDashboardLayoutRoundTripAndIsolation(t *testing.T) {
	url := os.Getenv("USAGE_TEST_PG_URL")
	if url == "" {
		t.Skip("USAGE_TEST_PG_URL 미설정 — pg 레이아웃 왕복·격리 테스트 건너뜀")
	}

	db.SetTenantResolver(tenant.From)
	ctx := context.Background()
	d, err := db.Open(ctx, db.Options{Mode: "remote", URL: url})
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close(); handle = nil })

	// 마이그레이션은 best-effort — 앱 롤에 CREATE 권한이 없으면 운영자가 미리 적용했다고 본다.
	if _, err := db.Migrate(ctx, d, "../../../migrations/pg"); err != nil {
		t.Logf("migrate(무시하고 진행 — 사전 적용 가정): %v", err)
	}
	if err := Init(ctx, d); err != nil {
		t.Fatalf("store.Init: %v", err)
	}

	// 재실행 가능하도록 유니크한 이름을 쓴다(PK 충돌 방지).
	uname := fmt.Sprintf("amy_%d", time.Now().UnixNano())
	ctxA := tenant.With(ctx, "tenant_a")
	ctxB := tenant.With(ctx, "tenant_b")
	t.Cleanup(func() { _ = DeleteDashboardLayout(ctxA, uname) })

	const raw = `[{"id":"cost","x":0,"y":0,"w":6,"h":4}]`
	if _, err := PutDashboardLayout(ctxA, uname, []byte(raw)); err != nil {
		t.Fatalf("PutDashboardLayout(A): %v", err)
	}

	got, at, ok, err := GetDashboardLayout(ctxA, uname)
	if err != nil || !ok {
		t.Fatalf("GetDashboardLayout(A): err=%v ok=%v", err, ok)
	}
	// jsonb 는 키 순서·공백을 정규화하므로 바이트 동일은 요구하지 않는다. 요구하는 것은
	// **문자열로 돌아온다는 것**과 값이 살아 있다는 것이다.
	if !strings.Contains(string(got), `"cost"`) || !strings.Contains(string(got), `"w"`) {
		t.Fatalf("jsonb 왕복이 JSON 문자열이 아니다: %q", got)
	}
	if at.IsZero() {
		t.Fatalf("updatedAt 이 비었다 — text 시각 파싱이 깨졌다")
	}

	/*
	 * ⚠ 저장된 jsonb 가 **배열**이어야 한다. 이 한 줄이 잡는 것은 이중 인코딩이다:
	 * 드라이버가 Go 문자열을 원본 JSON 이 아니라 "JSON 문자열 하나"로 감싸 넣으면
	 * jsonb_typeof 가 'string' 이 된다. 그래도 CAST(... AS text) 는 여전히 `"cost"` 를 품은
	 * 문자열을 돌려주므로 위 왕복 단정은 **통과한다** — 즉 여기가 아니면 아무도 못 잡는다.
	 * (증상은 프론트에서 "레이아웃이 배열이 아니다"로 뒤늦게 나타난다.)
	 */
	row, err := handle.QueryRow(ctxA,
		"SELECT jsonb_typeof(layout) AS kind FROM user_dashboard_layout WHERE username=?", uname)
	if err != nil || row == nil {
		t.Fatalf("jsonb_typeof 조회: err=%v row=%v", err, row)
	}
	if kind := teamStr(row, "kind"); kind != "array" {
		t.Fatalf("저장된 jsonb 가 %q 다 — 배열이어야 한다(문자열이면 이중 인코딩이다)", kind)
	}

	// 격리: tenant_b 에서는 아예 없다.
	if _, _, ok, err := GetDashboardLayout(ctxB, uname); err != nil || ok {
		t.Fatalf("크로스테넌트 누수: tenant_b 가 tenant_a 의 배치를 봤다 (ok=%v err=%v)", ok, err)
	}
}
