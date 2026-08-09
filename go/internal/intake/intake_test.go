package intake

// test/usage-keyword-safety.test.js 의 **모든** 케이스 + lib/intake.js 의 나머지 계약을
// 옮긴 테이블 테스트.
//
// 이 파일의 규율: **버리는 쪽으로만 실패해야 한다.** 평범한 낱말이 잘못 버려지면 집계가 조금
// 부정확해지지만, 시크릿이 통과하면 되돌릴 수 없다. 그래서 음성 대조(평범한 말은 남는가)를
// 양성 대조만큼 촘촘히 둔다 — 필터가 과하게 잡아 축이 통째로 비는 것도 조용한 고장이라서다.
//
// 기대값은 전부 현행 Node(lib/intake.js)를 실제로 돌려 받은 것이다.

import (
	"encoding/json"
	"strings"
	"testing"
)

const sid = "a1b2c3d4-0000-4000-8000-000000000001"

// keywords 는 counters.keyword 만 담은 최소 페이로드를 태워 살아남은 키를 돌려준다
// (JS 테스트의 helper 와 같은 자리).
func keywords(t *testing.T, bucket map[string]any) []string {
	t.Helper()
	s, ok := NormSession(map[string]any{
		"id":        sid,
		"startedAt": "2026-08-03T09:00:00.000Z",
		"counters":  map[string]any{"keyword": bucket},
	})
	if !ok {
		return nil
	}
	out := []string{}
	for _, r := range s.Counters {
		if r.Kind == "keyword" {
			out = append(out, r.Key)
		}
	}
	return out
}

// ── ①②③ 키워드 축 시크릿·PII 필터 ─────────────────────────────────────────
//
// 7개 축 중 keyword 만 **사람이 입력한 말**에서 나온다. 나머지는 도구명·명령어·에이전트명이라
// 어휘가 닫혀 있다. 프롬프트에 섞인 API 키·이메일·사번이 집계로 들어갈 수 있는 자리는 여기
// 하나뿐이고, 한 번 저장되면 토큰을 가진 전원이 상위 키워드 화면에서 본다.

