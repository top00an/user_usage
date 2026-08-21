// usage-collector — 팀원 PC 의 코딩 에이전트 트랜스크립트를 훑어 사용량을 서버로 보고한다.
//
// 원천은 셋이다:
//
//	claude  ~/.claude/projects/<슬러그>/<sessionId>.jsonl
//	codex   ~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl
//	gemini  ~/.gemini/tmp/<슬러그>/chats/session-<ts>-<id8>.jsonl
//
// 흐름은 넷이다: 원천마다 세션 파일을 찾고 → 증분(바뀐 세션만) 골라 파싱·매핑하고 →
// `POST /api/usage` 로 절대값을 보내고 → 보낸 세션의 지문을 체크포인트에 남긴다.
//
// 재실행은 언제나 안전하다. 서버가 session_id 절대값으로 UPSERT 하므로, 체크포인트를 지우고
// 전량을 다시 보내도 값이 부풀지 않는다(멱등성은 서버 키가 진다).
//
// 체크포인트는 두 원천이 **한 파일을 같이 쓴다.** 키가 파일 절대경로라 원천이 달라도
// 섞일 수 없다(`~/.claude/...` 와 `~/.codex/...` 는 같은 키가 될 수 없다). 그래서 키에
// 플랫폼 접두사를 붙이지 않는다 — 붙이면 기존 체크포인트가 통째로 무효가 되어 전량 재전송이
// 한 번 더 일어날 뿐이고, 얻는 것이 없다.
//
// 판정 로직은 전부 internal 패키지 안에 있다 — 이 파일이 하는 일은 배선과 설정뿐이다.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/tscorp/user-usage/collector/internal/antigravity"
	"github.com/tscorp/user-usage/collector/internal/codex"
	"github.com/tscorp/user-usage/collector/internal/codexcfg"
	"github.com/tscorp/user-usage/collector/internal/gemini"
	"github.com/tscorp/user-usage/collector/internal/payload"
	"github.com/tscorp/user-usage/collector/internal/sender"
	"github.com/tscorp/user-usage/collector/internal/state"
	"github.com/tscorp/user-usage/collector/internal/transcript"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

type options struct {
	dir      string
	codexDir string
	/*
	 * codexDirSet 은 `-codex-dir` 을 **사용자가 직접 줬는가**다.
	 *
	 * 필요한 이유: 기본값은 환경(CODEX_HOME)에서 오고, 그때는 기본 홈도 함께 훑어야 한다
	 * (codexDirs 참고 — 둘 중 하나만 보면 나머지가 조용히 빠진다). 반면 사용자가 명시했으면
	 * "이 디렉터리만 보라"는 지시이므로 넓히면 안 된다. 값만으로는 둘을 가를 수 없다.
	 */
	codexDirSet bool
	geminiDir   string
	agyDir      string
	agyHome     string
	platform    string
	server      string
	token       string
	state       string
	user        string
	machine     string
	limit       int
	dryRun      bool
	all         bool
	// statusLine 은 수집이 아니라 **기록** 모드다(§ runStatusLine).
	statusLine bool
}

// aggregator 는 원천별 파서의 공통 모양이다. transcript(Claude)·codex·gemini 셋 다 이걸
// 만족하고, 이 파일은 어느 쪽인지 몰라도 된다.
type aggregator interface {
	AddFile(fallbackID string, r io.Reader) error
	Sessions() []payload.Session
}

// pathAggregator 는 파일 **경로**까지 필요한 파서를 위한 선택적 확장이다.
//
// Gemini 만 이걸 만족한다: 프로젝트 이름이 파일 안이 아니라 경로의 슬러그 디렉터리에 있다
// (`<geminiDir>/tmp/<슬러그>/chats/...`). Claude·Codex 는 줄 안의 cwd 로 알 수 있어 이 확장을
// 구현하지 않고, 따라서 두 원천의 동작은 이 변경으로 한 글자도 바뀌지 않는다.
type pathAggregator interface {
	AddFileAt(path, fallbackID string, r io.Reader) error
}

// source 는 훑을 원천 하나다.
type source struct {
	platform string
	dir      string
	// match 는 스캔할 파일인지 판정한다. 기본은 `*.jsonl` 이고, Gemini 만 다르다
	// (레거시 `.json` 을 같이 받고, `.unreadable-*`·`.tmp-*` 곁가지를 반드시 제외한다).
	match func(path string) bool
	// key 는 파일 경로 → 세션 그룹 키. Claude 는 파일명 stem 이 곧 sessionId 이고,
	// Codex 는 롤아웃 파일명 꼬리의 uuid, Gemini 는 파일명 stem(서브에이전트는 부모 세션 id)이다.
	key    func(path string) string
	newAgg func() aggregator
}

