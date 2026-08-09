// Package policy — 전송 전에 값을 좁히는 **순수** 계층이다.
//
// 이 패키지가 데이터 정책의 클라이언트 측 집행 지점이다. 서버(`go/internal/intake`)도 같은
// 검사를 하지만, 여기서 한 번 더 하는 이유는 두 가지다:
//
//  1. **네트워크에 나가지 않은 값은 되돌릴 수 있다.** 서버가 걸러도 그 값은 이미 팀원 PC 를
//     떠나 TLS 종단·프록시 로그·서버 접근 로그를 지나간 뒤다. 유일하게 새지 않는 값은
//     애초에 보내지 않은 값이다.
//  2. 서버가 우리보다 낡을 수 있다. 서버는 수집기가 낡을 것을 걱정하고, 수집기는 서버가
//     낡을 것을 걱정한다 — 양쪽이 각자 거르면 어느 쪽이 낡아도 시크릿은 안 나간다.
//
// 판정은 전부 **버리는 방향으로만** 작동한다. 키워드 하나가 집계에서 빠지는 손실은 0 에
// 가깝고, 시크릿 하나가 저장되는 손실은 되돌릴 수 없다.
//
// 표준 라이브러리 말고 아무것도 import 하지 않는다.
package policy

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// 상한과 축 목록 — `go/internal/intake/intake.go` 의 값과 **같아야 한다.**
//
// 별도 모듈이라 서버의 internal 패키지를 import 할 수 없어(Go 가 모듈 밖 internal 을 막는다)
// 여기 다시 적는다. 갈라지면 수집기가 서버가 버릴 행을 만들어 보내게 되므로,
// drift_test.go 가 서버 소스를 직접 읽어 값이 갈라졌는지 감시한다.
const (
	KeyMax     = 120
	KeywordMax = 40
	PerKindMax = 80

	MaxSessions           = 50
	MaxSeriesPerSession   = 200
	MaxCountersPerSession = 400
)

// CounterKinds 는 서버가 저장을 허용하는 축이다.
var CounterKinds = []string{"tool", "bash", "slash", "skill", "agent", "mcp", "keyword"}

// SessionIDRe 는 서버가 세션 id 에 요구하는 모양이다. 트랜스크립트 파일명(uuid)이 이걸 만족한다.
var SessionIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{8,120}$`)

// ── 시크릿·PII 모양 ──────────────────────────────────────────────────────────
//
// 서버 intake.go 의 것과 같은 규칙이다. 사람이 입력한 말에서 나오는 축(keyword)만 이 검사를
// 통과해야 한다 — 나머지 축은 도구명·명령어·에이전트명이라 어휘가 닫혀 있다.

var secretPrefixRe = regexp.MustCompile(`(?i)^(?:` +
	`sk-|sk_live|sk_test|pk_live|rk_live|` +
	`ghp_|gho_|ghu_|ghs_|ghr_|github_pat_|glpat-|` +
	`xox[abprs]-|akia|asia|aiza|ya29\.|eyj|npm_|dop_v1_|shpat_|shpss_|hf_|` +
	`aws_secret|aws_access|private_key|-----begin` +
	`)`)

var (
	assignRe     = regexp.MustCompile(`[=:]`)                // token=abc · user:pass
	hexRe        = regexp.MustCompile(`(?i)^[0-9a-f]{32,}$`) // md5 · sha · 랜덤 hex
	longDigitsRe = regexp.MustCompile(`\d{10,}`)             // 전화·카드·사번
	urlishRe     = regexp.MustCompile(`://|@`)               // 이메일 · 접속문자열
	randomCharRe = regexp.MustCompile(`^[A-Za-z0-9_+/=-]+$`)
)

// looksRandom 은 무작위로 보이는 문자열(키·해시의 일반형)을 잡는다.
//
// ⚠ **대소문자 혼합은 정규화(소문자 접기) 전에만 보인다.** 그래서 SafeKeyword 가 원본과
// 정규화본을 둘 다 검사한다. 길이는 룬 단위다 — 바이트로 세면 한글 낱말이 3배로 부풀어
// 정상 어휘가 잘린다.
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

// SafeKeyword 는 원본 키워드를 받아 (정규화된 키, 보내도 되는가) 를 돌려준다.
// false 면 그 키는 통째로 버린다.
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
		looksRandom(strings.TrimFunc(raw, isSpace)) {
		return "", false
	}
	return k, true
}

// NormKeyOf 는 축별 키 정규화다. 서버의 intake.NormKeyOf 와 같은 규칙이다 —
// 여기서 미리 접어 두면 서버가 접을 때 키가 합쳐져 상위 N 이 달라지는 일이 없다.
func NormKeyOf(kind, raw string) string {
	k := Clip(raw, KeyMax)
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
	if kind != "tool" && strings.IndexFunc(k, isSpace) >= 0 {
		k = firstToken(k)
	}
	return Clip(k, KeyMax)
}

// ── bash 축 ─────────────────────────────────────────────────────────────────

// 명령을 감싸기만 하는 접두 낱말들. 이것들을 그대로 키로 삼으면 bash 축이 전부
// `sudo`·`env` 로 덮여 아무 정보도 남지 않는다.
var bashWrappers = map[string]bool{
	"sudo": true, "env": true, "nohup": true, "exec": true,
	"time": true, "command": true, "builtin": true, "cd": true,
}

