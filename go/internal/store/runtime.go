package store

import "strings"

/*
 * ── runtime 축 ────────────────────────────────────────────────────────
 *
 * 무엇을 재는 축인가: 이 세션이 **어디서 돌았나**다(`cloud` | `local`). platform 과 다르다 —
 * platform 은 "어느 도구"이고, 같은 도구가 클라우드 모델도 로컬 모델도 물 수 있다.
 *
 * 왜 platform 에 섞지 않는가: `codex-local` 같은 합성 값을 만들면 platform 이 "도구를 재는
 * 축"이라는 정의가 깨지고, 플랫폼별 수집 범위 지원표가 통째로 거짓이 된다. 두 개념은
 * 서로 직교하므로 축을 하나 더 둔다(docs/PLAN-local-llm.md §2.3).
 *
 * 왜 필요한가: 로컬 모델은 API 과금이 $0 이고 단가표에 없어 비용에서 빠진다. 그런데
 * **데이터에 로컬이라고 적힌 자리가 없으면** "우리 작업의 얼마가 로컬로 옮겨갔나"에 답할 수
 * 없고, 더 나쁘게는 로컬 모델 이름이 클라우드 과금 ID 와 겹치는 날 쓰지 않은 비용이 계산된다.
 * `cost/seed_openai.go` 가 gpt-oss-120b 를 미등록으로 둔 이유가 정확히 그것이다 —
 * "어디서 돌렸는지를 사용량 레코드가 말해 주지 않는다."
 *
 * ⚠ **runtime 은 격리 축이 아니다.** 테넌트 격리는 종전대로 tenant_id + RLS 가 한다.
 *   조회 필터일 뿐이므로 권한 판정에 쓰면 안 된다(platform 과 같은 경고).
 */

// Runtimes 는 허용 runtime 식별자다. 클라이언트가 보고하는 값이라 서버가 반드시 좁힌다
// (Platforms·CounterKinds 와 같은 규율).
var Runtimes = []string{"cloud", "local"}

const (
	/*
	 * RuntimeDefault — 미지정 보고의 runtime.
	 *
	 * **이 기본값이 하위호환의 전부다.** 구버전 수집기는 이 필드를 보내지 않고, 현행
	 * 수집기도 판정에 실패하면 보내지 않는다. 여기서 채우지 않으면 과거 데이터와 앞으로의
	 * 보고가 다른 축(빈 값 vs cloud)으로 갈린다.
	 *
	 * ⚠ 이 기본값은 **"클라우드였다"는 관측이 아니다.** "로컬이라는 표시가 없다"는 뜻이다.
	 *   압도적 다수가 실제로 클라우드이므로 그 이름을 쓰지만, 화면이 이 값을 "확인된
	 *   클라우드"로 말하면 과장이다.
	 */
	RuntimeDefault = "cloud"
)

/*
 * NormalizeRuntime 은 보고된 값을 저장 가능한 식별자로 좁힌다.
 * 빈 값 → cloud, 허용목록 → 그대로, 그 밖 → **cloud**. 빈 문자열을 돌려주지 않는다.
 *
 * platform 과 **다른 점이 하나** 있다: 허용목록 밖 값을 위한 제3의 값(`other`)을 두지 않는다.
 *
 * 왜: platform 의 `other` 는 "우리가 모르는 **도구**가 있다"를 화면에 보이게 하려고 둔
 * 자리다. 도구는 계속 늘어나므로 그 신호가 값을 한다. 반면 runtime 은 이분법이고 늘어날
 * 이유가 없다 — 제3의 값을 두면 화면에 영원히 비어 있는 칸이 생긴다. 모르는 값이 오면
 * 그것은 "로컬이라는 표시가 없다"와 실질적으로 같으므로 기본값으로 접는다.
 */
func NormalizeRuntime(raw string) string {
	r := strings.ToLower(strings.TrimSpace(raw))
	if r == "" {
		return RuntimeDefault
	}
	for _, v := range Runtimes {
		if v == r {
			return r
		}
	}
	return RuntimeDefault
}

// IsRuntimeFilter 는 조회 필터 값으로 받아도 되는지 본다.
//
// 필터에서는 **정규화하지 않는다.** 오타(`locl`)를 cloud 로 접으면 요청한 것과 다른 집합이
// 조용히 돌아온다 — 호출부가 400 으로 되돌려주는 편이 정직하다(platform 과 같은 규율).
func IsRuntimeFilter(s string) bool {
	for _, v := range Runtimes {
		if v == s {
			return true
		}
	}
	return false
}
