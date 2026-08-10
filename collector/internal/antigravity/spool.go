package antigravity

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tscorp/user-usage/collector/internal/payload"
	"github.com/tscorp/user-usage/collector/internal/policy"
)

// SpoolVersion 은 스풀 포맷 판이다. 모양이 바뀌면 올린다(구판은 조용히 무시한다 —
// 사용량 몇 건보다 수집기가 안 죽는 게 중요하다).
const SpoolVersion = 1

// Usage 는 토큰 네 축이다. statusLine 의 `current_usage` 와 같은 모양이다.
type Usage struct {
	Input       int64 `json:"input"`
	Output      int64 `json:"output"`
	CacheRead   int64 `json:"cacheRead"`
	CacheCreate int64 `json:"cacheCreate"`
}

// Bucket 은 시간(YYYY-MM-DDTHH, UTC)×모델 버킷의 스풀 표현이다.
type Bucket struct {
	Hour   string `json:"hour"`
	Model  string `json:"model"`
	Input  int64  `json:"input"`
	Output int64  `json:"output"`
	Cache  int64  `json:"cacheRead"`
	Turns  int64  `json:"turns"`
}

// Spool 은 대화 하나의 누적 상태다. statusLine 이 쓰고 수집기가 읽는다.
type Spool struct {
	Version        int    `json:"version"`
	ConversationID string `json:"conversationId"`

	Model   string `json:"model,omitempty"`   // API 모델 id **원문**(접미사 유지)
	Effort  string `json:"effort,omitempty"`  // low|medium|high (관측용)
	Project string `json:"project,omitempty"` // 워크스페이스 **basename** 만(경로 저장 금지)

	FirstSeen string `json:"firstSeen,omitempty"`
	LastSeen  string `json:"lastSeen,omitempty"`

	// Invocations 는 서로 다른 current_usage 스냅샷을 본 횟수다(= 모델 호출 수).
	Invocations int64 `json:"invocations"`

	// Last 는 마지막으로 **센** current_usage 다. 프로세스가 죽었다 살아나도 같은
	// invocation 을 두 번 세지 않으려고 디스크에 남긴다 — 실측에서 `--continue` 직후
	// 렌더가 직전 턴의 값을 그대로 다시 뱉었기 때문에 이게 없으면 곧바로 이중계상된다.
	Last Usage `json:"last"`

	// Sum 은 스냅샷 합산이다(입력·캐시읽기의 근거).
	Sum Usage `json:"sum"`

	// TotalOutput 은 관측된 `context_window.total_output_tokens` 의 최댓값이다.
	// 실측 2/2 로 print 모드의 대화 누적 output 과 정확히 일치했다. 최댓값을 쓰는 이유는
	// 컨텍스트 압축(checkpoint)으로 이 값이 되돌아가더라도 절대값이 줄어들지 않게
	// 하기 위해서다 — 수집기는 절대값을 보내므로 감소는 곧 "사용량이 줄었다"가 된다.
	TotalOutput int64 `json:"totalOutput"`

	// ObservedCacheCreate 는 statusLine 이 준 cache_creation_input_tokens 의 합이다.
	// **전송하지 않는다**(Gemini 는 암시적 캐싱이라 생성 과금 개념이 없다). 나중에
	// "정말 0 이 맞나"를 사람이 확인할 수 있게 관측만 남긴다.
	ObservedCacheCreate int64 `json:"observedCacheCreate"`

	Buckets []Bucket `json:"buckets,omitempty"`
}

// Totals 는 전송할 절대값 세 축을 낸다.
//
// 입력: 스냅샷 합산에서 캐시읽기를 뺀 순입력(`cache_read ⊆ input` 이므로 빼지 않으면
// 같은 토큰을 두 축에 두 번 싣는다).
// 출력: 누적값(TotalOutput)과 스냅샷 합산 중 **큰 쪽**. 누적값이 정확하지만 아직
// 관측되지 않았을 수 있어 합산을 하한으로 깐다.
func (s Spool) Totals() (input, output, cacheRead int64) {
	cacheRead = s.Sum.CacheRead
	input = s.Sum.Input - cacheRead
	if input < 0 {
		input = 0
	}
	output = s.Sum.Output
	if s.TotalOutput > output {
		output = s.TotalOutput
	}
	return input, output, cacheRead
}

