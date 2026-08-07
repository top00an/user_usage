package store

import (
	"fmt"
	"testing"
)

/*
 * 시간 버킷(usage_series) — 저장 왕복의 계약.
 *
 * 잡으려는 회귀는 **배포 후에야 드러나는** 종류다: 세대 공존(구버전 수집기가 series 를 안 보낸다) ·
 * TTL 분해 보존(5분과 1시간을 뭉뚱그리면 비용이 1.6배 틀린다) · 품질 축 왕복.
 */

// 시간 라벨 형식이 아닌 버킷은 조용히 버린다 — 시계열에 올릴 자리가 없다.
func TestSeriesUpsertDropsNonHourLabels(t *testing.T) {
	ctx := fresh(t)
	n := mustSeries(t, ctx, SeriesInput{SessionID: "s", Rows: []SeriesRow{
		{Hour: "2026-08-03", Model: "m", Turns: 1}, // 날짜만 — 시간이 없다
		{Hour: "yesterday", Model: "m", Turns: 1},
		{Hour: "", Model: "m", Turns: 1},
		{Hour: "2026-08-03T9", Model: "m", Turns: 1},  // 한 자리 시각
		{Hour: "2026-08-03T09", Model: "m", Turns: 1}, // 유일하게 유효
	}})
	if n != 1 {
		t.Fatalf("저장된 버킷 수 %d, want 1", n)
	}
	rows, _ := SeriesOf(ctx, "s")
	if len(rows) != 1 || rows[0].Hour != "2026-08-03T09" {
		t.Fatalf("%+v", rows)
	}
}

/*
 * ⚠ 전체 ISO 시각('2026-08-03T09:00:00Z')은 여기서 **잘려서 통과한다.**
 *
 * 13자로 clip 한 뒤 형식을 보기 때문이다(현행 lib/store.js:224~225 와 같은 순서). 이 동작을
 * 여기 못박아 두는 이유는 그게 좋아서가 아니라, **어디가 진짜 관문인지**를 남기기 위해서다:
 * 형식 검증의 단일 출처는 인테이크(intake.NormSession)이고 store 는 마지막 방어선일 뿐이다.
 * 이 테스트가 없으면 다음 사람이 "store 가 형식을 본다"고 믿고 인테이크 쪽 검증을 지운다.
 *
 * 잘린 결과가 **그 시각이 속한 시간 버킷과 같다**는 점도 이 동작이 무해한 이유다.
 */
func TestSeriesUpsertTruncatesFullISOToItsHourBucket(t *testing.T) {
	ctx := fresh(t)
	n := mustSeries(t, ctx, SeriesInput{SessionID: "s", Rows: []SeriesRow{
		{Hour: "2026-08-03T09:00:00Z", Model: "m", Turns: 1},
	}})
	if n != 1 {
		t.Fatalf("저장 수 %d", n)
	}
	rows, _ := SeriesOf(ctx, "s")
	if len(rows) != 1 || rows[0].Hour != "2026-08-03T09" {
		t.Fatalf("잘린 라벨이 그 시각의 시간 버킷과 다르다: %+v", rows)
	}
}

// 모델이 비면 '(미상)' 으로 모은다 — 빈 문자열 키를 만들지 않는다.
func TestSeriesUpsertEmptyModelBecomesUnknown(t *testing.T) {
	ctx := fresh(t)
	mustSeries(t, ctx, SeriesInput{SessionID: "s", Rows: []SeriesRow{{Hour: "2026-08-03T09", Turns: 1}}})
	rows, _ := SeriesOf(ctx, "s")
	if len(rows) != 1 || rows[0].Model != UnknownModel {
		t.Fatalf("%+v", rows)
	}
}

