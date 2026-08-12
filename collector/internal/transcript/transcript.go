// Package transcript 는 Claude Code 트랜스크립트(jsonl)를 세션 합계로 좁히는 **순수** 계층이다.
//
// 원천은 `~/.claude/projects/<슬러그>/<sessionId>.jsonl` 이고, 한 줄이 하나의 JSON 레코드다.
// 여기서 하는 일은 두 가지다:
//
//	① 매핑.  message.usage 의 토큰을 세션 합계와 시간×모델 버킷으로 접는다.
//	② 정책.  집계만 남긴다 — 프롬프트 원문·파일경로·명령인자는 애초에 뽑지 않는다. 어휘가
//	         열린 축(keyword)은 policy 패키지가 시크릿·PII 모양을 버린다.
//
// ── 왜 세션 단위로 누적하나 ────────────────────────────────────────────────
// 세션이 재개되면 같은 sessionId 가 여러 파일에 흩어질 수 있다. 서버는 session_id 절대값으로
// 덮어쓰므로, 한 세션을 파일마다 따로 보내면 뒤 파일이 앞 파일을 덮어 **과소집계**가 된다.
// 그래서 Aggregator 는 sessionId 로 묶어 누적한 뒤 한 번에 절대값을 낸다.
//
// 표준 라이브러리와 collector/internal/policy 말고 아무것도 import 하지 않는다.
package transcript

import (
	"bufio"
	"encoding/json"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tscorp/user-usage/collector/internal/payload"
	"github.com/tscorp/user-usage/collector/internal/policy"
)

// maxLineBytes — 한 줄(레코드)의 상한. 트랜스크립트 한 줄에 큰 tool_result 가 실릴 수 있어
// bufio 기본 버퍼(64KB)로는 잘린다. 잘린 줄은 JSON 파싱에 실패해 조용히 버려지는데, 그러면
// 그 턴의 토큰이 통째로 사라진다. 넉넉히 잡아 그 침묵을 없앤다.
const maxLineBytes = 16 * 1024 * 1024

// slashRe 는 사용자 메시지에 끼워진 슬래시 명령 이름을 뽑는다. **이름만** 남긴다 —
// 인자(command-args)는 정책상 절대 남기지 않는다.
var slashRe = regexp.MustCompile(`<command-name>\s*(/?[A-Za-z0-9:_.-]+)`)

/*
 * 모델 이름 자리의 자리표시자.
 *
 * Claude Code 는 중단·오류처럼 **모델이 관여하지 않은 턴**의 model 자리에 `<synthetic>` 을
 * 직접 쓴다(그 턴의 usage 는 전부 0). 이름이 아니라 "이름이 없다"는 표시다.
 *
 * 서버(`go/internal/intake`)가 최종 경계이고 거기서도 접는다 — 구버전 수집기가 계속 밀어
 * 넣으므로 서버만이 유일하게 빠뜨릴 자리가 없는 지점이다. 그런데도 여기서 또 접는 이유는
 * **여기서만 고칠 수 있는 것이 하나 있어서**다: `sa.model` 은 "마지막으로 본 모델"이고
 * tool_result 오류가 그 버킷에 달린다. 중단 턴이 그 자리를 차지하면 그 뒤의 도구 오류가
 * 실재하는 모델에서 떨어져 나가고, 서버는 집계만 보므로 그 귀속을 되돌릴 수 없다.
 * 대표 모델(dominantModel)도 마찬가지다 — 토큰 0 인 자리표시자가 대표가 되어선 안 된다.
 *
 * 판정을 `<synthetic>` 한 값이 아니라 꺾쇠로 감싼 값 전체로 두는 이유는 서버 쪽 주석과 같다:
 * 실제 과금 ID 에는 `<`·`>` 가 없으므로 이 모양은 전부 자리표시자다. 두 규칙은 같아야 한다.
 */
var placeholderModelRe = regexp.MustCompile(`^<[^<>]*>$`)

