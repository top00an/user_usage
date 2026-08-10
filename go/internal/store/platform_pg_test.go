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
 * platform 축을 **실제 pg 에서** 검증한다. sqlite 로는 못 잡는 것이 셋이다:
 *   · migrations/pg/0035_platform.sql 이 실제로 적용되는가(sqlite 는 store.Init DDL 이 소유)
 *   · DEFAULT 'claude' 가 컬럼을 안 주고 넣은 행에 걸리는가(하위호환의 근거)
 *   · platform 이 **격리 축이 아니라는 것** — 테넌트 격리는 종전대로 tenant_id + RLS 다.
 *     platform 이 같아도 남의 테넌트 행은 보이지 않아야 한다.
 *
 *   USAGE_TEST_PG_URL='postgres://…' go test ./internal/store -run TestPG -count=1
 *
 * ⚠ URL 은 반드시 비-슈퍼·비-BYPASSRLS 앱 롤이어야 한다(auth_pg_test.go 와 같은 전제).
 */
func TestPGPlatformAxis(t *testing.T) {
	url := os.Getenv("USAGE_TEST_PG_URL")
	if url == "" {
		t.Skip("USAGE_TEST_PG_URL 미설정 — pg platform 테스트 건너뜀")
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

	// 재실행 가능하도록 유니크한 접두사를 쓴다(PK 충돌 방지) — 자기 행만 세는 필터로 쓴다.
	tag := fmt.Sprintf("plat_%d", time.Now().UnixNano())
	ctxA := tenant.With(ctx, "tenant_a")
	ctxB := tenant.With(ctx, "tenant_b")
	day := "2026-08-07"

	// ① 컬럼을 안 주고 넣은 행은 DEFAULT 로 claude 가 된다(현행 수집기의 보고 모양).
	//    SessionInput 을 거치지 않고 **날 SQL 로** 넣어 DEFAULT 자체를 잰다.
	if err := d.Exec(ctxA,
		"INSERT INTO usage_sessions(session_id, username, model, output, started_at)"+
			" VALUES(?,?,?,?,?)", tag+"-legacy", tag, "claude-opus-4-8", 10, day+"T09:00:00.000Z"); err != nil {
		t.Fatalf("legacy insert: %v", err)
	}

	// ② 애플리케이션 경로 — 미지정·허용값·허용목록 밖.
	for _, in := range []SessionInput{
		{SessionID: tag + "-none", Username: tag, Model: "claude-opus-4-8", Output: 1, StartedAt: day + "T10:00:00.000Z"},
		{SessionID: tag + "-codex", Username: tag, Platform: "codex", Model: "gpt-5-codex", Output: 2, StartedAt: day + "T11:00:00.000Z"},
		{SessionID: tag + "-grok", Username: tag, Platform: "grok", Model: "grok-9", Output: 4, StartedAt: day + "T12:00:00.000Z"},
		/*
		 * 허용목록에 나중에 더해진 값(antigravity). **마이그레이션 없이** 들어가야 한다는 것이
		 * 이 줄이 재는 사실이다 — 컬럼은 text 이고 CHECK 제약이 없으므로 값만 늘어난다.
		 * 제약이 생기는 날 이 줄이 먼저 깨진다(운영에서 인테이크가 통째로 죽는 대신).
		 */
		{SessionID: tag + "-agy", Username: tag, Platform: "antigravity", Model: "gemini-3-pro", Output: 8, StartedAt: day + "T13:00:00.000Z"},
	} {
		if err := SessionUpsert(ctxA, in); err != nil {
			t.Fatalf("SessionUpsert(%s): %v", in.SessionID, err)
		}
	}

	got := map[string]string{}
	rows, err := SessionRows(ctxA, Filter{Username: tag})
	if err != nil {
		t.Fatalf("SessionRows: %v", err)
	}
	for _, r := range rows {
		got[r.SessionID] = r.Platform
	}
	want := map[string]string{
		tag + "-legacy": PlatformDefault, // DEFAULT 가 걸렸다
		tag + "-none":   PlatformDefault,
		tag + "-codex":  "codex",
		tag + "-grok":   PlatformOther,
		tag + "-agy":    "antigravity", // 마이그레이션 없이 저장된다
	}
	for id, w := range want {
		if got[id] != w {
			t.Fatalf("%s platform = %q (기대 %q)", id, got[id], w)
		}
	}

	// ③ 필터 — 미지정은 전체, 지정하면 갈린다.
	if n := len(rows); n != 5 {
		t.Fatalf("미지정 필터 행 수 = %d (기대 5)", n)
	}
	only, err := SessionRows(ctxA, Filter{Username: tag, Platform: "codex"})
	if err != nil {
		t.Fatalf("SessionRows(codex): %v", err)
	}
	if len(only) != 1 || only[0].SessionID != tag+"-codex" {
		t.Fatalf("platform 필터: %+v", only)
	}

	// ④ 롤업.
	roll, err := PlatformRollup(ctxA, Filter{Username: tag})
	if err != nil {
		t.Fatalf("PlatformRollup: %v", err)
	}
	sum := map[string]int{}
	for _, r := range roll {
		sum[r.Platform] = r.Sessions
	}
	if sum["claude"] != 2 || sum["codex"] != 1 || sum["antigravity"] != 1 || sum[PlatformOther] != 1 {
		t.Fatalf("롤업: %+v", roll)
	}

	// antigravity 도 조회 필터로 갈린다 — 저장만 되고 못 거르면 화면에서 영원히 안 보인다.
	agy, err := SessionRows(ctxA, Filter{Username: tag, Platform: "antigravity"})
	if err != nil {
		t.Fatalf("SessionRows(antigravity): %v", err)
	}
	if len(agy) != 1 || agy[0].SessionID != tag+"-agy" {
		t.Fatalf("platform=antigravity 필터: %+v", agy)
	}

	/*
	 * ⑤ platform 은 격리 축이 **아니다.** tenant_b 에서 같은 platform 으로 걸러도 tenant_a 의
	 *   행은 한 줄도 보이면 안 된다 — 격리는 tenant_id + RLS 가 한다.
	 */
	leak, err := SessionRows(ctxB, Filter{Username: tag})
	if err != nil {
		t.Fatalf("SessionRows(B): %v", err)
	}
	if len(leak) != 0 {
		t.Fatalf("tenant_b 가 tenant_a 의 행을 봤다 — 크로스테넌트 격리 실패: %+v", leak)
	}
	leakRoll, err := PlatformRollup(ctxB, Filter{Username: tag, Platform: "codex"})
	if err != nil {
		t.Fatalf("PlatformRollup(B): %v", err)
	}
	if len(leakRoll) != 0 {
		t.Fatalf("롤업이 크로스테넌트로 샌다: %+v", leakRoll)
	}
}
