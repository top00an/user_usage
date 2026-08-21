// Package runtime 은 "이 세션이 로컬 모델로 돌았나"를 **엔드포인트 하나만 보고** 판정한다.
//
// ── 왜 이 판정이 클라이언트에 있는가 ────────────────────────────────────────
//
// 로컬 LLM 사용량에서 가장 중요한 사실은 "로컬이었다"인데, 그것이 **어디에도 적혀 있지
// 않다.** `usage_sessions` 에는 platform 과 model 뿐이고, 로컬 모델을 클라우드 에이전트가
// 물고 돌면 두 값 모두 클라우드 세션과 구별되지 않는다.
//
// 레포는 이 문제를 이미 만나서 이름 붙여 놨다(`go/internal/cost/seed_openai.go`):
//
//	gpt-oss-120b 는 오픈웨이트 모델이라 호스팅 사업자마다 단가가 다르다 —
//	**어디서 돌렸는지를 사용량 레코드가 말해 주지 않는다.**
//
// 그래서 모델명으로 추론하지 않고 **기록한다.** 판정 근거인 엔드포인트를 아는 것은
// 클라이언트뿐이므로 판정도 여기서 한다.
//
// ── 호스트명을 보내지 않는 이유 (데이터 정책) ───────────────────────────────
//
// 이 패키지는 엔드포인트를 받아 **낱말 하나**(`local` 또는 빈 값)를 낸다. 호스트명·포트·
// 경로·쿼리·토큰은 **어디에도 남기지 않는다.** 내부 엔드포인트 목록은 그 자체로 팀 내부망
// 지도이고, 이 도구의 데이터 정책은 "필요한 최소만 저장"이다. 판정을 클라이언트에서 끝내면
// 저장된 값에서 되돌릴 정보가 없다.
//
// ⚠ 이 패키지는 **판정만** 한다. 어느 파일에서 엔드포인트를 읽어 오는지는 호출부의 일이고,
// 그 자리는 아직 정해지지 않았다(docs/PLAN-local-llm.md §3.1 · D2) — 세션 파일에 엔드포인트가
// 남는지 실데이터로 확인되지 않았다. 그래서 이 패키지는 **문자열을 받는다**: 원천이
// 세션 파일이든 설정 파일이든 같은 판정을 쓸 수 있어야 한다.
//
// 표준 라이브러리 말고 아무것도 import 하지 않는다.
package runtime

import (
	"net"
	"net/url"
	"strings"
)

// Local 은 로컬 런타임으로 판정된 세션의 값이다(payload.Session.Runtime).
//
// 빈 값(클라우드)을 별도 상수로 두지 않는다 — 서버가 미보고를 클라우드로 읽는 것이
// 하위호환의 전부이고, 그 기본값을 클라이언트가 명시하면 "명시된 클라우드"와
// "구버전 수집기의 침묵"이 구별되지 않는다.
const Local = "local"

// Of 는 엔드포인트 문자열을 판정 결과로 좁힌다. 로컬이면 Local, 그 밖(공인 주소·판정
// 불가·빈 값)이면 **빈 문자열**이다.
//
// 빈 문자열을 내는 것이 중요하다: "로컬이 아니다"와 "모르겠다"를 같은 값으로 둔다.
// 둘을 갈라 봐야 화면에서 할 수 있는 일이 없고, 갈라 두면 판정 실패가 클라우드 사용량으로
// 위조되는 경로가 생긴다. 모르면 아무 말도 하지 않는 편이 이 레포의 규율이다.
func Of(endpoint string) string {
	if IsLocal(endpoint) {
		return Local
	}
	return ""
}

/*
 * IsLocal 은 엔드포인트가 로컬을 가리키는지 본다.
 *
 * 판정 근거는 **호스트뿐이다.** 포트로 판정하지 않는다 — 11434(Ollama)·1234(LM Studio)·
 * 8080 같은 번호를 목록으로 두고 싶어지지만, 같은 포트를 원격 장비가 쓰면 그 사용량이
 * 로컬로 위조된다. 반대로 로컬에서 다른 포트를 쓰면 놓친다. 호스트가 진짜 근거다.
 *
 * 로컬로 보는 것:
 *   · loopback (127.0.0.0/8 · ::1 · localhost)
 *   · 사설 대역 (10/8 · 172.16/12 · 192.168/16 · fc00::/7) — 사내 GPU 서버가 여기 있다
 *   · 링크로컬 (169.254/16 · fe80::/10)
 *   · 이름에 점이 없는 단일 라벨 호스트 (`gpu-box`) — DNS 로 나가지 않는 사내 이름이다
 *   · WSL·컨테이너에서 호스트를 가리키는 관용 이름 (`host.docker.internal` 류)
 *
 * 로컬로 보지 **않는** 것: 공인 IP · 점이 있는 공개 도메인 · 판정 불가.
 */
func IsLocal(endpoint string) bool {
	h := hostOf(endpoint)
	if h == "" {
		return false
	}
	h = strings.ToLower(strings.TrimSuffix(h, "."))

	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	// 컨테이너·WSL 에서 호스트 머신을 가리키는 관용 이름. 이름 자체가 "이 기계"를 뜻한다.
	for _, s := range []string{"host.docker.internal", "host.containers.internal", "host.lima.internal", "gateway.docker.internal"} {
		if h == s {
			return true
		}
	}
	// `.local`(mDNS)·`.internal`·`.lan`·`.home.arpa` 는 라우팅되지 않는 이름이다.
	for _, suf := range []string{".local", ".internal", ".lan", ".home.arpa", ".intranet"} {
		if strings.HasSuffix(h, suf) {
			return true
		}
	}

	if ip := net.ParseIP(h); ip != nil {
		return isLocalIP(ip)
	}

	// 점이 없는 단일 라벨 이름은 공개 DNS 로 해석되지 않는다 — 사내 호스트다.
	// (`gpu-box` · `workstation`. 단 빈 값은 위에서 걸렀다.)
	return !strings.Contains(h, ".")
}

// isLocalIP 는 주소가 로컬·사설·링크로컬 대역인지 본다.
//
// net.IP 의 표준 판정자를 쓴다(직접 대역 계산을 하면 IPv4-mapped IPv6 같은 자리에서 틀린다).
// IsPrivate 는 10/8 · 172.16/12 · 192.168/16 · fc00::/7 을 덮는다.
func isLocalIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() // 0.0.0.0 · :: — 로컬에 바인딩된 서버를 가리키는 표기다
}

/*
 * hostOf 는 엔드포인트에서 호스트만 뽑는다. 뽑지 못하면 빈 값이다.
 *
 * 입력이 정규 URL 이라고 가정하지 않는다. 실제로 오는 모양이 뒤섞여 있다:
 *
 *	http://127.0.0.1:11434/v1     ← 완전한 URL
 *	127.0.0.1:11434               ← 스킴 없음(host:port)
 *	localhost                     ← 호스트만
 *	[::1]:1234                    ← IPv6 리터럴
 *
 * 스킴이 없으면 붙여서 파싱한다 — url.Parse 는 스킴 없는 `127.0.0.1:11434` 를 호스트가 아니라
 * **스킴 `127.0.0.1` + opaque `11434`** 로 읽는다. 그 함정에 빠지면 모든 로컬 주소가 판정
 * 불가로 떨어진다.
 */
func hostOf(endpoint string) string {
	s := strings.TrimSpace(endpoint)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	h := u.Hostname() // 포트와 IPv6 대괄호를 벗겨 준다
	return h
}
