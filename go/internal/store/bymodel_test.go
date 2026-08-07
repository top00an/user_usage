package store

import (
	"context"
	"testing"
)

/*
 * 모델별 집계(UsageByModel) — 오귀속을 고치면서 **아무것도 잃지 않는가**.
 *
 * 이 파일이 못박는 불변식은 넷이고, 넷 다 **깨져도 화면은 멀쩡해 보인다**:
 *
 *	① 총합 불변. ①series 몫 + ②③세션 폴백 몫 = usage_sessions 합 = totals 카드.
 *	   series 로 그냥 갈아타면 이게 조용히 줄어든다. 같은 화면의 다른 카드와 어긋나는 순간
 *	   그 차이가 다시 "유실"로 읽힌다.
 *	② 다중모델 세션이 **정확히** 쪼개진다(usage_series 는 PK 에 model 이 있다).
 *	③ series 가 없는 세션이 **사라지지 않는다**. 커버리지는 사람마다 다르다(실측: 91.3% ·
 *	   100% · **2.2%**). 버리면 커버리지 낮은 사람의 모델별이 통째로 사라진다.
 *	④ 근사의 **몫이 밝혀진다**(FromSeries/FromSession + 사람별 커버리지).
 */

const (
	mOpus   = "claude-opus-5"
	mSonnet = "claude-sonnet-5"
	mHaiku  = "claude-haiku-4-5"
)

/*
 * 골든 픽스처 — 브리프에 박힌 수치를 그대로 재현한다.
 *
 *	opus    out=11100  series=3000  session=8100
 *	sonnet  out= 7600  series=6800  session= 800
 *	haiku   out= 1900  series= 800  session=1100
 *	(미상)  out= 1500  series=   0  session=1500
 *	합계 22100 == totals.output 22100
 *
 * 축은 출력에 비례해 만든다(input=out/10 · cacheRead=out*10 · cacheCreate=out/2). 축마다 배율을
 * 달리 두는 이유: 축이 뒤바뀐 회귀(입력 자리에 출력을 넣는 류)가 같은 숫자에 가려지지 않게.
 */
func seedGolden(t *testing.T, ctx context.Context) {
	t.Helper()
	sess := func(id, model string, out int64) {
		mustSession(t, ctx, SessionInput{
			SessionID: id, Machine: "pc-a", Username: "user-a", Model: model,
			StartedAt: "2026-08-03T09:00:00.000Z",
			Input:     out / 10, Output: out, CacheRead: out * 10, CacheCreate: out / 2,
		})
	}
	row := func(hour, model string, out int64) SeriesRow {
		return SeriesRow{
			Hour: hour, Model: model,
			Input: out / 10, Output: out, CacheRead: out * 10, CacheCreate: out / 2,
		}
	}

	// ① series 가 세션을 **부분만** 덮는 두 세션. 잔여가 ③으로 최빈 모델에 남는다.
	sess("sess-mixed-1", mOpus, 6000)
	mustSeries(t, ctx, SeriesInput{SessionID: "sess-mixed-1", Username: "user-a", Machine: "pc-a", Rows: []SeriesRow{
		row("2026-08-03T09", mOpus, 2000),
		row("2026-08-03T09", mSonnet, 1500),
		row("2026-08-03T10", mHaiku, 500),
	}}) // series 합 4000 · 세션 6000 → 잔여 2000 이 opus 로

	sess("sess-mixed-2", mSonnet, 6900)
	mustSeries(t, ctx, SeriesInput{SessionID: "sess-mixed-2", Username: "user-a", Machine: "pc-a", Rows: []SeriesRow{
		row("2026-08-03T11", mSonnet, 5300),
		row("2026-08-03T11", mOpus, 1000),
		row("2026-08-03T12", mHaiku, 300),
	}}) // series 합 6600 · 세션 6900 → 잔여 300 이 sonnet 으로

	// ② series 가 **아예 없는** 세션들(구세대 수집기). 버리면 이 몫이 통째로 사라진다.
	sess("sess-nos-1", mOpus, 6100)
	sess("sess-nos-2", mSonnet, 500)
	sess("sess-nos-3", mHaiku, 1100)
	sess("sess-nos-4", "", 1500) // 모델을 모르는 보고 → '(미상)' 으로 모인다
}

