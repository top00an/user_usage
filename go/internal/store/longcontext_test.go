package store

// 계단(롱컨텍스트) 분리분의 저장 — 왕복(쓰기→읽기)과 옛 DB 보강.
//
// 이 축의 실패 모양은 조용하다: 컬럼이 없으면 질의가 통째로 죽고(그건 보인다), 배선이 빠지면
// 값이 0 으로 저장돼 **비용만 조용히 낮게** 나온다(그건 안 보인다). 그래서 왕복을 못박는다.

import (
	"context"
	"testing"

	"github.com/tscorp/user-usage/internal/db"
)

const longSessionID = "long-ctx-session-0001"

// 세션 축 왕복 — 쓴 값이 그대로 읽힌다. 총량은 의미 불변(전체 합계)이다.
func TestLong_SessionRoundTrip(t *testing.T) {
	ctx := fresh(t)
	mustSession(t, ctx, SessionInput{
		SessionID: longSessionID, StartedAt: "2026-08-10T09:00:00.000Z",
		Model: "gemini-2.5-pro",
		Input: 400_000, Output: 40_000, CacheRead: 200_000, CacheCreate: 1_000,
		InputLong: 300_000, OutputLong: 30_000, CacheReadLong: 150_000,
	})

	rows, err := SessionRows(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("세션 수 = %d", len(rows))
	}
	s := rows[0]
	if s.Input != 400_000 || s.Output != 40_000 || s.CacheRead != 200_000 {
		t.Fatalf("총량이 롱 몫만큼 깎였다: %+v", s)
	}
	if s.InputLong != 300_000 || s.OutputLong != 30_000 || s.CacheReadLong != 150_000 {
		t.Fatalf("롱 몫 = %d/%d/%d, want 300000/30000/150000",
			s.InputLong, s.OutputLong, s.CacheReadLong)
	}

	// 드릴다운 경로(SessionByID)도 같은 컬럼을 읽는다 — 조회구가 둘이라 한쪽만 배선되기 쉽다.
	one, err := SessionByID(ctx, longSessionID)
	if err != nil || one == nil {
		t.Fatalf("SessionByID: %v / %v", one, err)
	}
	if one.InputLong != 300_000 || one.CacheReadLong != 150_000 {
		t.Fatalf("SessionByID 가 롱 몫을 안 읽는다: %+v", one)
	}
}

