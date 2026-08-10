// usage-collector — 팀원 PC 의 코딩 에이전트 트랜스크립트를 훑어 사용량을 서버로 보고한다.
//
// 원천은 둘이다:
//
//	claude  ~/.claude/projects/<슬러그>/<sessionId>.jsonl
//	codex   ~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl
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

	"github.com/tscorp/user-usage/collector/internal/codex"
	"github.com/tscorp/user-usage/collector/internal/payload"
	"github.com/tscorp/user-usage/collector/internal/sender"
	"github.com/tscorp/user-usage/collector/internal/state"
	"github.com/tscorp/user-usage/collector/internal/transcript"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

type options struct {
	dir      string
	codexDir string
	platform string
	server   string
	token    string
	state    string
	user     string
	machine  string
	limit    int
	dryRun   bool
	all      bool
}

// aggregator 는 원천별 파서의 공통 모양이다. transcript(Claude)·codex 둘 다 이걸 만족하고,
// 이 파일은 어느 쪽인지 몰라도 된다.
type aggregator interface {
	AddFile(fallbackID string, r io.Reader) error
	Sessions() []payload.Session
}

// source 는 훑을 원천 하나다.
type source struct {
	platform string
	dir      string
	// key 는 파일 경로 → 세션 그룹 키. Claude 는 파일명 stem 이 곧 sessionId 이고,
	// Codex 는 롤아웃 파일명 꼬리의 uuid 다.
	key    func(path string) string
	newAgg func() aggregator
}

// sourcesOf 는 옵션에서 실제로 훑을 원천을 고른다. 디렉터리가 비어 있으면(플래그로 ""를 주면)
// 그 원천은 아예 빠진다 — 개별 비활성화가 그 방식이다.
func sourcesOf(opt options) []source {
	var out []source
	if wants(opt.platform, "claude") && opt.dir != "" {
		out = append(out, source{
			platform: "claude",
			dir:      opt.dir,
			key:      func(p string) string { return strings.TrimSuffix(filepath.Base(p), ".jsonl") },
			newAgg:   func() aggregator { return transcript.New() },
		})
	}
	if wants(opt.platform, "codex") && opt.codexDir != "" {
		out = append(out, source{
			platform: codex.Platform,
			dir:      opt.codexDir,
			key:      codex.SessionIDFromPath,
			newAgg:   func() aggregator { return codex.New() },
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
		groups, err := discover(src.dir, src.key, stderr)
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
				if err := agg.AddFile(stem, fh); err != nil {
					fmt.Fprintf(stderr, "  ⚠ 읽기 중단(%s): %v\n", f.path, err)
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
	fs.StringVar(&opt.platform, "platform", "all", "훑을 원천: all|claude|codex")
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
	case "all", "claude", codex.Platform:
	default:
		return options{}, fmt.Errorf("-platform 은 all|claude|codex 중 하나다(받은 값: %q)", opt.platform)
	}
	if len(sourcesOf(opt)) == 0 {
		return options{}, fmt.Errorf("훑을 원천이 없다 — -dir 또는 -codex-dir 을 정하라(-platform=%s)", opt.platform)
	}
	return opt, nil
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
func discover(dir string, key func(path string) string, stderr *os.File) (map[string][]fileRef, error) {
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
		if !strings.HasSuffix(path, ".jsonl") {
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

func defaultDir() string {
	if v := strings.TrimSpace(os.Getenv("CLAUDE_PROJECTS_DIR")); v != "" {
		return v
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".claude", "projects")
	}
	return ""
}

func defaultCodexDir() string {
	if v := strings.TrimSpace(os.Getenv("CODEX_SESSIONS_DIR")); v != "" {
		return v
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".codex", "sessions")
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
