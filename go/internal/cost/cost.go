// Package cost — 사용량 → 비용 환산.
//
// 왜 필요한가(실측): 화면이 토큰 4축을 원자료 그대로 보여주는 동안, 같은 사용량을 두고 도구마다
// 176배 차이 나는 숫자가 나왔다. 원인은 화면이 가장 크게 띄운 값이 **출력 토큰 하나**였다는 것이다.
// 실제 비용 구성은 정반대다(한 사용자의 한 달, opus 급 모델):
//
//	캐시읽기 4,468 MTok → $2,234 (64.5%)   ← 작은 글씨로 밀려 있던 축
//	캐시생성   136 MTok →   $848 (24.5%)
//	출력        15 MTok →   $381 (11.0%)   ← 화면이 제일 크게 보여주던 축
//	입력      0.06 MTok →     $0.31
//
// 토큰 수가 큰 축과 비용이 큰 축이 다르다. 축마다 단가 배수가 다르기 때문이고, 그래서 "총 토큰량"
// 합산은 비용도 작업량도 대변하지 못한다. 이 패키지가 그 환산을 한 곳에서 책임진다.
//
// 원칙:
//   - **저장하지 않고 읽을 때마다 계산한다.** 비용을 컬럼으로 굳히면 단가가 바뀌었을 때 과거
//     수치가 옛 단가에 묶인다. 단가는 실제로 바뀐다(Sonnet 5 도입가 참조).
//   - 호출 시점에 설정을 읽는다(캐시 금지 — USAGE_CONFIG 테스트 격리 보존).
//   - **절대 실패를 밖으로 내지 않는다** — 블록이 없거나 깨져도 시드로 수렴한다.
//   - 비밀 없음(순수 정책값).
//   - 모르는 모델은 priced=false 로 두고 이름을 남긴다. **조용히 $0 으로 처리하지 않는다** —
//     그러면 합계가 틀렸다는 사실이 화면에서 사라진다.
//   - 캐시 배수는 **모델별**이다. 전역 상수 하나로 두면 Anthropic 기준이 OpenAI·Google 에
//     그대로 적용돼 비용이 조용히 틀린다(예: o3 의 캐시읽기는 0.1배가 아니라 0.25배다).
//
// ── 이 패키지가 받는 토큰은 **이미 정규화된 값**이다 (중요) ──────────────────
//
// 공급사마다 usage 필드의 의미가 다르다. 그 차이를 흡수하는 것은 **수집기(리더)의 책임**이고,
// cost 는 정규화가 끝난 값을 받는다고 가정한다. 이 전제가 깨지면 비용이 조용히 부풀어 오른다.
//
//	① input 과 캐시의 관계 — 축이 서로소인가?
//	   Anthropic  input 이 캐시를 **제외**한다(input · cacheRead · cacheCreate 가 서로소).
//	   OpenAI     input(prompt_tokens)이 캐시를 **포함**한다. (실측 3,336행 대조로 확인)
//	   Google     input(promptTokenCount)이 캐시를 **포함**한다. (공식 문서의 정의)
//	   → 리더가 저장 전에 `input = max(0, input − cached)` 로 정규화해야 한다.
//	     안 하면 캐시된 입력이 입력가로 한 번·캐시읽기가로 또 한 번 청구되어 **최대 1.8배**
//	     부푼다. cost 는 집계된 행만 보므로 이 중복을 **탐지할 수 없다**.
//
//	② reasoning / thinking 토큰 — output 에 들어 있는가?
//	   OpenAI  reasoning_tokens 는 output(completion_tokens)에 **이미 포함**이다 → 더하면 이중 계상.
//	   Google  thoughtsTokenCount 는 output(candidatesTokenCount)에 **미포함**이다 → 더해야 한다.
//	   → 이것도 리더의 책임이다. cost 는 row.Output 을 "청구 대상 출력 토큰 전부"로 읽는다.
//
// 표준 라이브러리 말고 아무것도 import 하지 않는다.
package cost

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Price 는 USD / 1M 토큰.
type Price struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
}

// Table 은 정규화된 모델명 → 단가.
type Table map[string]Price

// Mult 는 입력가 대비 캐시 축 배수다.
//
// 계약서(go/CONTRACT.md)는 이 타입을 `Multipliers` 로 적었지만, 같은 이름의 함수
// `Multipliers()` 와 한 패키지에서 공존할 수 없다(Go 는 타입·함수 이름공간이 같다).
// 호출부가 실제로 쓰는 것은 **함수 이름**이므로 함수를 계약대로 두고 타입만 Mult 로 줄였다.
type Mult struct {
	CacheRead     float64 `json:"cacheRead"`
	CacheCreate   float64 `json:"cacheCreate"`
	CacheCreate1h float64 `json:"cacheCreate1h"`
}

// Axis 는 축별 비용(USD).
type Axis struct {
	Input       float64 `json:"input"`
	Output      float64 `json:"output"`
	CacheRead   float64 `json:"cacheRead"`
	CacheCreate float64 `json:"cacheCreate"`
}

