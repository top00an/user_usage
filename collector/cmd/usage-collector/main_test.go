package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tscorp/user-usage/collector/internal/payload"
)

// 이 테스트는 **배선**을 잠근다: 어떤 파일을 스캔 대상으로 볼지(discover/Match), 그 파일들을
// 어떤 그룹으로 묶을지(key), 경로를 파서에 넘기는지(AddFileAt).
//
// 파서 내부 규칙은 internal/gemini 의 단위 테스트가 덮는다. 여기서 굳이 한 번 더 도는 이유는
// 곁가지 파일(`.unreadable-*`·`.tmp-*`) 배제가 **파서가 아니라 스캐너의 책임**이기 때문이다 —
// 그게 새면 세션이 통째로 두 번(또는 세 번) 세어지고, 그 값이 그대로 비용이 된다.
//
// ⚠ 픽스처 기반이다. 구현 시점에 이 머신엔 Gemini CLI 도 세션 데이터도 없었다(실데이터 미검증).

const (
	gemA = "aaaaaaaa-1111-4111-8111-111111111111"
	gemC = "cccccccc-3333-4333-8333-333333333333"
	gemD = "dddddddd-4444-4444-8444-444444444444"
)

func writeFile(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// geminiFixture 는 가짜 `~/.gemini` 트리를 만들고 그 경로를 돌려준다.
func geminiFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	chats := filepath.Join(root, "tmp", "my-app", "chats")

	metaA := `{"sessionId":"` + gemA + `","projectHash":"h","startTime":"2026-08-01T10:00:00.000Z","lastUpdated":"2026-08-01T11:00:00.000Z"}`
	msgA := `{"id":"m1","timestamp":"2026-08-01T10:10:00.000Z","type":"gemini","content":"ok","model":"gemini-3-pro",` +
		`"tokens":{"input":1000,"output":50,"cached":400,"thoughts":120,"tool":11,"total":0}}`
	sess := filepath.Join(chats, "session-2026-08-01T10-00-"+gemA[:8]+".jsonl")
	writeFile(t, sess, metaA, msgA)

	// 곁가지: 같은 세션의 **전체 사본**이다. 스캔되면 그 세션이 그대로 3배가 된다.
	writeFile(t, sess+".unreadable-1767325200000", metaA, msgA)
	writeFile(t, sess+".tmp-91234", metaA, msgA)

	// 서브에이전트 — 부모 세션에 합산돼야 한다(별도 세션이 되면 세션 수가 부푼다).
	writeFile(t, filepath.Join(chats, "session-2026-08-01T10-00-"+gemC[:8]+".jsonl"),
		`{"sessionId":"`+gemC+`","projectHash":"h","startTime":"2026-08-01T10:00:00.000Z","lastUpdated":"2026-08-01T10:30:00.000Z"}`,
		`{"id":"c1","timestamp":"2026-08-01T10:00:00.000Z","type":"gemini","content":"ok","model":"gemini-3-pro",`+
			`"tokens":{"input":100,"output":10,"cached":0,"thoughts":0,"tool":0,"total":0},`+
			`"toolCalls":[{"id":"t9","name":"invoke_agent","args":{"agent_name":"code-reviewer","prompt":"비밀"},"status":"success","timestamp":"2026-08-01T10:00:00.000Z"}]}`)
	writeFile(t, filepath.Join(chats, gemC, gemD+".jsonl"),
		`{"sessionId":"`+gemD+`","projectHash":"h","kind":"subagent","startTime":"2026-08-01T10:01:00.000Z","lastUpdated":"2026-08-01T10:02:00.000Z"}`,
		`{"id":"d1","timestamp":"2026-08-01T10:01:00.000Z","type":"gemini","content":"ok","model":"gemini-3-pro",`+
			`"tokens":{"input":500,"output":50,"cached":0,"thoughts":0,"tool":0,"total":0}}`)

	// chats/ 밖의 `.json` 잡동사니 — 스캔되면 유령 세션이 생긴다.
	writeFile(t, filepath.Join(root, "tmp", "my-app", "checkpoints", "snap.json"),
		`{"sessionId":"zzzzzzzz-0000-4000-8000-000000000000","projectHash":"h","messages":[`+
			`{"id":"z1","timestamp":"2026-08-01T13:00:00.000Z","type":"gemini","content":"ok","model":"m",`+
			`"tokens":{"input":123456,"output":654321,"cached":0,"thoughts":0,"tool":0,"total":0}}]}`)
	return root
}