// 버킷 축 왕복. 세션만 배선하면 시간 뷰의 비용만 조용히 틀린다.
func TestLong_SeriesRoundTrip(t *testing.T) {
	ctx := fresh(t)
	mustSeries(t, ctx, SeriesInput{
		SessionID: longSessionID,
		Rows: []SeriesRow{{
			Hour: "2026-08-10T09", Model: "gemini-2.5-pro",
			Input: 400_000, Output: 40_000, CacheRead: 200_000,
			InputLong: 300_000, OutputLong: 30_000, CacheReadLong: 150_000,
			CC1h: 5,
		}},
	})

	buckets, err := SeriesRows(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 1 {
		t.Fatalf("버킷 수 = %d", len(buckets))
	}
	b := buckets[0]
	if b.Input != 400_000 || b.CacheRead != 200_000 {
		t.Fatalf("버킷 총량이 깎였다: %+v", b)
	}
	if b.InputLong != 300_000 || b.OutputLong != 30_000 || b.CacheReadLong != 150_000 {
		t.Fatalf("버킷 롱 몫 = %d/%d/%d", b.InputLong, b.OutputLong, b.CacheReadLong)
	}

	// 드릴다운(SeriesOf)도 같은 컬럼을 읽는다.
	of, err := SeriesOf(ctx, longSessionID)
	if err != nil || len(of) != 1 {
		t.Fatalf("SeriesOf: %d개 / %v", len(of), err)
	}
	if of[0].CacheReadLong != 150_000 {
		t.Fatalf("SeriesOf 가 롱 몫을 안 읽는다: %+v", of[0])
	}
}

// 안 보내면 0 — 기존 수집기의 보고가 그대로 저장되고, 읽을 때도 0 이다(= 전부 표준 구간).
func TestLong_AbsentStaysZero(t *testing.T) {
	ctx := fresh(t)
	mustSession(t, ctx, SessionInput{
		SessionID: "old-collector", StartedAt: "2026-08-10T09:00:00.000Z", Input: 100,
	})
	rows, _ := SessionRows(ctx, Filter{})
	if len(rows) != 1 || rows[0].InputLong != 0 || rows[0].OutputLong != 0 || rows[0].CacheReadLong != 0 {
		t.Fatalf("구버전 보고에 롱 몫이 생겼다: %+v", rows)
	}
}

// 음수는 저장 계층에서 0 으로 접힌다(nonNeg) — 불변식의 나머지 절반(총량 상한)은 인테이크가 본다.
func TestLong_NegativeIsFloored(t *testing.T) {
	ctx := fresh(t)
	mustSession(t, ctx, SessionInput{
		SessionID: "neg", StartedAt: "2026-08-10T09:00:00.000Z",
		Input: 100, InputLong: -5,
	})
	rows, _ := SessionRows(ctx, Filter{})
	if rows[0].InputLong != 0 {
		t.Fatalf("음수 롱 몫이 저장됐다: %d", rows[0].InputLong)
	}
}

/*
 * PlatformRollup 이 롱 몫을 **모델 단위로** 합산한다.
 *
 * 이게 빠지면 플랫폼 화면만 전부 표준 구간으로 계산돼, 같은 데이터의 비용이 좌석·시계열
 * 화면과 달라진다. 두 화면이 다른 값을 말하는 것이 이 축에서 가장 나쁜 실패다.
 */
func TestLong_PlatformRollupSumsLongShare(t *testing.T) {
	ctx := fresh(t)
	for i, in := range []SessionInput{
		{SessionID: "p1", Model: "gemini-2.5-pro", Platform: "gemini",
			Input: 1000, InputLong: 400, Output: 100, OutputLong: 40, CacheRead: 200, CacheReadLong: 80},
		{SessionID: "p2", Model: "gemini-2.5-pro", Platform: "gemini",
			Input: 500, InputLong: 100, Output: 50, OutputLong: 10, CacheRead: 100, CacheReadLong: 20},
		{SessionID: "p3", Model: "claude-opus-5", Platform: "claude", Input: 9, Output: 9},
	} {
		in.StartedAt = "2026-08-10T09:00:00.000Z"
		mustSession(t, ctx, in)
		_ = i
	}

	rollup, err := PlatformRollup(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	var gem *PlatformModelRow
	for i := range rollup {
		if rollup[i].Platform != "gemini" {
			continue
		}
		for j := range rollup[i].Models {
			if rollup[i].Models[j].Model == "gemini-2.5-pro" {
				gem = &rollup[i].Models[j]
			}
		}
	}
	if gem == nil {
		t.Fatalf("gemini/gemini-2.5-pro 행이 없다: %+v", rollup)
	}
	if gem.Input != 1500 || gem.Output != 150 || gem.CacheRead != 300 {
		t.Fatalf("총량 합이 틀렸다: %+v", gem)
	}
	if gem.InputLong != 500 || gem.OutputLong != 50 || gem.CacheReadLong != 100 {
		t.Fatalf("롱 몫 합 = %d/%d/%d, want 500/50/100",
			gem.InputLong, gem.OutputLong, gem.CacheReadLong)
	}
}

/*
 * 옛 DB 보강 — 컬럼이 없던 시절의 파일로 떠도 안 깨진다(0035 가 쓴 방식 그대로).
 *
 * `CREATE TABLE IF NOT EXISTS` 는 **기존 테이블에 새 컬럼을 안 넣는다.** 이 보강이 없으면
 * 옛 DB 로 뜬 서버에서 세션·버킷 조회가 통째로 실패한다 — 대시보드 전체가 죽는다.
 * **두 표 모두**를 본다. 한쪽만 보강하면 그 표를 읽는 화면만 죽는데, 그게 더 찾기 어렵다.
 */
func TestLong_InitAddsColumnsToOldDatabase(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, db.Options{Mode: "local", DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close(); handle = nil }()

	// 계단 컬럼이 없던 시절의 스키마(0035 시점).
	if err := d.Exec(ctx, `CREATE TABLE usage_sessions (
		session_id TEXT PRIMARY KEY, machine TEXT, username TEXT, project TEXT, model TEXT,
		platform TEXT NOT NULL DEFAULT 'claude',
		input INTEGER NOT NULL DEFAULT 0, output INTEGER NOT NULL DEFAULT 0,
		cache_read INTEGER NOT NULL DEFAULT 0, cache_create INTEGER NOT NULL DEFAULT 0,
		web_search INTEGER NOT NULL DEFAULT 0, web_fetch INTEGER NOT NULL DEFAULT 0,
		turns INTEGER NOT NULL DEFAULT 0, started_at TEXT, ended_at TEXT, reported_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := d.Exec(ctx, `CREATE TABLE usage_series (
		session_id TEXT NOT NULL, hour TEXT NOT NULL, model TEXT NOT NULL,
		input INTEGER NOT NULL DEFAULT 0, output INTEGER NOT NULL DEFAULT 0,
		cache_read INTEGER NOT NULL DEFAULT 0, cache_create INTEGER NOT NULL DEFAULT 0,
		cc_5m INTEGER NOT NULL DEFAULT 0, cc_1h INTEGER NOT NULL DEFAULT 0,
		turns INTEGER NOT NULL DEFAULT 0, tool_errors INTEGER NOT NULL DEFAULT 0,
		stop_max_tokens INTEGER NOT NULL DEFAULT 0, stop_refusal INTEGER NOT NULL DEFAULT 0,
		latency_ms_sum INTEGER NOT NULL DEFAULT 0, latency_ms_max INTEGER NOT NULL DEFAULT 0,
		latency_turns INTEGER NOT NULL DEFAULT 0,
		username TEXT, machine TEXT, project TEXT,
		PRIMARY KEY (session_id, hour, model))`); err != nil {
		t.Fatal(err)
	}
	// 보강 전에 행이 하나 있어야 DEFAULT 0 이 옛 행에 실제로 걸리는지 볼 수 있다.
	if err := d.Exec(ctx,
		"INSERT INTO usage_sessions(session_id, input, started_at) VALUES('legacy', 42, '2026-08-01T00:00:00.000Z')"); err != nil {
		t.Fatal(err)
	}

	// 두 번 돌려도 같다 — 부팅마다 도는 경로다.
	for i := 0; i < 2; i++ {
		if err := Init(ctx, d); err != nil {
			t.Fatalf("%d번째 Init 이 옛 DB 에서 죽었다: %v", i+1, err)
		}
	}

	for _, table := range []string{"usage_sessions", "usage_series"} {
		cols := map[string]bool{}
		rows, err := d.Query(ctx, "PRAGMA table_info("+table+")")
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			cols[r.Str("name")] = true
		}
		for _, c := range []string{"input_long", "output_long", "cache_read_long"} {
			if !cols[c] {
				t.Fatalf("%s.%s 가 보강되지 않았다: %v", table, c, cols)
			}
		}
	}

	// 그리고 그 컬럼을 읽는 질의가 실제로 돈다. 옛 행은 총량 유지 + 롱 몫 0 이다.
	rows, err := SessionRows(ctx, Filter{})
	if err != nil {
		t.Fatalf("보강 후에도 조회가 깨졌다: %v", err)
	}
	if len(rows) != 1 || rows[0].Input != 42 || rows[0].InputLong != 0 {
		t.Fatalf("옛 행이 손상됐다: %+v", rows)
	}
	if _, err := SeriesRows(ctx, Filter{}); err != nil {
		t.Fatalf("버킷 조회가 깨졌다: %v", err)
	}
	if _, err := PlatformRollup(ctx, Filter{}); err != nil {
		t.Fatalf("플랫폼 롤업이 깨졌다: %v", err)
	}
}
