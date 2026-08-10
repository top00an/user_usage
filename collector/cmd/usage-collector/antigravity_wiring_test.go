package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tscorp/user-usage/collector/internal/payload"
)

// 이 테스트는 Antigravity **배선**을 잠근다: statusLine 기록 모드가 스풀을 만드는지,
// 그 스풀이 다시 수집 경로를 타고 payload 로 나오는지.
//
// 토큰 누적 규칙 자체는 internal/antigravity 단위 테스트가 덮는다. 여기서 보는 것은
// "기록 → 수집 → 전송할 모양" 한 바퀴가 실제로 이어지는가다.

// 실측 statusLine payload 모양 그대로.
func statusLineFixture(conv string, totalOut int64, in, out int64) string {
	return `{
	  "cwd": "/Users/me/orca/user_usage",
	  "session_id": "` + conv + `",
	  "conversation_id": "` + conv + `",
	  "model": {"id":"Gemini 3.6 Flash (Medium)","display_name":"Gemini 3.6 Flash (Medium)","effort":"medium"},
	  "workspace": {"current_dir":"/Users/me/orca/user_usage","project_dir":"/Users/me/orca/user_usage"},
	  "context_window": {
	    "total_input_tokens": 25286,
	    "total_output_tokens": ` + itoaTest(totalOut) + `,
	    "context_window_size": 1048576,
	    "current_usage": {"input_tokens":` + itoaTest(in) + `,"output_tokens":` + itoaTest(out) + `,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}
	  },
	  "plan_tier": "Google AI Pro",
	  "email": "someone@example.com"
	}`
}

func itoaTest(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

const wireConv = "1c8f484f-af66-4c7f-97c2-22b57c5773eb"

// statusLine 기록 모드는 스풀을 만들고, 상태줄 한 줄을 stdout 으로 낸다.
func TestStatusLineModeWritesSpool(t *testing.T) {
	spool := filepath.Join(t.TempDir(), "antigravity")

	out := captureRun(t, statusLineFixture(wireConv, 21, 17283, 17),
		"-antigravity-statusline", "-antigravity-dir", spool)

	if !strings.Contains(out, "gemini-3.6-flash") {
		t.Fatalf("상태줄에 모델이 없다: %q", out)
	}
	if _, err := os.Stat(filepath.Join(spool, wireConv+".json")); err != nil {
		t.Fatalf("스풀이 생기지 않았다: %v", err)
	}
}

// 깨진 stdin 이 와도 상태줄은 살아 있어야 한다(사용자 화면을 깨뜨리지 않는다).
func TestStatusLineModeSurvivesGarbage(t *testing.T) {
	spool := filepath.Join(t.TempDir(), "antigravity")
	_ = captureRun(t, "not json at all", "-antigravity-statusline", "-antigravity-dir", spool)
	// 종료코드 0 은 captureRun 이 검사한다.
}

// 기록 → 수집 한 바퀴. 스풀이 payload.Session 으로 나오고 platform 이 antigravity 다.
func TestSpoolFlowsIntoPayload(t *testing.T) {
	dir := t.TempDir()
	spool := filepath.Join(dir, "antigravity")

	// 두 invocation 을 기록한다(실측값).
	captureRun(t, statusLineFixture(wireConv, 21, 17283, 17), "-antigravity-statusline", "-antigravity-dir", spool)
	captureRun(t, statusLineFixture(wireConv, 40, 17506, 19), "-antigravity-statusline", "-antigravity-dir", spool)

	// history.jsonl 로 slash 축을 채운다.
	home := filepath.Join(dir, "agy")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	hist := `{"display":"/model","timestamp":1,"workspace":"/Users/me/orca/user_usage","conversationId":"` + wireConv + `","type":"slash_command"}` + "\n"
	if err := os.WriteFile(filepath.Join(home, "history.jsonl"), []byte(hist), 0o600); err != nil {
		t.Fatal(err)
	}

	out := captureRun(t, "",
		"-dry-run", "-all",
		"-dir", "", "-codex-dir", "", "-gemini-dir", "",
		"-antigravity-dir", spool, "-antigravity-home", home,
		"-state", filepath.Join(dir, "state.json"),
	)

	var rep payload.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("payload 파싱 실패: %v\n%s", err, out)
	}
	if len(rep.Sessions) != 1 {
		t.Fatalf("세션 1개 기대: %d", len(rep.Sessions))
	}
	s := rep.Sessions[0]
	if s.Platform != "antigravity" {
		t.Fatalf("platform: %q", s.Platform)
	}
	if s.ID != wireConv {
		t.Fatalf("세션 id: %q", s.ID)
	}
	if s.Model != "gemini-3.6-flash-medium" {
		t.Fatalf("모델: %q", s.Model)
	}
	if s.Project != "user_usage" {
		t.Fatalf("프로젝트: %q", s.Project)
	}
	// 실측 대조: 입력 합산 17283+17506, 출력은 누적값 40.
	if s.Input != 34789 || s.Output != 40 {
		t.Fatalf("토큰 in=%d out=%d (기대 34789/40)", s.Input, s.Output)
	}
	if s.CacheCreate != 0 {
		t.Fatalf("CacheCreate 는 항상 0: %d", s.CacheCreate)
	}
	if s.Turns != 2 {
		t.Fatalf("턴 2 기대: %d", s.Turns)
	}
	if s.Counters["slash"]["model"] != 1 {
		t.Fatalf("slash 축: %v", s.Counters)
	}
	// 얻을 수 없는 축은 실리지 않는다(0 위조 금지).
	for _, axis := range []string{"tool", "bash", "skill", "agent", "mcp"} {
		if _, ok := s.Counters[axis]; ok {
			t.Fatalf("얻을 수 없는 축 %q 가 실렸다", axis)
		}
	}
	// 개인정보가 새지 않는다.
	if strings.Contains(out, "someone@example.com") || strings.Contains(out, "/Users/me") {
		t.Fatal("payload 에 개인정보/절대경로가 새어 나갔다")
	}
}

// 스풀 디렉터리가 없으면 조용히 지나간다(기존 원천과 같은 규율).
func TestMissingSpoolDirIsSilent(t *testing.T) {
	dir := t.TempDir()
	out := captureRun(t, "",
		"-dry-run", "-all",
		"-dir", "", "-codex-dir", "", "-gemini-dir", "",
		"-antigravity-dir", filepath.Join(dir, "nope"),
		"-state", filepath.Join(dir, "state.json"),
	)
	if strings.TrimSpace(out) != "" {
		t.Fatalf("없는 디렉터리인데 뭔가 출력했다: %q", out)
	}
}

// captureRun 은 run() 을 돌리고 stdout 을 문자열로 준다. 종료코드가 0 이 아니면 실패다.
func captureRun(t *testing.T, stdin string, args ...string) string {
	t.Helper()

	prev := stdinReader
	stdinReader = strings.NewReader(stdin)
	defer func() { stdinReader = prev }()

	outFile := filepath.Join(t.TempDir(), "stdout")
	f, err := os.Create(outFile)
	if err != nil {
		t.Fatal(err)
	}
	errFile, err := os.Create(filepath.Join(t.TempDir(), "stderr"))
	if err != nil {
		t.Fatal(err)
	}

	code := run(args, f, errFile)
	f.Close()
	errFile.Close()
	if code != 0 {
		b, _ := os.ReadFile(outFile)
		t.Fatalf("run(%v) 종료코드 %d\nstdout: %s", args, code, b)
	}
	b, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