// Usage 는 비용을 매길 한 행이다 — 세션·시간버킷·집계 무엇이든 된다.
//
// 토큰 수를 float64 로 두는 이유: 현행 JS 가 Number(=float64)로 계산하고, 골든이 그 결과를
// 마지막 자리까지 비교한다. int64 로 받아 변환하면 같은 값이지만, 계산 도중 형변환이 끼면
// 어긋날 여지를 남긴다.
type Usage struct {
	Model         string
	Input         float64
	Output        float64
	CacheRead     float64
	CacheCreate   float64
	CacheCreate5m float64
	CacheCreate1h float64

	// ── 계단(롱컨텍스트) 분리분 ────────────────────────────────────────────
	//
	// **총량 중 롱 구간 요청에서 발생한 몫**이다. 위의 Input/Output/CacheRead 는 의미가
	// 바뀌지 않는다 — 여전히 전체 합계이고, 표준 구간 몫은 `총량 − Long` 으로 나온다.
	//
	// 왜 몫으로 받나: 이 패키지는 **집계된 행**을 받으므로 "이 요청이 임계값을 넘었는가"를
	// 판정할 수 없다(하루치 합계가 200K 를 넘는 것과 한 요청이 200K 를 넘는 것은 다른
	// 얘기다). 판정은 요청 원문을 보는 수집기만 할 수 있고, 그 결과가 이 세 필드다.
	//
	// **0/부재가 기존 동작이다.** 전부 표준 구간으로 계산되며 결과는 개편 전과 비트 동일하다
	// (regression_test.go · longcontext_test.go 가 못박는다).
	//
	/*
	 * CacheCreateLong — 캐시 **생성**의 롱 몫.
	 *
	 * ⚠ 예전에는 "계단을 쓰는 두 공급사가 캐시 생성에 롱 단가를 두지 않는다"고 적고 이 필드를
	 *   두지 않았다. 2026-08-13 전수 감사에서 그것이 틀린 것을 잡았다 — OpenAI 공식 표는
	 *   5.6 계열의 캐시 쓰기를 **두 값**으로 싣는다(sol $6.25/$12.50 · terra $2.50/$5.00 ·
	 *   luna $0.25/$0.50). 앞이 표준 구간, 뒤가 롱 구간이고 각각 1.25 × 해당 구간 입력가다.
	 *   없으면 롱 구간의 캐시 쓰기가 표준가로 계산돼 **과소**계상된다.
	 *
	 *   Google 은 여전히 해당 없다(쓰기 토큰 과금 자체가 없다). Anthropic 도 해당 없다
	 *   (4.6+ 는 1M 컨텍스트가 표준가라 계단이 없다). 즉 실제로 이 필드가 값을 갖는 것은
	 *   OpenAI 5.6 계열뿐이고, 그쪽은 TTL 로 갈리지 않아(5분·1시간 배수가 같은 1.25)
	 *   롱 몫에 어느 TTL 배수를 쓸지 모호하지 않다.
	 */
	InputLong       float64
	OutputLong      float64
	CacheReadLong   float64
	CacheCreateLong float64

	/*
	 * ── 고속 모드(fast mode) 분리분 ─────────────────────────────────────────
	 *
	 * **총량 중 고속 모드로 처리된 몫**이다(롱 분리분과 같은 계약 — 총량의 부분집합).
	 *
	 * 왜 필요한가: 고속 모드는 같은 모델에 **다른 단가**가 붙는다.
	 *   Anthropic  Claude Opus 5 / Opus 4.8 — $10/$50 (표준 $5/$25 의 정확히 2배).
	 *              캐시 배수는 그 위에 얹힌다(공식: "Prompt caching multipliers apply on top
	 *              of fast mode pricing") → 캐시 축도 2배다.
	 *   OpenAI     공식 단가 표 각주 "Fast mode pricing is doubled." → 같은 2배.
	 *
	 * 두 공급사가 같은 배수라서 단일 상수(FastMult)로 갈음한다. 갈리면 seedEntry 로 내린다.
	 *
	 * ⚠ **원천은 이미 있다.** Claude Code 트랜스크립트가 메시지마다 `usage.speed` 를 싣는다
	 *   (실측 2026-08-13: 표본 1,994건 전부 "standard" — 즉 지금 오차는 0 이다).
	 *   그래서 이 축은 "관측 불가"가 아니라 **아직 배관이 없는 것**이다: 수집기·인테이크·
	 *   저장 컬럼이 채워지면 아래 계산이 그대로 동작한다.
	 *   0/부재가 기존 동작이며(전부 표준 속도) 결과는 비트 동일하다.
	 *
	 * ⚠ 고속 몫이 **롱 구간과 겹칠 때**는 표준 단가 기준으로 2배를 매긴다(아래 delta 항).
	 *   즉 `고속 + 272K 초과`가 동시인 요청은 그 겹친 부분만 소폭 과소가 된다. 혼합 비율을
	 *   추정해 곱하는 편이 이론상 정확하지만, 그건 근거 없는 가정을 비용 계산에 넣는 일이다 —
	 *   가정 대신 **경계를 문서로 남긴다**. (Opus 5 고속 + 272K 초과는 실측된 바 없다.)
	 */
	InputFast       float64
	OutputFast      float64
	CacheReadFast   float64
	CacheCreateFast float64
}

// LongPricing 은 롱 몫에 **무슨 단가를 적용했는지**다.
//
// flat 과 unknown 을 뭉치면 안 된다. 둘 다 표준가로 계산하지만 뜻이 정반대다 — 하나는
// "계단이 없으므로 이 값이 정확하다"이고, 다른 하나는 "계단이 있는지조차 모르므로 과소일 수
// 있다"이다. 뭉치면 화면이 후자를 전자로 위장한다.
type LongPricing string

const (
	// LongPricingNone 은 롱 몫이 없다는 뜻이다(분리분 0 또는 부재 = 기존 동작).
	LongPricingNone LongPricing = ""
	// LongPricingApplied 는 롱 몫을 롱 단가로 계산했다는 뜻이다.
	LongPricingApplied LongPricing = "applied"
	// LongPricingFlat 은 시드에 있는 모델인데 롱 단가 항목이 없다는 뜻이다
	// = **우리 표 기준 계단 없음**. 표준가로 계산했고 그 값이 정확하다.
	LongPricingFlat LongPricing = "flat"
	// LongPricingUnknown 은 시드 밖 모델(config 로 단가만 꽂힌 사내 게이트웨이 등)이라
	// 계단 여부 자체를 모른다는 뜻이다. 표준가로 계산했으므로 **과소일 수 있다.**
	LongPricingUnknown LongPricing = "unknown"
)

// Result 는 CostOf 와 Summarize 가 함께 쓰는 반환형이다(계약서가 둘 다 Result 로 적었다).
//
//	CostOf 만 채우는 것    Priced · TTLKnown · Model
//	Summarize 만 채우는 것 Unpriced · TTLUnknownRows · PricedAt
//
// Priced 가 이 타입의 핵심이다. 단가표에 없는 모델을 0 원으로 계산하면 비용이 화면에서
// **사라진다** — 사내 게이트웨이로 도는 서드파티 모델이 실제로 그 자리에 있다.
//
// TTLKnown 은 캐시 생성 비용의 신뢰도다. false 면 TTL 분해값이 없어 5분으로 가정했다는
// 뜻이고, 그 행의 캐시 생성 비용은 최대 1.6배까지 과소 추정일 수 있다.
type Result struct {
	USD    float64
	ByAxis Axis

	Priced   bool
	TTLKnown bool
	Model    string

	// Unpriced 는 단가 미등록 모델의 목록이다. 비어 있지 않으면 화면이 "이 모델들은 비용에서
	// 빠져 있다"고 말해야 한다 — 합계만 보여주면 누락이 보이지 않는다.
	Unpriced []string
	// TTLUnknownRows 는 TTL 을 몰라 5분으로 가정한 행 수다. 구버전 수집기를 쓰는 팀원이
	// 남아 있는 한 이 값은 0 이 아니다.
	TTLUnknownRows int
	PricedAt       string

	// ── 계단(롱컨텍스트) ──────────────────────────────────────────────────
	//
	// LongTokens 는 롱 구간으로 분리된 토큰 수다(입력 + 출력 + 캐시읽기 + 캐시쓰기). CostOf 는
	// 그 행의 값을, Summarize 는 합계를 담는다. 0 이면 계단이 개입하지 않았다는 뜻이다.
	LongTokens float64

	// ── 고속 모드 ─────────────────────────────────────────────────────────
	//
	// FastTokens 는 고속 단가로 계산된 토큰 수다(전 축 합). 0 이면 전부 표준 속도이며
	// 비용은 고속 모드가 없던 때와 비트 동일하다.
	//
	// 이 값을 내보내는 이유는 "왜 이 세션이 비싼가"를 화면이 답할 수 있어야 하기 때문이다 —
	// 같은 모델·같은 토큰인데 2배가 나오는 유일한 이유가 이 축이다.
	FastTokens float64
	// LongPricing 은 CostOf 만 채운다 — 한 행에 적용된 단가의 종류다.
	LongPricing LongPricing
	// LongUnknownRows 는 Summarize 만 채운다. **롱 몫이 있는데 롱 단가를 몰라 표준가로
	// 계산한 행 수**다(LongPricingUnknown). 0 이 아니면 그만큼 과소 추정일 수 있다 —
	// TTLUnknownRows 와 같은 규율로, 모르는 것을 모른다고 내보낸다.
	//
	// flat(계단 없는 모델)은 여기 세지 않는다. 그건 모르는 게 아니라 아는 사실이다.
	LongUnknownRows int
}

