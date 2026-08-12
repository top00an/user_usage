package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tscorp/user-usage/internal/org"
	"github.com/tscorp/user-usage/internal/tenant"
)

// install.sh — 무인증 서빙. Content-Type 과 본문(임베드된 스크립트 그대로)을 잡는다.
func TestServeInstallScript(t *testing.T) {
	h := New(testCfg(false))

	rec := do(t, h, http.MethodGet, "/install.sh", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("install.sh: code=%d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/x-shellscript" {
		t.Fatalf("Content-Type=%q (기대 text/x-shellscript)", ct)
	}
	if rec.Body.Len() == 0 || rec.Body.String() != string(installScript) {
		t.Fatalf("install.sh 본문이 임베드 스크립트와 다르다(len=%d)", rec.Body.Len())
	}
	// 상태변경 메서드는 405.
	if rec := do(t, h, http.MethodPost, "/install.sh", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("install.sh POST: code=%d (기대 405)", rec.Code)
	}
}

// collector — 인제스트 키 필수. 키 없음/틀림 401, 미지원 플랫폼 404, 지원 플랫폼은 200 또는 503
// (임베드 바이너리 유무는 빌드 환경마다 다르다 — 여기서는 인증·플랫폼 판정을 통과했음을 본다).
func TestServeCollector(t *testing.T) {
	ctx := tenant.With(context.Background(), "default")
	d := openDB(t)
	if err := org.Init(ctx, d); err != nil {
		t.Fatalf("org.Init: %v", err)
	}
	issued, err := org.IssueForTenant(ctx, "default", "default")
	if err != nil {
		t.Fatalf("IssueForTenant: %v", err)
	}
	h := New(testCfg(false))
	withKey := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+issued.Plain) }

	// 키 없음 → 401.
	if rec := do(t, h, http.MethodGet, "/api/agent/collector?os=linux&arch=amd64", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("키 없음: code=%d (기대 401)", rec.Code)
	}
	// 틀린 키 → 401.
	badKey := func(r *http.Request) { r.Header.Set("Authorization", "Bearer uu_ing_deadbeef") }
	if rec := do(t, h, http.MethodGet, "/api/agent/collector?os=linux&arch=amd64", "", badKey); rec.Code != http.StatusUnauthorized {
		t.Fatalf("틀린 키: code=%d (기대 401)", rec.Code)
	}
	// 유효 키 + 미지원 플랫폼 → 404.
	if rec := do(t, h, http.MethodGet, "/api/agent/collector?os=plan9&arch=foo", "", withKey); rec.Code != http.StatusNotFound {
		t.Fatalf("미지원 플랫폼: code=%d (기대 404)", rec.Code)
	}
	// 유효 키 + os/arch 누락 → 404.
	if rec := do(t, h, http.MethodGet, "/api/agent/collector", "", withKey); rec.Code != http.StatusNotFound {
		t.Fatalf("플랫폼 누락: code=%d (기대 404)", rec.Code)
	}
	// 유효 키 + 지원 플랫폼 → 200(바이너리 임베드됨) 또는 503(빌드 전). 401/404 면 배선 오류다.
	rec := do(t, h, http.MethodGet, "/api/agent/collector?os=linux&arch=amd64", "", withKey)
	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("지원 플랫폼(헤더 키): code=%d (기대 200 또는 503)", rec.Code)
	}
	if rec.Code == http.StatusOK {
		if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
			t.Fatalf("바이너리 Content-Type=%q (기대 application/octet-stream)", ct)
		}
	}
	// ?key= 쿼리 파라미터 인증 경로도 같은 판정을 통과한다.
	rec = do(t, h, http.MethodGet, "/api/agent/collector?os=linux&arch=amd64&key="+issued.Plain, "")
	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("지원 플랫폼(?key=): code=%d (기대 200 또는 503)", rec.Code)
	}
}

/*
 * 수집기 다운로드는 **보고 자격이면 충분하다** — 바이너리에는 테넌트 데이터가 없고, 이 엔드포인트의
 * 목적은 "오픈 바이너리 CDN 이 되지 않는 것" 하나다. 이미 보고할 수 있는 자격(cfg 인테이크 토큰)과
 * 관리 자격(admin 토큰)은 인제스트 키보다 강하므로 여기서 거절할 이유가 없다. 거절하면 종전
 * 단일 토큰 배포는 원커맨드 설치를 ②에서 401 로 못 넘긴다.
 *
 * 단 cfg 토큰은 **헤더로만** 받는다 — ?key= 는 액세스 로그·리퍼러에 남는 자리라 관리 토큰을
 * 태울 곳이 아니다.
 */
