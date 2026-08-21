package codex

import (
	"strings"
	"testing"

	"github.com/tscorp/user-usage/collector/internal/payload"
)

// 픽스처 헬퍼 — 줄들을 한 파일로 흘려 세션 하나를 얻는다.
func parse(t *testing.T, fallback string, lines ...string) payload.Session {
	t.Helper()
	a := New()
	if err := a.AddFile(fallback, strings.NewReader(strings.Join(lines, "\n")+"\n")); err != nil {
		t.Fatalf("AddFile: %v", err)
	}
	ss := a.Sessions()
	if len(ss) != 1 {
		t.Fatalf("세션 %d개, 1개를 기대했다: %+v", len(ss), ss)
	}
	return ss[0]
}

func tokenCount(ts string, in, cached, out, reasoning, total int64) string {
	return `{"timestamp":"` + ts + `","type":"event_msg","payload":{"type":"token_count","info":{` +
		`"total_token_usage":{"input_tokens":` + itoa(in) + `,"cached_input_tokens":` + itoa(cached) +
		`,"output_tokens":` + itoa(out) + `,"reasoning_output_tokens":` + itoa(reasoning) +
		`,"total_tokens":` + itoa(total) + `},` +
		`"last_token_usage":{"input_tokens":` + itoa(in) + `,"cached_input_tokens":` + itoa(cached) +
		`,"output_tokens":` + itoa(out) + `,"reasoning_output_tokens":` + itoa(reasoning) +
		`,"total_tokens":` + itoa(total) + `}}}}`
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

const metaLine = `{"timestamp":"2026-07-06T00:48:35.000Z","type":"session_meta","payload":{"id":"019f34e5-fe0e-7952-95fc-17b2c2c6215b","timestamp":"2026-07-06T00:48:35.000Z","cwd":"/Users/me/work/orca","originator":"Codex CLI","cli_version":"0.137.0","git":{"commit_hash":"abc123","branch":"main"}}}`

const ctxLine = `{"timestamp":"2026-07-06T00:48:36.000Z","type":"turn_context","payload":{"turn_id":"t-1","cwd":"/Users/me/work/orca","model":"gpt-5.5"}}`

// ── 토큰 (최대 위험 축) ───────────────────────────────────────────────────────

// total_token_usage 는 세션 누적이다. 합산하면 과대계상이 된다 — 마지막 값만 써야 한다.
func TestTokensUseLastCumulativeNotSum(t *testing.T) {
	s := parse(t, "fallback-id-0001", metaLine, ctxLine,
		tokenCount("2026-07-06T00:49:00.000Z", 1000, 600, 100, 40, 1100),
		tokenCount("2026-07-06T00:50:00.000Z", 3000, 2000, 250, 90, 3250),
	)

	// Input = input - cached = 3000-2000 = 1000
	if s.Input != 1000 {
		t.Errorf("Input=%d, want 1000 (input-cached, 누적 마지막 값)", s.Input)
	}
	if s.CacheRead != 2000 {
		t.Errorf("CacheRead=%d, want 2000", s.CacheRead)
	}
	// Output 은 reasoning 을 이미 포함한다 — 더하면 안 된다.
	if s.Output != 250 {
		t.Errorf("Output=%d, want 250 (reasoning 을 더하지 않는다)", s.Output)
	}
	// Codex 는 cache write 를 기록하지 않는다 — 0 을 위조하지 않고 관측 불가로 둔다.
	if s.CacheCreate != 0 {
		t.Errorf("CacheCreate=%d, want 0 (Codex 미관측)", s.CacheCreate)
	}
	// 총합이 total_tokens 와 같아야 한다(SQLite tokens_used 대조의 근거).
	if got := s.Input + s.CacheRead + s.Output; got != 3250 {
		t.Errorf("총 토큰=%d, want 3250 (= total_tokens)", got)
	}
}

// cached 가 input 을 넘는 이상 데이터에서도 음수를 내지 않는다.
func TestTokensNeverNegative(t *testing.T) {
	s := parse(t, "fallback-id-0001", metaLine, ctxLine,
		tokenCount("2026-07-06T00:49:00.000Z", 100, 500, 10, 0, 110),
	)
	if s.Input != 0 {
		t.Errorf("Input=%d, want 0 (max(0,…) 클램프)", s.Input)
	}
}

// info 가 없는 token_count(실데이터에 2건 있다)는 조용히 건너뛴다.
func TestTokenCountWithoutInfoIgnored(t *testing.T) {
	s := parse(t, "fallback-id-0001", metaLine, ctxLine,
		`{"timestamp":"2026-07-06T00:49:00.000Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"limit_id":"codex"}}}`,
		tokenCount("2026-07-06T00:50:00.000Z", 800, 300, 20, 5, 820),
	)
	if s.Input != 500 || s.CacheRead != 300 || s.Output != 20 {
		t.Errorf("in=%d cacheRead=%d out=%d, want 500/300/20", s.Input, s.CacheRead, s.Output)
	}
}

// 누적 카운터가 되감기면(리셋) 앞 스트림을 확정하고 새로 센다 — 뒤 값이 앞을 덮지 않는다.
func TestTokenCounterResetStartsNewStream(t *testing.T) {
	s := parse(t, "fallback-id-0001", metaLine, ctxLine,
		tokenCount("2026-07-06T00:49:00.000Z", 1000, 400, 100, 0, 1100),
		tokenCount("2026-07-06T00:50:00.000Z", 200, 50, 10, 0, 210), // 되감김
		tokenCount("2026-07-06T00:51:00.000Z", 500, 100, 30, 0, 530),
	)
	// 스트림1 최종(1100) + 스트림2 최종(530) = 1630
	if got := s.Input + s.CacheRead + s.Output; got != 1630 {
		t.Errorf("총 토큰=%d, want 1630 (1100+530)", got)
	}
}

// series 는 연속 total_token_usage 의 차분이며, 모델은 턴마다 바뀔 수 있다.
func TestSeriesSplitsByModelFromDeltas(t *testing.T) {
	s := parse(t, "fallback-id-0001", metaLine,
		`{"timestamp":"2026-07-06T00:00:00.000Z","type":"turn_context","payload":{"turn_id":"t-1","model":"gpt-5.5","cwd":"/w/orca"}}`,
		tokenCount("2026-07-06T00:10:00.000Z", 1000, 400, 100, 0, 1100),
		`{"timestamp":"2026-07-06T01:00:00.000Z","type":"turn_context","payload":{"turn_id":"t-2","model":"gpt-5.4-mini","cwd":"/w/orca"}}`,
		tokenCount("2026-07-06T01:10:00.000Z", 3000, 1400, 300, 0, 3300),
	)
	if len(s.Series) != 2 {
		t.Fatalf("버킷 %d개, 2개를 기대했다: %+v", len(s.Series), s.Series)
	}
	b0, b1 := s.Series[0], s.Series[1]
	if b0.Hour != "2026-07-06T00" || b0.Model != "gpt-5.5" {
		t.Errorf("버킷0=%s/%s", b0.Hour, b0.Model)
	}
	if b0.Input != 600 || b0.CacheRead != 400 || b0.Output != 100 {
		t.Errorf("버킷0 토큰 in=%d cr=%d out=%d, want 600/400/100", b0.Input, b0.CacheRead, b0.Output)
	}
	if b1.Hour != "2026-07-06T01" || b1.Model != "gpt-5.4-mini" {
		t.Errorf("버킷1=%s/%s", b1.Hour, b1.Model)
	}
	// 차분: input 3000-1000=2000, cached 1400-400=1000 → Input=1000
	if b1.Input != 1000 || b1.CacheRead != 1000 || b1.Output != 200 {
		t.Errorf("버킷1 토큰 in=%d cr=%d out=%d, want 1000/1000/200", b1.Input, b1.CacheRead, b1.Output)
	}
	// 버킷 합 == 세션 절대값 (series 와 총계가 갈라지면 대시보드가 서로 다른 말을 한다)
	var si, sc, so int64
	for _, b := range s.Series {
		si, sc, so = si+b.Input, sc+b.CacheRead, so+b.Output
	}
	if si != s.Input || sc != s.CacheRead || so != s.Output {
		t.Errorf("버킷 합(%d/%d/%d) != 세션 합계(%d/%d/%d)", si, sc, so, s.Input, s.CacheRead, s.Output)
	}
	// 대표 모델은 토큰이 큰 쪽
	if s.Model != "gpt-5.4-mini" {
		t.Errorf("Model=%q, want gpt-5.4-mini(토큰 최대)", s.Model)
	}
}

// 롤아웃 순서는 task_started → turn_context 다. 턴은 **자기 턴의 모델**로 귀속돼야 하고,
// 세션 첫 턴이 `(미상)` 으로 새면 안 된다.
func TestTurnAttributedToItsOwnModelNotPrevious(t *testing.T) {
	s := parse(t, "fallback-id-0001", metaLine,
		`{"timestamp":"2026-07-06T00:00:00.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t-1"}}`,
		`{"timestamp":"2026-07-06T00:00:01.000Z","type":"turn_context","payload":{"turn_id":"t-1","model":"gpt-5.5","cwd":"/w/orca"}}`,
		tokenCount("2026-07-06T00:10:00.000Z", 1000, 400, 100, 0, 1100),
		`{"timestamp":"2026-07-06T00:20:00.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t-2"}}`,
		`{"timestamp":"2026-07-06T00:20:01.000Z","type":"turn_context","payload":{"turn_id":"t-2","model":"gpt-5.4-mini","cwd":"/w/orca"}}`,
		tokenCount("2026-07-06T00:30:00.000Z", 1500, 500, 150, 0, 1650),
	)
	byModel := map[string]int64{}
	for _, b := range s.Series {
		byModel[b.Model] += b.Turns
	}
	if _, bad := byModel["(미상)"]; bad {
		t.Errorf("첫 턴이 (미상)으로 샜다: %+v", s.Series)
	}
	if byModel["gpt-5.5"] != 1 {
		t.Errorf("gpt-5.5 턴=%d, want 1: %+v", byModel["gpt-5.5"], s.Series)
	}
	// 직전 턴 모델(gpt-5.5)이 아니라 자기 턴 모델로 가야 한다.
	if byModel["gpt-5.4-mini"] != 1 {
		t.Errorf("gpt-5.4-mini 턴=%d, want 1: %+v", byModel["gpt-5.4-mini"], s.Series)
	}
	if s.Turns != 2 {
		t.Errorf("Turns=%d, want 2", s.Turns)
	}
}

// turn_context 가 끝내 안 와도 그 턴이 사라지지 않는다(마지막으로 알던 모델로 확정).
func TestPendingTurnFlushedAtEOF(t *testing.T) {
	s := parse(t, "fallback-id-0001", metaLine, ctxLine,
		tokenCount("2026-07-06T00:49:00.000Z", 10, 0, 5, 0, 15),
		`{"timestamp":"2026-07-06T00:50:00.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t-9"}}`,
	)
	var turns int64
	for _, b := range s.Series {
		turns += b.Turns
	}
	if turns != 1 {
		t.Errorf("버킷 턴 합=%d, want 1 (파일 끝에서 확정): %+v", turns, s.Series)
	}
}

// ── 세션 메타 ─────────────────────────────────────────────────────────────────

func TestSessionMeta(t *testing.T) {
	s := parse(t, "fallback-id-0001", metaLine, ctxLine,
		`{"timestamp":"2026-07-06T00:48:40.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t-1","started_at":1780726898}}`,
		`{"timestamp":"2026-07-06T00:52:40.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t-2"}}`,
		tokenCount("2026-07-06T00:53:00.000Z", 10, 0, 5, 0, 15),
	)
	if s.ID != "019f34e5-fe0e-7952-95fc-17b2c2c6215b" {
		t.Errorf("ID=%q, want session_meta.id", s.ID)
	}
	// 경로 원문은 저장하지 않는다 — basename 만(기존 Claude 규율과 동일).
	if s.Project != "orca" {
		t.Errorf("Project=%q, want orca(basename)", s.Project)
	}
	if strings.Contains(s.Project, "/") {
		t.Errorf("Project 에 경로가 샜다: %q", s.Project)
	}
	if s.Turns != 2 {
		t.Errorf("Turns=%d, want 2 (task_started 개수)", s.Turns)
	}
	if s.StartedAt != "2026-07-06T00:48:35.000Z" {
		t.Errorf("StartedAt=%q", s.StartedAt)
	}
	if s.EndedAt != "2026-07-06T00:53:00.000Z" {
		t.Errorf("EndedAt=%q", s.EndedAt)
	}
	if s.NoTsTurns == nil {
		t.Error("NoTsTurns 는 항상 값이 있어야 한다(nil=모른다 와 구분)")
	}
}

// session_meta 가 없으면 파일명에서 뽑은 id 로 떨어진다.
func TestFallbackSessionID(t *testing.T) {
	s := parse(t, "019f34e5-fe0e-7952-95fc-17b2c2c6215b", ctxLine,
		tokenCount("2026-07-06T00:49:00.000Z", 10, 0, 5, 0, 15))
	if s.ID != "019f34e5-fe0e-7952-95fc-17b2c2c6215b" {
		t.Errorf("ID=%q, want fallback", s.ID)
	}
}

func TestSessionIDFromPath(t *testing.T) {
	cases := map[string]string{
		"/x/2026/07/06/rollout-2026-07-06T09-48-35-019f34e5-fe0e-7952-95fc-17b2c2c6215b.jsonl": "019f34e5-fe0e-7952-95fc-17b2c2c6215b",
		"rollout-2026-07-06T09-48-35-019f34e5-fe0e-7952-95fc-17b2c2c6215b.jsonl":               "019f34e5-fe0e-7952-95fc-17b2c2c6215b",
		"/x/weird-name.jsonl": "weird-name",
	}
	for in, want := range cases {
		if got := SessionIDFromPath(in); got != want {
			t.Errorf("SessionIDFromPath(%q)=%q, want %q", in, got, want)
		}
	}
}

// 같은 세션 id 가 두 파일에 흩어져도 하나로 합쳐진다(과소집계 방지).
func TestSameSessionAcrossFilesMerges(t *testing.T) {
	a := New()
	_ = a.AddFile("019f34e5-fe0e-7952-95fc-17b2c2c6215b", strings.NewReader(
		metaLine+"\n"+ctxLine+"\n"+tokenCount("2026-07-06T00:49:00.000Z", 1000, 400, 100, 0, 1100)+"\n"))
	_ = a.AddFile("other-file-stem-9999", strings.NewReader(
		metaLine+"\n"+ctxLine+"\n"+tokenCount("2026-07-06T02:49:00.000Z", 500, 100, 30, 0, 530)+"\n"))
	ss := a.Sessions()
	if len(ss) != 1 {
		t.Fatalf("세션 %d개, 1개(같은 meta.id)를 기대했다", len(ss))
	}
	if got := ss[0].Input + ss[0].CacheRead + ss[0].Output; got != 1630 {
		t.Errorf("총 토큰=%d, want 1630", got)
	}
}

// ── 7축 ───────────────────────────────────────────────────────────────────────

func TestAxesLegacyFormat(t *testing.T) {
	s := parse(t, "fallback-id-0001", metaLine, ctxLine,
		// tool + bash — cmd 는 문자열이다(배열이 아니다)
		`{"timestamp":"2026-07-06T00:49:00.000Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"c1","arguments":"{\"cmd\":\"pwd && rg --files | head -80\",\"workdir\":\"/Users/me/secret-dir\",\"yield_time_ms\":1000}"}}`,
		// mcp — namespace 가 mcp__<server>
		`{"timestamp":"2026-07-06T00:49:01.000Z","type":"response_item","payload":{"type":"function_call","name":"develop","namespace":"mcp__claude","call_id":"c2","arguments":"{\"brief\":\"x\"}"}}`,
		// mcp_tool_call_end 는 같은 호출의 반대편이다 — 중복 계상하면 안 된다
		`{"timestamp":"2026-07-06T00:49:02.000Z","type":"event_msg","payload":{"type":"mcp_tool_call_end","call_id":"c2","invocation":{"server":"claude","tool":"develop"}}}`,
		// custom_tool_call
		`{"timestamp":"2026-07-06T00:49:03.000Z","type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","call_id":"c3","input":"*** Begin Patch"}}`,
		// webSearch
		`{"timestamp":"2026-07-06T00:49:04.000Z","type":"event_msg","payload":{"type":"web_search_end","call_id":"ws1","query":"golang json"}}`,
		`{"timestamp":"2026-07-06T00:49:05.000Z","type":"response_item","payload":{"type":"web_search_call","id":"ws1"}}`,
		// keyword
		`{"timestamp":"2026-07-06T00:49:06.000Z","type":"event_msg","payload":{"type":"user_message","message":"collector 파서를 리팩터링 해줘 sk-abcdefghijklmnopqrstuvwx"}}`,
		tokenCount("2026-07-06T00:49:10.000Z", 10, 0, 5, 0, 15),
	)

	if got := s.Counters["tool"]["exec_command"]; got != 1 {
		t.Errorf("tool[exec_command]=%d, want 1", got)
	}
	if got := s.Counters["tool"]["apply_patch"]; got != 1 {
		t.Errorf("tool[apply_patch]=%d, want 1", got)
	}
	// bash 는 선두 실행파일명만 — 인자·경로는 절대 남지 않는다
	if got := s.Counters["bash"]["pwd"]; got != 1 {
		t.Errorf("bash[pwd]=%d, want 1: %+v", got, s.Counters["bash"])
	}
	for k := range s.Counters["bash"] {
		if strings.ContainsAny(k, "/ ") {
			t.Errorf("bash 축에 인자·경로가 샜다: %q", k)
		}
	}
	// mcp 는 딱 한 번(function_call 쪽만) — mcp_tool_call_end 로 두 번 세지 않는다
	if got := s.Counters["mcp"]["mcp__claude__develop"]; got != 1 {
		t.Errorf("mcp[mcp__claude__develop]=%d, want 1 (중복 계상 금지): %+v", got, s.Counters["mcp"])
	}
	var mcpTotal int64
	for _, v := range s.Counters["mcp"] {
		mcpTotal += v
	}
	if mcpTotal != 1 {
		t.Errorf("mcp 축 총합=%d, want 1", mcpTotal)
	}
	// mcp 도구는 tool 축에 겹쳐 세지 않는다(기존 Claude 파서 규율)
	if _, ok := s.Counters["tool"]["develop"]; ok {
		t.Error("mcp 도구가 tool 축에도 셌다")
	}
	// webSearch 는 event_msg 쪽 하나만
	if s.WebSearch != 1 {
		t.Errorf("WebSearch=%d, want 1", s.WebSearch)
	}
	// keyword 는 policy 필터를 통과한다 — 시크릿 모양은 버려진다
	if got := s.Counters["keyword"]["collector"]; got != 1 {
		t.Errorf("keyword[collector]=%d, want 1: %+v", got, s.Counters["keyword"])
	}
	for k := range s.Counters["keyword"] {
		if strings.HasPrefix(k, "sk-") {
			t.Errorf("시크릿 모양 키워드가 살아남았다: %q", k)
		}
	}
	// slash 는 신뢰할 소스가 없다 — 0 이 아니라 **아예 보내지 않는다**
	if _, ok := s.Counters["slash"]; ok {
		t.Errorf("slash 축을 보냈다(미지원이라 키 자체가 없어야 한다): %+v", s.Counters["slash"])
	}
	// 샘플이 없는 축도 보내지 않는다
	if _, ok := s.Counters["agent"]; ok {
		t.Error("샘플 없는 agent 축을 보냈다")
	}
	if _, ok := s.Counters["skill"]; ok {
		t.Error("샘플 없는 skill 축을 보냈다")
	}
}

// ── 현행 exec 도구 모양 (실데이터) ───────────────────────────────────────────
//
// 실측: 이 머신의 Codex 세션 8개(2026-07, gpt-5.6-terra)는 셸을 이렇게 남긴다 —
// `custom_tool_call` + name=`exec` + input 은 **JS 소스**다. 예전 파서는 `function_call` +
// name=`exec_command` + `arguments`(JSON)만 봤으므로 셸 호출 47건에서 bash 축이 0건이었다.
// 거부가 아니라 침묵이라 화면에서는 "Codex 로 셸을 안 썼다"로 읽혔다.

func TestBashAxisFromCurrentExecToolShape(t *testing.T) {
	s := parse(t, "fallback-id-0001", metaLine, ctxLine,
		`{"timestamp":"2026-07-06T00:49:00.000Z","type":"response_item","payload":{"type":"custom_tool_call","name":"exec","call_id":"c1","input":"const r = await tools.exec_command({\"cmd\":\"pwd && rg --files -g '!*node_modules*' | head -n 240\",\"workdir\":\"/Users/me/secret-dir\",\"yield_time_ms\":10000});"}}`,
		tokenCount("2026-07-06T00:49:10.000Z", 10, 0, 5, 0, 15),
	)

	if got := s.Counters["bash"]["pwd"]; got != 1 {
		t.Errorf("bash[pwd]=%d, want 1 (현행 exec 모양에서 명령을 못 뽑았다): %+v", got, s.Counters["bash"])
	}
	// tool 축은 도구가 실제로 쓰는 이름 그대로 둔다 — exec_command 로 바꿔 쓰면 이미 쌓인
	// 데이터의 상위 N 이 두 키로 갈린다.
	if got := s.Counters["tool"]["exec"]; got != 1 {
		t.Errorf("tool[exec]=%d, want 1: %+v", got, s.Counters["tool"])
	}
}

// 한 JS 본문이 exec_command 를 여러 번 부를 수 있다. tool 축은 호출 수(1), bash 축은
// 실행된 명령 수를 센다 — 둘이 어긋나는 것이 정상이다.
func TestBashAxisMultipleExecCommandsInOneCall(t *testing.T) {
	s := parse(t, "fallback-id-0001", metaLine, ctxLine,
		`{"timestamp":"2026-07-06T00:49:00.000Z","type":"response_item","payload":{"type":"custom_tool_call","name":"exec","call_id":"c1","input":"await tools.exec_command({\"cmd\":\"npm run build\"});\nawait tools.exec_command({\"cmd\":\"pytest -q\"});"}}`,
		tokenCount("2026-07-06T00:49:10.000Z", 10, 0, 5, 0, 15),
	)

	if got := s.Counters["bash"]["npm"]; got != 1 {
		t.Errorf("bash[npm]=%d, want 1: %+v", got, s.Counters["bash"])
	}
	if got := s.Counters["bash"]["pytest"]; got != 1 {
		t.Errorf("bash[pytest]=%d, want 1: %+v", got, s.Counters["bash"])
	}
	if got := s.Counters["tool"]["exec"]; got != 1 {
		t.Errorf("tool[exec]=%d, want 1 (도구 호출은 한 번이다): %+v", got, s.Counters["tool"])
	}
}

// 새 경로도 인자 비저장 불변식을 지켜야 한다. 지키는 것은 추출기가 아니라 policy.BashKey 다
// (선두 실행파일명 하나만 남긴다) — 이 테스트가 그 배선을 못박는다.
func TestExecInputNeverLeaksArgs(t *testing.T) {
	s := parse(t, "fallback-id-0001", metaLine, ctxLine,
		`{"timestamp":"2026-07-06T00:49:00.000Z","type":"response_item","payload":{"type":"custom_tool_call","name":"exec","call_id":"c1","input":"await tools.exec_command({\"cmd\":\"curl -H 'Authorization: Bearer TOPSECRETTOKEN' https://x\",\"workdir\":\"/Users/me/secret-dir\"});"}}`,
		tokenCount("2026-07-06T00:49:10.000Z", 10, 0, 5, 0, 15),
	)

	if got := s.Counters["bash"]["curl"]; got != 1 {
		t.Errorf("bash[curl]=%d, want 1: %+v", got, s.Counters["bash"])
	}
	for k := range s.Counters["bash"] {
		if strings.ContainsAny(k, "/ :") || strings.Contains(k, "SECRET") {
			t.Errorf("bash 축에 인자·경로·비밀이 샜다: %q", k)
		}
	}
}

// cmd 를 못 찾으면 축을 만들지 않는다 — 0 을 위조하지 않는다.
func TestExecInputWithoutCmdMakesNoBashAxis(t *testing.T) {
	s := parse(t, "fallback-id-0001", metaLine, ctxLine,
		`{"timestamp":"2026-07-06T00:49:00.000Z","type":"response_item","payload":{"type":"custom_tool_call","name":"exec","call_id":"c1","input":"const x = 1;"}}`,
		tokenCount("2026-07-06T00:49:10.000Z", 10, 0, 5, 0, 15),
	)

	if _, ok := s.Counters["bash"]; ok {
		t.Errorf("cmd 가 없는데 bash 축을 보냈다: %+v", s.Counters["bash"])
	}
	if got := s.Counters["tool"]["exec"]; got != 1 {
		t.Errorf("tool[exec]=%d, want 1", got)
	}
}

// MCP 가 custom_tool_call 로 와도 function_call 과 같은 규율이다 — mcp 축에만 싣는다.
func TestCustomToolCallMcpNamespaceGoesToMcpAxis(t *testing.T) {
	s := parse(t, "fallback-id-0001", metaLine, ctxLine,
		`{"timestamp":"2026-07-06T00:49:00.000Z","type":"response_item","payload":{"type":"custom_tool_call","name":"develop","namespace":"mcp__claude","call_id":"c1","input":"{}"}}`,
		tokenCount("2026-07-06T00:49:10.000Z", 10, 0, 5, 0, 15),
	)

	if got := s.Counters["mcp"]["mcp__claude__develop"]; got != 1 {
		t.Errorf("mcp[mcp__claude__develop]=%d, want 1: %+v", got, s.Counters["mcp"])
	}
	if _, ok := s.Counters["tool"]["develop"]; ok {
		t.Error("mcp 도구가 tool 축에도 셌다")
	}
}

func TestAgentAxisFromSubAgentActivity(t *testing.T) {
	s := parse(t, "fallback-id-0001", metaLine, ctxLine,
		`{"timestamp":"2026-07-06T00:49:00.000Z","type":"event_msg","payload":{"type":"sub_agent_activity","agent_type":"reviewer","turn_id":"t-1"}}`,
		`{"timestamp":"2026-07-06T00:49:01.000Z","type":"inter_agent_communication","payload":{"agent":"planner","kind":"message"}}`,
		tokenCount("2026-07-06T00:49:10.000Z", 10, 0, 5, 0, 15),
	)
	if got := s.Counters["agent"]["reviewer"]; got != 1 {
		t.Errorf("agent[reviewer]=%d, want 1: %+v", got, s.Counters["agent"])
	}
	if got := s.Counters["agent"]["planner"]; got != 1 {
		t.Errorf("agent[planner]=%d, want 1: %+v", got, s.Counters["agent"])
	}
}

// ── LOC / 편집 결정 ───────────────────────────────────────────────────────────

func TestPatchApplyLOCAndVerdict(t *testing.T) {
	s := parse(t, "fallback-id-0001", metaLine, ctxLine,
		`{"timestamp":"2026-07-06T00:49:00.000Z","type":"event_msg","payload":{"type":"patch_apply_end","call_id":"c1","success":true,"status":"completed","changes":{"/Users/me/secret/a.go":{"type":"update","unified_diff":"--- a/a.go\n+++ b/a.go\n@@ -1,3 +1,4 @@\n ctx\n-old one\n-old two\n+new one\n+new two\n+new three\n"}}}}`,
		`{"timestamp":"2026-07-06T00:49:01.000Z","type":"event_msg","payload":{"type":"patch_apply_end","call_id":"c2","success":true,"changes":{"/Users/me/secret/b.txt":{"type":"add","content":"l1\nl2\n"}}}}`,
		`{"timestamp":"2026-07-06T00:49:02.000Z","type":"event_msg","payload":{"type":"patch_apply_end","call_id":"c3","success":false,"changes":{}}}`,
		tokenCount("2026-07-06T00:49:10.000Z", 10, 0, 5, 0, 15),
	)
	// unified_diff: +3 / -2, ---·+++ 헤더는 세지 않는다. add: content 2줄.
	if s.LinesAdded != 5 {
		t.Errorf("LinesAdded=%d, want 5 (diff 3 + add 2)", s.LinesAdded)
	}
	if s.LinesRemoved != 2 {
		t.Errorf("LinesRemoved=%d, want 2", s.LinesRemoved)
	}
	if s.EditsAccepted != 2 || s.EditsRejected != 1 {
		t.Errorf("accepted=%d rejected=%d, want 2/1", s.EditsAccepted, s.EditsRejected)
	}
}

// success 를 판정할 수 없으면 **0 을 위조하지 않는다**(보내지 않는다).
func TestPatchWithoutVerdictNotCounted(t *testing.T) {
	s := parse(t, "fallback-id-0001", metaLine, ctxLine,
		`{"timestamp":"2026-07-06T00:49:00.000Z","type":"event_msg","payload":{"type":"patch_apply_end","call_id":"c1","changes":{"/x/a.go":{"type":"add","content":"l1\n"}}}}`,
		tokenCount("2026-07-06T00:49:10.000Z", 10, 0, 5, 0, 15),
	)
	if s.EditsAccepted != 0 || s.EditsRejected != 0 {
		t.Errorf("판정 불가인데 accepted=%d rejected=%d 를 보냈다", s.EditsAccepted, s.EditsRejected)
	}
	if s.LinesAdded != 1 {
		t.Errorf("LinesAdded=%d, want 1 (LOC 는 판정과 무관하게 센다)", s.LinesAdded)
	}
}

// 파일 내용·경로는 절대 페이로드에 실리지 않는다.
func TestNoContentOrPathLeaks(t *testing.T) {
	s := parse(t, "fallback-id-0001", metaLine, ctxLine,
		`{"timestamp":"2026-07-06T00:49:00.000Z","type":"event_msg","payload":{"type":"patch_apply_end","call_id":"c1","success":true,"changes":{"/Users/me/TOPSECRETPATH/a.go":{"type":"add","content":"SUPERSECRETCONTENT\n"}}}}`,
		`{"timestamp":"2026-07-06T00:49:01.000Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"c9","arguments":"{\"cmd\":\"curl -H 'Authorization: Bearer TOPSECRETTOKEN' https://x\"}"}}`,
		tokenCount("2026-07-06T00:49:10.000Z", 10, 0, 5, 0, 15),
	)
	blob := dump(s)
	for _, bad := range []string{"TOPSECRETPATH", "SUPERSECRETCONTENT", "TOPSECRETTOKEN", "/Users/me"} {
		if strings.Contains(blob, bad) {
			t.Errorf("페이로드에 %q 가 샜다:\n%s", bad, blob)
		}
	}
}

// ── 신버전(paginated) ─────────────────────────────────────────────────────────
//
// ⚠ 이 머신엔 item_completed 샘플이 0건이라 **실증되지 않았다.** 단위 테스트로만 덮는다.

func TestPaginatedItemCompleted(t *testing.T) {
	s := parse(t, "fallback-id-0001", metaLine, ctxLine,
		`{"timestamp":"2026-07-06T00:49:00.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"command_execution","command":"npm run build --silent","exit_code":0}}}`,
		`{"timestamp":"2026-07-06T00:49:01.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"mcp_tool_call","server":"linear","tool":"get_issue","status":"completed"}}}`,
		`{"timestamp":"2026-07-06T00:49:02.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"web_search","query":"golang generics"}}}`,
		`{"timestamp":"2026-07-06T00:49:03.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"sub_agent_activity","agent_type":"reviewer"}}}`,
		`{"timestamp":"2026-07-06T00:49:04.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"user_message","content":[{"type":"text","text":"collector 리팩터링"},{"type":"skill","name":"loop-engineering"}]}}}`,
		`{"timestamp":"2026-07-06T00:49:05.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"file_change","status":"completed","changes":{"/x/a.go":{"type":"update","unified_diff":"--- a\n+++ b\n+one\n-two\n"}}}}}`,
		tokenCount("2026-07-06T00:49:10.000Z", 10, 0, 5, 0, 15),
	)
	if got := s.Counters["bash"]["npm"]; got != 1 {
		t.Errorf("bash[npm]=%d, want 1: %+v", got, s.Counters["bash"])
	}
	if got := s.Counters["mcp"]["mcp__linear__get_issue"]; got != 1 {
		t.Errorf("mcp=%+v", s.Counters["mcp"])
	}
	if s.WebSearch != 1 {
		t.Errorf("WebSearch=%d, want 1", s.WebSearch)
	}
	if got := s.Counters["agent"]["reviewer"]; got != 1 {
		t.Errorf("agent=%+v", s.Counters["agent"])
	}
	if got := s.Counters["skill"]["loop-engineering"]; got != 1 {
		t.Errorf("skill=%+v", s.Counters["skill"])
	}
	if got := s.Counters["keyword"]["collector"]; got != 1 {
		t.Errorf("keyword=%+v", s.Counters["keyword"])
	}
	if s.LinesAdded != 1 || s.LinesRemoved != 1 {
		t.Errorf("LOC=%d/%d, want 1/1", s.LinesAdded, s.LinesRemoved)
	}
}

// Rust enum 변형 이름(PascalCase)으로 실려도 읽는다.
func TestPaginatedPascalCaseVariant(t *testing.T) {
	s := parse(t, "fallback-id-0001", metaLine, ctxLine,
		`{"timestamp":"2026-07-06T00:49:00.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","command":"pytest -q"}}}`,
		tokenCount("2026-07-06T00:49:10.000Z", 10, 0, 5, 0, 15),
	)
	if got := s.Counters["bash"]["pytest"]; got != 1 {
		t.Errorf("bash=%+v", s.Counters["bash"])
	}
}

// 신·구 포맷이 한 세션에 섞이면 신버전만 센다 — 두 번 세면 도구 카운트가 2배가 된다.
func TestPaginatedWinsOverLegacyNoDoubleCount(t *testing.T) {
	s := parse(t, "fallback-id-0001", metaLine, ctxLine,
		`{"timestamp":"2026-07-06T00:49:00.000Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"c1","arguments":"{\"cmd\":\"npm run build\"}"}}`,
		`{"timestamp":"2026-07-06T00:49:01.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"command_execution","command":"npm run build"}}}`,
		tokenCount("2026-07-06T00:49:10.000Z", 10, 0, 5, 0, 15),
	)
	if got := s.Counters["bash"]["npm"]; got != 1 {
		t.Errorf("bash[npm]=%d, want 1 (신버전만 센다): %+v", got, s.Counters["bash"])
	}
}

// ── 견고성 ────────────────────────────────────────────────────────────────────

// 깨진 줄 하나가 세션 전체를 버리게 만들지 않는다.
func TestBrokenLinesSkipped(t *testing.T) {
	s := parse(t, "fallback-id-0001", metaLine, ctxLine,
		`{ this is not json`,
		``,
		`{"timestamp":"2026-07-06T00:49:00.000Z","type":"unknown_future_type","payload":{"x":1}}`,
		tokenCount("2026-07-06T00:49:10.000Z", 100, 40, 5, 0, 105),
	)
	if s.Input != 60 || s.CacheRead != 40 {
		t.Errorf("in=%d cr=%d, want 60/40", s.Input, s.CacheRead)
	}
}

// 신호가 전혀 없는 세션은 내지 않는다.
func TestEmptySessionDropped(t *testing.T) {
	a := New()
	_ = a.AddFile("fallback-id-0001", strings.NewReader(metaLine+"\n"+ctxLine+"\n"))
	if ss := a.Sessions(); len(ss) != 0 {
		t.Errorf("신호 없는 세션을 보냈다: %+v", ss)
	}
}

// 서버가 거부할 모양의 세션 id 는 내보내지 않는다.
func TestBadSessionIDDropped(t *testing.T) {
	a := New()
	_ = a.AddFile("x", strings.NewReader(tokenCount("2026-07-06T00:49:00.000Z", 10, 0, 5, 0, 15)+"\n"))
	if ss := a.Sessions(); len(ss) != 0 {
		t.Errorf("짧은 id 세션을 보냈다: %+v", ss)
	}
}

// 존 없는/깨진 타임스탬프는 넘겨짚지 않고 noTsTurns 로 센다.
func TestNoTimestampTurnsCounted(t *testing.T) {
	s := parse(t, "fallback-id-0001", metaLine, ctxLine,
		`{"timestamp":"","type":"event_msg","payload":{"type":"task_started","turn_id":"t-1"}}`,
		tokenCount("2026-07-06T00:49:10.000Z", 10, 0, 5, 0, 15),
	)
	if s.NoTsTurns == nil || *s.NoTsTurns != 1 {
		t.Errorf("NoTsTurns=%v, want 1", s.NoTsTurns)
	}
	if s.Turns != 1 {
		t.Errorf("Turns=%d, want 1 (합계는 살아야 한다)", s.Turns)
	}
}

func dump(s payload.Session) string {
	var b strings.Builder
	b.WriteString(s.ID + "|" + s.Project + "|" + s.Model + "|")
	for kind, m := range s.Counters {
		for k, v := range m {
			b.WriteString(kind + ":" + k + "=" + itoa(v) + " ")
		}
	}
	return b.String()
}
