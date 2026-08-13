// Package codex 는 Codex CLI 롤아웃(jsonl)을 세션 합계로 좁히는 **순수** 계층이다.
//
// 원천은 `~/.codex/sessions/YYYY/MM/DD/rollout-<ISO ts>-<uuidv7>.jsonl` 이고, 파일 하나가
// 세션 하나다(재개하면 **같은 파일에 append** 되므로 세션이 수일에 걸칠 수 있다).
// 한 줄은 `{"timestamp","type","payload"}` 이고 `type` 은 session_meta · response_item ·
// event_msg · turn_context · compacted · inter_agent_communication 중 하나다.
//
// 이 패키지는 `internal/transcript`(Claude)와 **같은 payload.Session 을 낸다.** 서버가 두
// 플랫폼을 한 스키마로 저장하기 때문이다. 그래서 축 이름·키 정규화·상한은 전부
// `internal/policy` 를 공유한다 — 축은 같은데 키 규칙만 갈라지면 대시보드의 상위 N 이
// 플랫폼마다 다른 뜻을 갖게 된다.
//
// ── 토큰이 이 파일에서 가장 위험한 부분이다 ────────────────────────────────
// `event_msg/token_count` 의 `info.total_token_usage` 는 **세션 누적(단조증가)** 이다.
// 이걸 이벤트마다 더하면 총량이 배로 부풀고, 그대로 비용이 된다. 그래서 여기서는
// "마지막 누적값"만 세션 합계로 접고, 시계열은 **연속 누적값의 차분**으로 만든다.
// (`last_token_usage` 는 쓰지 않는다 — 합산하면 실측 2.9% 과대계상이 난다.)
//
// 정규화 규칙(OpenAI 회계):
//
//	Input       = max(0, input_tokens - cached_input_tokens)  ← cached 는 input 의 부분집합이다
//	CacheRead   = cached_input_tokens
//	Output      = output_tokens                               ← reasoning 이 **이미 포함**돼 있다
//	CacheCreate = 0                                           ← 관측 불가(아래 주석 참고)
//
// 표준 라이브러리와 collector/internal/{payload,policy} 말고 아무것도 import 하지 않는다.
package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tscorp/user-usage/collector/internal/payload"
	"github.com/tscorp/user-usage/collector/internal/policy"
)

// Platform 은 이 원천이 서버에 자기를 밝히는 이름이다(payload.Session.Platform).
const Platform = "codex"

// maxLineBytes — 한 줄의 상한. 롤아웃 한 줄에 큰 tool 출력·unified_diff 가 통째로 실려
// bufio 기본 버퍼(64KB)로는 잘린다. 잘린 줄은 JSON 파싱에 실패해 조용히 버려지는데,
// 그러면 그 턴의 토큰이 통째로 사라진다. 넉넉히 잡아 그 침묵을 없앤다.
const maxLineBytes = 16 * 1024 * 1024

// uuidRe 는 롤아웃 파일명 꼬리의 uuidv7 을 뽑는다. 이 값이 `session_meta.id` 이자
// `~/.codex/state_5.sqlite` 의 `threads.id` 다.
var uuidRe = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// SessionIDFromPath 는 롤아웃 경로에서 세션 id 를 뽑는다. 모양이 다르면 파일명 stem 으로
// 떨어진다(그 값은 Sessions() 에서 policy.SessionIDRe 로 다시 걸러진다).
func SessionIDFromPath(path string) string {
	stem := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if m := uuidRe.FindString(stem); m != "" {
		return m
	}
	return stem
}

// ── 파싱 대상 모양 ────────────────────────────────────────────────────────────
//
// 스키마 전체를 모델링하지 않는다 — 매핑에 쓰는 필드만 좁게 받는다. 모르는 필드는
// encoding/json 이 조용히 버린다(그게 맞다: 스키마가 버전마다 흔들려도 여기가 안 깨진다).

type rawLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type payloadHead struct {
	Type string `json:"type"`
}

type sessionMeta struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"` // 일부 버전은 이쪽에 싣는다
	Cwd       string `json:"cwd"`
}

type turnContext struct {
	TurnID string `json:"turn_id"`
	Cwd    string `json:"cwd"`
	Model  string `json:"model"`
}

type tokenUsage struct {
	Input  int64 `json:"input_tokens"`
	Cached int64 `json:"cached_input_tokens"`
	Output int64 `json:"output_tokens"`
	// ReasoningOutput 은 **읽기만 하고 더하지 않는다.** output_tokens 에 이미 포함돼 있어
	// 더하면 그 세션의 출력 토큰이 이중으로 잡힌다. 필드를 남겨 두는 이유는, 이 사실을
	// 모르는 다음 사람이 "왜 안 쓰지?"를 코드에서 바로 보게 하기 위해서다.
	ReasoningOutput int64 `json:"reasoning_output_tokens"`
	Total           int64 `json:"total_tokens"`
}

