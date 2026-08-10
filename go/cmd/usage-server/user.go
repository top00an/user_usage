package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/tscorp/user-usage/internal/db"
	"github.com/tscorp/user-usage/internal/store"
	"github.com/tscorp/user-usage/internal/tenant"
)

// userCmd — 사람 계정(ID/PW 로그인) 관리 서브커맨드. 마이그레이션이 적용된 DB 가 필요하다
// (pg 는 migrations/pg/0034 이 auth_users·auth_sessions 를 소유). 최초 관리자·추가 사용자 생성용.
//
//	usage-server user add -tenant <t> -username <u> -role <admin|member> [-password <p>]
const userUsage = `usage-server user:
  user add -tenant <t> -username <u> -role <admin|member> [-password <p>]
      비밀번호를 -password 로 주지 않으면 프롬프트로 입력받는다(터미널이면 에코 없음).`

func userCmd(d db.DB, out io.Writer, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, userUsage)
		return 2
	}
	switch args[0] {
	case "add":
		return userAdd(d, out, args[1:])
	default:
		fmt.Fprintln(os.Stderr, userUsage)
		return 2
	}
}

func userAdd(d db.DB, out io.Writer, args []string) int {
	fs := flag.NewFlagSet("user add", flag.ContinueOnError)
	tenantID := fs.String("tenant", tenant.Default, "테넌트(기본 default)")
	username := fs.String("username", "", "아이디(필수)")
	role := fs.String("role", "", "역할 admin|member(필수)")
	password := fs.String("password", "", "비밀번호(생략하면 프롬프트로 입력)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *username == "" || *role == "" {
		fmt.Fprintln(os.Stderr, "user add: -username 과 -role 이 필요하다")
		return 2
	}
	if *role != "admin" && *role != "member" {
		fmt.Fprintln(os.Stderr, "user add: -role 은 admin|member 중 하나여야 한다")
		return 2
	}
	pw := *password
	if pw == "" {
		p, err := readPassword()
		if err != nil {
			fmt.Fprintf(os.Stderr, "user add: 비밀번호 입력 실패: %v\n", err)
			return 1
		}
		pw = p
	}
	if pw == "" {
		fmt.Fprintln(os.Stderr, "user add: 비밀번호가 비어 있다")
		return 2
	}

	ctx := tenant.With(context.Background(), *tenantID)
	if err := store.Init(ctx, d); err != nil {
		fmt.Fprintf(os.Stderr, "user add: store 초기화 실패: %v\n", err)
		return 1
	}
	hash, err := store.HashPassword(pw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "user add: %v\n", err)
		return 1
	}
	if err := store.CreateUser(ctx, *username, *role, hash); err != nil {
		fmt.Fprintf(os.Stderr, "user add 실패: %v\n", err)
		return 1
	}
	// 비밀번호는 절대 출력하지 않는다.
	fmt.Fprintf(out, "사용자 생성됨: tenant=%s username=%s role=%s\n", *tenantID, *username, *role)
	return 0
}

// readPassword 는 프롬프트로 비밀번호를 받는다. 터미널이면 에코 없이(x/term), 파이프 입력이면
// 한 줄 읽는다(테스트·스크립트 경로).
func readPassword() (string, error) {
	fmt.Fprint(os.Stderr, "비밀번호: ")
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
