package cost

import (
	"testing"
	"time"
)

/*
 * ── 단가표 전수 감사 (2026-08-13) ─────────────────────────────────────────
 *
 * 51개 등재 모델을 공급사 공식 단가표와 1:1 대조한 결과를 못 박는다. 여기 적힌 숫자는
 * 전부 **공식 문서에서 읽은 값**이고, 출처는 각 블록 주석에 적었다. 이 파일이 빨개지면
 * 둘 중 하나다: (ㄱ) 누가 표를 잘못 고쳤다, (ㄴ) 공급사가 단가를 바꿨다.
 * (ㄴ)이면 **출처를 다시 확인하고 주석의 날짜까지 갱신**해야 한다 — 숫자만 맞추면
 * 다음 사람이 근거 없는 값을 물려받는다.
 *
 * 감사에서 실제로 잡은 것 넷:
 *   ① claude-sonnet-5 인트로가가 표준가로 확정됐다(9월 인상 취소) — 그대로 두면 09-01 에
 *      1.5배 과대계상으로 저절로 틀어진다. 실사용 모델이라 영향이 가장 크다.
 *   ② gpt-5.6-cyber 미등재 — 공식 단가가 있는데 빠져 있었다.
 *   ③ gpt-5.4 · gpt-5.5-pro · gpt-5.4-pro 의 계단(롱컨텍스트) 단가 누락 — 과소계상.
 *   ④ (코드 밖) fast mode · inference_geo 는 단가 수정자인데 모델링이 없다. 트랜스크립트에
 *      usage.speed · usage.inference_geo 로 이미 기록되고 있다 — 별건으로 남겼다.
 */

// 오늘이 언제든 같은 답이어야 하는 단가는 이 날짜로 잰다. 미래 날짜를 쓰는 이유는
// "인트로 만료 후에도 값이 같은가"를 재기 위해서다(①의 회귀 방지).
var auditDay = day("2026-12-31")

func priceAt(t *testing.T, model string, d time.Time) Price {
	t.Helper()
	p, ok := Pricing(d)[model]
	if !ok {
		t.Fatalf("%s 이 단가표에 없다", model)
	}
	return p
}

func wantPrice(t *testing.T, model string, in, out float64) {
	t.Helper()
	got := priceAt(t, model, auditDay)
	if got.Input != in || got.Output != out {
		t.Errorf("%s: got %v/%v want %v/%v", model, got.Input, got.Output, in, out)
	}
}

/*
 * Anthropic — platform.claude.com/docs/en/about-claude/pricing (2026-08-13 확인).
 * 캐시 배수는 공식표와 같다: 5분 쓰기 1.25배 · 1시간 쓰기 2배 · 히트 0.1배.
 */
func TestAudit_Anthropic(t *testing.T) {
	for _, tc := range []struct {
		model   string
		in, out float64
	}{
		{"claude-fable-5", 10, 50},
		{"claude-mythos-5", 10, 50},
		{"claude-opus-5", 5, 25},
		{"claude-opus-4-8", 5, 25},
		{"claude-opus-4-7", 5, 25},
		{"claude-opus-4-6", 5, 25},
		{"claude-opus-4-5", 5, 25},
		{"claude-sonnet-5", 2, 10}, // ① 인트로가가 표준가로 확정됐다
		{"claude-sonnet-4-6", 3, 15},
		{"claude-sonnet-4-5", 3, 15},
		{"claude-haiku-4-5", 1, 5},
	} {
		wantPrice(t, tc.model, tc.in, tc.out)
	}

	// 전역 캐시 배수가 공식 표(1.25 / 2 / 0.1)와 같은지 — 이 상수가 바뀌면 전 모델이 틀린다.
	if CacheCreateMult != 1.25 || CacheCreate1hMult != 2 || CacheReadMult != 0.1 {
		t.Errorf("Anthropic 캐시 배수: 5m=%v 1h=%v read=%v (공식 1.25/2/0.1)",
			CacheCreateMult, CacheCreate1hMult, CacheReadMult)
	}
}