type tokenCountPayload struct {
	Info *struct {
		TotalTokenUsage *tokenUsage `json:"total_token_usage"`
	} `json:"info"`
}

type functionCall struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Arguments string `json:"arguments"` // JSON 이 **문자열로** 한 번 더 감싸여 있다
}

type execArgs struct {
	// ⚠ cmd 는 **문자열**이다(배열이 아니다). 배열로 읽으면 통째로 파싱 실패해 bash 축이 빈다.
	Cmd string `json:"cmd"`
}

type patchApplyEnd struct {
	Success *bool                  `json:"success"`
	Status  string                 `json:"status"`
	Changes map[string]patchChange `json:"changes"`
}

// patchChange 는 파일 하나의 변경이다. **내용은 절대 저장하지 않는다** — 줄 수만 센다.
type patchChange struct {
	Type        string `json:"type"` // add | update | delete
	UnifiedDiff string `json:"unified_diff"`
	Content     string `json:"content"`
}

type subAgentActivity struct {
	AgentType string `json:"agent_type"`
	Agent     string `json:"agent"`
	Name      string `json:"name"`
	Nickname  string `json:"agent_nickname"`
}

type userMessage struct {
	Message string `json:"message"`
}

// ── 신버전(paginated, 0.147+) ────────────────────────────────────────────────
//
// ⚠ **이 머신엔 item_completed 샘플이 0건이라 실증되지 않았다.** 아래 모양은 계약서에
// 적힌 TurnItem 변형 이름에 맞춘 것이고, 단위 테스트로만 덮여 있다. serde 가 어느 표기로
// 직렬화하든 읽히도록 변형 이름은 소문자·언더스코어 제거 후 비교한다
// (`CommandExecution` == `command_execution`).

type itemCompleted struct {
	Item     json.RawMessage `json:"item"`
	TurnItem json.RawMessage `json:"turn_item"`
}

type turnItem struct {
	Type     string `json:"type"`
	ItemType string `json:"item_type"`

	Command string `json:"command"` // CommandExecution
	Cmd     string `json:"cmd"`

	Server string `json:"server"` // McpToolCall
	Tool   string `json:"tool"`

	AgentType string `json:"agent_type"` // SubAgentActivity
	Agent     string `json:"agent"`
	Name      string `json:"name"`

	Status  string                 `json:"status"` // FileChange
	Success *bool                  `json:"success"`
	Changes map[string]patchChange `json:"changes"`

	Message string `json:"message"` // UserMessage
	Content []struct {
		Type string `json:"type"`
		Name string `json:"name"`
		Text string `json:"text"`
	} `json:"content"`
}

// ── 축 누적 ───────────────────────────────────────────────────────────────────

// axes 는 "한 포맷이 만들어 낸 축 값"이다. 구·신 포맷을 **따로** 담는 이유는 하나다:
// 한 세션에 둘이 섞이면 같은 호출을 두 번 세게 되고, 그러면 도구 카운트가 그대로 2배가
// 된다. Sessions() 에서 item_completed 를 본 세션은 신버전 쪽만 채택한다.
type axes struct {
	counters                     map[string]map[string]int64
	webSearch                    int64
	linesAdded, linesRemoved     int64
	editsAccepted, editsRejected int64
}

func newAxes() axes { return axes{counters: map[string]map[string]int64{}} }

func (ax *axes) count(kind, key string) {
	if key == "" {
		return
	}
	m := ax.counters[kind]
	if m == nil {
		m = map[string]int64{}
		ax.counters[kind] = m
	}
	m[key]++
}

func (ax *axes) merge(src *axes) {
	for kind, m := range src.counters {
		for k, v := range m {
			d := ax.counters[kind]
			if d == nil {
				d = map[string]int64{}
				ax.counters[kind] = d
			}
			d[k] += v
		}
	}
	ax.webSearch += src.webSearch
	ax.linesAdded += src.linesAdded
	ax.linesRemoved += src.linesRemoved
	ax.editsAccepted += src.editsAccepted
	ax.editsRejected += src.editsRejected
}

// ── 세션 누적 ─────────────────────────────────────────────────────────────────

