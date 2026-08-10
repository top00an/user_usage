package config

import (
	"strings"
	"testing"
	"time"
)

// baseEnv 는 거부되지 않는 최소 환경이다(관리자 토큰만 채운다).
func baseEnv() map[string]string {
	return map[string]string{"USAGE_ADMIN_TOKEN": "0123456789abcdef-token"}
}

func TestSessionTTLParsing(t *testing.T) {
	cases := map[string]time.Duration{
		"":     DefaultSessionTTL,
		"30m":  30 * time.Minute,
		"12h":  12 * time.Hour,
		"junk": DefaultSessionTTL,
		"-5m":  DefaultSessionTTL, // 0 이하는 기본값
	}
	for raw, want := range cases {
		env := baseEnv()
		if raw != "" {
			env["USAGE_SESSION_TTL"] = raw
		}
		cfg, errs := Read(env)
		if len(errs) != 0 {
			t.Fatalf("USAGE_SESSION_TTL=%q: 예상 못 한 거부: %v", raw, errs)
		}
		if cfg.SessionTTL != want {
			t.Fatalf("USAGE_SESSION_TTL=%q → %v, want %v", raw, cfg.SessionTTL, want)
		}
	}
}

func TestBootstrapFields(t *testing.T) {
	env := baseEnv()
	env["USAGE_BOOTSTRAP_ADMIN_USER"] = "root"
	env["USAGE_BOOTSTRAP_ADMIN_PASSWORD"] = "  spaced-pw  " // 비밀번호는 트림하지 않는다
	cfg, errs := Read(env)
	if len(errs) != 0 {
		t.Fatalf("예상 못 한 거부: %v", errs)
	}
	if cfg.BootstrapAdminUser != "root" {
		t.Fatalf("user = %q", cfg.BootstrapAdminUser)
	}
	if cfg.BootstrapAdminPassword != "  spaced-pw  " {
		t.Fatalf("비밀번호가 트림됐다: %q", cfg.BootstrapAdminPassword)
	}
	if cfg.BootstrapTenant != DefaultTenant {
		t.Fatalf("tenant = %q, want %q", cfg.BootstrapTenant, DefaultTenant)
	}

	env["USAGE_BOOTSTRAP_TENANT"] = "acme"
	cfg, _ = Read(env)
	if cfg.BootstrapTenant != "acme" {
		t.Fatalf("tenant = %q, want acme", cfg.BootstrapTenant)
	}
}

func TestHelpMentionsNewEnv(t *testing.T) {
	h := Help()
	for _, k := range []string{
		"USAGE_SESSION_TTL",
		"USAGE_BOOTSTRAP_ADMIN_USER",
		"USAGE_BOOTSTRAP_ADMIN_PASSWORD",
		"USAGE_BOOTSTRAP_TENANT",
	} {
		if !strings.Contains(h, k) {
			t.Fatalf("Help 에 %s 안내가 없다", k)
		}
	}
}
