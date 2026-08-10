package cost

/*
 * OpenAI 시드 단가 — USD / 1M 토큰.
 * 검증일 SeedPricedAtOpenAI(2026-08-10) · 출처 developers.openai.com/api/docs/pricing
 *
 * ── 왜 모델마다 cacheReadMult 를 따로 적는가 ────────────────────────────────
 * OpenAI 의 캐시 히트 할인율은 세대마다 다르고, 같은 세대 안에서도 갈린다:
 *
 *	0.10  gpt-5.x 본선 계열(gpt-5 · 5.1 · 5.2 · 5.4 · 5.5 · 5.6, codex·cyber 파생 포함)
 *	0.25  o3 · o4-mini · gpt-4.1 계열
 *	0.50  o1 · o3-mini · gpt-4o 계열
 *	1.00  *-pro 계열 — **캐시 할인이 아예 없다**
 *
 * 전역 0.1 을 그대로 적용하면 *-pro 의 캐시읽기 비용이 실제의 1/10 로 잡힌다. o1-pro 처럼
 * 입력가가 $150/MTok 인 모델에서 이 오차는 곧바로 세 자릿수 달러다.
 *
 * ── 캐시 **생성** 비용 ──────────────────────────────────────────────────────
 * OpenAI 는 자동 캐싱이라 대부분 쓰기 비용이 없다(cacheWriteMult 0 = 무과금).
 * 예외는 GPT-5.6 계열로, 명시적 캐시 쓰기에 입력가의 1.25배를 매긴다.
 *
 * ── 계단 요금(≤272K / >272K) ────────────────────────────────────────────────
 * gpt-5.5 와 gpt-5.6 계열은 **요청의 입력이 272K 토큰을 넘으면** 그 요청 전체에 할증이 붙는다:
 * 입력 2배 · 출력 1.5배. (Google 과 임계값도 배수도 다르다 — 공급사별로 따로 적는 이유다.)
 *
 * priceLong 값은 지어낸 것이 아니라 **위 price 에 공식 배수를 적용한 것**이다.
 * 예: gpt-5.5 $5/$30 → $10/$45. 파생값이지만 표에 박아 둔다 — 배수를 코드로 두면
 * "어느 모델에 계단이 있는가"가 로직이 되고, 그 로직이 틀리면 조용히 전 모델에 번진다.
 * (longcontext_test.go 가 배수 2/1.5 를 다시 대조한다.)
 *
 * ⚠ **계열 이름만 보고 넓히지 않았다.** gpt-5.5-pro·gpt-5.5-cyber·gpt-5.4* 는 공식 가격표에
 *   롱 컨텍스트 항목이 따로 없다. 이름이 비슷하다고 배수를 빌려 오면 그건 추측이고, 화면에는
 *   추측이라는 표시가 남지 않는다. 미등록으로 두면 계산은 표준가로 가고 Result.LongPricing 이
 *   flat(우리 표 기준 계단 없음)으로 나간다. 공식 표에 항목이 생기면 그때 여기에 적는다.
 *
 * ── 단가 미공개 모델은 **등록하지 않는다** ──────────────────────────────────
 * gpt-5.4-cyber · codex-auto-review · gpt-5-codex · gpt-5.1-codex* 는 공식 가격표에 항목이
 * 없다. 비슷한 이름의 단가를 빌려 오면 화면에는 그럴듯한 숫자가 뜨지만 그게 맞는지 아무도
 * 알 수 없다. 미등록으로 두면 Result.Unpriced 에 이름이 올라가 "이건 비용에서 빠져 있다"고
 * 화면이 말한다 — 틀린 숫자보다 없는 숫자가 낫다.
 */

const (
	oaiCacheRead     = 0.1  // gpt-5.x 본선
	oaiCacheReadO3   = 0.25 // o3 · o4-mini · gpt-4.1
	oaiCacheRead4o   = 0.5  // o1 · o3-mini · gpt-4o
	oaiCacheReadNone = 1.0  // *-pro — 할인 없음

	oaiCacheWrite56 = 1.25 // GPT-5.6 계열의 명시적 캐시 쓰기
)