type sessionAgg struct {
	id      string
	metaID  string
	project string
	minTs   string
	maxTs   string
	model   string // 가장 최근 turn_context 의 모델 — 턴마다 바뀔 수 있다

	input, cacheRead, output int64
	// 계단 몫(위 총량의 부분집합). 세션 총량은 버킷에서 파생되지 않고 따로 누적되므로
	// 버킷과 같은 자리에서 같이 더한다.
	inputLong, cacheReadLong, outputLong int64
	taskStarted                          int64
	noTsTurn                             int64
	turnIDs                              map[string]bool

	modelTokens map[string]int64
	buckets     map[string]*payload.Bucket

	legacy           axes
	paged            axes
	sawItemCompleted bool

	// streamCur 는 **파일 하나 안에서** 마지막으로 본 누적 토큰값이다. 파일이 끝나거나
	// 카운터가 되감기면 endStream 이 이 값을 세션 합계로 확정한다.
	streamCur *tokenUsage

	// 턴의 모델은 턴이 시작된 **뒤에** 온다. 롤아웃 순서가 task_started → turn_context 라
	// (실데이터 306턴 전부 이 순서) task_started 시점엔 그 턴이 어느 모델인지 아직 모른다.
	// 그 자리에서 바로 버킷에 꽂으면 턴이 **직전 턴의 모델**로 귀속되고, 세션 첫 턴은
	// 갈 곳이 없어 `(미상)` 으로 샌다. 그래서 턴을 붙들고 있다가 모델을 보면 그때 꽂는다.
	pendingTurnHour string
	pendingTurnOpen bool
}

// flushPendingTurn 은 붙들고 있던 턴을 지금 아는 모델로 버킷에 꽂는다.
// (파일 끝까지 turn_context 가 안 오면 마지막으로 알던 모델로 떨어진다.)
func (sa *sessionAgg) flushPendingTurn() {
	if !sa.pendingTurnOpen {
		return
	}
	sa.bucket(sa.pendingTurnHour, sa.model).Turns++
	sa.pendingTurnOpen = false
}

func newSessionAgg() *sessionAgg {
	return &sessionAgg{
		turnIDs:     map[string]bool{},
		modelTokens: map[string]int64{},
		buckets:     map[string]*payload.Bucket{},
		legacy:      newAxes(),
		paged:       newAxes(),
	}
}

// Aggregator 는 여러 파일을 세션 id 로 묶어 누적한다.
//
// 왜 파일 단위가 아니라 세션 단위인가: 서버는 session_id 절대값으로 덮어쓴다. 한 세션이
// 두 파일에 흩어졌는데 파일마다 따로 보내면 뒤 파일이 앞 파일을 덮어 **과소집계**가 된다.
type Aggregator struct {
	sessions map[string]*sessionAgg
	order    []string // 결정적 출력 순서(첫 등장 순)
}

// New 는 빈 누적기를 만든다.
func New() *Aggregator {
	return &Aggregator{sessions: map[string]*sessionAgg{}}
}

// AddFile 은 롤아웃 파일 하나를 누적한다. fallbackID 는 `session_meta` 가 없거나 그 id 가
// 서버 모양에 안 맞을 때 쓸 값이다(대개 SessionIDFromPath 의 결과).
//
// 파일을 먼저 통째로 파일-로컬 누적기에 담고 **끝난 뒤에** 세션 id 를 확정해 합치는 이유:
// Codex 는 줄마다 세션 id 를 싣지 않는다(파일이 곧 세션이다). 그래서 id 는 파일을 다 읽어야
// 확실해지고, 그 전에 맵에 꽂으면 나중에 키를 옮겨야 한다.
//
// 깨진 줄은 조용히 건너뛴다 — 텔레메트리 한 줄이 깨졌다고 그 세션 전체를 버리지 않는다.
func (a *Aggregator) AddFile(fallbackID string, r io.Reader) error {
	fa := newSessionAgg()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		fa.addLine(line)
	}
	fa.flushPendingTurn()
	fa.endStream()

	id := fa.metaID
	if !policy.SessionIDRe.MatchString(id) {
		id = fallbackID
	}
	fa.id = id
	if dst := a.sessions[id]; dst != nil {
		dst.merge(fa)
	} else {
		a.sessions[id] = fa
		a.order = append(a.order, id)
	}
	return sc.Err()
}

