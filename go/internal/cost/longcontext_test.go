package cost

// 계단(롱컨텍스트) 요금 — 요청단위 분리분이 들어왔을 때의 계산.
//
// 이 파일이 보는 것은 셋이다:
//   ① 분리분이 0/부재면 **개편 전과 비트 동일**하다(regression_test.go 가 값을 못박고, 여기서는
//      "롱 필드를 안 채운 것"과 "0 으로 채운 것"이 같은 비트인지를 본다).
//   ② 롱 몫에 롱 단가가 붙는다 — 손으로 계산한 값과 정확히 같다.
//   ③ 롱 단가를 **모르는** 경우와 **계단이 없는** 경우를 구분해 내보낸다.
//      둘을 뭉치면 "이 모델은 계단이 없다"는 없는 사실이 만들어진다.

import "testing"

/*
 * ── ② 계단 적용 실증 — gemini-2.5-pro, 손 검산 ──────────────────────────────
 *
 * 단가(seed_google.go): price {1.25, 10} · priceLong {2.5, 15} · cacheReadMult 0.10
 *
 *	입력      표준 100,000 × 1.25 =   125,000
 *	          롱   300,000 × 2.50 =   750,000   합 875,000 / 1e6 = $0.875
 *	출력      표준  10,000 × 10   =   100,000
 *	          롱    30,000 × 15   =   450,000   합 550,000 / 1e6 = $0.55
 *	캐시읽기  표준  50,000 × 1.25 =    62,500
 *	          롱   150,000 × 2.50 =   375,000   합 437,500 × 0.1 / 1e6 = $0.04375
 *	                                            총합 $1.46875
 *
 * 캐시읽기의 롱 단가는 **롱 입력가 × cacheReadMult** 다(공식 표가 그렇게 되어 있다:
 * 2.5-pro 캐시 0.125→0.25 는 입력 1.25→2.50 과 같은 0.1 배수다).
 */
func TestLongContext_Gemini25Pro_HandChecked(t *testing.T) {
	useConfig(t, `{}`)

	row := Usage{
		Model: "gemini-2.5-pro",
		Input: 400_000, InputLong: 300_000,
		Output: 40_000, OutputLong: 30_000,
		CacheRead: 200_000, CacheReadLong: 150_000,
	}
	got := CostOf(row, Pricing(day("2026-08-10")), Mult{})

	if !got.Priced {
		t.Fatal("priced=false — 모델이 표에서 사라졌다")
	}
	if got.LongPricing != LongPricingApplied {
		t.Fatalf("longPricing = %q, want %q", got.LongPricing, LongPricingApplied)
	}
	if got.LongTokens != 480_000 {
		t.Fatalf("longTokens = %v, want 480000", got.LongTokens)
	}

	// 손 검산값. 곱셈 순서가 코드와 같아야 비트가 맞는다 — 식을 "같은 뜻의 다른 모양"으로
	// 바꾸면 마지막 자리에서 갈린다.
	wantInput := ((100_000 * 1.25) + (300_000 * 2.5)) / 1e6
	wantOutput := ((10_000 * 10.0) + (30_000 * 15.0)) / 1e6
	wantCacheRead := (((50_000 * 1.25) + (150_000 * 2.5)) * 0.1) / 1e6

	if got.ByAxis.Input != wantInput {
		t.Fatalf("input = %v, want %v", got.ByAxis.Input, wantInput)
	}
	if got.ByAxis.Output != wantOutput {
		t.Fatalf("output = %v, want %v", got.ByAxis.Output, wantOutput)
	}
	if got.ByAxis.CacheRead != wantCacheRead {
		t.Fatalf("cacheRead = %v, want %v", got.ByAxis.CacheRead, wantCacheRead)
	}
	if got.ByAxis.CacheCreate != 0 {
		t.Fatalf("cacheCreate = %v, want 0 (Google 은 캐시 생성 무과금)", got.ByAxis.CacheCreate)
	}
	// 유리수로 정확히 1.46875 다(2의 거듭제곱 분모라 float64 로 정확히 표현된다).
	if got.USD != 1.46875 {
		t.Fatalf("usd = %v, want 1.46875", got.USD)
	}

	// 계단이 실제로 붙었는지 — 같은 토큰을 전부 표준 구간으로 두면 값이 **더 싸야** 한다.
	flat := CostOf(Usage{Model: "gemini-2.5-pro", Input: 400_000, Output: 40_000, CacheRead: 200_000},
		Pricing(day("2026-08-10")), Mult{})
	if !(flat.USD < got.USD) {
		t.Fatalf("계단이 적용되지 않았다: flat=%v long=%v", flat.USD, got.USD)
	}
	if flat.LongPricing != LongPricingNone {
		t.Fatalf("롱 몫이 없는 행에 longPricing=%q 가 붙었다", flat.LongPricing)
	}
}

