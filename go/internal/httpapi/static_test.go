package httpapi

import (
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
)

/*
 * 여기서 재는 것은 **Next.js 산출물을 embed FS 에서 화이트리스트로 만드는 배선**이다.
 *
 * 골든 44개는 /api/* 만 본다. 정적 축은 이 파일이 유일한 자동 판정자이고, 이 통합의 실패 모드가
 * 정확히 "서버는 200 을 주는데 화면이 에러 없이 빈 채로 뜨는 것"이라 curl 200 만으로는 부족하다.
 * 그래서 아래 두 테스트는 응답 코드가 아니라 **산출물의 내적 정합**을 잰다:
 *
 *   · TestIndexHTMLReferencesOnlyEmbeddedAssets — index.html 이 가리키는 URL 이 전부 표에 있는가
 *     (webroot/ 가 web/out/ 과 갈라지면 여기서 먼저 빨개진다 — 화면에서 보이기 전에)
 *   · TestNoInlineScriptRemainsInEmbeddedHTML  — 인라인이 하나라도 남으면 script-src 'self' 아래
 *     하이드레이션이 죽고, 그 증상이 "빈 화면 · 콘솔 CSP 위반"이다
 */

/* ── 화이트리스트 생성기 자체 (합성 FS) ──────────────────────────────── */

func TestBuildStaticPathsWalksEmbeddedFS(t *testing.T) {
	got := buildStaticPaths(fstest.MapFS{
		"webroot/index.html":                      {Data: []byte("x")},
		"webroot/favicon.svg":                     {Data: []byte("x")},
		"webroot/_next/static/chunks/a1b2c3.css":  {Data: []byte("x")},
		"webroot/_next/static/inline/deadbeef.js": {Data: []byte("x")},
	})

	want := map[string]string{
		"/":                                "webroot/index.html",
		"/index.html":                      "webroot/index.html",
		"/favicon.svg":                     "webroot/favicon.svg",
		"/_next/static/chunks/a1b2c3.css":  "webroot/_next/static/chunks/a1b2c3.css",
		"/_next/static/inline/deadbeef.js": "webroot/_next/static/inline/deadbeef.js",
	}
	if len(got) != len(want) {
		t.Fatalf("항목 수 = %d, want %d (%v)", len(got), len(want), keysOf(got))
	}
	for url, embedded := range want {
		if got[url] != embedded {
			t.Fatalf("%s → %q, want %q", url, got[url], embedded)
		}
	}
}

func TestBuildStaticPathsSkipsDotFiles(t *testing.T) {
	// build.sh 가 webroot/ 를 비울 때 남겨 두는 .gitkeep · .gitignore 류는 화면의 일부가 아니다.
	got := buildStaticPaths(fstest.MapFS{
		"webroot/index.html":      {Data: []byte("x")},
		"webroot/.gitkeep":        {Data: []byte("")},
		"webroot/.gitignore":      {Data: []byte("*")},
		"webroot/_next/.DS_Store": {Data: []byte("x")},
	})
	for _, bad := range []string{"/.gitkeep", "/.gitignore", "/_next/.DS_Store"} {
		if _, ok := got[bad]; ok {
			t.Fatalf("%s 가 표에 들어갔다", bad)
		}
	}
	if got["/"] != "webroot/index.html" {
		t.Fatalf("/ → %q", got["/"])
	}
}

/* ── 실제 임베드된 산출물 ────────────────────────────────────────────── */

// embeddedFiles 는 임베드된 webroot 의 실제 파일 목록이다(디렉터리·도트파일 제외).
func embeddedFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	err := fs.WalkDir(webroot, "webroot", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		out = append(out, p)
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir: %v", err)
	}
	sort.Strings(out)
	return out
}