// normModel 은 자리표시자를 **빈 값**으로 접는다. 버리는 것이 아니라 "모른다"로 되돌리는
// 것이다 — 빈 모델의 처리 규칙(버킷은 '(미상)', 대표 모델 후보에서 제외)은 이미 이 파일에
// 한 벌씩 있고, 그 자리로 합류시킨다. 턴·토큰·카운터는 그대로 센다.
func normModel(s string) string {
	s = strings.TrimSpace(s)
	if placeholderModelRe.MatchString(s) {
		return ""
	}
	return s
}

// ── 파싱 대상 모양 ────────────────────────────────────────────────────────────
//
// jsonl 스키마 전체를 모델링하지 않는다 — 매핑에 쓰는 필드만 좁게 받는다. 모르는 필드는
// encoding/json 이 조용히 버린다(그게 맞다: 스키마가 버전마다 흔들려도 여기가 안 깨진다).

type rawLine struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	SessionID string          `json:"sessionId"`
	Cwd       string          `json:"cwd"`
	Message   json.RawMessage `json:"message"`
}

type rawMessage struct {
	Role       string          `json:"role"`
	Model      string          `json:"model"`
	StopReason string          `json:"stop_reason"`
	Usage      *rawUsage       `json:"usage"`
	Content    json.RawMessage `json:"content"`
}

type rawUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheCreation            *struct {
		Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
		Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
	} `json:"cache_creation"`
	ServerToolUse *struct {
		WebSearch int64 `json:"web_search_requests"`
		WebFetch  int64 `json:"web_fetch_requests"`
	} `json:"server_tool_use"`
}

type rawBlock struct {
	Type      string          `json:"type"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	IsError   bool            `json:"is_error"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`          // tool_use 블록의 id
	ToolUseID string          `json:"tool_use_id"` // tool_result 가 가리키는 tool_use id
}

type rawToolInput struct {
	Command      string `json:"command"`
	SubagentType string `json:"subagent_type"`
	Skill        string `json:"skill"`
	// LOC 파생용(Edit/Write/MultiEdit). 내용 자체는 저장하지 않고 **줄 수만** 센다(정책 준수).
	Content   string `json:"content"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
	Edits     []struct {
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	} `json:"edits"`
}

// ── 누적기 ────────────────────────────────────────────────────────────────────

// bucketAgg 는 시간×모델 버킷 하나의 누적 상태다.
type bucketAgg struct {
	b payload.Bucket
}

// sessionAgg 는 세션 하나의 누적 상태다. 맵으로 들고 있다가 Sessions() 에서 payload 로 접는다.
type sessionAgg struct {
	id       string
	project  string // 마지막으로 본 cwd 의 basename
	minTs    string
	maxTs    string
	model    string // 마지막으로 본 usage 턴의 모델(toolErrors 귀속용)
	noTsTurn int64

	input, output, cacheRead, cacheCreate int64
	webSearch, webFetch, turns            int64

	// 개발 지표(파생). Edit/Write/MultiEdit tool_use 에서 **줄 수만** 센다(내용 미저장).
	linesAdded, linesRemoved     int64
	editsAccepted, editsRejected int64

	modelTokens map[string]int64            // 세션 대표 모델 선정용(토큰 합 최대)
	buckets     map[string]*bucketAgg       // key = hour|model
	counters    map[string]map[string]int64 // 축 → 키 → 횟수
	pendingEdit map[string]bool             // 편집 tool_use id → 결과 대기(accept/reject 판정용)
}

func newSessionAgg(id string) *sessionAgg {
	return &sessionAgg{
		id:          id,
		modelTokens: map[string]int64{},
		buckets:     map[string]*bucketAgg{},
		counters:    map[string]map[string]int64{},
		pendingEdit: map[string]bool{},
	}
}

// Aggregator 는 여러 파일의 줄을 sessionId 로 묶어 누적한다.
type Aggregator struct {
	sessions map[string]*sessionAgg
	order    []string // 결정적 출력 순서(첫 등장 순)
	fallback string   // 줄에 sessionId 가 없을 때 쓸 값(대개 파일명 stem)
}

