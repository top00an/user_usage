package cost

// 멀티 공급사(OpenAI·Google) 시드와 모델별 캐시 배수 검증.
//
// 이 파일이 지키는 것: 공급사마다 다른 **배수**가 실제로 갈리는가. 단가가 맞아도 배수를
// 전역 하나로 뭉개면 비용은 조용히 틀린다 — o3 캐시읽기는 0.25배인데 Anthropic 기준 0.1배를
// 적용하면 2.5배 과소, o1-pro(캐시 할인 없음)는 **10배 과소**가 된다.
//
// 기대 표는 브리프에 실린 공식 가격표를 그대로 옮긴 것이고, 시드와 이 표를 **양방향**으로
// 대조한다(빠진 모델도, 표에 없는 모델이 시드에 들어온 것도 잡는다).

import (
	"math"
	"testing"
	"time"
)

type modelSpec struct {
	in, out   float64
	readMult  float64
	writeMult float64
	long      Price // 제로값이면 계단 없음
}

// developers.openai.com/api/docs/pricing · 검증일 2026-08-10
var wantOpenAI = map[string]modelSpec{
	"gpt-5.5":       {in: 5, out: 30, readMult: 0.1},
	"gpt-5.4-mini":  {in: 0.75, out: 4.5, readMult: 0.1},
	"gpt-5.4":       {in: 2.5, out: 15, readMult: 0.1},
	"gpt-5.4-nano":  {in: 0.2, out: 1.25, readMult: 0.1},
	"gpt-5.2":       {in: 1.75, out: 14, readMult: 0.1},
	"gpt-5.1":       {in: 1.25, out: 10, readMult: 0.1},
	"gpt-5":         {in: 1.25, out: 10, readMult: 0.1},
	"gpt-5-mini":    {in: 0.25, out: 2, readMult: 0.1},
	"gpt-5-nano":    {in: 0.05, out: 0.4, readMult: 0.1},
	"gpt-5.3-codex": {in: 1.75, out: 14, readMult: 0.1},
	"gpt-5.5-cyber": {in: 12.5, out: 75, readMult: 0.1},

	// GPT-5.6 계열만 캐시 생성에 과금한다.
	"gpt-5.6-sol":   {in: 5, out: 30, readMult: 0.1, writeMult: 1.25},
	"gpt-5.6-terra": {in: 2, out: 12, readMult: 0.1, writeMult: 1.25},
	"gpt-5.6-luna":  {in: 0.2, out: 1.2, readMult: 0.1, writeMult: 1.25},

	// *-pro 는 캐시 할인이 없다(1.0).
	"gpt-5.5-pro": {in: 30, out: 180, readMult: 1.0},
	"gpt-5.4-pro": {in: 30, out: 180, readMult: 1.0},
	"gpt-5.2-pro": {in: 21, out: 168, readMult: 1.0},
	"gpt-5-pro":   {in: 15, out: 120, readMult: 1.0},

	"o3":      {in: 2, out: 8, readMult: 0.25},
	"o4-mini": {in: 1.1, out: 4.4, readMult: 0.25},
	"o3-mini": {in: 1.1, out: 4.4, readMult: 0.5},
	"o1":      {in: 15, out: 60, readMult: 0.5},
	"o1-pro":  {in: 150, out: 600, readMult: 1.0},
	"o3-pro":  {in: 20, out: 80, readMult: 1.0},

	"gpt-4.1":      {in: 2, out: 8, readMult: 0.25},
	"gpt-4.1-mini": {in: 0.4, out: 1.6, readMult: 0.25},
	"gpt-4.1-nano": {in: 0.1, out: 0.4, readMult: 0.25},
	"gpt-4o":       {in: 2.5, out: 10, readMult: 0.5},
	"gpt-4o-mini":  {in: 0.15, out: 0.6, readMult: 0.5},
}