func TestSafeKeyword_Table(t *testing.T) {
	cases := []struct {
		in   string
		keep string // "" 이면 버려야 한다
		why  string
	}{
		// ① 버려야 하는 것 — 시크릿 모양
		{"sk-ant-api03-abcdefghijklmnop", "", "Anthropic 키 접두사"},
		{"ghp_abcdefghijklmnopqrstuvwxyz0123", "", "GitHub PAT"},
		{"github_pat_11ABCDEFG0abcdefghijkl", "", "GitHub 세분화 PAT"},
		{"AKIAIOSFODNN7EXAMPLE", "", "AWS 액세스 키"},
		{"xoxb-123456789012-abcdefgh", "", "Slack 봇 토큰"},
		{"glpat-abcdefghijklmnopqrst", "", "GitLab PAT"},
		{"eyJhbGciOiJIUzI1NiIsInR5cCI6", "", "JWT 헤더 조각"},
		{"npm_abcdefghijklmnopqrstuvwxyz01", "", "npm 토큰"},
		{"token=abcdef123456", "", "값이 붙은 라벨"},
		{"user:hunter2", "", "콜론으로 붙은 값"},
		{"d41d8cd98f00b204e9800998ecf8427e", "", "32자 이상 hex(해시·랜덤 키)"},
		// 벤더 접두사에 걸리지 않는 임의 키. normKey 가 소문자로 접기 **전**을 봐야 잡힌다 —
		// 이 검사가 정규화된 값만 보면 영원히 통과시킨다(회귀 시 조용히 뚫린다).
		{"aB3dEfGh1jKlMn0pQrStUvWx9z", "", "대소문자+숫자가 섞인 24자 이상(키의 일반형)"},
		{"refactoring-plan-" + strings.Repeat("z", 30), "", "40자를 넘으면 무엇이든 버린다"},

		// 나머지 벤더 접두사 — SECRET_PREFIX_RE 를 한 갈래씩 다 밟는다.
		{"sk_live_abc", "", "Stripe live"},
		{"sk_test_abc", "", "Stripe test"},
		{"pk_live_abc", "", "Stripe publishable"},
		{"rk_live_abc", "", "Stripe restricted"},
		{"gho_x", "", "GitHub OAuth"},
		{"ghu_x", "", "GitHub user-to-server"},
		{"ghs_x", "", "GitHub server-to-server"},
		{"ghr_x", "", "GitHub refresh"},
		{"xoxa-1", "", "Slack app"},
		{"xoxp-1", "", "Slack user"},
		{"xoxr-1", "", "Slack refresh"},
		{"xoxs-1", "", "Slack session"},
		{"asia123", "", "AWS 임시 키"},
		{"aiza123", "", "Google API 키"},
		{"ya29.abc", "", "Google OAuth"},
		{"npm_x", "", "npm"},
		{"dop_v1_x", "", "DigitalOcean"},
		{"shpat_x", "", "Shopify private app"},
		{"shpss_x", "", "Shopify shared secret"},
		{"hf_x", "", "HuggingFace"},
		{"aws_secret_key", "", "AWS 시크릿 라벨"},
		{"aws_access_key", "", "AWS 액세스 라벨"},
		{"private_key_pem", "", "PEM"},
		{"-----begin", "", "PEM 헤더"},
		{"SK-ANT-UPPER", "", "접두사 판정은 대소문자를 가리지 않는다"},

		// ② 버려야 하는 것 — PII 모양
		{"someone@example.com", "", "이메일"},
		{"postgres://u:p@host/db", "", "접속 문자열 조각"},
		{"01012345678", "", "10자리 이상 연속 숫자(전화)"},
		{"emp-2026001234567", "", "10자리 이상 연속 숫자(사번)"},
		{"1234567890", "", "정확히 10자리"},
		{"abcdef0123456789abcdef0123456789", "", "32자 hex(소문자)"},
		{"ABCDEF0123456789ABCDEF0123456789", "", "32자 hex(대문자) — 정규화 후 hex 가 된다"},
		{strings.Repeat("a", 40), "", "40자 전부 hex 문자면 hex 로 걸린다"},
		{strings.Repeat("a", 41), "", "41자는 길이 상한에 먼저 걸린다"},
		{strings.Repeat("x", 24) + "Y1", "", "24자 이상 + 대소문자 + 숫자"},

		// ③ 남아야 하는 것 — 평범한 말
		// 이쪽이 무너지면 축이 조용히 빈다. 실제로 쓰이는 어휘를 표본으로 둔다.
		{"리팩터", "리팩터", ""},
		{"배포", "배포", ""},
		{"테스트코드", "테스트코드", ""},
		{"refactor", "refactor", ""},
		{"deploy", "deploy", ""},
		{"test", "test", ""},
		{"usage-dashboard", "usage-dashboard", ""},
		{"postgres", "postgres", ""},
		{"sqlite", "sqlite", ""},
		{"node22", "node22", ""},
		{"v2", "v2", ""},
		{"ci", "ci", ""},
		{"rls", "rls", ""},
		{"마이그레이션", "마이그레이션", ""},
		{"2026", "2026", "짧은 숫자는 남는다 — 10자리 미만은 식별자가 아니다"},
		{"123456789", "123456789", "9자리는 남는다"},
		{"refactoring-plan-" + strings.Repeat("z", 23), "refactoring-plan-" + strings.Repeat("z", 23), "정확히 40자 — 상한은 초과부터 버린다"},
		// 라벨 낱말은 값이 아니다 — 버려도 얻는 보안이 없고, 이 대시보드에서는 정상 어휘다.
		{"token", "token", "라벨 낱말 자체는 남는다"},
		{"password", "password", "라벨 낱말 자체는 남는다"},
		{"사용량관측대시보드", "사용량관측대시보드", "한글은 길이가 길어도 무작위 판정에 걸리지 않는다"},
		{"ab3defgh1jklmn0pqrstuvwx9z", "ab3defgh1jklmn0pqrstuvwx9z", "대문자가 없으면 무작위 판정에 걸리지 않는다"},
		{"a1B2c3", "a1b2c3", "24자 미만이면 대소문자+숫자여도 남는다"},
		{"deploy-2026", "deploy-2026", ""},
		{"v1.2.3", "v1.2.3", ""},
		{"DEPLOY", "deploy", "정규화가 소문자로 접는다"},
		{"ReFactor", "refactor", "정규화가 소문자로 접는다"},
		{"  spaced  ", "spaced", "정규화가 공백을 턴다"},
		{"two words", "two", "공백이 있으면 첫 토큰만"},
		{"tab\tsep", "tab", "탭도 공백이다"},
	}

	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			// SafeKeyword 를 단독으로도 쓸 수 있어야 한다(수집기 쪽과 규칙을 맞출 때 참조점).
			got, keep := SafeKeyword(c.in)
			if c.keep == "" {
				if keep {
					t.Fatalf("%q 가 %q 로 살아남았다 — %s", c.in, got, c.why)
				}
			} else {
				if !keep {
					t.Fatalf("%q 를 버렸다 — %s", c.in, c.why)
				}
				if got != c.keep {
					t.Fatalf("SafeKeyword(%q) = %q, want %q", c.in, got, c.keep)
				}
			}

			// 인테이크 전체를 태워도 같은 결과여야 한다(경로가 갈리면 그 틈이 구멍이다).
			via := keywords(t, map[string]any{c.in: 5})
			if c.keep == "" {
				if len(via) != 0 {
					t.Fatalf("%q 가 집계에 남았다: %v", c.in, via)
				}
			} else if len(via) != 1 || via[0] != c.keep {
				t.Fatalf("인테이크 경로 결과 = %v, want [%q]", via, c.keep)
			}
		})
	}
}

