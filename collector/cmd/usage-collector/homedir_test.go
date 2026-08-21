package main

import (
	"os"
	"path/filepath"
	"testing"
)

/*
 * ── 원천 홈이 옮겨졌을 때 (조용한 누락 방지) ────────────────────────────────
 *
 * 이 스위트가 막는 사고는 하나다: **도구가 자기 홈을 옮겼는데 수집기가 옛 자리를 보는 것.**
 * 그러면 수집기는 "세션 N개 전송 완료"라고 말하면서 그 사람 사용량을 통째로 빠뜨린다.
 * 거부도 경고도 없어서, 화면은 그 사람이 그 도구를 덜 썼다고 **단정**한다.
 *
 * 실제로 맞았다(2026-08-21 실측): Orca 가 `CODEX_HOME` 을 옮겨 띄우는데 수집기가 그 변수를
 * 보지 않아, 그 홈의 세션 11개가 빠지고 `~/.codex` 의 8개만 올라갔다.
 *
 * 우선순위 계약: **명시적 경로 > 도구의 설정 디렉터리 > 기본 홈.**
 * 앞쪽이 뒤쪽을 이긴다 — 명시적 지시가 넓은 설정보다 강하다.
 */

func TestDefaultCodexDirHonorsCodexHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_SESSIONS_DIR", "")
	t.Setenv("CODEX_HOME", "")

	// ① 아무것도 없으면 기본 홈.
	if got, want := defaultCodexDir(), filepath.Join(home, ".codex", "sessions"); got != want {
		t.Errorf("기본값 = %q, want %q", got, want)
	}

	// ② CODEX_HOME 을 옮기면 따라간다 — 이 한 줄이 없어서 세션 11개가 조용히 빠졌다.
	moved := filepath.Join(home, "orca", "codex-runtime-home", "home")
	t.Setenv("CODEX_HOME", moved)
	if got, want := defaultCodexDir(), filepath.Join(moved, "sessions"); got != want {
		t.Errorf("CODEX_HOME 반영 실패: %q, want %q", got, want)
	}

	// ③ 명시적 세션 경로가 이긴다(그쪽이 더 구체적인 지시다).
	explicit := filepath.Join(home, "explicit-sessions")
	t.Setenv("CODEX_SESSIONS_DIR", explicit)
	if got := defaultCodexDir(); got != explicit {
		t.Errorf("CODEX_SESSIONS_DIR 이 CODEX_HOME 을 못 이겼다: %q", got)
	}
}

func TestDefaultDirHonorsClaudeConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_PROJECTS_DIR", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	if got, want := defaultDir(), filepath.Join(home, ".claude", "projects"); got != want {
		t.Errorf("기본값 = %q, want %q", got, want)
	}

	// Claude Code 는 설정 디렉터리를 이 변수로 옮긴다(claude 바이너리가 그 이름을 읽는다).
	// 옮기면 트랜스크립트도 `<경로>/projects` 로 따라간다.
	moved := filepath.Join(home, "alt-claude")
	t.Setenv("CLAUDE_CONFIG_DIR", moved)
	if got, want := defaultDir(), filepath.Join(moved, "projects"); got != want {
		t.Errorf("CLAUDE_CONFIG_DIR 반영 실패: %q, want %q", got, want)
	}

	explicit := filepath.Join(home, "explicit-projects")
	t.Setenv("CLAUDE_PROJECTS_DIR", explicit)
	if got := defaultDir(); got != explicit {
		t.Errorf("CLAUDE_PROJECTS_DIR 이 CLAUDE_CONFIG_DIR 을 못 이겼다: %q", got)
	}
}

/*
 * provider 매핑은 **세션 디렉터리 기준으로** 찾는다(`<codexDir>/../config.toml`).
 *
 * 그래서 CODEX_HOME 이 옮겨지면 config 도 자동으로 그 홈의 것을 읽는다 — 홈을 옮긴 사람이
 * 옛 홈의 config 로 locality 판정을 받는 일이 없다. 이 대응이 깨지면 판정이 다른 설치의
 * 설정을 보게 되므로 함께 못박는다.
 */
func TestCodexProvidersFollowsMovedHome(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, "moved-codex")
	sessions := filepath.Join(codexHome, "sessions")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"),
		[]byte("[model_providers.ollama]\nbase_url = \"http://localhost:11434/v1\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := codexProviders(sessions)
	if got["ollama"] != "http://localhost:11434/v1" {
		t.Fatalf("옮긴 홈의 config 를 못 읽었다: %v", got)
	}
}

/*
 * codexDirs — 훑을 홈 목록.
 *
 * 이 함수가 막는 사고: 홈이 옮겨진 사람이 **다른 터미널로도** Codex 를 쓰면 세션이 두 곳에
 * 나뉜다. 하나만 훑으면 나머지가 조용히 빠진다. 훅이 물려받는 환경에 따라 빠지는 쪽이
 * 달라지므로 "환경변수를 존중한다"만으로는 손실이 자리만 옮긴다.
 *
 * 동시에 **명시적 지시는 넓히지 않는다** — `-codex-dir` 은 진단·다른 계정 검사에 쓰는
 * 플래그이고, 그 뜻은 "이 디렉터리만 보라"다.
 */
func TestCodexDirsScansBothHomesUnlessExplicit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	fallback := filepath.Join(home, ".codex", "sessions")
	moved := filepath.Join(home, "moved", "sessions")

	// ① 환경이 옮긴 홈 → 그 홈 + 기본 홈 **둘 다**.
	got := codexDirs(options{codexDir: moved})
	if len(got) != 2 || got[0] != moved || got[1] != fallback {
		t.Errorf("옮긴 홈에서 %v, want [%s %s]", got, moved, fallback)
	}

	// ② 기본 홈뿐이면 하나만 — 같은 디렉터리를 두 번 훑으면 로그의 세션 수가 두 배로 보인다.
	if got := codexDirs(options{codexDir: fallback}); len(got) != 1 || got[0] != fallback {
		t.Errorf("기본 홈에서 %v, want [%s] 하나", got, fallback)
	}

	// ③ 사용자가 명시했으면 그 하나만.
	if got := codexDirs(options{codexDir: moved, codexDirSet: true}); len(got) != 1 || got[0] != moved {
		t.Errorf("명시했는데 넓혔다: %v", got)
	}
}
