package intake

// 계단(롱컨텍스트) 분리분의 인테이크 — 신뢰 경계에서의 파싱과 불변식 방어.
//
// 여기가 보는 것:
//   ① 세 필드가 세션·버킷 **양쪽**에서 읽힌다.
//   ② 없으면 0 이고, 기존 페이로드는 아무것도 달라지지 않는다.
//   ③ 불변식(0 <= long <= 총량) 위반은 접되 **조용히 접지 않는다** — 개수가 남는다.

import (
	"encoding/json"
	"testing"
)

func normOne(t *testing.T, body string) Session {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("fixture 가 JSON 이 아니다: %v", err)
	}
	s, ok := NormSession(m)
	if !ok {
		t.Fatalf("세션이 통째로 버려졌다: %s", body)
	}
	return s
}

// ① 세션 축.
func TestLong_SessionFieldsAreParsed(t *testing.T) {
	s := normOne(t, `{
		"id":"11111111-2222-3333-4444-555555555555",
		"input":1000,"output":500,"cacheRead":800,
		"inputLong":300,"outputLong":200,"cacheReadLong":100
	}`)

	// 총량은 **의미 불변** — 여전히 전체 합계다.
	if s.Input != 1000 || s.Output != 500 || s.CacheRead != 800 {
		t.Fatalf("총량이 롱 몫만큼 깎였다: %+v", s)
	}
	if s.InputLong != 300 || s.OutputLong != 200 || s.CacheReadLong != 100 {
		t.Fatalf("롱 몫 = %d/%d/%d, want 300/200/100",
			s.InputLong, s.OutputLong, s.CacheReadLong)
	}
	if s.LongClamped != 0 {
		t.Fatalf("정상 페이로드가 위반으로 세어졌다: %d", s.LongClamped)
	}
}

// ① 버킷 축. 세션만 배선하고 버킷을 빠뜨리면 시간 뷰의 비용만 조용히 틀린다.
func TestLong_BucketFieldsAreParsed(t *testing.T) {
	s := normOne(t, `{
		"id":"11111111-2222-3333-4444-555555555555",
		"series":[{"hour":"2026-08-10T09","model":"gemini-2.5-pro",
			"input":1000,"output":500,"cacheRead":800,
			"inputLong":300,"outputLong":200,"cacheReadLong":100}]
	}`)
	if len(s.Series) != 1 {
		t.Fatalf("버킷 수 = %d", len(s.Series))
	}
	b := s.Series[0]
	if b.Input != 1000 || b.Output != 500 || b.CacheRead != 800 {
		t.Fatalf("버킷 총량이 깎였다: %+v", b)
	}
	if b.InputLong != 300 || b.OutputLong != 200 || b.CacheReadLong != 100 {
		t.Fatalf("버킷 롱 몫 = %d/%d/%d, want 300/200/100",
			b.InputLong, b.OutputLong, b.CacheReadLong)
	}
}

// ② 없으면 0 — 기존 수집기의 페이로드가 그대로 동작한다.
func TestLong_AbsentIsZeroAndNotAViolation(t *testing.T) {
	s := normOne(t, `{
		"id":"11111111-2222-3333-4444-555555555555",
		"input":1000,"output":500,"cacheRead":800,
		"series":[{"hour":"2026-08-10T09","model":"claude-opus-5","input":10,"output":5}]
	}`)
	if s.InputLong != 0 || s.OutputLong != 0 || s.CacheReadLong != 0 {
		t.Fatalf("부재인데 롱 몫이 생겼다: %+v", s)
	}
	if s.LongClamped != 0 {
		t.Fatalf("부재를 위반으로 셌다: %d — 구버전 수집기 전량이 로그에 찍힌다", s.LongClamped)
	}
	if s.Series[0].InputLong != 0 || s.Series[0].LongClamped != 0 {
		t.Fatalf("버킷 부재 처리가 다르다: %+v", s.Series[0])
	}
}

/*
 * ③ 불변식 위반 — 접되 세어 남긴다.
 *
 * "조용히 접지 마라"가 이 축의 계약이다. 접기만 하면 수집기의 계산 버그가 서버에서 정상값으로
 * 둔갑하고, 그 뒤로는 아무도 비용이 틀렸다는 것을 알 수 없다.
 */
