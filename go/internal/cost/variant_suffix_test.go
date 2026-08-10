package cost

// 변형 접미사(-high · -medium · -low · -thinking) 스트리핑 검증.
//
// 왜 필요한가(실측): Google Antigravity CLI(`agy`)가 보고하는 모델 ID 는 **API 과금 ID + 변형
// 접미사** 형태다(`agy models` 실제 출력: gemini-3.6-flash-medium · claude-opus-4-6-thinking …).
// 수집기가 원문 그대로 보내므로, 접미사를 벗기지 않으면 이 ID 들이 **전부 unpriced 로 빠진다**
// — 시드에는 gemini-3.6-flash · claude-opus-4-6 로 등록돼 있다.
//
// 이 파일이 지키는 것은 셋이다:
//   ① 접미사를 벗겨 단가에 닿는가 (매칭 표)
//   ② 벗기면 **안 되는 것**을 안 벗기는가 (-lite · -latest · 기존 시드 키 전부)
//   ③ 앞으로 이 접미사로 끝나는 모델이 시드에 추가되면 **즉시 빨개지는가** (충돌 검사)

import (
	"strings"
	"testing"
)

// ── ③ 충돌 검사 — 가장 중요한 테스트다 ──────────────────────────────────────
//
// 시드에 `-high|-medium|-low|-thinking` 로 끝나는 키가 생기면, 그 모델을 조회할 때 접미사가
// 잘려 **다른 모델의 단가**가 조용히 붙는다. 원문 우선 규칙(stripVariantSuffix)이 시드에 있는
// 이름은 보호하지만, 그건 "조회가 성공한다"는 뜻일 뿐 표가 안전하다는 뜻은 아니다 —
// 예컨대 `foo-low` 와 `foo` 가 **둘 다** 시드에 있으면 사람이 둘을 혼동한다.
// 그런 키가 등장하는 순간 여기서 멈추고, 그때 접미사 목록과 표를 함께 재검토하게 만든다.
func TestSeed_NoKeyEndsWithVariantSuffix(t *testing.T) {
	for model := range seed {
		for _, suf := range variantSuffixes {
			if strings.HasSuffix(model, suf) {
				t.Fatalf("시드 키 %q 가 변형 접미사 %q 로 끝난다 — 스트리핑과 충돌한다. "+
					"이 모델을 추가하려면 variantSuffixes 와 조회 규칙을 함께 재검토하라", model, suf)
			}
		}
	}
	// -lite 는 대상이 아니다(정식 과금 ID의 일부). 목록에 새어 들어가지 않았는지 못 박는다.
	for _, suf := range variantSuffixes {
		if suf == "-lite" || suf == "-preview" || suf == "-pro" || suf == "-latest" {
			t.Fatalf("variantSuffixes 에 %q 가 들어갔다 — 정식 과금 ID를 잘라 먹는다", suf)
		}
	}
}