func TestServeCollectorAcceptsAdminAndIntakeTokens(t *testing.T) {
	ctx := tenant.With(context.Background(), "default")
	d := openDB(t)
	if err := org.Init(ctx, d); err != nil {
		t.Fatalf("org.Init: %v", err)
	}
	h := New(testCfg(false))
	const target = "/api/agent/collector?os=linux&arch=amd64"

	// admin 토큰(헤더) → 인증 통과(200 또는 바이너리 미빌드 503).
	if rec := do(t, h, http.MethodGet, target, "", withAdmin); rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("admin 토큰 다운로드: code=%d (기대 200 또는 503)", rec.Code)
	}
	// 인테이크 토큰(헤더) → 인증 통과.
	if rec := do(t, h, http.MethodGet, target, "", withIntake); rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("인테이크 토큰 다운로드: code=%d (기대 200 또는 503)", rec.Code)
	}
	// cfg 토큰을 ?key= 로 → 401(쿼리에는 인제스트 키만).
	if rec := do(t, h, http.MethodGet, target+"&key="+testAdmin, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("?key=admin토큰: code=%d (기대 401)", rec.Code)
	}
	// 틀린 Bearer 는 여전히 401(org 키로도 해석 안 된다).
	badTok := func(r *http.Request) { r.Header.Set("Authorization", "Bearer nope-not-a-token") }
	if rec := do(t, h, http.MethodGet, target, "", badTok); rec.Code != http.StatusUnauthorized {
		t.Fatalf("틀린 Bearer: code=%d (기대 401)", rec.Code)
	}
}

/*
 * ── 설치 → 제거 왕복 ────────────────────────────────────────────────────────
 *
 * install.sh 는 개발자 PC 의 **남의 설정 파일**을 고친다. 그 코드가 회귀하면 피해자는 우리가
 * 아니라 사용자이고, 증상은 "상태줄이 사라졌다"처럼 늦게·조용히 나타난다. 그래서 여기서는
 * 문자열을 grep 하지 않고 **진짜로 돌린다** — 임시 HOME 에 설치하고, 제거하고, 남의 것이
 * 그대로 남았는지 본다.
 *
 * 수집기 바이너리는 httptest 서버가 **가짜 셸 스크립트**로 내려준다. install.sh 는 받은 것을
 * chmod +x 후 실행할 뿐이라 스크립트로도 경로가 그대로 성립한다(다운로드·실행·백필까지 진짜).
 */

// fakeCollectorScript — 어떤 인자로 불려도 0 으로 끝나는 가짜 수집기.
const fakeCollectorScript = `#!/bin/sh
case "${1:-}" in
  -h) exit 0 ;;
esac
echo "보낼 것이 없다(테스트 가짜 수집기)"
exit 0
`

// 설치 전부터 있던 남의 설정들. 제거 뒤에도 **한 글자도 달라지면 안 되는** 것들이다.
const (
	foreignHookCmd   = "echo 남의-훅"
	foreignStatusCmd = "my-own-statusline --fancy"
	foreignNS        = "orca-status"
)

func jsonToolAvailable() bool {
	for _, tool := range []string{"jq", "python3", "node"} {
		if _, err := exec.LookPath(tool); err == nil {
			return true
		}
	}
	return false
}

/*
 * installScriptSource — **진실의 출처**. 여기 있는 파일이 릴리스에 실린다.
 *
 * 옆의 `go/internal/httpapi/install.sh` 를 돌리지 않는 이유: 그건 scripts/build.sh 가 이 파일을
 * cp 해서 만드는 **빌드 산출물**이다. 사본을 돌리면 두 가지가 동시에 망가진다 — ① 원본을 고친
 * 사람은 빌드 전까지 테스트가 낡은 사본을 검사하는 걸 모르고, ② 사본만 고친 사람은 테스트가
 * 통과하니 다음 빌드에 작업이 사라지는 걸 모른다. 그래서 왕복은 **원본**을 돌리고, 사본이
 * 원본과 같은지는 TestEmbeddedInstallScriptMatchesSource 가 따로 본다.
 */
const installScriptSource = "../../../scripts/install.sh"