func (s Spool) sortedBuckets() []payload.Bucket {
	if len(s.Buckets) == 0 {
		return nil
	}
	bs := append([]Bucket(nil), s.Buckets...)
	sort.Slice(bs, func(i, j int) bool {
		if bs[i].Hour != bs[j].Hour {
			return bs[i].Hour < bs[j].Hour
		}
		return bs[i].Model < bs[j].Model
	})
	if len(bs) > policy.MaxSeriesPerSession {
		bs = bs[:policy.MaxSeriesPerSession]
	}
	out := make([]payload.Bucket, 0, len(bs))
	var bucketOut int64
	for _, b := range bs {
		model := b.Model
		if model == "" {
			model = "(미상)"
		}
		net := b.Input - b.Cache
		if net < 0 {
			net = 0
		}
		bucketOut += b.Output
		out = append(out, payload.Bucket{
			Hour:      b.Hour,
			Model:     model,
			Input:     net,
			Output:    b.Output,
			CacheRead: b.Cache,
			Turns:     b.Turns,
		})
	}

	// 버킷 합이 세션 합계와 어긋나는 것을 마지막(가장 최근) 버킷에서 메운다.
	//
	// 왜 필요한가: 세션 출력은 누적값(TotalOutput)을 쓰는데 버킷은 스냅샷 합이라 구조적으로
	// 적다. 실측에서 56 대 51 이었다 — 시간별 차트만 보면 출력이 9% 적게 보인다는 뜻이다.
	// 서버가 이 어긋남을 fromSeries/fromSession 으로 흡수해 총계는 맞지만, 시간축으로
	// 쪼개 보는 화면은 여전히 틀린 값을 그린다. 그래서 여기서 맞춰 보낸다.
	//
	// 마지막 버킷에 몰아주는 이유: 빠진 분(보조 호출·최종 정산)이 실제로 그 대화의 **마지막**
	// 응답에서 생긴 것이라 시각상 가장 가깝다. 임의로 안분하면 없는 시각에 사용량이 생긴다.
	if _, total, _ := s.Totals(); total > bucketOut && len(out) > 0 {
		out[len(out)-1].Output += total - bucketOut
	}
	return out
}

// ── statusLine 수신 ───────────────────────────────────────────────────────────