// matchJSONL 은 Claude·Codex 가 쓰는 기본 규칙이다(기존 동작 그대로).
func matchJSONL(path string) bool { return strings.HasSuffix(path, ".jsonl") }

// sourcesOf 는 옵션에서 실제로 훑을 원천을 고른다. 디렉터리가 비어 있으면(플래그로 ""를 주면)
// 그 원천은 아예 빠진다 — 개별 비활성화가 그 방식이다.
func sourcesOf(opt options) []source {
	var out []source
	if wants(opt.platform, "claude") && opt.dir != "" {
		out = append(out, source{
			platform: "claude",
			dir:      opt.dir,
			match:    matchJSONL,
			key:      func(p string) string { return strings.TrimSuffix(filepath.Base(p), ".jsonl") },
			newAgg:   func() aggregator { return transcript.New() },
		})
	}
	if wants(opt.platform, "codex") && opt.codexDir != "" {
		/*
		 * Codex 롤아웃 디렉터리는 **하나가 아닐 수 있다.**
		 *
		 * 실측(2026-08-21): 이 머신은 Orca 가 `CODEX_HOME` 을 옮겨 띄우고, 그때 세션은 그 홈에만
		 * 쌓인다. 그런데 사람은 같은 PC 에서 **다른 터미널로도** Codex 를 쓴다(그쪽은 기본
		 * `~/.codex`). 둘 중 하나만 훑으면 나머지가 통째로 빠지는데 경고가 없다.
		 *
		 * 훅이 물려받는 환경이 어느 쪽이냐에 따라 빠지는 쪽이 달라지므로, "환경변수를 존중한다"만
		 * 으로는 침묵 손실이 **자리만 옮긴다.** 그래서 서로 다르면 **둘 다** 훑는다.
		 *
		 * 중복 계상은 나지 않는다: 세션 id 가 uuidv7 이고 서버는 그 키로 절대값 UPSERT 를 한다.
		 * 같은 세션이 두 곳에 있어도 같은 행을 두 번 덮어쓸 뿐이다.
		 *
		 * ⚠ 사용자가 `-codex-dir` 을 **명시**했으면 그 하나만 본다. 명시적 지시를 넓히면
		 *   "이 디렉터리만 보라"는 뜻이 깨진다(진단·다른 계정 검사에 쓰는 플래그다).
		 */
		for _, dir := range codexDirs(opt) {
			/*
			 * provider 이름 → 엔드포인트 매핑을 그 홈의 config 에서 **한 번만** 읽어 주입한다.
			 *
			 * 홈마다 따로 읽는 것이 중요하다 — 두 홈은 서로 다른 설치이고 provider 정의가 다를
			 * 수 있다. 한쪽 config 로 양쪽을 판정하면 다른 설치의 설정으로 locality 를 매긴다.
			 *
			 * 세션 수만큼 다시 읽지는 않는다: 훑는 도중 파일이 바뀌면 세션마다 다른 답이 나와
			 * 결과가 비결정적이 된다. 읽기 실패는 빈 맵이고, 그러면 locality 가 판정되지 않는다
			 * (없는 정보를 추측하지 않는다).
			 */
			providers := codexProviders(dir)
			out = append(out, source{
				platform: codex.Platform,
				dir:      dir,
				match:    matchJSONL,
				key:      codex.SessionIDFromPath,
				newAgg: func() aggregator {
					a := codex.New()
					a.ResolveEndpoint = func(p string) string { return providers[p] }
					return a
				},
			})
		}
	}
	if wants(opt.platform, gemini.Platform) && opt.geminiDir != "" {
		// 세션은 `<geminiDir>/tmp/` 아래에만 있다. 스캔 뿌리를 거기로 좁혀야
		// settings.json·oauth 토큰 같은 `~/.gemini` 직속 파일을 아예 열지 않는다.
		out = append(out, source{
			platform: gemini.Platform,
			dir:      filepath.Join(opt.geminiDir, "tmp"),
			match:    gemini.Match,
			key:      gemini.SessionKeyFromPath,
			newAgg:   func() aggregator { return gemini.New() },
		})
	}
	if wants(opt.platform, antigravity.Platform) && opt.agyDir != "" {
		// Antigravity 는 디스크에 토큰이 없다(훅·transcript·대화 DB 전부 확인됨).
		// 그래서 훑는 대상이 CLI 의 홈이 아니라 **우리 스풀**이다 — statusLine 이 적어 둔 것.
		historyPath := ""
		if opt.agyHome != "" {
			historyPath = filepath.Join(opt.agyHome, "history.jsonl")
		}
		out = append(out, source{
			platform: antigravity.Platform,
			dir:      opt.agyDir,
			match:    antigravity.Match,
			key:      antigravity.SessionKeyFromPath,
			newAgg: func() aggregator {
				a := antigravity.New()
				// history.jsonl 은 slash·keyword·project 축의 유일한 출처다. 없으면
				// 그 축들만 조용히 빠진다(사용량 자체는 스풀만으로 온전하다).
				if historyPath != "" {
					if fh, err := os.Open(historyPath); err == nil {
						_ = a.AddHistory(fh)
						fh.Close()
					}
				}
				return a
			},
		})
	}
	return out
}

