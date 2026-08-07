package stats

// test/usage-stats.test.js 의 케이스를 그대로 옮긴 테이블 테스트.
//
// 검증하는 불변식:
//   ① 선형 보간 분위 — 표본이 적어도 p95 와 p99 가 갈린다(최근접 순위면 붙는다)
//   ② 0 을 버리지 않는다 — 0턴·0원 세션은 실재하는 관측이고 빼면 분포가 낙관적으로 왜곡된다
//   ③ 비유한값만 버리고 버린 개수를 돌려준다
//   ④ 정렬되지 않은 입력도 같은 답을 낸다

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

func ten() []float64 { return []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10} }

func deref(t *testing.T, p *float64, what string) float64 {
	t.Helper()
	if p == nil {
		t.Fatalf("%s 가 null 이다", what)
	}
	return *p
}

func TestSummarize_LinearInterpolation(t *testing.T) {
	s := Summarize(ten())
	if s.N != 10 {
		t.Fatalf("N = %d, want 10", s.N)
	}
	if got := deref(t, s.Min, "min"); got != 1 {
		t.Fatalf("min = %v, want 1", got)
	}
	if got := deref(t, s.Max, "max"); got != 10 {
		t.Fatalf("max = %v, want 10", got)
	}
	if got := deref(t, s.Avg, "avg"); got != 5.5 {
		t.Fatalf("avg = %v, want 5.5", got)
	}
	// (10-1)*0.50 = 4.5 → 5 와 6 사이
	if got := deref(t, s.P["p50"], "p50"); got != 5.5 {
		t.Fatalf("p50 = %v, want 5.5", got)
	}
	// 8.55 → 9 와 10 사이
	if got := deref(t, s.P["p95"], "p95"); math.Abs(got-9.55) > 1e-9 {
		t.Fatalf("p95 = %v, want ≈9.55", got)
	}
	// 8.91 → 9 와 10 사이
	if got := deref(t, s.P["p99"], "p99"); math.Abs(got-9.91) > 1e-9 {
		t.Fatalf("p99 = %v, want ≈9.91", got)
	}
}

func TestSummarize_P95AndP99DoNotCollapse(t *testing.T) {
	// 표본이 적어도 p95 와 p99 가 붙지 않는다 — 최근접 순위였다면 같은 값이 된다.
	s := Summarize(ten())
	if *s.P["p95"] == *s.P["p99"] {
		t.Fatalf("p95 와 p99 가 붙었다(%v) — 최근접 순위로 회귀했다", *s.P["p95"])
	}
}

