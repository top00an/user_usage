// Package antigravity 는 Google **Antigravity CLI**(`agy`) 의 사용량을 수집한다.
//
// 이 원천은 다른 셋(claude·codex·gemini)과 근본적으로 다르다. **디스크에 토큰이 없다.**
// 실측으로 확인한 사실만 적는다(추측 아님):
//
//	훅(SessionStart·PreInvocation·PostInvocation·Stop·PostToolUse)
//	  → usage 가 **없다.** 바이너리에 박힌 protobuf 디스크립터
//	    `third_party/jetski/hooks_pb/hooks.proto` 의 HookArgsCommon·StopHookArgs·
//	    PostInvocationHookArgs 어디에도 토큰 필드가 없고, 실제 stdin payload 도 그랬다.
//	    훅이 주는 것은 conversation_id·model_name·transcript_path·workspace_paths 뿐이다.
//	brain/<conv>/.system_generated/logs/transcript_full.jsonl
//	  → 토큰이 **없다.** 키는 step_index·source·type·status·created_at·content 뿐이다.
//	conversations/<uuid>.db
//	  → 토큰이 **없다**(protobuf BLOB 전수 스캔으로 확인됨).
//	statusLine
//	  → **여기에만 토큰이 있다.** `context_window.current_usage`.
//
// 그래서 이 패키지는 파일을 훑는 파서가 아니라 **스풀을 접는 집계기**다. 토큰을 보는 유일한
// 지점(statusLine)이 렌더될 때마다 스풀에 적고, 수집기는 그 스풀을 읽어 세션으로 만든다.
//
// # 왜 스풀이 필요한가
//
// statusLine 은 렌더마다 불린다(실측: 한 번의 `--print` 실행에 14회). 그리고 프로세스가
// 끝나면 그 값은 어디에도 남지 않는다. 붙잡아 두지 않으면 사라지는 값이라 스풀에 적는다.
//
// # 누적 규칙 — 여기가 이 패키지의 핵심이고, 실측으로 갈랐다
//
// 같은 대화를 두 턴 돌려 print 모드의 누적값과 대조했다:
//
//	              statusLine                         print(대화 누적)
//	turn1  current_usage.input=17283              usage.input=17380
//	turn2  current_usage.input=17506              usage.input=34886
//	       └ 17380 + 17506 = 34886 (정확히 일치)
//
// 즉 `current_usage` 는 **직전 invocation 하나**의 값이지 대화 누적이 아니다. 그래서
// **합산**해야 한다. 반대로 `context_window.total_output_tokens` 는 실측 2/2 로 print 의
// 대화 누적 output 과 **정확히 일치**했다(21, 40) — 이건 누적이라 그대로 쓴다.
//
// 그리고 `total_input_tokens` 는 누적이 아니다. 실측 2/2 로
// `used_percentage × context_window_size` 와 정확히 같았다(154, 25286) — 이건 과금된
// 입력이 아니라 **지금 컨텍스트에 들어 있는 양**이다. 그래서 입력만은 스냅샷 합산을 쓴다.
//
// # 알려진 정확도 한계 (위조하지 않고 그대로 둔다)
//
// 입력은 스냅샷 합산이라 **하한**이다. 실측에서 turn1 이 17283 로 남아 print 의 17380 보다
// 97 적었다(-0.6%). `current_usage` 는 "마지막 API 호출"만 보여주므로 한 invocation 안에서
// 일어난 보조 호출(제목 생성 등)이 빠진다. 출력은 total_output_tokens 가 그 보조 호출까지
// 잡아내므로(21 vs 스냅샷 17) 이 문제가 없다.
//
// 표준 라이브러리와 internal/{payload,policy} 말고 아무것도 import 하지 않는다.
package antigravity

import (
	"encoding/json"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tscorp/user-usage/collector/internal/payload"
	"github.com/tscorp/user-usage/collector/internal/policy"
)

// Platform 은 이 원천의 이름이다.
//
// `gemini` 를 재사용하지 않는 이유: 오픈소스 gemini-cli 와 **수집 가능한 축이 다르다.**
// 저쪽은 도구·MCP·LOC 를 주지만 Antigravity 는 토큰·모델·세션만 준다. 한 값으로 섞으면
// 대시보드의 "미수집 / 해당없음" 표기가 거짓이 된다 — 있는 것만 정직하게 싣는 것이
// 이 수집기의 규율이므로 값을 따로 쓴다.
//
// 서버 허용목록(store.Platforms)에 이 값이 아직 없으면 서버가 `other` 로 정규화한다.
// 그건 정상 폴백이고, 허용목록이 붙은 뒤 재전송하면 제자리를 찾는다(절대값 UPSERT 라 안전).
const Platform = "antigravity"

// Match 는 스풀 파일인지 판정한다. 스풀은 대화 하나당 `<conversation_id>.json` 이다.
//
// 원자적 쓰기의 임시 파일(`.tmp-*`)을 반드시 제외한다 — 반쯤 쓰인 파일을 읽으면
// 그 대화의 사용량이 통째로 0 이 된다(거부가 아니라 침묵이라 더 나쁘다).
func Match(path string) bool {
	base := filepath.Base(path)
	if strings.HasPrefix(base, ".") || strings.HasPrefix(base, "tmp-") {
		return false
	}
	return strings.HasSuffix(base, ".json")
}

