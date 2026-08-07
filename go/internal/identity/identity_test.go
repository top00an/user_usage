package identity

import (
	"context"
	"testing"

	"github.com/tscorp/user-usage/internal/db"
	"github.com/tscorp/user-usage/internal/store"
)

/*
 * 서버 권위 귀속 — 클라이언트가 무엇을 보내든 매핑이 이긴다.
 *
 * 이 계층이 있는 이유: 팀원 PC 는 자기 신원을 스스로 보고하고 그 기본값은 OS 계정명이다.
 * 어긋난 것을 팩 재배포 + 그 PC 재설치로 고치는 방식은 반복 비용이 크고, 실제로 같은 누락을
 * 세 번 반복했다. 서버가 권위를 가지면 관리자가 화면에서 한 줄 고치는 것으로 끝나고,
 * 클라이언트 경로가 몇 개든 인테이크 한 곳에서 수렴한다.
 */

// fresh 는 store 와 identity 를 같은 빈 DB 에 건다(재스탬프가 usage_* 를 건드리므로 둘 다 필요하다).
func fresh(t *testing.T) context.Context {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, db.Options{Mode: "local", DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close(); handle = nil })
	if err := store.Init(ctx, d); err != nil {
		t.Fatalf("store init: %v", err)
	}
	if err := Init(ctx, d); err != nil {
		t.Fatalf("identity init: %v", err)
	}
	return ctx
}

