package antigravity

// statusLine 체이닝 — **사용자의 상태줄을 빼앗지 않기 위한 장치다.**
//
// # 왜 필요한가
//
// Antigravity 의 `statusLine` 은 **한 자리뿐이다.** 우리가 토큰을 보려면 그 자리를 차지해야
// 하는데, 그 자리에 이미 남의 상태줄이 있으면 그것을 지우게 된다. 사용량 관측을 켠 대가로
// 매 순간 보던 화면을 잃는 것은 받아들일 수 없는 거래다.
//
// 그래서 설치기(scripts/install.sh)가 기존 명령을 `AGY_PREV_STATUSLINE` 에 옮겨 두고,
// 우리는 스풀에 적은 **다음** 그 명령을 대신 실행해 출력을 그대로 흘려보낸다.
// 화면은 여전히 그들의 것이고, 우리는 조용히 옆에서 센다.
//
// # 이 파일이 지키는 네 가지
//
//	1. 같은 JSON 을 넘긴다 — statusLine 은 stdin 으로만 데이터를 받는다. 우리가 먼저 다
//	   읽어버리므로, 읽은 바이트를 그대로 다시 먹여야 한다(안 그러면 남의 상태줄이 빈 입력을
//	   받고 조용히 망가진다 — 에러가 아니라 침묵이라 더 나쁘다).
//	2. 출력에 아무것도 덧붙이지 않는다 — 통과는 통과여야 한다.
//	3. 실패·타임아웃은 전부 폴백이다 — 상태줄 때문에 CLI 가 멈추는 일은 없어야 한다.
//	4. 자기참조면 실행하지 않는다 — `AGY_PREV_STATUSLINE` 이 우리를 가리키면 렌더 한 번마다
//	   프로세스가 무한히 갈라진다. 실행 전에 막는다.

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// PrevStatusLineEnv 는 **원래 쓰던** statusLine 명령을 보관하는 환경변수다.
// 설치기가 config.env(600)에 적고, 훅 래퍼가 export 해서 이 프로세스로 넘긴다.
const PrevStatusLineEnv = "AGY_PREV_STATUSLINE"

// StatusLineFlag 는 우리 statusLine 모드의 플래그다. 자기참조 판정의 근거이자,
// 설치기가 "이미 우리 것이 박혀 있는지" 판정하는 멱등 키이기도 하다(양쪽이 같은 문자열을 본다).
const StatusLineFlag = "-antigravity-statusline"

// ChainTimeout 은 기존 명령을 기다리는 최대 시간이다.
//
// 2초인 이유: statusLine 은 렌더마다 불린다. 여기서 오래 붙들면 그만큼 CLI 가 멈춘 것처럼
// 보인다. 남의 명령이 느린 것은 우리가 고칠 수 없으므로, 우리 쪽에서 끊고 폴백한다.
const ChainTimeout = 2 * time.Second

// chainWaitDelay 는 제한시간이 지난 뒤 파이프에서 손을 떼기까지의 유예다.
// 그룹 kill 이 정상 동작하면 여기까지 오지 않는다 — 순전히 백스톱이다.
const chainWaitDelay = 200 * time.Millisecond

// MaxStatusLineBytes 는 stdin 에서 읽을 상한이다. statusLine payload 는 실측 수 KB 라
// 1MiB 면 넉넉하고, 상한이 없으면 이상한 입력 하나로 상태줄 프로세스가 메모리를 먹는다.
const MaxStatusLineBytes int64 = 1 << 20

// minSelfBaseLen 은 basename 만으로 자기참조를 판정할 최소 길이다.
//
// `/bin/sh` 처럼 짧고 흔한 이름을 근거로 삼으면 남의 정상 명령을 이유 없이 죽인다.
// 실제 배포 이름(`usage-collector`)은 충분히 길고 고유해서 이 문턱을 여유롭게 넘는다.
const minSelfBaseLen = 8

// ReadStatusLineInput 은 statusLine stdin 을 전부 읽는다.
//
// **전부** 읽어야 하는 이유: 같은 바이트를 두 번 쓴다(스풀 기록 · 체인 명령의 stdin).
// 스트림은 한 번밖에 못 읽으므로 바이트로 들고 있어야 한다.
// 읽다 실패해도 그때까지 읽은 것을 돌려준다 — 상태줄에서 에러를 던져 봐야 할 일이 없다.
func ReadStatusLineInput(r io.Reader) []byte {
	if r == nil {
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(r, MaxStatusLineBytes))
	return b
}

