// Package intake — 사용량 보고 인테이크. 클라이언트 페이로드를 저장 가능한 형태로 좁히는
// **순수** 계층이다. lib/intake.js 의 포팅.
//
// 왜 별도 패키지인가: 이 지점이 신뢰 경계다. 보고를 보내는 것은 팀원 PC 에서 도는 훅이고,
// 수집기는 팀원 PC 에 따로 배포되므로 서버보다 낡을 수 있다(구버전이 새 필드를 안 보내거나,
// 신버전이 아직 서버가 모르는 축을 보낸다). 그래서 라우트가 아니라 여기서, DB 를 켜지 않고
// 단위로 검증할 수 있는 순수 함수로 둔다.
//
// 규율:
//   - 모르는 축·빈 키·0 이하 카운트는 **조용히 버린다**(400 을 내지 않는다). 텔레메트리 실패가
//     팀원 세션 시작을 흔들면 안 되고, 훅은 어차피 응답을 안 본다.
//   - 원문 텍스트는 애초에 받지 않는다. keyword 축은 이미 토큰화된 것만 온다는 전제이고,
//     여기서 공백·길이·개수로 한 번 더 좁힌다(훅이 못 자른 것을 서버가 자른다).
//   - keyword 축은 추가로 **시크릿·PII 모양을 버린다**(SafeKeyword). 이 축만 사람이 입력한
//     말에서 나오므로 API 키·이메일·사번이 섞여 들어올 수 있는 유일한 자리다.
//   - 세션 하나가 보낼 수 있는 총량에 상한을 둔다 — 키워드 축은 사실상 무제한이라
//     상한이 없으면 세션 하나가 테이블을 채운다.
//
// 표준 라이브러리 말고 아무것도 import 하지 않는다(패키지 간 의존도 없다).
package intake

import (
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// 상한과 축 목록.
//
// CounterKinds·MaxSeriesPerSession·MaxCountersPerSession 은 현행 JS 에서 lib/store.js 가
// 갖고 있고 intake 가 require 해서 쓴다. 계약(go/CONTRACT.md)이 이 패키지에 **어떤 내부
// import 도 금지**하므로 여기에 다시 둔다 — store 쪽 값과 갈라지면 인테이크가 저장 계층이
// 받지 않는 행을 만들게 되므로, 둘 중 하나를 고칠 때는 반드시 함께 고친다.
const (
	KeyMax     = 120 // 축 공통 키 상한
	KeywordMax = 40  // keyword 축만 더 좁게 — 사람이 쓰는 낱말은 40자를 넘지 않는다
	PerKindMax = 80  // 축마다 상위 N개까지 — 롱테일은 화면에 안 쓴다

	MaxSessions           = 50
	MaxSeriesPerSession   = 200
	MaxCountersPerSession = 400
)

// CounterKinds 는 저장을 허용하는 축이다. 여기 없는 축은 조용히 버린다.
var CounterKinds = []string{"tool", "bash", "slash", "skill", "agent", "mcp", "keyword"}

var counterKindSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(CounterKinds))
	for _, k := range CounterKinds {
		m[k] = struct{}{}
	}
	return m
}()

var (
	sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{8,120}$`) // 트랜스크립트 파일명(uuid) 형태만
	hourRe      = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}$`)
)

// Counter 는 한 축의 키 하나와 그 횟수다.
type Counter struct {
	Kind  string
	Key   string
	Count int64
}

// Bucket 은 시간 × 모델 버킷이다.
type Bucket struct {
	Hour  string
	Model string

	Input       int64
	Output      int64
	CacheRead   int64
	CacheCreate int64
	CC5m        int64
	CC1h        int64

	Turns         int64
	ToolErrors    int64
	StopMaxTokens int64
	StopRefusal   int64
	LatencyMsSum  int64
	LatencyMsMax  int64
	LatencyTurns  int64
}

