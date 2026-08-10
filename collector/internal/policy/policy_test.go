package policy

import (
	"strings"
	"testing"
)

// BashKey 는 선두 실행파일명 하나만 남긴다 — 인자·경로·감싸는 낱말은 절대 남기지 않는다.
func TestBashKey(t *testing.T) {
	cases := []struct{ cmd, want string }{
		{"git commit -m 'msg with secret'", "git"},
		{"/usr/local/bin/pytest -q tests/", "pytest"},
		{"sudo systemctl restart nginx", "systemctl"}, // 감싸는 낱말(sudo)은 건너뛴다
		{"cd /repo && npm run build", "npm"},          // cd + 구분자 뒤를 본다
		{"FOO=bar deploy --prod", "deploy"},           // 환경변수 대입은 건너뛴다
		{"", ""},
		{"./a b c", "a"},
	}
	for _, c := range cases {
		if got := BashKey(c.cmd); got != c.want {
			t.Errorf("BashKey(%q)=%q, want %q", c.cmd, got, c.want)
		}
	}
}

// BashKey 는 인자에 든 비밀을 절대 남기지 않는다.
func TestBashKeyDropsArgs(t *testing.T) {
	got := BashKey(`curl -H "Authorization: Bearer sk-live-abcdef" https://api.example.com/x?token=abc`)
	if got != "curl" {
		t.Fatalf("BashKey=%q, want curl", got)
	}
	if strings.Contains(got, "sk-") || strings.Contains(got, "token") || strings.Contains(got, "api") {
		t.Fatalf("인자가 샜다: %q", got)
	}
}

// SafeKeyword 는 시크릿·PII 모양을 버리고 정상 낱말만 (소문자로) 남긴다.
func TestSafeKeyword(t *testing.T) {
	drop := []string{
		"sk-live1234567890abcdef",             // 벤더 접두사
		"AKIAIOSFODNN7EXAMPLE",                // aws access key
		"ghp_0123456789abcdefghijABCDEF",      // github pat
		"token=abcdef",                        // 값이 붙은 대입
		"user@example.com",                    // 이메일
		"postgres://u:p@host/db",              // 접속문자열
		"1234567890123",                       // 10자리+ 숫자
		"deadbeefdeadbeefdeadbeefdeadbeef",    // 32+ hex
		"aVeryLongRandomLookingTokenXY12ab34", // 무작위 혼합
	}
	for _, d := range drop {
		if k, ok := SafeKeyword(d); ok {
			t.Errorf("SafeKeyword(%q) 가 통과했다 → %q", d, k)
		}
	}

	keep := map[string]string{
		"Refactor": "refactor", // 소문자로 접힌다
		"database": "database",
		"node.js":  "node.js",
		"c++":      "c++",
		"한글키워드":    "한글키워드",
	}
	for in, want := range keep {
		if k, ok := SafeKeyword(in); !ok || k != want {
			t.Errorf("SafeKeyword(%q)=(%q,%v), want (%q,true)", in, k, ok, want)
		}
	}
}

// Keywords 는 주입 블록을 통째로 잘라내고 사람이 친 낱말만 남긴다.
func TestKeywordsStripsInjectedBlocks(t *testing.T) {
	text := "please refactor <system-reminder>secret path /Users/x/.env AKIA leak</system-reminder> the parser"
	got := Keywords(text)
	joined := strings.Join(got, " ")
	if strings.Contains(joined, "secret") || strings.Contains(joined, "AKIA") ||
		strings.Contains(strings.ToLower(joined), "users") || strings.Contains(joined, "env") {
		t.Fatalf("주입 블록 내용이 낱말로 샜다: %v", got)
	}
	if !contains(got, "refactor") || !contains(got, "parser") {
		t.Fatalf("정상 낱말이 빠졌다: %v", got)
	}
}

// Keywords 는 닫히지 않은 주입 태그 뒤를 통째로 버린다.
func TestKeywordsDropsUnclosedInjection(t *testing.T) {
	got := Keywords("valid words here <system-reminder>trailing secret AKIA")
	if contains(got, "trailing") || contains(got, "secret") {
		t.Fatalf("잘린 주입 블록이 샜다: %v", got)
	}
	if !contains(got, "valid") || !contains(got, "words") {
		t.Fatalf("앞부분 낱말이 빠졌다: %v", got)
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
