// Package gemini 은 Gemini CLI 의 채팅 기록(jsonl)을 세션 합계로 좁히는 **순수** 계층이다.
//
// 원천은 `<geminiDir>/tmp/<projectSlug>/chats/` 아래다:
//
//	session-<타임스탬프>-<sessionId 앞 8자>.jsonl   ← 메인 세션
//	<parentSessionId>/<sessionId>.jsonl            ← 서브에이전트
//
// 이 패키지는 `internal/transcript`(Claude)·`internal/codex` 와 **같은 payload.Session 을 낸다.**
// 축 이름·키 정규화·상한은 전부 `internal/policy` 를 공유한다.
//
// ── 이 파일에서 가장 위험한 두 곳 ────────────────────────────────────────────
//
//  1. **리플레이.** 세션 파일은 append-only 로그이지 메시지 목록이 아니다. 세 종류의
//     레코드가 섞여 있고 그중 둘은 **이미 읽은 것을 되돌린다**:
//     · `{"$rewindTo":"<id>"}`  — 그 id **를 포함해** 뒤를 전부 지운다. id 가 없으면 **전부** 지운다.
//     · `{"$set":{...}}`        — 메타 갱신. `messages` 가 실려 있으면 목록을 비우고 그 배열로 재구축한다.
//     그래서 tail 파싱(마지막 N줄만 읽기)이 불가능하다 — **항상 파일 처음부터 전부 리플레이한다.**
//     증분은 "파일이 바뀌었나"로만 판단하고(기존 state 지문), 바뀌었으면 그 파일을 통째로 다시 읽는다.
//
//  2. **토큰 정규화.** Gemini 회계는 Claude·Codex 와 또 다르다:
//     Input       = max(0, tokens.input - tokens.cached)  ← promptTokenCount 는 cached 를 **포함**한다
//     CacheRead   = tokens.cached
//     Output      = tokens.output + tokens.thoughts       ← thoughts 는 candidatesTokenCount 에 **미포함**인데
//     출력 단가로 과금된다. 안 더하면 추론이 무거운 세션이 통째로 과소계산된다.
//     (⚠ Codex 와 정반대다 — Codex 의 reasoning 은 output 에 이미 포함이라 더하면 이중 계상이다.)
//     CacheCreate = 0                                     ← "미지원"이 아니라 **해당 없음**이다.
//     Gemini 는 암시적 캐싱이라 캐시 쓰기 과금 개념 자체가 없다.
//     `tokens.tool`(toolUsePromptTokenCount)은 **더하지 않는다** — 입력에 포함된 내역으로 보이지만
//     공식 문서에 명시가 없다(불확실. 실데이터로 확인 필요).
//
// 표준 라이브러리와 collector/internal/{payload,policy} 말고 아무것도 import 하지 않는다.
package gemini

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
const Platform = "gemini"

// chatsDir 는 세션 파일이 사는 디렉터리 이름이다(소스: chatRecordingService, `path.join(projectTempDir,'chats')`).
const chatsDir = "chats"

// 도구 이름 — 소스 `packages/core/src/tools/definitions/base-declarations.ts` 의 상수 값 그대로다.
const (
	toolShell     = "run_shell_command" // args.command
	toolSkill     = "activate_skill"    // args.name
	toolAgent     = "invoke_agent"      // args.agent_name  ← Claude 의 Task/subagent_type 대응
	toolWebSearch = "google_web_search"
	toolWebFetch  = "web_fetch"

	// 편집 계열은 이 **둘뿐**이다(소스 tool-names.ts: `EDIT_TOOL_NAMES = new Set([EDIT_TOOL_NAME,
	// WRITE_FILE_TOOL_NAME])`). Claude 의 MultiEdit 에 해당하는 도구는 없다.
	toolWrite   = "write_file" // args.content
	toolReplace = "replace"    // args.old_string / args.new_string ← Claude 의 Edit 와 같은 모양

	// 도구 호출의 종료 상태(소스 scheduler/types.ts `CoreToolCallStatus`).
	//
	// 세션 파일에는 **종료된 호출만** 실린다 — geminiChat.ts 의 recordCompletedToolCalls 가
	// `CompletedToolCall = success | cancelled | error` 만 받아 recordToolCalls 로 넘긴다.
	// 그래서 여기 셋 말고 다른 값(validating·executing 등)은 실제로는 안 나오지만, 나오면
	// **적용 여부를 모르는 것**으로 다룬다(0 으로 위조하지 않는다).
	statusSuccess   = "success"
	statusError     = "error"
	statusCancelled = "cancelled"

	// mcpPrefix — MCP 도구는 이름이 `mcp_<server>_<tool>` 로 생성된다(소스 mcp-tool.ts:
	// MCP_TOOL_PREFIX='mcp_', MCP_QUALIFIED_NAME_SEPARATOR='_').
	//
	// ⚠ **서버명을 이름에서 잘라내지 마라.** 구분자가 `_` 하나라 서버명·도구명에 `_` 가 있으면
	// 경계를 알 수 없고, 게다가 63자를 넘으면 generateValidName 이 **이름 가운데를 `...` 로 잘라낸다.**
	// 그래서 여기서는 도구명 **원문을 그대로** mcp 축 키로 쓴다. Claude 의 `mcp__<server>__<tool>`
	// 표기와는 모양이 다르다 — 맞출 수가 없다(정보가 원천에 없다). 대시보드에서 두 플랫폼의
	// mcp 키가 다르게 보이는 것은 의도된 것이다.
	mcpPrefix = "mcp_"
)