func TestLong_ViolationsAreClampedAndCounted(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantIn  int64
		wantOut int64
		wantCR  int64
		clamped int
	}{
		{
			name: "총량 초과",
			body: `{"id":"11111111-2222-3333-4444-555555555555",
				"input":100,"output":50,"cacheRead":80,
				"inputLong":999,"outputLong":50,"cacheReadLong":81}`,
			wantIn: 100, wantOut: 50, wantCR: 80, clamped: 2, // input·cacheRead 만 위반(output 은 경계값)
		},
		{
			name: "음수",
			body: `{"id":"11111111-2222-3333-4444-555555555555",
				"input":100,"output":50,"cacheRead":80,
				"inputLong":-1,"outputLong":-999,"cacheReadLong":0}`,
			wantIn: 0, wantOut: 0, wantCR: 0, clamped: 2,
		},
		{
			name: "총량이 0 인데 롱 몫이 있다",
			body: `{"id":"11111111-2222-3333-4444-555555555555",
				"inputLong":500}`,
			wantIn: 0, wantOut: 0, wantCR: 0, clamped: 1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := normOne(t, c.body)
			if s.InputLong != c.wantIn || s.OutputLong != c.wantOut || s.CacheReadLong != c.wantCR {
				t.Fatalf("접힌 값 = %d/%d/%d, want %d/%d/%d",
					s.InputLong, s.OutputLong, s.CacheReadLong, c.wantIn, c.wantOut, c.wantCR)
			}
			if s.LongClamped != c.clamped {
				t.Fatalf("위반 수 = %d, want %d — 조용히 접혔다", s.LongClamped, c.clamped)
			}
		})
	}
}

// 버킷의 위반은 세션 카운트로 올라간다. 안 올라가면 로그가 세션 단위라 버킷 위반이 사라진다.
func TestLong_BucketViolationsRollUpToSession(t *testing.T) {
	s := normOne(t, `{
		"id":"11111111-2222-3333-4444-555555555555",
		"series":[
			{"hour":"2026-08-10T09","model":"m1","input":10,"inputLong":99},
			{"hour":"2026-08-10T10","model":"m1","output":10,"outputLong":-5}
		]
	}`)
	if s.Series[0].LongClamped != 1 || s.Series[1].LongClamped != 1 {
		t.Fatalf("버킷별 위반 수 = %d/%d", s.Series[0].LongClamped, s.Series[1].LongClamped)
	}
	if s.LongClamped != 2 {
		t.Fatalf("세션 합계 위반 수 = %d, want 2", s.LongClamped)
	}
}

// 본문 전체의 위반 수가 Payload 로 올라간다 — 호출부가 한 줄로 로그를 낼 수 있어야 한다.
func TestLong_PayloadAggregatesViolations(t *testing.T) {
	p, err := NormPayload([]byte(`{"sessions":[
		{"id":"11111111-2222-3333-4444-555555555555","input":10,"inputLong":99},
		{"id":"22222222-3333-4444-5555-666666666666","output":10,"outputLong":99},
		{"id":"33333333-4444-5555-6666-777777777777","input":10,"inputLong":5}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Sessions) != 3 {
		t.Fatalf("세션 수 = %d", len(p.Sessions))
	}
	if p.LongClamped != 2 {
		t.Fatalf("페이로드 위반 수 = %d, want 2", p.LongClamped)
	}
}

// 문자열 숫자·bool 도 다른 축과 같은 JS 의미론으로 읽는다(수집기 버전 드리프트 흡수).
// 비수치 쓰레기는 "값 없음"이지 위반이 아니다 — 위반으로 세면 신호가 죽는다.
func TestLong_JSValueSemantics(t *testing.T) {
	s := normOne(t, `{"id":"11111111-2222-3333-4444-555555555555",
		"input":1000,"output":1000,"cacheRead":1000,
		"inputLong":"300","outputLong":true,"cacheReadLong":{"nope":1}}`)
	if s.InputLong != 300 {
		t.Fatalf(`inputLong "300" = %d, want 300`, s.InputLong)
	}
	if s.OutputLong != 1 {
		t.Fatalf("outputLong true = %d, want 1", s.OutputLong)
	}
	if s.CacheReadLong != 0 {
		t.Fatalf("객체 = %d, want 0", s.CacheReadLong)
	}
	if s.LongClamped != 0 {
		t.Fatalf("비수치를 위반으로 셌다: %d", s.LongClamped)
	}
}
