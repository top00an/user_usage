package cost

/*
 * Google (Gemini) 시드 단가 — USD / 1M 토큰.
 * 검증일 SeedPricedAtGoogle(2026-08-05) · 출처 ai.google.dev/gemini-api/docs/pricing
 *
 * 전 모델 공통:
 *	캐시 히트  입력가의 0.10배
 *	캐시 생성  **무과금**(cacheWriteMult 0). 암시적 캐싱이라 쓰기 비용이 청구되지 않는다.
 *	          Anthropic 처럼 TTL 로 갈리는 축이 아니므로 CacheCreate5m/1h 가 들어와도 $0 이다.
 *
 * ── 계단 요금(≤200K / >200K) ────────────────────────────────────────────────
 * pro 계열은 프롬프트가 200K 토큰을 넘으면 단가가 오른다. priceLong 에 초과 구간 단가를 적어
 * 두되 **계산에는 쓰지 않는다** — 우리가 가진 행은 이미 집계된 값이라, 하루치 합계가 200K 를
 * 넘은 것과 한 요청이 200K 를 넘은 것을 구분할 수 없다.
 * 집계 후 계산이라 계단 판정 불가 — 요청단위 분리 필요(후속).
 * 지금은 기본 구간 단가만 적용한다. 즉 긴 컨텍스트를 많이 쓴 팀은 **과소** 추정된다
 * (최대 2배). LongContextPrice 로 "이 모델은 계단이 있다"는 사실만 화면에 노출한다.
 */

const gemCacheRead = 0.10

var googleSeed = map[string]seedEntry{
	// ── flash 계열 (계단 없음) ──
	"gemini-3.6-flash":      {provider: ProviderGoogle, price: Price{1.5, 7.5}, cacheReadMult: gemCacheRead},
	"gemini-3.5-flash":      {provider: ProviderGoogle, price: Price{1.5, 9}, cacheReadMult: gemCacheRead},
	"gemini-3.5-flash-lite": {provider: ProviderGoogle, price: Price{0.3, 2.5}, cacheReadMult: gemCacheRead},
	"gemini-3.1-flash-lite": {provider: ProviderGoogle, price: Price{0.25, 1.5}, cacheReadMult: gemCacheRead},
	"gemini-2.5-flash":      {provider: ProviderGoogle, price: Price{0.3, 2.5}, cacheReadMult: gemCacheRead},
	"gemini-2.5-flash-lite": {provider: ProviderGoogle, price: Price{0.1, 0.4}, cacheReadMult: gemCacheRead},

	// ⚠ gemini-3-flash-preview 와 gemini-3-flash 는 **다른 모델이다.**
	//   gemini-3-flash-preview : 프리뷰 전용 ID. $0.50 / $3.00
	//   gemini-3-flash         : gemini-3.5-flash 의 **별칭**. $1.50 / $9.00 (아래 참조)
	// 접두사 매칭으로 조회하면 별칭이 프리뷰 단가로 붙어 **3배 틀린다**. 이 패키지는 정확
	// 일치로만 조회하고, 별칭을 별도 키로 등록해 그 사고를 구조적으로 막는다.
	"gemini-3-flash-preview": {provider: ProviderGoogle, price: Price{0.5, 3}, cacheReadMult: gemCacheRead},

	// gemini-3-flash 는 소스의 SECONDARY_GEMINI_3_5_FLASH_MODEL — gemini-3.5-flash 와 같은
	// 모델을 가리키는 별칭이므로 **같은 단가**여야 한다. 별칭 해석 로직을 따로 두지 않고 표에
	// 실체로 박아 둔다(로직이 없으면 로직이 틀릴 일도 없다).
	"gemini-3-flash": {provider: ProviderGoogle, price: Price{1.5, 9}, cacheReadMult: gemCacheRead},

	// ── pro 계열 (계단 요금 — priceLong 은 정의만, 미적용) ──
	"gemini-2.5-pro": {
		provider: ProviderGoogle, price: Price{1.25, 10}, priceLong: Price{2.5, 15},
		cacheReadMult: gemCacheRead,
	},
	"gemini-3.1-pro-preview": {
		provider: ProviderGoogle, price: Price{2, 12}, priceLong: Price{4, 18},
		cacheReadMult: gemCacheRead,
	},
	"gemini-3.1-pro-preview-customtools": {
		provider: ProviderGoogle, price: Price{2, 12}, priceLong: Price{4, 18},
		cacheReadMult: gemCacheRead,
	},
}

// googleUnpriced 는 **일부러 등록하지 않은** 모델이다.
// gemini-3-pro-preview 는 공식 가격표에 항목이 없다. gemini-3.1-pro-preview 단가를 빌려 오고
// 싶어지지만, 그건 추측이고 화면에는 추측이라는 표시가 남지 않는다. 미등록으로 두면
// Result.Unpriced 에 이름이 올라가 빠졌다는 사실이 보인다.
var googleUnpriced = []string{
	"gemini-3-pro-preview",
}
