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
 * pro 계열은 **요청의 입력 컨텍스트가 200K 토큰을 넘으면** 그 요청의 입력·출력 단가가 함께
 * 오른다. 공식 문구가 그 범위를 못박는다:
 *   "If a query input context is longer than 200K tokens, all tokens (input and output)
 *    are charged at long context rates."
 * 즉 임계를 넘긴 요청은 출력까지 롱 단가다 — 입력만 올리면 과소가 된다.
 *
 * 임계값 판정은 **수집기**가 한다(요청 원문을 보는 유일한 자리다). 서버는 수집기가 분리해
 * 보내 준 몫(Usage.InputLong/OutputLong/CacheReadLong)에 아래 priceLong 을 곱한다.
 * 분리분이 없으면(구버전 수집기·기존 데이터) 전부 표준 구간이고 결과는 종전과 같다.
 *
 * 캐시 히트의 롱 단가는 표에 따로 적지 않는다 — **롱 입력가 × cacheReadMult** 로 나온다.
 * (2.5-pro 캐시 0.125 → 0.25 는 입력 1.25 → 2.50 과 같은 0.1 배수다.)
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

	// ── pro 계열 (계단 요금 — 입력 >200K 이면 그 요청의 입력·출력이 모두 priceLong) ──
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
