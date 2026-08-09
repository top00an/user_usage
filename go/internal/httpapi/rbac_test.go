package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tscorp/user-usage/internal/store"
	"github.com/tscorp/user-usage/internal/tenant"
)

// RBAC — 개인 열람 토큰(member): 자기 것만, 타인 불가, 교차 뷰 403, 변경 403.
func TestRBACMemberScope(t *testing.T) {
	ctx := tenant.With(t.Context(), "default")
	openDB(t)

	day := time.Now().UTC().Format("2006-01-02")
	seed := func(sid, user string) {
		if err := store.SessionUpsert(ctx, store.SessionInput{
			SessionID: sid, Username: user, Machine: "m", Model: "claude-opus-4-8",
			Input: 100, Output: 100, Turns: 5,
			StartedAt: day + "T10:00:00.000Z", EndedAt: day + "T11:00:00.000Z",
		}); err != nil {
			t.Fatalf("seed %s: %v", sid, err)
		}
	}
	seed("alice-1", "alice")
	seed("alice-2", "alice")
	seed("bob-1", "bob")

	// alice 개인 토큰 발급.
	aliceTok, err := store.IssueMemberToken(ctx, "alice")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if !strings.HasPrefix(aliceTok, store.MemberTokenPrefix) {
		t.Fatalf("토큰 접두사: %q", aliceTok)
	}

	h := New(testCfg(false))
	withMember := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+aliceTok) }

	// ① 자기 세션만 보인다 — ?user=bob 을 보내도 alice 것만.
	rec := do(t, h, http.MethodGet, "/api/usage/sessions?user=bob&limit=1000", "", withMember)
	if rec.Code != http.StatusOK {
		t.Fatalf("member sessions: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "alice-1") || !strings.Contains(body, "alice-2") {
		t.Fatalf("alice 자기 세션이 안 보인다: %s", body)
	}
	if strings.Contains(body, "bob-1") {
		t.Fatalf("member 가 타인(bob) 세션을 봤다 — 격리 실패: %s", body)
	}

	// ② 교차 뷰(전사/팀)는 403 — deny-by-default.
	for _, p := range []string{
		"/api/usage/summary", "/api/usage/leaderboard", "/api/usage/seats",
		"/api/usage/teams", "/api/usage/dispatch",
	} {
		if rec := do(t, h, http.MethodGet, p, "", withMember); rec.Code != http.StatusForbidden {
			t.Fatalf("member 가 %s 에 접근: %d (기대 403)", p, rec.Code)
		}
	}

	// ③ 상태변경(비-GET)은 403.
	if rec := do(t, h, http.MethodPost, "/api/usage", `{"sessions":[]}`, withMember); rec.Code != http.StatusForbidden {
		t.Fatalf("member 인테이크: %d (기대 403)", rec.Code)
	}

	// ④ 화이트리스트 self 엔드포인트는 200(series·distribution·quality).
	for _, p := range []string{
		"/api/usage/series?days=3650&metric=tokens",
		"/api/usage/distribution?days=3650",
		"/api/usage/quality?days=3650",
	} {
		if rec := do(t, h, http.MethodGet, p, "", withMember); rec.Code != http.StatusOK {
			t.Fatalf("member self 엔드포인트 %s: %d (기대 200)", p, rec.Code)
		}
	}

	// ⑤ 해지된 토큰 → 401.
	if err := store.RevokeMemberToken(ctx, aliceTok); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if rec := do(t, h, http.MethodGet, "/api/usage/sessions", "", withMember); rec.Code != http.StatusUnauthorized {
		t.Fatalf("해지 토큰: %d (기대 401)", rec.Code)
	}
}

// admin 토큰은 종전대로 전부 본다(RBAC 이 관리자 경로를 건드리지 않는다).
func TestRBACAdminUnaffected(t *testing.T) {
	ctx := tenant.With(t.Context(), "default")
	openDB(t)
	if err := store.SessionUpsert(ctx, store.SessionInput{
		SessionID: "s1", Username: "alice", Machine: "m", Model: "claude-opus-4-8", Turns: 1,
	}); err != nil {
		t.Fatal(err)
	}
	h := New(testCfg(false))
	// admin 은 교차 뷰(seats·leaderboard)에 접근 가능.
	for _, p := range []string{"/api/usage/leaderboard", "/api/usage/seats", "/api/usage/summary"} {
		if rec := do(t, h, http.MethodGet, p, "", withAdmin); rec.Code != http.StatusOK {
			t.Fatalf("admin %s: %d (기대 200)", p, rec.Code)
		}
	}
}