// ai.google.dev/gemini-api/docs/pricing · 검증일 2026-08-05
// 전 모델 캐시 히트 0.10배 · 캐시 생성 무과금.
var wantGoogle = map[string]modelSpec{
	"gemini-3.6-flash":       {in: 1.5, out: 7.5, readMult: 0.1},
	"gemini-3.5-flash":       {in: 1.5, out: 9, readMult: 0.1},
	"gemini-3.5-flash-lite":  {in: 0.3, out: 2.5, readMult: 0.1},
	"gemini-3.1-flash-lite":  {in: 0.25, out: 1.5, readMult: 0.1},
	"gemini-2.5-flash":       {in: 0.3, out: 2.5, readMult: 0.1},
	"gemini-2.5-flash-lite":  {in: 0.1, out: 0.4, readMult: 0.1},
	"gemini-3-flash-preview": {in: 0.5, out: 3, readMult: 0.1},

	// gemini-3.5-flash 의 별칭 — 반드시 같은 단가여야 한다.
	"gemini-3-flash": {in: 1.5, out: 9, readMult: 0.1},

	"gemini-2.5-pro":                     {in: 1.25, out: 10, readMult: 0.1, long: Price{2.5, 15}},
	"gemini-3.1-pro-preview":             {in: 2, out: 12, readMult: 0.1, long: Price{4, 18}},
	"gemini-3.1-pro-preview-customtools": {in: 2, out: 12, readMult: 0.1, long: Price{4, 18}},
}

