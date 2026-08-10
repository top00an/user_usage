package gemini

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tscorp/user-usage/collector/internal/payload"
)

// ⚠ 이 패키지의 테스트는 전부 **픽스처 기반**이다. 구현 시점의 머신에 Gemini CLI 가 설치돼
// 있지 않아 실데이터로 검증하지 못했다. 픽스처는 소스(gemini-cli
// `packages/core/src/services/chatRecordingService.ts`)의 리플레이 분기를 그대로 따라 만들었다.

const (
	sid  = "11111111-2222-3333-4444-555555555555"
	sid2 = "99999999-8888-7777-6666-555555555555"
)

func meta(id string) string {
	return `{"sessionId":"` + id + `","projectHash":"abc","startTime":"2026-01-02T03:00:00.000Z","lastUpdated":"2026-01-02T04:00:00.000Z"}`
}

// geminiMsg 는 과금 턴 한 줄이다(tokens 는 input/output/cached/thoughts 순).
func geminiMsg(id, ts, model string, in, out, cached, thoughts int64, extra string) string {
	s := `{"id":"` + id + `","timestamp":"` + ts + `","type":"gemini","content":"ok","model":"` + model + `",` +
		`"tokens":{"input":` + itoa(in) + `,"output":` + itoa(out) + `,"cached":` + itoa(cached) +
		`,"thoughts":` + itoa(thoughts) + `,"tool":7,"total":0}`
	if extra != "" {
		s += "," + extra
	}
	return s + "}"
}