func TestEmbedIncludesNextUnderscoreDirs(t *testing.T) {
	/*
	 * go:embed 는 `_` · `.` 로 시작하는 이름을 **기본적으로 건너뛴다.** Next 산출물의 본체가
	 * 전부 `_next/` 아래라 `all:` 접두사를 빼면 셸만 나가고 스크립트·스타일이 통째로 빠진다 —
	 * 그리고 그 증상은 404 가 아니라 "빈 화면"이다.
	 */
	files := embeddedFiles(t)
	if len(files) < 10 {
		t.Fatalf("임베드된 파일이 %d개뿐이다 — webroot/ 가 비었거나 동기화되지 않았다: %v", len(files), files)
	}
	var next int
	for _, f := range files {
		if strings.HasPrefix(f, "webroot/_next/") {
			next++
		}
	}
	if next == 0 {
		t.Fatalf("webroot/_next/ 아래가 하나도 임베드되지 않았다 — //go:embed 에 all: 접두사가 필요하다.\n임베드된 것: %v", files)
	}
}

func TestWhitelistIsExactlyTheEmbeddedSetPlusRoot(t *testing.T) {
	// 손으로 적은 표가 아니라는 것을 잰다 — 파일명이 콘텐츠 해시라 손 표는 빌드마다 깨진다.
	files := embeddedFiles(t)
	if len(staticPaths) != len(files)+1 { // +1 = "/"
		t.Fatalf("표 %d개 vs 임베드 파일 %d개(+/) — 생성기가 순회를 빠뜨렸다", len(staticPaths), len(files))
	}
	for _, f := range files {
		url := strings.TrimPrefix(f, "webroot")
		if staticPaths[url] != f {
			t.Fatalf("%s 가 표에 없다(또는 %q 를 가리킨다)", url, staticPaths[url])
		}
	}
	if staticPaths["/"] != "webroot/index.html" {
		t.Fatalf(`staticPaths["/"] = %q, want "webroot/index.html"`, staticPaths["/"])
	}
}

func TestRootAndIndexHTMLServeTheSameBytes(t *testing.T) {
	h := New(testCfg(false)) // 정적은 무인증이다 — DB 도 필요 없다
	root := do(t, h, http.MethodGet, "/", "")
	idx := do(t, h, http.MethodGet, "/index.html", "")
	if root.Code != 200 || idx.Code != 200 {
		t.Fatalf("/ → %d, /index.html → %d", root.Code, idx.Code)
	}
	if root.Body.String() != idx.Body.String() {
		t.Fatal("/ 와 /index.html 의 본문이 다르다")
	}
	if !strings.Contains(root.Body.String(), "<!DOCTYPE html>") {
		t.Fatalf("/ 가 HTML 이 아니다: %.120q", root.Body.String())
	}
}

func TestNextAssetsCarryTheRightMIME(t *testing.T) {
	/*
	 * MIME 이 틀리면 nosniff 아래에서 브라우저가 그 리소스를 **거부한다.** 서버는 200 이고
	 * 네트워크 탭도 200 인데 화면만 안 뜬다 — 이 통합에서 가장 찾기 어려운 실패다.
	 */
	h := New(testCfg(false))
	wantByExt := map[string]string{
		".html":  "text/html; charset=utf-8",
		".css":   "text/css; charset=utf-8",
		".js":    "text/javascript; charset=utf-8",
		".svg":   "image/svg+xml",
		".txt":   "text/plain; charset=utf-8",
		".json":  "application/json; charset=utf-8",
		".map":   "application/json; charset=utf-8",
		".woff2": "font/woff2",
		".ico":   "image/x-icon",
	}

	seen := map[string]bool{}
	for _, f := range embeddedFiles(t) {
		url := strings.TrimPrefix(f, "webroot")
		ext := path.Ext(f)
		want, ok := wantByExt[ext]
		if !ok {
			t.Fatalf("%s: 확장자 %q 에 MIME 표 항목이 없다 — 표를 늘려라(octet-stream 으로 나가면 nosniff 가 거부한다)", url, ext)
		}
		w := do(t, h, http.MethodGet, url, "")
		if w.Code != 200 {
			t.Fatalf("%s → %d", url, w.Code)
		}
		if got := w.Header().Get("Content-Type"); got != want {
			t.Fatalf("%s: Content-Type = %q, want %q", url, got, want)
		}
		seen[ext] = true
	}

	// 산출물에 실제로 있는 확장자는 최소 이만큼이다(빌드가 갈라졌는지 잡는다).
	for _, ext := range []string{".html", ".css", ".js", ".svg", ".txt", ".json"} {
		if !seen[ext] {
			t.Fatalf("산출물에 %s 파일이 없다 — webroot/ 동기화를 확인하라", ext)
		}
	}
}

