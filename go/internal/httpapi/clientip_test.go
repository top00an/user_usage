package httpapi

import (
	"net/http"
	"testing"
)

// clientIP 는 rate-limit 키다. ALB 같은 신뢰 프록시 뒤에서 단일 버킷으로 붕괴하지 않게
// USAGE_TRUSTED_PROXY_COUNT(홉 수)로 X-Forwarded-For 에서 실클라이언트를 뽑는다.
// 기본(count=0)은 XFF 를 완전히 무시하고 RemoteAddr 만 쓴다 — 현행 동작과 100% 동일.
func TestClientIP(t *testing.T) {
	const remote = "203.0.113.9" // RemoteAddr 의 host(=폴백 기대값)

	req := func(xff string) *http.Request {
		r := &http.Request{
			RemoteAddr: remote + ":54321",
			Header:     http.Header{},
		}
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}

	cases := []struct {
		name  string
		count int
		xff   string
		want  string
	}{
		// count=0 — 현행 그대로. XFF 가 있어도 절대 보지 않는다(하위호환).
		{"count0 no xff", 0, "", remote},
		{"count0 ignores xff", 0, "1.1.1.1", remote},
		{"count0 ignores multi xff", 0, "1.1.1.1, 10.0.0.5", remote},

		// count=1 — ALB 단독. 실클라이언트 = parts[len-1](마지막 항목).
		{"count1 single", 1, "1.1.1.1", "1.1.1.1"},
		{"count1 last of two", 1, "1.1.1.1, 10.0.0.5", "10.0.0.5"},
		{"count1 no xff falls back", 1, "", remote},

		// count=2 — 실클라이언트 = parts[len-2].
		{"count2 picks len-2", 2, "1.1.1.1, 2.2.2.2, 3.3.3.3", "2.2.2.2"},

		// 위조·오설정: len(parts) < N → RemoteAddr 폴백(공유버킷보다 안전).
		{"count2 too few parts falls back", 2, "1.1.1.1", remote},
		{"count3 too few parts falls back", 3, "1.1.1.1, 2.2.2.2", remote},

		// 뽑은 값이 유효 IP 가 아니면 폴백.
		{"count1 non-ip falls back", 1, "not-an-ip", remote},
		{"count2 non-ip at slot falls back", 2, "bad, 9.9.9.9", remote},
		{"count1 empty slot falls back", 1, "  ", remote},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := clientIP(req(tc.xff), tc.count)
			if got != tc.want {
				t.Fatalf("clientIP(count=%d, xff=%q) = %q, want %q", tc.count, tc.xff, got, tc.want)
			}
		})
	}
}

// RemoteAddr 에 포트가 없어도(SplitHostPort 실패) count=0 은 원문 그대로 돌려준다.
func TestClientIPRemoteAddrWithoutPort(t *testing.T) {
	r := &http.Request{RemoteAddr: "unixsocket", Header: http.Header{}}
	if got := clientIP(r, 0); got != "unixsocket" {
		t.Fatalf("clientIP = %q, want %q", got, "unixsocket")
	}
}
