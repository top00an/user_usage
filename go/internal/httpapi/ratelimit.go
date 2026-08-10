package httpapi

import (
	"sync"
	"time"
)

/*
 * 테넌트별 토큰버킷 rate limiter — 멀티테넌트(SaaS) 인테이크 보호.
 *
 * 왜 테넌트 키로 버는가: 한 org 의 수집기가 폭주해도 다른 org 의 인제스트를 굶기지 않게 한다.
 * 버킷은 tenant(=org) 하나에 하나다. 격리와 같은 경계(tenant)를 rate 에도 그대로 쓴다.
 *
 * ⚠ **무엇에 거는지는 이 파일이 정하지 않는다.** 판정은 server.go 의 s.limiter.allow 호출
 *   한 줄에 있다(현재 server.go:286):
 *
 *	auth.Scope == ScopeIntake && (s.cfg.MultiTenant || auth.IngestKey)
 *
 *   즉 **모드와 무관하게** 인제스트 키(uu_ing_…)로 들어온 보고에는 걸리고, 멀티테넌트에서는
 *   인테이크 전부에 걸린다. 열려 있는 것은 **단일테넌트 + cfg 인테이크 토큰** 경로 하나뿐이다 —
 *   계약 하네스가 시드로 많은 요청을 빠르게 쏘므로 거기에 걸면 골든이 흔들린다.
 *   (조건을 고칠 일이 있으면 여기가 아니라 그 줄을 고치고, 이 주석을 같이 맞춘다.)
 */
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	rate    float64 // 초당 리필 토큰 수. <0 이면 무제한, 0 이면 리필 없음(버스트만).
	burst   float64 // 버킷 상한
	now     func() time.Time
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(rate, burst float64) *rateLimiter {
	return &rateLimiter{buckets: map[string]*tokenBucket{}, rate: rate, burst: burst, now: time.Now}
}

// allow 는 key(테넌트)의 버킷에서 토큰 1개를 소비한다. 없으면 false(→ 429).
func (rl *rateLimiter) allow(key string) bool {
	if rl == nil || rl.rate < 0 {
		return true // 무제한(리미터 미설정 포함)
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()

	t := rl.now()
	b := rl.buckets[key]
	if b == nil {
		b = &tokenBucket{tokens: rl.burst, last: t}
		rl.buckets[key] = b
	}
	// 경과 시간만큼 리필(상한 burst).
	if elapsed := t.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = min(rl.burst, b.tokens+elapsed*rl.rate)
		b.last = t
	}
	if b.tokens >= 1 {
		b.tokens -= 1
		return true
	}
	return false
}