func wants(sel, platform string) bool { return sel == "all" || sel == platform }

func run(args []string, stdout, stderr *os.File) int {
	opt, err := parseFlags(args, stderr)
	if err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		fmt.Fprintf(stderr, "설정 오류: %v\n", err)
		return 2
	}

	if opt.statusLine {
		return runStatusLine(opt, stdinReader, stdout, stderr)
	}

	st, err := state.Load(opt.state)
	if err != nil {
		fmt.Fprintf(stderr, "체크포인트를 읽지 못했다(%s): %v\n", opt.state, err)
		return 1
	}

	// 진행 로그는 전부 stderr 로 — stdout 은 -dry-run 의 페이로드(기계가 읽는 출력)만 태운다.
	var (
		sessions     []payload.Session
		pending      []fileRef // 전송이 성공한 뒤에야 지문을 남길 파일들
		totalFiles   int
		totalChanged int
	)
	for _, src := range sourcesOf(opt) {
		// 없는 디렉터리는 조용히 지나간다 — Claude 만 쓰는 팀원, Codex 만 쓰는 팀원 모두
		// 아무 설정 없이 그대로 돌아야 한다.
		if fi, err := os.Stat(src.dir); err != nil || !fi.IsDir() {
			continue
		}

		// 세션 파일 그룹: 세션키 → 파일 목록. 대개 세션당 파일 하나지만, 재개된 세션은
		// 여럿일 수 있어 그룹으로 든다.
		groups, err := discover(src.dir, src.match, src.key, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "[%s] 디렉터리를 훑지 못했다(%s): %v\n", src.platform, src.dir, err)
			continue
		}
		if len(groups) == 0 {
			continue
		}

		// 증분: 파일 중 하나라도 바뀐 세션만 고른다(-all 이면 전부).
		stems := sortedStems(groups)
		changed := changedStems(stems, groups, st, opt.all)
		// -limit 은 원천마다 적용한다. 전역으로 자르면 앞선 원천이 예산을 다 먹어 뒤 원천이
		// 영영 안 올라간다.
		if opt.limit > 0 && len(changed) > opt.limit {
			changed = changed[:opt.limit]
		}
		totalFiles += len(groups)
		totalChanged += len(changed)
		fmt.Fprintf(stderr, "[%s] %s — 세션 %d개 · 바뀐 세션 %d개%s\n",
			src.platform, src.dir, len(groups), len(changed), limitNote(opt))
		if len(changed) == 0 {
			continue
		}

		// 파싱·매핑 — 바뀐 세션의 모든 파일을 하나의 누적기에 흘려 절대값을 낸다.
		agg := src.newAgg()
		for _, stem := range changed {
			for _, f := range groups[stem] {
				fh, err := os.Open(f.path)
				if err != nil {
					fmt.Fprintf(stderr, "  ⚠ 열지 못함(%s): %v\n", f.path, err)
					continue
				}
				// 경로가 필요한 파서(Gemini)에게는 경로째 준다 — 프로젝트 이름이 경로에 있다.
				var addErr error
				if pa, ok := agg.(pathAggregator); ok {
					addErr = pa.AddFileAt(f.path, stem, fh)
				} else {
					addErr = agg.AddFile(stem, fh)
				}
				if addErr != nil {
					fmt.Fprintf(stderr, "  ⚠ 읽기 중단(%s): %v\n", f.path, addErr)
				}
				fh.Close()
				pending = append(pending, f)
			}
		}
		for _, s := range agg.Sessions() {
			// 귀속은 여기 한 곳에서만 한다 — 파서마다 따로 박아 두면 갈라진다.
			s.Platform = src.platform
			sessions = append(sessions, s)
		}
	}

	if totalFiles == 0 {
		fmt.Fprintln(stderr, "세션 파일(*.jsonl)이 없다(훑을 디렉터리가 없거나 비었다).")
		return 0
	}
	if totalChanged == 0 {
		fmt.Fprintln(stderr, "보낼 것이 없다(모든 세션이 마지막 전송 이후 그대로다).")
		return 0
	}
	if len(sessions) == 0 {
		fmt.Fprintln(stderr, "매핑 결과 보낼 세션이 없다(신호 없는 세션뿐).")
		return 0
	}

	if opt.dryRun {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(payload.Report{User: opt.user, Machine: opt.machine, Sessions: sessions})
		fmt.Fprintf(stderr, "[dry-run] 세션 %d개 — 전송하지 않았고 체크포인트도 갱신하지 않았다.\n", len(sessions))
		return 0
	}

	if opt.token == "" {
		fmt.Fprintln(stderr, "인테이크 토큰이 없다 — USAGE_INTAKE_TOKEN(또는 -token)을 설정하라. "+
			"(값을 보려면 -dry-run 을 쓴다)")
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cl := sender.New(opt.server, opt.token)
	res, err := cl.Send(ctx, opt.user, opt.machine, sessions)
	if err != nil {
		fmt.Fprintf(stderr, "전송 실패: %v\n", err)
		return 1
	}

	// 전송이 성공한 뒤에야 지문을 남긴다 — 실패한 세션이 "보냈다"로 기록되면 다음 실행이
	// 그것을 건너뛰어 영영 안 보낸다.
	for _, f := range pending {
		st.Mark(f.path, f.info)
	}
	if err := st.Save(); err != nil {
		fmt.Fprintf(stderr, "  ⚠ 체크포인트 저장 실패: %v(다음 실행이 재전송한다 — 무해하다)\n", err)
	}

	fmt.Fprintf(stderr, "전송 완료 — 서버 기준 세션 %d · 카운터 %d · 버킷 %d\n",
		res.Sessions, res.Counters, res.Buckets)
	return 0
}