/*
 * ① 회귀 방지 — claude-sonnet-5 는 **어느 날짜에도** $2/$10 이다.
 *
 * 예전 표는 introUntil="2026-08-31" · base=3/15 였다. 그 상태의 고약한 점은 코드를 아무도
 * 건드리지 않아도 09-01 부터 값이 바뀐다는 것이다 — CI 가 8월에 초록이어도 9월에 틀린다.
 * 그래서 인트로 기간 안/밖 두 날짜를 모두 잰다.
 */
func TestAudit_Sonnet5PriceIsDateStable(t *testing.T) {
	for _, d := range []string{"2026-08-01", "2026-08-31", "2026-09-01", "2027-01-01"} {
		got := priceAt(t, "claude-sonnet-5", day(d))
		if got.Input != 2 || got.Output != 10 {
			t.Fatalf("%s 기준 sonnet-5 = %v/%v (항상 2/10 이어야 한다 — 9월 인상은 취소됐다)",
				d, got.Input, got.Output)
		}
	}
}

/*
 * OpenAI — developers.openai.com/api/docs/pricing (2026-08-13 확인).
 * 5.6 계열 단가는 2026-07-30 인하가 반영된 값이다(Luna −80% · Terra −20%).
 */
func TestAudit_OpenAI(t *testing.T) {
	for _, tc := range []struct {
		model   string
		in, out float64
	}{
		{"gpt-5.6-sol", 5, 30},
		{"gpt-5.6-terra", 2, 12},
		{"gpt-5.6-luna", 0.2, 1.2},
		{"gpt-5.6-cyber", 12.5, 75}, // ② 감사에서 추가
		{"gpt-5.5", 5, 30},
		{"gpt-5.5-pro", 30, 180},
		{"gpt-5.5-cyber", 12.5, 75},
		{"gpt-5.4", 2.5, 15},
		{"gpt-5.4-mini", 0.75, 4.5},
		{"gpt-5.4-nano", 0.2, 1.25},
		{"gpt-5.4-pro", 30, 180},
		{"gpt-5.3-codex", 1.75, 14},
		{"gpt-5.2", 1.75, 14},
		{"gpt-5.2-pro", 21, 168},
		{"gpt-5.1", 1.25, 10},
		{"gpt-5", 1.25, 10},
		{"gpt-5-mini", 0.25, 2},
		{"gpt-5-nano", 0.05, 0.4},
		{"gpt-5-pro", 15, 120},
		{"gpt-4.1", 2, 8},
		{"gpt-4.1-mini", 0.4, 1.6},
		{"gpt-4.1-nano", 0.1, 0.4},
		{"gpt-4o", 2.5, 10},
		{"gpt-4o-mini", 0.15, 0.6},
		{"o1", 15, 60},
		{"o1-pro", 150, 600},
		{"o3", 2, 8},
		{"o3-mini", 1.1, 4.4},
		{"o3-pro", 20, 80},
		{"o4-mini", 1.1, 4.4},
	} {
		wantPrice(t, tc.model, tc.in, tc.out)
	}
}

/*
 * 캐시 히트 단가는 **모델별 배수 × 입력가**로 나와야 한다. 공식표의 "Cached Input" 열을
 * 그대로 적어 두고 우리 계산과 맞는지 본다 — 배수를 하나로 뭉치면 pro 가 10배 과소가 된다.
 */