// goldenOutput 은 브리프에 박힌 모델별 출력이다. 손으로 적는다 — 코드로 계산하면 같은 결함을
// 공유해 서로를 통과시킨다.
var goldenOutput = []struct {
	model            string
	total            int64
	series, sessions int64
}{
	{mOpus, 11100, 3000, 8100},
	{mSonnet, 7600, 6800, 800},
	{mHaiku, 1900, 800, 1100},
	{UnknownModel, 1500, 0, 1500},
}

const goldenTotalOutput = 22100

func TestUsageByModelGoldenShares(t *testing.T) {
	ctx := fresh(t)
	seedGolden(t, ctx)

	rows, err := UsageByModel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range goldenOutput {
		r := modelRow(t, rows, want.model)
		if r.Output != want.total {
			t.Errorf("%s.output want %d, got %d", want.model, want.total, r.Output)
		}
		if r.FromSeries.Output != want.series {
			t.Errorf("%s.fromSeries.output want %d, got %d", want.model, want.series, r.FromSeries.Output)
		}
		if r.FromSession.Output != want.sessions {
			t.Errorf("%s.fromSession.output want %d, got %d", want.model, want.sessions, r.FromSession.Output)
		}
	}
}

/*
 * ①+②+③ == Totals — 이 포팅의 핵심 불변식이다.
 *
 * ③(series 가 못 덮은 잔여)이 없으면 총합이 준다. 모델별만 작아지면 사람에게는 "유실"로 보이고,
 * 실제로 그렇게 읽혔다.
 */
func TestUsageByModelSumEqualsTotals(t *testing.T) {
	ctx := fresh(t)
	seedGolden(t, ctx)

	rows, err := UsageByModel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tot, err := Totals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := sumAxes(rows)
	want := axes{Input: tot.Input, Output: tot.Output, CacheRead: tot.CacheRead, CacheCreate: tot.CacheCreate}
	if got != want {
		t.Fatalf("모델별 총합이 totals 카드와 다르다 — 폴백/잔여를 버렸다\nwant %+v\ngot  %+v", want, got)
	}
	if got.Output != goldenTotalOutput {
		t.Fatalf("골든 총합이 아니다: want %d, got %d", goldenTotalOutput, got.Output)
	}
	// 그리고 그 총합은 ①몫 + ②③몫으로 정확히 갈린다.
	series := sumShare(rows, func(r ModelRow) Share { return r.FromSeries })
	session := sumShare(rows, func(r ModelRow) Share { return r.FromSession })
	if series.Output+session.Output != want.Output {
		t.Fatalf("①(%d) + ②③(%d) != Totals(%d)", series.Output, session.Output, want.Output)
	}
}

// 행마다 FromSeries + FromSession = 그 행의 값이고, 음수 몫이 없다.
func TestUsageByModelSharesAddUpPerRow(t *testing.T) {
	ctx := fresh(t)
	seedGolden(t, ctx)
	rows, err := UsageByModel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		type ax struct {
			name          string
			total, se, ss int64
		}
		for _, a := range []ax{
			{"input", r.Input, r.FromSeries.Input, r.FromSession.Input},
			{"output", r.Output, r.FromSeries.Output, r.FromSession.Output},
			{"cacheRead", r.CacheRead, r.FromSeries.CacheRead, r.FromSession.CacheRead},
			{"cacheCreate", r.CacheCreate, r.FromSeries.CacheCreate, r.FromSession.CacheCreate},
		} {
			if a.se+a.ss != a.total {
				t.Errorf("%s.%s 몫 합(%d+%d)이 행 값(%d)과 다르다", r.Model, a.name, a.se, a.ss, a.total)
			}
			if a.se < 0 || a.ss < 0 {
				t.Errorf("%s.%s 에 음수 몫이 나왔다: %d / %d", r.Model, a.name, a.se, a.ss)
			}
		}
	}
}