// SessionKeyFromPath 는 파일 경로 → 세션 키다. 파일명 stem 이 곧 conversation_id 다.
func SessionKeyFromPath(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".json")
}

// Aggregator 는 스풀 파일들을 세션 단위로 접는다.
type Aggregator struct {
	convs map[string]*conv
	// hist 는 history.jsonl 에서 뽑은 대화별 축이다. 스풀에 없는 대화는 버린다
	// (사용량을 모르는 대화의 키워드만 올리면 분모 없는 분자가 된다).
	hist map[string]*histAxes
}

type conv struct {
	spool   Spool
	project string
}

type histAxes struct {
	counters map[string]map[string]int64
	project  string
}

// New 는 빈 집계기를 만든다.
func New() *Aggregator {
	return &Aggregator{convs: map[string]*conv{}, hist: map[string]*histAxes{}}
}

// AddFile 은 스풀 파일 하나를 읽는다. fallbackID 는 파일명에서 온 대화 id 다
// (스풀 안의 conversationId 가 비어 있을 때만 쓴다).
func (a *Aggregator) AddFile(fallbackID string, r io.Reader) error {
	var s Spool
	if err := json.NewDecoder(r).Decode(&s); err != nil {
		return err
	}
	id := strings.TrimSpace(s.ConversationID)
	if id == "" {
		id = fallbackID
	}
	if id == "" {
		return nil
	}
	c, ok := a.convs[id]
	if !ok {
		c = &conv{}
		a.convs[id] = c
	}
	// 같은 대화의 스풀이 두 번 들어오는 일은 없지만(파일 하나=대화 하나), 들어오면
	// 나중 것이 이긴다 — 절대값이라 합치면 부풀기 때문이다.
	c.spool = s
	c.project = strings.TrimSpace(s.Project)
	return nil
}

// AddFileAt 은 pathAggregator 확장이다. 스풀은 경로에서 얻을 정보가 없어 AddFile 과 같다.
func (a *Aggregator) AddFileAt(_, fallbackID string, r io.Reader) error {
	return a.AddFile(fallbackID, r)
}

// Sessions 는 접힌 세션 목록을 낸다.
func (a *Aggregator) Sessions() []payload.Session {
	ids := make([]string, 0, len(a.convs))
	for id := range a.convs {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	out := make([]payload.Session, 0, len(ids))
	for _, id := range ids {
		c := a.convs[id]
		s := c.spool

		in, outTok, cacheRead := s.Totals()

		// 신호가 없는 대화는 아예 싣지 않는다 — 다른 원천과 같은 규율이다.
		if s.Invocations == 0 && in == 0 && outTok == 0 && cacheRead == 0 {
			continue
		}
		if !policy.SessionIDRe.MatchString(id) {
			continue
		}

		project := c.project
		counters := map[string]map[string]int64{}
		if h := a.hist[id]; h != nil {
			if project == "" {
				project = h.project
			}
			counters = h.counters
		}

		noTs := int64(0)
		sess := payload.Session{
			ID:        id,
			StartedAt: s.FirstSeen,
			EndedAt:   s.LastSeen,
			Project:   project,
			Model:     s.Model,

			Input:     in,
			Output:    outTok,
			CacheRead: cacheRead,
			// CacheCreate 는 **항상 0 이다.** statusLine 에
			// `cache_creation_input_tokens` 가 오기는 하지만(실측값 0), Gemini 는
			// 암시적 캐싱이라 캐시 **생성** 과금이라는 개념이 없다. 관측된 값을
			// 과금 축에 실으면 없는 비용을 만들어내므로 싣지 않는다.
			// 관측값 자체는 스풀의 ObservedCacheCreate 에 남겨 둔다.
			CacheCreate: 0,

			Turns:     s.Invocations,
			NoTsTurns: &noTs,
			Counters:  capCounters(counters),
			Series:    s.sortedBuckets(),
		}
		out = append(out, sess)
	}
	return out
}

// capCounters 는 서버 normCounters 와 같은 규칙으로 축을 자른다(gemini 패키지와 동일).
// 빈 축은 **엔트리 자체를 만들지 않는다** — 빈 축은 "미지원"을, 0 은 "0으로 관측됨"을
// 뜻하므로 둘을 섞으면 지원표가 거짓이 된다.
func capCounters(in map[string]map[string]int64) map[string]map[string]int64 {
	out := map[string]map[string]int64{}
	total := 0
	for _, kind := range policy.CounterKinds {
		m := in[kind]
		if len(m) == 0 {
			continue
		}
		type kv struct {
			k string
			n int64
		}
		rows := make([]kv, 0, len(m))
		for k, n := range m {
			if n <= 0 || k == "" {
				continue
			}
			rows = append(rows, kv{k, n})
		}
		if len(rows) == 0 {
			continue
		}
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].n != rows[j].n {
				return rows[i].n > rows[j].n
			}
			return rows[i].k < rows[j].k
		})
		if len(rows) > policy.PerKindMax {
			rows = rows[:policy.PerKindMax]
		}
		dst := map[string]int64{}
		for _, r := range rows {
			if total >= policy.MaxCountersPerSession {
				break
			}
			dst[r.k] = r.n
			total++
		}
		if len(dst) > 0 {
			out[kind] = dst
		}
	}
	return out
}
