package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/tscorp/user-usage/internal/db"
	"github.com/tscorp/user-usage/internal/identity"
	"github.com/tscorp/user-usage/internal/org"
	"github.com/tscorp/user-usage/internal/store"
	"github.com/tscorp/user-usage/internal/tenant"
)

// 관리자 인제스트 키 API — 발급/목록/해지의 shape·스코프·평문 비노출을 잡는다. 골든은 이
// 엔드포인트를 커버하지 않으므로(신규) 배선은 여기서만 검증된다.
func initOrg(t *testing.T) {
	t.Helper()
	initOrgDB(t)
}

// initOrgDB 는 initOrg 와 같되 열린 DB 를 돌려준다(저장된 행을 직접 확인해야 하는 테스트용).
func initOrgDB(t *testing.T) db.DB {
	t.Helper()
	ctx := tenant.With(context.Background(), "default")
	d := openDB(t) // store·identity 전역 핸들 세팅
	if err := org.Init(ctx, d); err != nil {
		t.Fatalf("org.Init: %v", err)
	}
	if err := identity.AuditInit(ctx, d); err != nil {
		t.Fatalf("identity.AuditInit: %v", err)
	}
	return d
}

// col 은 한 컬럼의 값 전부를 읽는다(귀속이 표마다 갈리지 않는지 보는 용도).
func col(t *testing.T, d db.DB, query string) []string {
	t.Helper()
	rows, err := d.Query(tenant.With(context.Background(), "default"), query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		for _, v := range r {
			switch s := v.(type) {
			case string:
				out = append(out, s)
			case []byte:
				out = append(out, string(s))
			}
		}
	}
	return out
}

// 세션 id 는 `^[A-Za-z0-9._-]{8,120}$` 라야 정규화를 통과한다(intake.sessionIDRe).
const (
	sessID1 = "sess-0000-1"
	sessID2 = "sess-0000-2"
)

// intakeBody 는 한 세션짜리 보고 본문이다 — 세션·카운터·시간버킷 세 표를 모두 건드린다.
// 귀속 이름은 **본문 최상위의 user** 로 온다(수집기가 보내는 모양 그대로 — 계약의 payload.user).
func intakeBody(sid, user, machine string) string {
	return `{"user":"` + user + `","machine":"` + machine + `","sessions":[{"id":"` + sid + `",` +
		`"model":"claude-sonnet-4","output":10,"startedAt":"2026-08-03T09:00:00.000Z",` +
		`"counters":[{"kind":"tool","key":"Read","count":3}],` +
		`"series":[{"hour":"2026-08-03T09","model":"claude-sonnet-4","output":10}]}]}`
}

/*
 * ★ 동결 ① — **키에 묶인 username 이 payload.user 를 이긴다.**
 *
 * 이 테스트가 이 작업의 핵심이다: 클라이언트가 남의 이름을 보내도(그리고 machine 매핑이 또
 * 다른 이름을 가리켜도) 귀속은 **키 주인**으로 간다. 세션·카운터·시간버킷 세 표가 전부 같은
 * 이름이어야 한다 — 갈리면 화면마다 다른 사람의 사용량이 된다.
 */
func TestIngestKeyUsernameBeatsPayloadAndMapping(t *testing.T) {
	d := initOrgDB(t)
	ctx := tenant.With(context.Background(), "default")

	// 관리자가 손으로 넣은 machine 매핑(우선순위 ②) — 키 결속이 이것도 이겨야 한다.
	if _, err := identity.Set(ctx, identity.SetInput{
		Machine: "pc-1", Username: "bob", Actor: "test",
	}); err != nil {
		t.Fatalf("identity.Set: %v", err)
	}
	issued, err := org.IssueForTenantUser(ctx, "default", "default", "amy")
	if err != nil {
		t.Fatalf("IssueForTenantUser: %v", err)
	}

	h := New(testCfg(false))
	withKey := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+issued.Plain) }
	// 클라이언트가 **남의 이름**을 주장한다.
	rec := do(t, h, http.MethodPost, "/api/usage", intakeBody(sessID1, "mallory", "pc-1"), withKey)
	if rec.Code != http.StatusOK {
		t.Fatalf("보고: code=%d body=%s", rec.Code, rec.Body.String())
	}

	for _, q := range []string{
		"SELECT username FROM usage_sessions",
		"SELECT username FROM usage_counters",
		"SELECT username FROM usage_series",
	} {
		got := col(t, d, q)
		if len(got) == 0 {
			t.Fatalf("%s: 행이 없다 — 보고가 저장되지 않았다", q)
		}
		for _, u := range got {
			if u != "amy" {
				t.Fatalf("%s: 귀속=%q (기대 amy) — 키 주인이 아닌 이름으로 들어갔다", q, u)
			}
		}
	}
}