func (sa *sessionAgg) merge(src *sessionAgg) {
	if src.project != "" {
		sa.project = src.project
	}
	if src.model != "" {
		sa.model = src.model
	}
	if src.minTs != "" && (sa.minTs == "" || src.minTs < sa.minTs) {
		sa.minTs = src.minTs
	}
	if src.maxTs > sa.maxTs {
		sa.maxTs = src.maxTs
	}
	sa.input += src.input
	sa.cacheRead += src.cacheRead
	sa.output += src.output
	sa.inputLong += src.inputLong
	sa.cacheReadLong += src.cacheReadLong
	sa.outputLong += src.outputLong
	sa.taskStarted += src.taskStarted
	sa.noTsTurn += src.noTsTurn
	for k := range src.turnIDs {
		sa.turnIDs[k] = true
	}
	for m, n := range src.modelTokens {
		sa.modelTokens[m] += n
	}
	for k, b := range src.buckets {
		d := sa.buckets[k]
		if d == nil {
			cp := *b
			sa.buckets[k] = &cp
			continue
		}
		d.Input += b.Input
		d.Output += b.Output
		d.CacheRead += b.CacheRead
		d.Turns += b.Turns
	}
	sa.legacy.merge(&src.legacy)
	sa.paged.merge(&src.paged)
	sa.sawItemCompleted = sa.sawItemCompleted || src.sawItemCompleted
}

// ── 줄 처리 ───────────────────────────────────────────────────────────────────

func (sa *sessionAgg) addLine(raw []byte) {
	var ln rawLine
	if err := json.Unmarshal(raw, &ln); err != nil {
		return
	}
	if ln.Timestamp != "" {
		if sa.minTs == "" || ln.Timestamp < sa.minTs {
			sa.minTs = ln.Timestamp
		}
		if ln.Timestamp > sa.maxTs {
			sa.maxTs = ln.Timestamp
		}
	}
	if len(ln.Payload) == 0 {
		return
	}

	switch ln.Type {
	case "session_meta":
		var m sessionMeta
		if json.Unmarshal(ln.Payload, &m) != nil {
			return
		}
		if m.ID != "" {
			sa.metaID = m.ID
		} else if m.SessionID != "" {
			sa.metaID = m.SessionID
		}
		if m.Cwd != "" {
			sa.project = baseName(m.Cwd) // 경로 원문은 남기지 않는다 — basename 만(Claude 와 동일)
		}
	case "turn_context":
		var c turnContext
		if json.Unmarshal(ln.Payload, &c) != nil {
			return
		}
		if c.Model != "" {
			sa.model = c.Model
		}
		// 이제야 이 턴의 모델을 알았다 — 붙들고 있던 턴을 여기서 꽂는다.
		sa.flushPendingTurn()
		if c.Cwd != "" && sa.project == "" {
			sa.project = baseName(c.Cwd)
		}
		if c.TurnID != "" {
			sa.turnIDs[c.TurnID] = true
		}
	case "event_msg":
		sa.addEvent(ln.Payload, ln.Timestamp)
	case "response_item":
		sa.addResponseItem(ln.Payload)
	case "inter_agent_communication":
		sa.addAgentSignal(ln.Payload)
	}
}

func (sa *sessionAgg) addEvent(raw json.RawMessage, ts string) {
	var head payloadHead
	if json.Unmarshal(raw, &head) != nil {
		return
	}
	switch head.Type {
	case "token_count":
		var p tokenCountPayload
		if json.Unmarshal(raw, &p) != nil || p.Info == nil || p.Info.TotalTokenUsage == nil {
			return // info 없는 token_count 가 실제로 있다(실데이터 2건) — 조용히 지나간다
		}
		sa.addTokens(*p.Info.TotalTokenUsage, ts)

	case "task_started":
		sa.taskStarted++
		var c turnContext
		if json.Unmarshal(raw, &c) == nil && c.TurnID != "" {
			sa.turnIDs[c.TurnID] = true
		}
		// 앞 턴이 모델을 못 본 채 끝났으면(turn_context 누락) 지금 아는 모델로 확정하고 넘어간다.
		sa.flushPendingTurn()
		hour, ok := hourOf(ts)
		if !ok {
			// 시각을 못 읽으면 시계열에 올릴 자리가 없다. 넘겨짚지 않고 관측 축으로 센다.
			sa.noTsTurn++
			return
		}
		sa.pendingTurnHour, sa.pendingTurnOpen = hour, true

	case "user_message":
		var u userMessage
		if json.Unmarshal(raw, &u) == nil {
			sa.keywordsInto(&sa.legacy, u.Message)
		}

	case "web_search_end":
		sa.legacy.webSearch++

	case "patch_apply_end":
		var p patchApplyEnd
		if json.Unmarshal(raw, &p) == nil {
			applyPatch(&sa.legacy, p.Changes, p.Success, p.Status)
		}

	case "sub_agent_activity":
		sa.addAgentSignal(raw)

	case "item_completed":
		sa.sawItemCompleted = true
		var p itemCompleted
		if json.Unmarshal(raw, &p) != nil {
			return
		}
		item := p.Item
		if len(item) == 0 {
			item = p.TurnItem
		}
		if len(item) > 0 {
			sa.addTurnItem(item)
		}

		// mcp_tool_call_end 는 의도적으로 무시한다 — 같은 호출이 이미 function_call 로 한 번
		// 세어졌다(namespace=mcp__*). 둘 다 세면 mcp 축이 정확히 2배가 된다.
	}
}