// Session 은 정규화된 세션 보고 하나다 — 세션 행과 그에 딸린 counters·series 를 함께 담는다.
//
// 문자열 필드가 포인터인 이유: "" 와 NULL 은 다른 사실이다. 저장 계층이 NULL 을 넣어야 할
// 자리에 빈 문자열을 넣으면 "보고하지 않았다"가 "빈 값을 보고했다"로 바뀐다.
type Session struct {
	SessionID string

	Username  *string
	Machine   *string
	StartedAt *string
	EndedAt   *string
	Project   *string
	Model     *string

	Input       int64
	Output      int64
	CacheRead   int64
	CacheCreate int64
	WebSearch   int64
	WebFetch    int64
	Turns       int64

	// NoTsTurns 는 시각이 없어 시계열(series)에 올릴 자리가 없던 턴 수다. **관측 축이다.**
	//
	// 왜 필요한가(2026-08-05): series 버킷은 시각 파싱이 성공할 때만 만들어진다. 세션 합계는
	// 시각이 필요 없어 정상이므로, 시각이 없으면 **합계는 맞는데 series 만 빈다** — 그 상태가
	// 어디에도 안 남아서 사용자별 series 커버리지가 2.2% 인 것을 DB 를 뒤져야 알았다.
	//
	// 구버전 수집기는 안 보낸다 → 0 이 아니라 **nil** 이다.
	// 0 과 nil 은 다른 사실이다: 0 은 "전 턴에 시각이 있었다", nil 은 "모른다".
	NoTsTurns *int64

	Counters []Counter
	Series   []Bucket
}

// Payload 는 보고 본문 하나에서 살아남은 세션들이다.
type Payload struct {
	Sessions []Session
}

// Ctx 는 페이로드 수준의 귀속 정보다. 세션이 자기 값을 갖고 있으면 그쪽이 이긴다.
type Ctx struct {
	Username string
	Machine  string
}

// ── keyword 축 전용 방어 ─────────────────────────────────────────────────────
//
// 이 축만 **사람이 입력한 말**에서 나온다. 나머지 축은 도구명·명령어·에이전트명이라 어휘가
// 닫혀 있지만, 여기는 열려 있다 — 그래서 프롬프트에 섞여 들어간 API 키·토큰·이메일·사번이
// 토큰화를 통과하면 그대로 상위 키워드가 되어 화면에 걸린다(토큰을 가진 전원이 본다).
//
// 훅이 클라이언트에서 한 번 거른다는 전제이지만 신뢰하지 않는다. 수집기는 팀원 PC 에서 도는
// 별도 프로세스라 서버보다 낡을 수 있고(구버전엔 이 필터가 아예 없다), 무엇보다 **한 번
// 저장되면 지우는 비용이 훨씬 크다.** 인테이크가 유일한 진입점이라 여기서 거르면 빠뜨릴
// 자리가 없다.
//
// 판정은 전부 "버리는" 방향으로만 작동한다 — 애매하면 남기지 않고 버린다. 키워드 하나가
// 집계에서 빠지는 손실은 0 에 가깝고, 시크릿 하나가 남는 손실은 되돌릴 수 없다.

// 벤더 자격증명 접두사. 이것들은 **정상 낱말이 될 수 없어서** 접두사만 맞으면 뒤를 안 본다.
//
// 여기에 `token`·`password` 같은 **라벨 낱말은 넣지 않는다.** 그건 값이 아니라 이름이라
// 버려도 얻는 보안이 없고, 하필 이 대시보드에서 'token' 은 정상 어휘다(토큰 사용량).
// 값이 붙은 형태(`token=abc`)는 assignRe 가 잡는다 — 그쪽이 정확한 신호다.
//
// 현행 정규식에 lookahead·backreference 가 없음을 확인했으므로 RE2 로 그대로 옮겨진다.
var secretPrefixRe = regexp.MustCompile(`(?i)^(?:` +
	`sk-|sk_live|sk_test|pk_live|rk_live|` +
	`ghp_|gho_|ghu_|ghs_|ghr_|github_pat_|glpat-|` +
	`xox[abprs]-|akia|asia|aiza|ya29\.|eyj|npm_|dop_v1_|shpat_|shpss_|hf_|` +
	`aws_secret|aws_access|private_key|-----begin` +
	`)`)