/*
 * OpenAI 계단 — 입력 2배 · 출력 1.5배가 표에 그대로 들어갔는지.
 *
 * 비교는 approx 다. 배수를 곱한 값(1.2 × 1.5)이 float64 에서 1.7999999999999998 이라
 * 공표 단가 $1.80 과 마지막 비트가 다르다 — 표에는 **공표 값**을 적는 것이 맞고, 여기서
 * `!=` 를 쓰면 맞는 표가 빨개진다. (비트 동일을 요구하는 자리는 regression_test.go 다.)
 */
func TestLongContext_OpenAIStepIsTwoAndOneAndHalf(t *testing.T) {
	useConfig(t, `{}`)
	// 2026-08-13 전수 재감사: gpt-5.4 와 *-pro 둘도 공식 표에 Long context 가 있다.
	// 이전 판단("계단 계열은 5.5 와 5.6-* 뿐")이 틀렸고, 그 탓에 계단 몫이 표준가로
	// 계산돼 **과소**계상되고 있었다.
	for _, m := range []string{"gpt-5.5", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna",
		"gpt-5.4", "gpt-5.5-pro", "gpt-5.4-pro"} {
		base, ok := seed[m]
		if !ok {
			t.Fatalf("%s 가 시드에서 사라졌다", m)
		}
		pl, ok := LongContextPrice(m)
		if !ok {
			t.Fatalf("%s 에 롱 단가가 없다 — 계단 모델인데 flat 으로 오분류된다", m)
		}
		approx(t, m+" 롱 입력가(2배)", pl.Input, base.price.Input*2)
		approx(t, m+" 롱 출력가(1.5배)", pl.Output, base.price.Output*1.5)
	}

	// 계열 이름이 비슷해도 공식 표에 항목이 없는 것은 등록하지 않았다 — flat 으로 나가야 한다
	// (여기가 무너지면 추측 단가가 표에 들어온 것이다).
	//
	// cyber 계열은 공식 표가 "short context only" 라고 명시한다 — 계단이 **없는 것이 사실**이고
	// 모르는 것이 아니다. 5.2/5.1/5 계열도 Long context 항목이 없다.
	for _, m := range []string{"gpt-5.5-cyber", "gpt-5.6-cyber", "gpt-5.2", "gpt-5.2-pro",
		"gpt-5.1", "gpt-5", "gpt-5-pro", "gpt-5.4-mini", "gpt-5.4-nano", "gpt-5.3-codex"} {
		if _, tiered := LongContextPrice(m); tiered {
			t.Fatalf("%s 에 출처 없는 롱 단가가 붙었다", m)
		}
	}
}

/*
 * ── ③ "계단이 없다" 와 "롱 단가를 모른다" 는 다른 사실이다 ──────────────────
 *
 * 둘 다 표준가로 계산하지만, 뜻이 다르다:
 *   flat    시드에 있는 모델인데 롱 단가 항목이 없다 = 우리 표 기준 계단 없음(정확한 값이다).
 *   unknown 시드 밖 모델(config 로 단가만 꽂힌 사내 게이트웨이 등) = 계단 여부 자체를 모른다.
 *           표준가로 계산했으므로 **과소일 수 있다.**
 */
func TestLongContext_FlatVsUnknownAreDistinct(t *testing.T) {
	useConfig(t, `{"usage":{"pricing":{"internal-gw-model":{"input":10,"output":30}}}}`)
	table := Pricing(day("2026-08-10"))

	flat := CostOf(Usage{Model: "claude-opus-5", Input: 1000, InputLong: 1000}, table, Mult{})
	if flat.LongPricing != LongPricingFlat {
		t.Fatalf("계단 없는 시드 모델 longPricing = %q, want %q", flat.LongPricing, LongPricingFlat)
	}
	// 표준가로 계산한다 — 롱 단가가 없다고 비용을 0 으로 만들지 않는다.
	if flat.ByAxis.Input != (1000*5.0)/1e6 {
		t.Fatalf("flat 입력 비용 = %v, want %v", flat.ByAxis.Input, (1000*5.0)/1e6)
	}

	unknown := CostOf(Usage{Model: "internal-gw-model", Input: 1000, InputLong: 1000}, table, Mult{})
	if !unknown.Priced {
		t.Fatal("config 로 단가를 꽂은 모델이 priced=false 다")
	}
	if unknown.LongPricing != LongPricingUnknown {
		t.Fatalf("시드 밖 모델 longPricing = %q, want %q", unknown.LongPricing, LongPricingUnknown)
	}
	if unknown.ByAxis.Input != (1000*10.0)/1e6 {
		t.Fatalf("unknown 입력 비용 = %v, want %v", unknown.ByAxis.Input, (1000*10.0)/1e6)
	}
}