/*
 * 세션 행이 없는 **고아 버킷**은 총합을 부풀리지 않는다.
 *
 * 라이브에 실재하는 조건이다(인테이크가 세션 행만 실패하고 버킷은 들어가는 자리).
 * 이걸 더하면 모델별만 totals 카드보다 커진다.
 */
func TestUsageByModelIgnoresOrphanBuckets(t *testing.T) {
	ctx := fresh(t)
	seedGolden(t, ctx)

	mustSeries(t, ctx, SeriesInput{
		SessionID: "sess-orphan-9999", Username: "ghost", Machine: "pc-ghost",
		Rows: []SeriesRow{{Hour: "2026-08-03T09", Model: "claude-orphan-9",
			Input: 7, Output: 7000, CacheRead: 70, CacheCreate: 7}},
	})

	rows, err := UsageByModel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := sumAxes(rows).Output; got != goldenTotalOutput {
		t.Fatalf("고아 버킷이 총합에 섞였다: want %d, got %d", goldenTotalOutput, got)
	}
	for _, r := range rows {
		if r.Model == "claude-orphan-9" {
			t.Fatal("세션 행이 없는 모델 행이 생겼다")
		}
	}
}

// 다중모델 세션이 최빈 모델 한 칸으로 뭉치지 않는다 — series 의 모델별 값 그대로다.
func TestUsageByModelSplitsMixedSessionExactly(t *testing.T) {
	ctx := fresh(t)
	mustSession(t, ctx, SessionInput{
		SessionID: "sess-mixed", Machine: "pc-a", Username: "user-a", Model: "claude-opus-4-8",
		StartedAt: "2026-08-03T09:00:00.000Z",
		Input:     300, Output: 3000, CacheRead: 30000, CacheCreate: 3000, Turns: 30,
	})
	mustSeries(t, ctx, SeriesInput{SessionID: "sess-mixed", Username: "user-a", Machine: "pc-a", Rows: []SeriesRow{
		{Hour: "2026-08-03T09", Model: "claude-opus-4-8", Input: 100, Output: 1000, CacheRead: 10000, CacheCreate: 1000, Turns: 10},
		{Hour: "2026-08-03T09", Model: "claude-fable-5", Input: 150, Output: 1500, CacheRead: 15000, CacheCreate: 1500, Turns: 12},
		{Hour: "2026-08-03T10", Model: mHaiku, Input: 50, Output: 500, CacheRead: 5000, CacheCreate: 500, Turns: 8},
	}})

	rows, err := UsageByModel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 종전이라면 이 한 행이 3,000 을 통째로 가졌다(그리고 나머지 두 모델은 행조차 없었다).
	if got := modelRow(t, rows, "claude-opus-4-8").Output; got != 1000 {
		t.Fatalf("최빈 모델이 남의 토큰을 먹고 있다: %d", got)
	}
	if got := modelRow(t, rows, "claude-fable-5").Output; got != 1500 {
		t.Fatalf("fable-5 output=%d", got)
	}
	if got := modelRow(t, rows, mHaiku).CacheRead; got != 5000 {
		t.Fatalf("haiku cacheRead=%d", got)
	}
	// 그리고 그 값들은 전부 series 근거다 — 근사가 섞이지 않았다.
	for _, m := range []string{"claude-opus-4-8", "claude-fable-5", mHaiku} {
		if got := modelRow(t, rows, m).FromSession.Output; got != 0 {
			t.Errorf("%s 이 근사 몫을 갖고 있다: %d", m, got)
		}
	}
}