func approx(t *testing.T, what string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9*math.Max(1, math.Abs(want)) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

// axisRates 는 1M 토큰씩 태워 축별 **실효 단가**(USD/1M)를 뽑는다.
// 배수를 시드 필드로 직접 읽지 않고 CostOf 를 통해 재는 이유: 필드가 맞아도 배선이 빠지면
// 비용은 그대로 틀리기 때문이다. 재야 할 것은 필드가 아니라 결과다.
func axisRates(t *testing.T, model string, tbl Table) (in, out, cacheRead, cc5m, cc1h float64) {
	t.Helper()
	one := func(u Usage) Result {
		u.Model = model
		c := CostOf(u, tbl, Mult{})
		if !c.Priced {
			t.Fatalf("%s 이 단가표에 없다 — 그 모델 사용량이 비용에서 통째로 빠진다", model)
		}
		return c
	}
	return one(Usage{Input: mtok}).ByAxis.Input,
		one(Usage{Output: mtok}).ByAxis.Output,
		one(Usage{CacheRead: mtok}).ByAxis.CacheRead,
		one(Usage{CacheCreate: mtok, CacheCreate5m: mtok}).ByAxis.CacheCreate,
		one(Usage{CacheCreate: mtok, CacheCreate1h: mtok}).ByAxis.CacheCreate
}

func checkSeed(t *testing.T, want map[string]modelSpec, have map[string]seedEntry, provider string) {
	t.Helper()
	useConfig(t, `{}`)
	tbl := Pricing(day("2026-08-10"))

	// ① 시드에 있는데 기대 표에 없는 모델 — 검증 없이 들어온 단가다.
	for m := range have {
		if _, ok := want[m]; !ok {
			t.Fatalf("%s: 시드에 %q 가 있는데 공식 대조표에 없다 — 출처 없는 단가다", provider, m)
		}
	}

	for m, w := range want {
		e, ok := have[m]
		if !ok {
			t.Fatalf("%s: %q 가 시드에 없다", provider, m)
		}
		if e.provider != provider {
			t.Fatalf("%s: %q 의 provider = %q", provider, m, e.provider)
		}

		in, out, cr, cc5, cc1 := axisRates(t, m, tbl)
		approx(t, m+" 입력", in, w.in)
		approx(t, m+" 출력", out, w.out)
		approx(t, m+" 캐시읽기", cr, w.in*w.readMult)
		// 캐시 생성은 TTL 로 갈리지 않는다(공급사가 TTL 개념을 청구에 쓰지 않는다).
		approx(t, m+" 캐시생성(5m)", cc5, w.in*w.writeMult)
		approx(t, m+" 캐시생성(1h)", cc1, w.in*w.writeMult)

		gotLong, tiered := LongContextPrice(m)
		if tiered != (w.long != Price{}) {
			t.Fatalf("%s: %q 계단 여부 = %v", provider, m, tiered)
		}
		if tiered && gotLong != w.long {
			t.Fatalf("%s: %q 초과구간 단가 = %+v, want %+v", provider, m, gotLong, w.long)
		}
	}
}

func TestSeed_OpenAIMatchesOfficialTable(t *testing.T) {
	checkSeed(t, wantOpenAI, openaiSeed, ProviderOpenAI)
}

func TestSeed_GoogleMatchesOfficialTable(t *testing.T) {
	checkSeed(t, wantGoogle, googleSeed, ProviderGoogle)
}

func TestSeed_UnpricedModelsStayUnpriced(t *testing.T) {
	// 단가 미공개 모델에 비슷한 이름의 단가를 빌려 오면, 화면에는 멀쩡한 숫자가 뜨는데 그게
	// 맞는지 아무도 모른다. 미등록이어야 Unpriced 목록에 이름이 올라간다.
	useConfig(t, `{}`)
	tbl := Pricing(day("2026-08-10"))
	for _, m := range append(append([]string{}, openaiUnpriced...), googleUnpriced...) {
		c := CostOf(Usage{Model: m, Output: 1e9}, tbl, Mult{})
		if c.Priced {
			t.Fatalf("%q 에 단가가 붙었다 — 공식 가격표에 항목이 없는 모델이다", m)
		}
		if c.USD != 0 || c.Model != m {
			t.Fatalf("%q: usd=%v model=%q", m, c.USD, c.Model)
		}
	}
}

func TestSeed_Gemini3FlashAliasIsNotThePreviewModel(t *testing.T) {
	// 접두사 매칭으로 조회하면 gemini-3-flash 가 gemini-3-flash-preview 에 붙어 3배 틀린다.
	// (별칭의 실제 단가 $1.50 vs 프리뷰 $0.50)
	useConfig(t, `{}`)
	tbl := Pricing(day("2026-08-10"))

	alias := tbl["gemini-3-flash"]
	canonical := tbl["gemini-3.5-flash"]
	preview := tbl["gemini-3-flash-preview"]

	if alias != canonical {
		t.Fatalf("별칭 단가 %+v ≠ 본체(gemini-3.5-flash) %+v", alias, canonical)
	}
	if alias == preview {
		t.Fatalf("별칭이 프리뷰 단가로 붙었다: %+v", alias)
	}
	if got := alias.Input / preview.Input; math.Abs(got-3) > 1e-9 {
		t.Fatalf("별칭/프리뷰 입력가 비 = %v, want 3 (혼동하면 정확히 이만큼 틀린다)", got)
	}
	// 정규화가 프리뷰 ID 를 별칭으로 깎아 먹지 않는지도 본다.
	if got := NormalizeModel("gemini-3-flash-preview"); got != "gemini-3-flash-preview" {
		t.Fatalf("NormalizeModel(gemini-3-flash-preview) = %q", got)
	}
}

func TestCostOf_CacheReadMultIsPerModel(t *testing.T) {
	// 같은 축, 같은 토큰량인데 모델마다 배수가 달라야 한다. 전역 상수 하나로 되돌아가면
	// 여기가 제일 먼저 깨진다.
	useConfig(t, `{}`)
	tbl := Pricing(day("2026-08-10"))
	cases := []struct {
		model string
		want  float64 // USD / 1M 캐시읽기 토큰
	}{
		{"claude-opus-5", 5 * 0.1},     // Anthropic — 전역 0.1 유지
		{"gpt-5.5", 5 * 0.1},           // OpenAI 본선
		{"o3", 2 * 0.25},               // o3 계열
		{"gpt-4o", 2.5 * 0.5},          // 4o 계열
		{"o1-pro", 150 * 1.0},          // 캐시 할인 없음 — 전역 0.1 이면 10배 과소
		{"gemini-2.5-pro", 1.25 * 0.1}, // Google
	}
	for _, c := range cases {
		got := CostOf(Usage{Model: c.model, CacheRead: mtok}, tbl, Mult{}).ByAxis.CacheRead
		approx(t, c.model+" 캐시읽기 실효단가", got, c.want)
	}
}

func TestCostOf_CacheCreateIsFreeWhereProviderDoesNotCharge(t *testing.T) {
	useConfig(t, `{}`)
	tbl := Pricing(day("2026-08-10"))

	// OpenAI 5.6 이외 · Google 전부 — TTL 분해값이 있든 없든 $0.
	for _, m := range []string{"gpt-5.5", "o3", "gpt-4o", "gemini-2.5-pro", "gemini-3.5-flash"} {
		for _, row := range []Usage{
			{Model: m, CacheCreate: mtok},
			{Model: m, CacheCreate: mtok, CacheCreate5m: mtok},
			{Model: m, CacheCreate: mtok, CacheCreate1h: mtok},
		} {
			if got := CostOf(row, tbl, Mult{}).ByAxis.CacheCreate; got != 0 {
				t.Fatalf("%s 캐시생성 = %v, want 0 (이 공급사는 캐시 쓰기를 청구하지 않는다)", m, got)
			}
		}
	}

	// GPT-5.6 계열만 1.25배로 청구한다. TTL 로 갈리지 않는다.
	for _, tc := range []struct {
		model string
		in    float64
	}{{"gpt-5.6-sol", 5}, {"gpt-5.6-terra", 2}, {"gpt-5.6-luna", 0.2}} {
		for _, row := range []Usage{
			{Model: tc.model, CacheCreate: mtok},
			{Model: tc.model, CacheCreate: mtok, CacheCreate5m: mtok},
			{Model: tc.model, CacheCreate: mtok, CacheCreate1h: mtok},
		} {
			got := CostOf(row, tbl, Mult{}).ByAxis.CacheCreate
			approx(t, tc.model+" 캐시생성", got, tc.in*1.25)
		}
	}
}

func TestModelMult_GlobalOverrideDoesNotLeakIntoOtherProviders(t *testing.T) {
	// config 의 전역 배수는 "Anthropic 기준 조정"으로 들어온 값이다. 이걸 o3(0.25배)에까지
	// 덮어씌우면 이번 개편이 고치려던 오류로 되돌아간다.
	useConfig(t, `{"usage":{"cacheReadMult":0.5,"cacheCreateMult":2}}`)
	tbl := Pricing(day("2026-08-10"))

	// Anthropic 은 전역 오버라이드를 그대로 받는다(기존 동작).
	approx(t, "opus 캐시읽기", CostOf(Usage{Model: "claude-opus-5", CacheRead: mtok}, tbl, Mult{}).ByAxis.CacheRead, 5*0.5)
	approx(t, "opus 캐시생성", CostOf(Usage{Model: "claude-opus-5", CacheCreate: mtok}, tbl, Mult{}).ByAxis.CacheCreate, 5*2)

	// OpenAI·Google 은 모델별 값을 유지한다.
	approx(t, "o3 캐시읽기", CostOf(Usage{Model: "o3", CacheRead: mtok}, tbl, Mult{}).ByAxis.CacheRead, 2*0.25)
	if got := CostOf(Usage{Model: "o3", CacheCreate: mtok}, tbl, Mult{}).ByAxis.CacheCreate; got != 0 {
		t.Fatalf("o3 캐시생성 = %v — 전역 cacheCreateMult 가 새어 들어왔다", got)
	}
	approx(t, "gemini 캐시읽기", CostOf(Usage{Model: "gemini-3.5-flash", CacheRead: mtok}, tbl, Mult{}).ByAxis.CacheRead, 1.5*0.1)
}

func TestNormalizeModel_MultiProviderSnapshots(t *testing.T) {
	cases := []struct{ in, want string }{
		// 기존 Anthropic 동작 — 바뀌면 안 된다.
		{"claude-opus-4-5-20251101", "claude-opus-4-5"},
		{"claude-haiku-4-5-20251001", "claude-haiku-4-5"},
		{"claude-opus-5[1m]", "claude-opus-5"},
		{"anthropic.claude-opus-5", "claude-opus-5"},

		// OpenAI 날짜 스냅샷.
		{"gpt-5.5-2026-04-23", "gpt-5.5"},
		{"gpt-4o-2024-08-06", "gpt-4o"},
		{"o3-2025-04-16", "o3"},

		// Gemini 프리뷰 스냅샷.
		{"gemini-2.5-flash-lite-preview-09-2025", "gemini-2.5-flash-lite"},
		{"gemini-2.5-flash-preview-05-2026", "gemini-2.5-flash"},

		// -latest 는 그 자체가 과금 ID다 — 절대 자르지 않는다.
		{"chat-latest", "chat-latest"},
		{"gpt-5.3-chat-latest", "gpt-5.3-chat-latest"},
		{"gemini-3-flash-latest", "gemini-3-flash-latest"},

		// 잘라 내면 안 되는 것들.
		{"gemini-3-flash-preview", "gemini-3-flash-preview"},
		{"gemini-3.1-pro-preview-customtools", "gemini-3.1-pro-preview-customtools"},
		{"gpt-5.6-sol", "gpt-5.6-sol"},
		{"o4-mini", "o4-mini"},
		{"gpt-4.1-nano", "gpt-4.1-nano"},
	}
	for _, c := range cases {
		if got := NormalizeModel(c.in); got != c.want {
			t.Fatalf("NormalizeModel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeModel_SnapshotStripLandsOnPricedModel(t *testing.T) {
	// 정규화의 목적은 이름을 예쁘게 만드는 게 아니라 **단가표에 닿는 것**이다.
	useConfig(t, `{}`)
	tbl := Pricing(day("2026-08-10"))
	pairs := [][2]string{
		{"gpt-5.5-2026-04-23", "gpt-5.5"},
		{"gemini-2.5-flash-lite-preview-09-2025", "gemini-2.5-flash-lite"},
		{"claude-opus-4-5-20251101", "claude-opus-4-5"},
	}
	for _, p := range pairs {
		snap := CostOf(Usage{Model: p[0], Input: mtok, Output: mtok, CacheRead: mtok}, tbl, Mult{})
		base := CostOf(Usage{Model: p[1], Input: mtok, Output: mtok, CacheRead: mtok}, tbl, Mult{})
		if !snap.Priced {
			t.Fatalf("%q 가 정규화 후에도 미등록이다", p[0])
		}
		if snap.USD != base.USD {
			t.Fatalf("%q → %v, %q → %v", p[0], snap.USD, p[1], base.USD)
		}
	}
}

func TestSeedMeta_PricedAtPerProvider(t *testing.T) {
	if SeedPricedAt != SeedPricedAtAnthropic {
		t.Fatalf("SeedPricedAt = %q — 기존 호출부 하위호환이 깨졌다", SeedPricedAt)
	}
	cases := map[string]string{
		ProviderAnthropic: "2026-08-04",
		ProviderOpenAI:    "2026-08-10",
		ProviderGoogle:    "2026-08-05",
	}
	for p, want := range cases {
		if got := SeedPricedAtFor(p); got != want {
			t.Fatalf("SeedPricedAtFor(%q) = %q, want %q", p, got, want)
		}
	}
	// 모르는 공급사에 날짜를 지어내지 않는다.
	if got := SeedPricedAtFor("meta"); got != "" {
		t.Fatalf("SeedPricedAtFor(meta) = %q, want \"\"", got)
	}
}

func TestProviderOf(t *testing.T) {
	cases := map[string]string{
		"claude-opus-5":            ProviderAnthropic,
		"claude-opus-4-5-20251101": ProviderAnthropic,
		"gpt-5.6-sol":              ProviderOpenAI,
		"gpt-5.5-2026-04-23":       ProviderOpenAI,
		"gemini-3-flash":           ProviderGoogle,
	}
	for m, want := range cases {
		got, ok := ProviderOf(m)
		if !ok || got != want {
			t.Fatalf("ProviderOf(%q) = %q,%v want %q,true", m, got, ok, want)
		}
	}
	// 이름만 보고 추측하지 않는다 — 미등록이면 미상이다.
	for _, m := range []string{"gpt-5-codex", "some-unreleased-model-x", ""} {
		if got, ok := ProviderOf(m); ok {
			t.Fatalf("ProviderOf(%q) = %q,true — 등록되지 않은 모델의 공급사를 지어냈다", m, got)
		}
	}
}

func TestLongContextPrice_DefinedButNotApplied(t *testing.T) {
	// 계단 요금은 이번 범위 밖이다. 정의는 있되 계산에는 들어가지 않아야 한다 —
	// 집계된 행으로는 요청별 컨텍스트 길이를 알 수 없어 계단 판정이 불가능하다.
	useConfig(t, `{}`)
	tbl := Pricing(day("2026-08-10"))

	long, tiered := LongContextPrice("gemini-2.5-pro")
	if !tiered || long != (Price{2.5, 15}) {
		t.Fatalf("gemini-2.5-pro 초과구간 = %+v,%v", long, tiered)
	}
	if _, tiered := LongContextPrice("gemini-3.5-flash"); tiered {
		t.Fatal("flash 계열에 계단이 붙었다")
	}
	if _, tiered := LongContextPrice("claude-opus-5"); tiered {
		t.Fatal("Anthropic 모델에 계단이 붙었다")
	}

	// 토큰이 아무리 많아도 기본 구간 단가만 쓴다(1M 입력 = $1.25, $2.50 아님).
	c := CostOf(Usage{Model: "gemini-2.5-pro", Input: 500 * mtok}, tbl, Mult{})
	approx(t, "gemini-2.5-pro 500M 입력", c.ByAxis.Input, 500*1.25)
}

func TestSeed_TableIsSaneAndDisjoint(t *testing.T) {
	// 시드 전체가 Pricing 표에 들어오는지, 단가가 0 이거나 음수인 항목이 없는지.
	// 단가 0 은 "공짜"가 아니라 거의 항상 오타이고, 그 오타는 화면에서 보이지 않는다.
	useConfig(t, `{}`)
	tbl := Pricing(day("2026-08-10"))
	if len(tbl) != len(seed) {
		t.Fatalf("Pricing 표 %d개 ≠ 시드 %d개", len(tbl), len(seed))
	}
	for m, p := range tbl {
		if p.Input <= 0 || p.Output <= 0 {
			t.Fatalf("%q 단가가 0 이하다: %+v", m, p)
		}
		if m != NormalizeModel(m) {
			t.Fatalf("시드 키 %q 가 정규화형이 아니다 — 영원히 조회되지 않는다", m)
		}
	}
	if len(seed) != len(anthropicSeed)+len(openaiSeed)+len(googleSeed) {
		t.Fatal("공급사 표 사이에 키가 겹쳐 조용히 덮어써졌다")
	}
}

func TestMergeSeeds_DuplicateKeyPanics(t *testing.T) {
	// 조용한 덮어쓰기가 이 표에서 가장 위험한 사고다. 겹치면 init 에서 죽어야 한다.
	defer func() {
		if recover() == nil {
			t.Fatal("중복 키인데 통과했다 — 어느 단가가 이겼는지 아무 흔적도 남지 않는다")
		}
	}()
	mergeSeeds(
		map[string]seedEntry{"dup": {price: Price{1, 1}}},
		map[string]seedEntry{"dup": {price: Price{9, 9}}},
	)
}

func TestSummarize_MixedProvidersDoNotBleed(t *testing.T) {
	// 공급사가 섞인 합계에서 각 행이 제 배수를 쓰는지. 축 합계가 손계산과 맞아야 한다.
	useConfig(t, `{}`)
	s := Summarize([]Usage{
		{Model: "claude-opus-5", CacheRead: mtok, CacheCreate: mtok, CacheCreate1h: mtok}, // 0.5 + 10
		{Model: "o3", CacheRead: mtok, CacheCreate: mtok, CacheCreate1h: mtok},            // 0.5 + 0
		{Model: "gemini-3.5-flash", CacheRead: mtok},                                      // 0.15
		{Model: "gpt-5.6-sol", CacheCreate: mtok, CacheCreate5m: mtok},                    // 6.25
	})
	approx(t, "캐시읽기 합", s.ByAxis.CacheRead, 0.5+0.5+0.15)
	approx(t, "캐시생성 합", s.ByAxis.CacheCreate, 10+6.25)
	approx(t, "총액", s.USD, 0.5+0.5+0.15+10+6.25)
	if len(s.Unpriced) != 0 {
		t.Fatalf("unpriced = %v", s.Unpriced)
	}
}

func TestPricing_ConfigOverrideStillWorksForNewProviders(t *testing.T) {
	// 단가는 config 가 이긴다(시드는 낡는다). 배수는 모델별 시드가 이긴다.
	useConfig(t, `{"usage":{"pricing":{"GPT-5.5-2026-04-23":{"input":7,"output":40}}}}`)
	tbl := Pricing(time.Time{})
	if got := tbl["gpt-5.5"]; got != (Price{Input: 7, Output: 40}) {
		t.Fatalf("오버라이드 키가 정규화되지 않았다: %+v", got)
	}
	// 배수는 그대로 0.1 — 오버라이드된 입력가 기준으로 적용된다.
	approx(t, "gpt-5.5 캐시읽기", CostOf(Usage{Model: "gpt-5.5", CacheRead: mtok}, tbl, Mult{}).ByAxis.CacheRead, 7*0.1)
}
