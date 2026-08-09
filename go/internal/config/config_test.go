package config

import (
	"strings"
	"testing"
)

// 이 파일이 재는 것은 단 하나다: **잘못된 설정으로는 뜨지 않는다.**
//
// 여섯 가지 거부 조건은 전부 "안 걸면 조용한 사고"다 — 서버는 뜨고 로그도 초록색인데
// 무인증으로 열려 있거나(토큰 없음), 게이트가 있다는 착각만 남거나(짧은 토큰·중복 토큰),
// 로컬 파일에 쓰고 있거나(모드 오타), 브라우저에서만 죽는다(bad port).

// ok 는 통과해야 하는 최소 환경이다. 각 테스트가 여기서 한 칸만 비틀어 그 한 칸을 잰다.
func ok() map[string]string {
	return map[string]string{
		"USAGE_ADMIN_TOKEN": "contract-admin-token-0123456789",
	}
}

func mustPass(t *testing.T, env map[string]string) Config {
	t.Helper()
	cfg, errs := Read(env)
	if len(errs) != 0 {
		t.Fatalf("통과해야 하는 설정이 거부됐다: %v", errs)
	}
	return cfg
}

// wantErr 는 거부 이유 문구에 needle 이 들어 있는 에러가 **하나 있는지** 본다.
// 문구까지 보는 이유: 개수만 세면 "다른 이유로 거부됐다"가 통과로 보인다.
func wantErr(t *testing.T, env map[string]string, needle string) {
	t.Helper()
	_, errs := Read(env)
	if len(errs) == 0 {
		t.Fatalf("거부돼야 하는 설정이 통과했다 (기대 문구: %q)", needle)
	}
	for _, e := range errs {
		if strings.Contains(e.Error(), needle) {
			return
		}
	}
	t.Fatalf("거부는 됐지만 이유가 다르다 — %q 를 기대했는데 %v", needle, errs)
}

/* ── ① 토큰 없음 ─────────────────────────────────────────────────────── */

func TestRejectsMissingAdminToken(t *testing.T) {
	wantErr(t, map[string]string{}, "USAGE_ADMIN_TOKEN 이 없다")
}

func TestRejectsBlankAdminToken(t *testing.T) {
	// 공백만 있는 값은 "설정했다"로 보이지만 아무것도 설정되지 않은 것이다.
	wantErr(t, map[string]string{"USAGE_ADMIN_TOKEN": "   "}, "USAGE_ADMIN_TOKEN 이 없다")
}

/* ── ② 토큰이 짧음 ───────────────────────────────────────────────────── */

func TestRejectsShortAdminToken(t *testing.T) {
	env := ok()
	env["USAGE_ADMIN_TOKEN"] = strings.Repeat("a", MinTokenLen-1)
	wantErr(t, env, "USAGE_ADMIN_TOKEN 이 너무 짧다")
}

func TestAcceptsExactlyMinLengthToken(t *testing.T) {
	env := ok()
	env["USAGE_ADMIN_TOKEN"] = strings.Repeat("a", MinTokenLen)
	mustPass(t, env)
}

func TestRejectsShortIntakeToken(t *testing.T) {
	env := ok()
	env["USAGE_INTAKE_TOKEN"] = "short"
	wantErr(t, env, "USAGE_INTAKE_TOKEN 이 너무 짧다")
}

/* ── ③ 두 토큰이 같음 ────────────────────────────────────────────────── */

func TestRejectsIntakeTokenEqualToAdmin(t *testing.T) {
	env := ok()
	env["USAGE_INTAKE_TOKEN"] = env["USAGE_ADMIN_TOKEN"]
	wantErr(t, env, "USAGE_INTAKE_TOKEN 이 USAGE_ADMIN_TOKEN 과 같다")
}

func TestAcceptsDistinctIntakeToken(t *testing.T) {
	env := ok()
	env["USAGE_INTAKE_TOKEN"] = "contract-intake-token-9876543210"
	cfg := mustPass(t, env)
	if cfg.IntakeToken != "contract-intake-token-9876543210" {
		t.Fatalf("IntakeToken = %q", cfg.IntakeToken)
	}
}

/* ── ④ 모드 오타 ─────────────────────────────────────────────────────── */

func TestRejectsUnknownDBMode(t *testing.T) {
	env := ok()
	env["USAGE_DB_MODE"] = "remot" // 오타를 local 로 조용히 접지 않는다
	wantErr(t, env, "USAGE_DB_MODE 가 'remot' 다")
}

func TestModeDefaultsToLocalAndIsNotReadOnly(t *testing.T) {
	cfg := mustPass(t, ok())
	if cfg.Mode != "local" {
		t.Fatalf("Mode = %q, want local", cfg.Mode)
	}
	if cfg.ReadOnly {
		t.Fatal("local 모드가 읽기 전용으로 잡혔다")
	}
}

func TestModeIsCaseInsensitive(t *testing.T) {
	env := ok()
	env["USAGE_DB_MODE"] = "  REMOTE "
	env["DATABASE_URL"] = "postgres://u@127.0.0.1:15432/usage"
	cfg := mustPass(t, env)
	if cfg.Mode != "remote" || !cfg.ReadOnly {
		t.Fatalf("Mode=%q ReadOnly=%v — remote 는 읽기 전용이어야 한다", cfg.Mode, cfg.ReadOnly)
	}
}

/* ── ⑤ remote 인데 DATABASE_URL 없음 ─────────────────────────────────── */

func TestRejectsRemoteWithoutDatabaseURL(t *testing.T) {
	env := ok()
	env["USAGE_DB_MODE"] = "remote"
	wantErr(t, env, "remote 모드인데 DATABASE_URL 이 없다")
}

