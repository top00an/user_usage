package intake

import "testing"

/*
 * platform 필드 — **선택적**이다.
 *
 * intake 는 값을 좁히기만 하고 판정하지 않는다: 허용목록 판정(claude|codex|gemini, 그 밖은
 * other)은 저장 계층(store.NormalizePlatform)이 단일 출처로 갖는다. 여기서 한 번 더 접으면
 * 규칙이 두 벌이 되고, 언젠가 한쪽만 고쳐진다.
 *
 * 그래서 여기서 재는 것은 두 가지뿐이다: 보고된 값이 **그대로 실려 오는가**, 안 보냈을 때
 * **nil 인가**(빈 문자열이 아니다 — "안 보냈다"와 "빈 값을 보냈다"는 다른 사실이다).
 */

func TestNormSessionCarriesPlatform(t *testing.T) {
	s, ok := NormSession(map[string]any{
		"id":       "abcdefgh-1234",
		"platform": "codex",
	})
	if !ok {
		t.Fatal("세션이 통째로 버려졌다")
	}
	if s.Platform == nil || *s.Platform != "codex" {
		t.Fatalf("platform = %v", s.Platform)
	}
}

func TestNormSessionPlatformAbsentIsNil(t *testing.T) {
	// 현행 수집기의 보고 — platform 키가 아예 없다.
	s, ok := NormSession(map[string]any{"id": "abcdefgh-1234"})
	if !ok {
		t.Fatal("세션이 통째로 버려졌다")
	}
	if s.Platform != nil {
		t.Fatalf("안 보낸 값이 %q 로 지어졌다", *s.Platform)
	}
	// 빈 문자열도 "안 보냈다"와 같게 접는다(clip → nilIfEmpty).
	s2, _ := NormSession(map[string]any{"id": "abcdefgh-1234", "platform": "   "})
	if s2.Platform != nil {
		t.Fatalf("빈 값이 %q 가 됐다", *s2.Platform)
	}
}

// 알 수 없는 값도 **여기서는 버리지 않는다** — 서버보다 새로운 수집기의 보고가 통째로
// 사라지면 안 된다. 좁히는 일은 저장 계층이 한다.
func TestNormSessionKeepsUnknownPlatformVerbatim(t *testing.T) {
	s, _ := NormSession(map[string]any{"id": "abcdefgh-1234", "platform": "grok"})
	if s.Platform == nil || *s.Platform != "grok" {
		t.Fatalf("platform = %v", s.Platform)
	}
}

func TestNormPayloadCarriesPlatform(t *testing.T) {
	p, err := NormPayload([]byte(`{"sessions":[{"id":"abcdefgh-1234","platform":"gemini"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Sessions) != 1 {
		t.Fatalf("세션 수 = %d", len(p.Sessions))
	}
	if p.Sessions[0].Platform == nil || *p.Sessions[0].Platform != "gemini" {
		t.Fatalf("platform = %v", p.Sessions[0].Platform)
	}
}
