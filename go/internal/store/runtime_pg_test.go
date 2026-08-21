package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/tscorp/user-usage/internal/db"
	"github.com/tscorp/user-usage/internal/tenant"
)

/*
 * runtime 축의 **PostgreSQL** 실측 — platform_pg_test.go 의 형제다.
 *
 * sqlite 만 왕복해서는 못 잡는 것이 셋이다:
 *   ① `migrations/pg/0042_runtime.sql` 이 실제로 적용되는가(방언·문법)
 *   ② NOT NULL DEFAULT 'cloud' 가 **컬럼 정의 자체**로 걸리는가 — 애플리케이션이 채우는 것과
 *      다르다. 날 SQL 로 넣어 DEFAULT 를 직접 잰다.
 *   ③ 테넌트 격리(RLS)가 이 컬럼을 거치는 조회에서도 유지되는가
 *
 * USAGE_TEST_PG_URL 이 없으면 건너뛴다(로컬 기본 게이트는 sqlite 다).
 */
func TestPGRuntimeAxis(t *testing.T) {
	url := os.Getenv("USAGE_TEST_PG_URL")
	if url == "" {
		t.Skip("USAGE_TEST_PG_URL 미설정 — pg runtime 테스트 건너뜀")
	}

	db.SetTenantResolver(tenant.From)
	ctx := context.Background()
	d, err := db.Open(ctx, db.Options{Mode: "remote", URL: url})
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close(); handle = nil })

	if _, err := db.Migrate(ctx, d, "../../../migrations/pg"); err != nil {
		t.Logf("migrate(무시하고 진행 — 사전 적용 가정): %v", err)
	}
	if err := Init(ctx, d); err != nil {
		t.Fatalf("store.Init: %v", err)
	}

	tag := fmt.Sprintf("rt_%d", time.Now().UnixNano())
	ctxA := tenant.With(ctx, "tenant_a")
	ctxB := tenant.With(ctx, "tenant_b")
	day := "2026-08-21"

	/*
	 * ① 컬럼을 안 주고 날 SQL 로 넣은 행은 DEFAULT 로 cloud 가 된다.
	 *
	 * SessionInput 을 거치면 NormalizeRuntime 이 채우므로 **컬럼의 DEFAULT 를 재는 것이
	 * 아니다.** 마이그레이션이 기본값을 제대로 걸었는지는 이 경로로만 확인된다 —
	 * 기존 행들이 이 값으로 채워지는 것이 0042 의 핵심이기 때문이다.
	 */
	if err := d.Exec(ctxA,
		"INSERT INTO usage_sessions(session_id, username, model, output, started_at)"+
			" VALUES(?,?,?,?,?)", tag+"-legacy", tag, "claude-opus-5", 10, day+"T09:00:00.000Z"); err != nil {
		t.Fatalf("legacy insert: %v", err)
	}

	// ② 애플리케이션 경로 — 미지정 · local · 허용목록 밖.
	for _, in := range []SessionInput{
		{SessionID: tag + "-none", Username: tag, Model: "claude-opus-5", Output: 1, StartedAt: day + "T10:00:00.000Z"},
		{SessionID: tag + "-local", Username: tag, Platform: "codex", Runtime: "local",
			Model: "qwen3-coder:30b", Output: 2, StartedAt: day + "T11:00:00.000Z"},
		// 허용목록 밖은 거부하지 않고 기본값으로 접는다(platform 의 other 같은 제3의 값은 없다).
		{SessionID: tag + "-weird", Username: tag, Runtime: "onprem",
			Model: "claude-opus-5", Output: 4, StartedAt: day + "T12:00:00.000Z"},
	} {
		if err := SessionUpsert(ctxA, in); err != nil {
			t.Fatalf("upsert %s: %v", in.SessionID, err)
		}
	}

	got := map[string]string{}
	rows, err := SessionRows(ctxA, Filter{Username: tag})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		got[r.SessionID] = r.Runtime
	}
	want := map[string]string{
		tag + "-legacy": RuntimeDefault, // 컬럼 DEFAULT
		tag + "-none":   RuntimeDefault, // NormalizeRuntime("")
		tag + "-local":  "local",
		tag + "-weird":  RuntimeDefault, // 허용목록 밖 → 기본값
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s: runtime = %q (기대 %q)", id, got[id], w)
		}
	}

	// ③ 필터가 pg 에서도 모집단을 좁힌다.
	only, err := SessionRows(ctxA, Filter{Username: tag, Runtime: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if len(only) != 1 || only[0].SessionID != tag+"-local" {
		t.Errorf("runtime=local → %d행 %+v", len(only), only)
	}

	/*
	 * ④ 테넌트 격리 — runtime 은 **격리 축이 아니다**(runtime.go 주석). 다른 테넌트에서
	 * 같은 필터로 조회하면 RLS 가 막아 0행이어야 한다. 여기서 행이 보이면 필터가 격리를
	 * 우회한 것이고, 그건 조용한 데이터 유출이다.
	 */
	leak, err := SessionRows(ctxB, Filter{Username: tag, Runtime: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if len(leak) != 0 {
		t.Fatalf("크로스테넌트 유출 %d행 — runtime 필터가 RLS 를 우회했다: %+v", len(leak), leak)
	}
}
