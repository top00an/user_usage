// Package stats — 분포 계산. 합계가 감추는 것을 드러낸다.
//
// lib/stats.js 의 포팅이다. 왜 필요한가: 사용량 화면이 합계만 보여주면 "평균적인 세션"과
// "이상한 세션"이 같은 숫자에 섞인다. 2026-08-03 실측에서 4,314턴짜리 세션 하나가 23억 토큰을
// 썼는데 그건 전체 합계의 13% 였다 — 합계만 보면 그 세션이 존재한다는 사실 자체가 보이지 않는다.
// p95/p99 는 그 한 건을 화면으로 끌어올리는 장치다.
//
// ── 왜 SQL 이 아닌가 ────────────────────────────────────────────────────────
// PostgreSQL 의 percentile_cont 를 쓰면 SQLite 경로가 갈라진다(이 프로젝트는 두 방언을 함께
// 문다). 방언 분기를 하나 더 만드는 비용보다, 행을 받아 여기서 정렬하는 비용이 싸다.
// 행 수는 호출부가 상한을 걸어 넘긴다(store.SessionRows 의 limit).
//
// ── 부동소수는 Node 와 바이트 단위로 같아야 한다 ────────────────────────────
// 골든이 JSON 숫자를 그대로 비교하므로 보간식을 lib/stats.js 에서 **그대로** 옮겼다.
// "같은 뜻의 다른 식"은 마지막 자리에서 갈리고, 그 차이는 통합 게이트에서 원인 불명으로
// 나타난다. 합(avg)도 **정렬한 뒤** 앞에서부터 더한다 — 덧셈 순서가 바뀌면 끝자리가 달라진다.
//
// 순수 함수만 둔다 — DB·설정·시간에 의존하지 않는다.
// 표준 라이브러리 말고 아무것도 import 하지 않는다.
package stats

import (
	"math"
	"sort"
	"strconv"
	"strings"
)

// DefaultPs 는 화면이 쓰는 셋이다. 한 곳에 둔다.
var DefaultPs = []float64{0.5, 0.95, 0.99}

// Summary 의 JSON 태그는 현행 응답(lib/stats.js)의 키를 그대로 쓴다.
// nil 포인터는 JSON 에서 null 로 나간다 — "표본이 없다"를 0 으로 위장하지 않기 위해서다.
// 필드 순서도 Node 출력 순서와 같게 두었다(골든이 본문을 문자열로 비교한다).
type Summary struct {
	N       int                 `json:"n"`
	Dropped int                 `json:"dropped"`
	Min     *float64            `json:"min"`
	Max     *float64            `json:"max"`
	Avg     *float64            `json:"avg"`
	P       map[string]*float64 `json:"p"`
}

// QuantileSorted 는 선형 보간 분위수다(PostgreSQL percentile_cont 와 같은 방식).
//
// 최근접 순위(nearest-rank)를 쓰지 않는 이유: 표본이 적을 때 p95 와 p99 가 같은 값으로 붙어
// 화면에서 두 열이 똑같아 보인다. 보간을 쓰면 적은 표본에서도 두 분위가 갈린다.
//
// sorted 는 **오름차순 정렬된** 유한수 슬라이스여야 한다. 빈 슬라이스에서는 죽지 않고 0 을
// 돌려준다 — 호출부가 "표본 없음"을 구분해야 하면 Summarize 를 쓴다(그쪽은 null 로 낸다).
func QuantileSorted(sorted []float64, p float64) float64 {
	v, _ := quantile(sorted, p)
	return v
}

func quantile(sorted []float64, p float64) (float64, bool) {
	n := len(sorted)
	if n == 0 {
		return 0, false
	}
	if n == 1 {
		return sorted[0], true
	}
	// JS 의 `Number(p) || 0` — NaN 은 0 으로 읽힌다.
	q := p
	if math.IsNaN(q) {
		q = 0
	}
	q = math.Max(0, math.Min(1, q))

	// idx 도 mulRounded 로 강제 반올림한다. 그냥 `float64(n-1)*q` 로 두면 arm64 가
	// 이 곱을 아래 `idx-float64(lo)` 뺄셈과 FMA(fma(n-1,q,-lo)) 로 융합해 반올림이
	// 한 번만 일어나고, idx 를 먼저 반올림한 뒤 빼는 Node(V8)와 p95 마지막 비트가
	// 갈린다(26.849999999999994 vs …998). 배리어로 idx 를 먼저 굳혀 순서를 맞춘다.
	idx := mulRounded(float64(n-1), q)
	lo := int(math.Floor(idx))
	hi := int(math.Ceil(idx))
	if lo == hi {
		return sorted[lo], true
	}
	// mulRounded 로 곱을 먼저 IEEE754 로 반올림시킨 뒤 더한다. arm64 백엔드는
	// `a + b*c` 를 SSA 단계에서 FMA 한 개로 재융합해 반올림을 한 번만 하는데,
	// Node(V8)는 곱·합을 따로(반올림 두 번) 하므로 마지막 비트가 갈린다. 로컬
	// 변수 분리로는 융합이 막히지 않아, 곱을 함수 경계 밖으로 밀어 강제 반올림한다.
	prod := mulRounded(sorted[hi]-sorted[lo], idx-float64(lo))
	return sorted[lo] + prod, true
}

