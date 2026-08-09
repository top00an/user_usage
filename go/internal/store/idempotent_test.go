package store

import (
	"context"
	"reflect"
	"testing"
)

/*
 * 멱등성 — 재전송이 값을 부풀리지 않는가.
 *
 * 수집기는 트랜스크립트를 다시 읽어 세션 **절대값**을 보낸다(델타가 아니다). 저장이 `+=` 로
 * 바뀌는 순간 훅이 두 번 도는 것만으로 값이 두 배가 되고, 화면에는 "사용량이 늘었다"로 보인다.
 * 훅은 실패해도 재시도하는 best-effort 경로라 **중복 전송이 정상 동작에 포함된다.**
 */

func seedOne(t *testing.T, ctx context.Context, output int64, gitCount int64) {
	t.Helper()
	const sid = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	mustSession(t, ctx, SessionInput{
		SessionID: sid, Machine: "pc-1", Username: "user-a", Model: mOpus,
		StartedAt: "2026-08-03T09:00:00.000Z",
		Input:     10, Output: output, CacheRead: 90000, CacheCreate: 500, Turns: 30,
	})
	mustCounters(t, ctx, CountersInput{
		SessionID: sid, Username: "user-a", Machine: "pc-1", StartedAt: "2026-08-03T09:00:00.000Z",
		Rows: []CounterRow{{Kind: "bash", Key: "git", Count: gitCount}, {Kind: "tool", Key: "Bash", Count: 40}},
	})
}

func TestSessionUpsertIsIdempotent(t *testing.T) {
	ctx := fresh(t)
	seedOne(t, ctx, 2000, 12)
	seedOne(t, ctx, 2000, 12)
	seedOne(t, ctx, 2000, 12)

	tot, err := Totals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tot.Sessions != 1 {
		t.Fatalf("세션이 늘었다: %d", tot.Sessions)
	}
	if tot.Output != 2000 {
		t.Fatalf("재전송이 누적됐다: output=%d", tot.Output)
	}
	keys, err := TopKeys(ctx, "bash", 0)
	if err != nil {
		t.Fatal(err)
	}
	want := KeyRow{Key: "git", Count: 12, Sessions: 1, Users: 1}
	if len(keys) != 1 || keys[0] != want {
		t.Fatalf("카운터가 누적됐다: %+v", keys)
	}
}

// 세션이 이어져 값이 커지면 **최신값으로 덮인다**(더해지지 않는다).
func TestSessionUpsertOverwritesWithLatestAbsoluteValue(t *testing.T) {
	ctx := fresh(t)
	seedOne(t, ctx, 2000, 12)
	seedOne(t, ctx, 5000, 20)

	tot, _ := Totals(ctx)
	if tot.Output != 5000 {
		t.Fatalf("최신 절대값으로 덮여야 한다: %d", tot.Output)
	}
	keys, _ := TopKeys(ctx, "bash", 0)
	if keys[0].Count != 20 {
		t.Fatalf("카운터가 최신값이 아니다: %d", keys[0].Count)
	}
}

// 같은 페이로드를 두 번 넣어도 시간 버킷의 **행 수와 값이 그대로**다.
func TestSeriesUpsertIsIdempotent(t *testing.T) {
	ctx := fresh(t)
	in := SeriesInput{SessionID: "sess-idem01", Username: "user-a", Machine: "pc-1", Rows: []SeriesRow{
		{Hour: "2026-08-03T09", Model: mOpus, Input: 200, Output: 2000, CacheRead: 800000,
			CacheCreate: 12000, CC5m: 0, CC1h: 12000, Turns: 25,
			ToolErrors: 2, StopMaxTokens: 1, LatencyMsSum: 125000, LatencyMsMax: 30000, LatencyTurns: 25},
		{Hour: "2026-08-03T10", Model: mHaiku, Input: 50, Output: 700, CacheRead: 300000,
			CacheCreate: 6000, CC5m: 6000, CC1h: 0, Turns: 10, StopRefusal: 1,
			LatencyMsSum: 40000, LatencyMsMax: 12000, LatencyTurns: 10},
	}}
	mustSession(t, ctx, SessionInput{SessionID: "sess-idem01", Username: "user-a", Machine: "pc-1",
		StartedAt: "2026-08-03T09:10:00.000Z", Model: mOpus, Output: 2700})

	mustSeries(t, ctx, in)
	once, err := SeriesOf(ctx, "sess-idem01")
	if err != nil {
		t.Fatal(err)
	}
	mustSeries(t, ctx, in)
	twice, err := SeriesOf(ctx, "sess-idem01")
	if err != nil {
		t.Fatal(err)
	}
	if len(twice) != len(once) {
		t.Fatalf("행이 늘었다 — PK 가 안 걸렸다: %d → %d", len(once), len(twice))
	}
	if !reflect.DeepEqual(once, twice) {
		t.Fatalf("값이 변했다 — 누적(+=)으로 회귀했다\n%+v\n%+v", once, twice)
	}
}

// 열 번 넣어도 마찬가지다(훅 재시도의 실제 모양).
func TestSeriesUpsertTenTimes(t *testing.T) {
	ctx := fresh(t)
	in := SeriesInput{SessionID: "s", Username: "u", Rows: []SeriesRow{
		{Hour: "2026-08-03T09", Model: mOpus, CacheRead: 800000, Turns: 25},
		{Hour: "2026-08-03T10", Model: mHaiku, CacheRead: 300000, Turns: 10},
	}}
	for i := 0; i < 10; i++ {
		mustSeries(t, ctx, in)
	}
	rows, _ := SeriesOf(ctx, "s")
	if len(rows) != 2 {
		t.Fatalf("행 수 %d", len(rows))
	}
	for _, r := range rows {
		if r.Hour == "2026-08-03T09" {
			if r.CacheRead != 800000 || r.Turns != 25 {
				t.Fatalf("값이 누적됐다: %+v", r)
			}
		}
	}
}