func TestSafeKeyword_EmptyIsDropped(t *testing.T) {
	for _, in := range []string{"", "   ", "\t"} {
		if _, keep := SafeKeyword(in); keep {
			t.Fatalf("빈 키워드(%q)를 받아들였다", in)
		}
	}
}

func TestKeywordMaxIsTighterThanKeyMax(t *testing.T) {
	// 키워드 상한이 일반 키 상한과 같으면 좁힌 의미가 없다.
	// 사람이 쓰는 낱말은 40자를 넘지 않는다 — 넘는 것은 사실상 식별자·해시·키다.
	if KeywordMax >= KeyMax {
		t.Fatalf("KeywordMax(%d) >= KeyMax(%d)", KeywordMax, KeyMax)
	}
}

func TestOtherAxesAreNotFiltered(t *testing.T) {
	// ④ bash·tool 축은 어휘가 닫혀 있어 필터가 필요 없고, 걸면 정상 도구명을 잡는다
	// (예: 'password' 라는 이름의 CLI 나 긴 MCP 서버명).
	s, ok := NormSession(map[string]any{
		"id": sid,
		"counters": map[string]any{
			"bash": map[string]any{"password": 2},
			"tool": map[string]any{"Bash": 1},
		},
	})
	if !ok {
		t.Fatal("세션이 거절됐다")
	}
	want := map[string]Counter{
		"bash": {Kind: "bash", Key: "password", Count: 2},
		"tool": {Kind: "tool", Key: "Bash", Count: 1},
	}
	got := map[string]Counter{}
	for _, r := range s.Counters {
		got[r.Kind] = r
	}
	for kind, w := range want {
		if got[kind] != w {
			t.Fatalf("%s 축 = %+v, want %+v", kind, got[kind], w)
		}
	}
	// 시크릿 필터가 keyword 축 밖으로 새면 정상 도구명이 사라진다.
	if len(s.Counters) != 2 {
		t.Fatalf("counters = %+v", s.Counters)
	}
}

// ── 키 정규화 ────────────────────────────────────────────────────────────────

