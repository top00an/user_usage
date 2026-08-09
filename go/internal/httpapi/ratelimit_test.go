package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/tscorp/user-usage/internal/org"
	"github.com/tscorp/user-usage/internal/tenant"
)

func TestRateLimiterBurstThenRefill(t *testing.T) {
	base := time.Unix(1700000000, 0)
	cur := base
	rl := newRateLimiter(1, 2) // 초당 1 리필, 버스트 2
	rl.now = func() time.Time { return cur }

	if !rl.allow("t") || !rl.allow("t") {
		t.Fatal("버스트 2 는 통과해야 한다")
	}
	if rl.allow("t") {
		t.Fatal("버스트 소진 후엔 거부해야 한다")
	}
	cur = base.Add(time.Second) // 1초 → 1토큰 리필
	if !rl.allow("t") {
		t.Fatal("1초 뒤 1토큰 리필됐어야 한다")
	}
	if rl.allow("t") {
		t.Fatal("리필은 1개뿐이어야 한다")
	}
}

func TestRateLimiterPerTenantIndependent(t *testing.T) {
	rl := newRateLimiter(0, 1) // 리필 없음, 버스트 1
	rl.now = func() time.Time { return time.Unix(1700000000, 0) }
	if !rl.allow("A") {
		t.Fatal("A 첫 요청 통과")
	}
	if rl.allow("A") {
		t.Fatal("A 버스트 소진")
	}
	if !rl.allow("B") {
		t.Fatal("B 는 A 와 독립이라 통과해야 한다 — 한 org 폭주가 남을 굶기면 안 된다")
	}
}

func TestRateLimiterNilIsUnlimited(t *testing.T) {
	var rl *rateLimiter // 단일테넌트: 서버가 limiter 를 nil 로 둔다
	for i := 0; i < 100; i++ {
		if !rl.allow("x") {
			t.Fatal("nil 리미터는 무제한이어야 한다")
		}
	}
}

// 멀티테넌트 인테이크가 버킷 소진 시 429 를 낸다(통합).
func TestMultiTenantRateLimit429(t *testing.T) {
	ctx := tenant.With(context.Background(), "default")
	d := openDB(t)
	if err := org.Init(ctx, d); err != nil {
		t.Fatalf("org.Init: %v", err)
	}
	o, _ := org.CreateOrg(ctx, d, "Acme")
	key, _ := org.IssueKey(ctx, d, o.ID)

	s := New(multiTenantCfg()).(*server)
	s.limiter = newRateLimiter(0, 1) // 버스트 1, 리필 없음
	withKey := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+key) }

	if rec := do(t, s, http.MethodPost, "/api/usage", `{"sessions":[]}`, withKey); rec.Code != http.StatusOK {
		t.Fatalf("1번째 인테이크: code=%d (기대 200)", rec.Code)
	}
	if rec := do(t, s, http.MethodPost, "/api/usage", `{"sessions":[]}`, withKey); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("2번째 인테이크: code=%d (기대 429)", rec.Code)
	}
}