func (sa *sessionAgg) addResponseItem(raw json.RawMessage) {
	var head payloadHead
	if json.Unmarshal(raw, &head) != nil {
		return
	}
	switch head.Type {
	case "function_call":
		var fc functionCall
		if json.Unmarshal(raw, &fc) != nil || fc.Name == "" {
			return
		}
		// MCP 호출은 mcp 축에만 싣는다(기존 Claude 파서와 같은 규율 — tool 축에 겹쳐 세지 않는다).
		// 키는 Claude 가 쓰는 것과 **같은 모양**으로 맞춘다: mcp__<server>__<tool>.
		// namespace 자체가 이미 `mcp__<server>` 라 여기에 도구명만 붙이면 된다.
		if strings.HasPrefix(fc.Namespace, "mcp__") {
			sa.legacy.count("mcp", policy.NormKeyOf("mcp", fc.Namespace+"__"+fc.Name))
			return
		}
		sa.legacy.count("tool", policy.NormKeyOf("tool", fc.Name))
		if fc.Name == "exec_command" && fc.Arguments != "" {
			var ea execArgs
			if json.Unmarshal([]byte(fc.Arguments), &ea) == nil {
				// BashKey 는 **선두 실행파일명 하나만** 남긴다. 인자는 절대 저장하지 않는다.
				sa.legacy.count("bash", policy.BashKey(ea.Cmd))
			}
		}

	case "custom_tool_call":
		var fc functionCall
		if json.Unmarshal(raw, &fc) == nil && fc.Name != "" {
			sa.legacy.count("tool", policy.NormKeyOf("tool", fc.Name))
		}

		// web_search_call 은 세지 않는다 — event_msg/web_search_end 가 같은 검색의 반대편이다.
	}
}

func (sa *sessionAgg) addAgentSignal(raw json.RawMessage) {
	var s subAgentActivity
	if json.Unmarshal(raw, &s) != nil {
		return
	}
	for _, cand := range []string{s.AgentType, s.Agent, s.Nickname, s.Name} {
		if cand != "" {
			sa.legacy.count("agent", policy.NormKeyOf("agent", cand))
			return
		}
	}
}

// addTurnItem 은 신버전 TurnItem 을 축으로 접는다(paged 쪽에 담는다).
func (sa *sessionAgg) addTurnItem(raw json.RawMessage) {
	var it turnItem
	if json.Unmarshal(raw, &it) != nil {
		return
	}
	kind := variantKey(it.Type)
	if kind == "" {
		kind = variantKey(it.ItemType)
	}
	switch kind {
	case "commandexecution":
		cmd := it.Command
		if cmd == "" {
			cmd = it.Cmd
		}
		sa.paged.count("tool", "exec_command")
		sa.paged.count("bash", policy.BashKey(cmd))

	case "mcptoolcall":
		if it.Server != "" && it.Tool != "" {
			sa.paged.count("mcp", policy.NormKeyOf("mcp", "mcp__"+it.Server+"__"+it.Tool))
		}

	case "websearch":
		sa.paged.webSearch++

	case "subagentactivity":
		for _, cand := range []string{it.AgentType, it.Agent, it.Name} {
			if cand != "" {
				sa.paged.count("agent", policy.NormKeyOf("agent", cand))
				break
			}
		}

	case "filechange":
		applyPatch(&sa.paged, it.Changes, it.Success, it.Status)

	case "usermessage":
		sa.keywordsInto(&sa.paged, it.Message)
		for _, c := range it.Content {
			switch c.Type {
			case "skill":
				sa.paged.count("skill", policy.NormKeyOf("skill", c.Name))
			case "text", "input_text":
				sa.keywordsInto(&sa.paged, c.Text)
			}
		}
	}
}

// variantKey 는 serde 표기 차이를 지운다: `CommandExecution` · `command_execution` 둘 다
// `commandexecution` 이 된다. 신버전을 실증할 수 없는 상태에서 표기 하나로 축이 통째로
// 비는 것을 막는 방어다.
func variantKey(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToLower(strings.ReplaceAll(s, "_", ""))
}

// ── 토큰 ──────────────────────────────────────────────────────────────────────