// maxLineBytes — 한 줄의 상한. 도구 결과가 한 줄에 통째로 실린다(소스 상한 MAX_TOOL_OUTPUT_SIZE=50KB
// 이지만 한 메시지에 여러 도구 호출이 붙는다). 잘린 줄은 JSON 파싱에 실패해 조용히 사라지고,
// 그러면 그 턴의 토큰이 통째로 없어진다. 넉넉히 잡아 그 침묵을 없앤다.
const maxLineBytes = 16 * 1024 * 1024

// hashLikeRe — 구버전 Gemini CLI 는 프로젝트 임시 디렉터리 이름으로 프로젝트 경로의 sha256 을
// 썼다(현재는 슬러그로 마이그레이션한다). 해시는 사람이 읽을 이름이 아니므로 project 로 쓰지 않는다.
var hashLikeRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ── 경로 규칙 ─────────────────────────────────────────────────────────────────

// Match 는 스캔 대상 파일인지 판정한다.
//
// **제외가 이 함수의 존재 이유다.** ChatRecordingService 는 저장 중에 곁가지 파일을 남긴다:
//
//	<파일>.jsonl.unreadable-<ms>  — 읽지 못한 원본 보존본
//	<파일>.jsonl.tmp-<pid>        — 원자적 쓰기용 임시본
//
// 둘 다 같은 세션의 **전체 사본**이라, 세션 파일로 오인하면 그 세션이 통째로 두 번 세어진다.
// (확장자 규칙만으로도 걸러지지만, 이름 규칙이 바뀌어도 안 새도록 명시적으로 막는다.)
//
// 또 `<geminiDir>/tmp/<slug>/` 아래에는 checkpoints·logs·plans 같은 다른 `.json` 이 잔뜩 있다.
// 그래서 `chats/` 안(또는 그 바로 아래 서브에이전트 디렉터리 안)만 받는다.
func Match(path string) bool {
	base := filepath.Base(path)
	if strings.Contains(base, ".unreadable-") || strings.Contains(base, ".tmp-") {
		return false
	}
	if !strings.HasSuffix(base, ".jsonl") && !strings.HasSuffix(base, ".json") {
		return false
	}
	dir := filepath.Dir(path)
	if filepath.Base(dir) == chatsDir {
		return true // chats/session-....jsonl
	}
	return filepath.Base(filepath.Dir(dir)) == chatsDir // chats/<parentId>/<sid>.jsonl
}

// SessionKeyFromPath 는 파일을 묶을 그룹 키를 낸다(증분 판정 단위이자 세션 id 폴백).
//
// 서브에이전트 파일은 **부모 세션 id 디렉터리명**으로 묶는다. Claude 파서가 사이드체인을 부모
// 세션에 합산하는 것과 같은 규율이다 — 서브에이전트를 별도 세션으로 올리면 대시보드의
// "세션 수"가 위임 횟수만큼 부풀고, 부모 세션의 비용이 실제보다 작게 보인다.
//
// 레거시 `.json` 과 현행 `.jsonl` 은 stem 이 같아 **같은 그룹**이 된다. 그래야 둘 중 하나만
// 바뀌어도 둘 다 다시 읽어 온전한 절대값을 낸다(한쪽만 읽으면 과소집계로 덮인다).
func SessionKeyFromPath(path string) string {
	dir := filepath.Dir(path)
	if filepath.Base(dir) != chatsDir {
		return filepath.Base(dir) // 서브에이전트 → 부모 세션 id
	}
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, ".jsonl")
	return strings.TrimSuffix(base, ".json")
}

// ProjectFromPath 는 경로에서 프로젝트 이름을 뽑는다.
//
// `chats` 바로 앞 경로 요소가 프로젝트 슬러그다. 이 슬러그는 소스가 `slugify(basename(projectRoot))`
// 로 만든 것이라(projectRegistry.claimNewSlug) **이미 basename 이다** — 경로 원문이 아니다.
// 그래서 Claude·Codex 가 `basename(cwd)` 를 쓰는 것과 같은 수준의 값이고, 경로 저장 금지 정책을
// 그대로 지킨다. `projects.json` 도 `.project_root` 도 읽지 않는다(둘 다 **절대경로 원문**을 담고 있어
// 읽을 이유가 없다).
func ProjectFromPath(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i := len(parts) - 1; i > 0; i-- {
		if parts[i] != chatsDir {
			continue
		}
		slug := parts[i-1]
		if slug == "" || hashLikeRe.MatchString(slug) {
			return "" // 구버전 해시 디렉터리 — 사람이 읽을 이름이 아니라 비운다
		}
		return slug
	}
	return ""
}