func TestNormKeyOf(t *testing.T) {
	cases := []struct{ kind, in, want string }{
		// bash — 경로가 붙어 오면 basename 만. 사람마다 PATH 가 달라 같은 도구가 여러 키로
		// 갈라지는 것을 막는다.
		{"bash", "/usr/bin/git", "git"},
		{"bash", `C:\tools\node.exe`, "node.exe"},
		{"bash", "git", "git"},
		// slash — 선행 슬래시를 유지하되 첫 토큰만.
		{"slash", "/plan do a thing", "/plan"},
		// keyword — 소문자.
		{"keyword", "ReFactor", "refactor"},
		// tool 은 공백을 지우지 않는다(도구명에 공백이 있을 수 있다).
		{"tool", "Bash tool", "Bash tool"},
		{"mcp", "a b", "a"},
		{"", "  x  ", "x"},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := NormKeyOf(c.kind, c.in); got != c.want {
			t.Fatalf("NormKeyOf(%q, %q) = %q, want %q", c.kind, c.in, got, c.want)
		}
	}
	// 계약 시그니처(kind 없는 형태)는 일반 정규화다.
	if got := NormKey("  two words  "); got != "two" {
		t.Fatalf("NormKey = %q, want two", got)
	}
	// KeyMax 초과는 자른다.
	if got := NormKeyOf("tool", strings.Repeat("a", 200)); len([]rune(got)) != KeyMax {
		t.Fatalf("KeyMax 로 자르지 않았다: %d자", len([]rune(got)))
	}
}

// ── 세션 정규화 ──────────────────────────────────────────────────────────────

func TestNormSession_Shape(t *testing.T) {
	s, ok := NormSession(map[string]any{
		"id":          sid,
		"startedAt":   "2026-08-03T09:00:00.000Z",
		"endedAt":     "2026-08-03T10:00:00.000Z",
		"project":     "proj",
		"model":       "claude-opus-5",
		"input":       "10", // 숫자 문자열도 받는다
		"output":      20.9, // 내림
		"cacheRead":   -5,   // 음수는 0
		"cacheCreate": nil,  // null 은 0
		"webSearch":   1, "webFetch": 2, "turns": 3,
	}, Ctx{Username: "user-a", Machine: "pc-1"})
	if !ok {
		t.Fatal("세션이 거절됐다")
	}
	if s.SessionID != sid {
		t.Fatalf("sessionId = %q", s.SessionID)
	}
	if deref(s.Username) != "user-a" || deref(s.Machine) != "pc-1" {
		t.Fatalf("ctx 귀속이 안 붙었다: %v %v", s.Username, s.Machine)
	}
	if s.Input != 10 || s.Output != 20 || s.CacheRead != 0 || s.CacheCreate != 0 {
		t.Fatalf("토큰 정규화가 틀렸다: %+v", s)
	}
	if s.WebSearch != 1 || s.WebFetch != 2 || s.Turns != 3 {
		t.Fatalf("카운트가 틀렸다: %+v", s)
	}
	if deref(s.Project) != "proj" || deref(s.Model) != "claude-opus-5" {
		t.Fatalf("문자열 필드가 틀렸다: %+v", s)
	}
}

func TestNormSession_RawWinsOverCtx(t *testing.T) {
	s, _ := NormSession(map[string]any{
		"id": sid, "username": "explicit", "machine": "explicit-pc",
	}, Ctx{Username: "ctx", Machine: "ctx-pc"})
	if deref(s.Username) != "explicit" || deref(s.Machine) != "explicit-pc" {
		t.Fatalf("세션 값이 ctx 에 밀렸다: %v %v", s.Username, s.Machine)
	}
	// 빈 문자열은 falsy 라 ctx 로 떨어진다(JS 의 `||`).
	s2, _ := NormSession(map[string]any{"id": sid, "username": ""}, Ctx{Username: "ctx"})
	if deref(s2.Username) != "ctx" {
		t.Fatalf("빈 username 이 ctx 로 떨어지지 않았다: %v", s2.Username)
	}
	// ctx 도 없으면 null 이다(빈 문자열이 아니라).
	s3, _ := NormSession(map[string]any{"id": sid})
	if s3.Username != nil || s3.Machine != nil || s3.StartedAt != nil {
		t.Fatalf("없는 값이 null 이 아니다: %+v", s3)
	}
}