// Summarize 는 "롱 몫이 있는데 단가를 몰라 표준가로 계산한 행"을 센다.
// 세지 않으면 화면이 과소 추정이라는 사실을 말할 수 없다(TTLUnknownRows 와 같은 규율).
func TestLongContext_SummarizeCountsUnknownRows(t *testing.T) {
	useConfig(t, `{"usage":{"pricing":{"internal-gw-model":{"input":10,"output":30}}}}`)

	sum := Summarize([]Usage{
		{Model: "gemini-2.5-pro", Input: 1000, InputLong: 1000},   // applied
		{Model: "claude-opus-5", Input: 1000, InputLong: 1000},    // flat
		{Model: "internal-gw-model", Input: 1000, InputLong: 500}, // unknown
		{Model: "internal-gw-model", Input: 1000},                 // 롱 몫 없음 — 세지 않는다
	})
	if sum.LongUnknownRows != 1 {
		t.Fatalf("longUnknownRows = %d, want 1", sum.LongUnknownRows)
	}
	if sum.LongTokens != 2500 {
		t.Fatalf("longTokens = %v, want 2500", sum.LongTokens)
	}
}

/*
 * 불변식 방어 — 총량을 넘는 롱 몫은 총량으로 접는다.
 *
 * 인테이크가 이미 접지만(그쪽이 신뢰 경계이고 위반을 센다), 여기서 한 번 더 접는 이유는
 * 표준 몫이 **음수**가 되는 것을 구조적으로 막기 위해서다 — 음수 토큰은 비용을 깎아
 * 합계를 조용히 줄인다. DB 에 이미 들어간 옛 행이 그럴 수 있다.
 */
func TestLongContext_LongOverTotalIsClamped(t *testing.T) {
	useConfig(t, `{}`)
	table := Pricing(day("2026-08-10"))

	over := CostOf(Usage{Model: "gemini-2.5-pro", Input: 1000, InputLong: 9999}, table, Mult{})
	exact := CostOf(Usage{Model: "gemini-2.5-pro", Input: 1000, InputLong: 1000}, table, Mult{})
	if over.USD != exact.USD {
		t.Fatalf("총량 초과 롱 몫이 접히지 않았다: over=%v exact=%v", over.USD, exact.USD)
	}
	// 음수는 0 이다(롱 몫 없음). 표준 몫이 총량을 넘어서면 안 된다.
	neg := CostOf(Usage{Model: "gemini-2.5-pro", Input: 1000, InputLong: -500}, table, Mult{})
	flat := CostOf(Usage{Model: "gemini-2.5-pro", Input: 1000}, table, Mult{})
	if neg.USD != flat.USD {
		t.Fatalf("음수 롱 몫이 0 으로 접히지 않았다: neg=%v flat=%v", neg.USD, flat.USD)
	}
}

// 롱 필드를 **명시적으로 0** 으로 채운 것과 아예 안 채운 것이 같은 비트여야 한다.
// 여기가 무회귀의 경계다 — 기존 수집기·기존 데이터는 전부 이 경로로 온다.
func TestLongContext_ZeroLongIsBitIdenticalToAbsent(t *testing.T) {
	useConfig(t, `{}`)

	for _, c := range regressionCases {
		t.Run(c.name, func(t *testing.T) {
			absent := CostOf(c.row, Pricing(day(c.day)), Mult{})

			explicit := c.row
			explicit.InputLong, explicit.OutputLong, explicit.CacheReadLong = 0, 0, 0
			got := CostOf(explicit, Pricing(day(c.day)), Mult{})

			if got.USD != absent.USD || got.ByAxis != absent.ByAxis {
				t.Fatalf("롱 0 이 부재와 다르다: got %v/%+v, want %v/%+v",
					got.USD, got.ByAxis, absent.USD, absent.ByAxis)
			}
			if got.LongPricing != LongPricingNone {
				t.Fatalf("롱 몫 0 인데 longPricing = %q", got.LongPricing)
			}
		})
	}
}