/*
 * 시드 단가 — USD / 1M 토큰. 출처: Anthropic 공개 가격표.
 *
 * **이 표는 낡는다.** config.json 의 `usage.pricing` 이 이기므로, 단가가 바뀌면 코드가 아니라
 * 설정을 고친다. 화면은 PricedAt 을 함께 표시해 이 표가 언제 것인지 보이게 한다.
 *
 * ⚠ introUntil·intro 는 **기간 한정 도입가** 장치다. 어느 쪽 단가를 시드에 박아도 한쪽
 *   기간에는 틀리기 때문에 날짜로 갈라야 한다.
 *
 *   지금 이 장치를 쓰는 모델은 **없다.** 유일한 사용자였던 Claude Sonnet 5 는 도입가
 *   $2/$10 이 그대로 표준가로 확정돼(2026-09-01 인상 취소) 평범한 항목이 됐다.
 *   장치는 남겨 둔다 — 다음 도입가가 오면 여기가 그 자리다.
 *
 *   ⚠ 도입가를 넣을 때는 **만료 후 단가에 근거가 있는지** 먼저 확인할 것. Sonnet 5 에서
 *     정확히 그 함정을 밟았다: 예고된 인상가(3/15)를 base 에 박아 뒀는데 인상이 취소되면서,
 *     코드를 아무도 건드리지 않아도 만료일에 1.5배 과대계상으로 넘어가는 상태가 됐다.
 *     예고는 사실이 아니다 — 시행된 뒤에 적는다.
 */
type seedEntry struct {
	provider   string
	price      Price
	introUntil string // "" 이면 도입가 없음
	intro      Price

	// cacheReadMult 는 **입력가 대비** 캐시 히트 배수다. 0 이면 전역 CacheReadMult(0.1) 를
	// 쓴다 — Anthropic 항목들이 이 경로로 기존 동작을 그대로 유지한다.
	//
	// 이 필드가 없으면 안 되는 이유: 배수는 공급사가 아니라 **모델**마다 다르다. 같은 OpenAI
	// 안에서도 gpt-5.x 는 0.1배, o3·gpt-4.1 은 0.25배, gpt-4o·o1 은 0.5배, *-pro 는 할인
	// 자체가 없다(1.0). 하나로 뭉치면 pro 모델 캐시읽기가 실제의 1/10 로 잡힌다.
	cacheReadMult float64

	// cacheWriteMult 는 캐시 **생성** 배수다. provider 가 anthropic 이 아닐 때만 본다.
	//   0  → 캐시 생성 무과금(OpenAI 대부분·Google 전부. 자동 캐싱이라 쓰기 비용이 없다)
	//   >0 → 그 배수로 과금(OpenAI GPT-5.6 계열의 1.25)
	//
	// Anthropic 은 이 필드를 쓰지 않는다. TTL(5분 1.25배 / 1시간 2배)로 갈리는 축이라
	// 단일 배수로 표현되지 않고, 기존 전역 상수 경로를 그대로 타야 무회귀이기 때문이다.
	cacheWriteMult float64

	// priceLong 은 임계값 초과 구간의 단가다(계단 요금). 제로값이면 **우리 표 기준 계단 없음**.
	//
	// 임계값은 공급사마다 다르다(Google 200K · OpenAI 272K). 그 판정은 여기서 하지 않는다 —
	// 이 패키지는 집계된 행을 받으므로 "이 요청이 임계값을 넘었는가"를 알 수 없다. 판정은 요청
	// 원문을 보는 **수집기**가 하고, 그 결과가 Usage 의 InputLong/OutputLong/CacheReadLong 이다.
	// 여기서는 그 몫에 이 단가를 곱하기만 한다(CostOf 참조).
	//
	// 분리분이 0/부재면 이 필드는 계산에 관여하지 않는다 — 기존 데이터·구버전 수집기의 결과가
	// 개편 전과 비트 동일한 근거다.
	priceLong Price
}

// 공급사 식별자. 캐시 생성 축의 계산 경로가 여기서 갈린다.
const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"
	ProviderGoogle    = "google"
)

var anthropicSeed = map[string]seedEntry{
	"claude-opus-5":   {provider: ProviderAnthropic, price: Price{5, 25}},
	"claude-opus-4-8": {provider: ProviderAnthropic, price: Price{5, 25}},
	"claude-opus-4-7": {provider: ProviderAnthropic, price: Price{5, 25}},
	"claude-opus-4-6": {provider: ProviderAnthropic, price: Price{5, 25}},
	"claude-opus-4-5": {provider: ProviderAnthropic, price: Price{5, 25}},
	/*
	 * claude-sonnet-5 — $2/$10 이 **표준가다**(인트로가 아니다).
	 *
	 * 처음에는 "2026-08-31 까지 인트로 $2/$10, 9월 1일부터 $3/$15" 로 넣었다. 그 예고가
	 * 취소됐다: Anthropic 이 인트로가를 그대로 표준가로 확정하고 9월 인상은 없다고 밝혔다
	 * (platform.claude.com/docs/en/about-claude/pricing 의 claude-sonnet-5-introductory-pricing
	 * 노트 — 2026-08-13 확인).
	 *
	 * ⚠ 그대로 두면 **2026-09-01 에 저절로 틀린다.** introUntil 이 지나면 base(3/15)로 올라가
	 *   실제 청구의 1.5배를 그린다. 게이트에도 안 걸린다 — 날짜가 바뀌기만 하면 되는 일이라
	 *   코드 변경이 없고, 골든은 인트로 기간에 캡처돼 있어 9월 1일에 조용히 갈린다.
	 *   (실사용 모델이다. 이 배포에서 출력 559,654 토큰이 이 모델로 잡혀 있다.)
	 */
	"claude-sonnet-5": {provider: ProviderAnthropic, price: Price{2, 10}},

	"claude-sonnet-4-6": {provider: ProviderAnthropic, price: Price{3, 15}},
	"claude-sonnet-4-5": {provider: ProviderAnthropic, price: Price{3, 15}},
	"claude-fable-5":    {provider: ProviderAnthropic, price: Price{10, 50}},
	"claude-mythos-5":   {provider: ProviderAnthropic, price: Price{10, 50}},
	"claude-haiku-4-5":  {provider: ProviderAnthropic, price: Price{1, 5}},

	/*
	 * ── 은퇴 모델 ────────────────────────────────────────────────────────
	 *
	 * 1st-party API 에서는 은퇴했지만 **Bedrock·Google Cloud 에서는 아직 돌아간다**
	 * (공식 단가 문서가 각 행에 "retired, except on Bedrock and Google Cloud" 로 명시하고
	 * 단가를 계속 싣고 있다). 2026-08-13 전수 감사에서 미등재를 잡았다.
	 *
	 * 등재하는 이유: 미등재로 두면 그 세션이 unpriced 로 빠져 **비용에서 사라진다.** 단가가
	 * 공개돼 있는데 비용을 0 으로 두는 것은 이 레포의 규율("틀린 숫자보다 없는 숫자가 낫다")이
	 * 말하는 상황이 아니다 — 그 규율은 단가를 **모를 때** 쓰는 것이고, 여기는 안다.
	 *
	 * ⚠ 이 모델들은 4.5 이전이라 데이터 레지던시(inference_geo)를 지원하지 않는다.
	 *   Bedrock·GCP 는 자체 지역 단가가 따로 있으므로, 그쪽으로 돌린 사용량의 실제 청구는
	 *   이 표와 다를 수 있다(공식 문서도 그 둘은 별도 페이지를 보라고 한다).
	 */
	"claude-opus-4-1":  {provider: ProviderAnthropic, price: Price{15, 75}},
	"claude-opus-4":    {provider: ProviderAnthropic, price: Price{15, 75}},
	"claude-sonnet-4":  {provider: ProviderAnthropic, price: Price{3, 15}},
	"claude-haiku-3-5": {provider: ProviderAnthropic, price: Price{0.8, 4}},
}