// ── 레코드 모양 ───────────────────────────────────────────────────────────────
//
// 스키마 전체를 모델링하지 않는다 — 매핑에 쓰는 필드만 좁게 받는다.

type tokensRec struct {
	Input    int64 `json:"input"`  // promptTokenCount — cached 를 **포함**한다
	Output   int64 `json:"output"` // candidatesTokenCount — thoughts 는 **미포함**이다
	Cached   int64 `json:"cached"`
	Thoughts int64 `json:"thoughts"`
	// Tool 은 **읽기만 하고 더하지 않는다.** toolUsePromptTokenCount 가 promptTokenCount 에
	// 포함되는지 공식 명시가 없다(불확실). 필드를 남겨 두는 이유는 다음 사람이 "왜 안 쓰지?"를
	// 코드에서 바로 보게 하기 위해서다.
	Tool  int64 `json:"tool"`
	Total int64 `json:"total"`
}

type toolCallRec struct {
	Name string   `json:"name"`
	Args toolArgs `json:"args"`
	// Status 는 종료 상태다(success | error | cancelled). LOC 를 "적용된 편집"으로 좁히고
	// editsAccepted/Rejected 를 판정하는 **유일한** 근거다.
	Status string `json:"status"`
	// AgentID 는 **쓰지 않는다.** 소스에서 `randomUUID()` 라(agents/local-executor.ts) 축 키로 쓰면
	// agent 축이 매 호출 새로 생기는 uuid 로 가득 찬다. 위임 대상 이름은 `invoke_agent` 의
	// `args.agent_name` 에 있고, 그쪽이 Claude 의 `subagent_type` 과 같은 의미다.
	AgentID string `json:"agentId"`
}

// toolArgs 는 도구 인자 중 **집계에 쓰는 것만** 담는다. 커스텀 UnmarshalJSON 을 쓰는 이유가 둘이다.
//
//  1. **편집 본문을 붙들지 않는다.** content·old_string·new_string 을 문자열 필드로 받으면 파일
//     원문이 세션 누적이 끝날 때까지(fold 시점까지) 메모리에 남는다. 큰 write_file 몇 개면 수백
//     MB 가 되고, 무엇보다 "내용은 저장하지 않는다"는 정책이 메모리 수준에서 깨진다.
//     여기서는 파싱하는 그 자리에서 **줄 수로 바꾸고 문자열을 버린다** — 원문은 이 함수를 벗어나지 않는다.
//
//  2. **타입이 어긋난 인자에 관대하다.** MCP 도구가 `content` 를 객체로, `command` 를 숫자로 싣는
//     일이 흔하다. 구조체 태그로 받으면 그 한 필드 때문에 messageRec 전체 unmarshal 이 실패하고
//     **그 턴의 토큰까지 통째로 사라진다**(거부가 아니라 침묵이라 더 나쁘다). 문자열일 때만 읽는다.
type toolArgs struct {
	Command   string // run_shell_command
	Name      string // activate_skill
	AgentName string // invoke_agent

	// 줄 수만 남는다. 소문자(비공개)인 것은 의도다 — 밖에서 원문을 기대하지 못하게 한다.
	contentLines   int64 // write_file.content
	oldStringLines int64 // replace.old_string
	newStringLines int64 // replace.new_string
}

// UnmarshalJSON 은 **절대 실패하지 않는다.** args 가 객체가 아니어도 도구 이름 축은 살린다 —
// 인자 하나 때문에 그 메시지의 토큰을 버리는 것이 훨씬 비싼 실수다.
func (a *toolArgs) UnmarshalJSON(b []byte) error {
	var obj map[string]json.RawMessage
	if json.Unmarshal(b, &obj) != nil {
		return nil
	}
	str := func(key string) string {
		raw, ok := obj[key]
		if !ok {
			return ""
		}
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return "" // 문자열이 아니면 모르는 것으로 둔다(추정 금지)
		}
		return s
	}
	a.Command = str("command")
	a.Name = str("name")
	a.AgentName = str("agent_name")
	a.contentLines = policy.LineCount(str("content"))
	a.oldStringLines = policy.LineCount(str("old_string"))
	a.newStringLines = policy.LineCount(str("new_string"))
	return nil
}

type messageRec struct {
	ID        string          `json:"id"`
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"` // user | gemini | info | error | warning
	Content   json.RawMessage `json:"content"`
	Model     string          `json:"model"`
	Tokens    *tokensRec      `json:"tokens"`
	ToolCalls []toolCallRec   `json:"toolCalls"`
}

// convMeta 는 리플레이가 확정한 세션 메타다. `$set` 으로 **sessionId 자체가 바뀔 수 있어**
// 파일명에서 sessionId 를 파싱하지 않는다 — 최종 메타 값만 믿는다.
type convMeta struct {
	SessionID   string
	StartTime   string
	LastUpdated string
	Kind        string // main | subagent
}