// addTokens 는 누적 토큰값 하나를 받는다.
//
// 세션 합계는 **마지막 누적값**으로, 시계열은 **직전 누적값과의 차분**으로 만든다.
// 둘을 같은 스트림에서 뽑으므로 `sum(buckets) == 세션 합계` 가 항상 성립한다 —
// 대시보드에서 합계와 시계열이 서로 다른 말을 하는 일이 없다.
func (sa *sessionAgg) addTokens(t tokenUsage, ts string) {
	d := t
	if sa.streamCur != nil {
		if t.Total < sa.streamCur.Total {
			// 누적 카운터가 되감겼다 — 앞 스트림을 확정하고 새로 시작한다. 그러지 않으면
			// 차분이 음수가 되어 시계열이 뒤로 간다.
			sa.endStream()
		} else {
			d = tokenUsage{
				Input:  t.Input - sa.streamCur.Input,
				Cached: t.Cached - sa.streamCur.Cached,
				Output: t.Output - sa.streamCur.Output,
				Total:  t.Total - sa.streamCur.Total,
			}
		}
	}
	cur := t
	sa.streamCur = &cur

	// 모델별 분해: 지금 유효한 turn_context 의 모델에 이 차분을 귀속한다.
	sa.modelTokens[sa.model] += d.Input + d.Output

	hour, ok := hourOf(ts)
	if !ok {
		return // 합계는 endStream 이 책임진다 — 시계열만 포기한다
	}
	b := sa.bucket(hour, sa.model)
	b.Input += netInput(d)
	b.CacheRead += d.Cached
	b.Output += d.Output
	// b.CacheCreate 는 건드리지 않는다 — Codex 는 cache write 토큰을 기록하지 않는다.
	// 롱 몫 — 총량에 더한 뒤 **같은 값**을 롱 쪽에도 더한다(부분집합이므로 빼지 않는다).
	if isLongRequest(d) {
		b.InputLong += netInput(d)
		b.CacheReadLong += d.Cached
		b.OutputLong += d.Output
		// CacheCreateLong 은 채우지 않는다 — CacheCreate 자체가 0 이라 몫이 있을 수 없다.
	}
}

// endStream 은 현재 스트림의 마지막 누적값을 세션 합계로 확정한다.
func (sa *sessionAgg) endStream() {
	t := sa.streamCur
	if t == nil {
		return
	}
	sa.input += netInput(*t)
	sa.cacheRead += t.Cached
	sa.output += t.Output // reasoning_output_tokens 는 여기 이미 들어 있다 — 더하지 않는다
	// 버킷 쪽(addBucket)과 **같은 판정·같은 값**이어야 한다. 한쪽만 세면 세션 합계와 시간
	// 뷰의 비용이 갈리고, 두 화면이 다른 값을 말하게 된다.
	if isLongRequest(*t) {
		sa.inputLong += netInput(*t)
		sa.cacheReadLong += t.Cached
		sa.outputLong += t.Output
	}
	sa.streamCur = nil
}

/*
 * ── 계단(롱컨텍스트) 임계 ─────────────────────────────────────────────────
 *
 * OpenAI 는 **한 요청의 입력 컨텍스트가 272K 토큰을 넘으면** 그 요청 전체를 롱 단가로 매긴다
 * (입력 2배 · 출력 1.5배). 계단이 있는 모델은 gpt-5.4 · gpt-5.5 · gpt-5.6 계열과 *-pro 다 —
 * 어느 모델인지는 **서버 단가표가 판정한다**(cost.LongContextPrice). 수집기가 할 일은
 * "이 요청이 임계를 넘었나"뿐이고, 그것은 요청 단위를 보는 이쪽만 알 수 있다.
 *
 * 모델별로 임계를 가르지 않는 이유: 공식 표의 계단 임계는 OpenAI 전체가 272K 로 같다.
 * 계단이 **없는** 모델에서 이 몫이 올라와도 서버가 롱 단가를 안 갖고 있어 표준가로 계산한다
 * (cost 의 LongPricingFlat) — 즉 여기서 넉넉하게 표시해도 비용이 부풀지 않는다.
 * 반대로 임계를 모델별로 흉내내면 수집기와 서버 두 곳에 단가 지식이 갈라져 놓인다.
 */
const longContextThreshold = 272_000

/*
 * isLongRequest 는 이 턴이 계단 구간이었는지다.
 *
 * 기준은 **input_tokens** 다(캐시 히트를 포함한 그 요청의 전체 입력 컨텍스트). netInput 이
 * 아니다 — 캐시된 프리픽스도 컨텍스트 길이에 들어가고, 공식 임계는 컨텍스트 길이로 매겨진다.
 * netInput 으로 재면 캐시가 많은 세션이 임계를 못 넘는 것으로 잘못 판정된다(실측 워크로드에서
 * 캐시읽기가 입력의 90% 이상이라 그 오판이 사실상 상시화된다).
 */
func isLongRequest(t tokenUsage) bool { return t.Input > longContextThreshold }