// 같은 (시간, 모델)이 중복으로 오면 하나만 남는다 — PK 다.
func TestSeriesUpsertDeduplicatesByPrimaryKey(t *testing.T) {
	ctx := fresh(t)
	mustSeries(t, ctx, SeriesInput{SessionID: "s", Rows: []SeriesRow{
		{Hour: "2026-08-03T09", Model: "a", Turns: 1},
		{Hour: "2026-08-03T09", Model: "a", Turns: 9},
		{Hour: "2026-08-03T09", Model: "b", Turns: 1},
	}})
	rows, _ := SeriesOf(ctx, "s")
	if len(rows) != 2 {
		t.Fatalf("(시간,모델)은 PK 다 — 중복이 행 수를 부풀리면 안 된다: %d", len(rows))
	}
}

// 세션당 버킷 수에 상한이 있다 — 없으면 세션 하나가 테이블을 채운다.
func TestSeriesUpsertCapsRowsPerSession(t *testing.T) {
	ctx := fresh(t)
	var many []SeriesRow
	for d := 1; d <= 20; d++ {
		for h := 0; h < 24; h++ {
			many = append(many, SeriesRow{
				Hour:  fmt.Sprintf("2026-08-%02dT%02d", d, h),
				Model: mOpus, Turns: 1, Input: 1,
			})
		}
	}
	n := mustSeries(t, ctx, SeriesInput{SessionID: "s", Rows: many})
	if n > MaxSeriesPerSession {
		t.Fatalf("상한이 없으면 세션 하나가 테이블을 채운다 (%d)", n)
	}
	rows, _ := SeriesOf(ctx, "s")
	if len(rows) > MaxSeriesPerSession {
		t.Fatalf("저장된 행이 상한을 넘었다: %d", len(rows))
	}
}

// 한 세션의 카운터 행에도 상한이 있다.
func TestCountersUpsertCapsRowsPerSession(t *testing.T) {
	ctx := fresh(t)
	var many []CounterRow
	for i := 0; i < MaxCountersPerSession+50; i++ {
		many = append(many, CounterRow{Kind: "keyword", Key: fmt.Sprintf("k%d", i), Count: 1})
	}
	n := mustCounters(t, ctx, CountersInput{SessionID: "s", StartedAt: "2026-08-03T00:00:00.000Z", Rows: many})
	if n != MaxCountersPerSession {
		t.Fatalf("상한이 안 걸렸다: %d", n)
	}
}

/*
 * TTL 분해가 저장 왕복에서 **살아남는다.**
 *
 * 캐시 생성은 5분이면 1.25배, 1시간이면 2배다. 실측에서 표본의 100%가 1시간이었고, 5분으로
 * 뭉뚱그리던 계산은 실제의 1/1.60 이었다. 분해값이 저장에서 뭉개지면 비용이 그만큼 낮아지는데,
 * 그 회귀는 총액이 조용히 낮아지는 형태로만 나타난다.
 */
