package transcript

import (
	"strings"
	"testing"

	"github.com/tscorp/user-usage/collector/internal/payload"
)

// aggregate 는 여러 jsonl 줄을 누적기에 흘려 세션 하나를 돌려준다(테스트 편의).
func aggregate(t *testing.T, stem string, lines ...string) []payload.Session {
	t.Helper()
	a := New()
	if err := a.AddFile(stem, strings.NewReader(strings.Join(lines, "\n")+"\n")); err != nil {
		t.Fatalf("AddFile: %v", err)
	}
	return a.Sessions()
}

func one(t *testing.T, sessions []payload.Session) payload.Session {
	t.Helper()
	if len(sessions) != 1 {
		t.Fatalf("세션 수 %d, want 1", len(sessions))
	}
	return sessions[0]
}

func findBucket(s payload.Session, hour, model string) (payload.Bucket, bool) {
	for _, b := range s.Series {
		if b.Hour == hour && b.Model == model {
			return b, true
		}
	}
	return payload.Bucket{}, false
}

const sid = "aaaaaaaa-bbbb-cccc-dddd"

// 기본 매핑: message.usage 의 네 토큰 필드 → 세션 합계 + 시간×모델 버킷, cc1h 포함.
func TestBasicMapping(t *testing.T) {
	line := `{"type":"assistant","timestamp":"2026-08-05T14:21:49.271Z","sessionId":"` + sid + `","cwd":"/Users/me/proj-x",` +
		`"message":{"role":"assistant","model":"claude-opus-5","stop_reason":"end_turn","usage":{` +
		`"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":100,"cache_creation_input_tokens":50,` +
		`"cache_creation":{"ephemeral_1h_input_tokens":50,"ephemeral_5m_input_tokens":0},` +
		`"server_tool_use":{"web_search_requests":2,"web_fetch_requests":1}}}}`

	s := one(t, aggregate(t, sid, line))
	if s.Input != 10 || s.Output != 20 || s.CacheRead != 100 || s.CacheCreate != 50 {
		t.Fatalf("세션 합계가 틀렸다: %+v", s)
	}
	if s.WebSearch != 2 || s.WebFetch != 1 {
		t.Fatalf("웹 도구 카운트가 틀렸다: search=%d fetch=%d", s.WebSearch, s.WebFetch)
	}
	if s.Turns != 1 {
		t.Fatalf("턴 수 %d, want 1", s.Turns)
	}
	if s.Project != "proj-x" {
		t.Fatalf("project=%q, want proj-x", s.Project)
	}
	if s.Model != "claude-opus-5" {
		t.Fatalf("model=%q", s.Model)
	}
	if s.NoTsTurns == nil || *s.NoTsTurns != 0 {
		t.Fatalf("noTsTurns 는 0 이어야 한다(nil 아님): %v", s.NoTsTurns)
	}
	b, ok := findBucket(s, "2026-08-05T14", "claude-opus-5")
	if !ok {
		t.Fatalf("버킷이 없다: %+v", s.Series)
	}
	if b.Input != 10 || b.Output != 20 || b.CacheRead != 100 || b.CacheCreate != 50 || b.CC1h != 50 {
		t.Fatalf("버킷 값이 틀렸다: %+v", b)
	}
}

// 존이 없는 타임스탬프: 버킷에 못 올린다 → noTsTurns 로 세고, **세션 합계는 그대로** 살린다.
func TestZonelessTimestampCountsAsNoTs(t *testing.T) {
	line := `{"type":"assistant","timestamp":"2026-08-05T14:21:49","sessionId":"` + sid + `",` +
		`"message":{"role":"assistant","model":"m1","usage":{"input_tokens":7,"output_tokens":3}}}`

	s := one(t, aggregate(t, sid, line))
	if s.Input != 7 || s.Output != 3 {
		t.Fatalf("합계가 사라졌다: %+v", s)
	}
	if len(s.Series) != 0 {
		t.Fatalf("존 없는 시각인데 버킷이 생겼다: %+v", s.Series)
	}
	if s.NoTsTurns == nil || *s.NoTsTurns != 1 {
		t.Fatalf("noTsTurns=%v, want 1", s.NoTsTurns)
	}
}