// netInput 은 캐시 히트를 뺀 순수 입력이다. OpenAI 는 cached_input_tokens 를
// input_tokens 의 **부분집합**으로 싣는다(실측 검증). 빼지 않으면 입력이 이중 계상된다.
func netInput(t tokenUsage) int64 {
	n := t.Input - t.Cached
	if n < 0 {
		return 0 // 이상 데이터 방어 — 음수 토큰은 서버에서 더 나쁜 모양이 된다
	}
	return n
}

func (sa *sessionAgg) bucket(hour, model string) *payload.Bucket {
	if model == "" {
		model = "(미상)"
	}
	key := hour + "|" + model
	b := sa.buckets[key]
	if b == nil {
		b = &payload.Bucket{Hour: hour, Model: model}
		sa.buckets[key] = b
	}
	return b
}

// ── LOC / 키워드 ──────────────────────────────────────────────────────────────

// applyPatch 는 패치 결과에서 **줄 수와 성패만** 뽑는다. 파일 경로도 내용도 저장하지 않는다.
func applyPatch(ax *axes, changes map[string]patchChange, success *bool, status string) {
	for _, ch := range changes {
		switch {
		case ch.UnifiedDiff != "":
			add, del := diffLines(ch.UnifiedDiff)
			ax.linesAdded += add
			ax.linesRemoved += del
		case ch.Type == "delete":
			ax.linesRemoved += policy.LineCount(ch.Content)
		default:
			ax.linesAdded += policy.LineCount(ch.Content)
		}
	}
	// 수락/거부는 **판정할 수 있을 때만** 센다. 판정 불가인데 0 을 보내면 서버는 그것을
	// "거부가 없었다"로 읽는다 — 모르는 것과 0 은 다르다.
	switch {
	case success != nil:
		if *success {
			ax.editsAccepted++
		} else {
			ax.editsRejected++
		}
	case status == "completed" || status == "succeeded":
		ax.editsAccepted++
	case status == "failed" || status == "error" || status == "rejected":
		ax.editsRejected++
	}
}

// diffLines 는 unified diff 에서 추가·삭제 **줄 수만** 센다(내용은 버린다).
func diffLines(d string) (added, removed int64) {
	for _, ln := range strings.Split(d, "\n") {
		switch {
		case strings.HasPrefix(ln, "+++"), strings.HasPrefix(ln, "---"):
			continue // 파일 헤더 — 경로가 들어 있고 변경 줄도 아니다
		case strings.HasPrefix(ln, "+"):
			added++
		case strings.HasPrefix(ln, "-"):
			removed++
		}
	}
	return added, removed
}

// keywordsInto 는 사람이 친 텍스트에서만 낱말을 뽑는다. `<` 로 시작하면 하네스가 끼워 넣은
// 블록이라 통째로 버린다. 실제 낱말 필터(시크릿·PII·랜덤)는 policy.Keywords 가 진다.
func (sa *sessionAgg) keywordsInto(ax *axes, s string) {
	if s == "" || strings.HasPrefix(strings.TrimSpace(s), "<") {
		return
	}
	for _, w := range policy.Keywords(s) {
		ax.count("keyword", w)
	}
}

// ── 출력 ──────────────────────────────────────────────────────────────────────

// Sessions 는 누적을 payload 로 접는다. 아무 신호도 없는 세션(턴 0·토큰 0·카운터 0)은 내지
// 않고, 서버가 거부할 모양의 id 도 내지 않는다.
func (a *Aggregator) Sessions() []payload.Session {
	out := make([]payload.Session, 0, len(a.order))
	for _, sid := range a.order {
		sa := a.sessions[sid]
		if !policy.SessionIDRe.MatchString(sid) {
			continue
		}
		// 구·신 포맷 중 하나만 채택한다 — 섞어 세면 정확히 2배가 된다.
		ax := &sa.legacy
		if sa.sawItemCompleted {
			ax = &sa.paged
		}
		// 신호가 없는 세션은 내지 않는다. 판정은 **실제 활동**으로만 한다 — turn_id 는
		// turn_context 만 있어도 생기므로(턴이 실제로 돌지 않아도 기록된다) 여기 쓰면
		// 아무 일도 없었던 세션이 턴 1개짜리로 올라온다.
		if sa.taskStarted == 0 && sa.input == 0 && sa.output == 0 && sa.cacheRead == 0 &&
			len(ax.counters) == 0 && ax.webSearch == 0 {
			continue
		}
		// turns 는 task_started 가 정본이고, 그것을 안 싣는 버전에서만 distinct turn_id 로 떨어진다.
		turns := sa.taskStarted
		if turns == 0 {
			turns = int64(len(sa.turnIDs))
		}
		noTs := sa.noTsTurn
		out = append(out, payload.Session{
			ID:        sid,
			Platform:  Platform,
			StartedAt: sa.minTs,
			EndedAt:   sa.maxTs,
			Project:   sa.project,
			Model:     sa.dominantModel(),

			Input:     sa.input,
			Output:    sa.output,
			CacheRead: sa.cacheRead,
			// 계단 몫 — 서버가 이 값에 롱 단가를 매긴다(모델별 계단 유무는 서버 단가표가 판정).
			InputLong:     sa.inputLong,
			OutputLong:    sa.outputLong,
			CacheReadLong: sa.cacheReadLong,
			// CacheCreate 는 항상 0 이다. Codex 롤아웃에 cache write 토큰이 없다 —
			// 관측 불가이지 "0 이라고 관측한 것"이 아니다.
			CacheCreate: 0,
			WebSearch:   ax.webSearch,
			// WebFetch 는 원천이 없다(Codex 는 fetch 를 따로 세지 않는다).
			Turns:     turns,
			NoTsTurns: &noTs,

			LinesAdded:    ax.linesAdded,
			LinesRemoved:  ax.linesRemoved,
			EditsAccepted: ax.editsAccepted,
			EditsRejected: ax.editsRejected,

			Counters: capCounters(ax.counters),
			Series:   sa.sortedBuckets(),
		})
	}
	return out
}