/* ── ⑥ 브라우저 차단 포트 ────────────────────────────────────────────── */

func TestRejectsBrowserBlockedPort(t *testing.T) {
	// 4190(managesieve)은 WHATWG Fetch 의 bad ports 에 있다. 서버는 뜨고 curl 도 200 인데
	// 브라우저에서만 통째로 죽는 — 진단이 가장 어려운 모양이다.
	env := ok()
	env["USAGE_PORT"] = "4190"
	wantErr(t, env, "브라우저가 차단하는 포트")
}

func TestRejectsEveryWHATWGBadPort(t *testing.T) {
	// 목록 하나가 빠지면 그 포트에서만 조용히 사고가 난다. 전부 밟는다.
	for _, p := range BadPorts() {
		env := ok()
		env["USAGE_PORT"] = itoa(p)
		if _, errs := Read(env); len(errs) == 0 {
			t.Fatalf("bad port %d 가 통과했다", p)
		}
	}
}

func TestDefaultPortIs4191NotThe4190Trap(t *testing.T) {
	cfg := mustPass(t, ok())
	if cfg.Port != 4191 {
		t.Fatalf("기본 포트 = %d, want 4191 (4190 은 브라우저 차단 포트다)", cfg.Port)
	}
}

func TestRejectsNonNumericPort(t *testing.T) {
	env := ok()
	env["USAGE_PORT"] = "http"
	wantErr(t, env, "USAGE_PORT 가 포트 번호가 아니다")
}

func TestRejectsOutOfRangePort(t *testing.T) {
	for _, raw := range []string{"0", "-1", "65536", "8080.5"} {
		env := ok()
		env["USAGE_PORT"] = raw
		wantErr(t, env, "USAGE_PORT 가 포트 번호가 아니다")
	}
}

/* ── 모아서 돌려준다 ─────────────────────────────────────────────────── */

func TestCollectsAllProblemsAtOnce(t *testing.T) {
	// 하나씩 알려주면 고치고 다시 띄우기를 반복하게 된다.
	env := map[string]string{
		"USAGE_ADMIN_TOKEN": "tooshort",
		"USAGE_DB_MODE":     "typo",
		"USAGE_PORT":        "4190",
	}
	_, errs := Read(env)
	if len(errs) < 3 {
		t.Fatalf("문제 3개를 한 번에 돌려줘야 한다 — 받은 것: %v", errs)
	}
}

/* ── 그 밖의 설정 ────────────────────────────────────────────────────── */

func TestHostDefaultsToLoopback(t *testing.T) {
	// 토큰이 있어도 LAN 에 자동 노출하지 않는다.
	cfg := mustPass(t, ok())
	if cfg.Host != "127.0.0.1" {
		t.Fatalf("Host = %q, want 127.0.0.1", cfg.Host)
	}
}

func TestTenantDefaultsToDefault(t *testing.T) {
	cfg := mustPass(t, ok())
	if cfg.Tenant != "default" {
		t.Fatalf("Tenant = %q", cfg.Tenant)
	}
}

func TestKeywordRetentionOffMeansNil(t *testing.T) {
	for _, raw := range []string{"off", "OFF", "no", "false", "0"} {
		env := ok()
		env["USAGE_KEYWORD_RETENTION_DAYS"] = raw
		cfg := mustPass(t, env)
		if cfg.KeywordRetentionDays != nil {
			t.Fatalf("%q → %d, want nil(무기한 보관)", raw, *cfg.KeywordRetentionDays)
		}
	}
}

func TestKeywordRetentionDefaults(t *testing.T) {
	cfg := mustPass(t, ok())
	if cfg.KeywordRetentionDays == nil || *cfg.KeywordRetentionDays != 90 {
		t.Fatalf("KeywordRetentionDays = %v, want 90", cfg.KeywordRetentionDays)
	}
}

func TestKeywordRetentionClampsToStoreBounds(t *testing.T) {
	env := ok()
	env["USAGE_KEYWORD_RETENTION_DAYS"] = "3"
	cfg := mustPass(t, env)
	if cfg.KeywordRetentionDays == nil || *cfg.KeywordRetentionDays != 7 {
		t.Fatalf("3일 → %v, want 7(store 의 하한)", cfg.KeywordRetentionDays)
	}
}

func TestDataDirPassesThrough(t *testing.T) {
	env := ok()
	env["USAGE_DATA_DIR"] = "/tmp/x"
	cfg := mustPass(t, env)
	if cfg.DataDir != "/tmp/x" {
		t.Fatalf("DataDir = %q", cfg.DataDir)
	}
}

func TestHelpMentionsRequiredToken(t *testing.T) {
	// 거부 메시지 뒤에 붙는 안내다. 여기에 토큰 이름이 없으면 사람이 무엇을 걸어야 할지 모른다.
	if !strings.Contains(Help(), "USAGE_ADMIN_TOKEN") {
		t.Fatal("Help() 에 USAGE_ADMIN_TOKEN 안내가 없다")
	}
}

func TestReadIsPure(t *testing.T) {
	// 부작용이 없어야 부팅 거부 판단을 서버 기동 없이 검증할 수 있다.
	env := ok()
	env["USAGE_PORT"] = "4190"
	_, a := Read(env)
	_, b := Read(env)
	if len(a) != len(b) {
		t.Fatalf("같은 env 로 두 번 부른 결과가 다르다: %d vs %d", len(a), len(b))
	}
	if env["USAGE_PORT"] != "4190" {
		t.Fatal("Read 가 넘겨받은 env 를 고쳤다")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
