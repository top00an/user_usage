// Package tz — 집계 시간대. 저장은 UTC, **집계·표시는 로컬(기본 KST)**.
//
// lib/tz.js 의 포팅이다. 그 파일의 결정과 이유를 그대로 옮겼다:
//
//	① 날짜가 밀린다. KST 하루는 UTC 로 [D-1 15:00, D 15:00) 이다. 그래서 00:00~09:00 KST
//	   에 한 일이 **전날**로 집계된다. 개발자가 가장 흔하게 일하는 심야가 통째로 어제로 간다.
//	② 시간대 그래프가 9시간 밀린다. 21시 KST 스파이크가 12시 칸에 그려진다.
//
// **고정 오프셋이다. IANA 시간대를 쓰지 않는다.** 한국은 서머타임이 없어 오프셋이 상수다.
// IANA 를 쓰려면 매 행마다 시간대 변환을 해야 하는데(수만 행) 그 비용을 서머타임 없는 지역에
// 지불할 이유가 없다. 서머타임 지역으로 옮길 일이 생기면 그때 바꾼다.
//
// 저장을 UTC 로 두는 이유도 같다 — 로컬로 저장하면 ①기존 데이터와 섞이고 ②수집기까지 고쳐야
// 하며 ③팀이 다른 시간대로 옮기면 과거가 통째로 틀려진다. **읽을 때 옮기는** 것이 되돌릴 수
// 있는 유일한 방향이라, 이 모듈에는 마이그레이션이 없다.
//
// 순수 함수만 둔다 — DB·설정 파일을 읽지 않는다(오프셋은 인자 또는 env).
// 표준 라이브러리 말고 아무것도 import 하지 않는다.
package tz