// New 는 빈 누적기를 만든다.
func New() *Aggregator {
	return &Aggregator{sessions: map[string]*sessionAgg{}}
}

// AddFile 은 파일 하나의 모든 줄을 누적한다. fallbackID 는 줄에 sessionId 필드가 없을 때
// 쓸 값(대개 파일명 stem)이다. 깨진 줄은 조용히 건너뛴다 — 텔레메트리 한 줄이 깨졌다고 그
// 세션 전체를 버리지 않는다.
func (a *Aggregator) AddFile(fallbackID string, r io.Reader) error {
	a.fallback = fallbackID
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		a.addLine(line)
	}
	return sc.Err()
}

func (a *Aggregator) addLine(raw []byte) {
	var ln rawLine
	if err := json.Unmarshal(raw, &ln); err != nil {
		return
	}
	sid := ln.SessionID
	if sid == "" {
		sid = a.fallback
	}
	if sid == "" {
		return // 귀속할 세션이 없다
	}

	sa := a.sessions[sid]
	if sa == nil {
		sa = newSessionAgg(sid)
		a.sessions[sid] = sa
		a.order = append(a.order, sid)
	}

	if ln.Cwd != "" {
		sa.project = baseName(ln.Cwd)
	}
	if ln.Timestamp != "" {
		if sa.minTs == "" || ln.Timestamp < sa.minTs {
			sa.minTs = ln.Timestamp
		}
		if ln.Timestamp > sa.maxTs {
			sa.maxTs = ln.Timestamp
		}
	}

	if len(ln.Message) == 0 {
		return
	}
	var m rawMessage
	if err := json.Unmarshal(ln.Message, &m); err != nil {
		return
	}

	switch ln.Type {
	case "assistant":
		sa.addAssistant(&m, ln.Timestamp)
	case "user":
		sa.addUser(&m, ln.Timestamp)
	}
}

func (sa *sessionAgg) addAssistant(m *rawMessage, ts string) {
	// 카운터(도구·bash·mcp·agent·skill)는 usage 유무와 무관하게 content 에서 뽑는다.
	sa.scanBlocks(m.Content, ts)

	if m.Usage == nil {
		return // 과금 턴이 아니다(예: thinking-only 조각)
	}
	u := m.Usage
	// 자리표시자는 여기서 빈 값이 된다 — 그래야 sa.model(도구 오류 귀속)과 대표 모델을
	// 빼앗지 않는다. 아래 합계·턴 카운트는 모델과 무관하게 그대로 센다.
	model := normModel(m.Model)
	if model != "" {
		sa.model = model
	}

	sa.input += u.InputTokens
	sa.output += u.OutputTokens
	sa.cacheRead += u.CacheReadInputTokens
	sa.cacheCreate += u.CacheCreationInputTokens
	sa.turns++
	if u.ServerToolUse != nil {
		sa.webSearch += u.ServerToolUse.WebSearch
		sa.webFetch += u.ServerToolUse.WebFetch
	}
	sa.modelTokens[model] += u.InputTokens + u.OutputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens

	hour, ok := hourOf(ts)
	if !ok {
		// 존 없는/깨진 타임스탬프 — 시계열에 올릴 자리가 없다. 합계는 위에서 이미 반영됐다.
		sa.noTsTurn++
		return
	}
	bk := sa.bucket(hour, model)
	bk.b.Input += u.InputTokens
	bk.b.Output += u.OutputTokens
	bk.b.CacheRead += u.CacheReadInputTokens
	bk.b.CacheCreate += u.CacheCreationInputTokens
	if u.CacheCreation != nil {
		bk.b.CC1h += u.CacheCreation.Ephemeral1h
		bk.b.CC5m += u.CacheCreation.Ephemeral5m
	}
	bk.b.Turns++
	switch m.StopReason {
	case "max_tokens":
		bk.b.StopMaxTokens++
	case "refusal":
		bk.b.StopRefusal++
	}
}