// cacheRead·cacheCreate·cache_creation 이 통째로 없는 구형 usage 도 0 으로 안전하게 접힌다.
func TestMissingCacheFields(t *testing.T) {
	line := `{"type":"assistant","timestamp":"2026-08-05T09:00:00Z","sessionId":"` + sid + `",` +
		`"message":{"role":"assistant","model":"m1","usage":{"input_tokens":5,"output_tokens":8}}}`

	s := one(t, aggregate(t, sid, line))
	if s.CacheRead != 0 || s.CacheCreate != 0 {
		t.Fatalf("없던 캐시가 0 이 아니다: %+v", s)
	}
	b, ok := findBucket(s, "2026-08-05T09", "m1")
	if !ok || b.CC1h != 0 || b.CC5m != 0 {
		t.Fatalf("cc1h/cc5m 는 0 이어야 한다: %+v ok=%v", b, ok)
	}
}

// 멀티모델 세션: 모델별 버킷이 갈리고, 세션 대표 모델은 토큰 합이 큰 쪽이다.
func TestMultiModelSession(t *testing.T) {
	opus := `{"type":"assistant","timestamp":"2026-08-05T10:00:00Z","sessionId":"` + sid + `",` +
		`"message":{"role":"assistant","model":"opus","usage":{"input_tokens":1000,"output_tokens":2000}}}`
	haiku := `{"type":"assistant","timestamp":"2026-08-05T10:30:00Z","sessionId":"` + sid + `",` +
		`"message":{"role":"assistant","model":"haiku","usage":{"input_tokens":10,"output_tokens":20}}}`

	s := one(t, aggregate(t, sid, opus, haiku))
	if len(s.Series) != 2 {
		t.Fatalf("버킷 수 %d, want 2: %+v", len(s.Series), s.Series)
	}
	if s.Model != "opus" {
		t.Fatalf("대표 모델은 토큰이 큰 opus 여야 한다: %q", s.Model)
	}
	if s.Input != 1010 || s.Output != 2020 {
		t.Fatalf("세션 합계가 두 모델을 합치지 않았다: %+v", s)
	}
	// 같은 시각·다른 모델이라 두 버킷은 같은 hour 아래 나뉜다.
	if _, ok := findBucket(s, "2026-08-05T10", "opus"); !ok {
		t.Fatal("opus 버킷 없음")
	}
	if _, ok := findBucket(s, "2026-08-05T10", "haiku"); !ok {
		t.Fatal("haiku 버킷 없음")
	}
}

// 도구 카운터: mcp·bash·agent·skill·tool 축이 규칙대로 갈린다. bash 는 선두 실행파일명만.
func TestToolCounters(t *testing.T) {
	content := `[` +
		`{"type":"tool_use","name":"Bash","input":{"command":"git commit -m \"x token=abc\"","description":"d"}},` +
		`{"type":"tool_use","name":"Read","input":{"file_path":"/etc/passwd"}},` +
		`{"type":"tool_use","name":"mcp__srv__do_thing","input":{}},` +
		`{"type":"tool_use","name":"Agent","input":{"subagent_type":"backend-engineer"}},` +
		`{"type":"tool_use","name":"Skill","input":{"skill":"loop-engineering"}}` +
		`]`
	line := `{"type":"assistant","timestamp":"2026-08-05T11:00:00Z","sessionId":"` + sid + `",` +
		`"message":{"role":"assistant","model":"m1","content":` + content + `,"usage":{"input_tokens":1,"output_tokens":1}}}`

	s := one(t, aggregate(t, sid, line))
	c := s.Counters

	if got := c["bash"]["git"]; got != 1 {
		t.Fatalf("bash git=%d, want 1: %+v", got, c["bash"])
	}
	if len(c["bash"]) != 1 {
		t.Fatalf("bash 축에 선두 실행파일명만 남아야 한다(인자·secret 금지): %+v", c["bash"])
	}
	if c["tool"]["Bash"] != 1 || c["tool"]["Read"] != 1 {
		t.Fatalf("tool 축: %+v", c["tool"])
	}
	if c["tool"]["Agent"] != 1 || c["tool"]["Skill"] != 1 {
		t.Fatalf("Agent/Skill 도 tool 축에 있어야 한다: %+v", c["tool"])
	}
	if c["mcp"]["mcp__srv__do_thing"] != 1 {
		t.Fatalf("mcp 축: %+v", c["mcp"])
	}
	if c["agent"]["backend-engineer"] != 1 {
		t.Fatalf("agent 축: %+v", c["agent"])
	}
	if c["skill"]["loop-engineering"] != 1 {
		t.Fatalf("skill 축: %+v", c["skill"])
	}
	// 파일 경로·명령 인자는 어느 축에도 남지 않는다.
	for kind, m := range c {
		for k := range m {
			if strings.Contains(k, "passwd") || strings.Contains(k, "token=") || strings.Contains(k, "/etc") {
				t.Fatalf("정책 위반: %s 축에 원문이 샜다: %q", kind, k)
			}
		}
	}
}