// ── 리플레이 ──────────────────────────────────────────────────────────────────

// replay 는 파일 하나를 처음부터 재생한 결과다.
//
// 메시지를 맵 + 등장 순서 슬라이스로 드는 이유는 두 가지다:
//   - 같은 id 가 다시 나오면 **뒤엣것이 이긴다**(소스 `messagesMap.set(id, record)`).
//   - 단 순서는 **처음 등장한 자리**를 지킨다(JS Map 의 의미론). `$rewindTo` 가 이 순서를
//     기준으로 "그 뒤"를 지우므로, 순서가 어긋나면 되돌리는 범위가 통째로 달라진다.
type replay struct {
	meta  convMeta
	msgs  map[string]*messageRec
	order []string
}

func newReplay() *replay { return &replay{msgs: map[string]*messageRec{}} }

func (rp *replay) set(m *messageRec) {
	if m.ID == "" {
		return
	}
	if _, ok := rp.msgs[m.ID]; !ok {
		rp.order = append(rp.order, m.ID)
	}
	rp.msgs[m.ID] = m
}

func (rp *replay) clearMessages() {
	rp.msgs = map[string]*messageRec{}
	rp.order = nil
}

// rewindTo 는 `{"$rewindTo": id}` 를 재현한다.
//
// ⚠ 두 가지가 직관과 다르다(소스 chatRecordingService.ts:189-201 기준):
//   - 지우는 범위는 그 id **를 포함해서** 뒤 전부다(`if (id === rewindId) found = true; if (found) push`).
//   - 그 id 가 지금까지 읽은 것 중에 **없으면 메시지를 전부 지운다**(`else { messagesMap.clear(); }`).
//     이걸 "못 찾았으니 아무것도 안 한다"로 구현하면, 되돌린 세션의 토큰이 통째로 남아 과대계상이 된다.
func (rp *replay) rewindTo(id string) {
	idx := -1
	for i, k := range rp.order {
		if k == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		rp.clearMessages()
		return
	}
	for _, k := range rp.order[idx:] {
		delete(rp.msgs, k)
	}
	rp.order = rp.order[:idx]
}

// applyMeta 는 있는 키만 덮어쓴다. 소스가 `{...metadata, ...record.$set}` 로 얕게 펼치므로
// **없는 키는 이전 값을 유지한다** — 구조체를 통째로 갈아치우면 startTime 이 사라진다.
func (rp *replay) applyMeta(obj map[string]json.RawMessage) {
	assign := func(key string, dst *string) {
		raw, ok := obj[key]
		if !ok {
			return
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			*dst = s
		}
	}
	assign("sessionId", &rp.meta.SessionID)
	assign("startTime", &rp.meta.StartTime)
	assign("lastUpdated", &rp.meta.LastUpdated)
	assign("kind", &rp.meta.Kind)
}

// addMessagesArray 는 `messages: [...]` 배열을 순서대로 넣는다.
func (rp *replay) addMessagesArray(raw json.RawMessage) {
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) != nil {
		return
	}
	for _, e := range arr {
		var m messageRec
		if json.Unmarshal(e, &m) == nil && m.ID != "" {
			rp.set(&m)
		}
	}
}

// applyRecord 는 레코드 하나를 재생한다. **판정 순서가 소스와 같아야 한다**(chatRecordingService.ts:172-342):
// rewind → message(문자열 id) → `$set`(객체) → 부분 메타(sessionId+projectHash 둘 다 문자열).
func (rp *replay) applyRecord(line []byte, obj map[string]json.RawMessage) {
	if raw, ok := obj["$rewindTo"]; ok {
		var id string
		if json.Unmarshal(raw, &id) == nil {
			rp.rewindTo(id)
			return
		}
	}
	if raw, ok := obj["id"]; ok {
		var id string
		if json.Unmarshal(raw, &id) == nil {
			var m messageRec
			if json.Unmarshal(line, &m) == nil {
				m.ID = id
				rp.set(&m)
			}
			return
		}
	}
	if raw, ok := obj["$set"]; ok {
		var set map[string]json.RawMessage
		if json.Unmarshal(raw, &set) == nil {
			// 체크포인트: messages 가 실려 있으면 목록을 **통째로 비우고** 이 배열로 재구축한다.
			// (소스가 `messagesMap.clear()` 후 재삽입한다 — 누적이 아니다.)
			if msgs, ok := set["messages"]; ok && isJSONArray(msgs) {
				rp.clearMessages()
				rp.addMessagesArray(msgs)
			}
			rp.applyMeta(set)
			return
		}
	}
	// 첫 줄 메타(또는 레거시 `.json` 한 덩어리). 소스는 sessionId·projectHash 가 **둘 다 문자열**일
	// 때만 메타로 인정한다 — 그 판정을 그대로 따른다.
	if isJSONString(obj["sessionId"]) && isJSONString(obj["projectHash"]) {
		rp.applyMeta(obj)
		if msgs, ok := obj["messages"]; ok && isJSONArray(msgs) {
			rp.addMessagesArray(msgs) // 여기서는 비우지 않는다(소스와 동일)
		}
	}
}

