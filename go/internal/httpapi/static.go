package httpapi

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
)

/*
 * 정적 서빙 — **경로 화이트리스트다.**
 *
 * 디렉터리를 열고 `..` 를 막는 대신 나갈 수 있는 URL 을 통째로 열거한다. 그러면 경로 탈출이라는
 * 문제 **자체가 성립하지 않는다** — 정규화·심링크·인코딩·이중 디코딩을 고민할 자리가 없다.
 *
 * ⚠ 표는 **손으로 쓰지 않고 embed FS 를 순회해 만든다.** Next.js 산출물의 파일명이 콘텐츠 해시라
 *   (`/_next/static/chunks/3l59e9bt0ngpj.css`) 손으로 쓴 표는 **빌드마다 깨진다.** 그리고 그때
 *   증상은 컴파일 에러가 아니라 "화면이 에러 없이 빈 채로 뜨는 것"이다.
 *
 *   생성으로 바꿔도 화이트리스트의 성질은 그대로다 — 나갈 수 있는 것은 **바이너리에 박힌
 *   파일뿐**이고, 디스크에도 상위 디렉터리에도 닿지 않는다. 열거를 사람이 하느냐 빌드가 하느냐만
 *   달라진다.
 *
 * go:embed 로 바이너리에 넣는다. 배포가 파일 한 개가 되고, 실행 디렉터리에 정적 파일이 없어서
 * 화면만 안 뜨는 사고가 사라진다.
 *
 * ⚠ `all:` 접두사가 **필수다.** go:embed 는 `_`·`.` 로 시작하는 이름을 기본적으로 건너뛰는데
 *   Next 산출물의 본체가 전부 `_next/` 아래다. 빼면 셸만 나가고 스크립트·스타일이 통째로
 *   빠지며, 그 증상은 404 가 아니라 빈 화면이다(static_test.go 가 잡는다).
 *
 * ⚠ webroot/ 는 web/out/ 의 **사본**이다. go:embed 가 패키지 디렉터리 밖을 참조하지 못하고
 *   심링크를 따라가지 않으므로 복사가 유일한 길이다. 두 벌이 갈라지지 않게 하는 것은 사람의
 *   기억이 아니라 **scripts/build.sh** 다 — 그 스크립트가 유일한 빌드 경로다.
 */

//go:embed all:webroot
var webroot embed.FS

// embedRoot 는 임베드 트리의 루트다. URL 은 여기를 떼어낸 나머지다.
const embedRoot = "webroot"

/*
 * staticPaths 는 URL → 임베드 경로. 이 표에 없는 URL 은 정적 파일이 아니다.
 * init 에서 한 번 만들고 그 뒤에는 읽기만 한다 — 요청 경로에서 파생되는 것이 없으므로
 * 런타임에 표가 자라지 않는다(자란다면 그것이 곧 화이트리스트를 잃는 것이다).
 */
var staticPaths = buildStaticPaths(webroot)

/*
 * buildStaticPaths 는 임베드 FS 를 순회해 화이트리스트를 만든다.
 *
 * 규칙은 셋뿐이다:
 *   ① 파일 하나 = URL 하나. 디렉터리는 URL 이 아니다(`/_next/` 는 404).
 *   ② 이름이 `.` 으로 시작하는 것은 뺀다 — build.sh 가 webroot/ 를 비울 때 남기는
 *      `.gitkeep`·`.gitignore` 나 OS 가 흘리는 `.DS_Store` 는 화면의 일부가 아니다.
 *   ③ `/` 는 index.html 이다. 라우팅이 해시(`#/usage`)라 그 밖의 SPA 폴백은 필요 없다.
 *
 * fs.FS 를 받는다 — 그래서 합성 FS 로 규칙 자체를 테스트할 수 있다.
 */