// runInstallSh — install.sh 를 **최소 환경**(HOME·PATH·TMPDIR 뿐)으로 돌린다. 테스트 프로세스의
// 환경이 새어 들어가면 "내 PC 에서만 통과"가 되므로 env 를 통째로 지정한다.
func runInstallSh(t *testing.T, home, path string, args ...string) (string, error) {
	t.Helper()
	abs, err := filepath.Abs(installScriptSource)
	if err != nil {
		t.Fatalf("install.sh 경로 해석 실패: %v", err)
	}
	cmd := exec.Command("sh", append([]string{abs}, args...)...)
	cmd.Env = []string{"HOME=" + home, "PATH=" + path, "TMPDIR=" + os.TempDir()}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func writeFileAt(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readJSONMap(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("읽기 실패 %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("유효 JSON 이 아니다 %s: %v\n%s", path, err, b)
	}
	return m
}

// sessionEndCommands — settings.json 의 SessionEnd 훅 명령을 평평하게 모은다(없으면 빈 슬라이스).
func sessionEndCommands(t *testing.T, path string) []string {
	t.Helper()
	var out []string
	root := readJSONMap(t, path)
	hooks, _ := root["hooks"].(map[string]any)
	groups, _ := hooks["SessionEnd"].([]any)
	for _, g := range groups {
		group, _ := g.(map[string]any)
		inner, _ := group["hooks"].([]any)
		for _, h := range inner {
			hook, _ := h.(map[string]any)
			if c, ok := hook["command"].(string); ok {
				out = append(out, c)
			}
		}
	}
	return out
}

func containsSubstr(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// statusLineCommand — 객체형·문자열형 두 모양을 모두 읽는다(설치기·제거기와 같은 폭).
func statusLineCommand(t *testing.T, path string) (string, bool) {
	t.Helper()
	root := readJSONMap(t, path)
	switch sl := root["statusLine"].(type) {
	case string:
		return sl, true
	case map[string]any:
		c, _ := sl["command"].(string)
		return c, true
	case nil:
		return "", false
	default:
		return "", true
	}
}

func TestInstallScriptRoundTrip(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("install.sh 는 linux/darwin 전용이다")
	}
	for _, need := range []string{"sh", "curl"} {
		if _, err := exec.LookPath(need); err != nil {
			t.Skipf("%s 가 없다", need)
		}
	}
	if !jsonToolAvailable() {
		t.Skip("jq/python3/node 가 없다 — 그 환경에서 스크립트가 멈추는 성질은 아래 테스트가 본다")
	}

	// 서버: 수집기 다운로드와 보고만 답한다. httptest 는 127.0.0.1 이라 https 강제를 통과한다.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/agent/collector") {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte(fakeCollectorScript))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	home := t.TempDir()
	settings := filepath.Join(home, ".claude", "settings.json")
	agySettings := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	agyHooks := filepath.Join(home, ".gemini", "config", "hooks.json")
	cfg := filepath.Join(home, ".config", "claude-usage", "config.env")
	bin := filepath.Join(home, ".local", "bin", "usage-collector")

	// 설치 전 상태: 남의 훅 · theme · 남의 상태줄 · 남의 Stop 훅.
	writeFileAt(t, settings, `{
  "theme": "dark",
  "hooks": {
    "SessionEnd": [ { "hooks": [ { "type": "command", "command": "`+foreignHookCmd+`" } ] } ],
    "PreToolUse": [ { "hooks": [ { "type": "command", "command": "echo 남의-pretooluse" } ] } ]
  }
}`)
	writeFileAt(t, agySettings, `{"theme":"solarized","statusLine":{"type":"command","command":"`+foreignStatusCmd+`","padding":1}}`)
	writeFileAt(t, agyHooks, `{"`+foreignNS+`":{"Stop":[{"type":"command","command":"echo 남의-stop"}]}}`)

	path := os.Getenv("PATH")

	// ── 설치 ────────────────────────────────────────────────────────────────
	out, err := runInstallSh(t, home, path, "--key", "uu_ing_test", "--server", srv.URL)
	if err != nil {
		t.Fatalf("설치 실패: %v\n%s", err, out)
	}
	if cmds := sessionEndCommands(t, settings); !containsSubstr(cmds, "claude-usage/config.env") {
		t.Fatalf("설치했는데 우리 훅이 없다: %v", cmds)
	}
	if sl, _ := statusLineCommand(t, agySettings); !strings.Contains(sl, "-antigravity-statusline") {
		t.Fatalf("설치했는데 우리 statusLine 이 아니다: %q", sl)
	}
	cfgBody, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatalf("config.env 가 없다: %v", err)
	}
	if !strings.Contains(string(cfgBody), foreignStatusCmd) {
		t.Fatalf("기존 상태줄이 AGY_PREV_STATUSLINE 으로 보관되지 않았다:\n%s", cfgBody)
	}

	// ── 제거 ────────────────────────────────────────────────────────────────
	out, err = runInstallSh(t, home, path, "--uninstall")
	if err != nil {
		t.Fatalf("제거 실패: %v\n%s", err, out)
	}

	// ① 우리 것은 빠졌다.
	cmds := sessionEndCommands(t, settings)
	if containsSubstr(cmds, "claude-usage/config.env") || containsSubstr(cmds, "usage-collector") {
		t.Fatalf("제거 뒤에도 우리 훅이 남았다: %v", cmds)
	}
	// ② 남의 것은 그대로다 — 이게 이 테스트의 핵심이다.
	if !containsSubstr(cmds, foreignHookCmd) {
		t.Fatalf("남의 SessionEnd 훅이 사라졌다: %v", cmds)
	}
	root := readJSONMap(t, settings)
	if root["theme"] != "dark" {
		t.Fatalf("theme 이 사라졌다: %v", root["theme"])
	}
	if hooks, _ := root["hooks"].(map[string]any); hooks["PreToolUse"] == nil {
		t.Fatalf("남의 PreToolUse 훅이 사라졌다: %v", root["hooks"])
	}
	// ③ statusLine 은 **원래 명령으로 복원**된다. 그냥 지우면 남의 상태줄이 사라진다.
	if sl, ok := statusLineCommand(t, agySettings); !ok || sl != foreignStatusCmd {
		t.Fatalf("statusLine 이 복원되지 않았다: %q (기대 %q)", sl, foreignStatusCmd)
	}
	if agy := readJSONMap(t, agySettings); agy["theme"] != "solarized" {
		t.Fatalf("Antigravity 의 형제 키가 사라졌다: %v", agy)
	}
	// ④ hooks.json 은 우리 네임스페이스만 빠지고 형제 키는 남는다.
	nsMap := readJSONMap(t, agyHooks)
	if _, ok := nsMap["claude-usage"]; ok {
		t.Fatalf("claude-usage 네임스페이스가 남았다: %v", nsMap)
	}
	if _, ok := nsMap[foreignNS]; !ok {
		t.Fatalf("남의 네임스페이스 %s 가 사라졌다: %v", foreignNS, nsMap)
	}
	// ⑤ 설정·바이너리는 지워진다(키 폐기).
	if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Fatalf("config.env 가 남았다(키가 디스크에 남는다): %v", err)
	}
	if _, err := os.Stat(bin); !os.IsNotExist(err) {
		t.Fatalf("수집기 바이너리가 남았다: %v", err)
	}

	// ── 멱등: 두 번째 제거는 **한 바이트도** 바꾸지 않는다 ───────────────────
	snapshot := map[string][]byte{}
	for _, p := range []string{settings, agySettings, agyHooks} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("읽기 실패 %s: %v", p, err)
		}
		snapshot[p] = b
	}
	if out, err := runInstallSh(t, home, path, "--uninstall"); err != nil {
		t.Fatalf("2회차 제거가 실패했다(멱등이 아니다): %v\n%s", err, out)
	}
	for p, want := range snapshot {
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("읽기 실패 %s: %v", p, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("2회차 제거가 %s 를 바꿨다:\n--- 이전\n%s\n--- 이후\n%s", p, want, got)
		}
	}

	// ── 설치 안 된 HOME 에서 제거해도 깨지지 않는다 ─────────────────────────
	clean := t.TempDir()
	if out, err := runInstallSh(t, clean, path, "--uninstall"); err != nil {
		t.Fatalf("미설치 상태 제거가 실패했다: %v\n%s", err, out)
	}
	if entries, err := os.ReadDir(clean); err != nil || len(entries) != 0 {
		t.Fatalf("미설치 상태 제거가 파일을 만들었다: %v (err=%v)", entries, err)
	}
}