var (
	assignRe     = regexp.MustCompile(`[=:]`)                // token=abc · user:pass — 값이 붙은 모양
	hexRe        = regexp.MustCompile(`(?i)^[0-9a-f]{32,}$`) // md5·sha·랜덤 hex
	longDigitsRe = regexp.MustCompile(`\d{10,}`)             // 전화번호·카드번호·사번 형태
	urlishRe     = regexp.MustCompile(`://|@`)               // 이메일·접속문자열 조각
	randomCharRe = regexp.MustCompile(`^[A-Za-z0-9_+/=-]+$`)
)

// looksRandom 은 무작위로 보이는 문자열(키·해시의 일반형)을 잡는다. 길이 24 이상이면서
// 소문자·대문자·숫자가 모두 섞여 있으면 사람의 낱말이 아니다. 한글·공백이 섞인 토큰은 이
// 검사에 걸리지 않는다.
//
// ⚠ **대소문자 혼합은 정규화 전에만 보인다.** 정규화가 keyword 를 소문자로 접으므로,
// 정규화된 키만 보면 이 판정은 영원히 거짓이 된다(즉 벤더 접두사에 안 걸리는 임의 키가
// 그대로 통과한다). 그래서 SafeKeyword 가 **원본을 받는다.**
//
// 길이는 룬 단위로 센다. 바이트로 세면 한글 낱말이 3배로 부풀어 정상 어휘가 잘린다.
func looksRandom(k string) bool {
	if k == "" || utf8.RuneCountInString(k) < 24 || !randomCharRe.MatchString(k) {
		return false
	}
	var lower, upper, digit bool
	for _, r := range k {
		switch {
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= '0' && r <= '9':
			digit = true
		}
	}
	return lower && upper && digit
}

// SafeKeyword 는 원본 키워드를 받아 (정규화된 키, 남겨도 되는가) 를 돌려준다.
// false 면 그 키는 통째로 버린다(카운트도 남기지 않는다).
//
// **원본을 받는 것이 계약의 핵심이다.** 정규화가 소문자로 접기 전의 대소문자 정보가 여기서만
// 보이고, 그 정보가 없으면 벤더 접두사에 안 걸리는 임의 키가 조용히 통과한다.
func SafeKeyword(raw string) (string, bool) {
	k := NormKeyOf("keyword", raw)
	if k == "" || utf8.RuneCountInString(k) > KeywordMax {
		return "", false
	}
	if secretPrefixRe.MatchString(k) ||
		assignRe.MatchString(k) ||
		urlishRe.MatchString(k) ||
		longDigitsRe.MatchString(k) ||
		hexRe.MatchString(k) ||
		looksRandom(k) ||
		looksRandom(strings.TrimFunc(raw, isJSSpace)) {
		return "", false
	}
	return k, true
}

// NormKey 는 축을 모를 때의 일반 정규화다(계약 시그니처).
func NormKey(raw string) string { return NormKeyOf("", raw) }

// NormKeyOf 는 축별 키 정규화다. 축마다 다른 이유가 있다:
//
//	bash    — 경로가 붙어 오면(예: /usr/bin/git) basename 만 남긴다. 사람마다 PATH 가 달라
//	          같은 도구가 여러 키로 갈라지는 것을 막는다.
//	slash   — 선행 슬래시를 유지하되 인자는 이미 훅이 잘랐다고 보고 첫 토큰만 취한다.
//	keyword — 소문자·공백 제거. 수집기가 같은 규칙으로 토큰화한다고 전제하되 신뢰하지는 않는다.
//	tool    — 공백을 지우지 않는다(도구명에 공백이 있을 수 있다).
func NormKeyOf(kind, raw string) string {
	k := clip(raw, KeyMax)
	if k == "" {
		return ""
	}
	if kind == "bash" {
		if i := strings.LastIndexAny(k, `\/`); i >= 0 {
			k = k[i+1:]
		}
	}
	if kind == "slash" {
		k = firstToken(k)
	}
	if kind == "keyword" {
		k = strings.ToLower(k)
	}
	if kind != "tool" && strings.IndexFunc(k, isJSSpace) >= 0 {
		k = firstToken(k)
	}
	return clip(k, KeyMax)
}

