package cost

// test/usage-cost.test.js 의 케이스를 그대로 옮긴 테이블 테스트.
//
// 이 테스트가 지키는 것은 숫자 하나가 아니라 **축의 순위**다. 2026-08-03 실측: 화면이 가장
// 크게 보여주던 출력 토큰이 비용의 11% 였고, 작은 글씨로 밀려 있던 캐시읽기가 64.5% 였다.
// 배수(캐시읽기 0.1×·캐시생성 1.25×)를 잘못 건드리면 그 순위가 조용히 뒤집힌다.
//
// 기대 숫자는 전부 현행 Node(lib/cost.js)를 실제로 돌려 받은 값이다 — 손으로 지어내지 않았다.

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// 2026-08-03 라이브에서 뽑은 실제 합계(usage_sessions, username='user-b').
var userB = Usage{
	Model:       "claude-opus-5",
	Input:       62082,
	Output:      15235576,
	CacheRead:   4468209525,
	CacheCreate: 135610378,
}

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

// 앰비언트 config.json 격리 — 라이브 설정에 usage.pricing 이 들어 있으면 시드 기준 산술이
// 흔들린다. usage 블록이 없는 임시 파일을 물려 시드를 고정한다.
func useConfig(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("USAGE_CONFIG", p)
}

func TestCostOf_AxisRanking(t *testing.T) {
	// ① 실측 픽스처 — 축별 비중 64.5/24.5/11.0 이 뒤집히지 않는다.
	useConfig(t, `{}`)
	c := CostOf(userB, nil, Mult{})
	if !c.Priced {
		t.Fatal("실측 픽스처가 priced=false 로 나왔다")
	}

	// Node 가 실제로 낸 값과 **정확히** 같아야 한다(골든이 JSON 숫자를 그대로 비교한다).
	if c.USD != 3462.869435 {
		t.Fatalf("usd = %v, want 3462.869435", c.USD)
	}
	want := Axis{Input: 0.31041, Output: 380.8894, CacheRead: 2234.1047625, CacheCreate: 847.5648625}
	if c.ByAxis != want {
		t.Fatalf("byAxis = %+v, want %+v", c.ByAxis, want)
	}

	share := func(v float64) string {
		return trimFixed1(v / c.USD * 100)
	}
	if got := share(c.ByAxis.CacheRead); got != "64.5" {
		t.Fatalf("캐시읽기 비중 = %s%%, want 64.5%%", got)
	}
	if got := share(c.ByAxis.CacheCreate); got != "24.5" {
		t.Fatalf("캐시생성 비중 = %s%%, want 24.5%%", got)
	}
	if got := share(c.ByAxis.Output); got != "11.0" {
		t.Fatalf("출력 비중 = %s%%, want 11.0%%", got)
	}
}

func TestCostOf_TokenRankIsNotCostRank(t *testing.T) {
	// 출력 토큰은 캐시읽기의 1/293 인데 비용은 1/5.9 — 이 둘이 같아지면 배수가 사라진 것이다.
	useConfig(t, `{}`)
	c := CostOf(userB, nil, Mult{})
	tokenRatio := userB.CacheRead / userB.Output
	costRatio := c.ByAxis.CacheRead / c.ByAxis.Output
	if tokenRatio <= 250 {
		t.Fatalf("토큰 비 %v", tokenRatio)
	}
	if costRatio <= 5 || costRatio >= 7 {
		t.Fatalf("비용 비 %v", costRatio)
	}
	if tokenRatio/costRatio <= 40 {
		t.Fatal("토큰 비와 비용 비가 붙었다 — 배수가 적용되지 않았다")
	}
}

func TestCostOf_UnknownModelIsNotSilentlyZero(t *testing.T) {
	// ② 단가 미등록 모델을 0 원으로 숨기지 않는다.
	useConfig(t, `{}`)
	c := CostOf(Usage{Model: "nvidia/nemotron-3-ultra-550b-a55b", Output: 1e9}, nil, Mult{})
	if c.Priced {
		t.Fatal("미등록 모델이 priced=true 로 나왔다")
	}
	if c.USD != 0 {
		t.Fatalf("usd = %v, want 0", c.USD)
	}
	if c.Model != "nvidia/nemotron-3-ultra-550b-a55b" {
		t.Fatalf("model = %q — 이름을 잃으면 화면이 무엇이 빠졌는지 말할 수 없다", c.Model)
	}
}

