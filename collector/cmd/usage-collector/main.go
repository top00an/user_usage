// usage-collector — 팀원 PC 의 Claude Code 트랜스크립트를 훑어 사용량을 서버로 보고한다.
//
// 흐름은 넷이다: 디렉터리에서 세션 파일을 찾고 → 증분(바뀐 세션만) 골라 파싱·매핑하고 →
// `POST /api/usage` 로 절대값을 보내고 → 보낸 세션의 지문을 체크포인트에 남긴다.
//
// 재실행은 언제나 안전하다. 서버가 session_id 절대값으로 UPSERT 하므로, 체크포인트를 지우고
// 전량을 다시 보내도 값이 부풀지 않는다(멱등성은 서버 키가 진다).
//
// 판정 로직은 전부 internal 패키지 안에 있다 — 이 파일이 하는 일은 배선과 설정뿐이다.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/tscorp/user-usage/collector/internal/payload"
	"github.com/tscorp/user-usage/collector/internal/sender"
	"github.com/tscorp/user-usage/collector/internal/state"
	"github.com/tscorp/user-usage/collector/internal/transcript"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

type options struct {
	dir     string
	server  string
	token   string
	state   string
	user    string
	machine string
	limit   int
	dryRun  bool
	all     bool
}

func run(args []string, stdout, stderr *os.File) int {
	opt, err := parseFlags(args, stderr)
	if err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		fmt.Fprintf(stderr, "설정 오류: %v\n", err)
		return 2
	}

	// 세션 파일 그룹: stem(=sessionId) → 파일 목록. 대개 세션당 파일 하나지만, 재개된
	// 세션은 여럿일 수 있어 그룹으로 든다.
	groups, err := discover(opt.dir)
	if err != nil {
		fmt.Fprintf(stderr, "트랜스크립트 디렉터리를 훑지 못했다(%s): %v\n", opt.dir, err)
		return 1
	}
	if len(groups) == 0 {
		fmt.Fprintf(stderr, "세션 파일(*.jsonl)이 없다: %s\n", opt.dir)
		return 0
	}

	st, err := state.Load(opt.state)
	if err != nil {
		fmt.Fprintf(stderr, "체크포인트를 읽지 못했다(%s): %v\n", opt.state, err)
		return 1
	}

	// 증분: 파일 중 하나라도 바뀐 세션만 고른다(-all 이면 전부).
	stems := sortedStems(groups)
	changed := changedStems(stems, groups, st, opt.all)
	if opt.limit > 0 && len(changed) > opt.limit {
		changed = changed[:opt.limit]
	}

	// 진행 로그는 전부 stderr 로 — stdout 은 -dry-run 의 페이로드(기계가 읽는 출력)만 태운다.
	fmt.Fprintf(stderr, "세션 파일 %d개 · 바뀐 세션 %d개%s\n",
		len(groups), len(changed), limitNote(opt))
	if len(changed) == 0 {
		fmt.Fprintln(stderr, "보낼 것이 없다(모든 세션이 마지막 전송 이후 그대로다).")
		return 0
	}

	// 파싱·매핑 — 바뀐 세션의 모든 파일을 하나의 누적기에 흘려 절대값을 낸다.
	agg := transcript.New()
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
		}
	}
	sessions := agg.Sessions()
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
	for _, stem := range changed {
		for _, f := range groups[stem] {
			st.Mark(f.path, f.info)
		}
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
	fs.StringVar(&opt.dir, "dir", defaultDir(), "트랜스크립트 디렉터리(기본 ~/.claude/projects)")
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
	if opt.dir == "" {
		return options{}, fmt.Errorf("트랜스크립트 디렉터리(-dir)를 정할 수 없다")
	}
	return opt, nil
}

// fileRef 는 파일 경로와 그 stat 이다(증분 판정·지문 기록에 쓴다).
type fileRef struct {
	path string
	info os.FileInfo
}

// discover 는 dir 아래 모든 *.jsonl 을 찾아 stem(파일명에서 .jsonl 제거 = sessionId)으로 묶는다.
func discover(dir string) (map[string][]fileRef, error) {
	groups := map[string][]fileRef{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // 못 읽는 하위 트리는 건너뛴다 — 한 디렉터리 권한 문제로 전체를 멈추지 않는다
		}
		if d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		stem := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		groups[stem] = append(groups[stem], fileRef{path: path, info: info})
		return nil
	})
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