// ── ① 접미사 스트리핑 + ② 안 벗기는 것 ──────────────────────────────────────
func TestNormalizeModel_VariantSuffix(t *testing.T) {
	cases := []struct{ in, want string }{
		// agy 가 실제로 보내는 ID들.
		{"gemini-3.6-flash-high", "gemini-3.6-flash"},
		{"gemini-3.6-flash-medium", "gemini-3.6-flash"},
		{"gemini-3.6-flash-low", "gemini-3.6-flash"},
		{"gemini-3.5-flash-high", "gemini-3.5-flash"},
		{"gemini-3.5-flash-medium", "gemini-3.5-flash"},
		{"gemini-3.5-flash-low", "gemini-3.5-flash"},
		{"gemini-3.1-pro-high", "gemini-3.1-pro"},
		{"gemini-3.1-pro-low", "gemini-3.1-pro"},
		{"claude-sonnet-4-6", "claude-sonnet-4-6"},
		{"claude-opus-4-6-thinking", "claude-opus-4-6"},
		{"gpt-oss-120b-medium", "gpt-oss-120b"},

		// 대소문자·공백·벤더 접두사와 함께 와도 같은 결과.
		{"  GEMINI-3.6-FLASH-MEDIUM  ", "gemini-3.6-flash"},
		{"anthropic.claude-opus-4-6-thinking", "claude-opus-4-6"},
		{"claude-opus-5-thinking[1m]", "claude-opus-5"},

		// 날짜 스냅샷과 접미사가 겹쳐 와도 순서대로 벗는다.
		{"gemini-3.5-flash-medium-preview-09-2025", "gemini-3.5-flash"},

		// ── 벗기면 안 되는 것들 ──
		{"gemini-3.5-flash-lite", "gemini-3.5-flash-lite"},   // -lite 는 대상이 아니다
		{"gemini-2.5-flash-lite", "gemini-2.5-flash-lite"},   //
		{"gemini-3-flash-latest", "gemini-3-flash-latest"},   // -latest 는 정식 과금 ID
		{"gpt-5.3-chat-latest", "gpt-5.3-chat-latest"},       //
		{"gemini-3-flash-preview", "gemini-3-flash-preview"}, //
		{"gpt-5.5-pro", "gpt-5.5-pro"},                       // -pro 도 대상이 아니다
		{"gpt-5.4-nano", "gpt-5.4-nano"},                     //

		// 접미사만 온 이름은 빈 문자열로 만들지 않는다(잘라 봐야 조회 불가능한 쓰레기가 된다).
		{"-low", "-low"},
		{"-thinking", "-thinking"},
	}
	for _, c := range cases {
		if got := NormalizeModel(c.in); got != c.want {
			t.Fatalf("NormalizeModel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// 한 번만 벗긴다 — 체인으로 반복하면 `foo-low-medium` 이 `foo` 가 되어 존재하지도 않는
// 매칭이 생긴다. 접미사가 두 겹이면 **바깥 한 겹만** 떨어진다.
func TestNormalizeModel_VariantSuffixStripsOnlyOnce(t *testing.T) {
	cases := []struct{ in, want string }{
		{"gemini-3.6-flash-low-medium", "gemini-3.6-flash-low"},
		{"claude-opus-4-6-thinking-thinking", "claude-opus-4-6-thinking"},
		{"gemini-3.6-flash-medium-high", "gemini-3.6-flash-medium"},
	}
	for _, c := range cases {
		if got := NormalizeModel(c.in); got != c.want {
			t.Fatalf("NormalizeModel(%q) = %q, want %q — 접미사를 두 번 이상 벗겼다", c.in, got, c.want)
		}
	}
}

// 원문 우선 — 벗기기 **전에** 원문이 시드에 있으면 그것을 쓴다.
//
// 지금 시드에는 이 접미사로 끝나는 키가 없다(위 충돌 검사가 보장한다). 그래서 이 분기는
// 실제 시드로는 검증할 수 없고, 알려진 모델 집합을 주입해서 본다. 미래에 `...-thinking` 이
// 정식 과금 ID로 등록되는 날 이 분기가 그 모델을 지킨다.
func TestStripVariantSuffix_KnownNameWins(t *testing.T) {
	known := map[string]seedEntry{
		"acme-thinking": {provider: ProviderAnthropic, price: Price{1, 2}},
		"acme":          {provider: ProviderAnthropic, price: Price{9, 9}},
	}
	if got := stripVariantSuffixIn("acme-thinking", known); got != "acme-thinking" {
		t.Fatalf("stripVariantSuffixIn(acme-thinking) = %q — 시드에 있는 원문을 잘라 먹었다", got)
	}
	// 원문이 없으면 벗긴다.
	if got := stripVariantSuffixIn("acme-high", known); got != "acme" {
		t.Fatalf("stripVariantSuffixIn(acme-high) = %q, want %q", got, "acme")
	}
}

// ── ①' 매칭 결과 표 — agy 가 실제로 보내는 11개 ID 전부 ──────────────────────
//
// 정규화가 이름을 예쁘게 만드는 게 아니라 **단가표에 닿는 것**이 목적이므로, 이름이 아니라
// 최종 단가로 판정한다. unpriced 로 남겨야 하는 둘(gemini-3.1-pro · gpt-oss-120b)은
// 단가 미공개라 **일부러** 등록하지 않은 것이다 — 추측 매핑을 넣으면 여기서 빨개진다.
func TestAgyModelIDs_PricingTable(t *testing.T) {
	useConfig(t, `{}`)
	tbl := Pricing(day("2026-08-10"))

	cases := []struct {
		id     string
		priced bool
		in     float64 // priced 일 때 기대 입력가
		out    float64 // priced 일 때 기대 출력가
		via    string  // 정규화 후 닿아야 할 시드 키
	}{
		{"gemini-3.6-flash-high", true, 1.50, 7.50, "gemini-3.6-flash"},
		{"gemini-3.6-flash-medium", true, 1.50, 7.50, "gemini-3.6-flash"},
		{"gemini-3.6-flash-low", true, 1.50, 7.50, "gemini-3.6-flash"},
		{"gemini-3.5-flash-high", true, 1.50, 9.00, "gemini-3.5-flash"},
		{"gemini-3.5-flash-medium", true, 1.50, 9.00, "gemini-3.5-flash"},
		{"gemini-3.5-flash-low", true, 1.50, 9.00, "gemini-3.5-flash"},
		{"gemini-3.1-pro-high", false, 0, 0, "gemini-3.1-pro"},
		{"gemini-3.1-pro-low", false, 0, 0, "gemini-3.1-pro"},
		{"claude-sonnet-4-6", true, 3.00, 15.00, "claude-sonnet-4-6"},
		{"claude-opus-4-6-thinking", true, 5.00, 25.00, "claude-opus-4-6"},
		{"gpt-oss-120b-medium", false, 0, 0, "gpt-oss-120b"},
	}

	t.Logf("%-28s %-24s %-9s %10s %10s", "agy model id", "→ normalized", "priced", "in $/MTok", "out $/MTok")
	t.Logf("%s", strings.Repeat("-", 86))
	for _, c := range cases {
		norm := NormalizeModel(c.id)
		// 1M 입력 · 1M 출력 = 단가 그 자체(USD/MTok).
		got := CostOf(Usage{Model: c.id, Input: mtok, Output: mtok}, tbl, Mult{})
		t.Logf("%-28s %-24s %-9v %10.2f %10.2f", c.id, norm, got.Priced, got.ByAxis.Input, got.ByAxis.Output)

		if norm != c.via {
			t.Errorf("%q → %q, want %q", c.id, norm, c.via)
		}
		if got.Priced != c.priced {
			t.Errorf("%q priced = %v, want %v (단가 미공개 모델에 추측 단가를 붙이지 않는다)", c.id, got.Priced, c.priced)
			continue
		}
		if !c.priced {
			if got.USD != 0 {
				t.Errorf("%q unpriced 인데 USD = %v", c.id, got.USD)
			}
			continue
		}
		approx(t, c.id+" 입력", got.ByAxis.Input, c.in)
		approx(t, c.id+" 출력", got.ByAxis.Output, c.out)

		// 접미사를 벗긴 결과가 **원본 모델과 비트 단위로 같은** 비용이어야 한다.
		base := CostOf(Usage{Model: c.via, Input: mtok, Output: mtok}, tbl, Mult{})
		if got.USD != base.USD {
			t.Errorf("%q → %v, %q → %v — 같은 단가여야 한다", c.id, got.USD, c.via, base.USD)
		}
	}
}

// unpriced 로 남겨야 하는 것들이 **합계에서 이름으로 보이는지**.
// 조용히 $0 으로 처리되면 합계가 틀렸다는 사실이 화면에서 사라진다.
func TestSummarize_AgyUnpricedSurfaces(t *testing.T) {
	useConfig(t, `{}`)
	s := Summarize([]Usage{
		{Model: "gemini-3.1-pro-high", Input: mtok, Output: mtok},
		{Model: "gemini-3.1-pro-low", Input: mtok, Output: mtok},
		{Model: "gpt-oss-120b-medium", Input: mtok, Output: mtok},
		{Model: "gemini-3.6-flash-medium", Input: mtok, Output: mtok},
	})
	want := []string{"gemini-3.1-pro", "gpt-oss-120b"}
	if len(s.Unpriced) != len(want) {
		t.Fatalf("unpriced = %v, want %v", s.Unpriced, want)
	}
	for i, w := range want {
		if s.Unpriced[i] != w {
			t.Fatalf("unpriced = %v, want %v", s.Unpriced, want)
		}
	}
	// 접미사가 붙은 원문이 아니라 **정규화형**으로 올라간다(같은 모델이 3벌로 보이지 않게).
	approx(t, "priced 행만 합산", s.USD, 1.50+7.50)
	t.Logf("Summarize.Unpriced = %v · USD = %v", s.Unpriced, s.USD)
}

// ── ② 무회귀 — 접미사 없는 기존 모델명의 매칭 결과가 하나도 안 바뀐다 ────────
//
// 시드 키 전부를 훑어 (a) 정규화가 자기 자신으로 떨어지는지, (b) 단가표 조회가 여전히
// 성공하는지 본다. 스트리핑이 기존 이름 하나라도 건드리면 여기서 즉시 잡힌다.
func TestNormalizeModel_NoRegressionAcrossSeed(t *testing.T) {
	useConfig(t, `{}`)
	tbl := Pricing(day("2026-08-10"))
	for model, e := range seed {
		if got := NormalizeModel(model); got != model {
			t.Fatalf("시드 키 %q → %q — 스트리핑이 기존 모델명을 건드렸다", model, got)
		}
		c := CostOf(Usage{Model: model, Input: mtok, Output: mtok}, tbl, Mult{})
		if !c.Priced {
			t.Fatalf("시드 키 %q 가 조회되지 않는다", model)
		}
		if c.ByAxis.Input != e.price.Input && (e.introUntil == "" || e.intro.Input != c.ByAxis.Input) {
			t.Fatalf("%q 입력가 = %v, 시드 = %v", model, c.ByAxis.Input, e.price.Input)
		}
	}
	// 일부러 미등록으로 둔 모델들이 여전히 미등록인지(접미사 스트리핑이 이들을 다른 모델에
	// 붙여 놓지 않았는지). openaiUnpriced·googleUnpriced 는 각 시드 파일의 선언이다.
	for _, m := range append(append([]string{}, openaiUnpriced...), googleUnpriced...) {
		if c := CostOf(Usage{Model: m, Input: mtok}, tbl, Mult{}); c.Priced {
			t.Fatalf("%q 가 priced 로 바뀌었다 — 추측 단가가 붙었다", m)
		}
	}
}