func TestNormSession_NoTsTurnsZeroIsNotNull(t *testing.T) {
	// 0 과 NULL 은 다른 사실이다: 0 은 "전 턴에 시각이 있었다", NULL 은 "모른다".
	// 구버전 수집기는 이 축을 안 보낸다 → 0 이 아니라 NULL 로 남아야 한다.
	s, _ := NormSession(map[string]any{"id": sid, "noTsTurns": 0})
	if s.NoTsTurns == nil || *s.NoTsTurns != 0 {
		t.Fatalf("noTsTurns=0 이 null 이 됐다: %v", s.NoTsTurns)
	}
	s2, _ := NormSession(map[string]any{"id": sid})
	if s2.NoTsTurns != nil {
		t.Fatalf("noTsTurns 미전송이 0 이 됐다: %v", *s2.NoTsTurns)
	}
	s3, _ := NormSession(map[string]any{"id": sid, "noTsTurns": nil})
	if s3.NoTsTurns != nil {
		t.Fatalf("noTsTurns=null 이 0 이 됐다: %v", *s3.NoTsTurns)
	}
}

func TestNormSession_SessionIDGate(t *testing.T) {
	// 트랜스크립트 파일명(uuid) 형태만 받는다.
	bad := []any{"short", "", nil, "has space", "has/slash", "has@at", 1234567}
	for _, id := range bad {
		if _, ok := NormSession(map[string]any{"id": id}); ok {
			t.Fatalf("잘못된 세션 id(%v)를 받아들였다", id)
		}
	}
	good := []string{"abcdefgh", strings.Repeat("a", 120), "a1b2c3d4-0000-4000-8000-000000000001", "a.b_c-d1"}
	for _, id := range good {
		if _, ok := NormSession(map[string]any{"id": id}); !ok {
			t.Fatalf("정상 세션 id(%q)를 거절했다", id)
		}
	}
	// 길이 상한은 **거절이 아니라 절단**이다 — 정규식 검사 전에 120자로 자른다.
	// (현행 Node 도 같다: 121자를 넣으면 120자 id 로 저장된다.)
	if s, ok := NormSession(map[string]any{"id": strings.Repeat("a", 121)}); !ok || len(s.SessionID) != 120 {
		t.Fatalf("121자 id 처리가 Node 와 다르다: ok=%v len=%d", ok, len(s.SessionID))
	}
	// 숫자로 온 id 도 문자열로 읽는다(수집기 버전 드리프트 흡수).
	if s, ok := NormSession(map[string]any{"id": 12345678}); !ok || s.SessionID != "12345678" {
		t.Fatalf("숫자 id 처리가 Node 와 다르다: ok=%v id=%q", ok, s.SessionID)
	}
	// sessionId 로도 받는다(id 가 비면 폴백).
	if _, ok := NormSession(map[string]any{"sessionId": sid}); !ok {
		t.Fatal("sessionId 키를 못 읽었다")
	}
	// 객체가 아니면 거절.
	if _, ok := NormSession(nil); ok {
		t.Fatal("nil 을 받아들였다")
	}
}

func TestNormSession_Series(t *testing.T) {
	s, ok := NormSession(map[string]any{
		"id": sid,
		"series": []any{
			map[string]any{
				"hour": "2026-08-03T09", "model": "claude-opus-5", "input": 1,
				"cc5m": 2, "cc1h": 3, "turns": 4, "toolErrors": 5, "stopMaxTokens": 6,
				"stopRefusal": 7, "latencyMsSum": 8, "latencyMsMax": 9, "latencyTurns": 10,
			},
			// 같은 hour|model 은 중복 — UPSERT 가 삼키지만 행 수를 정직하게 세려면 여기서 막는다.
			map[string]any{"hour": "2026-08-03T09", "model": "claude-opus-5", "input": 99},
			// 시간 형식이 아닌 행은 조용히 버린다 — 시계열에 올릴 자리가 없다.
			map[string]any{"hour": "bad", "model": "m"},
			// 모델이 없으면 '(미상)'.
			map[string]any{"hour": "2026-08-03T10"},
			nil,
			"not-an-object",
		},
	})
	if !ok {
		t.Fatal("세션이 거절됐다")
	}
	if len(s.Series) != 2 {
		t.Fatalf("series = %+v, want 2행", s.Series)
	}
	want0 := Bucket{
		Hour: "2026-08-03T09", Model: "claude-opus-5", Input: 1,
		CC5m: 2, CC1h: 3, Turns: 4, ToolErrors: 5, StopMaxTokens: 6,
		StopRefusal: 7, LatencyMsSum: 8, LatencyMsMax: 9, LatencyTurns: 10,
	}
	if s.Series[0] != want0 {
		t.Fatalf("series[0] = %+v, want %+v", s.Series[0], want0)
	}
	if s.Series[1] != (Bucket{Hour: "2026-08-03T10", Model: "(미상)"}) {
		t.Fatalf("series[1] = %+v", s.Series[1])
	}
}