func userMsg(id, ts, text string) string {
	return `{"id":"` + id + `","timestamp":"` + ts + `","type":"user","content":` + jsonStr(text) + `}`
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// parse 는 줄들을 한 파일로 붙여 한 세션으로 접는다.
func parse(t *testing.T, path string, lines ...string) payload.Session {
	t.Helper()
	a := New()
	if err := a.AddFileAt(path, "fallback-key-0001", strings.NewReader(strings.Join(lines, "\n")+"\n")); err != nil {
		t.Fatalf("AddFileAt: %v", err)
	}
	ss := a.Sessions()
	if len(ss) != 1 {
		t.Fatalf("세션 1개를 기대했는데 %d개다: %+v", len(ss), ss)
	}
	return ss[0]
}

const fixPath = "/x/.gemini/tmp/user-usage/chats/session-2026-01-02T03-00-abc12345.jsonl"

// ── 함정 1: $rewindTo 가 존재하는 id → 그 id 를 **포함해** 뒤를 지운다 ──────────

func TestRewindToExistingID(t *testing.T) {
	s := parse(t, fixPath,
		meta(sid),
		geminiMsg("m1", "2026-01-02T03:10:00.000Z", "gemini-3-pro", 100, 10, 0, 0, ""),
		geminiMsg("m2", "2026-01-02T03:20:00.000Z", "gemini-3-pro", 200, 20, 0, 0, ""),
		geminiMsg("m3", "2026-01-02T03:30:00.000Z", "gemini-3-pro", 400, 40, 0, 0, ""),
		`{"$rewindTo":"m2"}`,
	)
	// m2·m3 가 사라지고 m1 만 남는다.
	if s.Input != 100 || s.Output != 10 || s.Turns != 1 {
		t.Fatalf("m1 만 남아야 한다: input=%d output=%d turns=%d", s.Input, s.Output, s.Turns)
	}
}

// ── 함정 2: $rewindTo 가 없는 id → **전부** 삭제 ──────────────────────────────

func TestRewindToMissingIDClearsEverything(t *testing.T) {
	a := New()
	lines := strings.Join([]string{
		meta(sid),
		geminiMsg("m1", "2026-01-02T03:10:00.000Z", "gemini-3-pro", 100, 10, 0, 0, ""),
		geminiMsg("m2", "2026-01-02T03:20:00.000Z", "gemini-3-pro", 200, 20, 0, 0, ""),
		`{"$rewindTo":"nope"}`,
	}, "\n")
	if err := a.AddFileAt(fixPath, "fallback-key-0001", strings.NewReader(lines)); err != nil {
		t.Fatalf("AddFileAt: %v", err)
	}
	// 메시지가 전부 사라졌으므로 신호 없는 세션 → 아예 내지 않는다.
	if ss := a.Sessions(); len(ss) != 0 {
		t.Fatalf("전부 삭제되어 세션이 없어야 한다: %+v", ss)
	}
}

// ── 함정 3: $set.messages 체크포인트 → 목록 전체 교체(누적이 아니다) ──────────

func TestSetMessagesCheckpointReplaces(t *testing.T) {
	s := parse(t, fixPath,
		meta(sid),
		geminiMsg("m1", "2026-01-02T03:10:00.000Z", "gemini-3-pro", 1000, 100, 0, 0, ""),
		geminiMsg("m2", "2026-01-02T03:20:00.000Z", "gemini-3-pro", 2000, 200, 0, 0, ""),
		`{"$set":{"messages":[`+geminiMsg("c1", "2026-01-02T05:00:00.000Z", "gemini-3-flash", 7, 3, 0, 0, "")+`]}}`,
	)
	if s.Input != 7 || s.Output != 3 || s.Turns != 1 {
		t.Fatalf("체크포인트 배열로 통째 교체돼야 한다: input=%d output=%d turns=%d", s.Input, s.Output, s.Turns)
	}
	if s.Model != "gemini-3-flash" {
		t.Fatalf("모델=%q", s.Model)
	}
}

// ── 함정 4: $set 으로 sessionId 변경 → 파일명이 아니라 최종 메타 값 ───────────

func TestSetChangesSessionID(t *testing.T) {
	s := parse(t, fixPath,
		meta(sid),
		geminiMsg("m1", "2026-01-02T03:10:00.000Z", "gemini-3-pro", 10, 1, 0, 0, ""),
		`{"$set":{"sessionId":"`+sid2+`"}}`,
	)
	if s.ID != sid2 {
		t.Fatalf("최종 메타의 sessionId 를 써야 한다: %q (기대 %q)", s.ID, sid2)
	}
	// $set 에 startTime 이 없으므로 이전 값이 유지돼야 한다(얕은 병합).
	if s.StartedAt != "2026-01-02T03:00:00.000Z" {
		t.Fatalf("$set 이 없는 키를 지우면 안 된다: startedAt=%q", s.StartedAt)
	}
}

// ── 함정 5: 같은 id 재등장 → 뒤엣것이 이긴다(순서는 첫 등장 자리를 지킨다) ────

func TestDuplicateIDLastWins(t *testing.T) {
	s := parse(t, fixPath,
		meta(sid),
		geminiMsg("m1", "2026-01-02T03:10:00.000Z", "gemini-3-pro", 100, 10, 0, 0, ""),
		geminiMsg("m1", "2026-01-02T03:11:00.000Z", "gemini-3-pro", 555, 55, 0, 0, ""),
	)
	if s.Input != 555 || s.Output != 55 || s.Turns != 1 {
		t.Fatalf("뒤엣것이 이겨야 한다(이중 계상 금지): input=%d output=%d turns=%d", s.Input, s.Output, s.Turns)
	}
}

// 같은 id 를 다시 넣어도 **순서는 첫 등장 자리**를 지킨다(JS Map 의미론).
// 이게 어긋나면 $rewindTo 가 지우는 범위가 통째로 달라진다.
func TestDuplicateIDKeepsFirstPosition(t *testing.T) {
	s := parse(t, fixPath,
		meta(sid),
		geminiMsg("m1", "2026-01-02T03:10:00.000Z", "gemini-3-pro", 100, 10, 0, 0, ""),
		geminiMsg("m2", "2026-01-02T03:20:00.000Z", "gemini-3-pro", 200, 20, 0, 0, ""),
		geminiMsg("m1", "2026-01-02T03:30:00.000Z", "gemini-3-pro", 111, 11, 0, 0, ""),
		`{"$rewindTo":"m2"}`, // m1 이 앞자리를 지켰다면 m2 만 지워지고 m1(갱신본)이 남는다
	)
	if s.Input != 111 || s.Turns != 1 {
		t.Fatalf("m1 갱신본만 남아야 한다: input=%d turns=%d", s.Input, s.Turns)
	}
}

// ── 함정 6: *.unreadable-* / *.tmp-* 는 스캔 제외 ─────────────────────────────

func TestMatchExcludesSidecarFiles(t *testing.T) {
	base := "/x/.gemini/tmp/user-usage/chats/"
	cases := []struct {
		path string
		want bool
	}{
		{base + "session-2026-01-02T03-00-abc12345.jsonl", true},
		{base + "session-2026-01-02T03-00-abc12345.json", true}, // 레거시
		{base + "session-2026-01-02T03-00-abc12345.jsonl.unreadable-1767325200000", false},
		{base + "session-2026-01-02T03-00-abc12345.jsonl.tmp-91234", false},
		{base + sid + "/" + sid2 + ".jsonl", true}, // 서브에이전트
		{base + "notes.txt", false},
		{"/x/.gemini/tmp/user-usage/checkpoints/snap.json", false}, // chats/ 밖
		{"/x/.gemini/tmp/user-usage/logs/a/b/c.json", false},
	}
	for _, c := range cases {
		if got := Match(c.path); got != c.want {
			t.Errorf("Match(%q)=%v, 기대 %v", c.path, got, c.want)
		}
	}
}

func TestSessionKeyAndProjectFromPath(t *testing.T) {
	main := "/x/.gemini/tmp/user-usage/chats/session-2026-01-02T03-00-abc12345.jsonl"
	legacy := "/x/.gemini/tmp/user-usage/chats/session-2026-01-02T03-00-abc12345.json"
	sub := "/x/.gemini/tmp/user-usage/chats/" + sid + "/" + sid2 + ".jsonl"

	if k := SessionKeyFromPath(main); k != "session-2026-01-02T03-00-abc12345" {
		t.Errorf("메인 그룹 키=%q", k)
	}
	// 레거시 `.json` 과 현행 `.jsonl` 은 **같은 그룹**이어야 한다(둘 다 같이 재파싱되도록).
	if SessionKeyFromPath(legacy) != SessionKeyFromPath(main) {
		t.Errorf(".json 과 .jsonl 이 다른 그룹이다: %q vs %q", SessionKeyFromPath(legacy), SessionKeyFromPath(main))
	}
	if k := SessionKeyFromPath(sub); k != sid {
		t.Errorf("서브에이전트 그룹 키는 부모 세션 id 여야 한다: %q", k)
	}
	if p := ProjectFromPath(main); p != "user-usage" {
		t.Errorf("project=%q", p)
	}
	if p := ProjectFromPath(sub); p != "user-usage" {
		t.Errorf("서브에이전트 project=%q", p)
	}
	// 구버전 해시 디렉터리는 사람이 읽을 이름이 아니다 → 비운다.
	hashed := "/x/.gemini/tmp/" + strings.Repeat("a", 64) + "/chats/session-x.jsonl"
	if p := ProjectFromPath(hashed); p != "" {
		t.Errorf("해시 디렉터리는 project 로 쓰면 안 된다: %q", p)
	}
}

// ── 함정 7: 레거시 .json + .jsonl 공존 → sessionId 로 병합, 이중 계상 없음 ────

func TestLegacyJSONAndJSONLMerge(t *testing.T) {
	legacyPath := "/x/.gemini/tmp/user-usage/chats/session-2026-01-02T03-00-abc12345.json"
	// 레거시는 **한 덩어리 JSON**(여러 줄로 예쁘게 들여쓸 수 있다).
	legacy := "{\n  \"sessionId\": \"" + sid + "\",\n  \"projectHash\": \"abc\",\n" +
		"  \"startTime\": \"2026-01-02T03:00:00.000Z\",\n  \"lastUpdated\": \"2026-01-02T04:00:00.000Z\",\n" +
		"  \"messages\": [\n" + geminiMsg("m1", "2026-01-02T03:10:00.000Z", "gemini-3-pro", 100, 10, 0, 0, "") + "\n  ]\n}\n"

	jsonl := strings.Join([]string{
		meta(sid),
		geminiMsg("m1", "2026-01-02T03:10:00.000Z", "gemini-3-pro", 100, 10, 0, 0, ""), // 같은 메시지
		geminiMsg("m2", "2026-01-02T03:20:00.000Z", "gemini-3-pro", 200, 20, 0, 0, ""), // 새 메시지
	}, "\n")

	a := New()
	if err := a.AddFileAt(legacyPath, "session-2026-01-02T03-00-abc12345", strings.NewReader(legacy)); err != nil {
		t.Fatalf("legacy: %v", err)
	}
	if err := a.AddFileAt(fixPath, "session-2026-01-02T03-00-abc12345", strings.NewReader(jsonl)); err != nil {
		t.Fatalf("jsonl: %v", err)
	}
	ss := a.Sessions()
	if len(ss) != 1 {
		t.Fatalf("sessionId 로 한 세션에 합쳐져야 한다: %d개", len(ss))
	}
	// m1 이 두 파일에 다 있지만 **한 번만** 센다.
	if ss[0].Input != 300 || ss[0].Output != 30 || ss[0].Turns != 2 {
		t.Fatalf("이중 계상: input=%d output=%d turns=%d (기대 300/30/2)", ss[0].Input, ss[0].Output, ss[0].Turns)
	}
}

// ── 함정 8: 토큰 정규화 ───────────────────────────────────────────────────────

func TestTokenNormalization(t *testing.T) {
	// input(1000) 은 cached(400) 를 포함한다 → Input=600, CacheRead=400.
	// Output = output(50) + thoughts(120) = 170.  CacheCreate 는 항상 0.
	s := parse(t, fixPath,
		meta(sid),
		geminiMsg("m1", "2026-01-02T03:10:00.000Z", "gemini-3-pro", 1000, 50, 400, 120, ""),
	)
	if s.Input != 600 {
		t.Errorf("Input=%d, 기대 600 (input-cached)", s.Input)
	}
	if s.CacheRead != 400 {
		t.Errorf("CacheRead=%d, 기대 400", s.CacheRead)
	}
	if s.Output != 170 {
		t.Errorf("Output=%d, 기대 170 (output+thoughts)", s.Output)
	}
	if s.CacheCreate != 0 {
		t.Errorf("CacheCreate=%d, Gemini 는 캐시 쓰기 과금이 해당 없음이라 항상 0", s.CacheCreate)
	}
	// tokens.tool(7) 은 어디에도 더하지 않는다.
	if s.Input+s.Output+s.CacheRead != 600+170+400 {
		t.Errorf("tokens.tool 을 더하면 안 된다")
	}
	// 시계열 합계는 세션 합계와 같아야 한다(대시보드가 두 말을 하지 않도록).
	var bi, bo, bc int64
	for _, b := range s.Series {
		bi += b.Input
		bo += b.Output
		bc += b.CacheRead
	}
	if bi != s.Input || bo != s.Output || bc != s.CacheRead {
		t.Errorf("series 합(%d/%d/%d) != 세션 합(%d/%d/%d)", bi, bo, bc, s.Input, s.Output, s.CacheRead)
	}
}

// 방어: cached 가 input 보다 큰 이상 데이터에서 음수 토큰을 내지 않는다.
func TestTokenNormalizationCachedExceedsInput(t *testing.T) {
	s := parse(t, fixPath,
		meta(sid),
		geminiMsg("m1", "2026-01-02T03:10:00.000Z", "gemini-3-pro", 100, 10, 900, 0, ""),
	)
	if s.Input != 0 {
		t.Fatalf("Input=%d, 음수가 아니라 0 이어야 한다", s.Input)
	}
	if s.CacheRead != 900 {
		t.Fatalf("CacheRead=%d, 기대 900", s.CacheRead)
	}
}

// 모델이 세션 중에 바뀌면 시간×모델 버킷이 갈라진다.
func TestModelSplitBuckets(t *testing.T) {
	s := parse(t, fixPath,
		meta(sid),
		geminiMsg("m1", "2026-01-02T03:10:00.000Z", "gemini-3-pro", 100, 10, 0, 0, ""),
		geminiMsg("m2", "2026-01-02T03:40:00.000Z", "gemini-3-flash", 300, 30, 0, 0, ""),
	)
	if len(s.Series) != 2 {
		t.Fatalf("버킷 2개(같은 시각·다른 모델)를 기대: %+v", s.Series)
	}
	if s.Series[0].Model != "gemini-3-flash" || s.Series[1].Model != "gemini-3-pro" {
		t.Fatalf("버킷 정렬이 결정적이지 않다: %+v", s.Series)
	}
	if s.Model != "gemini-3-flash" { // 토큰 합이 더 큰 쪽이 대표 모델
		t.Fatalf("대표 모델=%q, 기대 gemini-3-flash", s.Model)
	}
}

// ── 함정 9: 서브에이전트 파일은 부모 세션에 합산된다 ─────────────────────────

func TestSubagentFoldsIntoParent(t *testing.T) {
	parentPath := "/x/.gemini/tmp/user-usage/chats/session-2026-01-02T03-00-" + sid[:8] + ".jsonl"
	subPath := "/x/.gemini/tmp/user-usage/chats/" + sid + "/" + sid2 + ".jsonl"

	parent := strings.Join([]string{
		meta(sid),
		geminiMsg("p1", "2026-01-02T03:10:00.000Z", "gemini-3-pro", 100, 10, 0, 0,
			`"toolCalls":[{"id":"t1","name":"invoke_agent","args":{"agent_name":"code-reviewer","prompt":"secret prompt"},"status":"success","timestamp":"2026-01-02T03:10:00.000Z","agentId":"7b1f1a9c-0000-4000-8000-000000000000"}]`),
	}, "\n")
	sub := strings.Join([]string{
		`{"sessionId":"` + sid2 + `","projectHash":"abc","startTime":"2026-01-02T03:11:00.000Z","lastUpdated":"2026-01-02T03:12:00.000Z","kind":"subagent"}`,
		geminiMsg("s1", "2026-01-02T03:11:00.000Z", "gemini-3-pro", 500, 50, 0, 0, ""),
	}, "\n")

	a := New()
	if err := a.AddFileAt(parentPath, SessionKeyFromPath(parentPath), strings.NewReader(parent)); err != nil {
		t.Fatalf("parent: %v", err)
	}
	if err := a.AddFileAt(subPath, SessionKeyFromPath(subPath), strings.NewReader(sub)); err != nil {
		t.Fatalf("sub: %v", err)
	}
	ss := a.Sessions()
	if len(ss) != 1 {
		t.Fatalf("서브에이전트는 부모 세션에 합산돼야 한다(별도 세션 금지): %d개 %+v", len(ss), ss)
	}
	s := ss[0]
	if s.ID != sid {
		t.Fatalf("세션 id=%q, 기대 부모 %q", s.ID, sid)
	}
	if s.Input != 600 || s.Output != 60 || s.Turns != 2 {
		t.Fatalf("합산 실패: input=%d output=%d turns=%d (기대 600/60/2)", s.Input, s.Output, s.Turns)
	}
	// agent 축은 랜덤 uuid(agentId)가 아니라 **에이전트 이름**이어야 한다.
	if got := s.Counters["agent"]["code-reviewer"]; got != 1 {
		t.Fatalf("agent 축=%v, 기대 {code-reviewer:1}", s.Counters["agent"])
	}
	for k := range s.Counters["agent"] {
		if strings.Contains(k, "7b1f1a9c") {
			t.Fatalf("agentId(uuid)가 agent 축에 샜다: %q", k)
		}
	}
}

// ── 함정 10: 깨진 JSON 줄은 **그 줄만** 버리고 나머지는 살린다 ───────────────

func TestBrokenLineSkippedOnly(t *testing.T) {
	s := parse(t, fixPath,
		meta(sid),
		geminiMsg("m1", "2026-01-02T03:10:00.000Z", "gemini-3-pro", 100, 10, 0, 0, ""),
		`{"id":"m2","timestamp":"2026-01-02T03:2`, // 잘린 줄
		geminiMsg("m3", "2026-01-02T03:30:00.000Z", "gemini-3-pro", 200, 20, 0, 0, ""),
	)
	if s.Input != 300 || s.Turns != 2 {
		t.Fatalf("깨진 줄 하나로 파일 전체를 버리면 안 된다: input=%d turns=%d", s.Input, s.Turns)
	}
}

// ── 7축 매핑 ──────────────────────────────────────────────────────────────────

func TestAxisMapping(t *testing.T) {
	longMCP := "mcp_a_very_long_server_name_that_gets_truncated_in_the..._middle_tool"
	s := parse(t, fixPath,
		meta(sid),
		userMsg("u1", "2026-01-02T03:05:00.000Z", "리팩터링 파서 토큰 정규화를 확인한다"),
		geminiMsg("m1", "2026-01-02T03:10:00.000Z", "gemini-3-pro", 100, 10, 0, 0,
			`"toolCalls":[`+strings.Join([]string{
				`{"id":"t1","name":"run_shell_command","args":{"command":"cd /Users/secret/dir && pytest -k auth --token=hunter2"},"status":"success","timestamp":"2026-01-02T03:10:00.000Z"}`,
				`{"id":"t2","name":"activate_skill","args":{"name":"loop-engineering"},"status":"success","timestamp":"2026-01-02T03:10:01.000Z"}`,
				`{"id":"t3","name":"read_file","args":{"path":"/Users/secret/x.go"},"status":"success","timestamp":"2026-01-02T03:10:02.000Z"}`,
				`{"id":"t4","name":"` + longMCP + `","args":{},"status":"success","timestamp":"2026-01-02T03:10:03.000Z"}`,
				`{"id":"t5","name":"google_web_search","args":{"query":"x"},"status":"success","timestamp":"2026-01-02T03:10:04.000Z"}`,
				`{"id":"t6","name":"web_fetch","args":{"prompt":"x"},"status":"success","timestamp":"2026-01-02T03:10:05.000Z"}`,
			}, ",")+`]`),
	)

	// bash 축은 **선두 실행파일명 하나만** — 경로도 인자도 남으면 안 된다.
	if got := s.Counters["bash"]; len(got) != 1 || got["pytest"] != 1 {
		t.Fatalf("bash 축=%v, 기대 {pytest:1}", got)
	}
	for k := range s.Counters["bash"] {
		if strings.Contains(k, "secret") || strings.Contains(k, "hunter2") || strings.Contains(k, "/") {
			t.Fatalf("bash 축에 인자·경로가 샜다: %q", k)
		}
	}
	if s.Counters["skill"]["loop-engineering"] != 1 {
		t.Fatalf("skill 축=%v", s.Counters["skill"])
	}
	// MCP 는 mcp 축에만 — tool 축에 겹쳐 세면 도구 카운트가 부풀고, 이름은 **원문 그대로** 쓴다.
	if s.Counters["mcp"][longMCP] != 1 {
		t.Fatalf("mcp 축=%v, 기대 키 %q", s.Counters["mcp"], longMCP)
	}
	if _, dup := s.Counters["tool"][longMCP]; dup {
		t.Fatalf("MCP 가 tool 축에 겹쳐 세어졌다: %v", s.Counters["tool"])
	}
	if s.Counters["tool"]["read_file"] != 1 || s.Counters["tool"]["run_shell_command"] != 1 ||
		s.Counters["tool"]["activate_skill"] != 1 {
		t.Fatalf("tool 축=%v", s.Counters["tool"])
	}
	if s.WebSearch != 1 || s.WebFetch != 1 {
		t.Fatalf("webSearch=%d webFetch=%d, 기대 1/1", s.WebSearch, s.WebFetch)
	}
	// keyword 축은 사용자 메시지에서만, policy 필터를 통과한 낱말만.
	if s.Counters["keyword"]["리팩터링"] != 1 {
		t.Fatalf("keyword 축=%v", s.Counters["keyword"])
	}
	// slash 축은 **아예 만들지 않는다**(0 을 보내면 서버가 "안 썼다"로 읽는다).
	if _, ok := s.Counters["slash"]; ok {
		t.Fatalf("slash 축을 만들면 안 된다: %v", s.Counters["slash"])
	}
	if s.Platform != Platform {
		t.Fatalf("platform=%q", s.Platform)
	}
	if s.Project != "user-usage" {
		t.Fatalf("project=%q", s.Project)
	}
}

// 사용자 메시지가 파트 배열이어도 텍스트를 뽑는다(PartListUnion).
func TestUserContentPartArray(t *testing.T) {
	s := parse(t, fixPath,
		meta(sid),
		`{"id":"u1","timestamp":"2026-01-02T03:05:00.000Z","type":"user","content":[{"text":"파서 리팩터링"},{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}`,
		geminiMsg("m1", "2026-01-02T03:10:00.000Z", "gemini-3-pro", 10, 1, 0, 0, ""),
	)
	if s.Counters["keyword"]["리팩터링"] != 1 {
		t.Fatalf("파트 배열에서 텍스트를 못 뽑았다: %v", s.Counters["keyword"])
	}
}

// 하네스가 끼워 넣은 블록(`<`로 시작)은 keyword 축에서 통째로 버린다.
func TestInjectedUserBlockDropped(t *testing.T) {
	s := parse(t, fixPath,
		meta(sid),
		userMsg("u1", "2026-01-02T03:05:00.000Z", "<system-reminder>비밀 경로 정보</system-reminder>"),
		geminiMsg("m1", "2026-01-02T03:10:00.000Z", "gemini-3-pro", 10, 1, 0, 0, ""),
	)
	if len(s.Counters["keyword"]) != 0 {
		t.Fatalf("주입 블록이 keyword 축에 샜다: %v", s.Counters["keyword"])
	}
}

// 시각을 못 읽는 턴은 시계열에서 빠지되 합계에는 남고, noTsTurns 로 관측된다.
func TestNoTimestampTurnIsObserved(t *testing.T) {
	s := parse(t, fixPath,
		meta(sid),
		geminiMsg("m1", "", "gemini-3-pro", 100, 10, 0, 0, ""),
	)
	if s.Input != 100 || s.Turns != 1 {
		t.Fatalf("합계는 살아야 한다: input=%d turns=%d", s.Input, s.Turns)
	}
	if len(s.Series) != 0 {
		t.Fatalf("시계열에 올릴 자리가 없다: %+v", s.Series)
	}
	if s.NoTsTurns == nil || *s.NoTsTurns != 1 {
		t.Fatalf("noTsTurns=%v, 기대 1", s.NoTsTurns)
	}
}

// info/error/warning 메시지는 축에도 토큰에도 들어가지 않는다.
func TestNonConversationTypesIgnored(t *testing.T) {
	a := New()
	lines := strings.Join([]string{
		meta(sid),
		`{"id":"i1","timestamp":"2026-01-02T03:05:00.000Z","type":"info","content":"세션 시작"}`,
		`{"id":"e1","timestamp":"2026-01-02T03:06:00.000Z","type":"error","content":"실패"}`,
	}, "\n")
	if err := a.AddFileAt(fixPath, "fallback-key-0001", strings.NewReader(lines)); err != nil {
		t.Fatalf("AddFileAt: %v", err)
	}
	if ss := a.Sessions(); len(ss) != 0 {
		t.Fatalf("신호 없는 세션은 내지 않는다: %+v", ss)
	}
}

// 세션 id 가 서버 모양에 안 맞으면 폴백(그룹 키)으로 떨어지고, 그것도 안 맞으면 버린다.
func TestInvalidSessionIDFallsBack(t *testing.T) {
	a := New()
	lines := strings.Join([]string{
		`{"sessionId":"x","projectHash":"abc","startTime":"2026-01-02T03:00:00.000Z"}`, // 8자 미만 → 서버 거부 모양
		geminiMsg("m1", "2026-01-02T03:10:00.000Z", "gemini-3-pro", 10, 1, 0, 0, ""),
	}, "\n")
	if err := a.AddFileAt(fixPath, "session-2026-01-02T03-00-abc12345", strings.NewReader(lines)); err != nil {
		t.Fatalf("AddFileAt: %v", err)
	}
	ss := a.Sessions()
	if len(ss) != 1 || ss[0].ID != "session-2026-01-02T03-00-abc12345" {
		t.Fatalf("그룹 키로 떨어져야 한다: %+v", ss)
	}
}

// ── LOC(추가/삭제 줄 수) ───────────────────────────────────────────────────────
//
// 규율은 Claude 파서(internal/transcript.editUse)와 **같은 모양의 입력, 같은 식**으로 맞춘다:
//
//	write_file → args.content    의 줄 수를 **추가**로만 센다(원본이 인자에 없어 삭제는 알 수 없다)
//	replace    → args.new_string = 추가, args.old_string = 삭제
//
// 줄 세는 식도 Claude 와 글자 그대로 같다 — strings.Count(s,"\n")+1, 빈 문자열은 0.
//
// **축 두 개의 의미가 다르다**(gemini.go editUse 주석과 같은 말):
//
//	linesAdded/Removed     = 제안된 편집 **규모**. 거부·실패분도 포함해 센다(상태로 거르지 않는다).
//	editsAccepted/Rejected = **결과**. 종료 상태로만 판정한다.
//
// 아래 표의 error·cancelled 행이 그 구분을 못박는다 — 줄 수는 세어지고 rejected 로 잡힌다.

// toolCall 은 toolCalls 원소 한 개다.
func toolCall(id, name, status, args string) string {
	return `{"id":"` + id + `","name":"` + name + `","args":` + args +
		`,"status":"` + status + `","timestamp":"2026-01-02T03:10:00.000Z"}`
}

// withTools 는 과금 턴 하나에 붙일 toolCalls 조각이다.
func withTools(calls ...string) string {
	return `"toolCalls":[` + strings.Join(calls, ",") + `]`
}

func writeArgs(content string) string {
	return `{"file_path":"/Users/secret/a.go","content":` + jsonStr(content) + `}`
}

func replaceArgs(oldS, newS string) string {
	return `{"file_path":"/Users/secret/a.go","old_string":` + jsonStr(oldS) +
		`,"new_string":` + jsonStr(newS) + `}`
}

// locOf 는 편집 도구 호출들만 실은 세션 하나를 접어 LOC 네 값을 낸다.
func locOf(t *testing.T, calls ...string) payload.Session {
	t.Helper()
	return parse(t, fixPath,
		meta(sid),
		geminiMsg("m1", "2026-01-02T03:10:00.000Z", "gemini-3-pro", 100, 10, 0, 0, withTools(calls...)),
	)
}

// 완료조건 5 — 도구별 픽스처와 기대 줄 수의 대조표. -v 로 실제 값을 찍는다.
func TestLOCFixtureTable(t *testing.T) {
	cases := []struct {
		name                               string
		call                               string
		wantAdd, wantDel, wantAcc, wantRej int64
	}{
		{"write_file 3줄(끝 개행 없음)", toolCall("t1", "write_file", "success", writeArgs("a\nb\nc")), 3, 0, 1, 0},
		{"write_file 끝 개행 포함", toolCall("t1", "write_file", "success", writeArgs("a\nb\n")), 3, 0, 1, 0},
		{"write_file 1줄", toolCall("t1", "write_file", "success", writeArgs("a")), 1, 0, 1, 0},
		{"write_file 빈 내용", toolCall("t1", "write_file", "success", writeArgs("")), 0, 0, 1, 0},
		{"write_file content 누락", toolCall("t1", "write_file", "success", `{"file_path":"/x/a.go"}`), 0, 0, 1, 0},
		{"replace 2줄→3줄", toolCall("t1", "replace", "success", replaceArgs("x\ny", "p\nq\nr")), 3, 2, 1, 0},
		{"replace 신규파일(old 빈값)", toolCall("t1", "replace", "success", replaceArgs("", "p\nq")), 2, 0, 1, 0},
		{"replace 삭제만(new 빈값)", toolCall("t1", "replace", "success", replaceArgs("x\ny\nz", "")), 0, 3, 1, 0},
		{"replace 인자 없음", toolCall("t1", "replace", "success", `{}`), 0, 0, 1, 0},
		// ↓ 규모는 세고 결과만 거부로 잡는 행들. 성공 게이트를 다시 넣으면 여기서 깨진다.
		{"write_file 실패(error)", toolCall("t1", "write_file", "error", writeArgs("a\nb\nc")), 3, 0, 0, 1},
		{"replace 사용자 거부(cancelled)", toolCall("t1", "replace", "cancelled", replaceArgs("x\ny", "p")), 1, 2, 0, 1},
		// ↓ 종료 상태가 아니면 결과는 **모르는 것**이라 어느 쪽도 안 센다. 규모는 그래도 센다.
		{"종료 상태 아님(executing)", toolCall("t1", "write_file", "executing", writeArgs("a\nb")), 2, 0, 0, 0},
		{"status 필드 없음", `{"id":"t1","name":"write_file","args":` + writeArgs("a\nb") + `}`, 2, 0, 0, 0},
		{"편집 도구 아님(read_file)", toolCall("t1", "read_file", "success", `{"path":"/x/a.go","content":"a\nb\nc"}`), 0, 0, 0, 0},
		{"편집 도구 아님(run_shell_command)", toolCall("t1", "run_shell_command", "success", `{"command":"git apply p.diff"}`), 0, 0, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := locOf(t, c.call)
			t.Logf("%-34s → linesAdded=%d linesRemoved=%d editsAccepted=%d editsRejected=%d (기대 %d/%d/%d/%d)",
				c.name, s.LinesAdded, s.LinesRemoved, s.EditsAccepted, s.EditsRejected,
				c.wantAdd, c.wantDel, c.wantAcc, c.wantRej)
			if s.LinesAdded != c.wantAdd || s.LinesRemoved != c.wantDel ||
				s.EditsAccepted != c.wantAcc || s.EditsRejected != c.wantRej {
				t.Fatalf("불일치: got %d/%d/%d/%d, want %d/%d/%d/%d",
					s.LinesAdded, s.LinesRemoved, s.EditsAccepted, s.EditsRejected,
					c.wantAdd, c.wantDel, c.wantAcc, c.wantRej)
			}
		})
	}
}

