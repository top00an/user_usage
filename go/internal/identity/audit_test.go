package identity

import (
	"context"
	"testing"
)

/*
 * 감사 로그 — "왜 어제 보던 이름이 오늘 다르지"에 답하는 표.
 *
 * 이 서비스에서 사람이 데이터를 바꾸는 경로는 귀속 교정 하나뿐이고, 그 한 동작이 과거 행
 * 수천 개의 username 을 재스탬프한다.
 */

func TestAuditLogRoundTrip(t *testing.T) {
	ctx := fresh(t)
	rec := AuditLog(ctx, "admin", "usage.identity.set", "pc-1",
		map[string]any{"username": "user-b", "moved": map[string]any{"sessions": 3}})

	if rec.At == "" || rec.Actor != "admin" || rec.Action != "usage.identity.set" {
		t.Fatalf("%+v", rec)
	}
	if rec.Target == nil || *rec.Target != "pc-1" {
		t.Fatalf("target=%v", rec.Target)
	}

	entries := AuditRecent(ctx, 0)
	if len(entries) != 1 {
		t.Fatalf("%+v", entries)
	}
	e := entries[0]
	if e.Actor != "admin" || e.Action != "usage.identity.set" {
		t.Fatalf("%+v", e)
	}
	// detail 은 값으로 다시 펼쳐진다(컬럼으로 펴면 새 동작마다 마이그레이션이 필요해진다).
	m, ok := e.Detail.(map[string]any)
	if !ok || m["username"] != "user-b" {
		t.Fatalf("detail=%#v", e.Detail)
	}
}

// 행위자를 모르면 'system' 이다 — 빈 칸으로 남기지 않는다.
func TestAuditLogDefaultsActorToSystem(t *testing.T) {
	ctx := fresh(t)
	rec := AuditLog(ctx, "", "usage.identity.remove", "pc-1", nil)
	if rec.Actor != "system" {
		t.Fatalf("actor=%q", rec.Actor)
	}
	if rec.Detail != nil {
		t.Fatalf("extra 가 없으면 detail 도 없다: %v", *rec.Detail)
	}
}

// 최신순이고 상한이 걸린다.
func TestAuditRecentIsNewestFirstAndCapped(t *testing.T) {
	ctx := fresh(t)
	for _, target := range []string{"pc-1", "pc-2", "pc-3"} {
		AuditLog(ctx, "admin", "usage.identity.set", target, nil)
	}
	entries := AuditRecent(ctx, 2)
	if len(entries) != 2 {
		t.Fatalf("limit 이 안 걸렸다: %d", len(entries))
	}
	if entries[0].Target == nil || *entries[0].Target != "pc-3" {
		t.Fatalf("최신순이 아니다: %+v", entries[0])
	}
}

/*
 * **기록 실패가 본 동작을 되돌리지 않는다.**
 *
 * 감사 기록 실패가 귀속 교정을 무너뜨리면 사람은 로그를 남기지 않으려고 기능을 피하게 된다.
 * 그래서 AuditLog 는 오류를 돌려주지 않고, DB 가 없어도 레코드는 만들어 낸다(흔적은 stderr).
 */
func TestAuditLogNeverFailsTheCaller(t *testing.T) {
	prev := handle
	handle = nil
	defer func() { handle = prev }()

	rec := AuditLog(context.Background(), "admin", "usage.identity.set", "pc-1", nil)
	if rec.Action != "usage.identity.set" {
		t.Fatalf("DB 가 없어도 레코드는 성립해야 한다: %+v", rec)
	}
	// 조회도 죽지 않고 빈 목록이다 — 감사 화면이 없다고 서비스가 죽을 이유는 없다.
	if got := AuditRecent(context.Background(), 10); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}
