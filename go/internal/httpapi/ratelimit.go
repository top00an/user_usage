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
 * ⚠ 이 리미터는 **멀티테넌트 모드에서 인테이크에만** 걸린다. 단일테넌트(골든) 경로는 건드리지
 *   않는다 — 계약 하네스가 시드로 많은 요청을 빠르게 쏘므로 거기에 걸면 골든이 흔들린다.
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
