package gemini

import "testing"

/*
 * ── 계단(롱컨텍스트) 판정 ─────────────────────────────────────────────────
 *
 * Google 은 한 요청의 입력 컨텍스트가 **200K** 를 넘으면 그 요청의 입력·출력을 함께 롱 단가로
 * 매긴다. OpenAI(272K)와 임계가 다르므로 상수를 각 수집기가 자기 것으로 갖는다 — 하나로
 * 뭉치면 한쪽이 반드시 틀린다.
 */

func TestIsLongRequest_ThresholdIsExclusive(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   int64
		want bool
	}{
		{"임계 미만", 199_999, false},
		{"임계 정확히", 200_000, false}, // 공식은 "longer than 200K" — 같은 값은 표준이다
		{"임계 초과", 200_001, true},
		{"0", 0, false},
	} {
		if got := isLongRequest(tokensRec{Input: tc.in}); got != tc.want {
			t.Errorf("%s(input=%d): got %v want %v", tc.name, tc.in, got, tc.want)
		}
	}
}

// promptTokenCount 는 cachedContentTokenCount 를 **포함**한다 — 그 값으로 판정해야 한다.
func TestIsLongRequest_CountsCachedPrefix(t *testing.T) {
	u := tokensRec{Input: 250_000, Cached: 240_000}
	if netInput(u) != 10_000 {
		t.Fatalf("netInput=%d (10,000 기대) — 전제가 깨졌다", netInput(u))
	}
	if !isLongRequest(u) {
		t.Fatal("캐시된 몫을 빼고 판정했다 — 컨텍스트 길이는 promptTokenCount 다")
	}
}

func TestLongContextThresholdMatchesOfficial(t *testing.T) {
	// ai.google.dev/gemini-api/docs/pricing — pro 계열 계단 임계 200K(2026-08-13 확인).
	if longContextThreshold != 200_000 {
		t.Fatalf("임계 %d (공식 200,000)", longContextThreshold)
	}
	// OpenAI 와 달라야 한다 — 같아지면 한쪽이 틀린 것이다.
	if longContextThreshold == 272_000 {
		t.Fatal("Google 임계가 OpenAI 값(272K)으로 바뀌었다")
	}
}