// seed 는 공급사별 표를 합친 것이다(openaiSeed·googleSeed 는 seed_openai.go·seed_google.go).
var seed = mergeSeeds(anthropicSeed, openaiSeed, googleSeed)

// mergeSeeds 는 키가 겹치면 **죽는다.** 조용한 덮어쓰기가 이 표에서 가장 위험한 사고이기
// 때문이다 — 예컨대 gemini-3-flash 를 두 표에 서로 다른 단가로 적어 넣으면 어느 쪽이 이겼는지
// 화면에 아무 흔적도 남지 않는다. 이 표는 전부 소스에 박힌 리터럴이라, 충돌하면 패키지 init
// 에서 즉시 터져 테스트가 빨개진다(사용자 데이터로는 절대 발생할 수 없다).
func mergeSeeds(tables ...map[string]seedEntry) map[string]seedEntry {
	out := make(map[string]seedEntry)
	for _, t := range tables {
		for k, v := range t {
			if _, dup := out[k]; dup {
				panic("cost: 시드 단가표에 중복 모델 키가 있다: " + k)
			}
			out[k] = v
		}
	}
	return out
}

// SeedPricedAt 은 공급자 공식 가격표와 대조·검증한 날짜다.
//
// 공급사마다 검증일이 다르다 — 한 날짜로 뭉뚱그리면 화면이 "2026-08-10 기준"이라고 말하면서
// 실제로는 08-04 에 확인한 Anthropic 값을 보여주게 된다. SeedPricedAt 은 기존 호출부(PricedAt)
// 하위호환을 위해 Anthropic 값을 유지하고, 공급사별 값은 SeedPricedAtFor 로 읽는다.
const (
	SeedPricedAtAnthropic = "2026-08-04" // platform.claude.com/docs/ko/about-claude/pricing
	SeedPricedAtOpenAI    = "2026-08-10" // developers.openai.com/api/docs/pricing
	SeedPricedAtGoogle    = "2026-08-05" // ai.google.dev/gemini-api/docs/pricing

	SeedPricedAt = SeedPricedAtAnthropic
)

// SeedPricedAtFor 는 공급사별 단가 검증일을 돌려준다. 모르는 공급사면 "" 다
// (화면이 "기준일 미상"으로 표시할 수 있게 — 아무 날짜나 지어내지 않는다).
func SeedPricedAtFor(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case ProviderAnthropic:
		return SeedPricedAtAnthropic
	case ProviderOpenAI:
		return SeedPricedAtOpenAI
	case ProviderGoogle:
		return SeedPricedAtGoogle
	default:
		return ""
	}
}

// ProviderOf 는 시드에 등록된 모델의 공급사를 돌려준다. 미등록이면 ("", false) —
// 이름만 보고 추측하지 않는다(`gpt-` 로 시작한다고 OpenAI 라는 보장이 없다. 사내 게이트웨이가
// 임의 별칭을 붙여 보내는 경우가 실제로 있다).
func ProviderOf(model string) (string, bool) {
	e, ok := seed[NormalizeModel(model)]
	if !ok {
		return "", false
	}
	return e.provider, true
}

// LongContextPrice 는 계단 초과 구간 단가와 "이 모델이 계단 요금인가"를 돌려준다.
//
// CostOf 가 롱 분리분(Usage.InputLong 등)에 이 단가를 적용한다. false 는 **우리 표 기준
// 계단 없음**이라는 뜻이고, 시드에 아예 없는 모델도 false 다 — 그 둘의 구분은 Result 의
// LongPricing(flat vs unknown)이 한다.
func LongContextPrice(model string) (Price, bool) {
	e, ok := seed[NormalizeModel(model)]
	if !ok || e.priceLong == (Price{}) {
		return Price{}, false
	}
	return e.priceLong, true
}

/*
 * 캐시 축의 입력가 대비 배수.
 *
 * 캐시 읽기는 입력가의 약 0.1배, 캐시 생성은 **5분 TTL 이면 1.25배·1시간 TTL 이면 2배**다.
 * 배수가 1.6배 차이 나므로 TTL 을 뭉뚱그리면 비용이 그만큼 틀린다.
 *
 * ── TTL 은 추정하지 않는다. 트랜스크립트에 적혀 있다 ─────────────────────
 * 실측 표본에서 assistant 턴의 usage 블록에 cache_creation 분해값이 100%(9,345/9,345)
 * 들어 있었고, 캐시 생성 46.2 MTok 의 **100% 가 1시간 TTL** 이었다. 5분(1.25배)으로 계산하던
 * 값은 실제의 1/1.60 이었다.
 *
 * 그래서 규칙은 **분해값 우선, 없으면 폴백**이다:
 *   ① CacheCreate5m / CacheCreate1h 의 합이 0 보다 크면 각각의 배수로 계산한다.
 *   ② 둘 다 없으면(= 구버전 수집기가 보낸 행) 총량에 5분 배수를 적용하고 TTLKnown=false 로
 *      표시한다. 과거 행에 TTL 을 소급해 지어내지 않는다 — 모르는 것은 모른다고 내보낸다.
 */
