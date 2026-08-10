package httpapi

import (
	"embed"
	"net/http"
	"strconv"

	"github.com/tscorp/user-usage/internal/org"
)

/*
 * 원커맨드 연동의 무인증/키검증 서빙.
 *
 *   GET /install.sh                        무인증 부트스트랩. 키가 인증을 대신한다.
 *   GET /api/agent/collector?os=&arch=      인제스트 키 필수 — 오픈 바이너리 CDN 이 되지 않게.
 *
 * 둘 다 **게이트 앞**(server.go)에서 처리된다:
 *   · install.sh 는 /api/* 가 아니라 게이트가 404 로 끊기 전에 잡아야 한다.
 *   · collector 는 게이트의 인테이크 규칙이 GET 조회를 막으므로(그 스코프는 POST 보고만) 여기서
 *     인제스트 키를 직접 검증한다.
 */

// agentbin — 수집기 크로스컴파일 산출물. 레이아웃은 agentbin/<goos>_<goarch>/usage-collector.
// all: 접두사로 .gitkeep(도트파일)까지 포함해 **바이너리가 하나도 없어도** 컴파일된다 —
// 실제 바이너리는 scripts/build.sh 가 채우고 .gitignore 가 커밋에서 제외한다. 없으면 503.
//
//go:embed all:agentbin
var agentbin embed.FS

// installScript — 임베드된 설치 스크립트 본문(scripts/build.sh 가 릴리스에서 교체). 자리
// 표시자라도 컴파일되도록 파일이 항상 존재한다.
//
//go:embed install.sh
var installScript []byte

const agentbinRoot = "agentbin"

/*
 * supportedTargets — 수집기를 내려줄 수 있는 플랫폼 화이트리스트(scripts/build.sh 의
 * 크로스컴파일 대상과 일치). **화이트리스트라 os/arch 로 경로를 만들어도 경로 탈출이 성립하지
 * 않는다** — 표에 없는 조합은 경로를 만들기 전에 404 로 끊긴다(정규화·`..` 를 고민할 자리가 없다).
 */
var supportedTargets = map[string]bool{
	"darwin_arm64": true,
	"darwin_amd64": true,
	"linux_amd64":  true,
	"linux_arm64":  true,
}

// serveInstallScript 는 임베드된 설치 스크립트를 그대로 서빙한다(무인증).
func serveInstallScript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		sendError(w, http.StatusMethodNotAllowed, "GET 만 허용됩니다")
		return
	}
	h := w.Header()
	// text/x-shellscript — `curl … | sh` 파이프라인이 기대하는 타입. nosniff 로 오해석을 막는다.
	h.Set("Content-Type", "text/x-shellscript")
	h.Set("Content-Length", strconv.Itoa(len(installScript)))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(installScript)
}

/*
 * serveCollector 는 플랫폼별 수집기 바이너리를 내려준다. 순서가 계약이다:
 *   ① 인제스트 키 검증(없거나 틀리면 401) — 인증을 먼저 해 어떤 플랫폼이 있는지조차 무인증에
 *      노출하지 않는다.
 *   ② 플랫폼 화이트리스트(지원 안 하면 404).
 *   ③ 임베드 조회(바이너리가 아직 없으면 503).
 */
func (s *server) serveCollector(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		sendError(w, http.StatusMethodNotAllowed, "GET 만 허용됩니다")
		return
	}

	// ① 자격 — 헤더 우선, 없으면 ?key=(curl 파이프라인 편의).
	key := bearerToken(r)
	fromHeader := key != ""
	if key == "" {
		key = r.URL.Query().Get("key")
	}
	if key == "" {
		sendError(w, http.StatusUnauthorized,
			"인제스트 키가 필요합니다 — Authorization: Bearer <key> 또는 ?key=<key>")
		return
	}
	/*
	 * 다운로드는 **보고 자격이면 충분하다.** 이 엔드포인트가 막는 것은 "오픈 바이너리 CDN 이
	 * 되는 것" 하나이고, 바이너리에는 테넌트 데이터가 없다. 이미 보고할 수 있는 cfg 인테이크
	 * 토큰과 그보다 강한 admin 토큰을 여기서만 거절하면, 그 토큰으로 설치하는 배포는 원커맨드가
	 * ②에서 401 로 끊긴다 — 자격에 따라 설치가 반쪽으로 끝나는 자리를 만들지 않는다.
	 *
	 * 단 cfg 토큰은 **헤더로만** 인정한다. ?key= 는 액세스 로그·리퍼러에 남는 자리라 전사 열람
	 * 권한이 있는 토큰을 태울 곳이 아니다(인제스트 키는 보고 전용이라 여기까지 감수한다).
	 */
	authorized := false
	if fromHeader {
		// Bearer 가 있으므로 Authenticate 는 헤더 분기만 탄다(쿠키로 흐르지 않는다).
		if a := Authenticate(r, s.cfg); a != nil {
			authorized = true
		}
	}
	// org.Resolve 는 전역 핸들로 키를 해석한다(게이트의 인테이크 해석과 같은 함수). err 은 조회
	// 자체 실패(또는 미초기화)이고, !ok 는 미지·해지 키다 — 둘 다 401 로 접는다(서버 상태를
	// 무인증 부트스트랩 경로로 흘리지 않는다). err 은 stderr 로만 남긴다.
	if !authorized {
		_, _, ok, err := org.Resolve(r.Context(), key)
		if err != nil {
			logf("collector 인제스트 키 해석 실패: %v", err)
		}
		authorized = err == nil && ok
	}
	if !authorized {
		sendError(w, http.StatusUnauthorized, "유효하지 않은 인제스트 키입니다")
		return
	}

	// ② 플랫폼 화이트리스트.
	goos := r.URL.Query().Get("os")
	goarch := r.URL.Query().Get("arch")
	target := goos + "_" + goarch
	if goos == "" || goarch == "" || !supportedTargets[target] {
		sendError(w, http.StatusNotFound, "지원하지 않는 플랫폼입니다 — os/arch 를 확인하세요")
		return
	}

	// ③ 임베드된 바이너리. 없으면(빌드가 아직 크로스컴파일하지 않음) 503.
	embedded := agentbinRoot + "/" + target + "/usage-collector"
	buf, err := agentbin.ReadFile(embedded)
	if err != nil || len(buf) == 0 {
		sendError(w, http.StatusServiceUnavailable,
			"이 플랫폼의 수집기 바이너리가 아직 준비되지 않았습니다 — 서버 빌드(scripts/build.sh)가 필요합니다")
		return
	}

	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Length", strconv.Itoa(len(buf)))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Disposition", `attachment; filename="usage-collector"`)
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(buf)
}
