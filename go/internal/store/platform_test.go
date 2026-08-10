package store

import (
	"context"
	"testing"

	"github.com/tscorp/user-usage/internal/db"
)

/*
 * platform 축 — 저장·기본값·정규화·필터.
 *
 * 이 스위트가 지키는 계약은 하나로 요약된다: **기존 수집기는 이 필드를 안 보낸다.**
 * 그래서 미지정이 claude 로 채워지지 않으면 과거 데이터와 현행 보고가 통째로 다른 축으로 갈린다.
 */

func TestPlatformDefaultsToClaudeWhenUnset(t *testing.T) {
	ctx := fresh(t)
	// 기존 수집기의 보고 그대로 — platform 필드가 아예 없다.
	mustSession(t, ctx, SessionInput{SessionID: "s1", StartedAt: "2026-08-03T09:00:00.000Z", Output: 10})

	rows, err := SessionRows(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("행 수 = %d", len(rows))
	}
	if rows[0].Platform != PlatformDefault {
		t.Fatalf("platform = %q (기대 %q) — 하위호환이 깨졌다", rows[0].Platform, PlatformDefault)
	}
}

func TestNormalizePlatform(t *testing.T) {
	cases := map[string]string{
		"":         "claude",
		"   ":      "claude",
		"claude":   "claude",
		"codex":    "codex",
		"gemini":   "gemini",
		"  Codex ": "codex",
		"GEMINI":   "gemini",
		// 허용목록 밖은 **거부하지도 claude 로 접지도 않는다** — 조용한 오분류를 만들지 않는다.
		"grok":  PlatformOther,
		"other": PlatformOther,
		"클로드":   PlatformOther,
	}
	for in, want := range cases {
		if got := NormalizePlatform(in); got != want {
			t.Fatalf("NormalizePlatform(%q) = %q (기대 %q)", in, got, want)
		}
	}
}