func TestSummarize_ByteIdenticalWithNode(t *testing.T) {
	// 골든이 JSON 숫자를 그대로 비교한다. Node 가 내는 문자열과 **바이트 단위로** 같아야 한다.
	// 아래 기대값은 `node -e "..."` 로 실제 Node 가 찍은 것을 그대로 박았다.
	cases := []struct {
		name string
		in   []float64
		want string
	}{
		{
			"1..10",
			ten(),
			`{"n":10,"dropped":0,"min":1,"max":10,"avg":5.5,"p":{"p50":5.5,"p95":9.549999999999999,"p99":9.91}}`,
		},
		{
			"보간이 마지막 자리까지 갈리는 표본",
			[]float64{0.1, 0.2, 0.30000000000000004, 7, 11, 13, 17, 19, 23, 29, 31, 37},
			`{"n":12,"dropped":0,"min":0.1,"max":37,"avg":15.633333333333333,"p":{"p50":15,"p95":33.699999999999996,"p99":36.34}}`,
		},
		{
			"빈 입력",
			nil,
			`{"n":0,"dropped":0,"min":null,"max":null,"avg":null,"p":{}}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := json.Marshal(Summarize(c.in))
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != c.want {
				t.Fatalf("JSON 이 Node 와 다르다\n got: %s\nwant: %s", b, c.want)
			}
		})
	}
}

func TestSummarize_Boundaries(t *testing.T) {
	empty := Summarize(nil)
	if empty.N != 0 {
		t.Fatalf("빈 입력 N = %d", empty.N)
	}
	if len(empty.P) != 0 {
		t.Fatalf("빈 입력의 p 가 비어 있지 않다: %v", empty.P)
	}
	if empty.Min != nil || empty.Max != nil || empty.Avg != nil {
		t.Fatalf("빈 입력에서 min/max/avg 가 null 이 아니다")
	}

	one := Summarize([]float64{42})
	if one.N != 1 {
		t.Fatalf("단일 값 N = %d", one.N)
	}
	if deref(t, one.P["p50"], "p50") != 42 || deref(t, one.P["p99"], "p99") != 42 {
		t.Fatalf("단일 값의 분위가 42 가 아니다: %v", one.P)
	}
}

func TestSummarize_CustomPercentiles(t *testing.T) {
	s := Summarize(ten(), 0.25, 0.75)
	if len(s.P) != 2 || s.P["p25"] == nil || s.P["p75"] == nil {
		t.Fatalf("분위 목록을 바꿀 수 없다: %v", s.P)
	}
}

func TestSummarize_ZerosAreObservations(t *testing.T) {
	// 0 을 걸렀다면 n=1, p50=10 이 됐을 것이다.
	s := Summarize([]float64{0, 0, 0, 0, 10})
	if s.N != 5 {
		t.Fatalf("N = %d, want 5 — 0 을 버렸다", s.N)
	}
	if deref(t, s.Min, "min") != 0 {
		t.Fatalf("min = %v, want 0", *s.Min)
	}
	if deref(t, s.P["p50"], "p50") != 0 {
		t.Fatalf("p50 = %v, want 0 — 0 을 버려 분포가 낙관적으로 왜곡됐다", *s.P["p50"])
	}
}

func TestSummarize_DropsNonFinite(t *testing.T) {
	s := Summarize([]float64{1, 2, math.NaN(), math.Inf(1), math.Inf(-1), 3})
	if s.N != 3 {
		t.Fatalf("N = %d, want 3", s.N)
	}
	if s.Dropped != 3 {
		t.Fatalf("Dropped = %d, want 3", s.Dropped)
	}
	if deref(t, s.P["p50"], "p50") != 2 {
		t.Fatalf("p50 = %v, want 2", *s.P["p50"])
	}
}

func TestSummarizeAny_JSSemantics(t *testing.T) {
	// lib/stats.js 의 ⚠ 규율: Number(null)·Number(false)·Number([]) 가 전부 0 이라,
	// 값 그대로 숫자로 바꾸면 "값 없음"이 "0 이라는 관측"으로 둔갑해 분포를 아래로 당긴다.
	s := SummarizeAny([]any{1, 2, math.NaN(), math.Inf(1), nil, "abc", 3})
	if s.N != 3 {
		t.Fatalf("N = %d, want 3", s.N)
	}
	if s.Dropped != 4 {
		t.Fatalf("Dropped = %d, want 4", s.Dropped)
	}
	if deref(t, s.P["p50"], "p50") != 2 {
		t.Fatalf("p50 = %v, want 2", *s.P["p50"])
	}

	// 숫자 문자열은 살린다(DB 드라이버가 bigint 를 문자열로 주는 경로가 있다).
	str := SummarizeAny([]any{"1", "2", "3"})
	if str.N != 3 || deref(t, str.P["p50"], "p50") != 2 {
		t.Fatalf("숫자 문자열을 버렸다: %+v", str)
	}

	// boolean·배열·객체도 거른다 — Number(false)·Number([]) 가 전부 0 이다.
	mixed := SummarizeAny([]any{true, false, []any{}, map[string]any{}, 5})
	if mixed.N != 1 || mixed.Dropped != 4 {
		t.Fatalf("boolean/배열/객체를 0 으로 셌다: %+v", mixed)
	}

	// 배열이 아닌(=nil) 입력도 죽지 않는다.
	if SummarizeAny(nil).N != 0 {
		t.Fatalf("nil 입력에서 N != 0")
	}
}

func TestSummarize_OrderIndependentAndNonDestructive(t *testing.T) {
	sorted := Summarize([]float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})
	shuffled := Summarize([]float64{7, 2, 10, 1, 5, 9, 3, 8, 4, 6})
	if !reflect.DeepEqual(jsonOf(t, sorted), jsonOf(t, shuffled)) {
		t.Fatalf("입력 순서에 답이 의존한다")
	}

	input := []float64{3, 1, 2}
	Summarize(input)
	if !reflect.DeepEqual(input, []float64{3, 1, 2}) {
		t.Fatalf("입력 배열을 제자리 정렬해 호출자를 망가뜨렸다: %v", input)
	}
}

func jsonOf(t *testing.T, s Summary) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestQuantileSorted(t *testing.T) {
	cases := []struct {
		sorted []float64
		p      float64
		want   float64
	}{
		{nil, 0.5, 0}, // 빈 슬라이스에서 죽지 않는다
		{[]float64{42}, 0.99, 42},
		{ten(), 0, 1},
		{ten(), 1, 10},
		{ten(), 0.5, 5.5},
		{ten(), -1, 1}, // 0~1 로 클램프
		{ten(), 2, 10}, // 0~1 로 클램프
	}
	for _, c := range cases {
		if got := QuantileSorted(c.sorted, c.p); got != c.want {
			t.Fatalf("QuantileSorted(%v, %v) = %v, want %v", c.sorted, c.p, got, c.want)
		}
	}
	// NaN 은 0 으로 읽는다(JS 의 `Number(p) || 0`).
	if got := QuantileSorted(ten(), math.NaN()); got != 1 {
		t.Fatalf("QuantileSorted(NaN) = %v, want 1", got)
	}
}