func parseFlags(args []string, stderr *os.File) (options, error) {
	fs := flag.NewFlagSet("usage-collector", flag.ContinueOnError)
	fs.SetOutput(stderr)

	opt := options{}
	fs.StringVar(&opt.dir, "dir", defaultDir(), "Claude 트랜스크립트 디렉터리(기본 ~/.claude/projects). \"\"면 Claude 원천 비활성")
	fs.StringVar(&opt.codexDir, "codex-dir", defaultCodexDir(), "Codex 롤아웃 디렉터리(기본 ~/.codex/sessions). \"\"면 Codex 원천 비활성")
	fs.StringVar(&opt.geminiDir, "gemini-dir", defaultGeminiDir(), "Gemini 홈 디렉터리(기본 ~/.gemini — 세션은 그 아래 tmp/*/chats). \"\"면 Gemini 원천 비활성")
	fs.StringVar(&opt.agyDir, "antigravity-dir", defaultAntigravityDir(), "Antigravity 스풀 디렉터리(기본 ~/.config/claude-usage/antigravity). \"\"면 Antigravity 원천 비활성")
	fs.StringVar(&opt.agyHome, "antigravity-home", defaultAntigravityHome(), "Antigravity CLI 홈(기본 ~/.gemini/antigravity-cli — history.jsonl 로 slash·keyword 축을 채운다). \"\"면 그 축만 비활성")
	fs.BoolVar(&opt.statusLine, "antigravity-statusline", false,
		"statusLine 기록 모드: stdin 의 StatusLineData 를 스풀에 적고 상태줄 한 줄을 출력한다(수집하지 않는다)")
	fs.StringVar(&opt.platform, "platform", "all", "훑을 원천: all|claude|codex|gemini|antigravity")
	fs.StringVar(&opt.server, "server", envOr("USAGE_SERVER_URL", "http://127.0.0.1:4191"), "서버 주소")
	fs.StringVar(&opt.token, "token", intakeToken(), "인테이크 토큰(기본 USAGE_INTAKE_TOKEN→USAGE_ADMIN_TOKEN)")
	fs.StringVar(&opt.state, "state", defaultState(), "체크포인트 파일 경로")
	fs.StringVar(&opt.user, "user", defaultUser(), "보고 계정명(서버 매핑이 있으면 서버가 덮는다)")
	fs.StringVar(&opt.machine, "machine", defaultMachine(), "머신 이름")
	fs.IntVar(&opt.limit, "limit", 0, "바뀐 세션 중 최근 N개만(0=전부). 소량 실증에 쓴다")
	fs.BoolVar(&opt.dryRun, "dry-run", false, "전송하지 않고 보낼 페이로드를 출력(토큰 불필요)")
	fs.BoolVar(&opt.all, "all", false, "체크포인트를 무시하고 전량 재파싱")

	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	switch opt.platform {
	case "all", "claude", codex.Platform, gemini.Platform, antigravity.Platform:
	default:
		return options{}, fmt.Errorf("-platform 은 all|claude|codex|gemini|antigravity 중 하나다(받은 값: %q)", opt.platform)
	}
	// `-codex-dir` 을 사람이 직접 줬는지는 **값이 아니라 Visit** 으로만 알 수 있다
	// (환경에서 온 기본값과 같은 문자열일 수 있다).
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "codex-dir" {
			opt.codexDirSet = true
		}
	})

	// 기록 모드는 원천을 훑지 않는다 — 원천이 하나도 없어도 정상이다.
	if !opt.statusLine && len(sourcesOf(opt)) == 0 {
		return options{}, fmt.Errorf("훑을 원천이 없다 — -dir·-codex-dir·-gemini-dir·-antigravity-dir 중 하나를 정하라(-platform=%s)", opt.platform)
	}
	return opt, nil
}

