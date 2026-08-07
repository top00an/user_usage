package store

import (
	"context"
	"testing"
	"time"
)

func at(t *testing.T, iso string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// seedKeyword 는 특정 날짜의 키워드 행을 심는다(day 는 startedAt 에서 유도된다).
func seedKeyword(t *testing.T, ctx context.Context, day, key, sid string) {
	t.Helper()
	mustCounters(t, ctx, CountersInput{
		SessionID: sid, Username: "user-a", Machine: "pc-1",
		StartedAt: day + "T00:00:00.000Z",
		Rows:      []CounterRow{{Kind: "keyword", Key: key, Count: 3}},
	})
}

// 기한 지난 키워드만 지우고 최근 것은 남긴다.
func TestPruneKeywordsRemovesOnlyExpired(t *testing.T) {
	ctx := fresh(t)
	now := at(t, "2026-08-03T00:00:00Z")
	seedKeyword(t, ctx, "2026-01-01", "오래된말", "s-old") // 214일 전
	seedKeyword(t, ctx, "2026-07-20", "최근말", "s-new")  // 14일 전

	r, err := PruneKeywordsDetail(ctx, 90, now)
	if err != nil {
		t.Fatal(err)
	}
	if r.Days != 90 {
		t.Fatalf("days=%d", r.Days)
	}
	if r.Cutoff != "2026-05-05" {
		t.Fatalf("cutoff=%s", r.Cutoff)
	}
	if r.Removed != 1 {
		t.Fatalf("removed=%d", r.Removed)
	}
	keys, _ := TopKeys(ctx, "keyword", 0)
	if len(keys) != 1 || keys[0].Key != "최근말" {
		t.Fatalf("%+v", keys)
	}
}

// **다른 축은 기한이 지나도 지우지 않는다** — 보존 대상은 키워드뿐이다.
func TestPruneKeywordsLeavesOtherAxesAlone(t *testing.T) {
	ctx := fresh(t)
	now := at(t, "2026-08-03T00:00:00Z")
	mustCounters(t, ctx, CountersInput{
		SessionID: "s-old2", Username: "user-a", Machine: "pc-1",
		StartedAt: "2026-01-01T00:00:00.000Z",
		Rows: []CounterRow{
			{Kind: "bash", Key: "git", Count: 5},
			{Kind: "keyword", Key: "옛말", Count: 5},
		},
	})
	if _, err := PruneKeywords(ctx, 90, now); err != nil {
		t.Fatal(err)
	}
	if rows, _ := TopKeys(ctx, "bash", 0); len(rows) != 1 {
		t.Fatalf("bash 축은 보존 대상이 아니다: %+v", rows)
	}
	if rows, _ := TopKeys(ctx, "keyword", 0); len(rows) != 0 {
		t.Fatalf("keyword 가 안 지워졌다: %+v", rows)
	}
}

/*
 * 나이를 알 수 없는 행(day NULL)도 지운다 — 개인 발화를 영구 보관하지 않는다.
 *
 * day 는 적재 시점에 항상 채워지므로 NULL 은 스키마 이전에 들어온 잔재이고 나이를 알 수 없다.
 */
func TestPruneKeywordsRemovesRowsOfUnknownAge(t *testing.T) {
	ctx := fresh(t)
	d, _ := conn()
	if err := d.Exec(ctx,
		"INSERT INTO usage_counters(session_id,kind,key,count,day) VALUES('s-null','keyword','미상말',2,NULL)"); err != nil {
		t.Fatal(err)
	}
	if rows, _ := TopKeys(ctx, "keyword", 0); len(rows) != 1 {
		t.Fatalf("심기 실패: %+v", rows)
	}
	if _, err := PruneKeywords(ctx, 90, at(t, "2026-08-03T00:00:00Z")); err != nil {
		t.Fatal(err)
	}
	if rows, _ := TopKeys(ctx, "keyword", 0); len(rows) != 0 {
		t.Fatalf("나이를 모르는 행이 남았다: %+v", rows)
	}
}

// 기본 보존은 90일이고 하한·상한으로 클램프된다.
func TestRetentionDaysClamp(t *testing.T) {
	if RetentionDays(nil) != KeywordRetentionDefault {
		t.Fatal("미설정은 기본값이다")
	}
	if KeywordRetentionDefault != 90 {
		t.Fatalf("기본값이 바뀌었다: %d", KeywordRetentionDefault)
	}
	one := 1
	if got := RetentionDays(&one); got != KeywordRetentionMin {
		t.Fatalf("너무 짧으면 축이 무의미해진다: %d", got)
	}
	big := 99999
	if got := RetentionDays(&big); got != KeywordRetentionMax {
		t.Fatalf("상한: %d", got)
	}
	zero := 0
	if got := RetentionDays(&zero); got != KeywordRetentionMin {
		t.Fatalf("0 은 하한으로 접힌다(현행 Node 동작): %d", got)
	}
}

/*
 * 시간 버킷 보존 — 키워드보다 **훨씬 길게** 둔다.
 *
 * 이 축은 개인 발화가 아니라 숫자뿐이고(지울 사생활 근거가 약하다), 비용 추세를 연 단위로
 * 보는 것이 이 테이블을 만든 이유의 절반이기 때문이다.
 */
func TestPruneSeriesUsesMuchLongerDefault(t *testing.T) {
	ctx := fresh(t)
	mustSeries(t, ctx, SeriesInput{SessionID: "s-new", Rows: []SeriesRow{
		{Hour: "2026-08-03T09", Model: mOpus, Turns: 1, Input: 1},
		{Hour: "2026-08-03T10", Model: mOpus, Turns: 1, Input: 1},
	}})
	mustSeries(t, ctx, SeriesInput{SessionID: "s-old", Rows: []SeriesRow{
		{Hour: "2020-01-01T00", Model: mOpus, Turns: 1, Input: 1},
	}})
	if rows, _ := SeriesRows(ctx, Filter{}); len(rows) != 3 {
		t.Fatalf("심기 실패: %d", len(rows))
	}

	r, err := PruneSeries(ctx, 0, at(t, "2026-08-03T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Removed != 1 {
		t.Fatalf("2020년 버킷만 지워져야 한다: removed=%d", r.Removed)
	}
	if rows, _ := SeriesRows(ctx, Filter{}); len(rows) != 2 {
		t.Fatalf("남은 행 %d", len(rows))
	}
	if r.Days < 365 {
		t.Fatalf("기본 보존 기한이 짧아졌다(%d일) — 비용 추세는 길게 봐야 한다", r.Days)
	}
}

// CutoffDay 는 경계 날짜를 UTC 기준으로 낸다.
func TestCutoffDay(t *testing.T) {
	ninety := 90
	if got := CutoffDay(&ninety, at(t, "2026-08-03T00:00:00Z")); got != "2026-05-05" {
		t.Fatalf("got %s", got)
	}
	if got := CutoffDay(nil, at(t, "2026-08-03T00:00:00Z")); got != "2026-05-05" {
		t.Fatalf("기본값 경로: %s", got)
	}
}

// 시계를 안 주면 주입된 clock 을 쓴다(테스트가 시간을 고정할 수 있어야 한다).
func TestCutoffDayFallsBackToInjectedClock(t *testing.T) {
	freezeClock(t, "2026-08-03T00:00:00Z")
	ninety := 90
	if got := CutoffDay(&ninety, time.Time{}); got != "2026-05-05" {
		t.Fatalf("got %s", got)
	}
}