func TestAudit_OpenAICachedInputMatchesOfficialColumn(t *testing.T) {
	const mtok = 1_000_000
	tbl := Pricing(auditDay)
	for _, tc := range []struct {
		model      string
		wantCached float64 // 공식표 Cached Input ($/1M)
	}{
		{"gpt-5.6-sol", 0.50},
		{"gpt-5.6-terra", 0.20},
		{"gpt-5.6-luna", 0.02},
		{"gpt-5.6-cyber", 1.25},
		{"gpt-5.5", 0.50},
		{"gpt-5.4", 0.25},
		{"gpt-5.4-mini", 0.075},
		{"gpt-5.3-codex", 0.175},
		{"gpt-5.1", 0.125},
		{"gpt-5-nano", 0.005},
		{"gpt-4.1", 0.50},      // 0.25배 계열
		{"gpt-4.1-mini", 0.10}, // 0.25배
		{"gpt-4o", 1.25},       // 0.5배 계열
		{"gpt-4o-mini", 0.075}, // 0.5배
		{"o1", 7.50},           // 0.5배
		{"o3", 0.50},           // 0.25배
		{"o4-mini", 0.275},     // 0.25배
		{"o3-mini", 0.55},      // 0.5배
		{"gpt-5-pro", 15},      // 할인 없음(1.0배) — 입력가와 같다
		{"gpt-5.5-pro", 30},
		{"gpt-5.2-pro", 21},
		{"o1-pro", 150},
		{"o3-pro", 20},
	} {
		got := CostOf(Usage{Model: tc.model, CacheRead: mtok}, tbl, Mult{}).ByAxis.CacheRead
		if d := got - tc.wantCached; d > 1e-9 || d < -1e-9 {
			t.Errorf("%s 캐시히트: got $%v want $%v (공식표 Cached Input 열)", tc.model, got, tc.wantCached)
		}
	}
}

/*
 * ③ 계단(롱컨텍스트) 단가. 공식표의 "Long context" 표기를 그대로 옮긴다.
 * 계단이 **없는** 모델에 값을 넣으면 없는 요금을 만들어 내므로 그쪽도 함께 잰다.
 */
func TestAudit_LongContextTiers(t *testing.T) {
	const mtok = 1_000_000
	tbl := Pricing(auditDay)

	for _, tc := range []struct {
		model   string
		longIn  float64
		longOut float64
	}{
		{"gpt-5.6-sol", 10, 45},
		{"gpt-5.6-terra", 4, 18},
		{"gpt-5.6-luna", 0.4, 1.8},
		{"gpt-5.5", 10, 45},
		{"gpt-5.4", 5, 22.5},     // ③ 감사에서 추가
		{"gpt-5.5-pro", 60, 270}, // ③
		{"gpt-5.4-pro", 60, 270}, // ③
		{"gemini-2.5-pro", 2.5, 15},
		{"gemini-3.1-pro-preview", 4, 18},
	} {
		in := CostOf(Usage{Model: tc.model, Input: mtok, InputLong: mtok}, tbl, Mult{}).ByAxis.Input
		if d := in - tc.longIn; d > 1e-9 || d < -1e-9 {
			t.Errorf("%s 롱 입력: got $%v want $%v", tc.model, in, tc.longIn)
		}
		out := CostOf(Usage{Model: tc.model, Output: mtok, OutputLong: mtok}, tbl, Mult{}).ByAxis.Output
		if d := out - tc.longOut; d > 1e-9 || d < -1e-9 {
			t.Errorf("%s 롱 출력: got $%v want $%v", tc.model, out, tc.longOut)
		}
	}

	// 계단이 없는 모델 — 롱 몫을 넣어도 표준가와 같아야 한다(없는 요금을 만들지 않는다).
	for _, m := range []string{"gpt-5.2", "gpt-5.1", "gpt-5", "gpt-5.4-mini", "gpt-4o", "o3",
		"claude-opus-5", "claude-sonnet-5", "gemini-3.6-flash"} {
		std := CostOf(Usage{Model: m, Input: mtok}, tbl, Mult{}).ByAxis.Input
		lng := CostOf(Usage{Model: m, Input: mtok, InputLong: mtok}, tbl, Mult{}).ByAxis.Input
		if d := std - lng; d > 1e-9 || d < -1e-9 {
			t.Errorf("%s 은 계단이 없어야 한다: 표준 $%v vs 롱 $%v", m, std, lng)
		}
	}
}

