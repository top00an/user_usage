package store

import (
	"context"
	"testing"

	"github.com/tscorp/user-usage/internal/db"
)

// Init 은 몇 번 돌려도 같다 — 부팅마다 도는 경로라 멱등이 아니면 두 번째 기동이 죽는다.
func TestInitIsIdempotent(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, db.Options{Mode: "local", DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close(); handle = nil }()

	for i := 0; i < 3; i++ {
		if err := Init(ctx, d); err != nil {
			t.Fatalf("%d번째 Init: %v", i+1, err)
		}
	}
	// 그리고 실제로 쓸 수 있다.
	mustSession(t, ctx, SessionInput{SessionID: "s", StartedAt: "2026-08-03T09:00:00.000Z", Output: 1})
	if tot, _ := Totals(ctx); tot.Sessions != 1 {
		t.Fatalf("%+v", tot)
	}
}

/*
 * 기존 DB 에 컬럼을 멱등 추가한다.
 *
 * `CREATE TABLE IF NOT EXISTS` 는 **기존 테이블에 새 컬럼을 안 넣는다.** ended_at·no_ts_turns 는
 * 나중에 생긴 컬럼이라, 이 보강이 없으면 옛 DB 로 뜬 서버에서 관련 질의가 통째로 실패한다.
 */
func TestInitAddsMissingColumnsToOldDatabase(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, db.Options{Mode: "local", DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close(); handle = nil }()

	// 두 컬럼이 없던 시절의 스키마를 손으로 만든다.
	if err := d.Exec(ctx, `CREATE TABLE usage_sessions (
		session_id TEXT PRIMARY KEY, machine TEXT, username TEXT, project TEXT, model TEXT,
		input INTEGER NOT NULL DEFAULT 0, output INTEGER NOT NULL DEFAULT 0,
		cache_read INTEGER NOT NULL DEFAULT 0, cache_create INTEGER NOT NULL DEFAULT 0,
		web_search INTEGER NOT NULL DEFAULT 0, web_fetch INTEGER NOT NULL DEFAULT 0,
		turns INTEGER NOT NULL DEFAULT 0, started_at TEXT, reported_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := Init(ctx, d); err != nil {
		t.Fatalf("옛 DB 에서 Init 이 죽었다: %v", err)
	}

	cols := map[string]bool{}
	rows, err := d.Query(ctx, "PRAGMA table_info(usage_sessions)")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		cols[r.Str("name")] = true
	}
	if !cols["ended_at"] || !cols["no_ts_turns"] {
		t.Fatalf("빠진 컬럼이 보강되지 않았다: %v", cols)
	}
	// 그리고 그 컬럼을 읽는 질의가 실제로 돈다.
	if _, err := UsageModelAxis(ctx); err != nil {
		t.Fatalf("no_ts_turns 를 읽는 질의가 깨졌다: %v", err)
	}
}

// nil DB 로 Init 하면 말이 되는 오류를 준다.
func TestInitRejectsNilDB(t *testing.T) {
	if err := Init(context.Background(), nil); err == nil {
		t.Fatal("nil DB 가 통과했다")
	}
}
