package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/tscorp/user-usage/internal/db"
	"github.com/tscorp/user-usage/internal/org"
	"github.com/tscorp/user-usage/internal/store"
	"github.com/tscorp/user-usage/internal/tenant"
)

// provision 은 org·인제스트 키 관리 서브커맨드다(멀티테넌트 SaaS 운영 CLI).
//
//	usage-server org  create --name "Acme"     org+tenant 생성 → id 출력
//	usage-server org  list                       등록된 org 목록
//	usage-server key  issue  --org <id> [--user <u>]  인제스트 키 발급(평문 1회 출력)
//	usage-server key  revoke --key <plaintext>   키 해지
//
// DB 는 서버와 같은 env 를 본다(USAGE_DB_MODE·DATABASE_URL·USAGE_DATA_DIR). ADMIN 토큰 게이트를
// 거치지 않는 이유: 이건 배포 호스트에서 운영자가 직접 부르는 명령이고, DB 접근 자체가 권한이다.
const provisionUsage = `usage-server 프로비저닝:
  org create --name <이름>       org+tenant 생성
  org list                       org 목록
  key issue --org <org-id> [--user <u>]  인제스트 키 발급(평문 1회 출력 · --user 면 그 사람에 묶임)
  key revoke --key <평문키>       키 해지
  team assign --user <u> --team <t>   사용자를 팀에 배정
  team list                      팀 멤버십 목록
  member issue --user <u>        개인 열람 토큰 발급(자기 데이터만·평문 1회 출력)
  member list                    개인 토큰 목록
  member revoke --token <t>      개인 토큰 해지
  user add -tenant <t> -username <u> -role <admin|member> [-password <p>]  사람 계정(ID/PW 로그인) 생성
  cleanup placeholder-models [--apply]  저장된 자리표시자 모델 라벨(<synthetic> 등) 정리(기본 dry-run)`

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
	case "member":
		return memberCmd(ctx, d, os.Stdout, args[1:])
	case "user":
		// 사람 계정(ID/PW). -tenant 플래그로 테넌트를 직접 정하므로 provision 의 기본 ctx 를 쓰지 않는다.
		return userCmd(d, os.Stdout, args[1:])
	case "cleanup":
		// 저장된 데이터 정리. **자동 실행 경로가 아니다** — 사람이 명시적으로 부르는 자리이고,
		// 기본값은 dry-run 이다(maintenance.go).
		return cleanupCmd(ctx, d, os.Stdout, args[1:])
	default:
		fmt.Fprintln(os.Stderr, provisionUsage)
		return 2
	}
}

// memberCmd — 개인 열람 토큰(RBAC) 관리. store 를 쓰므로 store.Init 선행.
func memberCmd(ctx context.Context, d db.DB, out io.Writer, args []string) int {
	if err := store.Init(ctx, d); err != nil {
		fmt.Fprintf(os.Stderr, "member: store 초기화 실패: %v\n", err)
		return 1
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, provisionUsage)
		return 2
	}
	switch args[0] {
	case "issue":
		fs := flag.NewFlagSet("member issue", flag.ContinueOnError)
		user := fs.String("user", "", "사용자명(필수)")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if *user == "" {
			fmt.Fprintln(os.Stderr, "member issue: --user 가 필요하다")
			return 2
		}
		tok, err := store.IssueMemberToken(ctx, *user)
		if err != nil {
			fmt.Fprintf(os.Stderr, "member issue 실패: %v\n", err)
			return 1
		}
		// 평문은 여기서 한 번만 보인다(저장은 해시).
		fmt.Fprintf(out, "%s\n", tok)
		return 0
	case "list":
		toks, err := store.ListMemberTokens(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "member list 실패: %v\n", err)
			return 1
		}
		if len(toks) == 0 {
			fmt.Fprintln(out, "(발급된 개인 토큰 없음)")
			return 0
		}
		for _, m := range toks {
			state := "active"
			if m.Revoked {
				state = "revoked"
			}
			fmt.Fprintf(out, "%s\t%s\n", m.Username, state)
		}
		return 0
	case "revoke":
		fs := flag.NewFlagSet("member revoke", flag.ContinueOnError)
		tok := fs.String("token", "", "평문 개인 토큰(필수)")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if *tok == "" {
			fmt.Fprintln(os.Stderr, "member revoke: --token 이 필요하다")
			return 2
		}
		if err := store.RevokeMemberToken(ctx, *tok); err != nil {
			fmt.Fprintf(os.Stderr, "member revoke 실패: %v\n", err)
			return 1
		}
		fmt.Fprintln(out, "해지됨")
		return 0
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

