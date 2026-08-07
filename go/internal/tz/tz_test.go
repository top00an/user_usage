package tz

// test/usage-tz.test.js 의 케이스를 그대로 옮긴 테이블 테스트.
//
// 이 테스트가 지키는 것은 숫자가 아니라 **날짜 경계**다. 경계가 틀리면 값 자체는 그럴듯해서
// 눈으로는 안 보인다.

import "testing"

const kst = DefaultOffsetMin

func TestLocalDay_KSTBoundary(t *testing.T) {
	// ① KST 하루는 UTC [D-1 15:00, D 15:00)
	cases := []struct {
		name, in, want string
	}{
		{"심야 KST 가 전날로 밀리지 않는다 (16:00Z)", "2026-08-03T16:00:00.000Z", "2026-08-04"},
		{"심야 KST 가 전날로 밀리지 않는다 (23:59Z)", "2026-08-03T23:59:59.000Z", "2026-08-04"},
		{"15:00Z 직전은 아직 전날", "2026-08-03T14:59:59.999Z", "2026-08-03"},
		{"15:00Z 부터 다음날", "2026-08-03T15:00:00.000Z", "2026-08-04"},
		{"낮 시간대는 UTC 날짜와 같다 (00:00Z)", "2026-08-04T00:00:00.000Z", "2026-08-04"},
		{"낮 시간대는 UTC 날짜와 같다 (05:30Z)", "2026-08-04T05:30:00.000Z", "2026-08-04"},
		{"월 경계", "2026-07-31T15:00:00.000Z", "2026-08-01"},
		{"연 경계", "2026-12-31T15:00:00.000Z", "2027-01-01"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := LocalDay(c.in, kst); got != c.want {
				t.Fatalf("LocalDay(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestLocalDay_InvalidPassesThrough(t *testing.T) {
	// 형식이 아니면 지어내지 않는다 — 앞 10자를 그대로 돌려준다.
	cases := []struct{ in, want string }{
		{"", ""},
		{"nope", "nope"},
		{"not-a-timestamp-at-all", "not-a-time"},
	}
	for _, c := range cases {
		if got := LocalDay(c.in, kst); got != c.want {
			t.Fatalf("LocalDay(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLocalHour(t *testing.T) {
	cases := []struct{ in, want string }{
		{"2026-08-03T12", "2026-08-03T21"},
		{"2026-08-03T00", "2026-08-03T09"},
		// 자정을 넘으면 날짜도 함께 넘어간다 — 여기가 9시간 밀리던 자리.
		{"2026-08-03T15", "2026-08-04T00"},
		{"2026-08-03T23", "2026-08-04T08"},
		// 형식이 아니면 그대로 돌려준다.
		{"2026-08-03", "2026-08-03"},
		{"", ""},
		{"nope", "nope"},
	}
	for _, c := range cases {
		if got := LocalHour(c.in, kst); got != c.want {
			t.Fatalf("LocalHour(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestWeekStart(t *testing.T) {
	cases := []struct{ in, want string }{
		{"2026-08-03", "2026-08-03"}, // 월
		{"2026-08-04", "2026-08-03"}, // 화
		{"2026-08-09", "2026-08-03"}, // 일 — 같은 주의 끝
		{"2026-08-10", "2026-08-10"}, // 다음 월
		{"nope", "nope"},
		{"", ""},
	}
	for _, c := range cases {
		if got := WeekStart(c.in); got != c.want {
			t.Fatalf("WeekStart(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestWeekStart_YearBoundaryIsMonday(t *testing.T) {
	w := WeekStart("2026-01-01")
	// 되돌려 파싱해 요일이 월요일인지 본다.
	if d := LocalDay(w+"T00:00:00.000Z", 0); d != w {
		t.Fatalf("주 시작 라벨이 날짜로 되돌아가지 않는다: %q", w)
	}
	if wd := weekdayOf(w); wd != 1 {
		t.Fatalf("WeekStart(2026-01-01) = %q 의 요일이 %d — 월요일(1)이 아니다", w, wd)
	}
}

func TestWidenUTCRange(t *testing.T) {
	from, to := WidenUTCRange("2026-08-04", "2026-08-04")
	if from != "2026-08-03" || to != "2026-08-05" {
		t.Fatalf("WidenUTCRange = (%q,%q), want (2026-08-03,2026-08-05)", from, to)
	}
	// 범위가 없으면 넓히지 않는다.
	if f, tt := WidenUTCRange("", ""); f != "" || tt != "" {
		t.Fatalf("빈 범위를 넓혔다: (%q,%q)", f, tt)
	}
	if f, tt := WidenUTCRange("nope", "nope"); f != "nope" || tt != "nope" {
		t.Fatalf("형식이 아닌 범위를 건드렸다: (%q,%q)", f, tt)
	}
}

func TestInRange(t *testing.T) {
	cases := []struct {
		label, from, to string
		want            bool
	}{
		{"2026-08-04", "2026-08-04", "2026-08-04", true},
		{"2026-08-03", "2026-08-04", "2026-08-04", false}, // 넓혀 뜬 앞날이 남으면 안 된다
		{"2026-08-05", "2026-08-04", "2026-08-04", false}, // 넓혀 뜬 뒷날이 남으면 안 된다
		{"2026-08-04T21", "2026-08-04", "2026-08-04", true},
		// 범위가 비면 전부 통과 — 필터가 없다는 뜻이지 전부 버린다는 뜻이 아니다.
		{"2026-08-04", "", "", true},
		// 라벨이 형식이 아니면 버린다.
		{"nope", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		if got := InRange(c.label, c.from, c.to); got != c.want {
			t.Fatalf("InRange(%q,%q,%q) = %v, want %v", c.label, c.from, c.to, got, c.want)
		}
	}
}

func TestOffsetMinFrom(t *testing.T) {
	if DefaultOffsetMin != 540 {
		t.Fatalf("DefaultOffsetMin = %d, want 540", DefaultOffsetMin)
	}
	ok := []struct {
		raw  string
		want int
	}{
		{"0", 0},
		{"-480", -480},
		{"330", 330}, // 30분 오프셋(인도)도 표현된다
		{"540", 540},
	}
	for _, c := range ok {
		if got := OffsetMinFrom(c.raw); got != c.want {
			t.Fatalf("OffsetMinFrom(%q) = %d, want %d", c.raw, got, c.want)
		}
	}
	// 이상값은 기본값으로 수렴한다 — 설정 오류가 데이터를 망가뜨리지 않게.
	// ⚠ 빈 문자열이 UTC(0)로 읽히면 컨테이너에서 집계가 조용히 UTC 로 되돌아간다.
	for _, bad := range []string{"abc", "", "   ", "9999", "-9999", "841", "-841"} {
		if got := OffsetMinFrom(bad); got != DefaultOffsetMin {
			t.Fatalf("OffsetMinFrom(%q) = %d — 이상값을 받아들였다", bad, got)
		}
	}
}

func TestOffsetZeroIsUTC(t *testing.T) {
	// 오프셋 0 이면 UTC 와 같아진다 — 되돌릴 수 있다는 증거.
	if got := LocalDay("2026-08-03T16:00:00.000Z", 0); got != "2026-08-03" {
		t.Fatalf("LocalDay(off=0) = %q", got)
	}
	if got := LocalHour("2026-08-03T15", 0); got != "2026-08-03T15" {
		t.Fatalf("LocalHour(off=0) = %q", got)
	}
}

func TestLabel(t *testing.T) {
	cases := []struct {
		off  int
		want string
	}{
		{540, "KST"}, // 아는 시간대는 이름으로 — UTC 라는 낱말이 오해를 만든다
		{0, "UTC"},
		{-480, "PST"},
		{480, "CST"},
		{330, "IST"},
		{60, "CET"},
		{45, "UTC+00:45"}, // 모르는 오프셋은 정확한 표기로 떨어진다
		{-45, "UTC-00:45"},
		{-570, "UTC-09:30"},
	}
	for _, c := range cases {
		if got := Label(c.off); got != c.want {
			t.Fatalf("Label(%d) = %q, want %q", c.off, got, c.want)
		}
	}
}