// replayAll 은 파일 내용 전체를 재생한다.
//
// 한 덩어리 JSON(레거시 `.json`) 과 JSONL 을 **같은 코드로** 처리한다: 먼저 파일 전체를 객체
// 하나로 읽어 보고, 실패하면 줄 단위로 간다. 레거시 파일은 예쁘게 들여쓴 여러 줄일 수 있어
// 줄 단위로만 읽으면 통째로 버려진다.
//
// 깨진 줄은 **그 줄만** 건너뛴다 — 텔레메트리 한 줄이 깨졌다고 그 세션 전체를 버리지 않는다.
func replayAll(data []byte) *replay {
	rp := newReplay()
	var whole map[string]json.RawMessage
	if json.Unmarshal(data, &whole) == nil {
		rp.applyRecord(data, whole)
		return rp
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(line, &obj) != nil {
			continue // 깨진 줄 — 이 줄만 버린다
		}
		rp.applyRecord(line, obj)
	}
	return rp
}

func isJSONString(raw json.RawMessage) bool {
	var s string
	return len(raw) > 0 && json.Unmarshal(raw, &s) == nil
}

func isJSONArray(raw json.RawMessage) bool {
	var a []json.RawMessage
	return len(raw) > 0 && json.Unmarshal(raw, &a) == nil
}

// ── 세션 누적 ─────────────────────────────────────────────────────────────────

type sessionAgg struct {
	project string
	minTs   string
	maxTs   string

	// msgs 는 **세션 전체**의 메시지다(파일 단위가 아니다). 레거시 `.json` 과 현행 `.jsonl` 이
	// 같은 세션의 같은 메시지를 각자 들고 있을 수 있어, 파일마다 합산하면 그 세션이 정확히
	// 두 배가 된다. 그래서 메시지 **id 로 합친다** — 같은 id 는 한 번만 센다.
	msgs  map[string]*messageRec
	order []string
}

func newSessionAgg() *sessionAgg {
	return &sessionAgg{msgs: map[string]*messageRec{}}
}

func (sa *sessionAgg) put(m *messageRec) {
	if _, ok := sa.msgs[m.ID]; !ok {
		sa.order = append(sa.order, m.ID)
	}
	sa.msgs[m.ID] = m
}

func (sa *sessionAgg) seeTs(ts string) {
	if ts == "" {
		return
	}
	if sa.minTs == "" || ts < sa.minTs {
		sa.minTs = ts
	}
	if ts > sa.maxTs {
		sa.maxTs = ts
	}
}

// Aggregator 는 여러 파일을 세션 id 로 묶어 누적한다.
type Aggregator struct {
	sessions map[string]*sessionAgg
	order    []string // 결정적 출력 순서(첫 등장 순)
}

// New 는 빈 누적기를 만든다.
func New() *Aggregator { return &Aggregator{sessions: map[string]*sessionAgg{}} }

// AddFile 은 경로를 모르는 호출부를 위한 형태다(프로젝트 이름이 비게 된다).
func (a *Aggregator) AddFile(fallbackID string, r io.Reader) error {
	return a.AddFileAt("", fallbackID, r)
}

// AddFileAt 은 세션 파일 하나를 누적한다.
//
// path 는 프로젝트 슬러그를 읽는 데만 쓴다(경로 원문은 저장하지 않는다).
// fallbackID 는 그룹 키다 — 서브에이전트 파일에서는 **부모 세션 id** 다.
func (a *Aggregator) AddFileAt(path, fallbackID string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	rp := replayAll(data)

	// 세션 키 결정. 서브에이전트는 자기 sessionId 가 따로 있지만 **부모에 합산한다**
	// (Claude 파서가 사이드체인을 부모 세션에 합산하는 것과 같은 규율).
	sid := rp.meta.SessionID
	if rp.meta.Kind == "subagent" && fallbackID != "" {
		sid = fallbackID
	}
	if !policy.SessionIDRe.MatchString(sid) {
		sid = fallbackID
	}
	if !policy.SessionIDRe.MatchString(sid) {
		return nil // 귀속할 세션이 없다 — 서버가 거부할 id 를 만들어 보내지 않는다
	}

	sa := a.sessions[sid]
	if sa == nil {
		sa = newSessionAgg()
		a.sessions[sid] = sa
		a.order = append(a.order, sid)
	}
	if p := ProjectFromPath(path); p != "" {
		sa.project = p
	}
	sa.seeTs(rp.meta.StartTime)
	sa.seeTs(rp.meta.LastUpdated)
	for _, id := range rp.order {
		sa.put(rp.msgs[id])
	}
	return nil
}

// ── 축·토큰 접기 ──────────────────────────────────────────────────────────────