// 여러 편집 호출이 한 턴에 붙으면 합산된다. 편집 도구는 tool 축에도 그대로 실린다.
func TestLOCAccumulatesAndKeepsToolAxis(t *testing.T) {
	s := locOf(t,
		toolCall("t1", "write_file", "success", writeArgs("a\nb\nc")),
		toolCall("t2", "replace", "success", replaceArgs("x", "p\nq")),
		toolCall("t3", "replace", "error", replaceArgs("zzz\nzzz", "y")),
	)
	// t3 는 실패했지만 **규모에는 들어간다**(added +1, removed +2) — 결과 축에서만 거부로 빠진다.
	if s.LinesAdded != 6 || s.LinesRemoved != 3 {
		t.Fatalf("linesAdded=%d linesRemoved=%d, 기대 6/3", s.LinesAdded, s.LinesRemoved)
	}
	if s.EditsAccepted != 2 || s.EditsRejected != 1 {
		t.Fatalf("editsAccepted=%d editsRejected=%d, 기대 2/1", s.EditsAccepted, s.EditsRejected)
	}
	if s.Counters["tool"]["write_file"] != 1 || s.Counters["tool"]["replace"] != 2 {
		t.Fatalf("tool 축=%v, 기대 write_file:1 replace:2", s.Counters["tool"])
	}
}

