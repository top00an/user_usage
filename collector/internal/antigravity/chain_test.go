package antigravity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 체이닝의 계약은 하나다: **사용자의 상태줄을 잃게 하지 않는다.**
// 우리가 statusLine 을 가져가면 원래 쓰던 상태줄이 사라지므로, 그것을 그대로 통과시킨다.
// 그리고 그 통과가 실패하더라도 상태줄이 멈추면 안 된다 — 실패는 전부 폴백이다.

const sampleJSON = `{"conversation_id":"c1","context_window":{"total_output_tokens":7}}`

// ① 성공 — 기존 명령의 stdout 이 **그대로** 나온다. 우리 문구는 한 글자도 붙지 않는다.
func TestChainPrevPassesThroughStdout(t *testing.T) {
	out, ok := ChainPrevTimeout("printf 'orca ▸ main ✓'", "/opt/usage-collector", []byte(sampleJSON), time.Second)
	if !ok {
		t.Fatalf("체인이 실패로 판정됐다")
	}
	if string(out) != "orca ▸ main ✓" {
		t.Fatalf("통과된 출력이 다르다: %q", out)
	}
}

// ①-b 같은 JSON 이 기존 명령의 **stdin 으로** 넘어간다(우리가 먼저 다 읽었으므로 재공급이 필수다).
func TestChainPrevFeedsStdin(t *testing.T) {
	out, ok := ChainPrevTimeout("cat", "/opt/usage-collector", []byte(sampleJSON), time.Second)
	if !ok {
		t.Fatal("체인이 실패로 판정됐다")
	}
	if string(out) != sampleJSON {
		t.Fatalf("stdin 이 그대로 넘어가지 않았다: %q", out)
	}
}

// ①-c 기존 명령의 stderr 는 상태줄로 새지 않는다(화면이 깨지는 흔한 경로다).
func TestChainPrevDropsStderr(t *testing.T) {
	out, ok := ChainPrevTimeout("printf 'ok'; printf 'noise' >&2", "/opt/usage-collector", nil, time.Second)
	if !ok || string(out) != "ok" {
		t.Fatalf("stderr 가 섞였거나 실패했다: ok=%v out=%q", ok, out)
	}
}

// ② 실패 — 종료코드가 0 이 아니면 조용히 무시하고 폴백한다.
func TestChainPrevFailureFallsBack(t *testing.T) {
	if out, ok := ChainPrevTimeout("printf 'half'; exit 3", "/opt/usage-collector", nil, time.Second); ok {
		t.Fatalf("실패한 명령을 통과시켰다: %q", out)
	}
	if _, ok := ChainPrevTimeout("this-command-does-not-exist-9f3a", "/opt/usage-collector", nil, time.Second); ok {
		t.Fatal("존재하지 않는 명령을 통과시켰다")
	}
}

// ③ 타임아웃 — 느린 명령이 상태줄을 붙잡지 못한다. 제한 시간 안에 반드시 돌아온다.
func TestChainPrevTimesOut(t *testing.T) {
	start := time.Now()
	out, ok := ChainPrevTimeout("sleep 5; printf 'late'", "/opt/usage-collector", nil, 200*time.Millisecond)
	el := time.Since(start)
	if ok {
		t.Fatalf("타임아웃인데 통과시켰다: %q", out)
	}
	if el > 3*time.Second {
		t.Fatalf("타임아웃이 걸리지 않았다(%s 걸림)", el)
	}
}

// 기본 제한은 2초다(계약값). 상수를 바꾸면 이 테스트가 먼저 깨진다.
func TestChainTimeoutIsTwoSeconds(t *testing.T) {
	if ChainTimeout != 2*time.Second {
		t.Fatalf("ChainTimeout=%s (계약은 2s)", ChainTimeout)
	}
}

// ④ 자기참조 방지 — AGY_PREV_STATUSLINE 이 우리를 가리키면 **실행조차 하지 않는다.**
// 실행되면 무한 재귀로 statusLine 렌더마다 프로세스가 폭증한다.
func TestChainPrevRefusesSelfReference(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "usage-collector")
	witness := filepath.Join(dir, "ran")

	cases := []string{
		self + " -antigravity-statusline",
		"/somewhere/else/usage-collector -antigravity-statusline",
		"sh -c '" + self + "'",
		"exec " + self + " -antigravity-statusline",
	}
	for _, prev := range cases {
		// touch 를 덧붙여, 만약 실행됐다면 증거 파일이 남게 한다.
		cmd := prev + "; touch " + witness
		if out, ok := ChainPrevTimeout(cmd, self, nil, time.Second); ok {
			t.Fatalf("자기참조를 통과시켰다: prev=%q out=%q", prev, out)
		}
		if _, err := os.Stat(witness); err == nil {
			t.Fatalf("자기참조 명령이 실행됐다: %q", prev)
		}
	}
}

// 빈 값·공백은 체인이 아니다(설치되지 않은 상태) → 폴백.
func TestChainPrevEmptyIsNoChain(t *testing.T) {
	for _, prev := range []string{"", "   ", "\n\t"} {
		if _, ok := ChainPrevTimeout(prev, "/opt/usage-collector", nil, time.Second); ok {
			t.Fatalf("빈 prev 를 체인으로 봤다: %q", prev)
		}
	}
}

// selfBin 의 basename 이 너무 짧거나 흔하면 그것만으로 자기참조로 몰지 않는다
// (그랬다가는 남의 정상 명령을 이유 없이 죽인다).
func TestChainPrevShortSelfBaseDoesNotOverMatch(t *testing.T) {
	if _, ok := ChainPrevTimeout("printf 'sh sh sh'", "/bin/sh", nil, time.Second); !ok {
		t.Fatal("짧은 basename 때문에 정상 명령이 막혔다")
	}
}

// ReadStatusLineInput 은 stdin 을 전부 읽되 무한정 읽지는 않는다.
func TestReadStatusLineInputLimits(t *testing.T) {
	if got := ReadStatusLineInput(strings.NewReader(sampleJSON)); string(got) != sampleJSON {
		t.Fatalf("전부 읽지 못했다: %q", got)
	}
	huge := strings.Repeat("x", int(MaxStatusLineBytes)+4096)
	if got := ReadStatusLineInput(strings.NewReader(huge)); int64(len(got)) > MaxStatusLineBytes {
		t.Fatalf("상한을 넘겨 읽었다: %d", len(got))
	}
}