func TestSeriesRoundTripPreservesTTLSplitAndQualityAxes(t *testing.T) {
	ctx := fresh(t)
	mustSeries(t, ctx, SeriesInput{SessionID: "s", Username: "u", Machine: "m", Project: "p", Rows: []SeriesRow{
		{Hour: "2026-08-03T09", Model: mOpus, Input: 200, Output: 2000, CacheRead: 800000,
			CacheCreate: 12000, CC5m: 0, CC1h: 12000, Turns: 25,
			ToolErrors: 2, StopMaxTokens: 1, StopRefusal: 0,
			LatencyMsSum: 125000, LatencyMsMax: 30000, LatencyTurns: 25},
		{Hour: "2026-08-03T10", Model: mHaiku, CacheCreate: 6000, CC5m: 6000, CC1h: 0, Turns: 10},
	}})
	rows, err := SeriesOf(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	byHour := map[string]Bucket{}
	for _, r := range rows {
		byHour[r.Hour] = r
	}
	h9 := byHour["2026-08-03T09"]
	if h9.CacheCreate1h != 12000 || h9.CacheCreate5m != 0 {
		t.Fatalf("TTL 분해가 뭉개졌다: 5m=%d 1h=%d", h9.CacheCreate5m, h9.CacheCreate1h)
	}
	if h10 := byHour["2026-08-03T10"]; h10.CacheCreate5m != 6000 || h10.CacheCreate1h != 0 {
		t.Fatalf("TTL 분해가 뭉개졌다: %+v", h10)
	}
	// 품질 축과 지연도 왕복에서 보존된다.
	if h9.ToolErrors != 2 || h9.StopMaxTokens != 1 || h9.LatencyTurns != 25 || h9.LatencyMsMax != 30000 {
		t.Fatalf("품질 축이 유실됐다: %+v", h9)
	}
	if h9.Username != "u" || h9.Machine != "m" || h9.Project != "p" {
		t.Fatalf("발신 정보가 유실됐다: %+v", h9)
	}
}

// 세션 종료 시각이 저장된다 — duration 을 물을 수 있다. 안 보낸 값은 "모른다"로 남는다.
func TestSessionEndedAtIsStoredAndUnknownStaysNull(t *testing.T) {
	ctx := fresh(t)
	mustSession(t, ctx, SessionInput{SessionID: "new", StartedAt: "2026-08-03T09:10:00.000Z",
		EndedAt: "2026-08-03T10:20:00.000Z", Turns: 3})
	mustSession(t, ctx, SessionInput{SessionID: "old", StartedAt: "2026-08-03T09:00:00.000Z", Turns: 3})

	s, err := SessionByID(ctx, "new")
	if err != nil || s == nil {
		t.Fatalf("s=%v err=%v", s, err)
	}
	if s.EndedAt == nil || *s.EndedAt != "2026-08-03T10:20:00.000Z" {
		t.Fatalf("endedAt=%v", s.EndedAt)
	}
	old, _ := SessionByID(ctx, "old")
	if old.EndedAt != nil {
		t.Fatalf("안 보낸 값은 '모른다'로 남아야 한다 — 0 이나 '' 로 지어내지 않는다: %v", *old.EndedAt)
	}
	if old.Turns != 3 {
		t.Fatalf("구버전 보고가 거절되면 그 사람 사용량이 통째로 사라진다: %+v", old)
	}
}

// 없는 세션은 (nil, nil) 이다 — "없음"은 오류가 아니다.
func TestSessionByIDMissingIsNilNil(t *testing.T) {
	ctx := fresh(t)
	s, err := SessionByID(ctx, "nope")
	if err != nil || s != nil {
		t.Fatalf("s=%v err=%v", s, err)
	}
	if s, err := SessionByID(ctx, ""); err != nil || s != nil {
		t.Fatalf("빈 id: s=%v err=%v", s, err)
	}
}

/*
 * SeriesRows 의 날짜 경계는 hour 의 **접두 10자**로 잡는다.
 *
 * SessionRows 의 관용구를 컬럼 이름만 바꿔 옮기면 안 되는 자리다 — hour 는 'YYYY-MM-DDTHH' 라
 * 앞 10자가 곧 날짜다.
 */
func TestSeriesRowsDateBoundaryIsInclusive(t *testing.T) {
	ctx := fresh(t)
	mustSeries(t, ctx, SeriesInput{SessionID: "s", Rows: []SeriesRow{
		{Hour: "2026-08-02T23", Model: mOpus, Turns: 1},
		{Hour: "2026-08-03T00", Model: mOpus, Turns: 1},
		{Hour: "2026-08-03T23", Model: mOpus, Turns: 1},
		{Hour: "2026-08-04T00", Model: mOpus, Turns: 1},
	}})
	rows, err := SeriesRows(ctx, Filter{From: "2026-08-03", To: "2026-08-03"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("그날 23시가 잘렸거나 남의 날이 섞였다: %d개 %+v", len(rows), rows)
	}
}

// 형식이 아닌 경계는 무시한다 — 경계가 깨진 채로 조회되느니 없는 편이 낫다.
func TestRowFiltersIgnoreMalformedDates(t *testing.T) {
	ctx := fresh(t)
	mustSession(t, ctx, SessionInput{SessionID: "s", StartedAt: "2026-08-03T09:00:00.000Z", Output: 1})
	rows, err := SessionRows(ctx, Filter{From: "어제", To: "2026-8-3"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("깨진 경계가 필터로 먹었다: %d", len(rows))
	}
}

// SessionRows 의 날짜 경계도 접두 10자다 — started_at 이 ISO 라 하루치를 조용히 잘라먹는 자리.
func TestSessionRowsDateBoundaryIsInclusive(t *testing.T) {
	ctx := fresh(t)
	for i, at := range []string{
		"2026-08-02T23:59:59.000Z", "2026-08-03T00:00:00.000Z",
		"2026-08-03T23:59:59.000Z", "2026-08-04T00:00:00.000Z",
	} {
		mustSession(t, ctx, SessionInput{SessionID: fmt.Sprintf("s%d", i), StartedAt: at, Output: 1})
	}
	rows, _ := SessionRows(ctx, Filter{From: "2026-08-03", To: "2026-08-03"})
	if len(rows) != 2 {
		t.Fatalf("하루치가 잘렸다: %d", len(rows))
	}
}

// 품질축 집계는 커버리지를 함께 낸다 — "이 지표는 세션 N/M 만 덮는다"고 말하게.
func TestSeriesQualityTotals(t *testing.T) {
	ctx := fresh(t)
	mustSeries(t, ctx, SeriesInput{SessionID: "s1", Username: "u", Rows: []SeriesRow{
		{Hour: "2026-08-03T09", Model: mOpus, Turns: 25, ToolErrors: 2, StopMaxTokens: 1,
			LatencyMsSum: 125000, LatencyMsMax: 30000, LatencyTurns: 25},
	}})
	mustSeries(t, ctx, SeriesInput{SessionID: "s2", Username: "u", Rows: []SeriesRow{
		{Hour: "2026-08-03T10", Model: mHaiku, Turns: 10, StopRefusal: 1,
			LatencyMsSum: 40000, LatencyMsMax: 12000, LatencyTurns: 10},
	}})
	q, err := SeriesQualityTotals(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	want := QualityTotals{
		SessionsWithSeries: 2, Turns: 35, ToolErrors: 2, StopMaxTokens: 1, StopRefusal: 1,
		LatencyMsSum: 165000, LatencyMsMax: 30000, LatencyTurns: 35,
	}
	if q != want {
		t.Fatalf("want %+v\ngot  %+v", want, q)
	}
	// 빈 저장소에서도 죽지 않고 0 을 낸다.
	ctx2 := fresh(t)
	if q2, err := SeriesQualityTotals(ctx2, Filter{}); err != nil || q2 != (QualityTotals{}) {
		t.Fatalf("q2=%+v err=%v", q2, err)
	}
}

// CountersOf 는 축을 좁혀 돌려준다(드릴다운 본문).
func TestCountersOfFiltersKinds(t *testing.T) {
	ctx := fresh(t)
	mustCounters(t, ctx, CountersInput{SessionID: "s", StartedAt: "2026-08-03T00:00:00.000Z", Rows: []CounterRow{
		{Kind: "bash", Key: "git", Count: 5},
		{Kind: "keyword", Key: "비밀말", Count: 9},
		{Kind: "tool", Key: "Bash", Count: 2},
	}})
	out, err := CountersOf(ctx, "s", []string{"bash", "tool", "bogus"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out["keyword"]; ok {
		t.Fatal("요청하지 않은 축이 나왔다")
	}
	if len(out["bash"]) != 1 || out["bash"][0].Count != 5 {
		t.Fatalf("%+v", out)
	}
	if _, ok := out["bogus"]; ok {
		t.Fatal("모르는 축이 통과했다")
	}
}