func TestNormSession_SeriesCap(t *testing.T) {
	rows := make([]any, 0, MaxSeriesPerSession+50)
	for i := 0; i < MaxSeriesPerSession+50; i++ {
		rows = append(rows, map[string]any{"hour": "2026-08-03T09", "model": "m" + itoa(i)})
	}
	s, _ := NormSession(map[string]any{"id": sid, "series": rows})
	if len(s.Series) != MaxSeriesPerSession {
		t.Fatalf("series 상한이 안 걸렸다: %d", len(s.Series))
	}
}

func TestNormSession_CountersObjectAndArray(t *testing.T) {
	// counters 는 { kind: { key: count } } 또는 [{kind,key,count}] 둘 다 받는다
	// (수집기 버전 드리프트 흡수).
	obj, _ := NormSession(map[string]any{
		"id": sid,
		"counters": map[string]any{
			"tool": map[string]any{"Bash": 3, "Read": 0, "Write": -1}, // 0 이하는 버린다
			"bash": map[string]any{"/usr/bin/git": 2},
			"nope": map[string]any{"x": 1}, // 모르는 축은 조용히 버린다
		},
	})
	if len(obj.Counters) != 2 {
		t.Fatalf("counters = %+v, want 2행", obj.Counters)
	}

	arr, _ := NormSession(map[string]any{
		"id": sid,
		"counters": []any{
			map[string]any{"kind": "tool", "key": "Bash", "count": 2},
			map[string]any{"kind": "bogus", "key": "x", "count": 1},
			map[string]any{"kind": "tool", "key": "", "count": 1}, // 빈 키는 버린다
			nil,
		},
	})
	if len(arr.Counters) != 1 || arr.Counters[0] != (Counter{Kind: "tool", Key: "Bash", Count: 2}) {
		t.Fatalf("배열 counters = %+v", arr.Counters)
	}
}

func TestNormSession_PerKindTopN(t *testing.T) {
	// 축마다 상위 N개까지 — 훅이 안 잘랐어도 서버에서 자른다. 롱테일은 화면에 안 쓴다.
	bucket := map[string]any{}
	for i := 0; i < PerKindMax+40; i++ {
		bucket["tool"+itoa(i)] = i + 1
	}
	s, _ := NormSession(map[string]any{"id": sid, "counters": map[string]any{"tool": bucket}})
	if len(s.Counters) != PerKindMax {
		t.Fatalf("축별 상한이 안 걸렸다: %d", len(s.Counters))
	}
	// 살아남은 것은 상위 N개여야 한다(가장 큰 count 부터).
	if s.Counters[0].Count != int64(PerKindMax+40) {
		t.Fatalf("가장 큰 카운트가 살아남지 않았다: %+v", s.Counters[0])
	}
	for i := 1; i < len(s.Counters); i++ {
		if s.Counters[i-1].Count < s.Counters[i].Count {
			t.Fatalf("카운트 내림차순이 아니다: %d 번째", i)
		}
	}
}