// 슬래시 명령: 사용자 메시지의 <command-name> 에서 **이름만** 뽑는다(인자 금지). 그 줄은
// `<` 로 시작하므로 keyword 로는 새지 않는다.
func TestSlashCommand(t *testing.T) {
	line := `{"type":"user","timestamp":"2026-08-05T12:00:00Z","sessionId":"` + sid + `",` +
		`"message":{"role":"user","content":"<command-name>/deploy</command-name>\n<command-args>--prod secret</command-args>"}}`
	// 세션이 신호를 가지려면 과금 턴이 하나는 있어야 한다.
	usage := `{"type":"assistant","timestamp":"2026-08-05T12:00:01Z","sessionId":"` + sid + `",` +
		`"message":{"role":"assistant","model":"m1","usage":{"input_tokens":1,"output_tokens":1}}}`

	s := one(t, aggregate(t, sid, line, usage))
	if s.Counters["slash"]["/deploy"] != 1 {
		t.Fatalf("slash 축: %+v", s.Counters["slash"])
	}
	if len(s.Counters["keyword"]) != 0 {
		t.Fatalf("command 블록에서 keyword 가 새면 안 된다: %+v", s.Counters["keyword"])
	}
	for k := range s.Counters["slash"] {
		if strings.Contains(k, "prod") || strings.Contains(k, "secret") {
			t.Fatalf("slash 인자가 샜다: %q", k)
		}
	}
}

// keyword 축: 사람이 친 낱말만 남고, 시크릿·PII 모양은 policy.SafeKeyword 가 버린다.
func TestKeywordPolicyFilter(t *testing.T) {
	text := "refactor database schema AKIAIOSFODNN7EXAMPLE and sk-live1234567890abcdefghij please refactor"
	line := `{"type":"user","timestamp":"2026-08-05T13:00:00Z","sessionId":"` + sid + `",` +
		`"message":{"role":"user","content":"` + text + `"}}`
	usage := `{"type":"assistant","timestamp":"2026-08-05T13:00:01Z","sessionId":"` + sid + `",` +
		`"message":{"role":"assistant","model":"m1","usage":{"input_tokens":1,"output_tokens":1}}}`

	s := one(t, aggregate(t, sid, line, usage))
	kw := s.Counters["keyword"]

	if kw["refactor"] != 2 {
		t.Fatalf("refactor 는 2회여야 한다: %+v", kw)
	}
	if kw["database"] != 1 || kw["schema"] != 1 || kw["please"] != 1 {
		t.Fatalf("정상 낱말이 빠졌다: %+v", kw)
	}
	if _, ok := kw["and"]; ok {
		t.Fatalf("스톱워드 'and' 가 남았다: %+v", kw)
	}
	for k := range kw {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "akia") || strings.HasPrefix(lk, "sk-") {
			t.Fatalf("시크릿류가 keyword 로 저장됐다: %q", k)
		}
	}
}