// stdinReader 는 표준 입력이다. 테스트가 갈아 끼울 수 있도록 변수로 둔다
// (기본값은 진짜 os.Stdin 이라 호출부는 아무것도 몰라도 된다).
var stdinReader io.Reader = os.Stdin

// runStatusLine 은 Antigravity 의 statusLine 핸들러다.
//
// # 왜 이게 수집기 안에 있나
//
// Antigravity 는 토큰을 **statusLine 에만** 준다(훅에는 없다 — hooks.proto 로 확인됨).
// statusLine 은 렌더될 때 stdin 으로 JSON 을 받는 외부 명령이라, 누군가는 그 값을
// 붙잡아 디스크에 적어야 한다. 그 "누군가"를 셸 스크립트로 두면 jq 의존이 생기고
// 테스트할 수 없어서, 이미 배포되는 이 바이너리의 서브모드로 만들었다.
//
// # 규율: 절대 실패하지 않는다
//
// 이 코드는 **사용자의 상태줄 안에서** 돈다. 사용량 한 건을 놓치는 것보다 상태줄이
// 깨지거나 멈추는 게 훨씬 나쁘다. 그래서 무슨 일이 있어도 0 으로 끝내고, 에러는
// stderr 로만 흘린다(상태줄에는 stdout 만 나간다).
//
// 설치(scripts/install.sh 가 비파괴·멱등으로 한다):
//
//	~/.gemini/antigravity-cli/settings.json
//	{ "statusLine": { "type": "command", "command": "/path/to/usage-collector -antigravity-statusline" } }
//
// # 체이닝 — 남의 상태줄을 빼앗지 않는다
//
// statusLine 자리는 하나뿐이라 우리가 들어가면 원래 쓰던 상태줄이 사라진다. 그래서 설치기가
// 기존 명령을 AGY_PREV_STATUSLINE 으로 옮겨 두고, 여기서 **같은 JSON 을 그 명령에 먹여**
// 표준출력을 그대로 통과시킨다(우리 문구는 덧붙이지 않는다 — 화면은 그들 것이다).
// 체인이 없거나 실패·타임아웃·자기참조면 우리 요약으로 폴백한다(§ internal/antigravity/chain.go).
func runStatusLine(opt options, in io.Reader, stdout, stderr *os.File) int {
	if opt.agyDir == "" {
		fmt.Fprintln(stdout, "")
		return 0
	}
	// stdin 은 **한 번밖에 못 읽는다.** 스풀 기록과 체인 명령이 같은 바이트를 봐야 하므로
	// 먼저 통째로 읽어 들고, 양쪽에 각각 먹인다(스트림을 두 번 쓰면 뒤쪽이 빈 입력을 받는다).
	raw := antigravity.ReadStatusLineInput(in)

	s, changed, recErr := antigravity.RecordStatusLine(opt.agyDir, bytes.NewReader(raw), time.Now())
	if recErr != nil {
		// 기록 실패는 상태줄을 죽일 이유가 못 된다 — 진단만 stderr 로 흘리고 계속 간다.
		fmt.Fprintf(stderr, "antigravity 스풀 기록 실패(무시하고 계속): %v\n", recErr)
	}
	_ = changed

	// 기록이 실패했어도 체인은 시도한다 — 사용자 화면은 우리 사정과 무관하다.
	self, _ := os.Executable()
	if out, ok := antigravity.ChainPrev(os.Getenv(antigravity.PrevStatusLineEnv), self, raw); ok {
		stdout.Write(out) //nolint:errcheck // 상태줄에서 쓰기 실패에 할 수 있는 일이 없다
		return 0
	}

	if recErr != nil {
		fmt.Fprintln(stdout, "")
		return 0
	}
	fmt.Fprintln(stdout, statusLineText(s))
	return 0
}

