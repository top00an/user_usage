package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/tscorp/user-usage/internal/db"
	"github.com/tscorp/user-usage/internal/org"
	"github.com/tscorp/user-usage/internal/store"
	"github.com/tscorp/user-usage/internal/tenant"
)

// provision 은 org·인제스트 키 관리 서브커맨드다(멀티테넌트 SaaS 운영 CLI).
//
//	usage-server org  create --name "Acme"     org+tenant 생성 → id 출력
//	usage-server org  list                       등록된 org 목록
//	usage-server key  issue  --org <id>          인제스트 키 발급(평문 1회 출력)
//	usage-server key  revoke --key <plaintext>   키 해지
//
// DB 는 서버와 같은 env 를 본다(USAGE_DB_MODE·DATABASE_URL·USAGE_DATA_DIR). ADMIN 토큰 게이트를
// 거치지 않는 이유: 이건 배포 호스트에서 운영자가 직접 부르는 명령이고, DB 접근 자체가 권한이다.
const provisionUsage = `usage-server 프로비저닝:
  org create --name <이름>       org+tenant 생성
  org list                       org 목록
  key issue --org <org-id>       인제스트 키 발급(평문 1회 출력)
  key revoke --key <평문키>       키 해지
  team assign --user <u> --team <t>   사용자를 팀에 배정
  team list                      팀 멤버십 목록`

func provision(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, provisionUsage)
		return 2
	}
	d, err := openProvisionDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "provision: DB 를 열 수 없다: %v\n", err)
		return 1
	}
	defer func() { _ = d.Close() }()

	ctx := tenant.With(context.Background(), tenant.Default)
	if err := org.Init(ctx, d); err != nil {
		fmt.Fprintf(os.Stderr, "provision: org 스키마 보장 실패: %v\n", err)
		return 1
	}

	switch args[0] {
	case "org":
		return orgCmd(ctx, d, os.Stdout, args[1:])
	case "key":
		return keyCmd(ctx, d, os.Stdout, args[1:])
	case "team":
		return teamCmd(ctx, d, os.Stdout, args[1:])
	default:
		fmt.Fprintln(os.Stderr, provisionUsage)
		return 2
	}
}

// teamCmd — 팀 멤버십 관리. store 를 쓰므로 store.Init 이 선행돼야 한다(provision 에서 호출).
func teamCmd(ctx context.Context, d db.DB, out io.Writer, args []string) int {
	if err := store.Init(ctx, d); err != nil {
		fmt.Fprintf(os.Stderr, "team: store 초기화 실패: %v\n", err)
		return 1
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, provisionUsage)
		return 2
	}
	switch args[0] {
	case "assign":
		fs := flag.NewFlagSet("team assign", flag.ContinueOnError)
		user := fs.String("user", "", "사용자명(필수)")
		team := fs.String("team", "", "팀 이름(필수)")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if *user == "" || *team == "" {
			fmt.Fprintln(os.Stderr, "team assign: --user 와 --team 이 필요하다")
			return 2
		}
		if err := store.AssignTeam(ctx, *user, *team); err != nil {
			fmt.Fprintf(os.Stderr, "team assign 실패: %v\n", err)
			return 1
		}
		fmt.Fprintf(out, "배정됨: %s → %s\n", *user, *team)
		return 0
	case "list":
		members, err := store.ListTeamMembers(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "team list 실패: %v\n", err)
			return 1
		}
		if len(members) == 0 {
			fmt.Fprintln(out, "(배정된 멤버 없음)")
			return 0
		}
		for _, m := range members {
			fmt.Fprintf(out, "%s\t%s\n", m.Team, m.Username)
		}
		return 0
	default:
		fmt.Fprintln(os.Stderr, provisionUsage)
		return 2
	}
}

func openProvisionDB() (db.DB, error) {
	// pg 경로가 tenant 를 주입하려면 서버와 같은 배선이 필요하다.
	db.SetTenantResolver(tenant.From)
	mode := os.Getenv("USAGE_DB_MODE")
	if mode == "" {
		mode = "local"
	}
	return db.Open(context.Background(), db.Options{
		Mode:    mode,
		DataDir: os.Getenv("USAGE_DATA_DIR"),
		URL:     os.Getenv("DATABASE_URL"),
	})
}

func orgCmd(ctx context.Context, d db.DB, out io.Writer, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, provisionUsage)
		return 2
	}
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("org create", flag.ContinueOnError)
		name := fs.String("name", "", "org 이름(필수)")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if *name == "" {
			fmt.Fprintln(os.Stderr, "org create: --name 이 필요하다")
			return 2
		}
		o, err := org.CreateOrg(ctx, d, *name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "org create 실패: %v\n", err)
			return 1
		}
		fmt.Fprintf(out, "org 생성됨: id=%s tenant=%s name=%q\n", o.ID, o.TenantID, o.Name)
		return 0
	case "list":
		orgs, err := org.ListOrgs(ctx, d)
		if err != nil {
			fmt.Fprintf(os.Stderr, "org list 실패: %v\n", err)
			return 1
		}
		if len(orgs) == 0 {
			fmt.Fprintln(out, "(org 없음)")
			return 0
		}
		for _, o := range orgs {
			fmt.Fprintf(out, "%s\t%s\t%s\n", o.ID, o.Status, o.Name)
		}
		return 0
	default:
		fmt.Fprintln(os.Stderr, provisionUsage)
		return 2
	}
}

func keyCmd(ctx context.Context, d db.DB, out io.Writer, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, provisionUsage)
		return 2
	}
	switch args[0] {
	case "issue":
		fs := flag.NewFlagSet("key issue", flag.ContinueOnError)
		orgID := fs.String("org", "", "org id(필수)")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if *orgID == "" {
			fmt.Fprintln(os.Stderr, "key issue: --org 가 필요하다")
			return 2
		}
		plain, err := org.IssueKey(ctx, d, *orgID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "key issue 실패: %v\n", err)
			return 1
		}
		// 평문은 **여기서 한 번만** 보인다 — 저장은 해시만 한다. 다시 못 본다.
		fmt.Fprintf(out, "%s\n", plain)
		return 0
	case "revoke":
		fs := flag.NewFlagSet("key revoke", flag.ContinueOnError)
		key := fs.String("key", "", "평문 인제스트 키(필수)")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if *key == "" {
			fmt.Fprintln(os.Stderr, "key revoke: --key 가 필요하다")
			return 2
		}
		if err := org.RevokeKey(ctx, d, *key); err != nil {
			fmt.Fprintf(os.Stderr, "key revoke 실패: %v\n", err)
			return 1
		}
		fmt.Fprintln(out, "해지됨")
		return 0
	default:
		fmt.Fprintln(os.Stderr, provisionUsage)
		return 2
	}
}