// folded 는 세션 하나를 전부 읽고 접은 결과다. 접기를 Sessions() 시점에 하는 이유:
// 리플레이가 메시지를 되돌릴 수 있고 파일 사이에 중복이 있어, **최종 메시지 집합이
// 확정된 뒤에야** 합계를 낼 수 있다. 중간에 더하면 되돌린 토큰이 남는다.
type folded struct {
	input, cacheRead, output int64
	// 계단 몫(위 총량의 부분집합) — 세션 총량이 버킷 파생이 아니라 별도 누적이므로 같이 센다.
	inputLong, cacheReadLong, outputLong int64
	turns, noTsTurn                      int64
	webSearch, webFetch                  int64

	// 개발 지표(파생). write_file·replace 호출에서 **줄 수만** 센다(내용 미저장).
	linesAdded, linesRemoved     int64
	editsAccepted, editsRejected int64

	model       string
	modelTokens map[string]int64
	buckets     map[string]*payload.Bucket
	counters    map[string]map[string]int64
}

func (sa *sessionAgg) fold() *folded {
	f := &folded{
		modelTokens: map[string]int64{},
		buckets:     map[string]*payload.Bucket{},
		counters:    map[string]map[string]int64{},
	}
	for _, id := range sa.order {
		m := sa.msgs[id]
		if m == nil {
			continue
		}
		f.addMessage(m)
	}
	return f
}

func (f *folded) count(kind, key string) {
	if key == "" {
		return
	}
	mm := f.counters[kind]
	if mm == nil {
		mm = map[string]int64{}
		f.counters[kind] = mm
	}
	mm[key]++
}

func (f *folded) addMessage(m *messageRec) {
	switch m.Type {
	case "user":
		// keyword 축은 사람이 친 말에서만 뽑는다. `<` 로 시작하면 하네스가 끼워 넣은 블록이라
		// 통째로 버린다. 실제 낱말 필터(시크릿·PII·랜덤)는 policy.Keywords 가 진다.
		s := strings.TrimFunc(partsText(m.Content), func(r rune) bool { return r == ' ' || r == '\n' || r == '\t' || r == '\r' })
		if s != "" && !strings.HasPrefix(s, "<") {
			for _, w := range policy.Keywords(s) {
				f.count("keyword", w)
			}
		}
		// slash 축은 **만들지 않는다.** Gemini 세션 파일에 슬래시 명령이 남지 않는다
		// (텔레메트리 전용). 0 을 보내면 서버가 "한 번도 안 썼다"로 읽는다 — 모르는 것과 0 은 다르다.

	case "gemini":
		if m.Model != "" {
			f.model = m.Model
		}
		f.addToolCalls(m)
		if m.Tokens == nil {
			return // 과금 턴이 아니다(도구 결과만 실린 조각 등)
		}
		t := *m.Tokens
		in, out := netInput(t), netOutput(t)
		f.input += in
		f.cacheRead += t.Cached
		f.output += out
		/*
		 * 롱 몫 — 총량에 더한 뒤 같은 값을 롱 쪽에도 더한다(부분집합이므로 빼지 않는다).
		 * 버킷 쪽과 **같은 판정·같은 값**이어야 한다: 한쪽만 세면 세션 합계와 시간 뷰의
		 * 비용이 갈리고 두 화면이 다른 값을 말한다.
		 */
		long := isLongRequest(t)
		if long {
			f.inputLong += in
			f.cacheReadLong += t.Cached
			f.outputLong += out
		}
		f.turns++
		f.modelTokens[m.Model] += in + out + t.Cached

		hour, ok := hourOf(m.Timestamp)
		if !ok {
			// 시각을 못 읽으면 시계열에 올릴 자리가 없다. 합계는 위에서 이미 반영됐다.
			f.noTsTurn++
			return
		}
		b := f.bucket(hour, m.Model)
		b.Input += in
		b.Output += out
		b.CacheRead += t.Cached
		if long {
			b.InputLong += in
			b.OutputLong += out
			b.CacheReadLong += t.Cached
		}
		b.Turns++
		// b.CacheCreate 는 건드리지 않는다 — Gemini 는 암시적 캐싱이라 캐시 **쓰기** 과금이 없다.

	default:
		// info · error · warning — 사람 입력도 모델 응답도 아니다. 축에 싣지 않는다.
	}
}

// addToolCalls 는 도구 호출을 축별 카운터로 접는다(정책 준수: 인자 원문은 절대 저장하지 않는다).
func (f *folded) addToolCalls(m *messageRec) {
	for _, tc := range m.ToolCalls {
		name := tc.Name
		if name == "" {
			continue
		}
		switch {
		case strings.HasPrefix(name, mcpPrefix):
			// MCP 는 mcp 축에만 싣는다(Claude·Codex 와 같은 규율 — tool 축에 겹쳐 세지 않는다).
			f.count("mcp", policy.NormKeyOf("mcp", name))
		case name == toolShell:
			f.count("tool", toolShell)
			// BashKey 는 **선두 실행파일명 하나만** 남긴다. 인자는 절대 저장하지 않는다.
			f.count("bash", policy.BashKey(tc.Args.Command))
		case name == toolSkill:
			f.count("tool", toolSkill)
			f.count("skill", policy.NormKeyOf("skill", tc.Args.Name))
		case name == toolAgent:
			f.count("tool", toolAgent)
			f.count("agent", policy.NormKeyOf("agent", tc.Args.AgentName))
		case name == toolWebSearch:
			f.count("tool", toolWebSearch)
			f.webSearch++
		case name == toolWebFetch:
			f.count("tool", toolWebFetch)
			f.webFetch++
		case name == toolWrite || name == toolReplace:
			f.count("tool", name)
			f.editUse(tc)
		default:
			f.count("tool", policy.NormKeyOf("tool", name))
		}
	}
}