var srcRE = regexp.MustCompile(`(?:src|href)="(/[^"]*)"`)

func TestIndexHTMLReferencesOnlyEmbeddedAssets(t *testing.T) {
	/*
	 * 드리프트 탐지기다. webroot/ 가 web/out/ 과 갈라지면 index.html 은 사라진 청크를 가리키고,
	 * 그때 증상은 404 하나와 **에러 없는 빈 화면**이다. 사람이 콘솔을 열어야 알게 되는 것을
	 * 테스트가 먼저 말하게 한다.
	 */
	buf, err := webroot.ReadFile("webroot/index.html")
	if err != nil {
		t.Fatalf("index.html 이 임베드되지 않았다: %v", err)
	}
	ms := srcRE.FindAllStringSubmatch(string(buf), -1)
	if len(ms) < 5 {
		t.Fatalf("index.html 에서 참조를 %d개만 찾았다 — 산출물이 이상하다", len(ms))
	}
	h := New(testCfg(false))
	for _, m := range ms {
		ref := m[1]
		if _, ok := staticPaths[ref]; !ok {
			t.Fatalf("index.html 이 %s 를 가리키는데 표에 없다 — webroot/ 가 web/out/ 과 갈라졌다", ref)
		}
		if w := do(t, h, http.MethodGet, ref, ""); w.Code != 200 {
			t.Fatalf("index.html 이 가리키는 %s → %d", ref, w.Code)
		}
	}
}

func TestNoInlineScriptRemainsInEmbeddedHTML(t *testing.T) {
	/*
	 * CSP 는 script-src 'self' 다. 인라인 <script> 가 하나라도 남으면 그 스크립트가 죽고,
	 * Next 의 하이드레이션 부트스트랩이라면 화면이 통째로 빈다.
	 * web/scripts/externalize-inline-scripts.mjs 가 빌드에서 이것을 보장하지만, 여기서 다시 잰다 —
	 * 우리가 실제로 서빙하는 바이트에 대해서.
	 */
	inline := regexp.MustCompile(`(?i)<script(?:\s[^>]*)?>`)
	hasSrc := regexp.MustCompile(`(?i)\ssrc\s*=`)
	for _, f := range embeddedFiles(t) {
		if path.Ext(f) != ".html" {
			continue
		}
		buf, err := webroot.ReadFile(f)
		if err != nil {
			t.Fatalf("ReadFile %s: %v", f, err)
		}
		for _, tag := range inline.FindAllString(string(buf), -1) {
			if !hasSrc.MatchString(tag) {
				t.Fatalf("%s 에 인라인 스크립트가 남아 있다: %s", f, tag)
			}
		}
	}
}

func TestCSPKeepsScriptSrcSelfWithoutUnsafeInline(t *testing.T) {
	/*
	 * web-next 오너가 인라인을 파일로 뽑아 둔 이유가 이 한 줄이다. 여기를 완화하면 그 후처리가
	 * 의미를 잃고, 스크립트 주입 방어를 통째로 포기하게 된다. 그래서 값 자체를 못 박는다.
	 */
	if !strings.Contains(csp, "script-src 'self';") {
		t.Fatalf("csp 에 script-src 'self' 가 없다: %s", csp)
	}
	scriptDirective := ""
	for _, d := range strings.Split(csp, ";") {
		if strings.HasPrefix(strings.TrimSpace(d), "script-src") {
			scriptDirective = d
		}
	}
	if strings.Contains(scriptDirective, "unsafe-inline") || strings.Contains(scriptDirective, "unsafe-eval") {
		t.Fatalf("script-src 가 완화됐다: %q", scriptDirective)
	}
	// 응답에도 실제로 실리는지 — 상수만 맞고 헤더에 안 붙으면 의미가 없다.
	h := New(testCfg(false))
	w := do(t, h, http.MethodGet, "/", "")
	if got := w.Header().Get("Content-Security-Policy"); got != csp {
		t.Fatalf("헤더 CSP = %q", got)
	}
}