/*
 * Google — ai.google.dev/gemini-api/docs/pricing (2026-08-13 확인).
 *
 * ⚠ 캐시 **쓰기 토큰** 과금은 없다(cacheWriteMult 0). 다만 공식표에는 명시적 컨텍스트
 *   캐싱의 **시간당 저장 비용**($1.00~$4.50/hr)이 있다 — 토큰 축이 아니라 이 패키지가
 *   표현할 수 없는 축이다. "무과금"이 아니라 "이 모델에 없는 축"이라는 뜻이다.
 */
func TestAudit_Google(t *testing.T) {
	for _, tc := range []struct {
		model   string
		in, out float64
	}{
		{"gemini-3.6-flash", 1.5, 7.5},
		{"gemini-3.5-flash", 1.5, 9},
		{"gemini-3.5-flash-lite", 0.3, 2.5},
		{"gemini-3.1-flash-lite", 0.25, 1.5},
		{"gemini-3.1-pro-preview", 2, 12},
		{"gemini-3.1-pro-preview-customtools", 2, 12},
		{"gemini-3-flash-preview", 0.5, 3},
		{"gemini-2.5-pro", 1.25, 10},
		{"gemini-2.5-flash", 0.3, 2.5},
		{"gemini-2.5-flash-lite", 0.1, 0.4},
	} {
		wantPrice(t, tc.model, tc.in, tc.out)
	}

	const mtok = 1_000_000
	tbl := Pricing(auditDay)
	// 캐시 히트 0.1배 — 공식표의 Cache Read 열과 같다(3.6 flash $0.15 = 0.1 × $1.50).
	for _, tc := range []struct {
		model      string
		wantCached float64
	}{
		{"gemini-3.6-flash", 0.15},
		{"gemini-3.5-flash", 0.15},
		{"gemini-3.5-flash-lite", 0.03},
		{"gemini-2.5-pro", 0.125},
		{"gemini-3.1-pro-preview", 0.20},
	} {
		got := CostOf(Usage{Model: tc.model, CacheRead: mtok}, tbl, Mult{}).ByAxis.CacheRead
		if d := got - tc.wantCached; d > 1e-9 || d < -1e-9 {
			t.Errorf("%s 캐시히트: got $%v want $%v", tc.model, got, tc.wantCached)
		}
	}
}

/*
 * 일부러 등재하지 않은 모델은 **비용에 안 잡히는 대신 이름이 드러나야** 한다.
 * (틀린 숫자보다 없는 숫자가 낫다 — seed_openai.go 머리 주석의 규율.)
 */
func TestAudit_UnpricedModelsStayVisible(t *testing.T) {
	tbl := Pricing(auditDay)
	for _, m := range []string{"gpt-5.4-cyber", "gpt-5-codex", "gpt-5.1-codex",
		"gpt-5.1-codex-mini", "gpt-5.1-codex-max", "codex-auto-review", "gpt-oss-120b",
		"gemini-3-pro-preview", "gemini-3.1-pro"} {
		r := CostOf(Usage{Model: m, Input: 1_000_000}, tbl, Mult{})
		if r.Priced {
			t.Errorf("%s 는 미등재여야 한다(공식 단가가 확인되지 않았다) — priced=true", m)
		}
		if r.USD != 0 {
			t.Errorf("%s: 미등재인데 비용이 붙었다 $%v", m, r.USD)
		}
	}
}

/*
 * ③ 캐시 쓰기의 롱 단가 — 공식 표는 5.6 계열의 Cache writes 를 **두 값**으로 싣는다:
 *      sol   $6.25 / $12.50
 *      terra $2.50 / $5.00
 *      luna  $0.25 / $0.50
 *    앞이 표준 구간(1.25 × 표준입력), 뒤가 롱 구간(1.25 × 롱입력)이다.
 */