// editUse 는 편집 도구 호출 하나를 접는다(정책: 줄 수·횟수만, 내용·경로는 절대 안 남긴다).
//
// ── 이 함수가 채우는 것은 **의미가 다른 두 축**이다. 섞지 마라 ──────────────────
//
//	linesAdded/linesRemoved  = **제안된 편집 규모.** 모델이 얼마나 큰 편집을 시도했나.
//	                           거부·실패분도 **포함해서** 센다. 상태로 거르지 않는다.
//	editsAccepted/Rejected   = **결과.** 그 시도가 받아들여졌나. 상태로만 판정한다.
//
// 규모를 결과로 거르면 두 축이 같은 말을 두 번 하게 되고, 정작 "얼마나 크게 시도했나"를
// 아무도 말하지 않게 된다. 무엇보다 **Claude 파서는 구조적으로 거를 수가 없다** —
// tool_use 시점에 세고 결과(tool_result)는 다른 줄에 온다(transcript.go:366 editUse).
// 그래서 여기서 성공만 세면 같은 행동을 해도 Gemini 만 수치가 작게 나오고, 대시보드의
// 플랫폼 비교가 조용히 거짓이 된다. 세 파서가 맞출 수 있는 방향은 "전부 센다" 하나뿐이다.
func (f *folded) editUse(tc toolCallRec) {
	// ① 규모 — 상태와 무관하게 센다.
	switch tc.Name {
	case toolWrite:
		// 덮어쓰기라면 원본이 인자에 없어 **삭제 줄을 알 수 없다.** 추정하지 않고 추가만 센다
		// (Claude 의 Write 와 같은 처리).
		f.linesAdded += tc.Args.contentLines
	case toolReplace:
		// allow_multiple=true 로 여러 군데가 한 번에 바뀌어도 **몇 군데인지는 인자에 없다.**
		// 한 번으로 센다 — 모르는 배수를 곱하는 것이 안 세는 것보다 나쁘다.
		f.linesAdded += tc.Args.newStringLines
		f.linesRemoved += tc.Args.oldStringLines
	}

	// ② 결과 — 종료 상태로만 판정한다.
	switch tc.Status {
	case statusSuccess:
		f.editsAccepted++
	case statusError, statusCancelled:
		f.editsRejected++
	default:
		// 종료 상태가 아니거나(validating·executing…) 필드가 비었다 — 받아들여졌는지 **모른다.**
		// 모르는 것을 0 으로 위조하지 않는다. 어느 쪽도 세지 않는다(Codex 의 applyPatch 와 같은 규율).
		// ⚠ 규모(①)는 이 경우에도 이미 세어졌다. 그게 맞다 — 시도한 것은 시도한 것이다.
	}
}

func (f *folded) bucket(hour, model string) *payload.Bucket {
	if model == "" {
		model = "(미상)"
	}
	key := hour + "|" + model
	b := f.buckets[key]
	if b == nil {
		b = &payload.Bucket{Hour: hour, Model: model}
		f.buckets[key] = b
	}
	return b
}

// netInput 은 캐시 히트를 뺀 순수 입력이다.
//
/*
 * ── 계단(롱컨텍스트) 임계 ─────────────────────────────────────────────────
 *
 * Google 은 **한 요청의 입력 컨텍스트가 200K 토큰을 넘으면** 그 요청의 입력·출력을 함께 롱
 * 단가로 매긴다. 공식 문구가 범위를 못 박는다:
 *   "If a query input context is longer than 200K tokens, all tokens (input and output)
 *    are charged at long context rates."
 * OpenAI(272K)와 임계가 다르므로 상수를 각 수집기가 자기 것으로 갖는다.
 *
 * 계단이 있는 모델은 pro 계열뿐이지만(flash 는 없다) 모델별로 가르지 않는다 — 계단 단가를
 * 가진 모델인지는 **서버 단가표가 판정한다**(cost.LongContextPrice). 계단이 없는 모델에서
 * 이 몫이 올라와도 서버가 표준가로 계산하므로 비용이 부풀지 않는다(LongPricingFlat).
 */
const longContextThreshold = 200_000

/*
 * isLongRequest — 기준은 **promptTokenCount**(tokens.input)다. 캐시된 몫을 포함한 그 요청의
 * 전체 입력 컨텍스트이고, 공식 임계도 컨텍스트 길이로 매겨진다. netInput(캐시 뺀 값)으로
 * 재면 캐시가 많은 세션이 임계를 못 넘는 것으로 잘못 판정된다.
 */