// ChainPrev 는 기존 statusLine 명령을 실행하고 그 표준출력을 그대로 돌려준다.
//
// prev  : AGY_PREV_STATUSLINE 값(셸 한 줄). 비어 있으면 체인이 없다.
// selfBin: 지금 실행 중인 우리 바이너리 경로(os.Executable). 자기참조 판정에 쓴다.
// input : statusLine 이 준 JSON 원본. 그대로 prev 의 stdin 으로 간다.
//
// 두 번째 반환값이 false 면 **호출자가 우리 요약을 출력해야 한다.** false 가 되는 경우는
// 체인 없음 · 자기참조 · 실행 실패 · 비정상 종료 · 타임아웃 넷뿐이고, 넷 다 조용하다.
func ChainPrev(prev, selfBin string, input []byte) ([]byte, bool) {
	return ChainPrevTimeout(prev, selfBin, input, ChainTimeout)
}

// ChainPrevTimeout 은 ChainPrev 의 제한시간을 직접 주는 판이다(테스트가 쓴다).
func ChainPrevTimeout(prev, selfBin string, input []byte, timeout time.Duration) ([]byte, bool) {
	prev = strings.TrimSpace(prev)
	if prev == "" {
		return nil, false
	}
	if isSelfReference(prev, selfBin) {
		// **실행하지 않는다.** 여기서 한 번이라도 돌면 그 자식이 또 같은 판정을 하러
		// 들어와 렌더 한 번이 프로세스 폭발이 된다.
		return nil, false
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// `sh -c` 로 실행한다 — AGY_PREV_STATUSLINE 은 파이프·리다이렉션이 섞인 셸 한 줄일 수
	// 있고(실제 상태줄 명령이 흔히 그렇다), 우리가 토큰으로 쪼개면 그런 명령이 전부 깨진다.
	cmd := exec.CommandContext(ctx, "sh", "-c", prev)
	cmd.Stdin = bytes.NewReader(input)
	var out bytes.Buffer
	cmd.Stdout = &out
	// 남의 stderr 는 버린다. 상태줄에 섞이면 화면이 깨지고, 그건 우리가 만든 피해가 된다.
	cmd.Stderr = io.Discard

	// ── 타임아웃이 진짜로 걸리게 만드는 두 줄 ────────────────────────────────────
	//
	// 기본 CommandContext 만으로는 **끊기지 않는다.** 실측: `sh -c 'sleep 5'` 에 200ms
	// 제한을 걸어도 5초를 기다렸다. `sh` 만 죽고 손자(`sleep`)가 살아남아 우리 stdout
	// 파이프의 쓰기 끝을 붙들고 있어서, 출력을 모으는 고루틴이 EOF 를 못 받는다.
	// 그러면 상태줄 렌더가 통째로 멈춘다 — 이 파일이 막으려던 바로 그 사고다.
	//
	//	Setpgid + Kill(-pid) : 손자까지 포함한 프로세스 **그룹**을 통째로 죽인다.
	//	WaitDelay            : 그래도 누가 파이프를 붙들면 강제로 손을 뗀다(백스톱).
	//
	// 대상 플랫폼은 darwin·linux 뿐이다(scripts/build.sh 의 크로스컴파일 표 그대로).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = chainWaitDelay

	if err := cmd.Run(); err != nil {
		return nil, false
	}
	// ctx 가 끝났는데 Run 이 성공으로 보이는 경계(출력이 잘렸을 수 있다)는 폴백으로 민다.
	if ctx.Err() != nil {
		return nil, false
	}
	// 성공하면 **있는 그대로** 돌려준다. 잘라내거나 덧붙이지 않는다 — 화면은 그들 것이다.
	return out.Bytes(), true
}

// isSelfReference 는 prev 가 우리 자신을 부르는지 본다.
//
// 근거는 둘이다:
//   - statusLine 플래그가 들어 있다 → 어떤 경로로 부르든 우리 모드다.
//   - 우리 바이너리 경로(또는 충분히 고유한 basename)가 들어 있다 → 플래그를 다른 방식으로
//     넘기거나 래퍼로 감싼 경우까지 잡는다.
//
// 과탐지 쪽으로 기울인 판정이다. 잘못 막으면 사용자는 우리 요약 한 줄을 보고(정보 손실),
// 잘못 통과시키면 프로세스가 무한히 갈라진다(기계가 멈춘다). 손해의 크기가 다르다.
func isSelfReference(prev, selfBin string) bool {
	if strings.Contains(prev, StatusLineFlag) {
		return true
	}
	selfBin = strings.TrimSpace(selfBin)
	if selfBin == "" {
		return false
	}
	if strings.Contains(prev, selfBin) {
		return true
	}
	base := filepath.Base(selfBin)
	if len(base) >= minSelfBaseLen && strings.Contains(prev, base) {
		return true
	}
	return false
}