func (sa *sessionAgg) addUser(m *rawMessage, ts string) {
	if len(m.Content) == 0 {
		return
	}
	// content 는 문자열이거나 블록 배열이다. 둘 다 받는다.
	var s string
	if err := json.Unmarshal(m.Content, &s); err == nil {
		sa.slashFrom(s)
		sa.keywordsFrom(s)
		return
	}
	var blocks []rawBlock
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return
	}
	for _, b := range blocks {
		switch b.Type {
		case "tool_result":
			if b.IsError {
				if hour, ok := hourOf(ts); ok {
					sa.bucket(hour, sa.model).b.ToolErrors++
				}
			}
			// 편집 계열 tool_use 의 결과면 accept/reject 로 센다(is_error=거부/실패 → reject).
			if b.ToolUseID != "" && sa.pendingEdit[b.ToolUseID] {
				delete(sa.pendingEdit, b.ToolUseID)
				if b.IsError {
					sa.editsRejected++
				} else {
					sa.editsAccepted++
				}
			}
		case "text":
			sa.slashFrom(b.Text)
			sa.keywordsFrom(b.Text)
		}
	}
}

// scanBlocks 는 assistant content 의 tool_use 블록을 축별 카운터로 접는다.
//
// 규칙(정책 준수):
//   - mcp__* 도구는 mcp 축에만 — 이름 전체가 닫힌 어휘라 그대로 키가 된다.
//   - Bash 는 tool 축 + bash 축(BashKey 로 **선두 실행파일명만**, 인자는 절대 남기지 않는다).
//   - Task/Agent 는 tool 축 + agent 축(subagent_type).
//   - Skill 은 tool 축 + skill 축(skill 이름).
//   - 그 밖은 tool 축(도구명).
func (sa *sessionAgg) scanBlocks(content json.RawMessage, _ string) {
	if len(content) == 0 {
		return
	}
	var blocks []rawBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
		return
	}
	for _, b := range blocks {
		if b.Type != "tool_use" || b.Name == "" {
			continue
		}
		name := b.Name
		switch {
		case strings.HasPrefix(name, "mcp__"):
			sa.count("mcp", policy.NormKeyOf("mcp", name))
		case name == "Bash":
			sa.count("tool", "Bash")
			var in rawToolInput
			if json.Unmarshal(b.Input, &in) == nil && in.Command != "" {
				if k := policy.BashKey(in.Command); k != "" {
					sa.count("bash", k)
				}
			}
		case name == "Task" || name == "Agent":
			sa.count("tool", name)
			var in rawToolInput
			if json.Unmarshal(b.Input, &in) == nil && in.SubagentType != "" {
				sa.count("agent", policy.NormKeyOf("agent", in.SubagentType))
			}
		case name == "Skill":
			sa.count("tool", "Skill")
			var in rawToolInput
			if json.Unmarshal(b.Input, &in) == nil && in.Skill != "" {
				sa.count("skill", policy.NormKeyOf("skill", in.Skill))
			}
		case name == "Edit" || name == "Write" || name == "MultiEdit":
			sa.count("tool", name)
			sa.editUse(b)
		default:
			sa.count("tool", policy.NormKeyOf("tool", name))
		}
	}
}

// editUse 는 편집 계열 tool_use 에서 LOC(추가·삭제 줄 수)를 센다. 내용은 저장하지 않는다 —
// 줄 수만 집계한다(집계-온리 정책). 그리고 결과 판정(accept/reject)을 위해 id 를 대기 등록한다.
func (sa *sessionAgg) editUse(b rawBlock) {
	var in rawToolInput
	if json.Unmarshal(b.Input, &in) == nil {
		switch b.Name {
		case "Write":
			sa.linesAdded += policy.LineCount(in.Content)
		case "Edit":
			sa.linesAdded += policy.LineCount(in.NewString)
			sa.linesRemoved += policy.LineCount(in.OldString)
		case "MultiEdit":
			for _, e := range in.Edits {
				sa.linesAdded += policy.LineCount(e.NewString)
				sa.linesRemoved += policy.LineCount(e.OldString)
			}
		}
	}
	if b.ID != "" {
		sa.pendingEdit[b.ID] = true
	}
}