const (
	CacheReadMult     = 0.1
	CacheCreateMult   = 1.25 // 5분 TTL
	CacheCreate1hMult = 2    // 1시간 TTL

	/*
	 * FastMult 는 고속 모드 배수다 — 전 축(입력·출력·캐시)에 같이 걸린다.
	 *
	 * 두 공급사가 같은 2배다:
	 *   Anthropic  Opus 5 / Opus 4.8 고속 $10/$50 = 표준 $5/$25 × 2. 캐시 배수는 그 위에 얹힌다.
	 *   OpenAI     공식 단가 표 각주 "Fast mode pricing is doubled."
	 * 갈리는 날이 오면 seedEntry 로 내린다(cacheReadMult 가 그렇게 내려간 전례가 있다).
	 *
	 * config 오버라이드를 두지 않는다 — 이건 협상 대상 배수가 아니라 공표된 단가 구조다.
	 */
	FastMult = 2.0

	multMin = 0
	multMax = 10
)

const mtok = 1e6

// Seed 는 도입가를 적용하지 않은 시드 단가표의 사본이다(현행 JS 의 `cost.SEED` 자리).
func Seed() Table {
	t := make(Table, len(seed))
	for m, e := range seed {
		t[m] = e.price
	}
	return t
}

// NormalizeModel 은 표 조회 전에 모델명의 꼬리를 떼어낸다.
//
//	'claude-opus-5[1m]'                     → 'claude-opus-5'          (컨텍스트 변형 접미사)
//	'claude-opus-4-5-20251101'              → 'claude-opus-4-5'        (Anthropic 날짜 스냅샷)
//	'gpt-5.5-2026-04-23'                    → 'gpt-5.5'                (OpenAI 날짜 스냅샷)
//	'gemini-2.5-flash-lite-preview-09-2025' → 'gemini-2.5-flash-lite'  (Gemini 프리뷰 스냅샷)
//	'anthropic.claude-opus-5'               → 'claude-opus-5'          (Bedrock 접두사)
//	'gemini-3.6-flash-medium'               → 'gemini-3.6-flash'       (변형 접미사)
//	'claude-opus-4-6-thinking'              → 'claude-opus-4-6'        (변형 접미사)
//
// **'-latest' 는 자르지 않는다.** 'chat-latest' · 'gpt-5.3-chat-latest' 는 스냅샷 별칭이 아니라
// 그 자체가 정식 과금 ID다. 자르면 표에 없는 이름이 되어 비용이 통째로 빠진다.
//
// 조회는 **정확 일치**로만 한다. 접두사 매칭을 넣고 싶어지지만 그러면 'gemini-3-flash' 가
// 'gemini-3-flash-preview' 에 붙어 3배 틀린 단가가 조용히 적용된다(seed_google.go 참조).
//
// 이 정규화가 실패해도 문제되지 않는다 — 표에 없으면 Priced=false 로 정직하게 나간다.
func NormalizeModel(model string) string {
	s := strings.ToLower(strings.TrimSpace(model))
	if s == "" {
		return ""
	}
	s = stripBracketSuffix(s) // [1m] 류 접미사
	s = stripDateSnapshot(s)  // -20251101 류 날짜 스냅샷
	s = strings.TrimPrefix(s, "anthropic.")
	s = strings.TrimSpace(s)
	return stripVariantSuffix(s) // -medium · -thinking 류 변형 접미사
}

/*
 * 변형 접미사 — 같은 과금 ID의 **동작 변형**을 가리키는 꼬리다. 단가는 원본과 같다.
 *
 * 실측: Google Antigravity CLI(`agy models`)가 보고하는 ID 는 API 과금 ID 에 추론 강도
 * (-high/-medium/-low) 또는 사고 모드(-thinking)를 덧붙인 형태다:
 *
 *	gemini-3.6-flash-medium    → gemini-3.6-flash
 *	claude-opus-4-6-thinking   → claude-opus-4-6
 *
 * 수집기는 이 원문을 **그대로** 보낸다(무엇이 실제로 보고됐는지 서버가 알 수 있어야 한다).
 * 벗기는 일은 여기서만 한다. 벗기지 않으면 이 ID 들이 전부 unpriced 로 빠진다.
 *
 * ⚠ **정확히 이 넷만 벗긴다.** 목록을 넓히고 싶어지지만 '-lite'(gemini-3.5-flash-lite)·
 *   '-pro'·'-preview'·'-nano' 는 정식 과금 ID의 일부라 자르면 다른 모델 단가가 붙는다.
 *   variant_suffix_test.go 가 시드 전체를 훑어 이 넷으로 끝나는 키가 없음을 못 박는다 —
 *   그런 모델이 등록되는 날 즉시 빨개진다.
 */
var variantSuffixes = []string{"-thinking", "-high", "-medium", "-low"}

// stripVariantSuffix 는 변형 접미사를 **한 겹만** 벗긴다.
//
// 한 겹인 이유: 반복해서 벗기면 'foo-low-medium' 이 'foo' 가 되어, 존재하지도 않는 매칭이
// 생긴다. 실제 ID 는 접미사가 하나뿐이므로 두 겹은 이름이 이상하다는 신호이고, 이상한 이름은
// unpriced 로 나가는 편이 옳다.
//
// **원문 우선** — 벗기기 전에 원문이 시드에 있으면 그것을 쓴다. 지금은 해당 키가 없지만,
// 미래에 '...-thinking' 이 별도 단가의 정식 과금 ID로 등록되면 이 분기가 그 모델을 지킨다.
func stripVariantSuffix(s string) string { return stripVariantSuffixIn(s, seed) }

// stripVariantSuffixIn 은 "알려진 이름" 집합을 주입받는다 — 테스트가 원문 우선 분기를
// 실제 시드에 가짜 모델을 심지 않고 검증할 수 있게 한다.
func stripVariantSuffixIn(s string, known map[string]seedEntry) string {
	if _, ok := known[s]; ok {
		return s // 원문이 곧 과금 ID다 — 건드리지 않는다.
	}
	for _, suf := range variantSuffixes {
		// len 이 같으면 접미사만 온 것이다. 잘라 봐야 빈 문자열이라 조회가 불가능해진다.
		if len(s) > len(suf) && strings.HasSuffix(s, suf) {
			return s[:len(s)-len(suf)]
		}
	}
	return s
}

// /\[[^\]]*\]$/ 와 같다.
func stripBracketSuffix(s string) string {
	if !strings.HasSuffix(s, "]") {
		return s
	}
	open := strings.LastIndexByte(s, '[')
	if open < 0 {
		return s
	}
	// [ 와 ] 사이에 ] 가 없어야 한다 — LastIndex 로 찾았으니 자동으로 만족한다.
	return s[:open]
}

// 모델명 꼬리에 붙는 날짜 스냅샷 표기. 공급사마다 모양이 다르다.
//
//	/-\d{8}$/               Anthropic  claude-opus-4-5-20251101
//	/-\d{4}-\d{2}-\d{2}$/   OpenAI     gpt-5.5-2026-04-23
//	/-preview-\d{2}-\d{4}$/ Gemini     gemini-2.5-flash-lite-preview-09-2025
//
// 'd' 는 숫자 한 자리, 나머지 문자는 그 문자 자신이다(suffixMatches 참조).
// regexp 를 쓰지 않는 이유: 패턴이 셋뿐이고 전부 고정 길이라 손으로 읽는 편이 빠르다.
const (
	snapAnthropic = "-dddddddd"
	snapOpenAI    = "-dddd-dd-dd"
	snapGemini    = "-preview-dd-dddd"
)

