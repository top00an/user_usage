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
	{
		name: "sonnet5/list/cacheCreate1h",
		day:  "2026-09-01",
		row:  Usage{Model: "claude-sonnet-5", Input: 1234, Output: 5678, CacheCreate: 2000000, CacheCreate1h: 2000000},
		usd:  12.088872, ax: Axis{Input: 0.003702, Output: 0.08517, CacheCreate: 12},
	},
	{
		name: "sonnet5/list/mixedTTL",
		day:  "2026-09-01",
		row: Usage{Model: "claude-sonnet-5", Input: 1234, Output: 5678, CacheRead: 9876543,
			CacheCreate: 2000000, CacheCreate5m: 1500000, CacheCreate1h: 500000},
		usd: 11.6768349, ax: Axis{Input: 0.003702, Output: 0.08517, CacheRead: 2.9629629000000004, CacheCreate: 8.625},
	},
	{
		name: "haiku45/cacheRead",
		day:  "2026-08-04",
		row:  Usage{Model: "claude-haiku-4-5-20251001", Input: 1234, Output: 5678, CacheRead: 9876543},
		usd:  1.0172783, ax: Axis{Input: 0.001234, Output: 0.02839, CacheRead: 0.9876543000000001},
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