func isLongRequest(t tokensRec) bool { return t.Input > longContextThreshold }

// `tokens.input` 은 promptTokenCount 이고 여기에 cachedContentTokenCount 가 **포함**돼 있다.
// Gemini CLI 자신도 표시할 때 같은 뺄셈을 한다. 빼지 않으면 입력이 이중 계상되어 그대로 비용이 된다.
func netInput(t tokensRec) int64 {
	n := t.Input - t.Cached
	if n < 0 {
		return 0 // 이상 데이터 방어 — 음수 토큰은 서버에서 더 나쁜 모양이 된다
	}
	return n
}

// netOutput 은 사고(thoughts) 토큰을 더한 출력이다.
//
// thoughtsTokenCount 는 candidatesTokenCount 에 **포함되지 않는데** 출력 단가로 과금된다.
// 안 더하면 추론이 무거운 세션이 통째로 과소계산된다. (Codex 와 정반대다 — 헷갈리지 마라.)
func netOutput(t tokensRec) int64 { return t.Output + t.Thoughts }

// partsText 는 PartListUnion(문자열 | Part | Part[])에서 텍스트만 이어 붙인다.
// 텍스트가 아닌 파트(inlineData 등)는 통째로 버린다 — 어휘 통계에 의미가 없고 원문 보관 금지다.
func partsText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) == nil {
		var b strings.Builder
		for _, e := range arr {
			if t := partsText(e); t != "" {
				b.WriteString(t)
				b.WriteString(" ")
			}
		}
		return b.String()
	}
	var one struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &one) == nil {
		return one.Text
	}
	return ""
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
		f := sa.fold()
		if f.turns == 0 && f.input == 0 && f.output == 0 && f.cacheRead == 0 && len(f.counters) == 0 {
			continue
		}
		// 메시지 타임스탬프도 세션 구간에 반영한다(메타의 lastUpdated 가 뒤처져 있을 수 있다).
		minTs, maxTs := sa.minTs, sa.maxTs
		for _, id := range sa.order {
			if m := sa.msgs[id]; m != nil && m.Timestamp != "" {
				if minTs == "" || m.Timestamp < minTs {
					minTs = m.Timestamp
				}
				if m.Timestamp > maxTs {
					maxTs = m.Timestamp
				}
			}
		}
		noTs := f.noTsTurn
		out = append(out, payload.Session{
			ID:        sid,
			Platform:  Platform,
			StartedAt: minTs,
			EndedAt:   maxTs,
			Project:   sa.project,
			Model:     f.dominantModel(),

			Input:     f.input,
			Output:    f.output,
			CacheRead: f.cacheRead,
			// 계단 몫 — 서버가 이 값에 롱 단가를 매긴다(모델별 계단 유무는 서버 단가표가 판정).
			InputLong:     f.inputLong,
			OutputLong:    f.outputLong,
			CacheReadLong: f.cacheReadLong,
			// CacheCreate 는 항상 0 이다 — Gemini 는 암시적 캐싱이라 캐시 쓰기 과금 개념이
			// **해당 없음**이다("미지원"이 아니다).
			CacheCreate: 0,
			WebSearch:   f.webSearch,
			WebFetch:    f.webFetch,
			Turns:       f.turns,
			NoTsTurns:   &noTs,

			// 편집이 하나도 없으면 넷 다 0 이고 omitempty 로 본문에서 빠진다. 그게 맞다 —
			// Gemini 는 편집을 기록하는 세션에서만 이 값을 관측한다.
			LinesAdded:    f.linesAdded,
			LinesRemoved:  f.linesRemoved,
			EditsAccepted: f.editsAccepted,
			EditsRejected: f.editsRejected,

			Counters: capCounters(f.counters),
			Series:   f.sortedBuckets(),
		})
	}
	return out
}

// dominantModel 은 토큰 합이 가장 큰 모델을 세션 대표로 고른다. 동점은 이름 오름차순으로
// 깬다 — 맵 순회 무작위성에 기대면 실행마다 대표 모델이 바뀐다.
func (f *folded) dominantModel() string {
	best := ""
	var bestN int64 = -1
	for model, n := range f.modelTokens {
		if model == "" {
			continue
		}
		if n > bestN || (n == bestN && model < best) {
			best, bestN = model, n
		}
	}
	if best == "" {
		return f.model
	}
	return best
}

func (f *folded) sortedBuckets() []payload.Bucket {
	rows := make([]payload.Bucket, 0, len(f.buckets))
	for _, b := range f.buckets {
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
// 값이 없는 축은 **키 자체를 만들지 않는다.** Gemini 에서는 slash 가 그렇다 — 세션 파일에
// 소스가 없어 0 을 보내면 서버가 "슬래시를 한 번도 안 썼다"로 읽는다. 빈 축은 "미지원"이고
// 0 은 "관측했고 0"이라, 둘을 섞으면 대시보드가 거짓을 말한다.
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
