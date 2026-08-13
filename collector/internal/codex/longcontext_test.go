package codex

import "testing"

/*
 * ── 계단(롱컨텍스트) 판정 ─────────────────────────────────────────────────
 *
 * OpenAI 는 한 요청의 입력 컨텍스트가 **272K** 를 넘으면 그 요청 전체를 롱 단가로 매긴다
 * (입력 2배 · 출력 1.5배). 판정은 요청 단위를 보는 수집기만 할 수 있다 — 서버는 집계된
 * 행을 받으므로 "하루 합계가 272K"와 "한 요청이 272K"를 구분할 수 없다.
 *
 * 이 파일이 지키는 것 셋:
 *   ① 기준이 **input_tokens(캐시 포함)** 다. netInput(캐시 뺀 값)으로 재면 안 된다.
 *   ② 경계가 **초과**다(272,000 은 롱이 아니고 272,001 이 롱이다).
 *   ③ 임계 상수가 공식 값이다.
 */

func TestIsLongRequest_ThresholdIsExclusive(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   int64
		want bool
	}{
		{"임계 미만", 271_999, false},
		{"임계 정확히", 272_000, false}, // 공식은 "longer than 272K" — 같은 값은 표준이다
		{"임계 초과", 272_001, true},
		{"한참 초과", 1_000_000, true},
		{"0", 0, false},
	} {
		got := isLongRequest(tokenUsage{Input: tc.in})
		if got != tc.want {
			t.Errorf("%s(input=%d): got %v want %v", tc.name, tc.in, got, tc.want)
		}
	}
}

/*
 * ① 캐시된 프리픽스도 컨텍스트 길이에 들어간다.
 *
 * 이것이 이 판정에서 가장 틀리기 쉬운 자리다. netInput(= input − cached)으로 재면 캐시가 많은
 * 세션이 임계를 못 넘는 것으로 잘못 판정되고, 실측 워크로드에서는 캐시읽기가 입력의 90% 이상
 * 이라 그 오판이 사실상 상시화된다 — 즉 계단이 **거의 항상 누락**된다.
 */
func TestIsLongRequest_CountsCachedPrefix(t *testing.T) {
	// 순입력은 1만뿐이지만 캐시된 프리픽스까지 합치면 30만이다 → 롱이다.
	u := tokenUsage{Input: 300_000, Cached: 290_000, Output: 500}
	if netInput(u) != 10_000 {
		t.Fatalf("netInput=%d (10,000 기대) — 전제가 깨졌다", netInput(u))
	}
	if !isLongRequest(u) {
		t.Fatal("캐시된 프리픽스를 빼고 판정했다 — 컨텍스트 길이는 input_tokens 다")
	}
}

func TestLongContextThresholdMatchesOfficial(t *testing.T) {
	// developers.openai.com/api/docs/pricing — 계단 임계 272K(2026-08-13 확인).
	if longContextThreshold != 272_000 {
		t.Fatalf("임계 %d (공식 272,000)", longContextThreshold)
	}
}