func TestNormSession_TotalCounterCap(t *testing.T) {
	// 키워드 축은 사실상 무제한이라 상한이 없으면 세션 하나가 테이블을 채운다.
	counters := map[string]any{}
	for _, kind := range CounterKinds {
		bucket := map[string]any{}
		for i := 0; i < PerKindMax; i++ {
			bucket[kind+"key"+itoa(i)] = i + 1
		}
		counters[kind] = bucket
	}
	s, _ := NormSession(map[string]any{"id": sid, "counters": counters})
	if len(s.Counters) > MaxCountersPerSession {
		t.Fatalf("세션 총 counter 상한이 안 걸렸다: %d > %d", len(s.Counters), MaxCountersPerSession)
	}
}

// ── 페이로드 ─────────────────────────────────────────────────────────────────

func TestNormPayload(t *testing.T) {
	body := `{"machine":"pc-1","user":"user-a","sessions":[
	  {"id":"` + sid + `","turns":3},
	  {"id":"short"},
	  {"id":"b1b2c3d4-0000-4000-8000-000000000002","turns":1}
	]}`
	p, err := NormPayload([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	// 한 세션이 깨져도 나머지는 살린다.
	if len(p.Sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(p.Sessions))
	}
	if deref(p.Sessions[0].Username) != "user-a" || deref(p.Sessions[0].Machine) != "pc-1" {
		t.Fatalf("페이로드 ctx 가 안 내려왔다: %+v", p.Sessions[0])
	}
}

func TestNormPayload_UsernameFallback(t *testing.T) {
	// `user` 가 없으면 `username` 을 본다.
	p, err := NormPayload([]byte(`{"username":"u2","sessions":[{"id":"` + sid + `"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Sessions) != 1 || deref(p.Sessions[0].Username) != "u2" {
		t.Fatalf("username 폴백 실패: %+v", p.Sessions)
	}
}

func TestNormPayload_SessionCap(t *testing.T) {
	// 훅이 오래 꺼져 있다가 한 번에 밀어 넣는 경우를 막는다.
	var sb strings.Builder
	sb.WriteString(`{"sessions":[`)
	for i := 0; i < MaxSessions+20; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"id":"sess-id-`)
		sb.WriteString(itoa(i))
		sb.WriteString(`0000000"}`)
	}
	sb.WriteString(`]}`)
	p, err := NormPayload([]byte(sb.String()))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Sessions) != MaxSessions {
		t.Fatalf("세션 상한이 안 걸렸다: %d", len(p.Sessions))
	}
}

func TestNormPayload_GarbageIsNotAPanic(t *testing.T) {
	// 텔레메트리 실패가 팀원 세션 시작을 흔들면 안 된다 — 모르는 모양은 조용히 빈 결과다.
	// 깨진 JSON 만 error 로 알린다(호출부가 400 을 낼지 200 을 낼지 정한다).
	for _, body := range []string{`null`, `[]`, `"x"`, `{}`, `{"sessions":null}`, `{"sessions":"x"}`, `{"sessions":[null,1,"x"]}`} {
		p, err := NormPayload([]byte(body))
		if err != nil {
			t.Fatalf("%s 에서 error: %v", body, err)
		}
		if len(p.Sessions) != 0 {
			t.Fatalf("%s 에서 세션이 나왔다: %+v", body, p.Sessions)
		}
	}
	if _, err := NormPayload([]byte(`{`)); err == nil {
		t.Fatal("깨진 JSON 을 조용히 삼켰다")
	}
	// Sessions 는 nil 이 아니라 빈 슬라이스여야 한다 — JSON 에서 null 이 되면 분기가 갈린다.
	p, _ := NormPayload([]byte(`{}`))
	b, _ := json.Marshal(p.Sessions)
	if string(b) != "[]" {
		t.Fatalf("Sessions JSON = %s, want []", b)
	}
}

func TestCounterKindsMatchesContract(t *testing.T) {
	want := []string{"tool", "bash", "slash", "skill", "agent", "mcp", "keyword"}
	if len(CounterKinds) != len(want) {
		t.Fatalf("CounterKinds = %v", CounterKinds)
	}
	for i := range want {
		if CounterKinds[i] != want[i] {
			t.Fatalf("CounterKinds = %v, want %v", CounterKinds, want)
		}
	}
}

func deref(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