// series 가 덮지 못한 잔여는 최빈 모델에 남고 **근사로 표시된다**.
func TestUsageByModelResidualIsDisclosedAsApproximation(t *testing.T) {
	ctx := fresh(t)
	mustSession(t, ctx, SessionInput{
		SessionID: "sess-part", Machine: "pc-a", Username: "user-a", Model: mOpus,
		StartedAt: "2026-08-03T11:00:00.000Z",
		Input:     100, Output: 1000, CacheRead: 10000, CacheCreate: 1000, Turns: 20,
	})
	mustSeries(t, ctx, SeriesInput{SessionID: "sess-part", Username: "user-a", Rows: []SeriesRow{
		{Hour: "2026-08-03T11", Model: mOpus, Input: 60, Output: 600, CacheRead: 6000, CacheCreate: 600, Turns: 12},
	}})

	rows, err := UsageByModel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r := modelRow(t, rows, mOpus)
	if r.Output != 1000 {
		t.Fatalf("잔여 40%% 를 버렸다 — 총합이 줄었다: %d", r.Output)
	}
	if r.FromSeries.Output != 600 {
		t.Fatalf("fromSeries=%d", r.FromSeries.Output)
	}
	if r.FromSession.Output != 400 {
		t.Fatalf("잔여가 근사 몫으로 밝혀지지 않았다: %d", r.FromSession.Output)
	}
}

// 구세대 보고만 있는 사람의 모델별이 통째로 남는다.
func TestUsageByModelKeepsSessionsWithoutSeries(t *testing.T) {
	ctx := fresh(t)
	mustSession(t, ctx, SessionInput{SessionID: "s1", Machine: "pc-b", Username: "user-b",
		Model: mSonnet, StartedAt: "2026-08-03T12:00:00.000Z",
		Input: 10, Output: 500, CacheRead: 1000, CacheCreate: 100, Turns: 2})
	mustSession(t, ctx, SessionInput{SessionID: "s2", Machine: "pc-b", Username: "user-b",
		Model: "claude-fable-5", StartedAt: "2026-08-03T13:00:00.000Z",
		Input: 20, Output: 700, CacheRead: 2000, CacheCreate: 200, Turns: 3})

	rows, err := UsageByModel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := modelRow(t, rows, mSonnet).Output; got != 500 {
		t.Fatalf("sonnet output=%d", got)
	}
	if got := modelRow(t, rows, mSonnet).FromSession.Sessions; got != 1 {
		t.Fatalf("sonnet fromSession.sessions=%d", got)
	}
	if got := sumAxes(rows); got != (axes{Input: 30, Output: 1200, CacheRead: 3000, CacheCreate: 300}) {
		t.Fatalf("구세대 세션이 사라졌다: %+v", got)
	}
}

// 신·구 세대가 섞여도 같은 모델 행에서 두 근거가 나란히 더해진다.
func TestUsageByModelMergesBothEvidencesInOneRow(t *testing.T) {
	ctx := fresh(t)
	seedGolden(t, ctx)
	r := modelRow(t, mustRows(t, ctx), mSonnet)
	if r.FromSeries.Output != 6800 {
		t.Fatalf("series 몫=%d", r.FromSeries.Output)
	}
	if r.FromSession.Output != 800 {
		t.Fatalf("폴백 몫=%d", r.FromSession.Output)
	}
	if r.Output != 7600 {
		t.Fatalf("합=%d", r.Output)
	}
}

// 세션 수는 두 번 세지 않는다 — ③의 세션은 ①에서 이미 세었다.
func TestUsageByModelDoesNotDoubleCountSessions(t *testing.T) {
	ctx := fresh(t)
	seedGolden(t, ctx)
	rows := mustRows(t, ctx)
	for _, want := range []struct {
		model            string
		series, sessions int
	}{
		{mOpus, 2, 1}, {mSonnet, 2, 1}, {mHaiku, 2, 1}, {UnknownModel, 0, 1},
	} {
		r := modelRow(t, rows, want.model)
		if r.FromSeries.Sessions != want.series || r.FromSession.Sessions != want.sessions {
			t.Errorf("%s sessions want (%d,%d), got (%d,%d)",
				want.model, want.series, want.sessions, r.FromSeries.Sessions, r.FromSession.Sessions)
		}
	}
}