func TestAudit_CacheWriteLongTier(t *testing.T) {
	const mtok = 1_000_000
	tbl := Pricing(auditDay)
	for _, tc := range []struct {
		model               string
		wantShort, wantLong float64
	}{
		{"gpt-5.6-sol", 6.25, 12.50},
		{"gpt-5.6-terra", 2.50, 5.00},
		{"gpt-5.6-luna", 0.25, 0.50},
	} {
		short := CostOf(Usage{Model: tc.model, CacheCreate: mtok}, tbl, Mult{}).ByAxis.CacheCreate
		if d := short - tc.wantShort; d > 1e-9 || d < -1e-9 {
			t.Errorf("%s 표준 캐시쓰기: got $%v want $%v", tc.model, short, tc.wantShort)
		}
		// 전량이 롱 구간인 행.
		long := CostOf(Usage{Model: tc.model, CacheCreate: mtok, CacheCreateLong: mtok}, tbl, Mult{}).ByAxis.CacheCreate
		if d := long - tc.wantLong; d > 1e-9 || d < -1e-9 {
			t.Errorf("%s 롱 캐시쓰기: got $%v want $%v", tc.model, long, tc.wantLong)
		}
	}

	// 캐시 쓰기 과금이 없는 공급사는 롱 몫을 넣어도 $0 이어야 한다(없는 요금을 만들지 않는다).
	for _, m := range []string{"gemini-2.5-pro", "gemini-3.1-pro-preview"} {
		got := CostOf(Usage{Model: m, CacheCreate: mtok, CacheCreateLong: mtok}, tbl, Mult{}).ByAxis.CacheCreate
		if got != 0 {
			t.Errorf("%s: Google 은 캐시 쓰기 토큰 과금이 없다 — got $%v", m, got)
		}
	}

	// 무회귀 — CacheCreateLong 이 0 이면 값이 종전과 같아야 한다(차액 항이 0 곱셈이다).
	for _, m := range []string{"claude-opus-5", "gpt-5.6-terra", "gpt-5.5"} {
		a := CostOf(Usage{Model: m, CacheCreate: 12345, CacheCreate5m: 12345}, tbl, Mult{}).ByAxis.CacheCreate
		b := CostOf(Usage{Model: m, CacheCreate: 12345, CacheCreate5m: 12345, CacheCreateLong: 0}, tbl, Mult{}).ByAxis.CacheCreate
		if a != b {
			t.Errorf("%s: CacheCreateLong 0 이 비트를 바꿨다 %v vs %v", m, a, b)
		}
	}
}

/*
 * ④ 은퇴 모델 — 1st-party 에서는 은퇴했지만 Bedrock·GCP 에서 돌아가고 공식 표가 단가를
 *    계속 싣는다. 미등재로 두면 그 세션이 비용에서 통째로 사라진다.
 */
func TestAudit_RetiredModelsArePriced(t *testing.T) {
	for _, tc := range []struct {
		model   string
		in, out float64
	}{
		{"claude-opus-4-1", 15, 75},
		{"claude-opus-4", 15, 75},
		{"claude-sonnet-4", 3, 15},
		{"claude-haiku-3-5", 0.8, 4},
	} {
		wantPrice(t, tc.model, tc.in, tc.out)
		r := CostOf(Usage{Model: tc.model, Input: 1_000_000}, Pricing(auditDay), Mult{})
		if !r.Priced {
			t.Errorf("%s 가 unpriced 다 — 공식 단가가 있는데 비용에서 빠진다", tc.model)
		}
	}
}

/*
 * ① 고속 모드 — 공식 표의 Fast mode 행을 그대로 대조한다.
 *
 *   Anthropic  Claude Opus 5 / Opus 4.8 : 입력 $10 · 출력 $50 (표준 $5/$25 의 2배)
 *   OpenAI     "Fast mode pricing is doubled."
 *
 * 캐시 축도 2배다 — 고속은 **기준 입력가**를 올리고 캐시 배수는 그 위에 얹힌다
 * (공식: "Prompt caching multipliers apply on top of fast mode pricing").
 */