// statusLineData 는 statusLine 이 stdin 으로 주는 JSON 중 **우리가 쓰는 부분만**이다.
//
// 실측된 전체 키는 다음과 같다(쓰지 않는 것은 일부러 파싱하지 않는다 — 특히 `email`·
// `plan_tier` 는 개인정보라 읽지도 않는다):
//
//	cwd, session_id, conversation_id, transcript_path, model{id,display_name,effort},
//	workspace{current_dir,project_dir}, version, context_window{...}, exceeds_200k_tokens,
//	product, quota{...}, agent_state, vcs, sandbox, plan_tier, email, terminal_width
type statusLineData struct {
	ConversationID string `json:"conversation_id"`
	SessionID      string `json:"session_id"`
	CWD            string `json:"cwd"`

	Model struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
		Effort      string `json:"effort"`
	} `json:"model"`

	Workspace struct {
		CurrentDir string `json:"current_dir"`
		ProjectDir string `json:"project_dir"`
	} `json:"workspace"`

	ContextWindow struct {
		TotalOutputTokens int64 `json:"total_output_tokens"`
		// CurrentUsage 는 응답이 도착해야 채워진다(생성 중에는 null 이다).
		CurrentUsage *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"current_usage"`
	} `json:"context_window"`
}

// Apply 는 statusLine 스냅샷 하나를 스풀에 반영한다. 값이 실제로 바뀌었을 때만 true 다.
//
// **throttle 이 여기 있다.** statusLine 은 렌더마다 불리므로(실측 한 번의 실행에 14회)
// 바뀌지 않았는데 매번 디스크에 쓰면 SSD 를 긁는 것 말고 하는 일이 없다.
func (s *Spool) Apply(d statusLineData, now time.Time) bool {
	changed := false
	ts := now.UTC().Format(time.RFC3339)

	if s.Version != SpoolVersion {
		s.Version = SpoolVersion
		changed = true
	}
	if s.ConversationID == "" && d.ConversationID != "" {
		s.ConversationID = d.ConversationID
		changed = true
	}
	if m := ModelID(d.Model.DisplayName, d.Model.ID, d.Model.Effort); m != "" && m != s.Model {
		s.Model = m
		changed = true
	}
	if e := strings.TrimSpace(d.Model.Effort); e != "" && e != s.Effort {
		s.Effort = e
		changed = true
	}
	if p := projectOf(d); p != "" && p != s.Project {
		s.Project = p
		changed = true
	}

	// 누적 출력은 되돌아가지 않는다(압축으로 줄어도 절대값을 낮추지 않는다).
	if d.ContextWindow.TotalOutputTokens > s.TotalOutput {
		s.TotalOutput = d.ContextWindow.TotalOutputTokens
		changed = true
	}

	if cu := d.ContextWindow.CurrentUsage; cu != nil {
		cur := Usage{
			Input:       cu.InputTokens,
			Output:      cu.OutputTokens,
			CacheRead:   cu.CacheReadInputTokens,
			CacheCreate: cu.CacheCreationInputTokens,
		}
		// 값이 그대로면 **같은 invocation 을 다시 보고 있는 것이다.** 세지 않는다.
		//
		// 한계: 연달아 일어난 두 invocation 이 네 축 모두 똑같은 값을 내면 하나로
		// 세어 과소계상된다. 입력에 컨텍스트가 계속 쌓여 값이 매번 달라지므로 실제로는
		// 거의 없지만, 없다고 말할 수는 없어 여기 적어 둔다.
		if cur != (Usage{}) && cur != s.Last {
			s.Last = cur
			s.Invocations++
			s.Sum.Input += cur.Input
			s.Sum.Output += cur.Output
			s.Sum.CacheRead += cur.CacheRead
			s.ObservedCacheCreate += cur.CacheCreate
			s.addBucket(now, cur)
			changed = true
		}
	}

	if changed {
		if s.FirstSeen == "" {
			s.FirstSeen = ts
		}
		s.LastSeen = ts
	}
	return changed
}

func (s *Spool) addBucket(now time.Time, u Usage) {
	hour := now.UTC().Format("2006-01-02T15")
	for i := range s.Buckets {
		if s.Buckets[i].Hour == hour && s.Buckets[i].Model == s.Model {
			s.Buckets[i].Input += u.Input
			s.Buckets[i].Output += u.Output
			s.Buckets[i].Cache += u.CacheRead
			s.Buckets[i].Turns++
			return
		}
	}
	if len(s.Buckets) >= policy.MaxSeriesPerSession {
		return
	}
	s.Buckets = append(s.Buckets, Bucket{
		Hour: hour, Model: s.Model,
		Input: u.Input, Output: u.Output, Cache: u.CacheRead, Turns: 1,
	})
}

// projectOf 는 프로젝트 이름을 고른다. **basename 만** 쓴다 — 전체 경로는 저장하지 않는다.
func projectOf(d statusLineData) string {
	for _, p := range []string{d.Workspace.ProjectDir, d.Workspace.CurrentDir, d.CWD} {
		if p = strings.TrimSpace(p); p != "" {
			if b := filepath.Base(p); b != "" && b != "." && b != string(filepath.Separator) {
				return b
			}
		}
	}
	return ""
}

// ── 모델 정규화 ───────────────────────────────────────────────────────────────

// modelByDisplay 는 `agy models` 의 출력을 **그대로** 옮긴 표시명 → API 모델 id 표다.
//
// 왜 표가 필요한가 — 규칙으로는 못 맞춘다. 실제 출력이 이렇게 불규칙하다:
//
//	Gemini 3.6 Flash (Medium)   → gemini-3.6-flash-medium   (effort 접미사 있음)
//	Gemini 3.1 Pro (Low)        → gemini-3.1-pro-low        (medium 이 아예 없는 모델)
//	Claude Sonnet 4.6 (Thinking)→ claude-sonnet-4-6         (접미사 **없음**, 점→하이픈)
//	Claude Opus 4.6 (Thinking)  → claude-opus-4-6-thinking  (접미사 **있음**)
//	GPT-OSS 120B (Medium)       → gpt-oss-120b-medium
//
// 같은 `(Thinking)` 인데 Sonnet 은 접미사가 없고 Opus 는 있다. 어떤 규칙도 이 둘을
// 동시에 맞출 수 없으므로 아는 것은 표로 못박고, 모르는 것만 규칙으로 민다.
//
// **Antigravity 는 Gemini 전용이 아니다** — Claude·GPT-OSS 도 같은 CLI 에서 돈다.
// 그래서 platform=antigravity 인 세션의 model 이 `claude-*` 일 수 있다(정상이다).
var modelByDisplay = map[string]string{
	"gemini 3.6 flash (high)":      "gemini-3.6-flash-high",
	"gemini 3.6 flash (medium)":    "gemini-3.6-flash-medium",
	"gemini 3.6 flash (low)":       "gemini-3.6-flash-low",
	"gemini 3.5 flash (high)":      "gemini-3.5-flash-high",
	"gemini 3.5 flash (medium)":    "gemini-3.5-flash-medium",
	"gemini 3.5 flash (low)":       "gemini-3.5-flash-low",
	"gemini 3.1 pro (high)":        "gemini-3.1-pro-high",
	"gemini 3.1 pro (low)":         "gemini-3.1-pro-low",
	"claude sonnet 4.6 (thinking)": "claude-sonnet-4-6",
	"claude opus 4.6 (thinking)":   "claude-opus-4-6-thinking",
	"gpt-oss 120b (medium)":        "gpt-oss-120b-medium",
}

// ModelID 는 statusLine 이 준 표시명을 **API 모델 id 원문**으로 되돌린다.
//
// # 접미사를 자르지 않는다
//
// `gemini-3.6-flash-medium` 을 `gemini-3.6-flash` 로 줄여 보내지 **않는다.**
// 접미사 정규화는 단가 계산 한 곳에서만 한다 — 규칙이 두 벌이 되면 언젠가 한쪽만
// 고쳐지고, 그때 어느 쪽이 맞는지 아무도 말할 수 없게 된다. 게다가 원문을 버리면
// `claude-opus-4-6-thinking` 처럼 접미사가 **모델 변형**을 뜻하는 경우를 복구할 수 없고,
// 서버는 effort 를 영영 모르게 된다.
//
// # 왜 표시명에서 되돌려야 하나
//
// statusLine 은 API id 를 주지 않는다. `model.id` 조차 표시명이다(실측:
// `"id":"Gemini 3.6 Flash (Medium)"`). API id 를 그대로 주는 것은 훅의 `model_name`
// 뿐인데 훅에는 토큰이 없어 쓰지 않는다. 그래서 표로 되돌린다.
//
// 폴백(표에 없는 새 모델): 소문자로 내리고 → `(...)` 를 떼고 → 공백을 `-` 로 바꾸고
// → effort 가 있으면 `-<effort>` 를 붙인다. 표가 낡아도 그럴듯한 id 가 나오고,
// 틀리면 단가가 안 붙어 unpriced 로 **눈에 띈다**(조용히 틀린 값이 되지 않는다).
func ModelID(display, id, effort string) string {
	raw := strings.TrimSpace(display)
	if raw == "" {
		raw = strings.TrimSpace(id)
	}
	if raw == "" {
		return ""
	}
	// 공백이 없으면 이미 API id 다(훅의 model_name 형식) — 원문 그대로 쓴다.
	if !strings.Contains(raw, " ") {
		return strings.ToLower(raw)
	}
	key := strings.ToLower(strings.Join(strings.Fields(raw), " "))
	if m, ok := modelByDisplay[key]; ok {
		return m
	}
	base := key
	if i := strings.LastIndex(base, "("); i > 0 {
		base = strings.TrimSpace(base[:i])
	}
	out := strings.ReplaceAll(base, " ", "-")
	if e := strings.ToLower(strings.TrimSpace(effort)); e != "" {
		out += "-" + e
	}
	return out
}

// ── 스풀 파일 입출력 ──────────────────────────────────────────────────────────

// RecordStatusLine 은 statusLine 이 준 JSON 하나를 스풀에 반영한다.
//
// 이 함수는 **사용자의 상태줄 안에서** 돈다. 그래서 실패해도 조용히 지나가는 것이
// 원칙이다 — 사용량 한 건을 놓치는 것보다 상태줄이 깨지는 게 훨씬 나쁘다.
// 호출자(main)가 에러를 삼키고 항상 0 으로 끝내도록 되어 있다.
func RecordStatusLine(spoolDir string, r io.Reader, now time.Time) (Spool, bool, error) {
	var d statusLineData
	if err := json.NewDecoder(r).Decode(&d); err != nil {
		return Spool{}, false, fmt.Errorf("statusLine JSON 을 읽지 못했다: %w", err)
	}
	id := strings.TrimSpace(d.ConversationID)
	if id == "" {
		id = strings.TrimSpace(d.SessionID)
	}
	// 대화가 아직 시작되지 않은 렌더(인증 중·유휴)는 적을 것이 없다.
	// 파일명이 될 값이라 경로 문자가 섞이면 그대로 버린다.
	if id == "" || !policy.SessionIDRe.MatchString(id) {
		return Spool{}, false, nil
	}

	path := filepath.Join(spoolDir, id+".json")
	s, err := loadSpool(path)
	if err != nil {
		return Spool{}, false, err
	}
	if !s.Apply(d, now) {
		return s, false, nil
	}
	if err := saveSpool(path, s); err != nil {
		return s, false, err
	}
	return s, true, nil
}

func loadSpool(path string) (Spool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Spool{}, nil
		}
		return Spool{}, err
	}
	var s Spool
	if err := json.Unmarshal(b, &s); err != nil {
		// 깨진 스풀은 버리고 새로 시작한다. 여기서 멈추면 그 대화는 영영 못 센다.
		return Spool{}, nil
	}
	if s.Version != SpoolVersion {
		return Spool{}, nil
	}
	return s, nil
}

// saveSpool 은 임시 파일에 쓰고 rename 한다. 상태줄은 언제 죽을지 모르는 자리라
// 반쯤 쓰인 스풀이 남으면 그 대화의 사용량이 통째로 0 이 된다.
func saveSpool(path string, s Spool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "tmp-*.json")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ── history.jsonl (slash · keyword · project) ────────────────────────────────

// historyLine 은 `~/.gemini/antigravity-cli/history.jsonl` 의 한 줄이다.
// 실측 키: display, timestamp, workspace, conversationId, type.
type historyLine struct {
	Display        string `json:"display"`
	Workspace      string `json:"workspace"`
	ConversationID string `json:"conversationId"`
	Type           string `json:"type"`
}

// AddHistory 는 history.jsonl 을 읽어 slash·keyword·project 축을 채운다.
//
// **프롬프트 원문은 저장하지 않는다.** policy.Keywords 가 단어만 뽑고 policy.SafeKeyword
// 가 비밀번호·토큰·URL 처럼 보이는 것을 떨궈낸다 — claude·gemini 원천과 같은 필터다.
func (a *Aggregator) AddHistory(r io.Reader) error {
	dec := json.NewDecoder(r)
	for {
		var h historyLine
		if err := dec.Decode(&h); err != nil {
			if err == io.EOF {
				return nil
			}
			// 한 줄이 깨져도 나머지는 살린다.
			if _, ok := err.(*json.SyntaxError); ok {
				return nil
			}
			return err
		}
		id := strings.TrimSpace(h.ConversationID)
		if id == "" {
			continue
		}
		ax := a.hist[id]
		if ax == nil {
			ax = &histAxes{counters: map[string]map[string]int64{}}
			a.hist[id] = ax
		}
		if w := strings.TrimSpace(h.Workspace); w != "" && ax.project == "" {
			if b := filepath.Base(w); b != "" && b != "." {
				ax.project = b
			}
		}
		text := strings.TrimSpace(h.Display)
		if text == "" {
			continue
		}
		if h.Type == "slash_command" || strings.HasPrefix(text, "/") {
			if k := policy.NormKeyOf("slash", strings.TrimPrefix(text, "/")); k != "" {
				ax.add("slash", k)
			}
			continue
		}
		for _, w := range policy.Keywords(text) {
			ax.add("keyword", w)
		}
	}
}

func (h *histAxes) add(kind, key string) {
	if key == "" {
		return
	}
	m := h.counters[kind]
	if m == nil {
		m = map[string]int64{}
		h.counters[kind] = m
	}
	m[key]++
}