// statusLineText 는 사용자가 실제로 보게 되는 한 줄이다.
//
// 이 모드를 켜면 원래 쓰던 상태줄을 잃는다. 그래서 최소한 "지금 이 대화가 얼마나
// 썼는지"는 보여준다 — 그게 이 수집기가 관측하는 값 그 자체이기도 하다.
func statusLineText(s antigravity.Spool) string {
	if s.ConversationID == "" {
		return ""
	}
	in, out, cache := s.Totals()
	model := s.Model
	if model == "" {
		model = "?"
	}
	return fmt.Sprintf("%s · %s in / %s out%s · %d턴",
		model, humanTokens(in), humanTokens(out), cacheNote(cache), s.Invocations)
}

func cacheNote(cache int64) string {
	if cache <= 0 {
		return ""
	}
	return " / " + humanTokens(cache) + " cache"
}

// humanTokens 는 17283 → "17.3k" 처럼 줄인다(상태줄은 폭이 좁다).
func humanTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// fileRef 는 파일 경로와 그 stat 이다(증분 판정·지문 기록에 쓴다).
type fileRef struct {
	path string
	info os.FileInfo
}

// discover 는 dir 아래 모든 *.jsonl 을 찾아 key(경로) 로 묶는다.
//
// `.zst` 는 **조용히 건너뛰지 않고 경고를 남긴다.** Codex 는 mtime 이 7일 지난 롤아웃을
// 압축할 수 있는데, 그때 그 세션이 소리 없이 사라지면 "왜 지난주 사용량이 줄었지?"를
// 아무도 추적할 수 없다. 압축 해제는 후속 과제이고, 지금은 침묵만 없앤다.
func discover(dir string, match func(path string) bool, key func(path string) string, stderr *os.File) (map[string][]fileRef, error) {
	groups := map[string][]fileRef{}
	compressed := 0
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // 못 읽는 하위 트리는 건너뛴다 — 한 디렉터리 권한 문제로 전체를 멈추지 않는다
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".zst") {
			compressed++
			return nil
		}
		if !match(path) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		k := key(path)
		groups[k] = append(groups[k], fileRef{path: path, info: info})
		return nil
	})
	if compressed > 0 {
		fmt.Fprintf(stderr, "  ⚠ 압축된 롤아웃 %d개를 건너뛴다(.zst 해제 미지원 — 그만큼 사용량이 빠진다): %s\n",
			compressed, dir)
	}
	return groups, err
}

func sortedStems(groups map[string][]fileRef) []string {
	stems := make([]string, 0, len(groups))
	for s := range groups {
		stems = append(stems, s)
	}
	// 최근 수정 세션이 앞에 오게 정렬 — -limit 이 "최근 N개"를 고를 수 있도록.
	sort.Slice(stems, func(i, j int) bool {
		return latestMod(groups[stems[i]]) > latestMod(groups[stems[j]])
	})
	return stems
}

func latestMod(fs []fileRef) int64 {
	var m int64
	for _, f := range fs {
		if n := f.info.ModTime().UnixNano(); n > m {
			m = n
		}
	}
	return m
}

func changedStems(stems []string, groups map[string][]fileRef, st *state.State, all bool) []string {
	out := make([]string, 0, len(stems))
	for _, stem := range stems {
		if all {
			out = append(out, stem)
			continue
		}
		for _, f := range groups[stem] {
			if st.Changed(f.path, f.info) {
				out = append(out, stem)
				break
			}
		}
	}
	return out
}

func limitNote(opt options) string {
	if opt.limit > 0 {
		return fmt.Sprintf(" (-limit %d 적용)", opt.limit)
	}
	return ""
}