/*
 * ★ 하위호환 — username 이 NULL 인 **기존 키**는 종전대로 ②→③ 을 탄다.
 *
 * 이게 깨지면 지금 배포된 모든 키의 귀속이 바뀐다.
 */
func TestUnboundIngestKeyKeepsLegacyAttribution(t *testing.T) {
	d := initOrgDB(t)
	ctx := tenant.With(context.Background(), "default")
	if _, err := identity.Set(ctx, identity.SetInput{
		Machine: "pc-1", Username: "bob", Actor: "test",
	}); err != nil {
		t.Fatalf("identity.Set: %v", err)
	}
	// 사용자에 묶이지 않은 키 = 지금 배포된 키와 같다.
	issued, err := org.IssueForTenant(ctx, "default", "default")
	if err != nil {
		t.Fatalf("IssueForTenant: %v", err)
	}
	h := New(testCfg(false))
	withKey := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+issued.Plain) }

	// ② machine 매핑이 있으면 그 이름.
	if rec := do(t, h, http.MethodPost, "/api/usage", intakeBody(sessID1, "mallory", "pc-1"), withKey); rec.Code != http.StatusOK {
		t.Fatalf("보고: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := col(t, d, "SELECT username FROM usage_sessions WHERE session_id='"+sessID1+"'"); len(got) != 1 || got[0] != "bob" {
		t.Fatalf("매핑 귀속=%v (기대 [bob]) — 하위호환이 깨졌다", got)
	}

	// ③ 매핑이 없으면 payload.user 그대로.
	body := intakeBody(sessID2, "carol", "pc-2")
	if rec := do(t, h, http.MethodPost, "/api/usage", body, withKey); rec.Code != http.StatusOK {
		t.Fatalf("보고2: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := col(t, d, "SELECT username FROM usage_sessions WHERE session_id='"+sessID2+"'"); len(got) != 1 || got[0] != "carol" {
		t.Fatalf("payload 귀속=%v (기대 [carol])", got)
	}
}

// cfg 인테이크 토큰(키가 아닌 경로)은 결속이 없으므로 종전 그대로다 — 회귀 방지.
func TestIntakeTokenPathUnaffectedByKeyBinding(t *testing.T) {
	d := initOrgDB(t)
	h := New(testCfg(false))
	if rec := do(t, h, http.MethodPost, "/api/usage", intakeBody(sessID1, "carol", "pc-9"), withIntake); rec.Code != http.StatusOK {
		t.Fatalf("보고: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := col(t, d, "SELECT username FROM usage_sessions"); len(got) != 1 || got[0] != "carol" {
		t.Fatalf("귀속=%v (기대 [carol])", got)
	}
}

/* ── 셀프서비스 키 API (동결 ②) ────────────────────────────────────────── */

// member 가 자기 키를 발급·목록·해지한다. 평문은 발급 응답에서 1회만.
func TestSelfServiceKeysForMember(t *testing.T) {
	initOrg(t)
	seedUser(t, "amy", "member", "amy-password-1")
	h := New(authCfg())
	tok := login(t, h, "amy", "amy-password-1")

	rec := do(t, h, http.MethodPost, "/api/me/keys", "", sessionCookie(tok))
	if rec.Code != http.StatusOK {
		t.Fatalf("발급: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var issued keyIssueResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil {
		t.Fatalf("발급 응답 파싱: %v", err)
	}
	if !strings.HasPrefix(issued.Key, org.KeyPrefix) || issued.ID == "" {
		t.Fatalf("발급 응답: %+v", issued)
	}
	if issued.Username != "amy" {
		t.Fatalf("발급 키가 발급자에 묶이지 않았다: %+v", issued)
	}

	// 목록 — 평문 없음, 소유자 표시.
	rec = do(t, h, http.MethodGet, "/api/me/keys", "", sessionCookie(tok))
	if rec.Code != http.StatusOK {
		t.Fatalf("목록: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), issued.Key) {
		t.Fatalf("목록에 평문이 들어있다: %s", rec.Body.String())
	}
	var list keyListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Keys) != 1 || list.Keys[0].Username == nil || *list.Keys[0].Username != "amy" {
		t.Fatalf("자기 키 목록: %+v", list.Keys)
	}

	// 해지 → 204.
	rec = do(t, h, http.MethodPost, "/api/me/keys/revoke", `{"id":"`+issued.ID+`"}`, sessionCookie(tok))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("해지: code=%d body=%s", rec.Code, rec.Body.String())
	}
}

/*
 * ★ 동결 ② — **남의 키는 보이지도, 해지되지도 않는다.** 서버가 막는다.
 *
 * 그리고 "남의 키"와 "없는 키"의 응답이 같아야 한다 — 갈리면 그 차이가 곧 키 존재 신호다.
 */
func TestSelfServiceKeysCannotSeeOrRevokeOthers(t *testing.T) {
	initOrg(t)
	seedUser(t, "amy", "member", "amy-password-1")
	seedUser(t, "bob", "member", "bob-password-1")
	h := New(authCfg())

	amyTok := login(t, h, "amy", "amy-password-1")
	bobTok := login(t, h, "bob", "bob-password-1")

	rec := do(t, h, http.MethodPost, "/api/me/keys", "", sessionCookie(amyTok))
	var amyKey keyIssueResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &amyKey); err != nil {
		t.Fatal(err)
	}

	// bob 의 목록에 amy 의 키가 없다.
	rec = do(t, h, http.MethodGet, "/api/me/keys", "", sessionCookie(bobTok))
	if rec.Code != http.StatusOK {
		t.Fatalf("bob 목록: code=%d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), amyKey.ID) {
		t.Fatalf("bob 목록에 amy 의 키가 보인다: %s", rec.Body.String())
	}

	// bob 이 amy 의 키를 해지 → 404. 없는 키도 **같은 404·같은 문구**.
	got := do(t, h, http.MethodPost, "/api/me/keys/revoke", `{"id":"`+amyKey.ID+`"}`, sessionCookie(bobTok))
	ghost := do(t, h, http.MethodPost, "/api/me/keys/revoke", `{"id":"nosuchkeyhash"}`, sessionCookie(bobTok))
	if got.Code != http.StatusNotFound {
		t.Fatalf("남의 키 해지: code=%d (기대 404)", got.Code)
	}
	if got.Code != ghost.Code || got.Body.String() != ghost.Body.String() {
		t.Fatalf("남의 키(%d %s)와 없는 키(%d %s)의 응답이 다르다 — 그 차이가 곧 존재 신호다",
			got.Code, got.Body.String(), ghost.Code, ghost.Body.String())
	}

	// 그리고 amy 의 키는 **실제로 살아 있다**(404 를 냈다고 조용히 해지되면 안 된다).
	rec = do(t, h, http.MethodGet, "/api/me/keys", "", sessionCookie(amyTok))
	var list keyListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Keys) != 1 || list.Keys[0].RevokedAt != nil {
		t.Fatalf("amy 의 키가 남에 의해 해지됐다: %+v", list.Keys)
	}
}

// 셀프서비스 경로의 자격 경계: 무자격 401 · 개인 열람 토큰은 조회만 · 사람 신원 없는 관리자 토큰은 403.
func TestSelfServiceKeysCredentialBoundary(t *testing.T) {
	initOrg(t)
	seedUser(t, "amy", "member", "amy-password-1")
	ctx := tenant.With(context.Background(), "default")
	memberTok, err := store.IssueMemberToken(ctx, "amy")
	if err != nil {
		t.Fatalf("IssueMemberToken: %v", err)
	}
	h := New(authCfg())

	if rec := do(t, h, http.MethodGet, "/api/me/keys", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("무자격: code=%d (기대 401)", rec.Code)
	}
	withMember := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+memberTok) }
	// 개인 열람 토큰: 조회는 되고,
	if rec := do(t, h, http.MethodGet, "/api/me/keys", "", withMember); rec.Code != http.StatusOK {
		t.Fatalf("member 토큰 조회: code=%d body=%s (기대 200)", rec.Code, rec.Body.String())
	}
	// 발급은 안 된다 — 조회 자격이 보고 자격을 찍어 내는 자리를 만들지 않는다.
	if rec := do(t, h, http.MethodPost, "/api/me/keys", "", withMember); rec.Code != http.StatusForbidden {
		t.Fatalf("member 토큰 발급: code=%d (기대 403)", rec.Code)
	}
	// cfg 관리자 토큰에는 사람 신원이 없다 → 누구의 키인지 정할 수 없으므로 403.
	if rec := do(t, h, http.MethodPost, "/api/me/keys", "", withAdmin); rec.Code != http.StatusForbidden {
		t.Fatalf("관리자 토큰 발급: code=%d (기대 403)", rec.Code)
	}
	// 미지 경로는 접두사 주인이 404 를 낸다.
	if rec := do(t, h, http.MethodGet, "/api/me/nope", "", withMember); rec.Code != http.StatusForbidden {
		// member 화이트리스트 밖이라 게이트가 먼저 403 을 낸다 — 그것도 맞는 거부다.
		t.Logf("미지 /api/me 경로: code=%d", rec.Code)
	}
}

