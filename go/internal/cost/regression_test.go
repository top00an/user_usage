package cost

// 무회귀 고정 — 멀티 공급사 개편(2026-08-10) **이전** 코드가 낸 값을 그대로 박아 둔다.
//
// 왜 별도 파일인가: cost_test.go 는 "공식 단가와 맞는가"를 본다. 이 파일은 그것과 무관하게
// **개편 전후로 같은 비트가 나오는가**만 본다. 둘은 실패했을 때 뜻이 다르다 —
// cost_test.go 가 깨지면 단가가 공식과 어긋난 것이고, 이 파일이 깨지면 개편이 기존 계산을
// 건드린 것이다. 기대값은 개편 직전 커밋의 코드를 실제로 돌려 받은 값이며, 손으로 지어내지
// 않았다. 단가 자체가 바뀌어 이 값이 달라져야 한다면 그건 **의도된 변경**이므로, 값을 고치기
// 전에 왜 바뀌는지 커밋 메시지에 남긴다.
//
// 비교는 `!=` 로 한다. 허용오차를 두면 "거의 같다"가 통과해 버려서, 배수 하나가 미세하게
// 어긋난 개편을 잡지 못한다.

import "testing"

type regressionCase struct {
	name string
	day  string
	row  Usage

	usd float64
	ax  Axis
}

// 대표 케이스: opus/sonnet × (cacheRead · cacheCreate5m · cacheCreate1h · TTL 미상 폴백).
// sonnet 은 도입가 구간(08-04)과 정가 구간(09-01)을 모두 건다.
var regressionCases = []regressionCase{
	{
		name: "opus5/cacheRead",
		day:  "2026-08-04",
		row:  Usage{Model: "claude-opus-5", Input: 1234, Output: 5678, CacheRead: 9876543},
		usd:  5.0863914999999995, ax: Axis{Input: 0.00617, Output: 0.14195, CacheRead: 4.9382715},
	},
	{
		name: "opus5/cacheCreate5m",
		day:  "2026-08-04",
		row:  Usage{Model: "claude-opus-5", Input: 1234, Output: 5678, CacheCreate: 2000000, CacheCreate5m: 2000000},
		usd:  12.64812, ax: Axis{Input: 0.00617, Output: 0.14195, CacheCreate: 12.5},
	},
	{
		name: "opus5/cacheCreate1h",
		day:  "2026-08-04",
		row:  Usage{Model: "claude-opus-5", Input: 1234, Output: 5678, CacheCreate: 2000000, CacheCreate1h: 2000000},
		usd:  20.14812, ax: Axis{Input: 0.00617, Output: 0.14195, CacheCreate: 20},
	},
	{
		name: "opus5/mixed",
		day:  "2026-08-04",
		row: Usage{Model: "claude-opus-5", Input: 62082, Output: 15235576, CacheRead: 4468209525,
			CacheCreate: 135610378, CacheCreate5m: 35610378, CacheCreate1h: 100000000},
		usd: 3837.869435, ax: Axis{Input: 0.31041, Output: 380.8894, CacheRead: 2234.1047625, CacheCreate: 1222.5648625},
	},
	{
		name: "opus5/ttlUnknownFallback",
		day:  "2026-08-04",
		row:  Usage{Model: "claude-opus-5", CacheCreate: 135610378},
		usd:  847.5648625, ax: Axis{CacheCreate: 847.5648625},
	},
	{
		name: "sonnet5/intro/cacheRead",
		day:  "2026-08-04",
		row:  Usage{Model: "claude-sonnet-5", Input: 1234, Output: 5678, CacheRead: 9876543},
		usd:  2.0345566, ax: Axis{Input: 0.002468, Output: 0.05678, CacheRead: 1.9753086000000002},
	},
	{
		name: "sonnet5/intro/cacheCreate5m",
		day:  "2026-08-04",
		row:  Usage{Model: "claude-sonnet-5", Input: 1234, Output: 5678, CacheCreate: 2000000, CacheCreate5m: 2000000},
		usd:  5.059248, ax: Axis{Input: 0.002468, Output: 0.05678, CacheCreate: 5},
	},
	/*
	 * ── 09-01 케이스: 기대값을 2026-08-13 에 갱신했다 ──────────────────────
	 *
	 * 원래 이 둘은 "인트로 만료 후의 정가($3/$15)"를 고정했다. 그 인상이 **취소됐다** —
	 * Anthropic 이 $2/$10 을 표준가로 확정했고 09-01 인상은 없다(공식 단가 문서의
	 * claude-sonnet-5-introductory-pricing 노트). 그래서 예전 기대값은 이제 존재하지 않는
	 * 요금을 고정하는 셈이다.
	 *
	 * 기대값은 코드 출력을 베끼지 않고 **공식 단가로 직접 계산**해 넣었다(입력가 $2 ·
	 * 출력가 $10 · 히트 0.1배 · 5분 쓰기 1.25배 · 1시간 쓰기 2배):
	 *   cacheCreate1h = 1234×2/1M + 5678×10/1M + 2,000,000×2×2/1M
	 *                 = 0.002468 + 0.05678 + 8 = 8.059248
	 *   mixedTTL      = 0.002468 + 0.05678 + 9,876,543×2×0.1/1M
	 *                 + (1,500,000×2×1.25 + 500,000×2×2)/1M
	 *                 = 0.002468 + 0.05678 + 1.9753086 + 5.75 = 7.7845566
	 *
	 * 날짜(09-01)는 그대로 둔다. 이제 이 두 케이스의 의미가 바뀌었다 — "만료 후 정가"가
	 * 아니라 **"인트로 창을 넘겨도 값이 그대로인가"**를 재는 자리다. 위의 08-04 케이스와
	 * 같은 단가가 나와야 한다.
	 */
	{
		name: "sonnet5/afterIntroWindow/cacheCreate1h",
		day:  "2026-09-01",
		row:  Usage{Model: "claude-sonnet-5", Input: 1234, Output: 5678, CacheCreate: 2000000, CacheCreate1h: 2000000},
		usd:  8.059248, ax: Axis{Input: 0.002468, Output: 0.05678, CacheCreate: 8},
	},
	{
		name: "sonnet5/afterIntroWindow/mixedTTL",
		day:  "2026-09-01",
		row: Usage{Model: "claude-sonnet-5", Input: 1234, Output: 5678, CacheRead: 9876543,
			CacheCreate: 2000000, CacheCreate5m: 1500000, CacheCreate1h: 500000},
		usd: 7.7845566, ax: Axis{Input: 0.002468, Output: 0.05678, CacheRead: 1.9753086000000002, CacheCreate: 5.75},
	},
	{
		name: "haiku45/cacheRead",
		day:  "2026-08-04",
		row:  Usage{Model: "claude-haiku-4-5-20251001", Input: 1234, Output: 5678, CacheRead: 9876543},
		usd:  1.0172783, ax: Axis{Input: 0.001234, Output: 0.02839, CacheRead: 0.9876543000000001},
	},
}