func (sa *sessionAgg) slashFrom(s string) {
	for _, m := range slashRe.FindAllStringSubmatch(s, -1) {
		if k := policy.NormKeyOf("slash", m[1]); k != "" {
			sa.count("slash", k)
		}
	}
}

// keywordsFrom 은 사람이 친 텍스트에서만 낱말을 뽑는다. `<` 로 시작하는 문자열은 주입 블록
// (caveat·command·stdout 등)이라 통째로 건너뛴다 — 정책은 "버리는 쪽"이다. 실제 낱말 필터
// (시크릿·PII·랜덤)는 policy.Keywords 가 진다.
func (sa *sessionAgg) keywordsFrom(s string) {
	if strings.HasPrefix(strings.TrimSpace(s), "<") {
		return
	}
	for _, w := range policy.Keywords(s) {
		sa.count("keyword", w)
	}
}

func (sa *sessionAgg) count(kind, key string) {
	if key == "" {
		return
	}
	m := sa.counters[kind]
	if m == nil {
		m = map[string]int64{}
		sa.counters[kind] = m
	}
	m[key]++
}

func (sa *sessionAgg) bucket(hour, model string) *bucketAgg {
	if model == "" {
		model = "(미상)"
	}
	key := hour + "|" + model
	bk := sa.buckets[key]
	if bk == nil {
		bk = &bucketAgg{b: payload.Bucket{Hour: hour, Model: model}}
		sa.buckets[key] = bk
	}
	return bk
}

// Sessions 는 누적을 payload 로 접는다. 상한·정렬·대표 모델 선정을 여기서 한 번에 한다.
// 아무 신호도 없는 세션(턴 0·토큰 0·카운터 0)은 내지 않는다.
func (a *Aggregator) Sessions() []payload.Session {
	out := make([]payload.Session, 0, len(a.order))
	for _, sid := range a.order {
		sa := a.sessions[sid]
		if !policy.SessionIDRe.MatchString(sid) {
			continue
		}
		if sa.turns == 0 && sa.input == 0 && sa.output == 0 && len(sa.counters) == 0 {
			continue
		}
		noTs := sa.noTsTurn
		s := payload.Session{
			ID:            sid,
			StartedAt:     sa.minTs,
			EndedAt:       sa.maxTs,
			Project:       sa.project,
			Model:         sa.dominantModel(),
			Input:         sa.input,
			Output:        sa.output,
			CacheRead:     sa.cacheRead,
			CacheCreate:   sa.cacheCreate,
			WebSearch:     sa.webSearch,
			WebFetch:      sa.webFetch,
			Turns:         sa.turns,
			NoTsTurns:     &noTs,
			LinesAdded:    sa.linesAdded,
			LinesRemoved:  sa.linesRemoved,
			EditsAccepted: sa.editsAccepted,
			EditsRejected: sa.editsRejected,
			Counters:      capCounters(sa.counters),
			Series:        sa.sortedBuckets(),
		}
		out = append(out, s)
	}
	return out
}

// dominantModel 은 토큰 합이 가장 큰 모델을 세션 대표로 고른다. 멀티모델 세션에서 대표를
// 결정적으로 뽑기 위해 동점은 이름 오름차순으로 깬다(맵 순회 무작위성에 기대지 않는다).
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
	return best
}

func (sa *sessionAgg) sortedBuckets() []payload.Bucket {
	rows := make([]payload.Bucket, 0, len(sa.buckets))
	for _, bk := range sa.buckets {
		rows = append(rows, bk.b)
	}
	// hour 오름차순, 동시각은 model 오름차순 — 출력이 실행마다 흔들리지 않게.
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
// 클라이언트가 먼저 좁혀 두면 서버가 자를 때 상위 N 이 달라지지 않는다.
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
// 존 없는 값을 UTC 로 넘겨짚으면 그 팀원의 버킷이 실제와 몇 시간씩 어긋나므로, 넘겨짚지 않고
// 실패로 돌려(호출부가 noTsTurns 로 센다) 합계만 살린다.
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