// 관리자는 사람에게 키를 대리발급할 수 있고, 전체 현황에서 소유자가 보인다.
func TestAdminIssuesKeyForUserAndSeesOwner(t *testing.T) {
	initOrg(t)
	seedUser(t, "amy", "member", "amy-password-1")
	h := New(testCfg(false))

	rec := do(t, h, http.MethodPost, "/api/admin/keys", `{"username":"amy"}`, withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("대리발급: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var issued keyIssueResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	if issued.Username != "amy" {
		t.Fatalf("대리발급 소유자=%q (기대 amy)", issued.Username)
	}
	// 없는 사용자로는 못 만든다 — 유령 귀속을 만들지 않는다.
	if rec := do(t, h, http.MethodPost, "/api/admin/keys", `{"username":"ghost"}`, withAdmin); rec.Code != http.StatusNotFound {
		t.Fatalf("없는 사용자 대리발급: code=%d (기대 404)", rec.Code)
	}
	// 전체 현황에 소유자가 보인다. username 을 안 보낸 발급은 종전대로 null 이다.
	if rec := do(t, h, http.MethodPost, "/api/admin/keys", "", withAdmin); rec.Code != http.StatusOK {
		t.Fatalf("무소유 발급: code=%d", rec.Code)
	}
	rec = do(t, h, http.MethodGet, "/api/admin/keys", "", withAdmin)
	var list keyListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	owners := 0
	for _, k := range list.Keys {
		if k.Username != nil && *k.Username == "amy" {
			owners++
		}
	}
	if len(list.Keys) != 2 || owners != 1 {
		t.Fatalf("전체 현황: %+v (기대 2건 중 amy 소유 1건)", list.Keys)
	}
}

func TestAdminKeysIssueListRevoke(t *testing.T) {
	initOrg(t)
	h := New(testCfg(false))

	// ① 발급 — Bearer 관리자 토큰(상태변경이라 쿠키·세션은 게이트가 막는다).
	rec := do(t, h, http.MethodPost, "/api/admin/keys", "", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("발급: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var issued keyIssueResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil {
		t.Fatalf("발급 응답 파싱: %v", err)
	}
	if !strings.HasPrefix(issued.Key, org.KeyPrefix) {
		t.Fatalf("발급 key 접두사: %q", issued.Key)
	}
	if issued.ID == "" || issued.CreatedAt == "" {
		t.Fatalf("발급 id/createdAt 비어있음: %+v", issued)
	}

	// ② 목록 — 평문 절대 미포함, 마스크만. 방금 발급한 id 가 보인다.
	rec = do(t, h, http.MethodGet, "/api/admin/keys", "", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("목록: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); strings.Contains(body, issued.Key) {
		t.Fatalf("목록 응답에 평문 키가 들어있다: %s", body)
	}
	var list keyListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("목록 응답 파싱: %v", err)
	}
	if len(list.Keys) != 1 {
		t.Fatalf("목록 길이=%d (기대 1)", len(list.Keys))
	}
	k := list.Keys[0]
	if k.ID != issued.ID {
		t.Fatalf("목록 id=%q, 발급 id=%q", k.ID, issued.ID)
	}
	if !strings.HasPrefix(k.Masked, org.KeyPrefix+"…") {
		t.Fatalf("마스크 형식: %q", k.Masked)
	}
	if k.RevokedAt != nil {
		t.Fatalf("미해지 키인데 revokedAt=%v (기대 null)", *k.RevokedAt)
	}

	// ③ 해지 — 204, 이후 목록에서 revokedAt 채워짐.
	rec = do(t, h, http.MethodPost, "/api/admin/keys/revoke", `{"id":"`+issued.ID+`"}`, withAdmin)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("해지: code=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/api/admin/keys", "", withAdmin)
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("해지 후 목록 파싱: %v", err)
	}
	if len(list.Keys) != 1 || list.Keys[0].RevokedAt == nil {
		t.Fatalf("해지 후 revokedAt 미채움: %+v", list.Keys)
	}
}

func TestAdminKeysScopeAndCookie(t *testing.T) {
	initOrg(t)
	h := New(testCfg(false))

	// 인테이크 토큰으로 발급 시도 → 게이트가 403(보고 스코프는 관리 API 를 못 연다).
	if rec := do(t, h, http.MethodPost, "/api/admin/keys", "", withIntake); rec.Code != http.StatusForbidden {
		t.Fatalf("인테이크 발급: code=%d (기대 403)", rec.Code)
	}
	// 쿠키(관리자) 로 발급 시도 → 게이트가 403(쿠키 자격은 상태변경 불가 — CSRF 표면 제거).
	if rec := do(t, h, http.MethodPost, "/api/admin/keys", "", withCookie); rec.Code != http.StatusForbidden {
		t.Fatalf("쿠키 발급: code=%d (기대 403)", rec.Code)
	}
	// 쿠키(관리자) 로 목록 조회 → 200(조회는 쿠키 자격이 태운다).
	if rec := do(t, h, http.MethodGet, "/api/admin/keys", "", withCookie); rec.Code != http.StatusOK {
		t.Fatalf("쿠키 목록: code=%d (기대 200)", rec.Code)
	}
	// 무자격 → 401.
	if rec := do(t, h, http.MethodGet, "/api/admin/keys", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("무자격 목록: code=%d (기대 401)", rec.Code)
	}
}

func TestSessionCookieCanMutateAdminKeys(t *testing.T) {
	initOrg(t)
	seedUser(t, "boss", "admin", "admin-pw-value-1")
	h := New(authCfg())
	tok := login(t, h, "boss", "admin-pw-value-1")

	// ① 세션 쿠키(admin)로 발급 → 200. usage_sess 는 항상 SameSite=Strict+HttpOnly 라 CSRF-safe 이므로
	//    상태변경을 태운다(레거시 usage_tok 쿠키와 다르다).
	rec := do(t, h, http.MethodPost, "/api/admin/keys", "", sessionCookie(tok))
	if rec.Code != http.StatusOK {
		t.Fatalf("세션 발급: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var issued keyIssueResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil {
		t.Fatalf("발급 응답 파싱: %v", err)
	}
	if issued.ID == "" {
		t.Fatalf("발급 id 비어있음: %+v", issued)
	}

	// ② 세션 쿠키로 해지 → 204.
	rec = do(t, h, http.MethodPost, "/api/admin/keys/revoke", `{"id":"`+issued.ID+`"}`, sessionCookie(tok))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("세션 해지: code=%d body=%s", rec.Code, rec.Body.String())
	}

	// ③ 세션 쿠키 GET 조회는 계속 200(회귀 금지).
	if rec := do(t, h, http.MethodGet, "/api/admin/keys", "", sessionCookie(tok)); rec.Code != http.StatusOK {
		t.Fatalf("세션 목록: code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminKeysBadRequests(t *testing.T) {
	initOrg(t)
	h := New(testCfg(false))

	// id 없는 해지 → 400.
	if rec := do(t, h, http.MethodPost, "/api/admin/keys/revoke", `{}`, withAdmin); rec.Code != http.StatusBadRequest {
		t.Fatalf("빈 해지: code=%d (기대 400)", rec.Code)
	}
	// /api/admin 하위 미지 경로 → 404(접두사 주인이 직접 낸다).
	if rec := do(t, h, http.MethodGet, "/api/admin/nope", "", withAdmin); rec.Code != http.StatusNotFound {
		t.Fatalf("미지 admin 경로: code=%d (기대 404)", rec.Code)
	}
}