import (
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultOffsetMin 은 +09:00(Asia/Seoul).
// 분 단위인 이유: 인도(+05:30) 같은 30분 오프셋 지역이 실재한다.
const DefaultOffsetMin = 540

// 실재하는 오프셋 범위 -14:00 ~ +14:00. 밖의 값은 설정 오류이지 "그런 시간대" 가 아니다.
const (
	minOffsetMin = -840
	maxOffsetMin = 840
)

const (
	dayLayout  = "2006-01-02"
	hourLayout = "2006-01-02T15"
)

// OffsetMin 은 env(USAGE_TZ_OFFSET_MIN) > 기본값 순으로 오프셋을 정한다.
func OffsetMin() int { return OffsetMinFrom(os.Getenv("USAGE_TZ_OFFSET_MIN")) }

// OffsetMinFrom 은 원시 문자열에서 오프셋을 읽는다(테스트가 env 없이 부를 수 있게 분리).
//
// ⚠ 빈 값(미설정과 같다)을 UTC 로 읽으면 컨테이너에서 env 를 빈 문자열로 넘긴 순간 집계가
// 조용히 UTC 로 되돌아간다. 그래서 빈 값은 0 이 아니라 기본값이다.
func OffsetMinFrom(raw string) int {
	s := strings.TrimSpace(raw)
	if s == "" {
		return DefaultOffsetMin
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || v < minOffsetMin || v > maxOffsetMin {
		return DefaultOffsetMin
	}
	return int(math.Trunc(v))
}

// LocalDay 는 ISO 타임스탬프를 로컬 날짜 'YYYY-MM-DD' 로 옮긴다.
// 형식이 아니면 **입력의 앞 10자를 그대로 돌려준다** — 라벨이 없는 행을 지어내지 않는다.
func LocalDay(iso string, offsetMin int) string {
	t, ok := parseTimestamp(iso)
	if !ok {
		return truncRunes(iso, 10)
	}
	return shifted(t, offsetMin).Format(dayLayout)
}

// LocalHour 는 UTC 시간 라벨('YYYY-MM-DDTHH')을 로컬 시간 라벨로 옮긴다.
// 수집기가 만든 라벨은 UTC 다(team-usage.js hourBucket). 그걸 읽을 때 옮긴다.
func LocalHour(label string, offsetMin int) string {
	if !isHourLabel(label) {
		return label
	}
	t, err := time.ParseInLocation(hourLayout, label, time.UTC)
	if err != nil {
		return label
	}
	return shifted(t, offsetMin).Format(hourLayout)
}

// WeekStart 는 로컬 날짜 'YYYY-MM-DD' 를 그 주의 **월요일**(ISO 8601 주 시작)로 접는다.
//
// 이미 로컬로 옮긴 날짜 라벨에만 적용한다 — UTC 라벨에 적용하면 주 경계가 9시간 밀린 채로
// 굳는다.
func WeekStart(day string) string {
	if !isDayLabel(day) {
		return day
	}
	t, err := time.ParseInLocation(dayLayout, day, time.UTC)
	if err != nil {
		return day
	}
	// time.Weekday 는 일요일이 0 — JS getUTCDay 와 같다.
	back := (int(t.Weekday()) + 6) % 7
	return t.AddDate(0, 0, -back).Format(dayLayout)
}

// WidenUTCRange 는 로컬 날짜 범위를 **UTC 조회 범위**로 하루씩 넓힌다.
//
// SQL 필터는 UTC 라벨 위에서 돈다(started_at·hour 가 UTC). 로컬 'YYYY-MM-DD' 를 그대로
// 넘기면 경계가 오프셋만큼 어긋나 하루의 앞뒤가 잘린다. 넓혀 뜬 다음 InRange 로 정확히 거른다.
//
// 양쪽을 넓히는 이유: 오프셋이 양수면(KST) 로컬 D 의 시작이 UTC D-1 이고, 음수면(미주)
// 로컬 D 의 끝이 UTC D+1 이다. 부호를 따지지 않고 양쪽을 넓히는 편이 안전하다.
func WidenUTCRange(from, to string) (string, string) {
	return shiftDay(from, -1), shiftDay(to, 1)
}

func shiftDay(day string, delta int) string {
	if !isDayLabel(day) {
		return day
	}
	t, err := time.ParseInLocation(dayLayout, day, time.UTC)
	if err != nil {
		return day
	}
	return t.AddDate(0, 0, delta).Format(dayLayout)
}

// InRange 는 로컬 라벨이 요청 범위 안인지 본다 — WidenUTCRange 로 넓혀 뜬 뒤 이걸로 정확히
// 거른다. 범위가 비면 전부 통과한다(필터가 없다는 뜻이지 전부 버린다는 뜻이 아니다).
func InRange(localLabel, from, to string) bool {
	d := truncRunes(localLabel, 10)
	if !isDayLabel(d) {
		return false
	}
	if isDayLabel(from) && d < from {
		return false
	}
	if isDayLabel(to) && d > to {
		return false
	}
	return true
}

// 아는 시간대는 **이름**으로 낸다("KST"). 'UTC+09:00' 은 정확하지만 화면에서 읽는 사람에게는
// UTC 라는 낱말이 먼저 들어와 "아직 UTC 인가?" 로 오해된다 — 실제로 그 오해가 있었다.
var named = map[int]string{540: "KST", 0: "UTC", -480: "PST", 480: "CST", 330: "IST", 60: "CET"}

// Label 은 화면이 "이 숫자가 어느 시간대 기준인가" 를 말할 수 있게 라벨을 만든다.
func Label(offsetMin int) string {
	if n, ok := named[offsetMin]; ok {
		return n
	}
	sign := "+"
	a := offsetMin
	if a < 0 {
		sign = "-"
		a = -a
	}
	return "UTC" + sign + pad2(a/60) + ":" + pad2(a%60)
}

func pad2(n int) string {
	s := strconv.Itoa(n)
	if len(s) < 2 {
		return "0" + s
	}
	return s
}

func shifted(t time.Time, offsetMin int) time.Time {
	return t.UTC().Add(time.Duration(offsetMin) * time.Minute)
}

// 받아들이는 타임스탬프 형식. 수집기는 항상 RFC3339(…Z)로 보내지만, 구버전·DB 드라이버가
// 존을 뗀 형태로 주는 경로가 있어 함께 받는다. **존이 없으면 UTC 로 읽는다** —
// 이 레포는 저장을 UTC 로 하기로 했고, 로컬 해석은 컨테이너 TZ 에 따라 값이 달라진다.
var timestampLayouts = []string{
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	dayLayout,
}

func parseTimestamp(iso string) (time.Time, bool) {
	s := strings.TrimSpace(iso)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range timestampLayouts {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func isDayLabel(s string) bool {
	// ^\d{4}-\d{2}-\d{2}$
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return false
	}
	return allDigits(s[0:4]) && allDigits(s[5:7]) && allDigits(s[8:10])
}

func isHourLabel(s string) bool {
	// ^\d{4}-\d{2}-\d{2}T\d{2}$
	if len(s) != 13 || s[10] != 'T' {
		return false
	}
	return isDayLabel(s[:10]) && allDigits(s[11:13])
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// truncRunes 는 앞 n 글자를 남긴다. JS 의 slice 는 UTF-16 단위이지만 여기서는 룬 단위로
// 자른다 — 바이트로 자르면 깨진 UTF-8 이 나오고, 이 경로는 애초에 형식이 아닌 입력을
// 그대로 되돌려 주는 자리라 그 차이가 값의 의미를 바꾸지 않는다.
func truncRunes(s string, n int) string {
	cnt := 0
	for i := range s {
		if cnt == n {
			return s[:i]
		}
		cnt++
	}
	return s
}

// weekdayOf 는 'YYYY-MM-DD' 의 요일(일=0)을 돌려준다. 테스트가 주 경계를 확인하는 데 쓴다.
func weekdayOf(day string) int {
	t, err := time.ParseInLocation(dayLayout, day, time.UTC)
	if err != nil {
		return -1
	}
	return int(t.Weekday())
}