// 되돌린($rewindTo) 편집은 세지 않는다 — LOC 도 리플레이 **확정 후**에 접혀야 한다.
func TestLOCRewoundEditsNotCounted(t *testing.T) {
	s := parse(t, fixPath,
		meta(sid),
		geminiMsg("m1", "2026-01-02T03:10:00.000Z", "gemini-3-pro", 100, 10, 0, 0,
			withTools(toolCall("t1", "write_file", "success", writeArgs("a\nb\nc")))),
		geminiMsg("m2", "2026-01-02T03:20:00.000Z", "gemini-3-pro", 100, 10, 0, 0,
			withTools(toolCall("t2", "write_file", "success", writeArgs("d\ne\nf\ng")))),
		`{"$rewindTo":"m2"}`,
	)
	if s.LinesAdded != 3 || s.EditsAccepted != 1 {
		t.Fatalf("되돌린 편집이 남았다: linesAdded=%d editsAccepted=%d, 기대 3/1", s.LinesAdded, s.EditsAccepted)
	}
}

// 같은 세션의 레거시 .json 과 .jsonl 이 같은 메시지를 들고 있어도 LOC 가 두 배가 되면 안 된다
// (메시지 id 로 합치는 기존 규율이 LOC 에도 그대로 적용된다).
func TestLOCNotDoubleCountedAcrossFiles(t *testing.T) {
	line := geminiMsg("m1", "2026-01-02T03:10:00.000Z", "gemini-3-pro", 100, 10, 0, 0,
		withTools(toolCall("t1", "replace", "success", replaceArgs("x\ny", "p\nq\nr"))))
	a := New()
	for _, p := range []string{
		"/x/.gemini/tmp/user-usage/chats/session-2026-01-02T03-00-abc12345.json",
		"/x/.gemini/tmp/user-usage/chats/session-2026-01-02T03-00-abc12345.jsonl",
	} {
		if err := a.AddFileAt(p, "session-2026-01-02T03-00-abc12345",
			strings.NewReader(meta(sid)+"\n"+line+"\n")); err != nil {
			t.Fatalf("AddFileAt: %v", err)
		}
	}
	ss := a.Sessions()
	if len(ss) != 1 {
		t.Fatalf("세션 1개를 기대했는데 %d개다", len(ss))
	}
	if ss[0].LinesAdded != 3 || ss[0].LinesRemoved != 2 {
		t.Fatalf("두 번 세어졌다: linesAdded=%d linesRemoved=%d, 기대 3/2", ss[0].LinesAdded, ss[0].LinesRemoved)
	}
}