// NormSession 은 세션 하나의 보고를 정규화한다. 두 번째 반환값이 false 면 그 세션은
// 통째로 건너뛴다(나머지 세션은 그대로 살린다).
//
// ctx 는 페이로드 수준의 귀속 정보로, 있으면 하나만 준다(계약 시그니처가 인자 하나이므로
// 가변 인자로 두었다).
func NormSession(raw map[string]any, ctx ...Ctx) (Session, bool) {
	if raw == nil {
		return Session{}, false
	}
	var c Ctx
	if len(ctx) > 0 {
		c = ctx[0]
	}

	sessionID := clip(firstTruthyString(raw["id"], raw["sessionId"]), 120)
	if !sessionIDRe.MatchString(sessionID) {
		return Session{}, false
	}

	s := Session{
		SessionID:   sessionID,
		Username:    nilIfEmpty(clip(firstTruthyString(raw["username"], c.Username), 200)),
		Machine:     nilIfEmpty(clip(firstTruthyString(raw["machine"], c.Machine), 200)),
		StartedAt:   nilIfEmpty(clip(jsString(raw["startedAt"]), 40)),
		EndedAt:     nilIfEmpty(clip(jsString(raw["endedAt"]), 40)),
		Project:     nilIfEmpty(clip(jsString(raw["project"]), 200)),
		Model:       nilIfEmpty(clip(jsString(raw["model"]), 120)),
		Input:       nat(raw["input"]),
		Output:      nat(raw["output"]),
		CacheRead:   nat(raw["cacheRead"]),
		CacheCreate: nat(raw["cacheCreate"]),
		WebSearch:   nat(raw["webSearch"]),
		WebFetch:    nat(raw["webFetch"]),
		Turns:       nat(raw["turns"]),
		Counters:    []Counter{},
		Series:      []Bucket{},
	}
	if v, present := raw["noTsTurns"]; present && v != nil {
		n := nat(v)
		s.NoTsTurns = &n
	}

	s.Series = normSeries(raw["series"])
	s.Counters = normCounters(raw["counters"])
	return s, true
}

// 시간 × 모델 버킷. **없으면 그냥 없는 것이다** — 400 을 내지 않는다.
//
// 이건 선택이 아니라 필수다. 수집기는 팀원 머신에 하루 1회 스로틀로 갱신되므로, 서버가 새
// 수집기를 발행한 뒤에도 며칠 동안 구버전과 신버전 보고가 **섞여서** 들어온다. 구버전을
// 거절하면 그 기간 동안 그 사람들의 사용량이 통째로 사라진다.
//
// hour 형식이 아닌 행은 조용히 버린다. 시간 라벨이 없는 버킷은 시계열에 올릴 자리가 없고,
// 형식이 깨진 값을 통과시키면 화면에 정체불명 칸이 생긴다.
func normSeries(v any) []Bucket {
	out := []Bucket{}
	arr, ok := v.([]any)
	if !ok {
		return out
	}
	seen := make(map[string]struct{}, len(arr))
	for _, item := range arr {
		b, ok := item.(map[string]any)
		if !ok {
			continue
		}
		hour := clip(jsString(b["hour"]), 13)
		if !hourRe.MatchString(hour) {
			continue
		}
		model := clip(jsString(b["model"]), 120)
		if model == "" {
			model = "(미상)"
		}
		key := hour + "|" + model
		if _, dup := seen[key]; dup {
			// 중복 키는 UPSERT 가 삼키지만 행 수를 정직하게 세려면 여기서 막는다.
			continue
		}
		seen[key] = struct{}{}

		out = append(out, Bucket{
			Hour: hour, Model: model,
			Input: nat(b["input"]), Output: nat(b["output"]),
			CacheRead: nat(b["cacheRead"]), CacheCreate: nat(b["cacheCreate"]),
			CC5m: nat(b["cc5m"]), CC1h: nat(b["cc1h"]),
			Turns: nat(b["turns"]), ToolErrors: nat(b["toolErrors"]),
			StopMaxTokens: nat(b["stopMaxTokens"]), StopRefusal: nat(b["stopRefusal"]),
			LatencyMsSum: nat(b["latencyMsSum"]), LatencyMsMax: nat(b["latencyMsMax"]),
			LatencyTurns: nat(b["latencyTurns"]),
		})
		if len(out) >= MaxSeriesPerSession {
			break
		}
	}
	return out
}