// stripDateSnapshot 은 날짜 스냅샷 꼬리를 **한 번만** 떼어낸다.
//
// 긴 패턴부터 본다. 세 패턴은 실제로 서로 겹치지 않지만(프리뷰 꼬리는 '-' 자리에 글자가 와서
// 다른 둘과 매치되지 않는다), 나중에 패턴이 늘었을 때 짧은 쪽이 먼저 먹고 이름을 반쪽만
// 남기는 사고를 구조적으로 막는다.
func stripDateSnapshot(s string) string {
	// '-latest' 는 스냅샷이 아니라 정식 과금 ID의 일부다. 지금의 세 패턴 중 어느 것도 여기에
	// 매치되지 않지만, 패턴이 늘어나면 그 보장이 사라지므로 입구에서 못 박아 둔다.
	if strings.HasSuffix(s, "-latest") {
		return s
	}
	for _, pat := range []string{snapGemini, snapOpenAI, snapAnthropic} {
		if suffixMatches(s, pat) {
			return s[:len(s)-len(pat)]
		}
	}
	return s
}

// suffixMatches 는 s 의 꼬리가 pat 모양인지 본다. pat 의 'd' 는 숫자 한 자리를 뜻한다.
func suffixMatches(s, pat string) bool {
	if len(s) < len(pat) {
		return false
	}
	tail := s[len(s)-len(pat):]
	for i := 0; i < len(pat); i++ {
		if pat[i] == 'd' {
			if tail[i] < '0' || tail[i] > '9' {
				return false
			}
			continue
		}
		if tail[i] != pat[i] {
			return false
		}
	}
	return true
}

// Pricing 은 시드에 config 오버라이드를 얹은 단가표를 만든다.
// 깨진 항목은 무시하고 시드를 유지한다(부분 오버라이드 허용).
//
// today 는 기간 한정 도입가를 어느 날짜 기준으로 볼지 정한다. 제로값이면 오늘(UTC)이다 —
// **테스트가 날짜를 고정할 수 있어야** 하므로 인자로 받는다. 안 그러면 2026-09-01 에
// 테스트가 저절로 빨개진다.
func Pricing(today time.Time) Table {
	if today.IsZero() {
		today = time.Now()
	}
	day := today.UTC().Format("2006-01-02")

	table := make(Table, len(seed))
	for model, e := range seed {
		// 도입가는 만료일**까지 포함**이다(공식 표기 "2026년 8월 31일까지").
		if e.introUntil != "" && day <= e.introUntil {
			table[model] = e.intro
		} else {
			table[model] = e.price
		}
	}

	over, ok := readBlock()["pricing"].(map[string]any)
	if !ok {
		return table
	}
	for model, raw := range over {
		v, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		in, okIn := rate(v["input"])
		out, okOut := rate(v["output"])
		if !okIn || !okOut {
			continue // 한쪽만 온 항목은 통째로 무시
		}
		table[NormalizeModel(model)] = Price{Input: in, Output: out}
	}
	return table
}

