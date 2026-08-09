package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/tscorp/user-usage/internal/store"
	"github.com/tscorp/user-usage/internal/tenant"
)

// 좌석당 비용 + 기간 비교(±%)를 검증한다: 현재/직전 두 윈도우로 델타가 계산되는가.
func TestSeatsPeriodComparison(t *testing.T) {
	ctx := tenant.With(t.Context(), "default")
	openDB(t)

	today := time.Now().UTC().Format("2006-01-02")
	cur := shiftDayLocal(today, -3)  // 현재 윈도우(days=7) 안
	prev := shiftDayLocal(today, -10) // 직전 윈도우 안

	// alice: 현재·직전 둘 다 활동(델타 계산됨). bob: 현재만(신규).
	seed := func(sid, user, day string, in, out, turns int64) {
		if err := store.SessionUpsert(ctx, store.SessionInput{
			SessionID: sid, Username: user, Machine: "m-" + user, Model: "claude-opus-4-8",
			Input: in, Output: out, Turns: turns,
			StartedAt: day + "T10:00:00.000Z", EndedAt: day + "T11:00:00.000Z",
		}); err != nil {
			t.Fatalf("seed %s: %v", sid, err)
		}
	}
	seed("a-prev", "alice", prev, 1000, 1000, 10) // 직전
	seed("a-cur", "alice", cur, 2000, 2000, 20)   // 현재(늘어남)
	seed("b-cur", "bob", cur, 500, 500, 5)        // 현재만 → 신규

	h := New(testCfg(false))
	rec := do(t, h, http.MethodGet, "/api/usage/seats?days=7", "", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("seats: %d %s", rec.Code, rec.Body.String())
	}
	var resp seatsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("디코드: %v", err)
	}

	if resp.Summary.ActiveSeats != 2 {
		t.Fatalf("현재 활성 좌석=%d, want 2", resp.Summary.ActiveSeats)
	}
	if resp.Summary.PrevActiveSeats != 1 {
		t.Fatalf("직전 활성 좌석=%d, want 1(alice만)", resp.Summary.PrevActiveSeats)
	}

	var alice, bob *seatRowDTO
	for i := range resp.Seats {
		switch resp.Seats[i].Username {
		case "alice":
			alice = &resp.Seats[i]
		case "bob":
			bob = &resp.Seats[i]
		}
	}
	if alice == nil || bob == nil {
		t.Fatalf("alice/bob 없음: %+v", resp.Seats)
	}
	// alice: 직전 활동 있음 → 델타 계산됨(양수, 비용 늘었으므로), 신규 아님.
	if alice.IsNew {
		t.Fatal("alice 는 직전 활동이 있어 신규가 아니어야 한다")
	}
	if alice.UsdDeltaPct == nil {
		t.Fatal("alice 델타가 nil — 직전 대비 증감이 계산돼야 한다")
	}
	if *alice.UsdDeltaPct <= 0 {
		t.Fatalf("alice 비용이 늘었으므로 델타>0 이어야 한다: %v", *alice.UsdDeltaPct)
	}
	// bob: 직전 활동 없음 → 신규, 델타 nil.
	if !bob.IsNew {
		t.Fatal("bob 은 현재만 활동 → 신규여야 한다")
	}
	if bob.UsdDeltaPct != nil {
		t.Fatalf("bob 델타는 nil 이어야 한다(직전 0): %v", *bob.UsdDeltaPct)
	}
	// 좌석당 평균 비용 = 총비용 / 활성좌석.
	if resp.Summary.UsdPerSeat <= 0 {
		t.Fatalf("좌석당 평균 비용이 0: %+v", resp.Summary)
	}
}
