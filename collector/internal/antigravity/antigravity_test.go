package antigravity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 이 테스트의 픽스처는 **실측 payload 원문**이다. `agy --print` 를 두 번 돌려(두 번째는
// `--continue`) statusLine 이 뱉은 JSON 을 그대로 받아 적었다. 그래서 여기서 검증하는
// 숫자는 지어낸 것이 아니라 실제로 관측된 값이다.
//
//	turn1  current_usage.input=17283 output=17   total_output_tokens=21
//	turn2  current_usage.input=17506 output=19   total_output_tokens=40
//	print(대화 누적)  input=34886  output=40
const convID = "1c8f484f-af66-4c7f-97c2-22b57c5773eb"

// sl 은 실측 payload 모양 그대로의 statusLine JSON 을 만든다.
// cu 가 nil 이면 `"current_usage": null` — 생성 중 렌더를 뜻한다(실측에서 그랬다).
func sl(t *testing.T, conv string, totalOut int64, cu *Usage) string {
	t.Helper()
	cuJSON := "null"
	if cu != nil {
		cuJSON = `{"input_tokens":` + itoa(cu.Input) +
			`,"output_tokens":` + itoa(cu.Output) +
			`,"cache_creation_input_tokens":` + itoa(cu.CacheCreate) +
			`,"cache_read_input_tokens":` + itoa(cu.CacheRead) + `}`
	}
	return `{
	  "cwd": "/Users/me/orca/user_usage",
	  "session_id": "` + conv + `",
	  "conversation_id": "` + conv + `",
	  "model": {"id":"Gemini 3.6 Flash (Medium)","display_name":"Gemini 3.6 Flash (Medium)","effort":"medium"},
	  "workspace": {"current_dir":"/Users/me/orca/user_usage","project_dir":"/Users/me/orca/user_usage"},
	  "version": "1.1.11",
	  "context_window": {
	    "total_input_tokens": 25286,
	    "total_output_tokens": ` + itoa(totalOut) + `,
	    "context_window_size": 1048576,
	    "current_usage": ` + cuJSON + `
	  },
	  "product": "antigravity",
	  "plan_tier": "Google AI Pro",
	  "email": "someone@example.com",
	  "agent_state": "working"
	}`
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func apply(t *testing.T, s *Spool, raw string, now time.Time) bool {
	t.Helper()
	var d statusLineData
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatalf("픽스처 JSON 파싱 실패: %v", err)
	}
	return s.Apply(d, now)
}

func at(hhmm string) time.Time {
	ts, _ := time.Parse(time.RFC3339, "2026-08-10T"+hhmm+":00Z")
	return ts
}

// ── 함정 1: 렌더는 여러 번 오지만 invocation 은 한 번이다 ──────────────────────
//
// statusLine 은 실측에서 한 번의 실행에 14회 불렸다. 같은 값을 다시 봤다고 다시 세면
// 사용량이 렌더 횟수만큼 부풀어 완전히 못 쓰는 수치가 된다.
func TestRepeatedRendersCountOnce(t *testing.T) {
	var s Spool
	u := &Usage{Input: 17283, Output: 17}

	if !apply(t, &s, sl(t, convID, 21, nil), at("09:34")) {
		t.Fatal("첫 렌더는 모델·프로젝트를 채우므로 변경으로 봐야 한다")
	}
	if s.Invocations != 0 {
		t.Fatalf("current_usage 가 null 인 렌더는 invocation 이 아니다: %d", s.Invocations)
	}

	if !apply(t, &s, sl(t, convID, 21, u), at("09:34")) {
		t.Fatal("usage 가 처음 등장한 렌더는 변경이다")
	}
	// 같은 값으로 다섯 번 더 — 실측에서 실제로 이렇게 반복됐다.
	for i := 0; i < 5; i++ {
		if apply(t, &s, sl(t, convID, 21, u), at("09:34")) {
			t.Fatalf("값이 안 바뀐 %d번째 렌더가 변경으로 잡혔다(스로틀 실패)", i+1)
		}
	}
	if s.Invocations != 1 {
		t.Fatalf("invocation 은 1이어야 한다(렌더 7회): %d", s.Invocations)
	}
	if s.Sum.Input != 17283 {
		t.Fatalf("입력 합산이 부풀었다: %d (기대 17283)", s.Sum.Input)
	}
}

// ── 함정 2: 프로세스가 바뀌어도 직전 invocation 을 다시 세면 안 된다 ──────────
//
// 실측 근거: `--continue` 로 새 프로세스가 붙자 statusLine 이 **직전 턴의 값을 그대로**
// 다시 뱉었다(렌더 8~12). Last 를 디스크에 남기지 않으면 여기서 곧바로 이중계상된다.
func TestResumedProcessDoesNotRecountLastInvocation(t *testing.T) {
	turn1 := &Usage{Input: 17283, Output: 17}
	turn2 := &Usage{Input: 17506, Output: 19}

	var s Spool
	apply(t, &s, sl(t, convID, 21, turn1), at("09:34"))

	// 프로세스 재시작 — 스풀을 JSON 으로 왕복시켜 디스크를 거친 상태를 재현한다.
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var s2 Spool
	if err := json.Unmarshal(b, &s2); err != nil {
		t.Fatal(err)
	}

	// 이어받은 렌더들이 turn1 값을 다시 보여준다.
	for i := 0; i < 3; i++ {
		if apply(t, &s2, sl(t, convID, 21, turn1), at("09:35")) {
			t.Fatal("재개 직후 같은 값을 다시 세었다 — 이중계상")
		}
	}
	if s2.Invocations != 1 {
		t.Fatalf("아직 invocation 1이어야 한다: %d", s2.Invocations)
	}

	// 진짜 두 번째 invocation.
	if !apply(t, &s2, sl(t, convID, 40, turn2), at("09:35")) {
		t.Fatal("새 값은 변경으로 잡혀야 한다")
	}
	if s2.Invocations != 2 {
		t.Fatalf("invocation 2 기대: %d", s2.Invocations)
	}

	in, out, cache := s2.Totals()
	// 실측 대조: print 누적은 input=34886, output=40.
	if want := int64(17283 + 17506); in != want {
		t.Fatalf("입력 합산 %d, 기대 %d", in, want)
	}
	if out != 40 {
		t.Fatalf("출력은 누적값 40 이어야 한다(스냅샷 합 36 보다 크다): %d", out)
	}
	if cache != 0 {
		t.Fatalf("캐시읽기 0 기대: %d", cache)
	}
}

// ── 함정 3: 출력은 누적값이 스냅샷 합보다 정확하다 ────────────────────────────
//
// 실측 2/2 로 total_output_tokens 가 print 누적 output 과 정확히 일치했다(21, 40).
// 스냅샷 합(17+19=36)을 쓰면 보조 호출분이 빠진다.
func TestOutputPrefersCumulativeTotal(t *testing.T) {
	var s Spool
	apply(t, &s, sl(t, convID, 21, &Usage{Input: 17283, Output: 17}), at("09:34"))
	apply(t, &s, sl(t, convID, 40, &Usage{Input: 17506, Output: 19}), at("09:35"))

	if s.Sum.Output != 36 {
		t.Fatalf("스냅샷 합은 36 이어야 한다: %d", s.Sum.Output)
	}
	if _, out, _ := s.Totals(); out != 40 {
		t.Fatalf("출력 40 기대(누적값 채택): %d", out)
	}
}

// 누적값이 압축으로 되돌아가도 절대값은 줄지 않는다 — 수집기는 절대값을 보내므로
// 감소는 곧 "사용량이 줄었다"로 기록된다.
func TestCumulativeOutputNeverGoesBackwards(t *testing.T) {
	var s Spool
	apply(t, &s, sl(t, convID, 40, &Usage{Input: 100, Output: 10}), at("09:34"))
	apply(t, &s, sl(t, convID, 5, &Usage{Input: 200, Output: 12}), at("09:35"))
	if s.TotalOutput != 40 {
		t.Fatalf("누적 출력이 되돌아갔다: %d", s.TotalOutput)
	}
}

// ── 함정 4: cache_read ⊆ input 이므로 빼지 않으면 두 번 실린다 ────────────────
func TestInputIsNetOfCacheRead(t *testing.T) {
	var s Spool
	apply(t, &s, sl(t, convID, 5, &Usage{Input: 1000, Output: 10, CacheRead: 400}), at("09:34"))
	in, _, cache := s.Totals()
	if in != 600 {
		t.Fatalf("순입력 600 기대(1000-400): %d", in)
	}
	if cache != 400 {
		t.Fatalf("캐시읽기 400 기대: %d", cache)
	}
}

// ── 함정 5: cache_creation 은 관측만 하고 과금 축에는 싣지 않는다 ─────────────
//
// Gemini 는 암시적 캐싱이라 캐시 **생성** 과금 개념이 없다. statusLine 이 값을 주더라도
// CacheCreate 로 실으면 없는 비용을 만들어낸다.
func TestCacheCreateIsObservedButNeverBilled(t *testing.T) {
	var s Spool
	apply(t, &s, sl(t, convID, 5, &Usage{Input: 1000, Output: 10, CacheCreate: 777}), at("09:34"))
	if s.ObservedCacheCreate != 777 {
		t.Fatalf("관측값은 남겨야 한다: %d", s.ObservedCacheCreate)
	}
	ss := sessionsFrom(t, s)
	if ss[0].CacheCreate != 0 {
		t.Fatalf("CacheCreate 는 항상 0 이어야 한다: %d", ss[0].CacheCreate)
	}
}

// ── 모델 정규화 ───────────────────────────────────────────────────────────────
func TestModelID(t *testing.T) {
	cases := []struct {
		name             string
		display, id, eff string
		want             string
	}{
		// 접미사를 **자르지 않는다** — 정규화는 단가 계산 한 곳에서만 한다.
		{"실측 표시명 → API id 원문", "Gemini 3.6 Flash (Medium)", "", "medium", "gemini-3.6-flash-medium"},
		{"effort 가 다르면 id 도 다르다", "Gemini 3.6 Flash (High)", "", "high", "gemini-3.6-flash-high"},
		{"medium 이 없는 모델", "Gemini 3.1 Pro (Low)", "", "low", "gemini-3.1-pro-low"},
		// 같은 (Thinking) 인데 Sonnet 은 접미사가 없고 Opus 는 있다 — 규칙으로 못 맞춘다.
		{"Sonnet: 접미사 없음 + 점→하이픈", "Claude Sonnet 4.6 (Thinking)", "", "", "claude-sonnet-4-6"},
		{"Opus: 접미사 있음", "Claude Opus 4.6 (Thinking)", "", "", "claude-opus-4-6-thinking"},
		{"Gemini 전용이 아니다 — GPT-OSS", "GPT-OSS 120B (Medium)", "", "medium", "gpt-oss-120b-medium"},
		{"이미 API id 면 원문 그대로", "", "gemini-3.6-flash-medium", "", "gemini-3.6-flash-medium"},
		{"모르는 표시명은 규칙 + effort", "Gemini 9.9 Ultra (Low)", "", "low", "gemini-9.9-ultra-low"},
		{"빈 값", "", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ModelID(c.display, c.id, c.eff); got != c.want {
				t.Fatalf("ModelID(%q,%q,%q) = %q, 기대 %q", c.display, c.id, c.eff, got, c.want)
			}
		})
	}
}