// PricedAt 은 이 단가표가 언제 것인지 말한다. 화면이 그대로 표시해 낡은 값이 보이게 한다.
func PricedAt() string {
	if v, ok := readBlock()["pricedAt"].(string); ok {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return SeedPricedAt
}

// Multipliers 는 캐시 축 배수를 config 오버라이드와 함께 읽는다.
func Multipliers() Mult {
	b := readBlock()
	return Mult{
		CacheRead:     clampMult(b["cacheReadMult"], CacheReadMult),
		CacheCreate:   clampMult(b["cacheCreateMult"], CacheCreateMult),
		CacheCreate1h: clampMult(b["cacheCreate1hMult"], CacheCreate1hMult),
	}
}

// modelMult 는 전역 배수(base) 위에 **모델별 배수**를 얹은 복사본을 돌려준다.
//
// 규칙:
//   - cacheReadMult 가 0 이면 base.CacheRead 를 그대로 쓴다. Anthropic 시드가 전부 0 이므로
//     기존 동작(전역 0.1 + config 오버라이드)이 비트 단위로 보존된다.
//   - 캐시 **생성**은 공급사로 갈린다.
//     Anthropic(및 공급사 미상): TTL 로 갈리는 축이라 base 의 5분/1시간 배수를 그대로 쓴다.
//     OpenAI·Google: cacheWriteMult 하나로 갈음한다. 0 이면 캐시 생성 무과금이다.
//
// 모델별 값이 config 의 전역 오버라이드보다 **우선**한다. 전역 오버라이드는 "Anthropic 기준
// 배수를 조정한다"는 뜻으로 들어온 값이라, 그걸 o3(0.25배)나 *-pro(1.0배)에 덮어씌우면
// 이번 개편이 고치려던 바로 그 오류(공급사 기준이 뒤섞여 조용히 틀리는 것)로 되돌아간다.
// 모델별로 배수를 조정해야 할 일이 생기면 그때 config 스키마에 모델별 칸을 판다.
func modelMult(model string, base Mult) Mult {
	e, ok := seed[model]
	if !ok {
		// 시드에 없는 모델 — 대개는 priced=false 로 나가지만, config 의 `usage.pricing` 이
		// 단가를 직접 꽂아 넣은 사내 게이트웨이 모델일 수도 있다. 그 경우 배수를 알 길이
		// 없으므로 전역 기준선을 쓴다(현행 동작과 같다).
		return base
	}
	m := base
	if e.cacheReadMult > 0 {
		m.CacheRead = clampRange(e.cacheReadMult)
	}
	// 공급사는 **허용 목록**으로 판정한다. 값이 비었거나 모르는 공급사면 캐시 생성 배수를
	// 건드리지 않는다 — 여기서 잘못 0 을 넣으면 비용이 조용히 사라지는 쪽으로 틀린다.
	switch e.provider {
	case ProviderOpenAI, ProviderGoogle:
		w := clampRange(e.cacheWriteMult)
		m.CacheCreate, m.CacheCreate1h = w, w
	}
	return m
}

// CostOf 는 한 행의 비용을 낸다.
//
// table 이 nil 이면 그 자리에서 Pricing(오늘)을 읽는다. mult 가 제로값이면 Multipliers() 를
// 읽는다 — 세 배수가 **동시에** 0 인 설정은 "캐시가 전부 공짜"라는 뜻이라 실재하는 의도가
// 아니고, 이 모듈의 규율(0 으로 클램프해 비용을 사라지게 하지 않는다)과도 어긋나기 때문에
// "안 넘겼다"로 읽는 편이 안전하다.
//
// mult 는 **전역 기준선**이다. 시드에 모델별 배수가 적혀 있으면 그 모델에 한해 기준선을
// 덮어쓴다(modelMult 참조). 넘긴 값을 변형하지는 않는다 — 복사본에만 얹는다.
func CostOf(row Usage, table Table, mult Mult) Result {
	if table == nil {
		table = Pricing(time.Time{})
	}
	if mult == (Mult{}) {
		mult = Multipliers()
	}

	model := NormalizeModel(row.Model)
	p, priced := table[model]
	mult = modelMult(model, mult)

	// TTL 분해값이 있으면 그것이 진실이다. 없을 때만 총량 + 5분 가정으로 내려간다.
	cc5 := tok(row.CacheCreate5m)
	cc1h := tok(row.CacheCreate1h)
	ttlKnown := cc5+cc1h > 0

	/*
	 * 계단(롱컨텍스트) 분리 — 총량에서 롱 몫을 떼어 낸다.
	 *
	 * 접는 이유(longShare): 롱 몫이 총량을 넘으면 표준 몫이 **음수**가 되고, 음수 토큰은 비용을
	 * 깎아 합계를 조용히 줄인다. 인테이크가 이미 접고 위반을 세지만(그쪽이 신뢰 경계다), 이미
	 * 저장된 옛 행이 그럴 수 있으므로 계산부에서도 구조적으로 막는다.
	 */
	inLong := longShare(row.InputLong, row.Input)
	outLong := longShare(row.OutputLong, row.Output)
	crLong := longShare(row.CacheReadLong, row.CacheRead)
	ccLong := longShare(row.CacheCreateLong, row.CacheCreate)
	/*
	 * ccLong 도 여기 합산한다. longPriceOf 가 이 합으로 "롱 단가를 적용할지"를 정하므로,
	 * 빼놓으면 **캐시 쓰기만 롱인 행**에서 롱 단가가 발동하지 않고 차액이 0 이 된다
	 * (누락을 고치려고 필드를 넣었는데 그 필드만으로는 동작하지 않는 상태가 된다).
	 */
	longTokens := inLong + outLong + crLong + ccLong

	if !priced {
		return Result{
			Priced: false, TTLKnown: ttlKnown, Model: model,
			LongTokens: longTokens,
		}
	}

	// 롱 몫이 0 이면 pLong 은 p 와 같고, 아래 식의 롱 항은 0 곱셈이라 결과에 관여하지 않는다
	// (부동소수 비트까지 개편 전과 같다 — `x + 0*y` 는 `x` 다).
	pLong, longPricing := longPriceOf(model, p, longTokens)

	var cacheCreateUSD float64
	if ttlKnown {
		cacheCreateUSD = ((cc5 * mult.CacheCreate) + (cc1h * mult.CacheCreate1h)) * p.Input / mtok
	} else {
		cacheCreateUSD = (tok(row.CacheCreate) * p.Input * mult.CacheCreate) / mtok
	}
	/*
	 * 롱 구간 캐시 쓰기의 **차액**만 더한다.
	 *
	 * 차액으로 더하는 이유는 무회귀다: ccLong 이 0 이면 이 항이 통째로 0 이라 위에서 낸 값의
	 * 비트가 그대로 남는다(regression_test.go 가 그것을 요구한다). 롱 몫을 본식에 끼워 넣으면
	 * 곱셈 순서가 바뀌어 롱이 없는 기존 행의 마지막 비트가 흔들린다.
	 *
	 * 배수는 mult.CacheCreate 를 쓴다. 이 필드가 값을 갖는 것은 OpenAI 5.6 계열뿐이고
	 * (위 CacheCreateLong 주석), 그쪽은 modelMult 가 CacheCreate 와 CacheCreate1h 에 같은
	 * 값(1.25)을 넣으므로 TTL 어느 쪽을 골라도 결과가 같다 — 가정이 아니라 항등이다.
	 */
	if ccLong > 0 {
		cacheCreateUSD += ccLong * (pLong.Input - p.Input) * mult.CacheCreate / mtok
	}

	byAxis := Axis{
		Input:  ((tok(row.Input)-inLong)*p.Input + inLong*pLong.Input) / mtok,
		Output: ((tok(row.Output)-outLong)*p.Output + outLong*pLong.Output) / mtok,
		/*
		 * 캐시 읽기의 롱 단가는 **롱 입력가 × cacheReadMult** 다. 공식 표가 그렇게 되어 있다:
		 * gemini-2.5-pro 의 캐시 히트가 0.125 → 0.25 로 오르는 것은 입력가 1.25 → 2.50 과
		 * 같은 0.1 배수다. 별도의 롱 캐시 단가를 표에 또 두면 두 값이 갈라질 자리가 생긴다.
		 *
		 * 곱셈 순서를 바꾸지 않는다 — `((x*p.Input) * mult.CacheRead) / mtok` 그대로여야
		 * 롱 몫이 0 일 때 개편 전과 비트가 같다.
		 */
		CacheRead:   (((tok(row.CacheRead)-crLong)*p.Input + crLong*pLong.Input) * mult.CacheRead) / mtok,
		CacheCreate: cacheCreateUSD,
	}

	/*
	 * ── 고속 모드 차액 ────────────────────────────────────────────────────
	 *
	 * 고속 단가는 표준의 FastMult 배다. 총량에 이미 1배가 들어가 있으므로 여기서는
	 * **(FastMult − 1)배만** 더한다. 고속 몫이 0 이면 fastTokens 가 0 이라 이 블록을 아예
	 * 건너뛴다 — 기존 행의 비트가 그대로 남는 근거다.
	 *
	 * 캐시 축에도 걸린다. 고속 모드는 **기준 입력가**를 올리는 것이고 캐시 배수는 그 위에
	 * 얹히기 때문이다(공식 문구). 캐시만 빼면 고속 세션의 캐시 비용이 절반으로 잡힌다.
	 *
	 * 차액에 표준가(p)를 쓰는 이유와 그 경계는 Usage.InputFast 주석에 적었다.
	 */
	inFast := longShare(row.InputFast, row.Input)
	outFast := longShare(row.OutputFast, row.Output)
	crFast := longShare(row.CacheReadFast, row.CacheRead)
	ccFast := longShare(row.CacheCreateFast, row.CacheCreate)
	fastTokens := inFast + outFast + crFast + ccFast
	if fastTokens > 0 {
		const extra = FastMult - 1
		byAxis.Input += inFast * p.Input * extra / mtok
		byAxis.Output += outFast * p.Output * extra / mtok
		byAxis.CacheRead += crFast * p.Input * mult.CacheRead * extra / mtok
		byAxis.CacheCreate += ccFast * p.Input * mult.CacheCreate * extra / mtok
	}

	return Result{
		USD:         byAxis.Input + byAxis.Output + byAxis.CacheRead + byAxis.CacheCreate,
		ByAxis:      byAxis,
		Priced:      true,
		TTLKnown:    ttlKnown,
		Model:       model,
		LongTokens:  longTokens,
		LongPricing: longPricing,
		FastTokens:  fastTokens,
	}
}

// longShare 는 롱 몫을 [0, 총량] 으로 접는다. 음수·NaN 은 0 이다(tok 과 같은 규율).
func longShare(long, total float64) float64 {
	l, t := tok(long), tok(total)
	if l > t {
		return t
	}
	return l
}

/*
 * longPriceOf 는 롱 몫에 쓸 단가와 "무슨 근거로 그 단가인지"를 함께 돌려준다.
 *
 * 근거를 함께 내보내는 것이 이 함수의 요점이다. 롱 단가가 없을 때 표준가로 계산하는 것은
 * 같지만, 그 이유가 "계단이 없는 모델"인지 "우리가 단가를 모르는 모델"인지는 전혀 다른
 * 사실이다 — 뭉치면 후자가 정확한 값으로 위장된다.
 *
 * ⚠ 롱 단가는 **시드에서만** 온다. config 의 `usage.pricing` 오버라이드는 표준 단가만 바꾸므로,
 *   오버라이드된 모델의 롱 단가는 시드 값 그대로 남는다(그 모델이 시드에 있는 경우). 롱 단가
 *   오버라이드가 필요해지면 그때 config 스키마에 칸을 판다 — 지금 없는 요구에 스키마를 늘리면
 *   양쪽이 갈라질 자리만 만든다.
 */
func longPriceOf(model string, p Price, longTokens float64) (Price, LongPricing) {
	if longTokens <= 0 {
		return p, LongPricingNone
	}
	e, inSeed := seed[model]
	switch {
	case inSeed && e.priceLong != (Price{}):
		return e.priceLong, LongPricingApplied
	case inSeed:
		// 시드에 있고 롱 단가 항목이 없다 = 계단 없는 모델. 표준가가 맞는 값이다.
		return p, LongPricingFlat
	default:
		// 시드 밖 모델(config 로 단가만 꽂힌 사내 게이트웨이 등) — 계단 여부를 모른다.
		// 표준가로 계산하되 그 사실을 결과에 남긴다(과소 추정일 수 있다).
		return p, LongPricingUnknown
	}
}

// Summarize 는 여러 행의 합계를 낸다. 단가표·배수를 **한 번만** 읽고 돌린다
// (행마다 config.json 을 다시 읽지 않게).
func Summarize(rows []Usage) Result {
	table := Pricing(time.Time{})
	mult := Multipliers()

	var byAxis Axis
	var usd float64
	var longTokens float64
	ttlUnknownRows := 0
	longUnknownRows := 0
	unpricedSet := map[string]struct{}{}

	for _, r := range rows {
		c := CostOf(r, table, mult)
		if !c.Priced {
			if c.Model != "" {
				unpricedSet[c.Model] = struct{}{}
			}
			continue
		}
		if !c.TTLKnown && tok(r.CacheCreate) > 0 {
			ttlUnknownRows++
		}
		// 롱 몫이 있는데 롱 단가를 몰라 표준가로 계산한 행. flat(계단 없는 모델)은 세지 않는다
		// — 그건 모르는 게 아니라 아는 사실이고, 세면 경고가 무뎌진다.
		if c.LongPricing == LongPricingUnknown {
			longUnknownRows++
		}
		longTokens += c.LongTokens
		usd += c.USD
		byAxis.Input += c.ByAxis.Input
		byAxis.Output += c.ByAxis.Output
		byAxis.CacheRead += c.ByAxis.CacheRead
		byAxis.CacheCreate += c.ByAxis.CacheCreate
	}

	// nil 이 아니라 빈 슬라이스로 — JSON 에서 null 이 되면 화면 분기가 갈린다.
	unpriced := make([]string, 0, len(unpricedSet))
	for m := range unpricedSet {
		unpriced = append(unpriced, m)
	}
	sort.Strings(unpriced)

	return Result{
		USD:             usd,
		ByAxis:          byAxis,
		Unpriced:        unpriced,
		TTLUnknownRows:  ttlUnknownRows,
		PricedAt:        PricedAt(),
		LongTokens:      longTokens,
		LongUnknownRows: longUnknownRows,
	}
}

// ── 설정 읽기 ────────────────────────────────────────────────────────────────

// readBlock 은 config.json 의 `usage` 블록을 돌려준다. 어떤 실패도 밖으로 내지 않는다 —
// 파일이 없거나 JSON 이 깨졌거나 usage 가 객체가 아니면 빈 맵이다(시드로 수렴).
//
// 경로는 USAGE_CONFIG > 작업 디렉터리의 config.json 이다. 현행 JS 는 모듈 파일 위치를 기준
// 삼지만(`__dirname/..`), 컴파일된 바이너리에는 그 기준이 없다. 배포에서는 USAGE_CONFIG 를
// 명시하는 편이 안전하다.
func readBlock() map[string]any {
	p := strings.TrimSpace(os.Getenv("USAGE_CONFIG"))
	if p == "" {
		p = "config.json"
	} else if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}

	raw, err := os.ReadFile(p)
	if err != nil {
		return map[string]any{}
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return map[string]any{}
	}
	// 배열·null 은 객체가 아니다 — Unmarshal 이 이미 걸러 주지만 usage 쪽은 직접 본다.
	if u, ok := root["usage"].(map[string]any); ok {
		return u
	}
	return map[string]any{}
}