/*
 * requireUserForKey 는 키를 사람에게 묶기 **전에** 그 계정이 실재하는지 본다. 통과면 0,
 * 거부면 종료코드(!=0)를 돌려주고 사유를 stderr 로 낸다(호출부가 그대로 반환한다).
 *
 * tenant 를 org 에서 다시 읽는 이유: auth_users 는 tenant 로 격리되지만(pg RLS · 0034)
 * orgs·ingest_keys 는 아니다(0038 의 주석이 단일 출처). CLI 의 기본 ctx tenant(default)로
 * 조회하면 멀티테넌트 호스트에서 **엉뚱한 테넌트의 명부**를 보게 된다 — 실재하는 사람이
 * 없다고 거부되거나, 남의 테넌트에 있는 이름이 통과한다.
 */
func requireUserForKey(ctx context.Context, d db.DB, orgID, username string) int {
	tnt, ok, err := orgTenant(ctx, d, orgID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "key issue 실패: %v\n", err)
		return 1
	}
	if !ok {
		fmt.Fprintf(os.Stderr, "key issue: 알 수 없는 org %q — `org list` 로 id 를 확인해라\n", orgID)
		return 1
	}
	utx := tenant.With(ctx, tnt)
	if err := store.Init(utx, d); err != nil {
		fmt.Fprintf(os.Stderr, "key issue: store 초기화 실패: %v\n", err)
		return 1
	}
	_, found, err := store.GetUser(utx, username)
	if err != nil {
		fmt.Fprintf(os.Stderr, "key issue 실패: %v\n", err)
		return 1
	}
	if !found {
		// 조용히 만들지 않는다. 무엇이 틀렸고 다음에 뭘 해야 하는지까지 말한다.
		fmt.Fprintf(os.Stderr,
			"key issue: 사용자 %q 가 tenant %q 에 없다 — 오타이거나 계정이 아직 없다.\n"+
				"  먼저 계정을 만들고 다시 발급해라:\n"+
				"    usage-server user add -tenant %s -username %s -role member\n"+
				"  (없는 이름에 묶은 키의 보고는 영영 아무에게도 귀속되지 않는다)\n",
			username, tnt, tnt, username)
		return 1
	}
	return 0
}

// orgTenant 는 org 의 tenant_id 를 읽는다. CreateOrg 는 tenant_id 를 org id 와 같게 두지만
// ensureOrgForTenant 로 생긴 org 는 둘이 다르다 — 그래서 추측하지 않고 값을 읽는다.
func orgTenant(ctx context.Context, d db.DB, orgID string) (string, bool, error) {
	orgs, err := org.ListOrgs(ctx, d)
	if err != nil {
		return "", false, err
	}
	for _, o := range orgs {
		if o.ID == orgID {
			return o.TenantID, true, nil
		}
	}
	return "", false, nil
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
		user := fs.String("user", "", "이 키를 묶을 사용자명(생략하면 종전대로 org 공용 키)")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if *orgID == "" {
			fmt.Fprintln(os.Stderr, "key issue: --org 가 필요하다")
			return 2
		}
		/*
		 * --user 는 **선택**이다. 생략하면 종전과 같은 org 공용 키다(컬럼은 NULL) — 이미 배포된
		 * 호출 형태가 그대로 돌아야 한다.
		 *
		 * 주면 그 사람에게 묶인다. 묶인 키로 들어온 보고는 인테이크가 payload.user 도 machine
		 * 매핑도 보지 않고 이 이름으로 귀속한다(귀속 우선순위 ①) — "그 사용자의 키를 실제로
		 * 갖고 있음"이 증명된 사실이기 때문이다.
		 *
		 * 그래서 **발급 전에** 계정이 있는지 확인한다. 없는 이름에 조용히 묶으면 오타 하나가
		 * 영원히 아무에게도 귀속되지 않는 키를 낳고, 그 보고는 화면에 유령 사용자로 쌓인다.
		 * 게다가 키는 평문을 다시 볼 수 없어 "잘못 발급했다"를 되돌리는 유일한 길이 해지다.
		 */
		owner := strings.TrimSpace(*user)
		if owner != "" {
			if rc := requireUserForKey(ctx, d, *orgID, owner); rc != 0 {
				return rc
			}
		}
		plain, err := org.IssueKeyFor(ctx, d, *orgID, owner)
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
