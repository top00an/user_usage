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
 * 계단(롱컨텍스트) 컬럼을 **실제 pg 에서** 검증한다. sqlite 로는 못 잡는 것이 셋이다:
 *   · migrations/pg/0036_long_context.sql 이 실제로 적용되는가(sqlite 는 store.Init DDL 이 소유)
 *   · DEFAULT 0 이 컬럼을 안 주고 넣은 행에 걸리는가(하위호환의 근거 — 기존 행 전량이 이 경로다)
 *   · 계단이 **격리 축이 아니라는 것** — 롱 몫이 같아도 남의 테넌트 행은 보이지 않아야 한다.
 *
 *   USAGE_TEST_PG_URL='postgres://…' go test ./internal/store -run TestPG -count=1
 *
 * ⚠ URL 은 반드시 비-슈퍼·비-BYPASSRLS 앱 롤이어야 한다(auth_pg_test.go 와 같은 전제).
 */
func TestPGLongContextColumns(t *testing.T) {
	url := os.Getenv("USAGE_TEST_PG_URL")
	if url == "" {
		t.Skip("USAGE_TEST_PG_URL 미설정 — pg 계단 컬럼 테스트 건너뜀")
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

	// 재실행 가능하도록 유니크한 접두사를 쓴다(PK 충돌 방지).
	tag := fmt.Sprintf("lc_%d", time.Now().UnixNano())
	ctxA := tenant.With(ctx, "tenant_a")
	ctxB := tenant.With(ctx, "tenant_b")
	day := "2026-08-10"
	at := day + "T09:00:00.000Z"

	// ① 롱 몫을 준 행 · ② 안 준 행(= 구버전 수집기). 둘 다 tenant_a.
	mustSession(t, ctxA, SessionInput{
		SessionID: tag + "_long", Username: tag, Model: "gemini-2.5-pro", Platform: "gemini",
		StartedAt: at, Input: 400000, Output: 40000, CacheRead: 200000,
		InputLong: 300000, OutputLong: 30000, CacheReadLong: 150000,
	})
	mustSession(t, ctxA, SessionInput{
		SessionID: tag + "_flat", Username: tag, Model: "gemini-2.5-pro", Platform: "gemini",
		StartedAt: at, Input: 400000, Output: 40000, CacheRead: 200000,
	})
	// 남의 테넌트에 같은 모양의 행 — 롱 몫이 같아도 새어 나오면 안 된다.
	mustSession(t, ctxB, SessionInput{
		SessionID: tag + "_other", Username: tag, Model: "gemini-2.5-pro", Platform: "gemini",
		StartedAt: at, Input: 999, InputLong: 999,
	})

	rows, err := SessionRows(ctxA, Filter{From: day, To: day, Username: tag})
	if err != nil {
		t.Fatalf("SessionRows: %v", err)
	}
	byID := map[string]Session{}
	for _, r := range rows {
		byID[r.SessionID] = r
	}
	if len(byID) != 2 {
		t.Fatalf("tenant_a 세션 수 = %d, want 2 — 격리가 깨졌거나 행이 사라졌다: %v", len(byID), byID)
	}
	if _, leaked := byID[tag+"_other"]; leaked {
		t.Fatal("남의 테넌트 행이 보인다 — RLS 가 깨졌다")
	}

	if s := byID[tag+"_long"]; s.InputLong != 300000 || s.OutputLong != 30000 || s.CacheReadLong != 150000 {
		t.Fatalf("롱 몫이 pg 왕복에서 사라졌다: %+v", s)
	}
	// DEFAULT 0 — 컬럼을 안 주고 넣은 행이 NULL 이 아니라 0 이다. 이게 하위호환의 전부다.
	if s := byID[tag+"_flat"]; s.InputLong != 0 || s.OutputLong != 0 || s.CacheReadLong != 0 {
		t.Fatalf("안 보낸 행에 롱 몫이 생겼다: %+v", s)
	}

	// 버킷 축도 같은 컬럼을 갖는다(0036 이 두 표를 모두 바꾼다).
	if _, err := SeriesUpsertN(ctxA, SeriesInput{
		SessionID: tag + "_long", Username: tag,
		Rows: []SeriesRow{{
			Hour: day + "T09", Model: "gemini-2.5-pro",
			Input: 400000, Output: 40000, CacheRead: 200000,
			InputLong: 300000, OutputLong: 30000, CacheReadLong: 150000,
		}},
	}); err != nil {
		t.Fatalf("SeriesUpsert: %v", err)
	}
	buckets, err := SeriesOf(ctxA, tag+"_long")
	if err != nil || len(buckets) != 1 {
		t.Fatalf("SeriesOf: %d개 / %v", len(buckets), err)
	}
	if b := buckets[0]; b.InputLong != 300000 || b.OutputLong != 30000 || b.CacheReadLong != 150000 {
		t.Fatalf("버킷 롱 몫이 pg 왕복에서 사라졌다: %+v", b)
	}

	// 플랫폼 롤업의 SUM(*_long) 이 pg 에서도 돈다(sqlite 와 SQL 이 같은 문자열이지만
	// 집계 타입이 방언마다 다르다 — bigint SUM 은 numeric 을 돌려준다).
	rollup, err := PlatformRollup(ctxA, Filter{From: day, To: day, Username: tag})
	if err != nil {
		t.Fatalf("PlatformRollup: %v", err)
	}
	var got PlatformModelRow
	for _, p := range rollup {
		for _, m := range p.Models {
			if p.Platform == "gemini" && m.Model == "gemini-2.5-pro" {
				got = m
			}
		}
	}
	if got.Input != 800000 || got.InputLong != 300000 || got.OutputLong != 30000 || got.CacheReadLong != 150000 {
		t.Fatalf("롤업 합계가 틀렸다: %+v (want input=800000 long=300000/30000/150000)", got)
	}
}
