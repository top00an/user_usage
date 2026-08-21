// Package codexcfg 는 Codex 의 `~/.codex/config.toml` 에서 **커스텀 provider 의 엔드포인트만**
// 뽑는다. 순수 계층이다(io.Reader 를 받고 파일을 열지 않는다).
//
// ── 왜 이게 필요한가 ────────────────────────────────────────────────────────
//
// 로컬 모델을 Codex 로 쓰는 방법은 `[model_providers.<이름>]` 블록에 `base_url` 을 적고
// 그 이름을 모델 provider 로 지정하는 것이다. 그리고 **세션 파일에는 이름만 남는다**
// (실측: `session_meta.model_provider` = `"openai"`, 실세션 8/8). 이름만으로는 그것이
// 로컬인지 알 수 없으므로 이름 → base_url 매핑이 필요하고, 그 매핑이 여기에 있다.
//
// ── 왜 TOML 라이브러리를 쓰지 않는가 ────────────────────────────────────────
//
// 수집기는 팀원 PC 에서 도는 클라이언트라 **의존성이 표준 라이브러리뿐이다**(collector/go.mod).
// 서드파티를 들이면 공급망 표면이 사람 수만큼 늘어난다. 여기서 필요한 것은
// `[model_providers.X]` 섹션의 `base_url` 한 줄이라, 그 부분집합만 줄 단위로 훑는다.
//
// ⚠ **이건 TOML 파서가 아니다.** 멀티라인 문자열·인라인 테이블·배열·주석 안의 대괄호 같은
// 것을 제대로 다루지 않는다. 그런 모양이 오면 **조용히 못 읽고 넘어간다** — 그게 맞는
// 실패 방식이다. 못 읽으면 locality 판정이 "모른다"로 떨어지고(§ internal/runtime 의 침묵
// 규율), 틀린 값을 만들지는 않는다.
//
// ── 무엇을 남기지 않는가 ────────────────────────────────────────────────────
//
// 이 패키지가 내는 것은 이름 → base_url 맵이고, 호출부는 그것을 곧바로
// `runtime.Of` 에 먹여 **낱말 하나**로 줄인다. base_url 자체는 페이로드에 실리지 않는다.
// env_key·api_key 류는 **읽지도 않는다** — 파싱 대상 키를 base_url 하나로 좁힌 이유다.
//
// 표준 라이브러리 말고 아무것도 import 하지 않는다.
package codexcfg

import (
	"bufio"
	"io"
	"strings"
)

// providerPrefix — 우리가 찾는 섹션 머리.
const providerPrefix = "model_providers."

// maxLineBytes — 한 줄 상한. config.toml 은 사람이 쓰는 파일이라 크지 않지만, 상한이 없으면
// 손상된 파일 하나가 메모리를 통째로 먹는다.
const maxLineBytes = 1 << 20

/*
 * Providers 는 provider 이름 → base_url 맵을 낸다. 읽을 것이 없으면 빈 맵이다(nil 아님).
 *
 * 받는 모양:
 *
 *	[model_providers.ollama]
 *	name = "Ollama"
 *	base_url = "http://localhost:11434/v1"
 *	wire_api = "chat"
 *
 * 섹션 이름이 따옴표로 감싸인 형태(`[model_providers."my host"]`)도 받는다 — TOML 이
 * 허용하는 표기이고, 따옴표를 벗겨 이름으로 쓴다.
 *
 * base_url 이 없는 블록은 **맵에 넣지 않는다.** 빈 문자열을 넣으면 호출부가 "블록은 있는데
 * URL 을 모른다"와 "블록이 없다"를 구분할 수 없고, 둘 다 판정 불가로 다뤄야 하므로
 * 없는 것으로 두는 편이 단순하다.
 */
func Providers(r io.Reader) map[string]string {
	out := map[string]string{}
	if r == nil {
		return out
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)

	cur := "" // 지금 읽고 있는 provider 이름("" 이면 관심 없는 섹션)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			cur = providerNameOf(line)
			continue
		}
		if cur == "" {
			continue
		}
		k, v, ok := keyValue(line)
		if !ok || k != "base_url" || v == "" {
			continue
		}
		out[cur] = v
	}
	return out
}

// providerNameOf 는 섹션 머리에서 provider 이름을 뽑는다. 우리 섹션이 아니면 "" 다.
//
// `[[...]]`(배열 테이블)는 받지 않는다 — model_providers 는 테이블이고, 배열 표기가 오면
// 우리가 아는 모양이 아니므로 관심 없는 섹션으로 둔다.
func providerNameOf(line string) string {
	if strings.HasPrefix(line, "[[") {
		return ""
	}
	// 줄 끝에 주석이 붙을 수 있다: `[model_providers.x]  # 로컬`
	if i := strings.Index(line, "]"); i >= 0 {
		line = line[:i+1]
	}
	s := strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, providerPrefix) {
		return ""
	}
	name := strings.TrimSpace(s[len(providerPrefix):])
	return unquote(name)
}

// keyValue 는 `k = "v"` 한 줄을 쪼갠다. 값의 따옴표를 벗기고 줄 끝 주석을 버린다.
func keyValue(line string) (string, string, bool) {
	i := strings.Index(line, "=")
	if i <= 0 {
		return "", "", false
	}
	k := strings.TrimSpace(line[:i])
	v := strings.TrimSpace(line[i+1:])

	// 따옴표 안의 `#` 은 주석이 아니다. 따옴표로 시작하면 닫는 따옴표까지가 값이다.
	if len(v) > 1 && (v[0] == '"' || v[0] == '\'') {
		q := v[0]
		if j := strings.IndexByte(v[1:], q); j >= 0 {
			return k, v[1 : 1+j], true
		}
		return "", "", false // 닫히지 않은 따옴표 — 못 읽는다
	}
	if j := strings.Index(v, "#"); j >= 0 {
		v = strings.TrimSpace(v[:j])
	}
	return k, unquote(v), true
}

// unquote 는 감싼 따옴표 한 겹을 벗긴다. 이스케이프는 다루지 않는다 — base_url 과 provider
// 이름에 이스케이프가 필요한 문자는 실제로 오지 않고, 다루는 척하면 틀리는 자리가 늘어난다.
func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
