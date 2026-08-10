package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/tscorp/user-usage/internal/org"
	"github.com/tscorp/user-usage/internal/store"
	"github.com/tscorp/user-usage/internal/tenant"
)

/*
 * 인제스트 키 접두사 게이트 — 무인증 브루트포스가 DB 를 건드리지 못하게 한다.
 *
 * 게이트의 rate limit 은 **인증 성공 후**에 걸리므로 이 경로를 막지 못한다. 접두사가 없는
 * Bearer 를 매번 sha256 + PK 조회로 태우면, 인증에 실패하는 요청이 오히려 DB 를 때리는
 * 증폭 비용이 된다. 여기 테스트는 그 호출이 **0회**임을 세서 못박는다.
 */

// countResolve 는 resolveIngestKey 호출 횟수를 세는 훅을 끼운다(실제 해석은 그대로 위임).
// 반환 포인터를 읽으면 "이 요청이 DB 해석을 탔는가"가 숫자로 나온다.
func countResolve(t *testing.T) *int {
	t.Helper()
	prev := resolveIngestKey
	n := 0
	resolveIngestKey = func(ctx context.Context, plaintext string) (string, string, bool, error) {
		n++
		return prev(ctx, plaintext)
	}
	t.Cleanup(func() { resolveIngestKey = prev })
	return &n
}

// 접두사가 없는 Bearer 는 401 이면서 **키 해석을 아예 타지 않는다**(DB 무접촉).
func TestIngestKeyResolveSkippedWithoutPrefix(t *testing.T) {
	ctx := tenant.With(context.Background(), "default")
	d := openDB(t)
	if err := org.Init(ctx, d); err != nil {
		t.Fatalf("org.Init: %v", err)
	}
	h := New(testCfg(false))
	n := countResolve(t)

	// 브루트포스가 쏠 법한 문자열들 — 전부 접두사가 없다.
	for _, bad := range []string{
		"deadbeef",
		"not-a-key",
		"Bearer",                             // 접두사 흉내
		"uu_ing",                             // 접두사 직전까지만 (언더바 빠짐)
		"UU_ING_deadbeef",                    // 대문자 — 접두사는 대소문자를 구분한다
		store.MemberTokenPrefix + "cafebabe", // 열람 토큰 접두사
	} {
		opt := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+bad) }
		if rec := do(t, h, http.MethodPost, "/api/usage", `{"sessions":[]}`, opt); rec.Code != http.StatusUnauthorized {
			t.Fatalf("bearer=%q: code=%d (기대 401)", bad, rec.Code)
		}
	}
	if *n != 0 {
		t.Fatalf("접두사 없는 Bearer 가 org.Resolve 를 %d회 태웠다 — 무인증 브루트포스 증폭", *n)
	}
}

// member(열람) 토큰 요청도 키 해석을 타지 않는다 — DB 왕복 1회가 덤으로 붙던 자리.
func TestMemberTokenSkipsIngestKeyResolve(t *testing.T) {
	ctx := tenant.With(context.Background(), "default")
	d := openDB(t)
	if err := org.Init(ctx, d); err != nil {
		t.Fatalf("org.Init: %v", err)
	}
	tok, err := store.IssueMemberToken(ctx, "alice")
	if err != nil {
		t.Fatalf("IssueMemberToken: %v", err)
	}
	h := New(testCfg(false))
	n := countResolve(t)

	opt := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
	if rec := do(t, h, http.MethodGet, "/api/usage/sessions?days=30", "", opt); rec.Code != http.StatusOK {
		t.Fatalf("member 조회: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if *n != 0 {
		t.Fatalf("member 토큰이 org.Resolve 를 %d회 태웠다 — 불필요한 DB 왕복", *n)
	}
}

/*
 * 수집기 다운로드(GET /api/agent/collector)에도 같은 접두사 게이트가 걸린다.
 *
 * 이 경로는 게이트보다 조건이 나쁘다: **무인증**(게이트 앞)이고 자격을 `?key=` 쿼리로도 받는다 —
 * URL 이라 브루트포스를 스크립팅하기가 더 쉽다. 게이트만 막고 여기를 두면 "막혔다고 생각하는데
 * 안 막힌" 반쪽이 남는다.
 *
 * ⚠ 응답은 접두사 유무로 갈리지 않는다(둘 다 401 + 같은 문구). 메시지가 갈리면 그 차이가
 *   "이 접두사가 맞다"는 신호가 되어 무인증 경로로 서버 상태를 흘린다.
 */
func TestCollectorSkipsResolveWithoutPrefix(t *testing.T) {
	ctx := tenant.With(context.Background(), "default")
	d := openDB(t)
	if err := org.Init(ctx, d); err != nil {
		t.Fatalf("org.Init: %v", err)
	}
	h := New(testCfg(false))
	const target = "/api/agent/collector?os=darwin&arch=arm64"

	// ① 접두사 없는 자격 — 헤더 경로와 ?key= 경로 **둘 다** DB 해석을 타지 않는다.
	n := countResolve(t)
	var wantBody string
	for _, bad := range []string{"deadbeefcafe", "not-a-key", "uu_ing", "UU_ING_deadbeef"} {
		hdr := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+bad) }
		rec := do(t, h, http.MethodGet, target, "", hdr)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("헤더 bearer=%q: code=%d (기대 401)", bad, rec.Code)
		}
		// 응답 본문이 접두사 유무로 갈리지 않는지 — 첫 응답을 기준으로 전부 대조한다.
		if wantBody == "" {
			wantBody = rec.Body.String()
		} else if got := rec.Body.String(); got != wantBody {
			t.Fatalf("bearer=%q 응답 본문이 다르다: %s (기대 %s)", bad, got, wantBody)
		}

		rec = do(t, h, http.MethodGet, target+"&key="+bad, "")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("?key=%q: code=%d (기대 401)", bad, rec.Code)
		}
		if got := rec.Body.String(); got != wantBody {
			t.Fatalf("?key=%q 응답 본문이 다르다: %s (기대 %s)", bad, got, wantBody)
		}
	}
	if *n != 0 {
		t.Fatalf("접두사 없는 자격이 org.Resolve 를 %d회 태웠다 — 무인증 브루트포스 증폭(반쪽 방어)", *n)
	}
	// 접두사가 있는 미지 키는 해석을 타고(정상), 응답은 **위와 완전히 같아야** 한다.
	rec := do(t, h, http.MethodGet, target+"&key="+org.KeyPrefix+"deadbeef", "")
	if rec.Code != http.StatusUnauthorized || rec.Body.String() != wantBody {
		t.Fatalf("접두사 있는 미지 키: code=%d body=%s — 접두사 유무로 응답이 갈리면 그 자체가 신호다",
			rec.Code, rec.Body.String())
	}
	if *n == 0 {
		t.Fatal("접두사 있는 키가 해석을 타지 않았다 — 게이트가 과하게 넓다")
	}
}