func TestPlatformUnknownValueIsStoredAsOther(t *testing.T) {
	ctx := fresh(t)
	mustSession(t, ctx, SessionInput{
		SessionID: "s1", Platform: "grok", StartedAt: "2026-08-03T09:00:00.000Z", Output: 1,
	})
	rows, err := SessionRows(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Platform != PlatformOther {
		t.Fatalf("platform = %q (기대 %q)", rows[0].Platform, PlatformOther)
	}
}

// 필터 미지정 = 전체. 이것이 무회귀의 핵심이다 — 기본 동작이 바뀌면 골든이 통째로 깨진다.
func TestSessionRowsPlatformFilter(t *testing.T) {
	ctx := fresh(t)
	mustSession(t, ctx, SessionInput{SessionID: "c1", Platform: "claude", StartedAt: "2026-08-03T09:00:00.000Z", Output: 1})
	mustSession(t, ctx, SessionInput{SessionID: "x1", Platform: "codex", StartedAt: "2026-08-03T10:00:00.000Z", Output: 2})
	mustSession(t, ctx, SessionInput{SessionID: "g1", Platform: "gemini", StartedAt: "2026-08-03T11:00:00.000Z", Output: 4})

	all, err := SessionRows(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("미지정 필터가 전체를 안 준다: %d", len(all))
	}

	for _, want := range []string{"claude", "codex", "gemini"} {
		got, err := SessionRows(ctx, Filter{Platform: want})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Platform != want {
			t.Fatalf("platform=%s 필터: %d행 %+v", want, len(got), got)
		}
	}
}

/*
 * usage_series 에는 platform 컬럼이 없다 — 같은 사실을 두 테이블에 적지 않는다.
 * 대신 세션 행으로 조인해 거른다. 안 거르면 platform=codex 요청이 조용히 전체를 돌려준다.
 */
func TestSeriesRowsPlatformFilter(t *testing.T) {
	ctx := fresh(t)
	mustSession(t, ctx, SessionInput{SessionID: "c1", Platform: "claude", StartedAt: "2026-08-03T09:00:00.000Z"})
	mustSession(t, ctx, SessionInput{SessionID: "x1", Platform: "codex", StartedAt: "2026-08-03T10:00:00.000Z"})
	mustSeries(t, ctx, SeriesInput{SessionID: "c1", Rows: []SeriesRow{{Hour: "2026-08-03T09", Model: "m1", Output: 1, Turns: 1}}})
	mustSeries(t, ctx, SeriesInput{SessionID: "x1", Rows: []SeriesRow{{Hour: "2026-08-03T10", Model: "m2", Output: 2, Turns: 3}}})

	all, err := SeriesRows(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("미지정 필터가 전체를 안 준다: %d", len(all))
	}
	only, err := SeriesRows(ctx, Filter{Platform: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if len(only) != 1 || only[0].SessionID != "x1" {
		t.Fatalf("series platform 필터: %+v", only)
	}

	qt, err := SeriesQualityTotals(ctx, Filter{Platform: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if qt.SessionsWithSeries != 1 || qt.Turns != 3 {
		t.Fatalf("quality platform 필터: %+v", qt)
	}
	qtAll, err := SeriesQualityTotals(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if qtAll.SessionsWithSeries != 2 || qtAll.Turns != 4 {
		t.Fatalf("quality 미지정 필터가 전체가 아니다: %+v", qtAll)
	}
}

func TestPlatformRollup(t *testing.T) {
	ctx := fresh(t)
	mustSession(t, ctx, SessionInput{
		SessionID: "c1", Platform: "claude", Model: "claude-opus-4-8",
		Input: 10, Output: 20, CacheRead: 30, CacheCreate: 40,
		StartedAt: "2026-08-03T09:00:00.000Z",
	})
	mustSession(t, ctx, SessionInput{
		SessionID: "c2", Platform: "", Model: "claude-sonnet-4-5", // 미지정 → claude
		Input: 1, Output: 2, CacheRead: 3, CacheCreate: 4,
		StartedAt: "2026-08-05T09:00:00.000Z",
	})
	mustSession(t, ctx, SessionInput{
		SessionID: "x1", Platform: "codex", Model: "gpt-5",
		Input: 100, Output: 200, StartedAt: "2026-08-04T09:00:00.000Z",
	})

	rows, err := PlatformRollup(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("플랫폼 행 수 = %d (%+v)", len(rows), rows)
	}
	byName := map[string]PlatformRow{}
	for _, r := range rows {
		byName[r.Platform] = r
	}
	c := byName["claude"]
	if c.Sessions != 2 || c.Input != 11 || c.Output != 22 || c.CacheRead != 33 || c.CacheCreate != 44 {
		t.Fatalf("claude 롤업: %+v", c)
	}
	if c.FirstSeen != "2026-08-03T09:00:00.000Z" || c.LastSeen != "2026-08-05T09:00:00.000Z" {
		t.Fatalf("claude 관측 경계: %q ~ %q", c.FirstSeen, c.LastSeen)
	}
	if len(c.Models) != 2 {
		t.Fatalf("claude 모델 분해: %+v", c.Models)
	}
	x := byName["codex"]
	if x.Sessions != 1 || x.Input != 100 || x.Output != 200 {
		t.Fatalf("codex 롤업: %+v", x)
	}
	// 정렬은 결정론이다 — 세션 수 내림차순, 동률은 이름 오름차순.
	if rows[0].Platform != "claude" {
		t.Fatalf("정렬이 흔들린다: %+v", rows)
	}

	// 필터가 걸리면 그 플랫폼만 남는다.
	only, err := PlatformRollup(ctx, Filter{Platform: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if len(only) != 1 || only[0].Platform != "codex" {
		t.Fatalf("롤업 platform 필터: %+v", only)
	}

	// 날짜 경계도 종전 조회구와 같은 규칙(접두 10자)이다.
	win, err := PlatformRollup(ctx, Filter{From: "2026-08-04", To: "2026-08-04"})
	if err != nil {
		t.Fatal(err)
	}
	if len(win) != 1 || win[0].Platform != "codex" {
		t.Fatalf("롤업 날짜 필터: %+v", win)
	}
}

/*
 * 기존 sqlite 파일에 컬럼이 없어도 깨지지 않는다.
 * `CREATE TABLE IF NOT EXISTS` 는 기존 테이블에 새 컬럼을 안 넣는다 — 이 보강이 없으면
 * 옛 DB 로 뜬 서버에서 세션 조회가 통째로 실패한다.
 */
func TestInitAddsPlatformColumnToOldDatabase(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, db.Options{Mode: "local", DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close(); handle = nil }()

	// platform 이 없던 시절의 스키마 + 그 시절의 행 하나.
	if err := d.Exec(ctx, `CREATE TABLE usage_sessions (
		session_id TEXT PRIMARY KEY, machine TEXT, username TEXT, project TEXT, model TEXT,
		input INTEGER NOT NULL DEFAULT 0, output INTEGER NOT NULL DEFAULT 0,
		cache_read INTEGER NOT NULL DEFAULT 0, cache_create INTEGER NOT NULL DEFAULT 0,
		web_search INTEGER NOT NULL DEFAULT 0, web_fetch INTEGER NOT NULL DEFAULT 0,
		turns INTEGER NOT NULL DEFAULT 0, started_at TEXT, reported_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := d.Exec(ctx,
		"INSERT INTO usage_sessions(session_id, started_at) VALUES('old', '2026-08-01T00:00:00.000Z')"); err != nil {
		t.Fatal(err)
	}
	if err := Init(ctx, d); err != nil {
		t.Fatalf("옛 DB 에서 Init 이 죽었다: %v", err)
	}

	rows, err := SessionRows(ctx, Filter{})
	if err != nil {
		t.Fatalf("platform 컬럼을 읽는 질의가 깨졌다: %v", err)
	}
	if len(rows) != 1 || rows[0].Platform != PlatformDefault {
		t.Fatalf("기존 행이 %q 로 채워지지 않았다: %+v", PlatformDefault, rows)
	}
}