// ── 프로젝트는 basename 만 (경로 저장 금지) ───────────────────────────────────
func TestProjectIsBasenameOnly(t *testing.T) {
	var s Spool
	apply(t, &s, sl(t, convID, 5, &Usage{Input: 10, Output: 1}), at("09:34"))
	if s.Project != "user_usage" {
		t.Fatalf("프로젝트는 basename 이어야 한다: %q", s.Project)
	}
	b, _ := json.Marshal(s)
	if strings.Contains(string(b), "/Users/me") {
		t.Fatalf("스풀에 절대경로가 새어 들어갔다: %s", b)
	}
}

// 개인정보(email·plan_tier)는 읽지도 저장하지도 않는다.
func TestNoPIIInSpool(t *testing.T) {
	var s Spool
	apply(t, &s, sl(t, convID, 5, &Usage{Input: 10, Output: 1}), at("09:34"))
	b, _ := json.Marshal(s)
	for _, bad := range []string{"someone@example.com", "Google AI Pro"} {
		if strings.Contains(string(b), bad) {
			t.Fatalf("스풀에 개인정보가 실렸다(%q): %s", bad, b)
		}
	}
}

// ── 세션 매핑 ─────────────────────────────────────────────────────────────────

func sessionsFrom(t *testing.T, s Spool) []payloadSession {
	t.Helper()
	if s.ConversationID == "" {
		s.ConversationID = convID
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	a := New()
	if err := a.AddFile(convID, strings.NewReader(string(b))); err != nil {
		t.Fatal(err)
	}
	ss := a.Sessions()
	if len(ss) != 1 {
		t.Fatalf("세션 1개 기대: %d", len(ss))
	}
	return []payloadSession{{
		ID: ss[0].ID, Model: ss[0].Model, Project: ss[0].Project,
		Input: ss[0].Input, Output: ss[0].Output,
		CacheRead: ss[0].CacheRead, CacheCreate: ss[0].CacheCreate,
		Turns: ss[0].Turns, NoTs: ss[0].NoTsTurns, Buckets: len(ss[0].Series),
	}}
}

type payloadSession struct {
	ID, Model, Project                    string
	Input, Output, CacheRead, CacheCreate int64
	Turns                                 int64
	NoTs                                  *int64
	Buckets                               int
}

func TestSessionMapping(t *testing.T) {
	var s Spool
	apply(t, &s, sl(t, convID, 21, &Usage{Input: 17283, Output: 17}), at("09:34"))
	apply(t, &s, sl(t, convID, 40, &Usage{Input: 17506, Output: 19}), at("10:35"))

	got := sessionsFrom(t, s)[0]
	if got.ID != convID {
		t.Fatalf("세션 id: %q", got.ID)
	}
	if got.Model != "gemini-3.6-flash-medium" {
		t.Fatalf("모델: %q", got.Model)
	}
	if got.Project != "user_usage" {
		t.Fatalf("프로젝트: %q", got.Project)
	}
	if got.Input != 34789 || got.Output != 40 {
		t.Fatalf("토큰 in=%d out=%d (기대 34789/40)", got.Input, got.Output)
	}
	if got.Turns != 2 {
		t.Fatalf("턴 2 기대: %d", got.Turns)
	}
	if got.NoTs == nil || *got.NoTs != 0 {
		t.Fatalf("NoTsTurns 는 항상 non-nil 0 이어야 한다: %v", got.NoTs)
	}
	// 시각이 다른 두 invocation → 시간 버킷 두 개.
	if got.Buckets != 2 {
		t.Fatalf("버킷 2 기대: %d", got.Buckets)
	}
}

// ── 함정 6: 시간 버킷의 합은 세션 합계와 같아야 한다 ─────────────────────────
//
// 세션 출력은 누적값을, 버킷은 스냅샷 합을 쓰므로 그냥 두면 어긋난다(실측 56 vs 51).
// 총계만 맞고 시간축 차트가 9% 적게 그려지는 상태를 허용하지 않는다.
func TestSeriesSumMatchesSessionTotals(t *testing.T) {
	var s Spool
	apply(t, &s, sl(t, convID, 21, &Usage{Input: 17283, Output: 17}), at("09:34"))
	apply(t, &s, sl(t, convID, 56, &Usage{Input: 17506, Output: 19}), at("10:35"))

	a := New()
	b, _ := json.Marshal(s)
	if err := a.AddFile(convID, strings.NewReader(string(b))); err != nil {
		t.Fatal(err)
	}
	sess := a.Sessions()[0]

	var bi, bo, bc int64
	for _, x := range sess.Series {
		bi += x.Input
		bo += x.Output
		bc += x.CacheRead
	}
	if bo != sess.Output {
		t.Fatalf("버킷 출력 합 %d != 세션 출력 %d", bo, sess.Output)
	}
	if bi != sess.Input {
		t.Fatalf("버킷 입력 합 %d != 세션 입력 %d", bi, sess.Input)
	}
	if bc != sess.CacheRead {
		t.Fatalf("버킷 캐시 합 %d != 세션 캐시 %d", bc, sess.CacheRead)
	}
	// 메움은 **마지막** 버킷에 들어간다(없는 시각에 사용량을 만들지 않는다).
	if sess.Series[len(sess.Series)-1].Output != 19+(56-36) {
		t.Fatalf("보정이 마지막 버킷에 없다: %+v", sess.Series)
	}
}

// 신호가 없는 스풀은 세션을 만들지 않는다(0 을 위조하지 않는다).
func TestEmptySpoolYieldsNoSession(t *testing.T) {
	a := New()
	if err := a.AddFile(convID, strings.NewReader(`{"version":1,"conversationId":"`+convID+`"}`)); err != nil {
		t.Fatal(err)
	}
	if ss := a.Sessions(); len(ss) != 0 {
		t.Fatalf("세션이 없어야 한다: %d", len(ss))
	}
}

// ── history.jsonl 축 ──────────────────────────────────────────────────────────

func TestHistoryAxes(t *testing.T) {
	hist := strings.Join([]string{
		`{"display":"/model","timestamp":1,"workspace":"/Users/me/orca/user_usage","conversationId":"` + convID + `","type":"slash_command"}`,
		`{"display":"관제센터 알람 자동화 기준을 정리해줘","timestamp":2,"workspace":"/Users/me/orca/user_usage","conversationId":"` + convID + `"}`,
		`{"display":"export AWS_SECRET_ACCESS_KEY=AKIAIOSFODNN7EXAMPLE","timestamp":3,"workspace":"/Users/me/orca/user_usage","conversationId":"` + convID + `"}`,
		`{"display":"다른 대화","timestamp":4,"workspace":"/x/other","conversationId":"99999999-0000-0000-0000-000000000000"}`,
	}, "\n")

	var s Spool
	apply(t, &s, sl(t, convID, 21, &Usage{Input: 100, Output: 10}), at("09:34"))
	b, _ := json.Marshal(s)

	a := New()
	if err := a.AddHistory(strings.NewReader(hist)); err != nil {
		t.Fatal(err)
	}
	if err := a.AddFile(convID, strings.NewReader(string(b))); err != nil {
		t.Fatal(err)
	}
	ss := a.Sessions()
	if len(ss) != 1 {
		t.Fatalf("스풀에 있는 대화만 실려야 한다: %d", len(ss))
	}

	if ss[0].Counters["slash"]["model"] != 1 {
		t.Fatalf("slash 축: %v", ss[0].Counters["slash"])
	}
	if len(ss[0].Counters["keyword"]) == 0 {
		t.Fatalf("keyword 축이 비었다: %v", ss[0].Counters)
	}
	// 시크릿은 policy 필터가 떨궈야 한다.
	for k := range ss[0].Counters["keyword"] {
		if strings.Contains(strings.ToLower(k), "akiaiosfodnn") || strings.Contains(k, "=") {
			t.Fatalf("시크릿이 키워드로 새어 나갔다: %q", k)
		}
	}
	// 얻지 못한 축은 **엔트리 자체가 없어야 한다**(0 을 위조하지 않는다).
	for _, axis := range []string{"tool", "bash", "skill", "agent", "mcp"} {
		if _, ok := ss[0].Counters[axis]; ok {
			t.Fatalf("얻을 수 없는 축 %q 가 실렸다", axis)
		}
	}
}

// ── 파일 규칙 ─────────────────────────────────────────────────────────────────

func TestMatchAndKey(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/s/" + convID + ".json", true},
		{"/s/tmp-123.json", false}, // 원자적 쓰기 중인 임시 파일
		{"/s/.hidden.json", false},
		{"/s/notes.txt", false},
	}
	for _, c := range cases {
		if got := Match(c.path); got != c.want {
			t.Fatalf("Match(%q)=%v, 기대 %v", c.path, got, c.want)
		}
	}
	if got := SessionKeyFromPath("/s/" + convID + ".json"); got != convID {
		t.Fatalf("SessionKeyFromPath: %q", got)
	}
}