func TestAudit_FastModeDoublesEveryAxis(t *testing.T) {
	const mtok = 1_000_000
	tbl := Pricing(auditDay)

	// 전량 고속인 행: 입력 $10 · 출력 $50 (공식 Fast mode 표와 같아야 한다).
	for _, m := range []string{"claude-opus-5", "claude-opus-4-8"} {
		in := CostOf(Usage{Model: m, Input: mtok, InputFast: mtok}, tbl, Mult{}).ByAxis.Input
		if d := in - 10; d > 1e-9 || d < -1e-9 {
			t.Errorf("%s 고속 입력: got $%v want $10 (공식 Fast mode)", m, in)
		}
		out := CostOf(Usage{Model: m, Output: mtok, OutputFast: mtok}, tbl, Mult{}).ByAxis.Output
		if d := out - 50; d > 1e-9 || d < -1e-9 {
			t.Errorf("%s 고속 출력: got $%v want $50", m, out)
		}
		// 캐시 히트: 0.1 × $10 = $1.00 (표준은 0.1 × $5 = $0.50)
		cr := CostOf(Usage{Model: m, CacheRead: mtok, CacheReadFast: mtok}, tbl, Mult{}).ByAxis.CacheRead
		if d := cr - 1; d > 1e-9 || d < -1e-9 {
			t.Errorf("%s 고속 캐시히트: got $%v want $1.00", m, cr)
		}
		// 5분 캐시 쓰기: 1.25 × $10 = $12.50 (표준은 $6.25)
		cc := CostOf(Usage{Model: m, CacheCreate: mtok, CacheCreate5m: mtok, CacheCreateFast: mtok}, tbl, Mult{}).ByAxis.CacheCreate
		if d := cc - 12.5; d > 1e-9 || d < -1e-9 {
			t.Errorf("%s 고속 5분 캐시쓰기: got $%v want $12.50", m, cc)
		}
	}

	// 절반만 고속인 행 — 표준과 고속의 중간이어야 한다(비례).
	half := CostOf(Usage{Model: "claude-opus-5", Input: mtok, InputFast: mtok / 2}, tbl, Mult{}).ByAxis.Input
	if d := half - 7.5; d > 1e-9 || d < -1e-9 {
		t.Errorf("절반 고속 입력: got $%v want $7.50 (표준 5 + 고속분 2.5)", half)
	}

	// FastTokens 가 보고돼야 한다 — "왜 2배인가"를 화면이 답할 유일한 근거다.
	r := CostOf(Usage{Model: "claude-opus-5", Input: mtok, InputFast: mtok}, tbl, Mult{})
	if r.FastTokens != mtok {
		t.Errorf("FastTokens=%v want %v", r.FastTokens, float64(mtok))
	}

	// 무회귀 — 고속 몫이 0/부재면 비트가 종전과 같아야 한다.
	for _, m := range []string{"claude-opus-5", "claude-sonnet-5", "gpt-5.6-terra", "gemini-3.6-flash"} {
		row := Usage{Model: m, Input: 1234, Output: 5678, CacheRead: 98765, CacheCreate: 4321, CacheCreate5m: 4321}
		a := CostOf(row, tbl, Mult{})
		row.InputFast, row.OutputFast, row.CacheReadFast, row.CacheCreateFast = 0, 0, 0, 0
		b := CostOf(row, tbl, Mult{})
		if a.USD != b.USD {
			t.Errorf("%s: 고속 몫 0 이 비트를 바꿨다 %v vs %v", m, a.USD, b.USD)
		}
		if a.FastTokens != 0 {
			t.Errorf("%s: 고속 몫이 없는데 FastTokens=%v", m, a.FastTokens)
		}
	}

	// 배수 상수가 공식(2배)과 같은지.
	if FastMult != 2.0 {
		t.Errorf("FastMult=%v (공식 표: Opus 5 고속 $10/$50 = 표준의 2배)", FastMult)
	}
}