/*
 * 계단(롱컨텍스트) 개편(2026-08-10) **이전** 값 — 계단 모델을 롱 분리분 없이 계산했을 때.
 *
 * 왜 따로 두는가: 위의 regressionCases 는 Anthropic 뿐이라 priceLong 이 있는 모델을 하나도 안
 * 지난다. 계단 개편이 건드릴 수 있는 자리는 정확히 **priceLong 이 붙은 모델**이므로, 그쪽에
 * 분리분 0 을 넣었을 때 종전 값이 그대로 나오는지를 못박아야 게이트가 비지 않는다.
 *
 * 기대값은 개편 전 공식(계단 항이 없는 식)을 그대로 옮겨 **따로 돌려** 얻은 값이며, 개편 후
 * 코드의 출력을 받아 적은 것이 아니다. 그래야 "코드가 낸 값을 코드로 검증"하는 자기충족을
 * 피한다.
 */
var longFlatRegressionCases = []regressionCase{
	{
		name: "gemini-2.5-pro/mixed",
		day:  "2026-08-10",
		row: Usage{Model: "gemini-2.5-pro", Input: 62082, Output: 15235576,
			CacheRead: 4468209525, CacheCreate: 135610378},
		usd: 710.9595531250001,
		ax:  Axis{Input: 0.0776025, Output: 152.35576, CacheRead: 558.526190625},
	},
	{
		name: "gemini-2.5-pro/small",
		day:  "2026-08-10",
		row:  Usage{Model: "gemini-2.5-pro", Input: 1234, Output: 5678, CacheRead: 9876543},
		usd:  1.292890375,
		ax:   Axis{Input: 0.0015425, Output: 0.05678, CacheRead: 1.234567875},
	},
	{
		name: "gemini-3.1-pro-preview/small",
		day:  "2026-08-10",
		row: Usage{Model: "gemini-3.1-pro-preview", Input: 1234, Output: 5678,
			CacheRead: 9876543, CacheCreate: 2000000},
		usd: 2.0459126000000003,
		ax:  Axis{Input: 0.002468, Output: 0.068136, CacheRead: 1.9753086000000002},
	},
	{
		name: "gpt-5.5/small",
		day:  "2026-08-10",
		row: Usage{Model: "gpt-5.5", Input: 1234, Output: 5678,
			CacheRead: 9876543, CacheCreate: 2000000},
		usd: 5.1147815,
		ax:  Axis{Input: 0.00617, Output: 0.17034, CacheRead: 4.9382715},
	},
	{
		// gpt-5.6 계열만 캐시 생성에 과금한다(1.25배) — 계단을 얹어도 그 경로가 그대로여야 한다.
		name: "gpt-5.6-luna/cacheCreate",
		day:  "2026-08-10",
		row: Usage{Model: "gpt-5.6-luna", Input: 1234, Output: 5678,
			CacheRead: 9876543, CacheCreate: 2000000},
		usd: 0.7045912599999999,
		ax: Axis{Input: 0.00024680000000000004, Output: 0.0068135999999999995,
			CacheRead: 0.19753086, CacheCreate: 0.5},
	},
	{
		// TTL 분해가 들어와도 OpenAI 는 단일 배수라 값이 같아야 한다(공급사 경로가 안 섞였는지).
		name: "gpt-5.6-luna/ttlSplitIsSameAsTotal",
		day:  "2026-08-10",
		row: Usage{Model: "gpt-5.6-luna", Input: 1234, Output: 5678, CacheRead: 9876543,
			CacheCreate: 2000000, CacheCreate5m: 1500000, CacheCreate1h: 500000},
		usd: 0.7045912599999999,
		ax: Axis{Input: 0.00024680000000000004, Output: 0.0068135999999999995,
			CacheRead: 0.19753086, CacheCreate: 0.5},
	},
}