// ── 기본값 헬퍼 ───────────────────────────────────────────────────────────────

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func intakeToken() string {
	if v := strings.TrimSpace(os.Getenv("USAGE_INTAKE_TOKEN")); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("USAGE_ADMIN_TOKEN"))
}

/*
 * defaultDir 는 Claude 트랜스크립트 디렉터리다.
 *
 * ⚠ **CLAUDE_CONFIG_DIR 을 보는 것이 이 함수의 핵심이다.** Claude Code 는 자기 설정 디렉터리를
 *   그 변수로 옮길 수 있고(claude 바이너리가 그 이름을 읽는다 — 실측), 옮기면 트랜스크립트도
 *   `<그 경로>/projects` 로 따라간다. `~/.claude/projects` 만 보면 그 사람의 사용량이 통째로
 *   빠지는데 **거부도 경고도 없다.**
 *
 *   같은 함정을 Codex 에서 실제로 맞았다(defaultCodexDir 의 주석 — CODEX_HOME 을 안 봐서
 *   세션 11개가 조용히 빠졌다). 원천마다 그 도구 자신이 쓰는 변수를 존중하는 것이 규율이다.
 *
 * 우선순위: 명시적 경로 > 도구의 설정 디렉터리 > 기본 홈.
 */
func defaultDir() string {
	if v := strings.TrimSpace(os.Getenv("CLAUDE_PROJECTS_DIR")); v != "" {
		return v
	}
	// CLAUDE_CONFIG_DIR 은 Claude Code 자신이 읽는 변수다 — 같은 이름을 그대로 존중한다.
	if v := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); v != "" {
		return filepath.Join(v, "projects")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".claude", "projects")
	}
	return ""
}

/*
 * codexProviders 는 `<codexDir>/../config.toml` 에서 provider 이름 → base_url 을 읽는다.
 *
 * 경로를 세션 디렉터리 기준으로 잡는 이유: `-codex-dir` 로 원천을 옮긴 사람(테스트·다른
 * 계정 검사)은 그 옆의 config 를 보게 되는 것이 맞다. 홈 디렉터리를 고정으로 쓰면 원천과
 * 설정이 서로 다른 설치를 가리킬 수 있다.
 *
 * 실패는 전부 조용하다 — 파일이 없거나 못 읽으면 빈 맵이고, 그러면 locality 가 판정되지
 * 않는다(없는 정보를 추측하지 않는다). config.toml 이 없는 것은 **정상**이다: 커스텀
 * provider 를 안 쓰면 만들 이유가 없다.
 *
 * ⚠ 이 파일에는 API 키가 들어 있을 수 있다. codexcfg 는 `base_url` **한 키만** 읽고 그
 *   값도 곧바로 locality 낱말로 줄어들어 페이로드에 실리지 않는다.
 */