// tool_result 의 is_error 는 그 시각·최근 모델 버킷의 toolErrors 로 센다. stop_reason 도.
func TestToolErrorsAndStopReasons(t *testing.T) {
	a1 := `{"type":"assistant","timestamp":"2026-08-05T15:00:00Z","sessionId":"` + sid + `",` +
		`"message":{"role":"assistant","model":"m1","stop_reason":"max_tokens","usage":{"input_tokens":1,"output_tokens":1}}}`
	uerr := `{"type":"user","timestamp":"2026-08-05T15:10:00Z","sessionId":"` + sid + `",` +
		`"message":{"role":"user","content":[{"type":"tool_result","is_error":true,"content":"boom"}]}}`

	s := one(t, aggregate(t, sid, a1, uerr))
	b, ok := findBucket(s, "2026-08-05T15", "m1")
	if !ok {
		t.Fatalf("버킷 없음: %+v", s.Series)
	}
	if b.StopMaxTokens != 1 {
		t.Fatalf("stopMaxTokens=%d, want 1", b.StopMaxTokens)
	}
	if b.ToolErrors != 1 {
		t.Fatalf("toolErrors=%d, want 1", b.ToolErrors)
	}
}

// 세션 id 가 서버 정규식에 안 맞으면(너무 짧음) 통째로 버린다 — 서버가 어차피 거부한다.
func TestInvalidSessionIDDropped(t *testing.T) {
	line := `{"type":"assistant","timestamp":"2026-08-05T16:00:00Z","sessionId":"bad",` +
		`"message":{"role":"assistant","model":"m1","usage":{"input_tokens":1,"output_tokens":1}}}`
	if got := aggregate(t, "bad", line); len(got) != 0 {
		t.Fatalf("짧은 세션 id 가 통과했다: %+v", got)
	}
}

// 깨진 줄은 건너뛰고 나머지는 살린다.
func TestBrokenLineSkipped(t *testing.T) {
	good := `{"type":"assistant","timestamp":"2026-08-05T17:00:00Z","sessionId":"` + sid + `",` +
		`"message":{"role":"assistant","model":"m1","usage":{"input_tokens":9,"output_tokens":0}}}`
	s := one(t, aggregate(t, sid, "{not json", good, ""))
	if s.Input != 9 {
		t.Fatalf("깨진 줄 뒤 정상 줄이 사라졌다: %+v", s)
	}
}

// 재개된 세션: 같은 sessionId 가 두 파일에 흩어져도 하나로 합쳐 절대값을 낸다.
func TestSameSessionAcrossFiles(t *testing.T) {
	a := New()
	l1 := `{"type":"assistant","timestamp":"2026-08-05T18:00:00Z","sessionId":"` + sid + `",` +
		`"message":{"role":"assistant","model":"m1","usage":{"input_tokens":100,"output_tokens":0}}}`
	l2 := `{"type":"assistant","timestamp":"2026-08-05T19:00:00Z","sessionId":"` + sid + `",` +
		`"message":{"role":"assistant","model":"m1","usage":{"input_tokens":50,"output_tokens":0}}}`
	if err := a.AddFile(sid, strings.NewReader(l1+"\n")); err != nil {
		t.Fatal(err)
	}
	if err := a.AddFile(sid, strings.NewReader(l2+"\n")); err != nil {
		t.Fatal(err)
	}
	s := one(t, a.Sessions())
	if s.Input != 150 {
		t.Fatalf("두 파일이 합쳐지지 않았다: input=%d, want 150", s.Input)
	}
	if len(s.Series) != 2 {
		t.Fatalf("버킷 수 %d, want 2", len(s.Series))
	}
}

/*
 * ── `<synthetic>` — 모델 이름 자리에 온 자리표시자 ────────────────────────────
 *
 * Claude Code 는 중단·오류 턴의 model 자리에 `<synthetic>` 을 직접 쓴다(usage 는 전부 0).
 * 이름이 아니라 "이름이 없다"는 표시이므로 모델 축에 올리면 안 된다.
 *
 * 서버(intake)가 최종 경계이고 거기서도 접지만, 여기서 접어야만 고쳐지는 것이 하나 있다:
 * **도구 오류의 귀속**이다. sa.model 은 "마지막으로 본 모델"이고 tool_result 오류가 그 버킷에
 * 달리는데, 중단 턴이 그 자리를 차지하면 그 뒤의 도구 오류가 실재하는 모델에서 떨어져 나간다.
 * 서버는 집계만 보므로 그 귀속을 되돌릴 수 없다.
 */

