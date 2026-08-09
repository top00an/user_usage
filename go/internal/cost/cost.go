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
}

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
}

/*
 * 시드 단가 — USD / 1M 토큰. 출처: Anthropic 공개 가격표.
 *
 * **이 표는 낡는다.** config.json 의 `usage.pricing` 이 이기므로, 단가가 바뀌면 코드가 아니라
 * 설정을 고친다. 화면은 PricedAt 을 함께 표시해 이 표가 언제 것인지 보이게 한다.
 *
 * ⚠ Claude Sonnet 5 는 **기간 한정 도입가**($2/$10, 2026-08-31 까지)가 있다. intro 로 적어
 *   날짜에 따라 자동으로 갈린다 — 어느 쪽을 시드에 박아도 한쪽 기간에는 틀리기 때문이다.
 *   정가를 박으면 도입 기간 중 1.5배 과대, 도입가를 박으면 9/1 부터 33% 과소가 된다.
 *   기간이 끝나면 intro 를 지우기만 하면 된다(지우지 않아도 만료일이 지나면 정가를 쓴다).
 */
type seedEntry struct {
	price      Price
	introUntil string // "" 이면 도입가 없음
	intro      Price
}

var seed = map[string]seedEntry{
	"claude-opus-5":   {price: Price{5, 25}},
	"claude-opus-4-8": {price: Price{5, 25}},
	"claude-opus-4-7": {price: Price{5, 25}},
	"claude-opus-4-6": {price: Price{5, 25}},
	"claude-opus-4-5": {price: Price{5, 25}},
	"claude-sonnet-5": {price: Price{3, 15}, introUntil: "2026-08-31", intro: Price{2, 10}},

	"claude-sonnet-4-6": {price: Price{3, 15}},
	"claude-sonnet-4-5": {price: Price{3, 15}},
	"claude-fable-5":    {price: Price{10, 50}},
	"claude-mythos-5":   {price: Price{10, 50}},
	"claude-haiku-4-5":  {price: Price{1, 5}},
}

// SeedPricedAt 은 공급자 공식 가격표와 대조·검증한 날짜다.
const SeedPricedAt = "2026-08-04"

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
//	'claude-opus-5[1m]'        → 'claude-opus-5'   (컨텍스트 변형 접미사)
//	'claude-opus-4-5-20251101' → 'claude-opus-4-5' (날짜 스냅샷)
//	'anthropic.claude-opus-5'  → 'claude-opus-5'   (Bedrock 접두사)
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
	return strings.TrimSpace(s)
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

// /-\d{8}$/ 와 같다.
func stripDateSnapshot(s string) string {
	if len(s) < 9 {
		return s
	}
	tail := s[len(s)-9:]
	if tail[0] != '-' {
		return s
	}
	for i := 1; i < len(tail); i++ {
		if tail[i] < '0' || tail[i] > '9' {
			return s
		}
	}
	return s[:len(s)-9]
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

// CostOf 는 한 행의 비용을 낸다.
//
// table 이 nil 이면 그 자리에서 Pricing(오늘)을 읽는다. mult 가 제로값이면 Multipliers() 를
// 읽는다 — 세 배수가 **동시에** 0 인 설정은 "캐시가 전부 공짜"라는 뜻이라 실재하는 의도가
// 아니고, 이 모듈의 규율(0 으로 클램프해 비용을 사라지게 하지 않는다)과도 어긋나기 때문에
// "안 넘겼다"로 읽는 편이 안전하다.
func CostOf(row Usage, table Table, mult Mult) Result {
	if table == nil {
		table = Pricing(time.Time{})
	}
	if mult == (Mult{}) {
		mult = Multipliers()
	}

	model := NormalizeModel(row.Model)
	p, priced := table[model]

	// TTL 분해값이 있으면 그것이 진실이다. 없을 때만 총량 + 5분 가정으로 내려간다.
	cc5 := tok(row.CacheCreate5m)
	cc1h := tok(row.CacheCreate1h)
	ttlKnown := cc5+cc1h > 0

	if !priced {
		return Result{Priced: false, TTLKnown: ttlKnown, Model: model}
	}

	var cacheCreateUSD float64
	if ttlKnown {
		cacheCreateUSD = ((cc5 * mult.CacheCreate) + (cc1h * mult.CacheCreate1h)) * p.Input / mtok
	} else {
		cacheCreateUSD = (tok(row.CacheCreate) * p.Input * mult.CacheCreate) / mtok
	}

	byAxis := Axis{
		Input:       (tok(row.Input) * p.Input) / mtok,
		Output:      (tok(row.Output) * p.Output) / mtok,
		CacheRead:   (tok(row.CacheRead) * p.Input * mult.CacheRead) / mtok,
		CacheCreate: cacheCreateUSD,
	}
	return Result{
		USD:      byAxis.Input + byAxis.Output + byAxis.CacheRead + byAxis.CacheCreate,
		ByAxis:   byAxis,
		Priced:   true,
		TTLKnown: ttlKnown,
		Model:    model,
	}
}

// Summarize 는 여러 행의 합계를 낸다. 단가표·배수를 **한 번만** 읽고 돌린다
// (행마다 config.json 을 다시 읽지 않게).
func Summarize(rows []Usage) Result {
	table := Pricing(time.Time{})
	mult := Multipliers()

	var byAxis Axis
	var usd float64
	ttlUnknownRows := 0
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
		USD:            usd,
		ByAxis:         byAxis,
		Unpriced:       unpriced,
		TTLUnknownRows: ttlUnknownRows,
		PricedAt:       PricedAt(),
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
	return math.Max(multMin, math.Min(multMax, n))
}

// tok 은 유한하고 0 보다 큰 값만 인정한다. 음수·NaN 은 0 이다.
func tok(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
		return 0
	}
	return v
}