// 인자 타입이 어긋나도(content 가 객체 등) **그 턴의 토큰이 사라지면 안 된다.**
// 편집 도구가 아닌 MCP 도구가 content 라는 이름의 객체 인자를 쓰는 일이 흔하다.
func TestLOCWeirdArgsDoNotDropTurn(t *testing.T) {
	s := parse(t, fixPath,
		meta(sid),
		geminiMsg("m1", "2026-01-02T03:10:00.000Z", "gemini-3-pro", 100, 10, 0, 0,
			withTools(
				`{"id":"t1","name":"mcp_srv_put","args":{"content":{"a":1},"command":42},"status":"success","timestamp":"2026-01-02T03:10:00.000Z"}`,
				`{"id":"t2","name":"write_file","args":"not-an-object","status":"success","timestamp":"2026-01-02T03:10:01.000Z"}`,
			)),
	)
	if s.Input != 100 || s.Output != 10 || s.Turns != 1 {
		t.Fatalf("이상한 인자 때문에 턴이 통째로 사라졌다: %+v", s)
	}
	if s.LinesAdded != 0 || s.LinesRemoved != 0 {
		t.Fatalf("문자열이 아닌 인자를 세면 안 된다: %d/%d", s.LinesAdded, s.LinesRemoved)
	}
	if s.Counters["mcp"]["mcp_srv_put"] != 1 {
		t.Fatalf("mcp 축=%v", s.Counters["mcp"])
	}
}

// 편집이 하나도 없으면 네 필드 모두 0 이고, omitempty 로 본문에서 아예 빠진다
// (구버전 수집기와 같은 모양 — 0 을 명시해 "관측했고 0"이라고 말하지 않는다).
func TestLOCAbsentWhenNoEdits(t *testing.T) {
	s := locOf(t, toolCall("t1", "read_file", "success", `{"path":"/x/a.go"}`))
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, k := range []string{"linesAdded", "linesRemoved", "editsAccepted", "editsRejected"} {
		if strings.Contains(string(b), k) {
			t.Fatalf("편집이 없는데 %s 가 본문에 실렸다: %s", k, b)
		}
	}
}