// dryRun 은 -dry-run 페이로드를 읽어 돌려준다.
func dryRun(t *testing.T, args ...string) payload.Report {
	t.Helper()
	out, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	errf, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer errf.Close()

	if code := run(args, out, errf); code != 0 {
		b, _ := os.ReadFile(errf.Name())
		t.Fatalf("종료 코드 %d\nstderr:\n%s", code, b)
	}
	raw, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	var rep payload.Report
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &rep); err != nil {
			t.Fatalf("페이로드 파싱 실패: %v\n%s", err, raw)
		}
	}
	return rep
}

func TestGeminiSourceWiring(t *testing.T) {
	root := geminiFixture(t)
	state := filepath.Join(t.TempDir(), "state.json")
	rep := dryRun(t, "-platform=gemini", "-gemini-dir", root, "-state", state, "-dry-run", "-all")

	byID := map[string]payload.Session{}
	for _, s := range rep.Sessions {
		byID[s.ID] = s
	}
	if len(rep.Sessions) != 2 {
		t.Fatalf("세션 2개(A·C)를 기대했는데 %d개다: %v", len(rep.Sessions), byID)
	}

	a, ok := byID[gemA]
	if !ok {
		t.Fatalf("세션 A 가 없다: %v", byID)
	}
	// 곁가지 파일이 스캔됐다면 여기가 3배(1800/510/1200)가 된다.
	if a.Input != 600 || a.Output != 170 || a.CacheRead != 400 || a.Turns != 1 {
		t.Fatalf("곁가지 파일이 이중 집계됐다: input=%d output=%d cacheRead=%d turns=%d (기대 600/170/400/1)",
			a.Input, a.Output, a.CacheRead, a.Turns)
	}
	if a.Platform != "gemini" {
		t.Fatalf("platform=%q", a.Platform)
	}
	if a.Project != "my-app" {
		t.Fatalf("project=%q — 슬러그 디렉터리명이어야 한다", a.Project)
	}

	c, ok := byID[gemC]
	if !ok {
		t.Fatalf("세션 C 가 없다: %v", byID)
	}
	if c.Input != 600 || c.Output != 60 || c.Turns != 2 {
		t.Fatalf("서브에이전트가 부모에 합산되지 않았다: input=%d output=%d turns=%d", c.Input, c.Output, c.Turns)
	}
	if _, ghost := byID[gemD]; ghost {
		t.Fatalf("서브에이전트가 별도 세션으로 올라왔다")
	}
	if c.Counters["agent"]["code-reviewer"] != 1 {
		t.Fatalf("agent 축=%v", c.Counters["agent"])
	}
	for _, s := range rep.Sessions {
		if _, ok := s.Counters["slash"]; ok {
			t.Fatalf("slash 축은 만들지 않는다(원천이 없다): %v", s.Counters["slash"])
		}
		if s.Input > 100000 {
			t.Fatalf("chats/ 밖의 잡동사니가 스캔됐다: %+v", s)
		}
	}
}

// ~/.gemini/tmp 가 없는 머신에서는 **조용히** 지나가야 한다(오류도 경고도 없이).
// Gemini 를 안 쓰는 팀원이 아무 설정 없이 그대로 돌아야 하기 때문이다.
func TestGeminiMissingDirIsSilent(t *testing.T) {
	root := t.TempDir() // tmp/ 가 없다
	state := filepath.Join(t.TempDir(), "state.json")
	rep := dryRun(t, "-platform=gemini", "-gemini-dir", root, "-state", state, "-dry-run", "-all")
	if len(rep.Sessions) != 0 {
		t.Fatalf("세션이 없어야 한다: %+v", rep.Sessions)
	}
}

// Claude·Codex 의 스캔 규칙은 이 변경으로 바뀌지 않는다: `*.jsonl` 만, 그 밖은 전부 제외.
func TestMatchJSONLUnchanged(t *testing.T) {
	cases := map[string]bool{
		"/x/.claude/projects/p/sess.jsonl":                    true,
		"/x/.codex/sessions/2026/08/01/rollout-a-b.jsonl":     true,
		"/x/.claude/projects/p/sess.json":                     false,
		"/x/.codex/sessions/2026/08/01/rollout-a-b.jsonl.zst": false,
		"/x/.claude/projects/p/notes.md":                      false,
	}
	for p, want := range cases {
		if got := matchJSONL(p); got != want {
			t.Errorf("matchJSONL(%q)=%v, 기대 %v", p, got, want)
		}
	}
}