// ingest 는 인테이크가 하는 일을 그대로 재현한다: 매핑을 **먼저 적용하고** 저장한다.
func ingest(t *testing.T, ctx context.Context, machine, claimed, sid string, output int64, git int64) {
	t.Helper()
	user, _ := Resolve(ctx, machine, claimed)
	if err := store.SessionUpsert(ctx, store.SessionInput{
		SessionID: sid, Machine: machine, Username: user, Model: "claude-opus-5",
		StartedAt: "2026-08-03T09:00:00.000Z", Output: output,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CountersUpsertN(ctx, store.CountersInput{
		SessionID: sid, Machine: machine, Username: user, StartedAt: "2026-08-03T09:00:00.000Z",
		Rows: []store.CounterRow{{Kind: "bash", Key: "git", Count: git}},
	}); err != nil {
		t.Fatal(err)
	}
}

func firstUsername(t *testing.T, ctx context.Context) string {
	t.Helper()
	rows, err := store.UsageByUser(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("사용자 행이 없다")
	}
	return rows[0].Username
}

// 매핑이 없으면 클라이언트가 보낸 이름을 그대로 쓴다.
func TestResolveWithoutMappingKeepsClaimed(t *testing.T) {
	ctx := fresh(t)
	got, err := Resolve(ctx, "PC-A", "os-account")
	if err != nil {
		t.Fatal(err)
	}
	if got != "os-account" {
		t.Fatalf("got %q", got)
	}
}

// 매핑을 걸면 과거 데이터가 함께 옮겨진다(소급).
func TestSetRestampsPastRows(t *testing.T) {
	ctx := fresh(t)
	ingest(t, ctx, "pc-1", "user-a", "sess-1", 2000, 3)
	if got := firstUsername(t, ctx); got != "user-a" {
		t.Fatalf("got %q", got)
	}

	r, err := Set(ctx, SetInput{Machine: "pc-1", Username: "user-b", Actor: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Moved.Sessions != 1 {
		t.Fatalf("옮겨진 세션 %d", r.Moved.Sessions)
	}
	if r.Moved.Counters < 1 {
		t.Fatalf("옮겨진 카운터 %d", r.Moved.Counters)
	}
	if got := firstUsername(t, ctx); got != "user-b" {
		t.Fatalf("과거 행이 안 옮겨지면 한 사람이 두 줄로 보인다: %q", got)
	}
}

/*
 * 매핑 후 같은 세션이 옛 이름으로 재보고돼도 되돌아가지 않는다.
 *
 * 이게 이 계층의 핵심이다 — 서버 UPSERT 는 재보고 때 username 을 덮어쓰므로, DB 를 손으로
 * 고쳐 두는 방식은 다음 보고 한 번에 무너진다(실측).
 */
func TestMappingSurvivesReReport(t *testing.T) {
	ctx := fresh(t)
	ingest(t, ctx, "pc-1", "user-a", "sess-1", 2000, 3)
	if _, err := Set(ctx, SetInput{Machine: "pc-1", Username: "user-b", Actor: "admin"}); err != nil {
		t.Fatal(err)
	}
	// 옛 이름을 그대로 들고 재보고한다.
	ingest(t, ctx, "pc-1", "user-a", "sess-1", 9999, 3)

	rows, _ := store.UsageByUser(ctx)
	if len(rows) != 1 {
		t.Fatalf("재보고가 매핑을 무너뜨렸다: %+v", rows)
	}
	if rows[0].Username != "user-b" {
		t.Fatalf("username=%q", rows[0].Username)
	}
	if rows[0].Output != 9999 {
		t.Fatalf("값 자체는 최신으로 갱신돼야 한다: %d", rows[0].Output)
	}
}

// 미매핑 머신 목록이 고칠 대상을 알려준다.
func TestUnmappedListsCandidates(t *testing.T) {
	ctx := fresh(t)
	ingest(t, ctx, "pc-1", "user-a", "sess-1", 10, 1)

	before, err := Unmapped(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 || before[0].Machine != "pc-1" || before[0].Sessions != 1 {
		t.Fatalf("%+v", before)
	}
	if _, err := Set(ctx, SetInput{Machine: "pc-1", Username: "user-b", Actor: "admin"}); err != nil {
		t.Fatal(err)
	}
	after, _ := Unmapped(ctx)
	if len(after) != 0 {
		t.Fatalf("매핑된 머신은 목록에서 빠져야 한다: %+v", after)
	}
}

// 해제하면 이후 보고는 클라이언트 값을 쓰되 **과거는 그대로 둔다**(되돌릴 원본이 없다).
func TestRemoveDoesNotRevertPast(t *testing.T) {
	ctx := fresh(t)
	ingest(t, ctx, "pc-1", "user-a", "sess-1", 10, 1)
	if _, err := Set(ctx, SetInput{Machine: "pc-1", Username: "user-b", Actor: "admin"}); err != nil {
		t.Fatal(err)
	}
	ok, err := Remove(ctx, "pc-1")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got, _ := Resolve(ctx, "pc-1", "user-a"); got != "user-a" {
		t.Fatalf("해제 후에는 클라이언트 값을 쓴다: %q", got)
	}
	if got := firstUsername(t, ctx); got != "user-b" {
		t.Fatalf("과거는 되돌리지 않는다: %q", got)
	}
	// 없는 매핑을 지우면 false 다(오류가 아니다).
	if ok, err := Remove(ctx, "pc-없음"); err != nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

/*
 * 빈 값은 거부한다 — **실수로 귀속을 지우지 못하게.**
 *
 * 통과시키면 과거 행 수천 개의 username 이 한 번에 지워지고, 되돌릴 원본이 남지 않는다.
 */
func TestSetRejectsEmptyValues(t *testing.T) {
	ctx := fresh(t)
	if _, err := Set(ctx, SetInput{Machine: "", Username: "user-b"}); err == nil {
		t.Fatal("빈 machine 이 통과했다")
	}
	if _, err := Set(ctx, SetInput{Machine: "pc-1", Username: "  "}); err == nil {
		t.Fatal("공백뿐인 username 이 통과했다")
	}
	if _, err := Set(ctx, SetInput{Machine: "pc-1", Username: ""}); err == nil {
		t.Fatal("빈 username 이 통과했다")
	}
	// 그리고 아무 매핑도 남지 않는다.
	list, _ := List(ctx)
	if len(list) != 0 {
		t.Fatalf("거부했는데 행이 생겼다: %+v", list)
	}
}

// 재스탬프는 **그 머신의 행만** 건드린다 — 다른 사람 행은 그대로다.
func TestRestampScopedToMachine(t *testing.T) {
	ctx := fresh(t)
	ingest(t, ctx, "pc-1", "user-a", "sess-1", 10, 1)
	ingest(t, ctx, "pc-2", "user-c", "sess-2", 20, 1)

	if _, err := Set(ctx, SetInput{Machine: "pc-1", Username: "user-b"}); err != nil {
		t.Fatal(err)
	}
	rows, _ := store.UsageByUser(ctx)
	got := map[string]bool{}
	for _, r := range rows {
		got[r.Username] = true
	}
	if !got["user-b"] || !got["user-c"] || got["user-a"] {
		t.Fatalf("남의 머신 행을 건드렸다: %v", got)
	}
}

// 이미 그 이름인 행은 옮길 것이 없다(0 건).
func TestRestampCountsOnlyChangedRows(t *testing.T) {
	ctx := fresh(t)
	ingest(t, ctx, "pc-1", "user-b", "sess-1", 10, 1)
	r, err := Set(ctx, SetInput{Machine: "pc-1", Username: "user-b"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Moved.Sessions != 0 {
		t.Fatalf("이미 같은 이름인데 옮겼다고 센다: %d", r.Moved.Sessions)
	}
}

// 갱신은 덮어쓴다(매핑 표에 행이 늘지 않는다).
func TestSetIsUpsert(t *testing.T) {
	ctx := fresh(t)
	if _, err := Set(ctx, SetInput{Machine: "pc-1", Username: "u1", Note: "첫 판"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Set(ctx, SetInput{Machine: "pc-1", Username: "u2", Note: "고침", Actor: "admin"}); err != nil {
		t.Fatal(err)
	}
	list, err := List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("행이 늘었다: %+v", list)
	}
	if list[0].Username != "u2" || list[0].Note != "고침" || list[0].UpdatedBy != "admin" {
		t.Fatalf("%+v", list[0])
	}
	if list[0].UpdatedAt == "" {
		t.Fatal("갱신 시각이 비었다")
	}
}

// Init 전에 쓰면 말이 되는 오류를 준다(nil 역참조로 죽지 않는다).
func TestUninitializedErrors(t *testing.T) {
	prev := handle
	handle = nil
	defer func() { handle = prev }()
	if _, err := List(context.Background()); err == nil {
		t.Fatal("Init 전인데 통과했다")
	}
}
