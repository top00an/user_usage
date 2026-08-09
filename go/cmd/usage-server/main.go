// usage-server — 사용량 대시보드의 실행 진입점.
//
// 이 파일이 하는 일은 배선뿐이다: 설정을 확정하고, 거부 조건이면 뜨지 않고, DB 를 열고,
// 스키마를 보장하고, 보존 정리기를 걸고, 핸들러를 붙여 듣는다. 판정 로직은 전부 패키지 안에 있다 —
// 여기서 조건문이 늘어나기 시작하면 그 판정은 테스트 없이 사는 코드가 된다.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/tscorp/user-usage/internal/config"
	"github.com/tscorp/user-usage/internal/db"
	"github.com/tscorp/user-usage/internal/httpapi"
	"github.com/tscorp/user-usage/internal/identity"
	"github.com/tscorp/user-usage/internal/org"
	"github.com/tscorp/user-usage/internal/store"
	"github.com/tscorp/user-usage/internal/tenant"
)

// rlsProbeTimeout — DB 롤 확인 상한. 터널이 안 붙었으면 여기서 오래 매달리지 않고
// "판정 불가"로 접는다(그건 거부가 아니다).
const rlsProbeTimeout = 5 * time.Second

// shutdownGrace — 열린 keep-alive 커넥션이 종료를 무한정 잡지 않게 한다.
const shutdownGrace = 3 * time.Second

func main() {
	// 프로비저닝 서브커맨드(멀티테넌트 운영 CLI). 없으면 서버를 띄운다.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "org", "key":
			os.Exit(provision(os.Args[1:]))
		}
	}
	os.Exit(run())
}