/*
 * JSON 도구(jq|python3|node)가 하나도 없으면 제거는 **한 바이트도 건드리지 않고 멈춘다.**
 * 남의 settings.json 을 깨뜨리느니 제거를 포기하는 쪽이다 — 설치기와 같은 판단이고, 이 성질이
 * 무너지면 "제거했더니 설정이 죽었다"가 된다.
 */
func TestInstallScriptUninstallStopsWithoutJSONTool(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("install.sh 는 linux/darwin 전용이다")
	}
	// jq/python3/node 만 빠진 최소 도구 상자를 만들어 PATH 로 준다.
	binDir := t.TempDir()
	for _, name := range []string{"sh", "sed", "mktemp", "rm", "cp", "mv", "rmdir", "mkdir", "cat", "uname", "chmod"} {
		p, err := exec.LookPath(name)
		if err != nil {
			t.Skipf("%s 가 없어 '도구 없는 환경'을 만들 수 없다", name)
		}
		if err := os.Symlink(p, filepath.Join(binDir, name)); err != nil {
			t.Fatalf("symlink %s: %v", name, err)
		}
	}

	home := t.TempDir()
	settings := filepath.Join(home, ".claude", "settings.json")
	body := `{"theme":"dark","hooks":{"SessionEnd":[{"hooks":[{"type":"command","command":"sh -c '. \"$HOME/.config/claude-usage/config.env\" && exec \"$COLLECTOR_BIN\"'"}]}]}}`
	writeFileAt(t, settings, body)

	out, err := runInstallSh(t, home, binDir, "--uninstall")
	if err == nil {
		t.Fatalf("JSON 도구 없이 제거가 성공했다(기대: 중단)\n%s", out)
	}
	if !strings.Contains(out, "jq/python3/node") {
		t.Fatalf("중단 이유를 말하지 않았다:\n%s", out)
	}
	got, err := os.ReadFile(settings)
	if err != nil {
		t.Fatalf("settings.json 이 사라졌다: %v", err)
	}
	if !bytes.Equal(got, []byte(body)) {
		t.Fatalf("멈춘다면서 파일을 바꿨다:\n%s", got)
	}
	if _, err := os.Stat(settings + ".bak"); !os.IsNotExist(err) {
		t.Fatalf(".bak 이 생겼다 — 파일에 손댔다는 뜻이다: %v", err)
	}
}

