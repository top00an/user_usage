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
		/*
		 * antigravity 는 gemini 와 **다른 값이다.**
		 *
		 * 사용자가 "Gemini CLI" 라 부르는 것이 실제로는 Google Antigravity CLI(agy) 인데,
		 * 둘은 수집 가능 범위가 다르다 — 오픈소스 gemini-cli 는 세션 파일에서 도구·MCP·LOC 까지
		 * 나오지만 antigravity 는 statusLine 의 토큰·모델·세션·프로젝트가 전부다.
		 * 한 값으로 접으면 화면의 "미수집/해당없음" 지원표가 통째로 거짓이 된다.
		 */
		"antigravity":   "antigravity",
		"  Antigravity": "antigravity",
		"ANTIGRAVITY":   "antigravity",
		// 허용목록 밖은 **거부하지도 claude 로 접지도 않는다** — 조용한 오분류를 만들지 않는다.
		"grok":         PlatformOther,
		"other":        PlatformOther,
		"클로드":          PlatformOther,
		"antigrav":     PlatformOther, // 오타는 antigravity 로 붙지 않는다(접두 매칭 없음)
		"gravity":      PlatformOther,
		"antigra vity": PlatformOther,
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
	mustSession(t, ctx, SessionInput{SessionID: "a1", Platform: "antigravity", StartedAt: "2026-08-03T12:00:00.000Z", Output: 8})

	all, err := SessionRows(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("미지정 필터가 전체를 안 준다: %d", len(all))
	}

	for _, want := range []string{"claude", "codex", "gemini", "antigravity"} {
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
 * 필터 허용값은 허용목록에서 **파생된다** — 목록에 값을 더하면 필터도 같이 따라와야 한다.
 * 두 벌로 적히면 저장은 되는데 조회는 400 인 값이 생기고, 그 데이터는 화면에서 영원히 안 보인다.
 */
func TestIsPlatformFilterCoversAllowlistPlusOther(t *testing.T) {
	for _, v := range append(append([]string{}, Platforms...), PlatformOther) {
		if !IsPlatformFilter(v) {
			t.Fatalf("허용목록의 %q 가 필터값이 아니다", v)
		}
	}
	if !IsPlatformFilter("antigravity") {
		t.Fatal("antigravity 를 필터로 못 보낸다 — 저장은 되는데 조회가 400 이 된다")
	}
	// 오타는 그대로 거절한다(호출부가 400 을 낸다). other 로 접으면 요청과 다른 집합이 돌아온다.
	for _, v := range []string{"antigrav", "Antigravity", "ANTIGRAVITY", "gemini-cli", ""} {
		if IsPlatformFilter(v) {
			t.Fatalf("%q 가 필터값으로 통과했다", v)
		}
	}
}

/*
 * antigravity 와 gemini 는 **집계에서 갈린다.**
 *
 * 같은 Google 모델을 쓰므로 model 로는 구분되지 않는다. 여기서 합쳐지면 화면은 "gemini 는
 * 도구를 안 쓴다"(실은 antigravity 라 못 잰다)는 없는 결론을 만든다.
 */
func TestPlatformRollupSeparatesAntigravityFromGemini(t *testing.T) {
	ctx := fresh(t)
	mustSession(t, ctx, SessionInput{
		SessionID: "g1", Platform: "gemini", Model: "gemini-3-pro",
		Input: 1000, Output: 2000, CacheRead: 3000, StartedAt: "2026-08-03T09:00:00.000Z",
	})
	mustSession(t, ctx, SessionInput{
		SessionID: "a1", Platform: "antigravity", Model: "gemini-3-pro",
		Input: 10, Output: 20, CacheRead: 30, StartedAt: "2026-08-04T09:00:00.000Z",
	})
	mustSession(t, ctx, SessionInput{
		SessionID: "a2", Platform: "antigravity", Model: "gemini-3-pro",
		Input: 1, Output: 2, CacheRead: 3, StartedAt: "2026-08-05T09:00:00.000Z",
	})

	rows, err := PlatformRollup(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("플랫폼 행 수 = %d (%+v) — 같은 모델이라고 접히면 안 된다", len(rows), rows)
	}
	by := map[string]PlatformRow{}
	for _, r := range rows {
		by[r.Platform] = r
	}
	// 자릿수를 갈라 뒀다 — 합만 보면 두 몫이 뒤바뀌어도 통과한다.
	if g := by["gemini"]; g.Sessions != 1 || g.Input != 1000 || g.Output != 2000 || g.CacheRead != 3000 {
		t.Fatalf("gemini 롤업: %+v", g)
	}
	a := by["antigravity"]
	if a.Sessions != 2 || a.Input != 11 || a.Output != 22 || a.CacheRead != 33 {
		t.Fatalf("antigravity 롤업: %+v", a)
	}
	if a.FirstSeen != "2026-08-04T09:00:00.000Z" || a.LastSeen != "2026-08-05T09:00:00.000Z" {
		t.Fatalf("antigravity 관측 경계: %q ~ %q", a.FirstSeen, a.LastSeen)
	}
	// 정렬은 결정론 — 세션 수 내림차순이라 antigravity(2)가 gemini(1)보다 앞이다.
	if rows[0].Platform != "antigravity" {
		t.Fatalf("정렬이 흔들린다: %+v", rows)
	}

	// 배타 — 한쪽 필터에 다른 쪽 값이 한 톨도 안 섞인다.
	only, err := PlatformRollup(ctx, Filter{Platform: "antigravity"})
	if err != nil {
		t.Fatal(err)
	}
	if len(only) != 1 || only[0].Platform != "antigravity" || only[0].Input != 11 {
		t.Fatalf("롤업 antigravity 필터: %+v", only)
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