// ── RecordStatusLine 왕복 (권한 포함) ─────────────────────────────────────────

func TestRecordStatusLineRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "antigravity")

	_, changed, err := RecordStatusLine(dir, strings.NewReader(sl(t, convID, 21, &Usage{Input: 17283, Output: 17})), at("09:34"))
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("첫 기록은 변경이어야 한다")
	}

	path := filepath.Join(dir, convID+".json")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("스풀 파일이 없다: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("스풀 권한은 600 이어야 한다: %o", perm)
	}

	// 같은 값 재기록 → 디스크를 건드리지 않는다.
	_, changed, err = RecordStatusLine(dir, strings.NewReader(sl(t, convID, 21, &Usage{Input: 17283, Output: 17})), at("09:34"))
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("값이 같으면 다시 쓰지 않아야 한다(스로틀)")
	}

	// 대화 id 가 없는 렌더(인증 중)는 파일을 만들지 않는다.
	empty := filepath.Join(t.TempDir(), "none")
	if _, changed, err = RecordStatusLine(empty, strings.NewReader(sl(t, "", 0, nil)), at("09:34")); err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("대화 id 가 없으면 아무것도 쓰지 않아야 한다")
	}
	if _, err := os.Stat(empty); !os.IsNotExist(err) {
		t.Fatal("대화 id 가 없는데 디렉터리를 만들었다")
	}
}

// 경로 문자가 섞인 대화 id 는 파일명이 되지 못하게 막는다.
func TestRecordStatusLineRejectsUnsafeID(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "antigravity")
	_, changed, err := RecordStatusLine(dir, strings.NewReader(sl(t, "../../etc/passwd", 5, &Usage{Input: 1, Output: 1})), at("09:34"))
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("안전하지 않은 대화 id 는 거부해야 한다")
	}
}

// 깨진 스풀을 만나도 그 대화를 영영 잃지 않는다(새로 시작한다).
func TestCorruptSpoolRestarts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, convID+".json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, changed, err := RecordStatusLine(dir, strings.NewReader(sl(t, convID, 21, &Usage{Input: 5, Output: 1})), at("09:34"))
	if err != nil {
		t.Fatalf("깨진 스풀에서 멈추면 안 된다: %v", err)
	}
	if !changed {
		t.Fatal("새로 시작해 기록해야 한다")
	}
}