func TestPathsOutsideTheWhitelistAre404JSON(t *testing.T) {
	/*
	 * 화이트리스트가 embed FS 에서 생성되어도 성질은 그대로다 — 나갈 수 있는 것은 바이너리에
	 * 박힌 파일뿐이고, 디스크에도 상위 디렉터리에도 닿지 않는다. 아래는 그 성질의 증거다.
	 *
	 * ⚠ 경로는 서버가 실제로 받는 문자열 그대로 판정된다(server.go 는 EscapedPath 를 쓴다).
	 *   그래서 %2e%2e 가 디코딩되어 표를 다르게 통과하는 일이 없다.
	 */
	h := New(testCfg(false))
	for _, p := range []string{
		// 경로 탈출 시도
		"/../server.js",
		"/../../etc/passwd",
		"/_next/../../server.js",
		"/%2e%2e/app.js",
		"/%2e%2e%2fserver.js",
		"/_next/static/chunks/../../../server.js",
		"/webroot/index.html",
		// 디렉터리는 파일이 아니다
		"/_next",
		"/_next/",
		"/_next/static/chunks",
		"/_next/static/chunks/",
		// 레포 파일
		"/package.json",
		"/server.js",
		"/config.json",
		// 구 바닐라 프런트 — 이것들이 200 이면 교체가 안 된 것이다
		"/app.js",
		"/app.css",
		"/js/core.js",
		"/js/router.js",
		"/js/theme-boot.js",
		"/views/usagetrack.js",
		"/views/usageobs.js",
		// 그냥 없는 것
		"/definitely-not-a-thing",
		"/_next/static/chunks/deadbeefdeadbeef.js",
	} {
		w := do(t, h, http.MethodGet, p, "")
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s → %d, want 404", p, w.Code)
		}
		// 404 의 shape 는 현행 계약이다(골든 err-404-unknown-path 가 잡는다).
		// Next 의 404.html 로 바꾸면 /api/* 아닌 미지 경로의 응답 shape 가 바뀐다.
		if ct := w.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
			t.Fatalf("%s: 404 Content-Type = %q, want JSON", p, ct)
		}
		if got := decode(t, w)["error"]; got != "not found" {
			t.Fatalf("%s: 404 body = %s", p, w.Body.String())
		}
	}
}

func TestNext404HTMLIsServedOnlyAtItsOwnPath(t *testing.T) {
	// Next 가 404.html 을 낸다. 그 파일 자체는 서빙되지만 **미지 경로의 응답이 되지는 않는다.**
	h := New(testCfg(false))
	if w := do(t, h, http.MethodGet, "/404.html", ""); w.Code != 200 {
		t.Fatalf("/404.html → %d, want 200", w.Code)
	}
	w := do(t, h, http.MethodGet, "/nope", "")
	if ct := w.Header().Get("Content-Type"); strings.HasPrefix(ct, "text/html") {
		t.Fatalf("미지 경로가 HTML 을 냈다(Content-Type=%q) — 404 는 JSON 이어야 한다", ct)
	}
}

func TestStaticSecurityHeadersAreOnEveryAsset(t *testing.T) {
	h := New(testCfg(false))
	want := map[string]string{
		"X-Frame-Options":         "DENY",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "same-origin",
		"Content-Security-Policy": csp,
		"Cache-Control":           "no-cache",
	}
	for _, url := range []string{"/", "/index.html", "/favicon.svg", "/theme-boot.js"} {
		w := do(t, h, http.MethodGet, url, "")
		if w.Code != 200 {
			t.Fatalf("%s → %d", url, w.Code)
		}
		for k, v := range want {
			if got := w.Header().Get(k); got != v {
				t.Fatalf("%s: %s = %q, want %q", url, k, got, v)
			}
		}
	}
}

func TestHeadOnStaticSendsHeadersWithoutBody(t *testing.T) {
	h := New(testCfg(false))
	w := do(t, h, http.MethodHead, "/index.html", "")
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("HEAD 에 본문이 실렸다(%d바이트)", w.Body.Len())
	}
	if w.Header().Get("Content-Length") == "" {
		t.Fatal("Content-Length 가 없다")
	}
}

func TestStaticIsNotServedForNonReadMethods(t *testing.T) {
	// server.go 는 GET/HEAD 에서만 정적을 본다. POST /는 정적이 아니므로 404 JSON 이다.
	h := New(testCfg(false))
	w := do(t, h, http.MethodPost, "/", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("POST / → %d, want 404", w.Code)
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