const synthetic = "<synthetic>"

func TestSyntheticModelIsNotAModelName(t *testing.T) {
	// 중단 턴 하나뿐인 세션 — 실측상 이 모양이 56개 중 22개였다.
	line := `{"type":"assistant","timestamp":"2026-08-05T14:00:00.000Z","sessionId":"` + sid + `",` +
		`"message":{"role":"assistant","model":"` + synthetic + `","usage":{` +
		`"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`

	s := one(t, aggregate(t, sid, line))

	// ① 모델 축에서 사라진다.
	if s.Model == synthetic {
		t.Fatalf("세션 대표 모델 = %q — 자리표시자가 모델 이름이 됐다", s.Model)
	}
	for _, b := range s.Series {
		if b.Model == synthetic {
			t.Fatalf("버킷 모델 = %q: %+v", b.Model, s.Series)
		}
	}
	// ② 그 세션의 사용은 살아 있다 — 버킷은 '(미상)' 으로 남고 턴이 세어진다.
	if s.Turns != 1 {
		t.Fatalf("턴 수 %d, want 1 — 실재하는 턴을 버렸다", s.Turns)
	}
	if _, ok := findBucket(s, "2026-08-05T14", "(미상)"); !ok {
		t.Fatalf("버킷이 '(미상)' 으로 남지 않았다: %+v", s.Series)
	}
}

// 중단 턴이 실재하는 모델의 자리를 빼앗지 않는다 — 대표 모델도, 도구 오류 귀속도.
func TestSyntheticDoesNotStealAttribution(t *testing.T) {
	real := `{"type":"assistant","timestamp":"2026-08-05T14:00:00.000Z","sessionId":"` + sid + `",` +
		`"message":{"role":"assistant","model":"claude-opus-5","usage":{"input_tokens":10,"output_tokens":20}}}`
	synth := `{"type":"assistant","timestamp":"2026-08-05T14:10:00.000Z","sessionId":"` + sid + `",` +
		`"message":{"role":"assistant","model":"` + synthetic + `","usage":{"input_tokens":0,"output_tokens":0}}}`
	toolErr := `{"type":"user","timestamp":"2026-08-05T14:20:00.000Z","sessionId":"` + sid + `",` +
		`"message":{"role":"user","content":[{"type":"tool_result","is_error":true}]}}`

	s := one(t, aggregate(t, sid, real, synth, toolErr))

	if s.Model != "claude-opus-5" {
		t.Fatalf("대표 모델 = %q, want claude-opus-5 — 토큰 0 인 중단 턴이 대표를 빼앗았다", s.Model)
	}
	b, ok := findBucket(s, "2026-08-05T14", "claude-opus-5")
	if !ok {
		t.Fatalf("opus 버킷이 없다: %+v", s.Series)
	}
	if b.ToolErrors != 1 {
		t.Fatalf("도구 오류가 실재하는 모델에 안 붙었다: %+v", s.Series)
	}
}

// 판정은 **꺾쇠로 감싼 값 전체** 다 — 실제 과금 ID 에는 꺾쇠가 없다.
func TestModelPlaceholderShape(t *testing.T) {
	line := func(model string) string {
		return `{"type":"assistant","timestamp":"2026-08-05T14:00:00.000Z","sessionId":"` + sid + `",` +
			`"message":{"role":"assistant","model":"` + model + `","usage":{"input_tokens":1,"output_tokens":1}}}`
	}
	for _, m := range []string{"<synthetic>", "<none>", "<unknown>", "<>"} {
		s := one(t, aggregate(t, sid, line(m)))
		if s.Model != "" {
			t.Fatalf("model=%q 가 접히지 않았다: %q", m, s.Model)
		}
	}
	// 음성 대조 — 실재하는 모델 ID 와 꺾쇠가 일부만 있는 값은 그대로 둔다.
	for _, m := range []string{"claude-opus-5", "nemotron", "a<b>c", "<partial", "partial>"} {
		s := one(t, aggregate(t, sid, line(m)))
		if s.Model != m {
			t.Fatalf("정상 모델 %q 가 %q 로 바뀌었다", m, s.Model)
		}
	}
}
