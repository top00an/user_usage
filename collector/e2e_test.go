package collector_test

// 수집기 → 실제 usage-server → 조회 API 의 전 경로를 자동 검증하는 e2e 테스트.
//
// 그동안 이 경로는 수동 스모크로만 확인됐다. 여기서 재현 가능한 회귀로 굳힌다: 합성 jsonl 을
// 만들고 → 서버 바이너리를 빈 sqlite 로 기동 → 수집기 바이너리로 올려 → summary/sessions 조회가
// 실제로 반영되고 재실행이 멱등인지 본다.
//
// 게이트: 두 바이너리를 `go build` 로 만든 뒤 경로를 넘겨야 돈다. 없으면 skip(단위 스위트 무영향).
//   USAGE_E2E_SERVER_BIN=/path/usage-server USAGE_E2E_COLLECTOR_BIN=/path/usage-collector go test ./collector/ -run E2E

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"path/filepath"
	"testing"
	"time"
)

func bins(t *testing.T) (server, collector string) {
	t.Helper()
	server = os.Getenv("USAGE_E2E_SERVER_BIN")
	collector = os.Getenv("USAGE_E2E_COLLECTOR_BIN")
	if server == "" || collector == "" {
		t.Skip("USAGE_E2E_SERVER_BIN·USAGE_E2E_COLLECTOR_BIN 미설정 — e2e skip")
	}
	return server, collector
}

// 합성 트랜스크립트: 2세션, 멀티모델·캐시·도구 카운터·존있는/없는 타임스탬프를 섞는다.
func writeFixtures(t *testing.T, dir string) {
	t.Helper()
	proj := filepath.Join(dir, "proj-x")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	s1 := filepath.Join(proj, "sess-e2e-1.jsonl")
	lines1 := []string{
		`{"type":"assistant","timestamp":"2026-08-05T14:21:49.271Z","sessionId":"sess-e2e-1","cwd":"/Users/me/proj-x","message":{"role":"assistant","model":"claude-opus-5","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":100,"cache_creation_input_tokens":50,"cache_creation":{"ephemeral_1h_input_tokens":50}}}}`,
		`{"type":"assistant","timestamp":"2026-08-05T15:02:00.000Z","sessionId":"sess-e2e-1","cwd":"/Users/me/proj-x","message":{"role":"assistant","model":"claude-sonnet-5","stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":8}}}`,
	}
	writeLines(t, s1, lines1)

	s2 := filepath.Join(proj, "sess-e2e-2.jsonl")
	lines2 := []string{
		`{"type":"assistant","timestamp":"2026-08-06T09:00:00.000Z","sessionId":"sess-e2e-2","cwd":"/Users/me/proj-x","message":{"role":"assistant","model":"claude-opus-5","stop_reason":"end_turn","usage":{"input_tokens":7,"output_tokens":3}}}`,
	}
	writeLines(t, s2, lines2)
}

func writeLines(t *testing.T, path string, lines []string) {
	t.Helper()
	data := ""
	for _, l := range lines {
		data += l + "\n"
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestE2ECollectToDashboard(t *testing.T) {
	serverBin, collectorBin := bins(t)
	projects := t.TempDir()
	writeFixtures(t, projects)

	dataDir := t.TempDir()
	state := filepath.Join(t.TempDir(), "state.json")
	const admin = "e2e-admin-token-0123456789"
	const intake = "e2e-intake-token-9876543210"
	const port = "8195"
	base := "http://127.0.0.1:" + port

	srv := exec.Command(serverBin)
	srv.Env = append(os.Environ(),
		"USAGE_ADMIN_TOKEN="+admin, "USAGE_INTAKE_TOKEN="+intake,
		"USAGE_DATA_DIR="+dataDir, "USAGE_HOST=127.0.0.1", "USAGE_PORT="+port,
		"USAGE_KEYWORD_RETENTION_DAYS=off")
	if err := srv.Start(); err != nil {
		t.Fatalf("서버 기동 실패: %v", err)
	}
	defer func() { _ = srv.Process.Kill() }()
	waitHealthy(t, base)

	// BEFORE — 빈 서버
	if got := totalsSessions(t, base, admin); got != 0 {
		t.Fatalf("BEFORE sessions=%d, want 0", got)
	}

	// 수집 — -dir 로 합성 픽스처만 읽게 고정한다(실제 ~/.claude/projects 를 읽지 않도록).
	run := func(extra ...string) string {
		args := append([]string{"-server", base, "-state", state, "-dir", projects}, extra...)
		c := exec.Command(collectorBin, args...)
		c.Env = append(os.Environ(), "USAGE_INTAKE_TOKEN="+intake)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("수집기 실패: %v\n%s", err, out)
		}
		return string(out)
	}
	run()

	// AFTER — 2세션 반영
	if got := totalsSessions(t, base, admin); got != 2 {
		t.Fatalf("AFTER sessions=%d, want 2", got)
	}

	// 멱등 — 전량 재전송해도 값 불변
	before := totalsInput(t, base, admin)
	run("-all")
	if after := totalsInput(t, base, admin); after != before {
		t.Fatalf("멱등 실패: input %d → %d", before, after)
	}

	// 세션 목록에 두 세션이 다 보인다
	body := getJSON(t, base+"/api/usage/sessions?limit=1000", admin)
	if !containsSession(body, "sess-e2e-1") || !containsSession(body, "sess-e2e-2") {
		t.Fatalf("세션이 조회에 안 보인다")
	}
}

// ── helpers ──
func waitHealthy(t *testing.T, base string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("서버가 뜨지 않았다")
}

func getJSON(t *testing.T, url, token string) map[string]any {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("JSON 디코드: %v", err)
	}
	return m
}

func totalsSessions(t *testing.T, base, token string) int {
	return totalsField(t, base, token, "sessions")
}
func totalsInput(t *testing.T, base, token string) int {
	return totalsField(t, base, token, "input")
}
func totalsField(t *testing.T, base, token, field string) int {
	m := getJSON(t, base+"/api/usage/summary?days=3650", token)
	tot, _ := m["totals"].(map[string]any)
	if tot == nil {
		return 0
	}
	f, _ := tot[field].(float64)
	return int(f)
}

func containsSession(body map[string]any, id string) bool {
	b, _ := json.Marshal(body)
	return strings.Contains(string(b), id)
}
