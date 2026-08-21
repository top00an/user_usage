package cost

import (
	"strings"
	"testing"
)

/*
 * 로컬 LLM 의 비용 처리를 못박는다 (기획: docs/PLAN-local-llm.md §2.1).
 *
 * 결정된 것: 로컬 모델은 **미등록으로 둔다.** priced=false 로 나가고 이름이 Unpriced 목록에
 * 오른다. 단가 0 으로 등록하지 않고, "클라우드로 했다면 $X" 환산가도 넣지 않는다.
 *
 * 이 파일은 새 동작을 만들지 않는다 — **현행 동작이 로컬 모델명에도 성립함을 고정한다.**
 * 고정해 두는 이유: 나중에 누군가 "로컬은 어차피 0 이니 단가표에 0 으로 넣자"고 생각하는
 * 날이 오는데, 그러면 등록 누락(모르는 클라우드 모델)과 진짜 0(로컬)이 화면에서 같아진다.
 * cost.go 가 경계하는 "조용한 $0" 이 정확히 그것이다.
 */

// localModelIDs — 실제 로컬 런타임이 보고하는 모양의 모델 id 들.
//
// Ollama 는 `<이름>:<태그>`, LM Studio 는 리포지터리 경로 모양을 쓴다. 둘 다 클라우드
// 과금 ID 와 겹치지 않는 형태지만, 겹치지 않음을 **가정으로 두지 않고 테스트로 둔다.**
var localModelIDs = []string{
	"qwen3-coder:30b",
	"qwen2.5-coder:7b-instruct-q4_K_M",
	"deepseek-r1:7b",
	"gemma3:4b",
	"llama3.3:70b",
	"lmstudio-community/Qwen3-Coder-30B-GGUF",
	"hf.co/unsloth/gpt-oss-20b-GGUF:Q4_K_M",
}

func TestLocalModels_StayUnpricedAndKeepTheirName(t *testing.T) {
	useConfig(t, `{}`)
	tbl := Pricing(day("2026-08-21"))

	for _, m := range localModelIDs {
		c := CostOf(Usage{Model: m, Input: 1e6, Output: 1e6, CacheRead: 1e6}, tbl, Mult{})
		if c.Priced {
			t.Errorf("%q 에 단가가 붙었다 — 로컬 모델은 미등록이어야 한다", m)
		}
		if c.USD != 0 {
			t.Errorf("%q: usd = %v, want 0", m, c.USD)
		}
		/*
		 * 이름을 잃으면 화면이 "무엇이 비용에서 빠졌는지"를 말할 수 없다.
		 *
		 * ⚠ 비교가 EqualFold 인 이유(실측): Result.Model 은 원문이 아니라 **정규화된
		 *   이름**이다. NormalizeModel 이 단가표 조회를 위해 소문자로 낮추고, 그 값이
		 *   그대로 Model 에 실린다. 그래서 대소문자가 섞인 로컬 id
		 *   (`lmstudio-community/Qwen3-Coder-30B-GGUF`)는 화면의 미등록 목록에
		 *   **소문자로** 뜬다. 식별에는 문제가 없어 고치지 않고 사실로 고정한다 —
		 *   원문 보존으로 바꾸면 조회 키와 표시 이름이 갈라져 두 벌이 된다.
		 */
		if !strings.EqualFold(c.Model, m) {
			t.Errorf("%q: model = %q — 이름이 바뀌었다", m, c.Model)
		}
	}
}

// 미등록 모델은 합계를 오염시키지 않고 목록에 이름을 남긴다 — 로컬 모델도 같다.
func TestLocalModels_ExcludedFromTotalButListed(t *testing.T) {
	useConfig(t, `{}`)

	priced := Usage{Model: "claude-opus-5", Output: 1e6}
	base := Summarize([]Usage{priced})

	withLocal := Summarize([]Usage{
		priced,
		{Model: "qwen3-coder:30b", Input: 5e8, Output: 5e8},
		{Model: "qwen3-coder:30b", Output: 1e8}, // 중복은 한 번만 오른다
	})

	if withLocal.USD != base.USD {
		t.Errorf("로컬 세션이 합계를 움직였다: %v → %v", base.USD, withLocal.USD)
	}
	if len(withLocal.Unpriced) != 1 || withLocal.Unpriced[0] != "qwen3-coder:30b" {
		t.Errorf("unpriced = %v, want [qwen3-coder:30b]", withLocal.Unpriced)
	}
}

/*
 * 정규화가 로컬 모델명을 클라우드 단가로 끌고 가지 않는지 본다.
 *
 * NormalizeModel 은 변형 접미사(-thinking · -high · -medium · -low)를 한 겹 벗긴다. 로컬
 * 모델을 그런 이름으로 서빙하는 경우(`qwen3-thinking`)에도 벗긴 결과가 단가표에 없어야
 * 한다 — 벗기기가 우연히 등록된 이름에 착지하면 로컬 사용량에 클라우드 단가가 붙는다.
 */
func TestLocalModels_VariantStrippingDoesNotLandOnAPricedID(t *testing.T) {
	useConfig(t, `{}`)
	tbl := Pricing(day("2026-08-21"))

	for _, m := range []string{"qwen3-thinking", "qwen3-coder-high", "deepseek-r1-low", "gemma3-medium"} {
		if c := CostOf(Usage{Model: m, Output: 1e9}, tbl, Mult{}); c.Priced {
			t.Errorf("%q → 정규화 후 %q 에 단가가 붙었다", m, NormalizeModel(m))
		}
	}
}

/*
 * ⚠ 이 테스트는 **한계를 고정한다.** 통과하는 것이 좋은 상태라는 뜻이 아니다.
 *
 * 모델명만으로는 로컬과 클라우드를 가를 수 없다. 같은 id 면 같은 비용이 나온다 — 로컬에서
 * 돌렸는지 여부는 이 계층에 들어오는 데이터에 **적혀 있지 않기 때문**이다.
 * seed_openai.go 가 gpt-oss-120b 를 미등록으로 둔 이유가 정확히 이것이고("어디서 돌렸는지를
 * 사용량 레코드가 말해 주지 않는다"), 로컬 LLM 은 그 사례를 예외에서 중심으로 옮긴다.
 *
 * 해결은 이 패키지가 아니라 **수집기가 locality 를 기록하는 것**이다
 * (docs/PLAN-local-llm.md §2.2 의 payload.Session.Runtime). 그것이 들어오면 이 테스트를
 * 지우고 "로컬은 클라우드 단가를 쓰지 않는다"로 바꿔 쓰는 것이 맞다.
 */
func TestLocalityIsNotInferableFromModelName(t *testing.T) {
	useConfig(t, `{}`)
	tbl := Pricing(day("2026-08-21"))

	// 로컬에서 돌렸다고 가정한 세션과 클라우드 세션이 **구별되지 않는다**.
	asLocal := CostOf(Usage{Model: "claude-opus-5", Output: 1e6}, tbl, Mult{})
	asCloud := CostOf(Usage{Model: "claude-opus-5", Output: 1e6}, tbl, Mult{})

	if asLocal.USD != asCloud.USD {
		t.Fatal("같은 모델 id 가 다른 비용을 냈다 — 이 계층에 locality 입력이 없으므로 불가능하다")
	}
	if !asLocal.Priced {
		t.Fatal("전제가 깨졌다: claude-opus-5 는 단가표에 있어야 한다")
	}
	// 이 자리에 남기는 사실: locality 는 데이터에 적혀야 하고, 추론할 수 없다.
}