func run() int {
	cfg, errs := config.Read(config.Env())
	if len(errs) > 0 {
		fmt.Fprintln(os.Stderr, "사용량 대시보드 기동을 거부한다:")
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "  · %v\n", e)
		}
		fmt.Fprintln(os.Stderr, config.Help())
		return 2
	}

	/*
	 * ⚠ **이 배선이 빠지면 조용히 틀린다.** 안 걸면 pg 의 모든 쿼리가 `default` 테넌트로
	 * 흐른다 — 요청은 200 이고 화면도 정상으로 보이는데, remote 로 남의 DB 를 볼 때 그게
	 * 맞는다는 보장이 없다. db 는 tenant 를 import 할 수 없으므로(의존 방향이 반대다)
	 * 배선은 부팅이 진다.
	 */
	db.SetTenantResolver(tenant.From)

	ctx := context.Background()
	handle, err := db.Open(ctx, db.Options{Mode: cfg.Mode, DataDir: cfg.DataDir, URL: cfg.DatabaseURL})
	if err != nil {
		fmt.Fprintf(os.Stderr, "사용량 대시보드 기동 실패: DB 를 열 수 없다: %v\n", err)
		return 1
	}

	/*
	 * RLS 성립 전제 확인 — remote(=pg)에서만 의미가 있다(sqlite 는 단일 테넌트라 격리 대상이
	 * 없다). 위반이면 **여기서 끊는다.** 뜨고 나면 증상이 없어서 아무도 못 잡는 종류의 사고다:
	 * 앱이 SUPERUSER·BYPASSRLS 롤로 붙으면 FORCE ROW LEVEL SECURITY 조차 무시되어 전 테넌트의
	 * 행이 서로 보이는데, 요청은 200 이고 데이터도 잘 보인다(남의 것까지).
	 *
	 * ⚠ 거부 판정은 **Verdict.Rejects() 로만** 한다. `!v.OK` 로 분기하면 터널 미개통(판정 불가)
	 *   에서 부팅이 막혀, "터널을 먼저 뚫는다"는 정상 절차가 부팅 실패로 보인다.
	 */
	if cfg.Mode == "remote" {
		probeCtx, cancel := context.WithTimeout(ctx, rlsProbeTimeout)
		verdict := db.ProbeRLS(probeCtx, handle)
		cancel()
		switch rlsGate(verdict) {
		case rlsReject:
			fmt.Fprintln(os.Stderr, "사용량 대시보드 기동을 거부한다:")
			fmt.Fprintf(os.Stderr, "  · %s\n", db.Remedy(verdict.Message))
			/*
			 * 풀을 닫고 나간다. 프로브가 성공한 뒤 거부하는 경로라 유휴 커넥션이 남아 있고,
			 * 그것이 프로세스를 붙잡아 **거부 메시지를 찍고도 10초를 더 산다**(실측 10.5초 —
			 * pg 유휴 타임아웃). 사람이 보기엔 "에러를 냈는데 안 죽는다"이고, 재시작을 거는
			 * 감독 프로세스에겐 매 기동마다 10초 지연이다.
			 */
			_ = handle.Close()
			return 2
		case rlsWarn:
			fmt.Fprintf(os.Stderr, "  ⚠ DB 롤을 확인하지 못했다(%s) — RLS 격리 전제가 검증되지 않은 채 뜬다. "+
				"터널이 붙은 뒤 재기동해 확인하라.\n", verdict.Message)
		case rlsProceed:
			fmt.Println("  · DB 롤 확인 — 비-슈퍼·비-BYPASSRLS(RLS 테넌트 격리 성립)")
		}
	}
	defer func() { _ = handle.Close() }()

	/*
	 * 스키마 보장. sqlite 면 DDL(멱등), pg 면 조기 반환한다(스키마는 migrations 소유) —
	 * 즉 remote 모드의 부팅은 원격 DB 에 아무것도 쓰지 않는다.
	 */
	initCtx := tenant.With(ctx, cfg.Tenant)
	if err := store.Init(initCtx, handle); err != nil {
		fmt.Fprintf(os.Stderr, "사용량 대시보드 기동 실패: store 스키마 보장 실패: %v\n", err)
		return 1
	}
	if err := identity.Init(initCtx, handle); err != nil {
		fmt.Fprintf(os.Stderr, "사용량 대시보드 기동 실패: identity 스키마 보장 실패: %v\n", err)
		return 1
	}
	// 멀티테넌트(SaaS) 모드에서만 org·인제스트 키 스키마를 걸고 전역 핸들을 세팅한다.
	if cfg.MultiTenant {
		if err := org.Init(initCtx, handle); err != nil {
			fmt.Fprintf(os.Stderr, "사용량 대시보드 기동 실패: org 스키마 보장 실패: %v\n", err)
			return 1
		}
		fmt.Println("  · 멀티테넌트 모드 — 인테이크는 org 인제스트 키로 인증한다")
	}

	var stopRetention func()
	if cfg.ReadOnly {
		fmt.Println("  · remote 모드 — 읽기 전용(인테이크·귀속 쓰기·보존 정리 모두 미등록)")
		if cfg.IntakeToken != "" {
			fmt.Fprintln(os.Stderr, "  ⚠ USAGE_INTAKE_TOKEN 이 걸려 있지만 remote 모드에는 인테이크가 없다 — "+
				"이 토큰은 아무것도 열지 않는다.")
		}
	} else if cfg.KeywordRetentionDays != nil {
		stopRetention = startRetention(initCtx, *cfg.KeywordRetentionDays)
		fmt.Printf("  · 키워드 보존 정리: %d일\n", *cfg.KeywordRetentionDays)
	}
	if stopRetention != nil {
		defer stopRetention()
	}

	srv := &http.Server{
		Handler: httpapi.New(cfg),
		// 느린/멈춘 클라이언트가 커넥션을 무한정 잡지 않게 한다. 인테이크 본문은 최대 5MB 라
		// 넉넉하다.
		ReadHeaderTimeout: 10 * time.Second,
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "사용량 대시보드 기동 실패: %s 를 들을 수 없다: %v\n", addr, err)
		return 1
	}

	/*
	 * 기동 표식 — 하네스가 "이 자식이 그 포트를 잡았다"를 확인하는 근거다.
	 * **토큰은 절대 찍지 않는다.**
	 */
	fmt.Printf("usage-dashboard: http://%s  (mode=%s, tenant=%s)\n", addr, cfg.Mode, cfg.Tenant)
	fmt.Println("  · 브라우저에서 열고 토큰을 입력하면 두 탭(사용 추적·사용 관측)이 뜬다.")
	// ⚠ cwd 가 레포 루트가 아니면 단가가 조용히 시드로 떨어진다. 어느 파일을 봤는지 남긴다.
	fmt.Printf("  · 단가표: %s (없으면 시드 단가표를 쓴다)\n", cfg.ConfigPath)
	if !cfg.ReadOnly {
		// 어느 자격으로 보고가 들어오는지는 운영자가 알아야 한다(토큰 값은 절대 찍지 않는다).
		if cfg.IntakeToken != "" {
			fmt.Println("  · 인테이크 자격: USAGE_INTAKE_TOKEN(보고 전용 — 조회 불가)")
		} else {
			fmt.Println("  · 인테이크 자격: USAGE_ADMIN_TOKEN 겸용 — 수집기에 배포하는 토큰이 곧 전원 열람 " +
				"토큰이다. USAGE_INTAKE_TOKEN 으로 분리하는 것을 권한다.")
		}
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(os.Stderr, "사용량 대시보드 서빙 실패: %v\n", err)
			return 1
		}
	case <-sig:
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}
	return 0
}