// mulRounded 는 곱 결과를 IEEE754 double 로 강제 반올림시키는 값 배리어다.
// go:noinline 이라 호출부에 인라인되며 FMA 로 융합되지 않는다 — Node 와의
// 바이트 단위 분위수 파리티를 지키는 유일하게 안정적인 방법이다.
//
//go:noinline
func mulRounded(a, b float64) float64 { return a * b }

// Summarize 는 값 슬라이스를 분포 요약으로 접는다.
//
// 유한수가 아닌 값(NaN·±Inf)은 버리고 개수를 Dropped 로 돌려준다 — 호출부가 표본이 얼마나
// 깎였는지 알 수 있게. **0 은 버리지 않는다**: 0턴 세션이나 0원 세션은 실재하는 관측이고,
// 그걸 빼면 분포가 낙관적으로 왜곡된다.
//
// ps 를 주지 않으면 DefaultPs(p50·p95·p99)를 쓴다. 호출자의 슬라이스는 훼손하지 않는다.
func Summarize(values []float64, ps ...float64) Summary {
	clean := make([]float64, 0, len(values))
	dropped := 0
	for _, v := range values {
		if isFinite(v) {
			clean = append(clean, v)
		} else {
			dropped++
		}
	}
	return summarizeClean(clean, dropped, ps)
}

// SummarizeAny 는 DB 드라이버·JSON 이 준 이질적인 값에서 같은 요약을 만든다.
//
// ⚠ **null 을 0 으로 세지 않는다.** JS 의 Number(null) 이 0 이라, 값 그대로 숫자로 바꾸면
// "값 없음"이 "0 이라는 관측"으로 둔갑해 분포를 아래로 당긴다(현행 단위 테스트가 실제로 잡은
// 자리). 같은 이유로 bool·배열·맵도 거른다 — Number(false)·Number([]) 가 전부 0 이다.
// 숫자 문자열("1")은 살린다: 드라이버가 bigint 를 문자열로 주는 경로가 있다.
func SummarizeAny(values []any, ps ...float64) Summary {
	clean := make([]float64, 0, len(values))
	dropped := 0
	for _, v := range values {
		n, ok := numeric(v)
		if !ok || !isFinite(n) {
			dropped++
			continue
		}
		clean = append(clean, n)
	}
	return summarizeClean(clean, dropped, ps)
}

func summarizeClean(clean []float64, dropped int, ps []float64) Summary {
	if len(clean) == 0 {
		return Summary{N: 0, Dropped: dropped, P: map[string]*float64{}}
	}
	sort.Float64s(clean)

	// 합은 **정렬한 뒤** 앞에서부터 — lib/stats.js 가 clean.sort() 다음에 reduce 한다.
	// 순서를 바꾸면 부동소수 끝자리가 달라지고 골든이 갈린다.
	sum := 0.0
	for _, v := range clean {
		sum += v
	}
	avg := sum / float64(len(clean))

	if len(ps) == 0 {
		ps = DefaultPs
	}
	p := make(map[string]*float64, len(ps))
	for _, q := range ps {
		// 키는 'p50'·'p95'·'p99' 처럼 읽히게 — 화면이 그대로 쓴다.
		v, ok := quantile(clean, q)
		if !ok {
			continue
		}
		p[percentileKey(q)] = ptr(v)
	}

	return Summary{
		N:       len(clean),
		Dropped: dropped,
		Min:     ptr(clean[0]),
		Max:     ptr(clean[len(clean)-1]),
		Avg:     ptr(avg),
		P:       p,
	}
}

func percentileKey(q float64) string {
	if math.IsNaN(q) {
		q = 0
	}
	// JS Math.round 는 .5 를 올림 쪽(양의 무한대)으로 보낸다. math.Round 는 0 에서 멀어지는
	// 쪽이라 음수에서만 갈리는데, 분위는 0~1 이므로 여기서는 같다.
	return "p" + strconv.Itoa(int(math.Round(q*100)))
}

// numeric 은 JS 의 Number() 중 **분포에 넣어도 되는 것만** 통과시킨다.
// 통과: 숫자 · 비어 있지 않은 숫자 문자열. 그 밖(nil·bool·배열·맵·비수치 문자열)은 버린다.
func numeric(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int32:
		return float64(t), true
	case int64:
		return float64(t), true
	case uint:
		return float64(t), true
	case uint32:
		return float64(t), true
	case uint64:
		return float64(t), true
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0, false
		}
		n, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

func isFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func ptr(v float64) *float64 { return &v }
