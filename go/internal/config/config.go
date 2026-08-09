// Package config 는 부팅 설정과 **거부 게이트**다.
//
// 순수 함수만 둔다(부작용 없음) — 부팅 거부 판단을 서버 기동 없이 검증할 수 있게. 그래서
// Read 는 os.Getenv 를 부르지 않고 env 맵을 인자로 받는다. 프로세스 환경을 그 맵으로 만드는
// 것은 Env() 하나가 하고, 그 함수만 부작용을 안다.
//
// ── 왜 게이트가 여기 있나 ──────────────────────────────────────────────────
// 여섯 가지 거부 조건은 전부 **안 걸면 조용한 사고**다. 서버는 뜨고 로그도 초록색이다:
//
//	토큰 없음        사람별 사용량·비용이 무인증으로 열린다. 옵션으로 두면 누군가는 반드시
//	                 토큰 없이 띄우고, 그때 아무 에러도 나지 않는다.
//	토큰 16자 미만   짧은 토큰은 인증이 아니라 설정 실수다. 인증으로 취급하면 게이트가
//	                 있다는 착각만 남는다.
//	두 토큰이 같음   분리한 것처럼 보이지만 보고 자격이 곧 열람 자격이다.
//	모드 오타        local 로 조용히 접으면 "로컬 파일에 쓰고 있었다"로 끝난다.
//	remote+URL 없음  붙을 곳이 없다.
//	bad port         서버는 뜨고 curl 도 200 인데 **브라우저에서만** 죽는다 — 로그도 테스트도
//	                 전부 초록색이라 이 레포에서 가장 진단하기 어려운 모양이다.
//
// 잘못된 설정은 **모아서** 돌려준다. 하나씩 알려주면 고치고 다시 띄우기를 반복하게 된다.
package config

import (
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tscorp/user-usage/internal/store"
)

const (
	// DefaultPort 는 **4190 이 아니다.**
	//
	// 처음 정한 값은 4190 이었는데 그 포트로는 화면이 뜨지 않는다: 4190(managesieve)은 WHATWG
	// Fetch 명세의 차단 포트 목록에 있고 Chrome·Firefox·undici 가 전부 구현한다. 브라우저는
	// 그 포트로의 이동을 ERR_UNSAFE_PORT 로 거부하고, 페이지가 어찌어찌 떠도 동일 출처 fetch 가
	// 통째로 막힌다(실측 2026-08-06: node fetch → `bad port`, 서버는 멀쩡히 응답하는데
	// 클라이언트가 안 보낸다). 그래서 기본을 옆 번호로 옮겼다.
	DefaultPort = 4191

	// DefaultHost 는 루프백이다 — 토큰이 있어도 LAN 에 자동 노출하지 않는다.
	DefaultHost = "127.0.0.1"

	// DefaultTenant 는 조회 테넌트의 기본값이다.
	DefaultTenant = "default"

	// MinTokenLen — 토큰 하한. 짧은 토큰은 인증이 아니라 설정 실수이고, 그것을 인증으로
	// 취급하면 게이트가 있다는 착각만 남는다.
	MinTokenLen = 16
)

// Modes 는 인정하는 DB 모드 전부다.
var Modes = []string{"local", "remote"}

/*
 * 브라우저가 거부하는 포트(WHATWG Fetch "bad ports").
 * 이 목록에 있는 포트로 띄우면 서버는 멀쩡히 뜨고 curl 도 통과하는데 **브라우저에서만**
 * 아무것도 안 된다. 그래서 부팅에서 끊는다 — 조회 도구라 대체 포트를 고르는 비용이 0 이다.
 */