func TestSummarize_ReportsUnpriced(t *testing.T) {
	useConfig(t, `{}`)
	s := Summarize([]Usage{
		userB,
		{Model: "nemotron", Output: 5e8},
		{Model: "nemotron", Output: 1e8}, // 중복은 한 번만
		{Model: "", Output: 1e8},         // 모델 미상은 목록에 넣지 않는다(이름이 없다)
	})
	if len(s.Unpriced) != 1 || s.Unpriced[0] != "nemotron" {
		t.Fatalf("unpriced = %v, want [nemotron]", s.Unpriced)
	}
	if s.USD != 3462.869435 {
		t.Fatalf("usd = %v — 미등록 모델이 합계를 오염시켰다", s.USD)
	}
	if s.TTLUnknownRows != 1 {
		t.Fatalf("ttlUnknownRows = %d, want 1", s.TTLUnknownRows)
	}
	if s.PricedAt == "" {
		t.Fatal("pricedAt 이 비었다")
	}
}

func TestNormalizeModel(t *testing.T) {
	// ③ 접미사·날짜 스냅샷·벤더 접두사를 떼고 같은 단가로 조회한다.
	useConfig(t, `{}`)
	base := CostOf(Usage{Model: "claude-opus-5", Output: 1e6}, nil, Mult{}).USD
	for _, m := range []string{"claude-opus-5[1m]", "anthropic.claude-opus-5", "CLAUDE-OPUS-5", "  claude-opus-5  "} {
		if got := CostOf(Usage{Model: m, Output: 1e6}, nil, Mult{}).USD; got != base {
			t.Fatalf("정규화 실패: %q → usd %v, want %v", m, got, base)
		}
	}

	cases := []struct{ in, want string }{
		{"claude-opus-4-5-20251101", "claude-opus-4-5"},
		{"claude-opus-5[1m]", "claude-opus-5"},
		{"anthropic.claude-opus-5", "claude-opus-5"},
		{"  CLAUDE-OPUS-5  ", "claude-opus-5"},
		{"", ""},
		{"   ", ""},
	}
	for _, c := range cases {
		if got := NormalizeModel(c.in); got != c.want {
			t.Fatalf("NormalizeModel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPricing_ConfigOverride(t *testing.T) {
	// ④ 부분 오버라이드 — 지정한 모델만 바뀌고 나머지는 시드를 유지한다.
	useConfig(t, `{"usage":{"pricing":{"claude-opus-5":{"input":10,"output":50}}}}`)
	tbl := Pricing(time.Time{})
	if tbl["claude-opus-5"] != (Price{Input: 10, Output: 50}) {
		t.Fatalf("오버라이드가 안 먹었다: %+v", tbl["claude-opus-5"])
	}
	if tbl["claude-haiku-4-5"] != Seed()["claude-haiku-4-5"] {
		t.Fatalf("오버라이드가 다른 모델까지 건드렸다: %+v", tbl["claude-haiku-4-5"])
	}
	// 단가를 2배로 올렸으니 총액도 2배가 되어야 한다.
	if got := CostOf(userB, nil, Mult{}).USD / 3462.869435; math.Abs(got-2) > 0.01 {
		t.Fatalf("총액 배수 = %v, want ≈2", got)
	}
}

func TestPricing_HalfOverrideIsIgnored(t *testing.T) {
	// 한쪽만 온 항목은 통째로 무시하고 시드를 유지한다.
	useConfig(t, `{"usage":{"pricing":{"claude-opus-5":{"input":99}}}}`)
	if got := Pricing(time.Time{})["claude-opus-5"]; got != Seed()["claude-opus-5"] {
		t.Fatalf("반쪽 오버라이드를 받아들였다: %+v", got)
	}
}

func TestPricedAt_ConfigWins(t *testing.T) {
	useConfig(t, `{"usage":{"pricedAt":"2026-09-01"}}`)
	if got := PricedAt(); got != "2026-09-01" {
		t.Fatalf("PricedAt() = %q, want 2026-09-01", got)
	}
	useConfig(t, `{}`)
	if got := PricedAt(); got != SeedPricedAt {
		t.Fatalf("PricedAt() = %q, want %q", got, SeedPricedAt)
	}
}

func TestMultipliers_TwoTierDefense(t *testing.T) {
	// ⑤ 무효값(음수·비수치)은 **기본값으로**, 범위 초과만 상한으로 클램프.
	// 여기서 음수를 0 으로 클램프하면 "캐시읽기가 공짜"가 되어 비용이 조용히 사라진다.
	useConfig(t, `{"usage":{"cacheReadMult":-5,"cacheCreateMult":9999}}`)
	m := Multipliers()
	if m.CacheRead != CacheReadMult {
		t.Fatalf("cacheRead = %v — 음수를 0 으로 클램프해 캐시읽기를 공짜로 만들었다", m.CacheRead)
	}
	if m.CacheCreate != 10 {
		t.Fatalf("cacheCreate = %v — 범위 초과가 상한(10)으로 잘리지 않았다", m.CacheCreate)
	}

	useConfig(t, `{"usage":{"cacheReadMult":"abc"}}`)
	if got := Multipliers().CacheRead; got != CacheReadMult {
		t.Fatalf("비수치 배수 = %v, want %v", got, CacheReadMult)
	}
}

func TestMultipliers_CacheCreateOverrideMovesOnlyThatAxis(t *testing.T) {
	useConfig(t, `{}`)
	base := CostOf(userB, nil, Mult{}).ByAxis

	useConfig(t, `{"usage":{"cacheCreateMult":2}}`)
	raised := CostOf(userB, nil, Mult{}).ByAxis

	if got := raised.CacheCreate / base.CacheCreate; math.Abs(got-(2.0/1.25)) > 0.001 {
		t.Fatalf("캐시생성 배율 = %v, want ≈1.6", got)
	}
	if raised.Output != base.Output || raised.Input != base.Input || raised.CacheRead != base.CacheRead {
		t.Fatalf("다른 축이 함께 움직였다: base=%+v raised=%+v", base, raised)
	}
}

func TestPricing_OfficialTable(t *testing.T) {
	// ⑥ 공식 가격표 대조 — 2026-08-04, platform.claude.com/docs/ko/about-claude/pricing
	// 단가가 틀려도 화면은 멀쩡한 숫자를 보여준다. 틀린 것을 알아채는 유일한 방법이 이 대조다.
	useConfig(t, `{}`)
	official := map[string]Price{
		"claude-fable-5":    {Input: 10, Output: 50},
		"claude-mythos-5":   {Input: 10, Output: 50},
		"claude-opus-5":     {Input: 5, Output: 25},
		"claude-opus-4-8":   {Input: 5, Output: 25},
		"claude-opus-4-7":   {Input: 5, Output: 25},
		"claude-opus-4-6":   {Input: 5, Output: 25},
		"claude-opus-4-5":   {Input: 5, Output: 25},
		"claude-sonnet-4-6": {Input: 3, Output: 15},
		"claude-sonnet-4-5": {Input: 3, Output: 15},
		"claude-haiku-4-5":  {Input: 1, Output: 5},
	}
	tbl := Pricing(day("2026-08-04"))
	for model, p := range official {
		got, ok := tbl[model]
		if !ok {
			t.Fatalf("%s 이 단가표에 없다 — 그 모델 사용량이 비용에서 빠진다", model)
		}
		if got != p {
			t.Fatalf("%s 단가가 공식과 다르다: %+v, want %+v", model, got, p)
		}
	}
}

/*
 * 도입가 장치(introUntil·intro)가 날짜로 갈리는지.
 *
 * ⚠ 이 테스트는 원래 claude-sonnet-5 로 쟀다. 그 모델은 이제 도입가가 없다 — $2/$10 이
 *   표준가로 확정됐고 2026-09-01 인상은 취소됐다(공식 단가 문서). 그래서 실제 모델 대신
 *   **합성 항목**으로 장치만 잰다. 장치를 지우지 않는 이유는 다음 도입가가 오면 쓸 자리이고,
 *   쓰는 모델이 없다고 검증을 지우면 다시 쓰는 날 아무도 동작을 모르기 때문이다.
 */
func TestPricing_IntroPriceSplitsOnDate(t *testing.T) {
	useConfig(t, `{}`)

	const probe = "zz-intro-probe"
	if _, exists := seed[probe]; exists {
		t.Fatalf("%s 가 이미 시드에 있다 — 테스트 전용 이름을 바꿔라", probe)
	}
	seed[probe] = seedEntry{
		provider: ProviderAnthropic, price: Price{9, 90},
		introUntil: "2026-08-31", intro: Price{3, 30},
	}
	t.Cleanup(func() { delete(seed, probe) })

	for _, c := range []struct {
		day  string
		want Price
		why  string
	}{
		{"2026-08-04", Price{Input: 3, Output: 30}, "도입 기간인데 정가를 쓴다"},
		{"2026-08-31", Price{Input: 3, Output: 30}, "만료일 당일이 이미 정가로 넘어갔다(경계 포함이어야 한다)"},
		{"2026-09-01", Price{Input: 9, Output: 90}, "도입가가 만료 후에도 남아 있다"},
	} {
		if got := Pricing(day(c.day))[probe]; got != c.want {
			t.Fatalf("%s: %+v — %s", c.day, got, c.why)
		}
	}

	// 그리고 sonnet-5 는 **어느 날짜에도** 같아야 한다(도입가가 사라진 결과).
	for _, d := range []string{"2026-08-04", "2026-09-01"} {
		if got := Pricing(day(d))["claude-sonnet-5"]; got != (Price{Input: 2, Output: 10}) {
			t.Fatalf("%s sonnet-5 = %+v (항상 2/10 이어야 한다)", d, got)
		}
	}
}

func TestCacheMultipliersMatchOfficial(t *testing.T) {
	// 공식: 5분 쓰기 1.25배 · 1시간 쓰기 2배 · 캐시 히트 0.1배 (기본 입력가 대비)
	if CacheReadMult != 0.1 || CacheCreateMult != 1.25 || CacheCreate1hMult != 2 {
		t.Fatalf("배수 상수가 공식과 다르다: %v/%v/%v", CacheReadMult, CacheCreateMult, CacheCreate1hMult)
	}

	useConfig(t, `{}`)
	tbl := Pricing(day("2026-08-04"))
	m := Multipliers()
	// 공식 표는 캐시 단가를 절대값으로도 적어 둔다 — 배수 계산이 그 값과 맞는지 교차 검증한다.
	opus := tbl["claude-opus-5"].Input
	if opus*m.CacheCreate != 6.25 {
		t.Fatalf("5분 캐시 쓰기 단가 = %v, 공식은 $6.25", opus*m.CacheCreate)
	}
	if opus*m.CacheCreate1h != 10 {
		t.Fatalf("1시간 캐시 쓰기 단가 = %v, 공식은 $10", opus*m.CacheCreate1h)
	}
	if opus*m.CacheRead != 0.5 {
		t.Fatalf("캐시 히트 단가 = %v, 공식은 $0.50", opus*m.CacheRead)
	}
	haiku := tbl["claude-haiku-4-5"].Input
	if haiku*m.CacheCreate != 1.25 || haiku*m.CacheCreate1h != 2 {
		t.Fatalf("haiku 캐시 단가가 공식과 다르다")
	}
	if math.Abs(haiku*m.CacheRead-0.1) > 1e-12 {
		t.Fatalf("haiku 캐시 히트 단가 = %v", haiku*m.CacheRead)
	}
}

func TestCostOf_HandCalculatedRow(t *testing.T) {
	// Opus 5 기준: 입력 1M·출력 1M·캐시읽기 10M·캐시생성 1M(1시간)
	useConfig(t, `{}`)
	c := CostOf(Usage{
		Model: "claude-opus-5", Input: 1e6, Output: 1e6, CacheRead: 1e7,
		CacheCreate: 1e6, CacheCreate5m: 0, CacheCreate1h: 1e6,
	}, Pricing(day("2026-08-04")), Mult{})

	if c.ByAxis.Input != 5 {
		t.Fatalf("1M 입력 = %v, want $5", c.ByAxis.Input)
	}
	if c.ByAxis.Output != 25 {
		t.Fatalf("1M 출력 = %v, want $25", c.ByAxis.Output)
	}
	if c.ByAxis.CacheRead != 5 {
		t.Fatalf("10M 캐시읽기 = %v, want 10 × $0.50 = $5", c.ByAxis.CacheRead)
	}
	if c.ByAxis.CacheCreate != 10 {
		t.Fatalf("1M 1시간 캐시생성 = %v, want $10", c.ByAxis.CacheCreate)
	}
	if c.USD != 45 {
		t.Fatalf("usd = %v, want 45", c.USD)
	}
	if !c.TTLKnown {
		t.Fatal("TTL 분해값이 있는데 ttlKnown=false 다")
	}
}

func TestCostOf_TTLFallback(t *testing.T) {
	// TTL 분해값이 없으면 총량 + 5분 가정으로 내려가고 ttlKnown=false 로 표시한다.
	// 과거 행에 TTL 을 소급해 지어내지 않는다 — 모르는 것은 모른다고 내보낸다.
	useConfig(t, `{}`)
	tbl := Pricing(day("2026-08-04"))
	fallback := CostOf(Usage{Model: "claude-opus-5", CacheCreate: 1e6}, tbl, Mult{})
	if fallback.TTLKnown {
		t.Fatal("분해값이 없는데 ttlKnown=true 다")
	}
	if fallback.ByAxis.CacheCreate != 6.25 { // 1M × $5 × 1.25
		t.Fatalf("5분 가정 캐시생성 = %v, want 6.25", fallback.ByAxis.CacheCreate)
	}

	known := CostOf(Usage{Model: "claude-opus-5", CacheCreate: 1e6, CacheCreate1h: 1e6}, tbl, Mult{})
	if !known.TTLKnown {
		t.Fatal("분해값이 있는데 ttlKnown=false 다")
	}
	if known.ByAxis.CacheCreate != 10 { // 1M × $5 × 2
		t.Fatalf("1시간 캐시생성 = %v, want 10", known.ByAxis.CacheCreate)
	}
}

func TestCostOf_NegativeAndZeroTokens(t *testing.T) {
	// tok(): 유한하고 0 보다 큰 값만 인정한다. 음수는 0 으로 접는다.
	useConfig(t, `{}`)
	c := CostOf(Usage{Model: "claude-opus-5", Output: -1e6, Input: 0}, nil, Mult{})
	if c.USD != 0 {
		t.Fatalf("음수 토큰이 비용을 만들었다: %v", c.USD)
	}
	if !c.Priced {
		t.Fatal("등록된 모델인데 priced=false 다")
	}
}

func TestSummarize_EmptyDoesNotDie(t *testing.T) {
	useConfig(t, `{}`)
	s := Summarize(nil)
	if s.USD != 0 || s.TTLUnknownRows != 0 {
		t.Fatalf("빈 입력 요약이 이상하다: %+v", s)
	}
	// unpriced 는 빈 배열이어야 한다 — nil 이면 JSON 에서 null 로 나가 화면 분기가 갈린다.
	b, err := json.Marshal(s.Unpriced)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "[]" {
		t.Fatalf("unpriced JSON = %s, want []", b)
	}
}

func TestReadBlock_BrokenConfigFallsBackToSeed(t *testing.T) {
	// 절대 throw 하지 않는다 — 블록이 없거나 깨져도 시드로 수렴한다.
	for _, body := range []string{`{`, `[]`, `{"usage":[]}`, `{"usage":null}`, `null`} {
		useConfig(t, body)
		if got := PricedAt(); got != SeedPricedAt {
			t.Fatalf("깨진 config(%s)에서 pricedAt = %q", body, got)
		}
		if got := Multipliers(); got != (Mult{CacheReadMult, CacheCreateMult, CacheCreate1hMult}) {
			t.Fatalf("깨진 config(%s)에서 배수 = %+v", body, got)
		}
		if got := Pricing(day("2026-08-04"))["claude-opus-5"]; got != (Price{Input: 5, Output: 25}) {
			t.Fatalf("깨진 config(%s)에서 단가 = %+v", body, got)
		}
	}
}

func TestReadBlock_MissingFileIsSeed(t *testing.T) {
	t.Setenv("USAGE_CONFIG", filepath.Join(t.TempDir(), "does-not-exist.json"))
	if got := PricedAt(); got != SeedPricedAt {
		t.Fatalf("없는 config 에서 pricedAt = %q", got)
	}
}

// trimFixed1 은 JS 의 toFixed(1) 과 같은 문자열을 만든다(비중 대조용).
func trimFixed1(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }
