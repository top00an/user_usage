package transcript

import (
	"strings"
	"testing"

	"github.com/tscorp/user-usage/collector/internal/payload"
)

// mustParse 는 jsonl 원문을 누적기에 흘려 세션 목록을 돌려준다(aggregate 의 문자열 판).
func mustParse(t *testing.T, stem, src string) []payload.Session {
	t.Helper()
	return aggregate(t, stem, strings.Split(src, "\n")...)
}

/*
 * ── 고속 모드(usage.speed) 분리 ───────────────────────────────────────────
 *
 * 고속 모드는 같은 모델에 **2배 단가**가 붙는다(Claude Opus 5 고속 $10/$50 vs 표준 $5/$25,
 * 캐시 배수는 그 위에 얹힌다). 그래서 수집기가 그 몫을 갈라 보내야 하고, 안 보내면 서버가
 * 전부 표준가로 계산해 **비용이 절반**으로 나온다.
 *
 * 이 파일이 지키는 것 넷:
 *   ① "fast" 인 턴의 토큰이 fast 몫으로 들어간다(총량에서 빼지 않는다 — 부분집합이다).
 *   ② "standard"·빈 값·모르는 값은 표준이다(모르는 값을 고속으로 접으면 없는 청구를 만든다).
 *   ③ 세션 합계와 버킷(시간 뷰)이 **같은 값**이다 — 갈리면 두 화면이 다른 비용을 말한다.
 *   ④ 캐시 축도 포함된다 — 이 워크로드에서 캐시가 토큰의 대부분이라 누락이 가장 크다.
 */

// line 은 assistant 턴 한 줄을 만든다. speed 가 빈 문자열이면 필드를 아예 넣지 않는다
// (구버전 Claude Code 가 그 필드를 안 싣는 상황을 재현한다).
func line(ts, model, speed string, in, out, cr, cc int64) string {
	sp := ""
	if speed != "" {
		sp = `,"speed":"` + speed + `"`
	}
	return `{"type":"assistant","timestamp":"` + ts + `","message":{"role":"assistant","model":"` + model + `",` +
		`"usage":{"input_tokens":` + itoa(in) + `,"output_tokens":` + itoa(out) +
		`,"cache_read_input_tokens":` + itoa(cr) + `,"cache_creation_input_tokens":` + itoa(cc) + sp + `}}}`
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

func TestFastMode_SplitsPortionAndKeepsTotals(t *testing.T) {
	// 같은 시각·같은 모델이라 버킷 하나로 모인다 — 세션 합계와 버킷을 바로 대조할 수 있다.
	src := strings.Join([]string{
		line("2026-08-13T09:00:00.000Z", "claude-opus-5", "standard", 100, 200, 300, 40),
		line("2026-08-13T09:10:00.000Z", "claude-opus-5", "fast", 10, 20, 30, 4),
	}, "\n")

	sessions := mustParse(t, "fast-sess-0001", src)
	if len(sessions) != 1 {
		t.Fatalf("세션 %d개", len(sessions))
	}
	s := sessions[0]

	// ① 총량은 둘을 합친 값이다(고속 몫을 빼지 않는다).
	if s.Input != 110 || s.Output != 220 || s.CacheRead != 330 || s.CacheCreate != 44 {
		t.Fatalf("총량이 부분집합 규칙을 안 지킨다: in=%d out=%d cr=%d cc=%d (기대 110/220/330/44)",
			s.Input, s.Output, s.CacheRead, s.CacheCreate)
	}
	// ①④ 고속 몫은 fast 턴의 값만 — 캐시 축까지 포함한다.
	if s.InputFast != 10 || s.OutputFast != 20 || s.CacheReadFast != 30 || s.CacheCreateFast != 4 {
		t.Fatalf("고속 몫: in=%d out=%d cr=%d cc=%d (기대 10/20/30/4)",
			s.InputFast, s.OutputFast, s.CacheReadFast, s.CacheCreateFast)
	}
	// 불변식 — 몫은 총량을 넘지 않는다(서버 intake 가 접기 전에 여기서 이미 맞아야 한다).
	if s.InputFast > s.Input || s.OutputFast > s.Output ||
		s.CacheReadFast > s.CacheRead || s.CacheCreateFast > s.CacheCreate {
		t.Fatalf("몫이 총량을 넘었다: %+v", s)
	}

	// ③ 버킷 합이 세션 합계와 같아야 한다 — 갈리면 시간 뷰와 좌석 뷰가 다른 비용을 말한다.
	var bIn, bOut, bCr, bCc int64
	for _, b := range s.Series {
		bIn += b.InputFast
		bOut += b.OutputFast
		bCr += b.CacheReadFast
		bCc += b.CacheCreateFast
	}
	if bIn != s.InputFast || bOut != s.OutputFast || bCr != s.CacheReadFast || bCc != s.CacheCreateFast {
		t.Fatalf("버킷 합 != 세션 합계: 버킷 %d/%d/%d/%d vs 세션 %d/%d/%d/%d",
			bIn, bOut, bCr, bCc, s.InputFast, s.OutputFast, s.CacheReadFast, s.CacheCreateFast)
	}
}

/*
 * ② 모르는 값은 **표준**이다.
 *
 * 방향이 중요하다: 모르는 값을 고속으로 접으면 비용을 2배로 지어내게 된다(없는 청구를 만든다).
 * 과소보다 과대가 더 나쁜 자리다 — 그래서 "fast" 와 정확히 일치할 때만 고속으로 센다.
 */
func TestFastMode_UnknownSpeedIsStandard(t *testing.T) {
	for _, speed := range []string{"standard", "", "FAST", "fast-beta", "turbo", "1"} {
		src := line("2026-08-13T09:00:00.000Z", "claude-opus-5", speed, 100, 200, 300, 40)
		sessions := mustParse(t, "spd-"+strings.NewReplacer("", "x").Replace(speed)+"-0001", src)
		if len(sessions) != 1 {
			t.Fatalf("speed=%q: 세션 %d개", speed, len(sessions))
		}
		s := sessions[0]
		if s.InputFast != 0 || s.OutputFast != 0 || s.CacheReadFast != 0 || s.CacheCreateFast != 0 {
			t.Errorf("speed=%q 를 고속으로 셌다 — 모르는 값은 표준이어야 한다: %+v", speed, s)
		}
		// 총량은 그대로여야 한다(고속 판정과 무관하다).
		if s.Input != 100 || s.CacheRead != 300 {
			t.Errorf("speed=%q: 총량이 변했다 in=%d cr=%d", speed, s.Input, s.CacheRead)
		}
	}
}

// 전부 고속인 세션 — 몫이 총량과 같아야 한다(서버가 전 축에 2배를 매기는 경우).
func TestFastMode_AllFastMeansPortionEqualsTotal(t *testing.T) {
	src := strings.Join([]string{
		line("2026-08-13T09:00:00.000Z", "claude-opus-5", "fast", 100, 200, 300, 40),
		line("2026-08-13T10:00:00.000Z", "claude-opus-5", "fast", 1, 2, 3, 4),
	}, "\n")
	sessions := mustParse(t, "allfast-0001", src)
	s := sessions[0]
	if s.InputFast != s.Input || s.OutputFast != s.Output ||
		s.CacheReadFast != s.CacheRead || s.CacheCreateFast != s.CacheCreate {
		t.Fatalf("전부 고속인데 몫 != 총량: %+v", s)
	}
	// 시각이 달라 버킷이 둘이다 — 둘 다 고속으로 갈라졌는지 본다.
	if len(s.Series) != 2 {
		t.Fatalf("버킷 %d개(2 기대)", len(s.Series))
	}
	for _, b := range s.Series {
		if b.InputFast != b.Input {
			t.Errorf("버킷 %s: 고속 몫 %d != 총량 %d", b.Hour, b.InputFast, b.Input)
		}
	}
}
