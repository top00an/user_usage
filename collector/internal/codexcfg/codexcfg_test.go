package codexcfg

import (
	"strings"
	"testing"
)

func TestProviders_ReadsBaseURL(t *testing.T) {
	got := Providers(strings.NewReader(`
model = "qwen3-coder:30b"
model_provider = "ollama"

[model_providers.ollama]
name = "Ollama"
base_url = "http://localhost:11434/v1"
wire_api = "chat"

[model_providers.lmstudio]
name = "LM Studio"
base_url = "http://127.0.0.1:1234/v1"

[model_providers.together]
name = "Together"
base_url = "https://api.together.xyz/v1"
env_key = "TOGETHER_API_KEY"
`))

	want := map[string]string{
		"ollama":   "http://localhost:11434/v1",
		"lmstudio": "http://127.0.0.1:1234/v1",
		"together": "https://api.together.xyz/v1",
	}
	if len(got) != len(want) {
		t.Fatalf("providers = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("providers[%q] = %q, want %q", k, got[k], v)
		}
	}
}

// 관심 없는 섹션의 base_url 을 주워 오면 안 된다 — projects 블록이 실제 config 에 잔뜩 있다.
func TestProviders_IgnoresOtherSections(t *testing.T) {
	got := Providers(strings.NewReader(`
[projects."/mnt/c/work/repo"]
trust_level = "trusted"
base_url = "http://should-not-be-read"

[history]
persistence = "save-all"

[model_providers.ollama]
base_url = "http://localhost:11434/v1"

[tui]
base_url = "http://nope"
`))

	if len(got) != 1 || got["ollama"] != "http://localhost:11434/v1" {
		t.Fatalf("providers = %v, want only ollama", got)
	}
}

// base_url 이 없는 블록은 맵에 넣지 않는다 — "블록은 있는데 URL 을 모른다"를 빈 문자열로
// 위조하면 호출부가 "블록이 없다"와 구분할 수 없다.
func TestProviders_BlockWithoutBaseURLIsAbsent(t *testing.T) {
	got := Providers(strings.NewReader(`
[model_providers.mystery]
name = "Mystery"
wire_api = "chat"
`))
	if _, ok := got["mystery"]; ok {
		t.Fatalf("base_url 없는 블록이 맵에 들어갔다: %v", got)
	}
	if got == nil {
		t.Fatal("nil 맵을 냈다 — 호출부가 nil 검사를 하게 만들지 않는다")
	}
}

func TestProviders_QuotedSectionNameAndTrailingComment(t *testing.T) {
	got := Providers(strings.NewReader(`
[model_providers."gpu box"]  # 사내 장비
base_url = "http://gpu-box:11434/v1"   # 로컬
`))
	if got["gpu box"] != "http://gpu-box:11434/v1" {
		t.Fatalf("providers = %v", got)
	}
}

// 따옴표 안의 `#` 은 주석이 아니다.
func TestProviders_HashInsideQuotesIsNotAComment(t *testing.T) {
	got := Providers(strings.NewReader(`
[model_providers.x]
base_url = "http://h/v1?tag=#frag"
`))
	if got["x"] != "http://h/v1?tag=#frag" {
		t.Fatalf("providers = %v", got)
	}
}

// 배열 테이블은 우리가 아는 모양이 아니다 — 관심 없는 섹션으로 둔다.
func TestProviders_ArrayTableIgnored(t *testing.T) {
	got := Providers(strings.NewReader(`
[[model_providers.weird]]
base_url = "http://nope"
`))
	if len(got) != 0 {
		t.Fatalf("배열 테이블을 읽었다: %v", got)
	}
}

// 못 읽는 모양은 조용히 넘어간다 — 틀린 값을 만드는 것보다 낫다.
func TestProviders_MalformedIsSilent(t *testing.T) {
	for _, src := range []string{
		"",
		"쓰레기\n[[[\nbase_url\n",
		"[model_providers.x]\nbase_url = \"닫히지 않은\n",
		"[model_providers.x]\nbase_url =\n",
	} {
		got := Providers(strings.NewReader(src))
		for k, v := range got {
			if v == "" {
				t.Errorf("빈 값이 맵에 들어갔다: %q", k)
			}
		}
	}
	if got := Providers(nil); got == nil || len(got) != 0 {
		t.Fatalf("Providers(nil) = %v, want 빈 맵", got)
	}
}