/*
 * **우리 것이 아닌 statusLine 은 건드리지 않는다.**
 *
 * 왜 별도 테스트인가: 왕복 테스트는 "우리 statusLine → 원래 명령 복원"만 본다. 그 경로에서는
 * 항상 우리 것이 먼저 박혀 있으므로 "남의 것을 만나면 물러난다"는 판정이 한 번도 실행되지
 * 않는다. 실제 회귀는 그쪽에서 난다 — 사람이 우리 설치 뒤에 자기 상태줄로 바꿔 놓았고,
 * 제거가 그걸 우리 것으로 착각해 지우는 경우다. 그러면 사용자는 제거의 대가로 상태줄을 잃는다.
 *
 * 그리고 그때도 **바이너리·키는 지워져야 한다.** "남의 것이라 물러났다"가 제거 전체의 중단으로
 * 번지면, 키가 디스크에 남은 채 제거했다고 믿는 상태가 된다.
 */
func TestInstallScriptUninstallLeavesForeignStatusLine(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("install.sh 는 linux/darwin 전용이다")
	}
	if !jsonToolAvailable() {
		t.Skip("jq/python3/node 가 없다")
	}

	home := t.TempDir()
	agySettings := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	agyHooks := filepath.Join(home, ".gemini", "config", "hooks.json")
	cfg := filepath.Join(home, ".config", "claude-usage", "config.env")
	bin := filepath.Join(home, ".local", "bin", "usage-collector")

	// 우리 것이 한 번도 박힌 적 없는(또는 사람이 되돌려 놓은) statusLine + 남의 Stop 훅.
	agyBody := `{"statusLine":{"type":"command","command":"` + foreignStatusCmd + `","padding":1}}`
	hooksBody := `{"` + foreignNS + `":{"Stop":[{"type":"command","command":"echo 남의-stop"}]}}`
	writeFileAt(t, agySettings, agyBody)
	writeFileAt(t, agyHooks, hooksBody)
	// 설정·바이너리는 있다 — 제거는 이쪽 일은 끝까지 해야 한다.
	writeFileAt(t, cfg, "SERVER='http://127.0.0.1:1'\nKEY='uu_ing_secret'\nCOLLECTOR_BIN='"+bin+"'\n")
	writeFileAt(t, bin, "#!/bin/sh\nexit 0\n")

	out, err := runInstallSh(t, home, os.Getenv("PATH"), "--uninstall")
	if err != nil {
		t.Fatalf("제거가 실패했다: %v\n%s", err, out)
	}

	// ① 남의 statusLine 은 **한 바이트도** 달라지지 않는다.
	got, err := os.ReadFile(agySettings)
	if err != nil {
		t.Fatalf("읽기 실패: %v", err)
	}
	if !bytes.Equal(got, []byte(agyBody)) {
		t.Fatalf("남의 statusLine 을 건드렸다:\n--- 이전\n%s\n--- 이후\n%s", agyBody, got)
	}
	if _, err := os.Stat(agySettings + ".bak"); !os.IsNotExist(err) {
		t.Fatalf(".bak 이 생겼다 — 파일에 손댔다는 뜻이다: %v", err)
	}
	// 물러났다는 사실을 **말해야** 한다. 조용히 넘어가면 사용자는 왜 남았는지 알 수 없다.
	if !strings.Contains(out, "우리 것이 아니다") {
		t.Fatalf("남의 statusLine 을 남긴 이유를 말하지 않았다:\n%s", out)
	}
	// ② 남의 네임스페이스도 그대로다(우리 키가 없으니 hooks.json 은 무변경).
	if got, err := os.ReadFile(agyHooks); err != nil || !bytes.Equal(got, []byte(hooksBody)) {
		t.Fatalf("남의 hooks.json 이 달라졌다: %s (err=%v)", got, err)
	}
	// ③ 그래도 키·바이너리는 지워진다.
	if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Fatalf("config.env 가 남았다(키가 디스크에 남는다): %v", err)
	}
	if _, err := os.Stat(bin); !os.IsNotExist(err) {
		t.Fatalf("수집기 바이너리가 남았다: %v", err)
	}
}