// rate 는 0 이상 유한수만 인정한다. 그 외는 "값 없음"이다(설정 오타가 비용을 0 으로 만들지 않게).
//
// 배열·맵은 받지 않는다. JS 의 Number([]) 는 0 이지만, 그 0 을 단가로 받아들이면 오타 하나가
// 그 모델의 비용을 통째로 사라지게 한다 — 여기서는 버리는 쪽이 맞다.
func rate(v any) (float64, bool) {
	switch t := v.(type) {
	case nil:
		return 0, false
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) || t < 0 {
			return 0, false
		}
		return t, true
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0, false
		}
		n, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

// clampMult 는 배수를 **2단으로** 방어한다 — 두 오류가 성격이 다르기 때문이다.
//
//	무효값(음수·비수치·빈값) → 기본값. 배수로 성립하지 않는 입력이라 살릴 의도가 없다.
//	  여기서 0 으로 클램프하면 "캐시읽기가 공짜"가 되어 비용이 조용히 사라진다.
//	범위 초과(예: 9999)      → 상한으로 클램프. 의도("아주 높게")는 살리되 폭발 반경만 자른다.
func clampMult(v any, dflt float64) float64 {
	n, ok := rate(v)
	if !ok {
		n = dflt
	}
	return clampRange(n)
}

// clampRange 는 배수를 [multMin, multMax] 로 자른다. 시드에 박힌 모델별 배수도 이 문을 지난다
// — 지금은 전부 범위 안이지만, 오타 하나가 폭발 반경을 키우지 않게 같은 문을 쓴다.
func clampRange(n float64) float64 {
	return math.Max(multMin, math.Min(multMax, n))
}

// tok 은 유한하고 0 보다 큰 값만 인정한다. 음수·NaN 은 0 이다.
func tok(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
		return 0
	}
	return v
}
