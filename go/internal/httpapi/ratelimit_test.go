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

/*
 * 단일테넌트에서도 **인제스트 키로 들어온 보고**는 rate limit 을 받는다.
 *
 * 인제스트 키는 팀원 PC 마다 복제되는 자격이고, 이제 그 사본 하나로 POST /api/usage 가 열린다.
 * 사본이 새거나 수집기가 루프에 빠지면 쓰기가 무한정 들어오므로 배포 단위 상한을 건다.
 */
func TestSingleTenantIngestKeyRateLimited(t *testing.T) {
	ctx := tenant.With(context.Background(), "default")
	d := openDB(t)
	if err := org.Init(ctx, d); err != nil {
		t.Fatalf("org.Init: %v", err)
	}
	issued, err := org.IssueForTenant(ctx, "default", "default")
	if err != nil {
		t.Fatalf("IssueForTenant: %v", err)
	}

	s := New(testCfg(false)).(*server) // MultiTenant=false
	s.limiter = newRateLimiter(0, 1)   // 버스트 1, 리필 없음
	withKey := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+issued.Plain) }

	if rec := do(t, s, http.MethodPost, "/api/usage", `{"sessions":[]}`, withKey); rec.Code != http.StatusOK {
		t.Fatalf("1번째 인테이크: code=%d (기대 200)", rec.Code)
	}
	if rec := do(t, s, http.MethodPost, "/api/usage", `{"sessions":[]}`, withKey); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("2번째 인테이크: code=%d (기대 429)", rec.Code)
	}
}

/*
 * 단일테넌트의 **cfg 인테이크 토큰** 경로는 종전대로 무제한이다 — 계약 하네스가 시드를 빠르게
 * 쏘므로 여기에 상한을 걸면 골든이 흔들린다. 새로 연 자격(키)만 제한한다.
 */
func TestSingleTenantIntakeTokenNotRateLimited(t *testing.T) {
	openDB(t)
	s := New(testCfg(false)).(*server)
	s.limiter = newRateLimiter(0, 1) // 버스트 1 — 키였다면 2번째에서 429
	for i := 0; i < 5; i++ {
		if rec := do(t, s, http.MethodPost, "/api/usage", `{"sessions":[]}`, withIntake); rec.Code != http.StatusOK {
			t.Fatalf("%d번째 인테이크 토큰 보고: code=%d (기대 200)", i+1, rec.Code)
		}
	}
}