var openaiSeed = map[string]seedEntry{
	// ── GPT-5.x 본선 (캐시 히트 0.1배) ──
	// 계단 요금 — 입력 >272K 이면 요청 전체가 입력 2배 · 출력 1.5배.
	"gpt-5.5":       {provider: ProviderOpenAI, price: Price{5, 30}, priceLong: Price{10, 45}, cacheReadMult: oaiCacheRead},
	"gpt-5.4-mini":  {provider: ProviderOpenAI, price: Price{0.75, 4.5}, cacheReadMult: oaiCacheRead},
	"gpt-5.4":       {provider: ProviderOpenAI, price: Price{2.5, 15}, cacheReadMult: oaiCacheRead},
	"gpt-5.4-nano":  {provider: ProviderOpenAI, price: Price{0.2, 1.25}, cacheReadMult: oaiCacheRead},
	"gpt-5.2":       {provider: ProviderOpenAI, price: Price{1.75, 14}, cacheReadMult: oaiCacheRead},
	"gpt-5.1":       {provider: ProviderOpenAI, price: Price{1.25, 10}, cacheReadMult: oaiCacheRead},
	"gpt-5":         {provider: ProviderOpenAI, price: Price{1.25, 10}, cacheReadMult: oaiCacheRead},
	"gpt-5-mini":    {provider: ProviderOpenAI, price: Price{0.25, 2}, cacheReadMult: oaiCacheRead},
	"gpt-5-nano":    {provider: ProviderOpenAI, price: Price{0.05, 0.4}, cacheReadMult: oaiCacheRead},
	"gpt-5.3-codex": {provider: ProviderOpenAI, price: Price{1.75, 14}, cacheReadMult: oaiCacheRead},
	"gpt-5.5-cyber": {provider: ProviderOpenAI, price: Price{12.5, 75}, cacheReadMult: oaiCacheRead},

	// ── GPT-5.6 계열 — 유일하게 캐시 **생성**에 과금한다(1.25배). 계단도 있다(2배/1.5배) ──
	"gpt-5.6-sol":   {provider: ProviderOpenAI, price: Price{5, 30}, priceLong: Price{10, 45}, cacheReadMult: oaiCacheRead, cacheWriteMult: oaiCacheWrite56},
	"gpt-5.6-terra": {provider: ProviderOpenAI, price: Price{2, 12}, priceLong: Price{4, 18}, cacheReadMult: oaiCacheRead, cacheWriteMult: oaiCacheWrite56},
	"gpt-5.6-luna":  {provider: ProviderOpenAI, price: Price{0.2, 1.2}, priceLong: Price{0.4, 1.8}, cacheReadMult: oaiCacheRead, cacheWriteMult: oaiCacheWrite56},

	// ── *-pro — 캐시 할인 없음(1.0배). 배수를 0 으로 두면 전역 0.1 로 떨어져 10배 과소가 된다. ──
	"gpt-5.5-pro": {provider: ProviderOpenAI, price: Price{30, 180}, cacheReadMult: oaiCacheReadNone},
	"gpt-5.4-pro": {provider: ProviderOpenAI, price: Price{30, 180}, cacheReadMult: oaiCacheReadNone},
	"gpt-5.2-pro": {provider: ProviderOpenAI, price: Price{21, 168}, cacheReadMult: oaiCacheReadNone},
	"gpt-5-pro":   {provider: ProviderOpenAI, price: Price{15, 120}, cacheReadMult: oaiCacheReadNone},

	// ── o 시리즈 ──
	"o3":      {provider: ProviderOpenAI, price: Price{2, 8}, cacheReadMult: oaiCacheReadO3},
	"o4-mini": {provider: ProviderOpenAI, price: Price{1.1, 4.4}, cacheReadMult: oaiCacheReadO3},
	"o3-mini": {provider: ProviderOpenAI, price: Price{1.1, 4.4}, cacheReadMult: oaiCacheRead4o},
	"o1":      {provider: ProviderOpenAI, price: Price{15, 60}, cacheReadMult: oaiCacheRead4o},
	"o1-pro":  {provider: ProviderOpenAI, price: Price{150, 600}, cacheReadMult: oaiCacheReadNone},
	"o3-pro":  {provider: ProviderOpenAI, price: Price{20, 80}, cacheReadMult: oaiCacheReadNone},

	// ── GPT-4 계열 ──
	"gpt-4.1":      {provider: ProviderOpenAI, price: Price{2, 8}, cacheReadMult: oaiCacheReadO3},
	"gpt-4.1-mini": {provider: ProviderOpenAI, price: Price{0.4, 1.6}, cacheReadMult: oaiCacheReadO3},
	"gpt-4.1-nano": {provider: ProviderOpenAI, price: Price{0.1, 0.4}, cacheReadMult: oaiCacheReadO3},
	"gpt-4o":       {provider: ProviderOpenAI, price: Price{2.5, 10}, cacheReadMult: oaiCacheRead4o},
	"gpt-4o-mini":  {provider: ProviderOpenAI, price: Price{0.15, 0.6}, cacheReadMult: oaiCacheRead4o},
}

// openaiUnpriced 는 **일부러 등록하지 않은** 모델이다. 목록으로 남겨 두는 이유는 다음 사람이
// "빠뜨렸나?" 하고 아무 단가나 채워 넣지 않게 하기 위해서다. 공식 가격표에 항목이 생기면
// 그때 openaiSeed 로 옮긴다. 전부 2026-08-05 확인 시점에 공식 표에 항목이 없었다.
var openaiUnpriced = []string{
	"gpt-5.4-cyber",
	"codex-auto-review",
	"gpt-5-codex",
	"gpt-5.1-codex",
	"gpt-5.1-codex-mini",
	"gpt-5.1-codex-max",
	// gpt-oss-120b 는 오픈웨이트 모델이라 **호스팅 사업자마다 단가가 다르다** — 공식 가격표에
	// 항목이 없고(2026-08-05 확인), 어디서 돌렸는지를 사용량 레코드가 말해 주지 않는다.
	// 아무 제공자의 단가나 골라 박으면 그 숫자가 맞는지 아무도 확인할 수 없다.
	"gpt-oss-120b",
}