// counters 는 { kind: { key: count } } 또는 [{kind,key,count}] 둘 다 받는다.
// 수집기는 객체 형태가 짜기 쉬워 그쪽을 기본으로 두되, 배열도 거절하지 않는다(버전 드리프트 흡수).
func normCounters(v any) []Counter {
	rows := []Counter{}
	push := func(kind string, key any, count any) {
		if _, ok := counterKindSet[kind]; !ok {
			return
		}
		c := nat(count)
		if c <= 0 {
			return
		}
		rawKey := jsString(key)
		var k string
		if kind == "keyword" {
			// 사람이 입력한 말에서 나오는 축만 시크릿·PII 검사를 통과해야 한다.
			// **원본을 넘긴다** — 대소문자 혼합(키의 일반형)은 정규화 전에만 보인다.
			var keep bool
			if k, keep = SafeKeyword(rawKey); !keep {
				return
			}
		} else {
			k = NormKeyOf(kind, rawKey)
		}
		if k == "" {
			return
		}
		rows = append(rows, Counter{Kind: kind, Key: k, Count: c})
	}

	switch t := v.(type) {
	case []any:
		for _, item := range t {
			r, ok := item.(map[string]any)
			if !ok {
				continue
			}
			push(clip(jsString(r["kind"]), 40), r["key"], r["count"])
		}
	case map[string]any:
		// 축은 CounterKinds 순으로 돈다. Go 맵은 순회 순서가 무작위라, 그대로 돌면 총 상한
		// (MaxCountersPerSession)에 걸릴 때 어느 축이 잘리는지가 실행마다 달라진다.
		for _, kind := range CounterKinds {
			bucket, ok := t[kind].(map[string]any)
			if !ok {
				continue
			}
			// 축마다 상위 N개만 — 훅이 안 잘랐어도 서버에서 자른다.
			type kv struct {
				k string
				v int64
			}
			entries := make([]kv, 0, len(bucket))
			for k, raw := range bucket {
				if n := nat(raw); n > 0 {
					entries = append(entries, kv{k, n})
				}
			}
			// count 내림차순, 동점은 키 오름차순.
			// 동점 정렬을 키로 고정하는 이유: JS 는 객체의 삽입 순서로 갈리는데 Go 맵에는 그
			// 순서가 없다. 무작위로 두면 같은 입력이 실행마다 다른 상위 N을 남긴다.
			sort.Slice(entries, func(i, j int) bool {
				if entries[i].v != entries[j].v {
					return entries[i].v > entries[j].v
				}
				return entries[i].k < entries[j].k
			})
			if len(entries) > PerKindMax {
				entries = entries[:PerKindMax]
			}
			for _, e := range entries {
				push(kind, e.k, e.v)
			}
		}
	}

	if len(rows) > MaxCountersPerSession {
		rows = rows[:MaxCountersPerSession]
	}
	return rows
}

// NormPayload 는 보고 본문 전체를 세션 목록으로 좁힌다. 한 세션이 깨져도 나머지는 살린다.
//
// 상한(MaxSessions)은 훅이 오래 꺼져 있다가 한 번에 밀어 넣는 경우를 막는다 — 그 경우
// 오래된 것부터가 아니라 **최근 것부터** 살아야 하므로 호출부가 정렬해 보낸다는 전제는 두지
// 않고 여기서 자르기만 한다(훅은 최근 세션부터 담는다).
//
// error 는 **본문이 JSON 이 아닐 때만** 난다. 그 밖의 모양 오류(세션이 배열이 아니다, 필드가
// 이상하다)는 조용히 빈 결과다 — 텔레메트리 실패가 팀원 세션 시작을 흔들면 안 된다.
func NormPayload(raw []byte) (Payload, error) {
	out := Payload{Sessions: []Session{}}

	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return out, err
	}
	body, ok := root.(map[string]any)
	if !ok {
		return out, nil
	}

	ctx := Ctx{
		Username: clip(firstTruthyString(body["user"], body["username"]), 200),
		Machine:  clip(jsString(body["machine"]), 200),
	}

	arr, ok := body["sessions"].([]any)
	if !ok {
		return out, nil
	}
	if len(arr) > MaxSessions {
		arr = arr[:MaxSessions]
	}
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if s, ok := NormSession(m, ctx); ok {
			out.Sessions = append(out.Sessions, s)
		}
	}
	return out, nil
}