func buildStaticPaths(fsys fs.FS) map[string]string {
	out := map[string]string{}
	err := fs.WalkDir(fsys, embedRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		out[strings.TrimPrefix(p, embedRoot)] = p
		return nil
	})
	if err != nil {
		// 임베드 트리를 읽지 못하면 셸이 없는 바이너리다. 조용히 404 를 내는 서버로 뜨는 것보다
		// 부팅에서 죽는 편이 낫다 — 전자는 "화면이 왜 안 뜨지"로 몇 시간을 태운다.
		panic(fmt.Sprintf("httpapi: 임베드된 %s/ 를 순회할 수 없다: %v", embedRoot, err))
	}
	if index, ok := out["/index.html"]; ok {
		out["/"] = index
	} else {
		panic(fmt.Sprintf("httpapi: 임베드된 %s/index.html 이 없다 — "+
			"web 빌드가 동기화되지 않았다(scripts/build.sh 로 빌드하라)", embedRoot))
	}
	return out
}

/*
 * mimeTypes — Next 산출물이 내는 확장자를 전부 덮는다.
 *
 * ⚠ 빠진 확장자는 octet-stream 으로 나가고, 그러면 `nosniff` 아래에서 브라우저가 그 리소스를
 *   **거부한다.** 서버는 200 이고 네트워크 탭도 200 인데 화면만 안 뜬다.
 *   `.txt` 는 Next 의 RSC 페이로드(`index.txt` · `__next._tree.txt`)이고, `.json` 은
 *   `embed-manifest.json` 이다.
 */
var mimeTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".json":  "application/json; charset=utf-8",
	".map":   "application/json; charset=utf-8",
	".txt":   "text/plain; charset=utf-8",
	".svg":   "image/svg+xml",
	".woff2": "font/woff2",
	".ico":   "image/x-icon",
}

/*
 * CSP — server.js:289~293 을 그대로 옮겼다.
 *
 * ⚠ script-src 는 'self' 로 끝난다. web-next 오너의 빌드 후처리
 *   (web/scripts/externalize-inline-scripts.mjs)가 Next 가 남기는 인라인 <script> 를 전부 파일로
 *   뽑아내기 때문이다. 여기에 'unsafe-inline' 을 열면 그 후처리를 만든 이유가 사라지고, 스크립트
 *   주입 방어를 통째로 포기하게 된다. **완화하지 마라** — static_test.go 가 이 값을 못 박는다.
 *
 * style-src 만 'unsafe-inline' 을 남긴다(컴포넌트가 style="…" 속성을 쓴다).
 */
const csp = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; font-src 'self'; connect-src 'self'; " +
	"form-action 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'"

// resolveStatic 은 URL 경로를 임베드 경로로 옮긴다. 정적 파일이 아니면 ok=false.
//
// 판정은 **서버가 실제로 받은 문자열**(server.go 의 EscapedPath) 위에서 단 한 번의 맵 조회다.
// 정규화하지 않으므로 `/%2e%2e/x` 와 `/../x` 가 같은 것으로 접히는 자리가 없다 — 둘 다 표에
// 없어서 404 다.
func resolveStatic(p string) (string, bool) {
	hit, ok := staticPaths[p]
	return hit, ok
}

/*
 * serveFile — 셸은 **무인증**이다. 데이터는 전부 /api/* 로 오고 그쪽에 게이트가 있다.
 * 화면 껍데기를 가리면 "토큰을 어디에 넣어야 하는가"를 안내할 자리가 사라진다.
 *
 * 보안 헤더는 server.js:309~317 을 그대로 옮겼다.
 */
func serveFile(w http.ResponseWriter, r *http.Request, embedded string) {
	buf, err := webroot.ReadFile(embedded)
	if err != nil {
		// 화이트리스트에 있는데 파일이 없다 = 빌드가 깨진 것이다. 클라이언트에는 404 로 보인다.
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	h := w.Header()
	ct := mimeTypes[path.Ext(embedded)]
	if ct == "" {
		ct = "application/octet-stream"
	}
	h.Set("Content-Type", ct)
	h.Set("Content-Length", strconv.Itoa(len(buf)))
	/*
	 * no-cache 를 유지한다. `_next/static/*` 는 파일명이 콘텐츠 해시라 immutable 로 굳혀도
	 * 안전하지만, 그것은 이 통합의 일이 아니다 — 지금 바꾸면 캐시 때문에 "옛 화면"이 뜨는지
	 * 아니면 동기화가 안 된 것인지 구분이 어려워진다. 성능이 문제가 되면 그때 나눈다.
	 */
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Content-Security-Policy", csp)
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(buf)
}