// 정렬은 결정론이다 — 출력 내림차순, 동률은 모델명 오름차순.
func TestUsageByModelSortIsDeterministic(t *testing.T) {
	ctx := fresh(t)
	seedGolden(t, ctx)
	rows := mustRows(t, ctx)
	want := []string{mOpus, mSonnet, mHaiku, UnknownModel}
	if len(rows) != len(want) {
		t.Fatalf("행 수 %d, want %d", len(rows), len(want))
	}
	for i, m := range want {
		if rows[i].Model != m {
			t.Fatalf("정렬이 다르다 [%d]: want %s, got %s", i, m, rows[i].Model)
		}
	}
}

/*
 * ④ 근거를 밝힌다 — 사람별 커버리지.
 *
 * 커버리지가 낮은 사람이 그대로 드러나야 한다 — 지금은 DB 를 열어야만 보인다.
 */
func TestUsageModelAxisReportsPerUserCoverage(t *testing.T) {
	ctx := fresh(t)
	// user-a: 2세션 모두 series 있음 / user-b: 3세션 중 1개만
	mustSession(t, ctx, SessionInput{SessionID: "a1", Username: "user-a", Machine: "pc-a", Model: mOpus, StartedAt: "2026-08-03T09:00:00.000Z", Output: 100})
	mustSeries(t, ctx, SeriesInput{SessionID: "a1", Username: "user-a", Rows: []SeriesRow{{Hour: "2026-08-03T09", Model: mOpus, Output: 100}}})
	mustSession(t, ctx, SessionInput{SessionID: "a2", Username: "user-a", Machine: "pc-a", Model: mOpus, StartedAt: "2026-08-03T10:00:00.000Z", Output: 100})
	mustSeries(t, ctx, SeriesInput{SessionID: "a2", Username: "user-a", Rows: []SeriesRow{{Hour: "2026-08-03T10", Model: mOpus, Output: 100}}})

	mustSession(t, ctx, SessionInput{SessionID: "b1", Username: "user-b", Machine: "pc-b", Model: mOpus, StartedAt: "2026-08-03T09:00:00.000Z", Output: 100})
	mustSession(t, ctx, SessionInput{SessionID: "b2", Username: "user-b", Machine: "pc-b", Model: mOpus, StartedAt: "2026-08-03T10:00:00.000Z", Output: 100})
	mustSession(t, ctx, SessionInput{SessionID: "b3", Username: "user-b", Machine: "pc-b", Model: mOpus, StartedAt: "2026-08-03T11:00:00.000Z", Output: 100})
	mustSeries(t, ctx, SeriesInput{SessionID: "b3", Username: "user-b", Rows: []SeriesRow{{Hour: "2026-08-03T11", Model: mOpus, Output: 100}}})

	ax, err := UsageModelAxis(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byUser := map[string]ModelAxisUser{}
	for _, u := range ax.Users {
		byUser[u.Username] = u
	}
	if u := byUser["user-a"]; u.Sessions != 2 || u.WithSeries != 2 {
		t.Errorf("user-a want (2,2), got (%d,%d)", u.Sessions, u.WithSeries)
	}
	if u := byUser["user-b"]; u.Sessions != 3 || u.WithSeries != 1 {
		t.Errorf("커버리지가 낮은 사람이 드러나야 한다: user-b got (%d,%d)", u.Sessions, u.WithSeries)
	}
	if ax.Sessions != 5 || ax.WithSeries != 3 || ax.OverSessions != 0 {
		t.Errorf("ax=%+v", ax)
	}
}

/*
 * series 합이 세션 행보다 크면 0 에서 끊고 그 건수를 센다 — **조용히 덮지 않는다.**
 */
func TestUsageModelAxisCountsOverSessions(t *testing.T) {
	ctx := fresh(t)
	mustSession(t, ctx, SessionInput{
		SessionID: "sess-over", Username: "over", Machine: "pc-x", Model: mOpus,
		StartedAt: "2026-08-03T09:00:00.000Z",
		Input:     10, Output: 100, CacheRead: 1000, CacheCreate: 10, Turns: 2,
	})
	mustSeries(t, ctx, SeriesInput{SessionID: "sess-over", Username: "over", Rows: []SeriesRow{
		{Hour: "2026-08-03T09", Model: mOpus, Input: 99, Output: 999, CacheRead: 9999, CacheCreate: 99, Turns: 2},
	}})

	ax, err := UsageModelAxis(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ax.OverSessions != 1 {
		t.Fatalf("초과분이 조용히 사라졌다: overSessions=%d", ax.OverSessions)
	}
	r := modelRow(t, mustRows(t, ctx), mOpus)
	if r.FromSession.Output != 0 {
		t.Fatalf("잔여가 음수로 새어 나왔다: %d", r.FromSession.Output)
	}
	for _, v := range []int64{r.Input, r.Output, r.CacheRead, r.CacheCreate} {
		if v < 0 {
			t.Fatalf("음수 축이 나왔다: %+v", r)
		}
	}
}

// 보고가 하나도 없어도 응답이 성립한다(빈 슬라이스이지 nil 이 아니다).
func TestUsageByModelEmptyIsWellFormed(t *testing.T) {
	ctx := fresh(t)
	rows, err := UsageByModel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rows == nil || len(rows) != 0 {
		t.Fatalf("want empty slice, got %v", rows)
	}
	ax, err := UsageModelAxis(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ax.Users == nil || len(ax.Users) != 0 || ax.Sessions != 0 {
		t.Fatalf("ax=%+v", ax)
	}
}

func mustRows(t *testing.T, ctx context.Context) []ModelRow {
	t.Helper()
	rows, err := UsageByModel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

/*
 * no_ts_turns 의 NULL 과 0 을 뭉치지 않는다.
 *
 * NULL = "구버전 수집기라 **모른다**" · 0 = "전 턴에 시각이 있었다". 모르는 것을 0 으로 접으면
 * 화면이 "시각 문제 없음" 이라고 단정하고, 사람은 커버리지가 낮은 이유를 엉뚱한 데(토큰·서버
 * 주소)에서 찾는다. 그 구분이 이 컬럼이 존재하는 이유의 전부다(migrations/pg/0026 주석).
 */
func TestNoTsTurnsSeparatesUnknownFromZero(t *testing.T) {
	ctx := fresh(t)
	zero := int64(0)
	five := int64(5)

	// 구버전 수집기 — 이 값을 아예 안 보낸다(모른다).
	mustSession(t, ctx, SessionInput{SessionID: "old", Username: "u", Machine: "m",
		StartedAt: "2026-08-03T09:00:00.000Z", Output: 1})
	// 신버전 — 전 턴에 시각이 있었다(0 이라고 **주장한다**).
	mustSession(t, ctx, SessionInput{SessionID: "new-ok", Username: "u", Machine: "m",
		StartedAt: "2026-08-03T10:00:00.000Z", Output: 1, NoTsTurns: &zero})
	// 신버전 — 5턴이 시각 없이 왔다.
	mustSession(t, ctx, SessionInput{SessionID: "new-bad", Username: "u", Machine: "m",
		StartedAt: "2026-08-03T11:00:00.000Z", Output: 1, NoTsTurns: &five})

	ax, err := UsageModelAxis(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ax.Users) != 1 {
		t.Fatalf("%+v", ax.Users)
	}
	u := ax.Users[0]
	if u.NoTsUnknown != 1 {
		t.Fatalf("'모른다'가 '없다'로 접혔다 — 구버전 PC 를 정상으로 단정한다: noTsUnknown=%d", u.NoTsUnknown)
	}
	if u.NoTsTurns != 5 {
		t.Fatalf("NULL 을 세면 안 되고 5 만 세야 한다: noTsTurns=%d", u.NoTsTurns)
	}
}