// ── JS 값 의미론 헬퍼 ────────────────────────────────────────────────────────

// clip 은 JS 의 `s(v, n)` 이다: 값을 문자열로 만들고 공백을 턴 뒤 n 글자로 자른다.
// 자르기는 룬 단위다 — 바이트로 자르면 깨진 UTF-8 이 나오고 한글 키가 1/3 길이로 잘린다.
func clip(v any, n int) string {
	t := strings.TrimFunc(jsString(v), isJSSpace)
	if utf8.RuneCountInString(t) <= n {
		return t
	}
	cnt := 0
	for i := range t {
		if cnt == n {
			return t[:i]
		}
		cnt++
	}
	return t
}

// nat 은 JS 의 `nat(v)` 이다: 내림한 뒤 유한하고 0 보다 큰 값만 인정한다.
func nat(v any) int64 {
	n, ok := jsNumber(v)
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0
	}
	f := math.Floor(n)
	if f <= 0 {
		return 0
	}
	// int64 범위를 넘는 값은 상한으로 접는다 — 오버플로로 음수가 되면 카운트가 뒤집힌다.
	if f > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(f)
}

// jsNumber 는 JS 의 Number() 중 **여기서 의미가 있는 것만** 옮긴다.
// 배열·맵은 숫자가 아니다(JS 의 Number([]) === 0 은 여기서 도움이 안 된다).
func jsNumber(v any) (float64, bool) {
	switch t := v.(type) {
	case nil:
		return 0, true // Number(null) === 0
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	case float64:
		return t, true
	case int:
		return float64(t), true
	case int32:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		n, err := t.Float64()
		return n, err == nil
	case string:
		s := strings.TrimFunc(t, isJSSpace)
		if s == "" {
			return 0, true // Number('') === 0
		}
		n, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

// jsString 은 JS 의 String() 중 페이로드에 실제로 올 수 있는 것만 옮긴다.
// null·undefined 는 빈 문자열이다(JS 의 `v == null ? ” : String(v)`).
func jsString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		// encoding/json 의 숫자 표기는 ECMAScript 와 같은 규칙이라 Node 의 String(n) 과 맞는다.
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		return string(b)
	case json.Number:
		return t.String()
	case int:
		return strconv.FormatInt(int64(t), 10)
	case int32:
		return strconv.FormatInt(int64(t), 10)
	case int64:
		return strconv.FormatInt(t, 10)
	default:
		return ""
	}
}

// firstTruthyString 은 JS 의 `a || b` 다 — 앞의 값이 falsy 면 뒤를 쓴다.
// 빈 문자열·0·false·null 이 falsy 다.
func firstTruthyString(vals ...any) string {
	for _, v := range vals {
		if !truthy(v) {
			continue
		}
		if s := jsString(v); s != "" {
			return s
		}
	}
	return ""
}

func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return t != ""
	case bool:
		return t
	case float64:
		return t != 0 && !math.IsNaN(t)
	case json.Number:
		return t.String() != "" && t.String() != "0"
	case int:
		return t != 0
	case int32:
		return t != 0
	case int64:
		return t != 0
	default:
		return v != nil
	}
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// firstToken 은 첫 공백 앞까지를 남긴다(JS 의 `split(/\s+/)[0]`).
func firstToken(s string) string {
	if i := strings.IndexFunc(s, isJSSpace); i >= 0 {
		return s[:i]
	}
	return s
}

// isJSSpace 는 JS 정규식의 `\s` 에 대응한다 — unicode.IsSpace 에 BOM 을 더한다.
// Go 의 `\s` 는 ASCII 다섯 개뿐이라 그대로 쓰면 전각 공백이 낀 키가 통째로 남는다.
func isJSSpace(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' }