/*
 * ── 사본 동기 가드 ──────────────────────────────────────────────────────────
 *
 * install.sh 는 레포에 **두 벌** 있다:
 *
 *   scripts/install.sh                 ← 진실의 출처. 사람이 고치는 곳.
 *   go/internal/httpapi/install.sh     ← //go:embed 대상. scripts/build.sh 가 위 파일을 cp 로 덮는다.
 *
 * 두 벌인 이유는 go:embed 가 패키지 밖(../..)을 못 읽기 때문이고, 그래서 사본은 **빌드
 * 산출물**이다. 문제는 사본이 원본과 바이트 단위로 같아서 placeholder 처럼 보이지 않는다는
 * 것이다 — 누가 사본만 고쳐도 **다음 빌드까지는 조용히 잘 돌아간다.** 그러다 빌드가 원본으로
 * 덮으면 그 작업은 흔적도 없이 사라진다. 실제로 한 번 사라졌다(제거 경로 전체가).
 *
 * 그래서 여기서 빨간불을 낸다. 이 테스트가 실패한다는 것은 "둘이 갈렸다"는 뜻이고, 고칠 곳은
 * 항상 원본이다. 비교 대상은 파일이 아니라 **임베드된 바이트**(installScript)다 — 실제로
 * 서빙되는 것이 그것이므로, 임베드 경로가 바뀌어도 이 가드는 계속 유효하다.
 */
func TestEmbeddedInstallScriptMatchesSource(t *testing.T) {
	src, err := os.ReadFile(installScriptSource)
	if err != nil {
		t.Fatalf("원본 install.sh 를 읽지 못했다 (%s): %v", installScriptSource, err)
	}
	if bytes.Equal(src, installScript) {
		return
	}
	t.Fatalf(`임베드된 install.sh 가 원본과 다르다 — 이대로 빌드하면 한쪽 작업이 사라진다.

  원본(고칠 곳): %s            %d bytes
  사본(빌드 산출물): go/internal/httpapi/install.sh   %d bytes

고치는 방법:
  · 원본을 고쳤다면 → 사본을 다시 만든다:
      cp scripts/install.sh go/internal/httpapi/install.sh     (scripts/build.sh 가 하는 일)
  · 사본을 고쳤다면 → **그 수정은 다음 빌드에 사라진다.** 원본으로 옮겨라.`,
		installScriptSource, len(src), len(installScript))
}