// dominantModel 은 토큰 합이 가장 큰 모델을 세션 대표로 고른다. 동점은 이름 오름차순으로
// 깬다 — 맵 순회 무작위성에 기대면 실행마다 대표 모델이 바뀐다.
func (sa *sessionAgg) dominantModel() string {
	best := ""
	var bestN int64 = -1
	for model, n := range sa.modelTokens {
		if model == "" {
			continue
		}
		if n > bestN || (n == bestN && model < best) {
			best, bestN = model, n
		}
	}
	if best == "" {
		return sa.model
	}
	return best
}

func (sa *sessionAgg) sortedBuckets() []payload.Bucket {
	rows := make([]payload.Bucket, 0, len(sa.buckets))
	for _, b := range sa.buckets {
		rows = append(rows, *b)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Hour != rows[j].Hour {
			return rows[i].Hour < rows[j].Hour
		}
		return rows[i].Model < rows[j].Model
	})
	if len(rows) > policy.MaxSeriesPerSession {
		rows = rows[:policy.MaxSeriesPerSession]
	}
	return rows
}

// capCounters 는 서버(normCounters)와 같은 규칙으로 좁힌다: 축마다 count 내림차순·키
// 오름차순으로 상위 PerKindMax, 축은 CounterKinds 순, 전체는 MaxCountersPerSession 상한.
//
// 값이 없는 축은 **키 자체를 만들지 않는다.** slash 가 대표적이다 — Codex 로컬 기록엔
// 신뢰할 소스가 없어 0 을 보내면 서버가 "슬래시를 한 번도 안 썼다"로 읽는다. 빈 축은
// "미지원"이고 0 은 "관측했고 0"이라, 둘을 섞으면 대시보드가 거짓을 말한다.
func capCounters(in map[string]map[string]int64) map[string]map[string]int64 {
	out := map[string]map[string]int64{}
	total := 0
	for _, kind := range policy.CounterKinds {
		src := in[kind]
		if len(src) == 0 {
			continue
		}
		type kv struct {
			k string
			v int64
		}
		entries := make([]kv, 0, len(src))
		for k, v := range src {
			if v > 0 {
				entries = append(entries, kv{k, v})
			}
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].v != entries[j].v {
				return entries[i].v > entries[j].v
			}
			return entries[i].k < entries[j].k
		})
		if len(entries) > policy.PerKindMax {
			entries = entries[:policy.PerKindMax]
		}
		for _, e := range entries {
			if total >= policy.MaxCountersPerSession {
				break
			}
			m := out[kind]
			if m == nil {
				m = map[string]int64{}
				out[kind] = m
			}
			m[e.k] = e.v
			total++
		}
		if total >= policy.MaxCountersPerSession {
			break
		}
	}
	return out
}

// hourOf 는 타임스탬프를 UTC 시각 버킷(YYYY-MM-DDTHH)으로 접는다. **존이 있어야 한다** —
// 존 없는 값을 UTC 로 넘겨짚으면 그 팀원의 버킷이 실제와 몇 시간씩 어긋난다.
func hourOf(ts string) (string, bool) {
	if ts == "" {
		return "", false
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return "", false
	}
	return t.UTC().Format("2006-01-02T15"), true
}

func baseName(p string) string {
	p = strings.TrimRight(p, `/\`)
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}