var badPorts = map[int]bool{
	1: true, 7: true, 9: true, 11: true, 13: true, 15: true, 17: true, 19: true, 20: true,
	21: true, 22: true, 23: true, 25: true, 37: true, 42: true, 43: true, 53: true, 69: true,
	77: true, 79: true, 87: true, 95: true, 101: true, 102: true, 103: true, 104: true,
	109: true, 110: true, 111: true, 113: true, 115: true, 117: true, 119: true, 123: true,
	135: true, 137: true, 138: true, 139: true, 143: true, 161: true, 179: true, 389: true,
	427: true, 465: true, 512: true, 513: true, 514: true, 515: true, 526: true, 530: true,
	531: true, 532: true, 540: true, 548: true, 554: true, 556: true, 563: true, 636: true,
	989: true, 990: true, 993: true, 995: true, 1719: true, 1720: true, 1723: true, 2049: true,
	3659: true, 4045: true, 4190: true, 5060: true, 5061: true, 6000: true, 6566: true,
	6665: true, 6666: true, 6667: true, 6668: true, 6669: true, 6679: true, 6697: true,
	10080: true,
}

// IsBadPort 는 그 포트가 브라우저 차단 목록에 있는지 본다.
func IsBadPort(p int) bool { return badPorts[p] }

// BadPorts 는 차단 포트 전부를 오름차순으로 돌려준다(테스트·문서용).
func BadPorts() []int {
	out := make([]int, 0, len(badPorts))
	for p := range badPorts {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

// Config 는 확정된 부팅 설정이다.
type Config struct {
	Token       string // USAGE_ADMIN_TOKEN — 열람 자격
	IntakeToken string // USAGE_INTAKE_TOKEN — 보고 전용(빈 문자열이면 admin 이 겸한다)
	Mode        string // "local" | "remote"
	DatabaseURL string // remote 전용
	Port        int
	Host        string
	Tenant      string
	DataDir     string // local 모드 sqlite 디렉터리(빈 문자열이면 db 가 "data" 로 접는다)

	// ReadOnly 는 모드와 **별개 이름**으로 둔다. 호출부가 "원격이니까"가 아니라 "읽기 전용
	// 이니까"로 분기해야 나중에 모드가 늘어도 규칙이 흐려지지 않는다.
	ReadOnly bool

	// MultiTenant 는 SaaS 모드다(USAGE_MULTITENANT). 켜면 인테이크가 단일 토큰이 아니라
	// **org 별 인제스트 키**로 인증되고, 키가 해석한 tenant 가 요청에 실린다(RLS 격리).
	// 끄면(기본) 종전대로 단일 tenant·단일 토큰이라 골든 계약이 그대로 유지된다.
	// 진짜 격리는 pg(RLS)에서만 성립한다 — sqlite 는 단일 테넌트다.
	MultiTenant bool

	// KeywordRetentionDays 는 nil 이면 **무기한 보관**이다(정리기를 아예 띄우지 않는다).
	// 0 이 아니라 nil 인 이유: 0 은 "즉시 삭제"로 읽히고, 그건 여기서 낼 수 있는 값이 아니다.
	KeywordRetentionDays *int

	// ConfigPath 는 단가표(config.json)를 어디서 읽을지다. cost 패키지가 같은 규칙으로
	// 직접 읽으므로 여기 값은 **로그로 밝히기 위한 것**이다 — cwd 가 레포 루트가 아니면
	// 단가가 조용히 시드로 떨어지는데, 그 사실이 어디에도 안 남으면 아무도 모른다.
	ConfigPath string
}

// Env 는 프로세스 환경을 Read 가 받는 맵으로 만든다. **부작용을 아는 유일한 함수다.**
func Env() map[string]string {
	out := make(map[string]string, len(os.Environ()))
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 {
			out[kv[:i]] = kv[i+1:]
		}
	}
	return out
}

/*
 * Read 는 설정을 확정하고 **잘못된 것을 모아서** 돌려준다.
 *
 * env 를 고치지 않는다(순수). 반환 에러가 하나라도 있으면 호출부는 띄우지 말고 나가야 한다 —
 * "경고만 찍고 뜨기"는 이 게이트를 없애는 것과 같다.
 */
func Read(env map[string]string) (Config, []error) {
	get := func(k string) string { return strings.TrimSpace(env[k]) }
	var errs []error

	token := get("USAGE_ADMIN_TOKEN")
	if token == "" {
		errs = append(errs, errors.New("USAGE_ADMIN_TOKEN 이 없다 — 이 대시보드는 사람별 사용량·비용을 담고 있어 "+
			"무인증으로 띄우지 않는다(기본값 없음이 의도다)."))
	} else if n := utf8.RuneCountInString(token); n < MinTokenLen {
		errs = append(errs, fmt.Errorf("USAGE_ADMIN_TOKEN 이 너무 짧다(%d자) — 최소 %d자.", n, MinTokenLen))
	}

	/*
	 * 보고 전용 토큰(선택). 안 걸면 종전대로 admin 토큰 하나가 보고와 열람을 겸한다.
	 *
	 * admin 과 **같은 값이면 거부한다.** 그건 분리한 것처럼 보이지만 아무것도 분리되지 않은
	 * 상태이고, 이 레포에서 가장 비싼 사고 유형(게이트가 있다는 착각)이 정확히 그 모양이다.
	 */
	intakeToken := get("USAGE_INTAKE_TOKEN")
	if intakeToken != "" {
		if n := utf8.RuneCountInString(intakeToken); n < MinTokenLen {
			errs = append(errs, fmt.Errorf("USAGE_INTAKE_TOKEN 이 너무 짧다(%d자) — 최소 %d자.", n, MinTokenLen))
		} else if token != "" && intakeToken == token {
			errs = append(errs, errors.New("USAGE_INTAKE_TOKEN 이 USAGE_ADMIN_TOKEN 과 같다 — 분리한 것처럼 보이지만 "+
				"보고 자격이 곧 열람 자격이다. 다른 값을 쓰거나 아예 지워라."))
		}
	}

	mode := strings.ToLower(get("USAGE_DB_MODE"))
	if mode == "" {
		mode = "local"
	}
	if !known(mode) {
		errs = append(errs, fmt.Errorf("USAGE_DB_MODE 가 '%s' 다 — %s 중 하나여야 한다(오타를 local 로 접지 않는다).",
			mode, strings.Join(Modes, "|")))
	}

	databaseURL := get("DATABASE_URL")
	if mode == "remote" && databaseURL == "" {
		errs = append(errs, errors.New("remote 모드인데 DATABASE_URL 이 없다 — SSH 터널의 접속 문자열이 필요하다"+
			"(예: postgres://usage:…@127.0.0.1:15432/usage)."))
	}

	port := DefaultPort
	if raw := get("USAGE_PORT"); raw != "" {
		n, ok := portNumber(raw)
		if !ok {
			errs = append(errs, fmt.Errorf("USAGE_PORT 가 포트 번호가 아니다: %s", raw))
		} else {
			port = n
		}
	}
	if IsBadPort(port) {
		errs = append(errs, fmt.Errorf("USAGE_PORT=%d 는 브라우저가 차단하는 포트다(WHATWG Fetch bad ports) — "+
			"서버는 뜨고 curl 도 통과하지만 브라우저에서는 이동도 fetch 도 막혀 화면이 통째로 죽는다. "+
			"다른 번호를 쓰라(기본 %d).", port, DefaultPort))
	}

	host := get("USAGE_HOST")
	if host == "" {
		host = DefaultHost
	}
	tenant := get("USAGE_TENANT")
	if tenant == "" {
		tenant = DefaultTenant
	}

	cfg := Config{
		Token:                token,
		IntakeToken:          intakeToken,
		Mode:                 mode,
		DatabaseURL:          databaseURL,
		Port:                 port,
		Host:                 host,
		Tenant:               tenant,
		DataDir:              get("USAGE_DATA_DIR"),
		ReadOnly:             mode == "remote",
		MultiTenant:          isTruthy(get("USAGE_MULTITENANT")),
		KeywordRetentionDays: keywordRetention(get("USAGE_KEYWORD_RETENTION_DAYS")),
		ConfigPath:           configPath(get("USAGE_CONFIG")),
	}
	return cfg, errs
}

// isTruthy 는 불리언 env 를 읽는다("1"·"true"·"yes"·"on"). 그 밖은 전부 false.
func isTruthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func known(mode string) bool {
	for _, m := range Modes {
		if m == mode {
			return true
		}
	}
	return false
}

/*
 * portNumber 는 JS 의 `Number(raw)` + 정수·범위 검사를 옮긴 것이다.
 * 소수(8080.5)·범위 밖·숫자가 아닌 것은 전부 거부한다 — 조용히 잘라 쓰면 사람이 의도한
 * 포트와 실제로 뜬 포트가 갈리고, 그 차이는 "왜 안 붙지"로만 보인다.
 */
func portNumber(raw string) (int, bool) {
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	if f != math.Trunc(f) || f < 1 || f > 65535 {
		return 0, false
	}
	return int(f), true
}

/*
 * keywordRetention 은 keyword 보존 기한(일)을 읽는다.
 *
 * 'off'·'no'·'false'·'0' → nil(정리기를 띄우지 않는다 = 무기한 보관).
 * 그 밖의 값은 store 가 하한·상한으로 클램프한다 — **상수를 여기 복제하지 않는다.**
 * 복제하면 둘 중 하나만 고쳐지는 날이 오고, 그때 설정 화면과 실제 삭제 기준이 갈린다.
 */
func keywordRetention(raw string) *int {
	if raw == "" {
		d := store.KeywordRetentionDefault
		return &d
	}
	switch strings.ToLower(raw) {
	case "off", "no", "false", "0":
		return nil
	}
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 {
		d := store.KeywordRetentionDefault
		return &d
	}
	v := int(n)
	d := store.RetentionDays(&v)
	return &d
}

/*
 * configPath 는 단가표를 어디서 읽을지다. cost 패키지와 **같은 규칙**이어야 한다:
 * USAGE_CONFIG → 작업 디렉터리의 config.json.
 *
 * JS 는 모듈 파일 기준(`__dirname/..`)으로 찾지만 컴파일된 바이너리엔 그 기준이 없다.
 * ⚠ cwd 가 레포 루트가 아니면 단가가 조용히 시드로 떨어진다 — 그래서 부팅 로그가 이 경로를
 * 찍는다. 배포에서는 USAGE_CONFIG 를 명시하는 편이 안전하다.
 */
func configPath(raw string) string {
	if raw == "" {
		return "config.json"
	}
	return raw
}

// Help 는 거부 메시지 뒤에 붙는 안내다. **토큰 값은 절대 찍지 않는다.**
func Help() string {
	return strings.Join([]string{
		"",
		"사용법: USAGE_ADMIN_TOKEN=<토큰> usage-server",
		"",
		"  USAGE_ADMIN_TOKEN  (필수) 조회 토큰. Authorization: Bearer <토큰> 또는 쿠키 usage_tok.",
		"  USAGE_INTAKE_TOKEN (선택) 보고 전용 토큰. POST /api/usage 만 열린다(조회 403).",
		"                     수집기에 배포할 토큰을 열람 토큰과 분리하는 용도. admin 과 같은 값은 거부한다.",
		fmt.Sprintf("  USAGE_PORT         포트(기본 %d). 브라우저 차단 포트(4190·6000 등)는 거부한다", DefaultPort),
		fmt.Sprintf("  USAGE_HOST         바인드 주소(기본 %s — 루프백)", DefaultHost),
		"  USAGE_DB_MODE      local(기본, 로컬 sqlite) | remote(DATABASE_URL 로 원격 pg, 읽기 전용)",
		"  DATABASE_URL       remote 모드의 접속 문자열(SSH 터널 예: postgres://…@127.0.0.1:15432/usage)",
		"  USAGE_TENANT       조회 테넌트(기본 default)",
		"  USAGE_DATA_DIR     local 모드의 sqlite 디렉터리(기본 <cwd>/data)",
		"  USAGE_CONFIG       단가표 config.json 경로(기본 <cwd>/config.json)",
		"  USAGE_KEYWORD_RETENTION_DAYS  keyword 축 보존일(기본 90). off 면 정리기를 띄우지 않는다",
		"",
		"자세한 절차: README.md",
	}, "\n")
}