// 같은 세션·같은 버킷이 더 자란 뒤 재전송되는 정상 경로 — 그 값으로 덮인다.
func TestSeriesUpsertOverwritesGrownBucket(t *testing.T) {
	ctx := fresh(t)
	mustSeries(t, ctx, SeriesInput{SessionID: "s", Rows: []SeriesRow{
		{Hour: "2026-08-03T09", Model: mOpus, CacheRead: 800000, Turns: 25},
	}})
	mustSeries(t, ctx, SeriesInput{SessionID: "s", Rows: []SeriesRow{
		{Hour: "2026-08-03T09", Model: mOpus, CacheRead: 900000, Turns: 40},
	}})
	rows, _ := SeriesOf(ctx, "s")
	if len(rows) != 1 || rows[0].Turns != 40 || rows[0].CacheRead != 900000 {
		t.Fatalf("최신 절대값으로 덮이지 않았다: %+v", rows)
	}
}

/*
 * 골든 픽스처를 두 번 심어도 모델별 값이 그대로다.
 *
 * ①②③ 세 경로가 모두 걸린 데이터로 멱등성을 본다 — 어느 한 경로가 누적으로 회귀하면
 * 총합이 두 배가 되거나 몫만 어긋난다.
 */
func TestGoldenSeedIsIdempotent(t *testing.T) {
	ctx := fresh(t)
	seedGolden(t, ctx)
	first := mustRows(t, ctx)
	seedGolden(t, ctx)
	second := mustRows(t, ctx)

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("두 번 심으니 모델별이 달라졌다 — 누적으로 회귀했다\n%+v\n%+v", first, second)
	}
	if got := sumAxes(second).Output; got != goldenTotalOutput {
		t.Fatalf("총합이 부풀었다: want %d, got %d", goldenTotalOutput, got)
	}
	tot, _ := Totals(ctx)
	if tot.Sessions != 6 {
		t.Fatalf("세션이 늘었다: %d", tot.Sessions)
	}
}

/*
 * 서버측 계측은 **누적이다**(호출마다 +1).
 *
 * 클라이언트 보고는 절대값을 덮어쓰지만, 서버가 직접 세는 축(mcp)은 이벤트마다 하나씩 는다.
 * 읽고-고쳐-쓰면 동시 요청에서 카운트가 샌다 — 한 문장 UPSERT 로 DB 가 더하게 한다.
 */
func TestCounterBumpAccumulates(t *testing.T) {
	ctx := fresh(t)
	for _, k := range []string{"search_kb", "search_kb", "get_kb"} {
		if err := CounterBump(ctx, CounterBumpInput{Kind: "mcp", Key: k, Day: "2026-08-03"}); err != nil {
			t.Fatal(err)
		}
	}
	rows, _ := TopKeys(ctx, "mcp", 0)
	got := map[string]int64{}
	for _, r := range rows {
		got[r.Key] = r.Count
	}
	if got["search_kb"] != 2 || got["get_kb"] != 1 {
		t.Fatalf("서버 계측이 누적되지 않았다: %v", got)
	}
}

// 모르는 축은 CounterBump 도 거부한다 — 오타 하나가 집계 축을 늘리지 못하게.
func TestCounterBumpRejectsUnknownKind(t *testing.T) {
	ctx := fresh(t)
	if err := CounterBump(ctx, CounterBumpInput{Kind: "bogus", Key: "x"}); err == nil {
		t.Fatal("모르는 축을 받았다")
	}
	if err := CounterBump(ctx, CounterBumpInput{Kind: "mcp", Key: "   "}); err == nil {
		t.Fatal("빈 키를 받았다")
	}
}

// 모르는 축·빈 키·0 이하 카운트는 저장 단계에서 최종적으로 걸린다(클라이언트 검증을 신뢰하지 않는다).
func TestCountersUpsertNarrowsAxesAtWrite(t *testing.T) {
	ctx := fresh(t)
	n := mustCounters(t, ctx, CountersInput{
		SessionID: "s", Username: "u", Machine: "m", StartedAt: "2026-08-03T00:00:00.000Z",
		Rows: []CounterRow{
			{Kind: "bogus", Key: "x", Count: 9},   // 모르는 축
			{Kind: "bash", Key: "  ", Count: 3},   // 빈 키
			{Kind: "bash", Key: "git", Count: 0},  // 0
			{Kind: "bash", Key: "npm", Count: -3}, // 음수
			{Kind: "bash", Key: "node", Count: 2}, // 유일하게 유효
		},
	})
	if n != 1 {
		t.Fatalf("저장된 행 수 %d, want 1", n)
	}
	keys, _ := TopKeys(ctx, "bash", 0)
	if len(keys) != 1 || keys[0].Key != "node" {
		t.Fatalf("%+v", keys)
	}
}

// 세션 id 가 없으면 저장할 자리가 없다.
func TestUpsertsRejectEmptySessionID(t *testing.T) {
	ctx := fresh(t)
	if err := SessionUpsert(ctx, SessionInput{Output: 5}); err == nil {
		t.Fatal("세션 id 없이 저장됐다")
	}
	if _, err := SeriesUpsertN(ctx, SeriesInput{Rows: []SeriesRow{{Hour: "2026-08-03T09"}}}); err == nil {
		t.Fatal("세션 id 없이 버킷이 저장됐다")
	}
}
