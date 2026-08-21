package runtime

import "testing"

func TestIsLocal_LoopbackAndPrivate(t *testing.T) {
	local := []string{
		// loopback — 가장 흔한 모양들
		"http://127.0.0.1:11434/v1",
		"http://localhost:1234/v1",
		"https://localhost",
		"127.0.0.1:11434",
		"localhost",
		"http://[::1]:1234/v1",
		"[::1]:1234",
		"http://127.0.0.53",
		// 사설 대역 — 사내 GPU 서버가 여기 있다
		"http://10.0.0.5:8000/v1",
		"http://172.16.31.9:8080",
		"http://172.31.255.254",
		"http://192.168.1.42:11434",
		"http://[fc00::1]:8080",
		// 링크로컬
		"http://169.254.10.1",
		"http://[fe80::1]",
		// 로컬에 바인딩된 서버를 가리키는 표기
		"http://0.0.0.0:11434",
		// 라우팅되지 않는 이름
		"http://gpu-box:11434",
		"gpu-box",
		"http://workstation.local:1234",
		"http://llm.internal/v1",
		"http://rig.lan:8080",
		"http://host.docker.internal:11434",
		"http://api.localhost:3000",
	}
	for _, e := range local {
		if !IsLocal(e) {
			t.Errorf("IsLocal(%q) = false, want true", e)
		}
		if got := Of(e); got != Local {
			t.Errorf("Of(%q) = %q, want %q", e, got, Local)
		}
	}
}

// 공인 주소는 로컬이 아니다. 사설 대역의 **바로 밖** 주소를 함께 두어 경계를 못박는다 —
// 대역 판정을 손으로 계산하면 정확히 이 자리에서 틀린다.
func TestIsLocal_PublicIsNotLocal(t *testing.T) {
	notLocal := []string{
		"https://api.anthropic.com",
		"https://api.openai.com/v1",
		"https://generativelanguage.googleapis.com",
		"https://openrouter.ai/api/v1",
		"http://8.8.8.8",
		// 172.16/12 의 위·아래 경계 바로 밖
		"http://172.32.0.1",
		"http://172.15.255.1",
		// 10/8 바로 밖
		"http://11.0.0.1",
		// 192.168 과 한 글자 차이
		"http://193.168.1.1",
		// 이름이 로컬처럼 보여도 공개 도메인이다
		"https://gpu-box.example.com",
	}
	for _, e := range notLocal {
		if IsLocal(e) {
			t.Errorf("IsLocal(%q) = true, want false", e)
		}
		if got := Of(e); got != "" {
			t.Errorf("Of(%q) = %q, want empty", e, got)
		}
	}
}

// 판정 불가와 "로컬이 아니다"를 같은 값으로 둔다 — 모르면 아무 말도 하지 않는다.
// 갈라 두면 판정 실패가 클라우드 사용량으로 위조되는 경로가 생긴다.
func TestOf_UnknownIsSilentNotCloud(t *testing.T) {
	for _, e := range []string{"", "   ", "://", "http://"} {
		if got := Of(e); got != "" {
			t.Errorf("Of(%q) = %q, want empty (판정 불가는 침묵이다)", e, got)
		}
	}
}

/*
 * 스킴 없는 host:port 가 판정되는지 본다.
 *
 * 이게 왜 별도 테스트인가: url.Parse 는 `127.0.0.1:11434` 를 호스트가 아니라 **스킴
 * `127.0.0.1` + opaque `11434`** 로 읽는다. 그 함정에 빠지면 Hostname() 이 빈 값을 내고
 * **모든 로컬 주소가 조용히 판정 불가로 떨어진다** — 거부가 아니라 침묵이라 더 나쁘다.
 */
func TestHostOf_SchemelessHostPort(t *testing.T) {
	cases := []struct{ in, want string }{
		{"127.0.0.1:11434", "127.0.0.1"},
		{"localhost:1234", "localhost"},
		{"[::1]:1234", "::1"},
		{"http://h:1/v1?a=b", "h"},
		{"gpu-box", "gpu-box"},
		{"https://x.example.com/v1/chat", "x.example.com"},
	}
	for _, c := range cases {
		if got := hostOf(c.in); got != c.want {
			t.Errorf("hostOf(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// 경로·쿼리·자격정보는 판정에 쓰지 않고 결과에도 남지 않는다 — 반환값이 상수 하나라는
// 것이 곧 누출 불가의 증명이다.
func TestOf_NeverLeaksEndpointDetail(t *testing.T) {
	got := Of("http://user:s3cr3t@127.0.0.1:11434/v1/chat?key=TOPSECRET")
	if got != Local {
		t.Fatalf("Of = %q, want %q", got, Local)
	}
	if got != "local" {
		t.Fatalf("반환값이 상수가 아니다: %q", got)
	}
}

// 대소문자·후행 점(FQDN 표기)에 흔들리지 않는다.
func TestIsLocal_CaseAndTrailingDot(t *testing.T) {
	for _, e := range []string{"http://LOCALHOST:1234", "http://localhost.", "http://GPU-BOX"} {
		if !IsLocal(e) {
			t.Errorf("IsLocal(%q) = false, want true", e)
		}
	}
}
