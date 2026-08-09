package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/tscorp/user-usage/internal/store"
	"github.com/tscorp/user-usage/internal/tenant"
)

// 팀 롤업: 사용자별 집계가 team_members 매핑으로 팀에 묶이고, 미배정은 "미배정" 그룹으로.
func TestTeamsRollup(t *testing.T) {
	ctx := tenant.With(t.Context(), "default")
	openDB(t)

	day := time.Now().UTC().Format("2006-01-02")
	seed := func(sid, user string, in int64) {
		if err := store.SessionUpsert(ctx, store.SessionInput{
			SessionID: sid, Username: user, Machine: "m", Model: "claude-opus-4-8",
			Input: in, Output: in, Turns: 5,
			StartedAt: day + "T10:00:00.000Z", EndedAt: day + "T11:00:00.000Z",
		}); err != nil {
			t.Fatalf("seed %s: %v", sid, err)
		}
	}
	seed("s-alice", "alice", 1000)
	seed("s-bob", "bob", 2000)
	seed("s-carol", "carol", 500) // 팀 미배정

	// alice·bob 은 platform 팀, carol 은 배정 안 함.
	if err := store.AssignTeam(ctx, "alice", "platform"); err != nil {
		t.Fatalf("assign alice: %v", err)
	}
	if err := store.AssignTeam(ctx, "bob", "platform"); err != nil {
		t.Fatalf("assign bob: %v", err)
	}

	h := New(testCfg(false))
	rec := do(t, h, http.MethodGet, "/api/usage/teams?days=3650", "", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("teams: %d %s", rec.Code, rec.Body.String())
	}
	var resp teamsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("디코드: %v", err)
	}

	var platform, unassigned *teamRowDTO
	for i := range resp.Teams {
		switch resp.Teams[i].Team {
		case "platform":
			platform = &resp.Teams[i]
		case "미배정":
			unassigned = &resp.Teams[i]
		}
	}
	if platform == nil {
		t.Fatalf("platform 팀 없음: %+v", resp.Teams)
	}
	if platform.Members != 2 {
		t.Fatalf("platform 멤버=%d, want 2(alice·bob)", platform.Members)
	}
	if platform.Sessions != 2 {
		t.Fatalf("platform 세션=%d, want 2", platform.Sessions)
	}
	if unassigned == nil || unassigned.Members != 1 {
		t.Fatalf("미배정 그룹에 carol 1명 있어야 한다: %+v", unassigned)
	}
	if platform.UsdPerMember < 0 {
		t.Fatal("멤버당 비용이 음수")
	}
}

// 재배정은 팀을 덮는다(멱등 UPSERT).
func TestAssignTeamReassign(t *testing.T) {
	ctx := tenant.With(t.Context(), "default")
	openDB(t)
	if err := store.AssignTeam(ctx, "alice", "team-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.AssignTeam(ctx, "alice", "team-b"); err != nil {
		t.Fatal(err)
	}
	m, err := store.TeamOf(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m["alice"] != "team-b" {
		t.Fatalf("재배정 실패: alice=%q, want team-b", m["alice"])
	}
}