func TestRegression_AnthropicCostIsBitIdentical(t *testing.T) {
	useConfig(t, `{}`)
	for _, c := range regressionCases {
		t.Run(c.name, func(t *testing.T) {
			got := CostOf(c.row, Pricing(day(c.day)), Mult{})
			if !got.Priced {
				t.Fatalf("priced=false — 모델이 표에서 사라졌다")
			}
			if got.USD != c.usd {
				t.Fatalf("usd = %v, want %v (개편 전 값과 비트가 다르다)", got.USD, c.usd)
			}
			if got.ByAxis != c.ax {
				t.Fatalf("byAxis = %+v, want %+v", got.ByAxis, c.ax)
			}
		})
	}
}

/*
 * 계단 모델을 **분리분 없이** 계산하면 계단 개편 전과 비트가 같아야 한다.
 *
 * 이것이 무회귀의 핵심 게이트다. 기존 수집기와 이미 저장된 모든 행이 이 경로로 오므로,
 * 여기가 어긋나면 개편이 과거 데이터의 비용을 통째로 바꾼 것이다.
 */
func TestRegression_LongContextModelsUnchangedWithoutSplit(t *testing.T) {
	useConfig(t, `{}`)
	for _, c := range longFlatRegressionCases {
		t.Run(c.name, func(t *testing.T) {
			got := CostOf(c.row, Pricing(day(c.day)), Mult{})
			if !got.Priced {
				t.Fatalf("priced=false — 모델이 표에서 사라졌다")
			}
			if got.USD != c.usd {
				t.Fatalf("usd = %v, want %v (계단 개편 전 값과 비트가 다르다)", got.USD, c.usd)
			}
			if got.ByAxis != c.ax {
				t.Fatalf("byAxis = %+v, want %+v", got.ByAxis, c.ax)
			}
			// 분리분이 없으므로 계단은 개입하지 않았다는 사실이 결과에도 남아야 한다.
			if got.LongPricing != LongPricingNone || got.LongTokens != 0 {
				t.Fatalf("롱 몫이 없는데 longPricing=%q longTokens=%v", got.LongPricing, got.LongTokens)
			}
		})
	}
}