// 실행파일명으로 인정하는 모양. 여기 안 맞으면 **버린다** — 인자·경로·URL 조각이
// 실행파일명으로 위장해 들어오는 자리를 없앤다.
var execNameRe = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,60}$`)

// BashKey 는 명령줄에서 **선두 실행파일명 하나만** 뽑는다. 인자는 절대 남기지 않는다.
//
// 명령 인자에는 파일 경로·호스트명·토큰이 일상적으로 들어 있다(`curl -H "Authorization: ..."`).
// 그래서 이 함수는 화이트리스트 방식이다 — 실행파일명으로 보이는 것만 남기고 나머지는 전부 버린다.
//
// `cd x && pytest` 처럼 감싸는 낱말로 시작하면 구분자(`&&` · `;` · `|`) 뒤를 본다.
// 그렇게 안 하면 bash 축이 통째로 `cd` 가 되어 아무 것도 못 읽는다.
func BashKey(cmd string) string {
	fields := strings.FieldsFunc(cmd, isSpace)
	for i := 0; i < len(fields) && i < 40; i++ {
		w := fields[i]
		// `FOO=bar cmd` — 환경변수 대입은 건너뛴다. 값 쪽에 비밀이 있을 수 있어 절대 안 남긴다.
		if j := strings.Index(w, "="); j > 0 && !strings.ContainsAny(w[:j], `/\.`) {
			continue
		}
		// 구분자는 그 자체로 버리고 계속 본다.
		if w == "&&" || w == "||" || w == ";" || w == "|" || w == "(" || w == "{" {
			continue
		}
		if i := strings.LastIndexAny(w, `\/`); i >= 0 {
			w = w[i+1:]
		}
		if !execNameRe.MatchString(w) {
			return "" // 실행파일명으로 안 보인다 → 버린다
		}
		if bashWrappers[strings.ToLower(w)] {
			continue // 감싸는 낱말 — 다음 낱말을 본다
		}
		return w
	}
	return ""
}

// ── keyword 축 ──────────────────────────────────────────────────────────────

// 주입된 블록. 사람이 친 말이 아니라 하네스가 끼워 넣은 텍스트라 어휘 통계에 의미가 없고,
// 그 안에 경로·환경정보가 통째로 들어 있어 **토큰화 전에 잘라낸다.**
var injectedBlockRe = regexp.MustCompile(`(?s)<(system-reminder|command-name|command-message|command-args|local-command-stdout|teammate-message|task-notification)>.*?</\1>`)

// 이미지·첨부 자리표시자.
var placeholderRe = regexp.MustCompile(`\[(Image|Attachment|Pasted text)[^\]]*\]`)

// 낱말로 인정하는 모양: 라틴 문자로 시작하거나 한글. `+#.-` 는 낱말 안에서만 허용
// (c++ · node.js · e2e 같은 정상 어휘를 살린다).
var wordRe = regexp.MustCompile(`[\p{Hangul}]{2,}|[A-Za-z][A-Za-z0-9+#._-]{1,}`)

// stopwords — 순위표에 올라와도 아무 것도 말해 주지 않는 낱말.
// 정확성이 목적이 아니라 상위 N 자리를 낭비하지 않는 것이 목적이라 짧게 유지한다.
var stopwords = map[string]bool{
	"the": true, "and": true, "for": true, "you": true, "are": true, "that": true,
	"this": true, "with": true, "not": true, "but": true, "can": true, "has": true,
	"have": true, "was": true, "were": true, "will": true, "from": true, "all": true,
	"any": true, "get": true, "use": true, "your": true, "its": true, "our": true,
	"into": true, "then": true, "than": true, "them": true, "they": true, "what": true,
	"when": true, "which": true, "there": true, "here": true, "just": true, "only": true,
	"이거": true, "그거": true, "저거": true, "하고": true, "해줘": true, "해서": true,
	"그리고": true, "하지만": true, "그런데": true, "합니다": true, "입니다": true,
	"있는": true, "없는": true, "같은": true, "대해": true, "위해": true, "다음": true,
}

// Keywords 는 사람이 입력한 텍스트에서 **낱말만** 뽑는다. 원문은 절대 남기지 않는다.
//
// 순서는 등장 순서다(호출부가 세어 상위 N 을 고른다). 여기서 하는 일은 자르기뿐이다:
// 주입 블록 제거 → 자리표시자 제거 → 낱말 추출 → 스톱워드/길이 → SafeKeyword.
func Keywords(text string) []string {
	if text == "" {
		return nil
	}
	t := injectedBlockRe.ReplaceAllString(text, " ")
	t = placeholderRe.ReplaceAllString(t, " ")
	// 닫히지 않은 태그가 남아 있으면 그 뒤를 통째로 버린다 — 잘린 주입 블록의 내용이
	// 낱말로 새어 나오는 자리를 없앤다.
	if i := strings.Index(t, "<system-reminder>"); i >= 0 {
		t = t[:i]
	}

	out := make([]string, 0, 32)
	for _, m := range wordRe.FindAllString(t, 4000) {
		if utf8.RuneCountInString(m) < 2 {
			continue
		}
		k, ok := SafeKeyword(m)
		if !ok || stopwords[k] {
			continue
		}
		if utf8.RuneCountInString(k) < 2 {
			continue
		}
		out = append(out, k)
	}
	return out
}

// ── 공통 헬퍼 ───────────────────────────────────────────────────────────────

// Clip 은 공백을 턴 뒤 n **룬**으로 자른다. 바이트로 자르면 깨진 UTF-8 이 나온다.
func Clip(s string, n int) string {
	t := strings.TrimFunc(s, isSpace)
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

func firstToken(s string) string {
	if i := strings.IndexFunc(s, isSpace); i >= 0 {
		return s[:i]
	}
	return s
}

// isSpace 는 JS 정규식의 `\s` 에 대응한다 — Go 의 `\s` 는 ASCII 다섯 개뿐이라
// 그대로 쓰면 전각 공백이 낀 키가 통째로 남는다.
func isSpace(r rune) bool { return unicode.IsSpace(r) || r == '﻿' }