// 무회귀 — 유효 키는 여전히 다운로드까지 통과하고(해석을 탄다), 해지 후에는 401.
func TestCollectorPrefixedKeyStillResolves(t *testing.T) {
	ctx := tenant.With(context.Background(), "default")
	d := openDB(t)
	if err := org.Init(ctx, d); err != nil {
		t.Fatalf("org.Init: %v", err)
	}
	issued, err := org.IssueForTenant(ctx, "default", "default")
	if err != nil {
		t.Fatalf("IssueForTenant: %v", err)
	}
	h := New(testCfg(false))
	const target = "/api/agent/collector?os=darwin&arch=arm64"
	n := countResolve(t)
	withKey := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+issued.Plain) }

	/*
	 * 200(바이너리 임베드됨) 또는 503(빌드 전) — agentbin/ 은 .gitignore 대상이라 클린 체크아웃
	 * 에서는 비어 있다. 여기서 200 을 못박으면 **인증과 무관한 이유로** 빨개져 오히려 신호가
	 * 죽는다. 이 테스트가 판정하는 것은 자격 판정이므로 401/404 만 실패로 본다
	 * (TestServeCollector 와 같은 관례). 실제 200 은 실서버 실증에서 확인한다.
	 */
	rec := do(t, h, http.MethodGet, target, "", withKey)
	t.Logf("유효 키(헤더) 다운로드 code=%d · resolve 호출 %d회", rec.Code, *n)
	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("유효 키: code=%d (기대 200 또는 503) — 접두사 게이트가 유효 키까지 막았다", rec.Code)
	}
	if *n == 0 {
		t.Fatal("유효 키가 해석을 타지 않았다")
	}
	// ?key= 경로도 같은 판정.
	rec = do(t, h, http.MethodGet, target+"&key="+issued.Plain, "")
	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("유효 키(?key=): code=%d (기대 200 또는 503)", rec.Code)
	}

	if err := org.RevokeByID(ctx, "default", issued.ID); err != nil {
		t.Fatalf("RevokeByID: %v", err)
	}
	if rec := do(t, h, http.MethodGet, target, "", withKey); rec.Code != http.StatusUnauthorized {
		t.Fatalf("해지 키(헤더): code=%d (기대 401)", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, target+"&key="+issued.Plain, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("해지 키(?key=): code=%d (기대 401)", rec.Code)
	}
}

// 무회귀 — 접두사를 가진 **유효** 키는 그대로 통과하고(해석을 탄다), 해지 후에는 401.
func TestPrefixedIngestKeyStillResolves(t *testing.T) {
	ctx := tenant.With(context.Background(), "default")
	d := openDB(t)
	if err := org.Init(ctx, d); err != nil {
		t.Fatalf("org.Init: %v", err)
	}
	issued, err := org.IssueForTenant(ctx, "default", "default")
	if err != nil {
		t.Fatalf("IssueForTenant: %v", err)
	}
	h := New(testCfg(false))
	n := countResolve(t)
	withKey := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+issued.Plain) }

	if rec := do(t, h, http.MethodPost, "/api/usage", `{"sessions":[]}`, withKey); rec.Code != http.StatusOK {
		t.Fatalf("유효 키 보고: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if *n == 0 {
		t.Fatal("유효 키가 해석을 타지 않았다 — 접두사 게이트가 유효 키까지 막았다")
	}
	if err := org.RevokeByID(ctx, "default", issued.ID); err != nil {
		t.Fatalf("RevokeByID: %v", err)
	}
	if rec := do(t, h, http.MethodPost, "/api/usage", `{"sessions":[]}`, withKey); rec.Code != http.StatusUnauthorized {
		t.Fatalf("해지 키: code=%d (기대 401)", rec.Code)
	}
}