func codexProviders(codexDir string) map[string]string {
	path := filepath.Join(filepath.Dir(strings.TrimRight(codexDir, `/\`)), "config.toml")
	f, err := os.Open(path)
	if err != nil {
		return map[string]string{}
	}
	defer f.Close() //nolint:errcheck // 읽기 전용 — 닫기 실패에 할 수 있는 일이 없다
	return codexcfg.Providers(f)
}

/*
 * codexDirs 는 훑을 Codex 롤아웃 디렉터리들이다 — **보통 하나, 홈이 옮겨져 있으면 둘.**
 *
 * 왜 여럿인가: 실측(2026-08-21) 이 머신은 `CODEX_HOME` 이 옮겨진 상태로 돌고, 그때 세션은 그
 * 홈에만 쌓인다. 그런데 같은 PC 에서 다른 터미널로 쓴 Codex 는 기본 `~/.codex` 에 쌓인다.
 * 하나만 훑으면 나머지가 통째로 빠지고 **경고가 없다.**
 *
 * opt.codexDir 이 기본값과 다르면(=사용자가 `-codex-dir` 로 명시했거나 환경이 옮겼거나) 그것을
 * 먼저 쓰고, 기본 홈이 그와 다르면 그것도 덧붙인다. 순서는 결정적이다(옮긴 홈 → 기본 홈).
 *
 * ⚠ **경로가 같으면 하나만 낸다.** 같은 디렉터리를 두 번 훑으면 파일 수·세션 수 로그가 두 배로
 *   보여 사람이 데이터가 늘었다고 오해한다(값은 UPSERT 라 안 틀리지만 로그가 거짓말을 한다).
 */
func codexDirs(opt options) []string {
	out := []string{opt.codexDir}
	// 명시적 지시는 넓히지 않는다 — `-codex-dir` 은 "이 디렉터리만 보라"는 뜻이다.
	if opt.codexDirSet {
		return out
	}
	// 환경이 옮긴 홈만 보고 있을 때, 기본 홈에도 옛 세션이 남아 있으면 그것까지 본다.
	if home, err := os.UserHomeDir(); err == nil {
		fallback := filepath.Join(home, ".codex", "sessions")
		if fallback != opt.codexDir {
			out = append(out, fallback)
		}
	}
	return out
}

/*
 * defaultCodexDir 는 Codex 롤아웃 디렉터리다.
 *
 * ⚠ **CODEX_HOME 을 보는 것이 이 함수의 핵심이다.** Codex 는 자기 홈을 그 환경변수로 옮길 수
 *   있고, 옮기는 도구가 실제로 있다(실측: Orca 가 `CODEX_HOME=~/.local/share/orca/
 *   codex-runtime-home/home` 으로 띄운다). 그때 세션은 **거기에만** 쌓이고 `~/.codex` 는
 *   옛 파일만 남는다.
 *
 *   이 한 줄이 없으면 수집기가 "세션 8개 전송 완료"라고 말하는 동안 그 홈의 11개가 통째로
 *   빠진다(2026-08-21 실측 수치다). 거부도 경고도 없는 침묵이라, 화면은 그 사람이 Codex 를
 *   덜 썼다고 말하게 된다 — 이 수집기가 가장 경계하는 실패 모양이다.
 *
 * 우선순위: 명시적 세션 경로 > Codex 홈 > 기본 홈. CODEX_SESSIONS_DIR 이 먼저인 이유는
 * 그것이 "이 디렉터리를 훑어라"는 직접 지시이고, CODEX_HOME 은 그보다 넓은 설정이기 때문이다.
 */
func defaultCodexDir() string {
	if v := strings.TrimSpace(os.Getenv("CODEX_SESSIONS_DIR")); v != "" {
		return v
	}
	// CODEX_HOME 은 Codex 자신이 읽는 변수다 — 같은 이름을 그대로 존중한다.
	if v := strings.TrimSpace(os.Getenv("CODEX_HOME")); v != "" {
		return filepath.Join(v, "sessions")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".codex", "sessions")
	}
	return ""
}

// defaultGeminiDir 는 Gemini **홈**이다(세션 디렉터리가 아니다 — 그건 그 아래 `tmp/`).
// 환경변수 이름은 Gemini CLI 자신이 쓰는 것과 같다(storage.ts 의 GEMINI_DIR 해석과 동일한 뿌리).
func defaultGeminiDir() string {
	if v := strings.TrimSpace(os.Getenv("GEMINI_HOME_DIR")); v != "" {
		return v
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".gemini")
	}
	return ""
}

// defaultAntigravityDir 는 **스풀** 디렉터리다(Antigravity 의 홈이 아니다).
//
// 왜 ~/.gemini 아래가 아닌가: 이건 우리가 만든 파일이지 Antigravity 의 것이 아니다.
// 남의 CLI 홈에 우리 상태를 섞어 두면 그쪽이 정리할 때 같이 지워지고, 무엇보다
// 이 수집기는 ~/.gemini 에 **쓰지 않는다**는 규율이 있다.
func defaultAntigravityDir() string {
	if v := strings.TrimSpace(os.Getenv("ANTIGRAVITY_SPOOL_DIR")); v != "" {
		return v
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "claude-usage", "antigravity")
	}
	return ""
}

// defaultAntigravityHome 은 Antigravity CLI 의 홈이다. **읽기 전용으로만** 쓴다
// (history.jsonl 하나를 읽어 slash·keyword·project 축을 채운다).
func defaultAntigravityHome() string {
	if v := strings.TrimSpace(os.Getenv("ANTIGRAVITY_HOME_DIR")); v != "" {
		return v
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".gemini", "antigravity-cli")
	}
	return ""
}

func defaultState() string {
	if v := strings.TrimSpace(os.Getenv("USAGE_COLLECTOR_STATE")); v != "" {
		return v
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".claude", "usage-collector-state.json")
	}
	return "usage-collector-state.json"
}

func defaultUser() string {
	if v := strings.TrimSpace(os.Getenv("USAGE_REPORT_USER")); v != "" {
		return v
	}
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return ""
}

func defaultMachine() string {
	if v := strings.TrimSpace(os.Getenv("USAGE_REPORT_MACHINE")); v != "" {
		return v
	}
	if h, err := os.Hostname(); err == nil {
		return h
	}
	return ""
}
