package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tscorp/user-usage/internal/identity"
	"github.com/tscorp/user-usage/internal/org"
	"github.com/tscorp/user-usage/internal/store"
	"github.com/tscorp/user-usage/internal/tenant"
)

/*
 * 사용자 관리 API — **동결 계약 ③의 보안 불변식 8개를 여기서 못박는다.**
 *
 * 이 스위트가 재는 것은 "구현했는가"가 아니라 **"위반이 잡히는가"**다. 아래 각 테스트는 해당
 * 가드를 지우면 빨개진다(그 RED 를 실제로 확인했다 — 보고서의 검증 절 참조).
 *
 * 불변식과 테스트의 대응:
 *   ① 관리자 전용 서버 403      TestAdminUsersAreAdminOnly
 *   ② 마지막 관리자 보호        TestLastAdminCannotBeDemotedOrDeleted
 *   ③ 자기 강등·삭제 금지       TestSelfCannotBeDemotedOrDeleted
 *   ④ 역할변경·삭제 시 세션 무효 TestDemotionAndDeletionKillSessions
 *   ⑤ bcrypt 전용·평문 미노출   TestPasswordNeverLeaves
 *   ⑥ 테넌트 스코프             TestAdminUsersUseRequestTenant (+ org_test.go 의 키 스코프)
 *   ⑦ 감사 로그                 TestUserAdminIsAudited
 *   ⑧ 레거시 쿠키 상태변경 차단  TestLegacyCookieCannotMutateUsers
 */

// adminEnv 는 org·store·identity·감사표를 세운 뒤 세션 로그인이 되는 핸들러를 돌려준다.
func adminEnv(t *testing.T) http.Handler {
	t.Helper()
	initOrgDB(t)
	return New(authCfg())
}

/* ── ① 관리자 전용은 서버가 낸다 ──────────────────────────────────────── */

func TestAdminUsersAreAdminOnly(t *testing.T) {
	h := adminEnv(t)
	seedUser(t, "amy", "member", "amy-password-1")
	memberTok := login(t, h, "amy", "amy-password-1")

	// member 로그인 세션 — 조회도 상태변경도 전부 403.
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/admin/users", ""},
		{http.MethodPost, "/api/admin/users", `{"username":"x","password":"password-x1","role":"admin"}`},
		{http.MethodPost, "/api/admin/users/role", `{"username":"amy","role":"admin"}`},
		{http.MethodPost, "/api/admin/users/delete", `{"username":"amy"}`},
	} {
		rec := do(t, h, tc.method, tc.path, tc.body, sessionCookie(memberTok))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("member %s %s: code=%d (기대 403) body=%s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
	// 인테이크 토큰도 403(보고 스코프는 관리 API 를 못 연다).
	if rec := do(t, h, http.MethodGet, "/api/admin/users", "", withIntake); rec.Code != http.StatusForbidden {
		t.Fatalf("인테이크: code=%d (기대 403)", rec.Code)
	}
	// 무자격 → 401.
	if rec := do(t, h, http.MethodGet, "/api/admin/users", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("무자격: code=%d (기대 401)", rec.Code)
	}
	// 그리고 amy 는 실제로 승격되지 않았다(403 뒤에 조용히 통과한 것이 없다).
	ctx := tenant.With(context.Background(), "default")
	if u, _, _ := store.GetUser(ctx, "amy"); u.Role != "member" {
		t.Fatalf("403 인데 role 이 바뀌었다: %q", u.Role)
	}
	// 관리자는 통과한다 — 라우트 등록 순서(routeOnboarding 보다 앞)의 회귀 방지.
	if rec := do(t, h, http.MethodGet, "/api/admin/users", "", withAdmin); rec.Code != http.StatusOK {
		t.Fatalf("관리자 목록: code=%d body=%s (404 면 라우트 순서가 뒤집혔다)", rec.Code, rec.Body.String())
	}
}

/* ── 정상 경로 ───────────────────────────────────────────────────────── */

func TestAdminUsersCRUD(t *testing.T) {
	h := adminEnv(t)

	// 생성 — role 을 안 보내면 최소 권한(member)이다.
	rec := do(t, h, http.MethodPost, "/api/admin/users",
		`{"username":"amy","password":"amy-password-1"}`, withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("생성: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var created userMutationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !created.OK || created.User.Username != "amy" || created.User.Role != "member" {
		t.Fatalf("생성 응답: %+v", created)
	}
	if created.User.CreatedAt == "" {
		t.Fatalf("createdAt 이 비었다: %+v", created.User)
	}
	if created.User.Team != nil {
		t.Fatalf("미배정 team 은 null 이라야 한다: %v", *created.User.Team)
	}

	// 중복 → 409.
	if rec := do(t, h, http.MethodPost, "/api/admin/users",
		`{"username":"amy","password":"amy-password-2"}`, withAdmin); rec.Code != http.StatusConflict {
		t.Fatalf("중복 생성: code=%d (기대 409)", rec.Code)
	}
	// 짧은 비밀번호 → 400.
	if rec := do(t, h, http.MethodPost, "/api/admin/users",
		`{"username":"bob","password":"short"}`, withAdmin); rec.Code != http.StatusBadRequest {
		t.Fatalf("짧은 비밀번호: code=%d (기대 400)", rec.Code)
	}
	// 알 수 없는 role → 400.
	if rec := do(t, h, http.MethodPost, "/api/admin/users",
		`{"username":"bob","password":"bob-password-1","role":"root"}`, withAdmin); rec.Code != http.StatusBadRequest {
		t.Fatalf("알 수 없는 role: code=%d (기대 400)", rec.Code)
	}
	// username 없음 → 400.
	if rec := do(t, h, http.MethodPost, "/api/admin/users", `{}`, withAdmin); rec.Code != http.StatusBadRequest {
		t.Fatalf("빈 username: code=%d (기대 400)", rec.Code)
	}

	// 팀 배정 — 기존 store.AssignTeam 재사용.
	if rec := do(t, h, http.MethodPost, "/api/admin/users/team",
		`{"username":"amy","team":"플랫폼"}`, withAdmin); rec.Code != http.StatusOK {
		t.Fatalf("팀 배정: code=%d body=%s", rec.Code, rec.Body.String())
	}

	// 목록 — 팀이 함께 나온다.
	rec = do(t, h, http.MethodGet, "/api/admin/users", "", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("목록: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var list userListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Users) != 1 || list.Users[0].Team == nil || *list.Users[0].Team != "플랫폼" {
		t.Fatalf("목록: %+v", list.Users)
	}

	// 없는 사용자 → 404(역할·비밀번호·팀·삭제 전부).
	for _, tc := range []struct{ path, body string }{
		{"/api/admin/users/role", `{"username":"ghost","role":"admin"}`},
		{"/api/admin/users/password", `{"username":"ghost","password":"ghost-password-1"}`},
		{"/api/admin/users/team", `{"username":"ghost","team":"t"}`},
		{"/api/admin/users/delete", `{"username":"ghost"}`},
	} {
		if rec := do(t, h, http.MethodPost, tc.path, tc.body, withAdmin); rec.Code != http.StatusNotFound {
			t.Fatalf("%s 없는 사용자: code=%d (기대 404)", tc.path, rec.Code)
		}
	}

	// 삭제 — 목록에서 사라진다.
	rec = do(t, h, http.MethodPost, "/api/admin/users/delete", `{"username":"amy"}`, withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("삭제: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var deleted userDeleteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &deleted); err != nil {
		t.Fatal(err)
	}
	if !deleted.OK || deleted.Username != "amy" || !deleted.SessionsRevoked {
		t.Fatalf("삭제 응답: %+v", deleted)
	}
	rec = do(t, h, http.MethodGet, "/api/admin/users", "", withAdmin)
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Users) != 0 {
		t.Fatalf("삭제 후 목록: %+v", list.Users)
	}

	// 미지 하위 경로 → 404(접두사 주인이 직접 낸다).
	if rec := do(t, h, http.MethodGet, "/api/admin/users/nope", "", withAdmin); rec.Code != http.StatusNotFound {
		t.Fatalf("미지 경로: code=%d (기대 404)", rec.Code)
	}
}

/* ── ② 마지막 관리자 보호 ────────────────────────────────────────────── */

func TestLastAdminCannotBeDemotedOrDeleted(t *testing.T) {
	h := adminEnv(t)
	seedUser(t, "boss", "admin", "boss-password-1")
	seedUser(t, "amy", "member", "amy-password-1")

	// cfg 관리자 토큰(사람 신원 없음)이라 ③(자기 자신)에는 안 걸린다 — 순수하게 ②만 잰다.
	rec := do(t, h, http.MethodPost, "/api/admin/users/role",
		`{"username":"boss","role":"member"}`, withAdmin)
	if rec.Code != http.StatusConflict {
		t.Fatalf("마지막 관리자 강등: code=%d (기대 409) body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "마지막 관리자") {
		t.Fatalf("이유가 문구에 없다: %s", rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, "/api/admin/users/delete", `{"username":"boss"}`, withAdmin)
	if rec.Code != http.StatusConflict {
		t.Fatalf("마지막 관리자 삭제: code=%d (기대 409)", rec.Code)
	}

	// **거부가 실제로 아무것도 바꾸지 않았다.**
	ctx := tenant.With(context.Background(), "default")
	if u, ok, _ := store.GetUser(ctx, "boss"); !ok || u.Role != "admin" {
		t.Fatalf("거부됐는데 boss 가 바뀌었다: %+v ok=%v", u, ok)
	}

	// 관리자가 둘이 되면 강등이 열린다 — 가드가 과하게 넓지 않다.
	if rec := do(t, h, http.MethodPost, "/api/admin/users/role",
		`{"username":"amy","role":"admin"}`, withAdmin); rec.Code != http.StatusOK {
		t.Fatalf("amy 승격: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPost, "/api/admin/users/role",
		`{"username":"boss","role":"member"}`, withAdmin); rec.Code != http.StatusOK {
		t.Fatalf("둘째 관리자가 있는데 강등이 막혔다: code=%d body=%s", rec.Code, rec.Body.String())
	}
	// member 를 지우는 것은 마지막 관리자 판정과 무관하다.
	if rec := do(t, h, http.MethodPost, "/api/admin/users/delete",
		`{"username":"boss"}`, withAdmin); rec.Code != http.StatusOK {
		t.Fatalf("member 삭제가 막혔다: code=%d body=%s", rec.Code, rec.Body.String())
	}
}

/* ── ② 마지막 관리자 보호 · **동시 요청** (보안검토 H-1) ────────────────
 *
 * 위의 TestLastAdminCannotBeDemotedOrDeleted 는 순차 요청만 잰다. 그 스위트는 **초록인 채로**
 * H-1 을 통과시켰다 — 세는 것(CountAdmins)과 바꾸는 것(SetUserRole/DeleteUser)이 한 트랜잭션이
 * 아니어서, 관리자 2명에게 동시에 강등 2건이 오면 둘 다 n=2 를 읽고 둘 다 통과한다. 결과는
 * **관리자 0명 잠금**이고, 두 응답 모두 200 이며 감사 로그조차 "정상 강등 2건"으로 보인다.
 * 검토자 실측: 유효 10회 중 8회 재현.
 *
 * 그래서 이 회귀 테스트는 **반드시 동시 요청**이다. 순차 테스트로는 이 버그가 잡히지 않는다는
 * 사실 자체가 이 건의 교훈이다.
 *
 * 재는 것은 셋이다:
 *   ① 관리자 수가 **한 번도** 0 이 되지 않는다 (잠금 = 되돌리려면 서버 호스트 접근이 필요하다)
 *   ② 정확히 하나만 200 이고 나머지는 409 다 (둘 다 200 이면 그 순간이 사고다)
 *   ③ 409 를 받은 쪽은 **실제로 아무것도 바꾸지 않았다** (응답만 409 이고 DB 는 바뀐 자리 방지)
 */

// adminsLeft 는 지금 tenant 의 관리자 수다.
func adminsLeft(t *testing.T) int {
	t.Helper()
	n, err := store.CountAdmins(tenant.With(context.Background(), "default"))
	if err != nil {
		t.Fatalf("CountAdmins: %v", err)
	}
	return n
}

// resetTwoAdmins 는 매 시도의 시작 상태를 **단정으로** 세운다 — 관리자 정확히 2명(a1·a2).
// 시작 상태를 확인하지 않으면 "이미 1명이라 409 가 났다"를 "가드가 막았다"로 오독한다.
func resetTwoAdmins(t *testing.T) {
	t.Helper()
	ctx := tenant.With(context.Background(), "default")
	for _, u := range []string{"a1", "a2"} {
		_ = store.DeleteUser(ctx, u)
		if err := store.CreateUserWithPassword(ctx, u, "admin", u+"-password-1"); err != nil {
			t.Fatalf("시작 상태 세팅(%s): %v", u, err)
		}
	}
	if n := adminsLeft(t); n != 2 {
		t.Fatalf("시작 상태가 관리자 2명이 아니다: n=%d", n)
	}
}

// fireConcurrently 는 두 요청을 **같은 순간에** 보낸다(배리어로 출발을 맞춘다). 돌아온 상태코드를
// 보낸 순서 그대로 돌려준다.
func fireConcurrently(t *testing.T, h http.Handler, path string, bodies [2]string) [2]int {
	t.Helper()
	var start, done sync.WaitGroup
	start.Add(1)
	done.Add(2)
	var codes [2]int
	for i, body := range bodies {
		go func(i int, body string) {
			defer done.Done()
			start.Wait() // 배리어 — 둘이 같이 출발한다
			r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			withAdmin(r)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			codes[i] = w.Code
		}(i, body)
	}
	start.Done()
	done.Wait()
	return codes
}

// concurrentTrials — 검토자가 "유효 10회 중 8회" 재현이라고 했으므로 그보다 넉넉히 돈다.
// 고쳐진 코드에서는 결정적으로 초록이다(직렬화되므로 확률에 기대지 않는다).
const concurrentTrials = 30

func TestConcurrentDemotionCannotLockOutAllAdmins(t *testing.T) {
	h := adminEnv(t)
	locked, bothOK := 0, 0
	for trial := 1; trial <= concurrentTrials; trial++ {
		resetTwoAdmins(t)
		codes := fireConcurrently(t, h, "/api/admin/users/role", [2]string{
			`{"username":"a1","role":"member"}`,
			`{"username":"a2","role":"member"}`,
		})
		left := adminsLeft(t)
		if left == 0 {
			locked++
			t.Errorf("시도 %d: 응답=%v 남은 관리자=0 ★ 관리자 0명 잠금 — 아무도 로그인할 수 없다",
				trial, codes)
		}
		if codes[0] == http.StatusOK && codes[1] == http.StatusOK {
			bothOK++
			t.Errorf("시도 %d: 동시 강등 2건이 **둘 다 200** 이다 — 두 응답 다 정상으로 보인다", trial)
		}
		// ② 정확히 하나만 통과한다. 관리자 2명 중 하나를 강등하면 남은 하나는 마지막 관리자다.
		if (codes[0] == http.StatusOK) == (codes[1] == http.StatusOK) {
			t.Errorf("시도 %d: 200 이 정확히 하나가 아니다: %v (남은=%d)", trial, codes, left)
		}
		if left != 1 {
			t.Errorf("시도 %d: 남은 관리자=%d (기대 1) 응답=%v", trial, left, codes)
		}
	}
	if locked > 0 || bothOK > 0 {
		t.Fatalf("==== 유효 시도 %d 회 중 잠금 %d 회 · 둘 다 200 %d 회 ====",
			concurrentTrials, locked, bothOK)
	}
}

func TestConcurrentDeletionCannotLockOutAllAdmins(t *testing.T) {
	h := adminEnv(t)
	locked := 0
	for trial := 1; trial <= concurrentTrials; trial++ {
		resetTwoAdmins(t)
		codes := fireConcurrently(t, h, "/api/admin/users/delete", [2]string{
			`{"username":"a1"}`,
			`{"username":"a2"}`,
		})
		left := adminsLeft(t)
		if left == 0 {
			locked++
			t.Errorf("시도 %d: 응답=%v 남은 관리자=0 ★ 관리자 0명 잠금", trial, codes)
		}
		if (codes[0] == http.StatusOK) == (codes[1] == http.StatusOK) {
			t.Errorf("시도 %d: 200 이 정확히 하나가 아니다: %v (남은=%d)", trial, codes, left)
		}
		// ③ 409 를 받은 쪽은 실제로 지워지지 않았다 — 응답만 409 이고 DB 는 바뀐 자리를 막는다.
		ctx := tenant.With(context.Background(), "default")
		survivor := "a2"
		if codes[1] == http.StatusOK {
			survivor = "a1"
		}
		if u, ok, _ := store.GetUser(ctx, survivor); !ok || u.Role != "admin" {
			t.Errorf("시도 %d: 409 를 받은 %s 가 실제로는 사라졌다(ok=%v role=%q)", trial, survivor, ok, u.Role)
		}
	}
	if locked > 0 {
		t.Fatalf("==== 유효 시도 %d 회 중 잠금 %d 회 ====", concurrentTrials, locked)
	}
}

/* ── ③ 자기 자신 강등·삭제 금지 ─────────────────────────────────────── */

func TestSelfCannotBeDemotedOrDeleted(t *testing.T) {
	h := adminEnv(t)
	seedUser(t, "boss", "admin", "boss-password-1")
	seedUser(t, "vice", "admin", "vice-password-1") // ②에 안 걸리게 관리자를 둘로 둔다
	tok := login(t, h, "boss", "boss-password-1")

	rec := do(t, h, http.MethodPost, "/api/admin/users/role",
		`{"username":"boss","role":"member"}`, sessionCookie(tok))
	if rec.Code != http.StatusConflict {
		t.Fatalf("자기 강등: code=%d (기대 409) body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "자기 자신") {
		t.Fatalf("이유가 문구에 없다: %s", rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, "/api/admin/users/delete",
		`{"username":"boss"}`, sessionCookie(tok))
	if rec.Code != http.StatusConflict {
		t.Fatalf("자기 삭제: code=%d (기대 409) body=%s", rec.Code, rec.Body.String())
	}

	ctx := tenant.With(context.Background(), "default")
	if u, ok, _ := store.GetUser(ctx, "boss"); !ok || u.Role != "admin" {
		t.Fatalf("거부됐는데 boss 가 바뀌었다: %+v ok=%v", u, ok)
	}
	// 남은 바꿀 수 있다 — 가드가 "관리자는 아무것도 못 한다"로 넓어지지 않았다.
	if rec := do(t, h, http.MethodPost, "/api/admin/users/role",
		`{"username":"vice","role":"member"}`, sessionCookie(tok)); rec.Code != http.StatusOK {
		t.Fatalf("남 강등: code=%d body=%s", rec.Code, rec.Body.String())
	}
}

/* ── ④ 역할 변경·삭제·비밀번호 재설정 → 세션 무효화 ─────────────────── */

func TestDemotionAndDeletionKillSessions(t *testing.T) {
	h := adminEnv(t)
	seedUser(t, "boss", "admin", "boss-password-1")
	seedUser(t, "amy", "admin", "amy-password-1")
	seedUser(t, "bob", "member", "bob-password-1")
	seedUser(t, "cid", "member", "cid-password-1")
	bossTok := login(t, h, "boss", "boss-password-1")

	// ── 강등: amy 의 살아 있는 세션이 즉시 죽어야 한다.
	amyTok := login(t, h, "amy", "amy-password-1")
	if rec := do(t, h, http.MethodGet, "/api/auth/me", "", sessionCookie(amyTok)); rec.Code != http.StatusOK {
		t.Fatalf("강등 전 amy 세션: code=%d", rec.Code)
	}
	rec := do(t, h, http.MethodPost, "/api/admin/users/role",
		`{"username":"amy","role":"member"}`, sessionCookie(bossTok))
	if rec.Code != http.StatusOK {
		t.Fatalf("강등: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var res userMutationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if !res.SessionsRevoked {
		t.Fatal("응답이 sessionsRevoked=false 다 — 화면이 무효화가 돌았다고 믿을 근거가 없다")
	}
	if rec := do(t, h, http.MethodGet, "/api/auth/me", "", sessionCookie(amyTok)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("강등된 amy 의 세션이 살아 있다: code=%d body=%s — 만료까지 관리자다",
			rec.Code, rec.Body.String())
	}

	// ── 삭제: bob 의 세션이 죽어야 한다(auth_sessions 에 FK 가 없어 행이 남는다).
	bobTok := login(t, h, "bob", "bob-password-1")
	if rec := do(t, h, http.MethodPost, "/api/admin/users/delete",
		`{"username":"bob"}`, sessionCookie(bossTok)); rec.Code != http.StatusOK {
		t.Fatalf("삭제: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, "/api/auth/me", "", sessionCookie(bobTok)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("삭제된 bob 의 세션이 살아 있다: code=%d — 삭제된 계정이 만료까지 산다", rec.Code)
	}

	// ── 비밀번호 재설정: cid 의 세션이 죽고, 새 비밀번호로만 로그인된다.
	cidTok := login(t, h, "cid", "cid-password-1")
	if rec := do(t, h, http.MethodPost, "/api/admin/users/password",
		`{"username":"cid","password":"cid-password-2"}`, sessionCookie(bossTok)); rec.Code != http.StatusOK {
		t.Fatalf("재설정: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, "/api/auth/me", "", sessionCookie(cidTok)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("재설정 뒤 cid 의 옛 세션이 살아 있다: code=%d", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, "/api/auth/login",
		`{"username":"cid","password":"cid-password-1"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("옛 비밀번호로 로그인됐다: code=%d", rec.Code)
	}
	_ = login(t, h, "cid", "cid-password-2")

	// ── 승격은 세션을 끊지 않는다(권한이 늘어난 세션은 위험이 아니다 — 과잉 로그아웃 방지).
	promoteTok := login(t, h, "amy", "amy-password-1")
	if rec := do(t, h, http.MethodPost, "/api/admin/users/role",
		`{"username":"amy","role":"admin"}`, sessionCookie(bossTok)); rec.Code != http.StatusOK {
		t.Fatalf("승격: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, "/api/auth/me", "", sessionCookie(promoteTok)); rec.Code != http.StatusOK {
		t.Fatalf("승격에서 세션이 끊겼다: code=%d", rec.Code)
	}
	// boss 자신의 세션은 어떤 경우에도 멀쩡하다.
	if rec := do(t, h, http.MethodGet, "/api/auth/me", "", sessionCookie(bossTok)); rec.Code != http.StatusOK {
		t.Fatalf("boss 세션이 끊겼다: code=%d", rec.Code)
	}
}

/* ── ④+ 삭제·재설정과 인제스트 키 (보안검토 M-1) ──────────────────────
 *
 * 세션은 이미 끊었지만 **키는 계속 보고했다**. 계정을 지워도 그 사람의 결속 키가
 * `revoked_at IS NULL` 로 살아남아 POST /api/usage 를 통과했고, 삭제된 이름으로 귀속이 계속
 * 쌓였다 — 퇴사 처리와 침해 대응이 반쪽이었다.
 *
 * 그래서 이 테스트는 상태를 들여다보지 않고 **실제 왕복**으로 잰다: 그 키로 진짜 보고를 보내
 * 200 이던 것이 삭제 뒤 401 이 되는지를 본다. "revoked_at 컬럼이 찼다"는 배선이 끊긴 자리를
 * 못 잡는다(게이트가 그 컬럼을 실제로 보는지는 왕복만이 증명한다).
 */

func TestDeletingUserRevokesTheirIngestKeys(t *testing.T) {
	d := initOrgDB(t)
	h := New(authCfg())
	ctx := tenant.With(context.Background(), "default")

	seedUser(t, "leaver", "member", "leaver-password-1")
	issued, err := org.IssueForTenantUser(ctx, "default", "default", "leaver")
	if err != nil {
		t.Fatalf("IssueForTenantUser: %v", err)
	}
	// 남의 키는 이 삭제에 휩쓸리면 안 된다 — 퇴사자 하나가 팀 전체의 수집기를 멈추는 자리다.
	seedUser(t, "stayer", "member", "stayer-password-1")
	other, err := org.IssueForTenantUser(ctx, "default", "default", "stayer")
	if err != nil {
		t.Fatalf("IssueForTenantUser(stayer): %v", err)
	}
	withKey := func(key string) reqOpt {
		return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+key) }
	}

	// 퇴사 **전** — 그 키로 보고가 통한다(대조군이 없으면 아래 401 이 "원래 안 되던 것"과 같다).
	if rec := do(t, h, http.MethodPost, "/api/usage",
		intakeBody(sessID1, "leaver", "pc-leaver"), withKey(issued.Plain)); rec.Code != http.StatusOK {
		t.Fatalf("퇴사 전 보고: code=%d body=%s", rec.Code, rec.Body.String())
	}

	// 퇴사 처리 — 응답이 **무엇을 거뒀는지** 수로 말한다.
	rec := do(t, h, http.MethodPost, "/api/admin/users/delete", `{"username":"leaver"}`, withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("삭제: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var deleted userDeleteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &deleted); err != nil {
		t.Fatal(err)
	}
	if !deleted.SessionsRevoked || deleted.KeysRevoked != 1 {
		t.Fatalf("삭제 응답: %+v (기대 sessionsRevoked=true keysRevoked=1) — "+
			"sessionsRevoked:true 만 보고 '정리가 끝났다'로 읽히는 것이 M-1 의 위험이었다", deleted)
	}

	// ★ 그 키로는 이제 보고가 **거부된다**(실제 왕복).
	rec = do(t, h, http.MethodPost, "/api/usage",
		intakeBody(sessID2, "leaver", "pc-leaver"), withKey(issued.Plain))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("삭제된 사람의 키로 보고가 통했다: code=%d body=%s — 명부에 없는 이름으로 사용량이 계속 쌓인다",
			rec.Code, rec.Body.String())
	}
	// 그리고 그 보고는 저장되지 않았다(401 뒤에 조용히 들어간 것이 없다).
	for _, u := range col(t, d, "SELECT session_id FROM usage_sessions") {
		if u == sessID2 {
			t.Fatal("401 인데 세션이 저장됐다")
		}
	}
	// 남의 키는 멀쩡하다.
	if rec := do(t, h, http.MethodPost, "/api/usage",
		intakeBody("sess-stayer-1", "stayer", "pc-stayer"), withKey(other.Plain)); rec.Code != http.StatusOK {
		t.Fatalf("남의 키가 함께 죽었다: code=%d body=%s", rec.Code, rec.Body.String())
	}

	// 키가 없는 사람을 지워도 0 이 정직하게 나온다("안 봤다"가 아니라 "거둘 것이 없었다").
	seedUser(t, "keyless", "member", "keyless-password-1")
	rec = do(t, h, http.MethodPost, "/api/admin/users/delete", `{"username":"keyless"}`, withAdmin)
	if err := json.Unmarshal(rec.Body.Bytes(), &deleted); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || deleted.KeysRevoked != 0 {
		t.Fatalf("키 없는 사용자 삭제: code=%d %+v (기대 keysRevoked=0)", rec.Code, deleted)
	}
}

/*
 * 비밀번호 재설정은 키를 **거두지 않는다** — 대신 몇 개가 살아 있는지 말한다.
 *
 * 이 방향을 고른 근거: 재설정의 이유 절반은 단순 분실이고, 거기서 키까지 조용히 죽이면 그 사람의
 * 수집기가 아무 신호 없이 멈춘다. 침해 대응 쪽은 activeKeys 를 보고 사람이 명시적으로 해지한다.
 */
func TestPasswordResetReportsLiveKeysWithoutRevokingThem(t *testing.T) {
	initOrgDB(t)
	h := New(authCfg())
	ctx := tenant.With(context.Background(), "default")

	seedUser(t, "amy", "member", "amy-password-1")
	issued, err := org.IssueForTenantUser(ctx, "default", "default", "amy")
	if err != nil {
		t.Fatalf("IssueForTenantUser: %v", err)
	}
	if _, err := org.IssueForTenantUser(ctx, "default", "default", "amy"); err != nil {
		t.Fatalf("IssueForTenantUser 2: %v", err)
	}

	rec := do(t, h, http.MethodPost, "/api/admin/users/password",
		`{"username":"amy","password":"amy-password-2"}`, withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("재설정: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var res userMutationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.ActiveKeys == nil || *res.ActiveKeys != 2 {
		t.Fatalf("재설정 응답 activeKeys=%v (기대 2) — 화면이 회전을 요구할 근거가 없다", res.ActiveKeys)
	}
	// 키는 살아 있다(조용히 죽이지 않는다 — 그 침묵이 더 비싸다).
	withKey := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+issued.Plain) }
	if rec := do(t, h, http.MethodPost, "/api/usage",
		intakeBody(sessID1, "amy", "pc-amy"), withKey); rec.Code != http.StatusOK {
		t.Fatalf("재설정이 키를 죽였다: code=%d body=%s", rec.Code, rec.Body.String())
	}

	// 키가 없는 사람은 0 이 실린다 — 필드가 빠지면 화면이 "못 셌다"와 구분할 수 없다.
	seedUser(t, "bob", "member", "bob-password-1")
	rec = do(t, h, http.MethodPost, "/api/admin/users/password",
		`{"username":"bob","password":"bob-password-2"}`, withAdmin)
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.ActiveKeys == nil || *res.ActiveKeys != 0 {
		t.Fatalf("키 없는 사용자 재설정 activeKeys=%v (기대 0)", res.ActiveKeys)
	}
	// 다른 변경(생성·역할·팀)에는 이 필드가 실리지 않는다 — 재설정 응답만의 신호다.
	rec = do(t, h, http.MethodPost, "/api/admin/users",
		`{"username":"cid","password":"cid-password-1"}`, withAdmin)
	if strings.Contains(rec.Body.String(), "activeKeys") {
		t.Fatalf("생성 응답에 activeKeys 가 실렸다: %s", rec.Body.String())
	}
}

/* ── ⑤ bcrypt 전용 · 평문 미노출 ────────────────────────────────────── */

func TestPasswordNeverLeaves(t *testing.T) {
	h := adminEnv(t)
	const pw = "amy-password-secret-9"

	rec := do(t, h, http.MethodPost, "/api/admin/users",
		`{"username":"amy","password":"`+pw+`","role":"member"}`, withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("생성: code=%d body=%s", rec.Code, rec.Body.String())
	}
	// 응답 어디에도 평문이 없다.
	if strings.Contains(rec.Body.String(), pw) {
		t.Fatalf("생성 응답에 평문이 있다: %s", rec.Body.String())
	}
	// 목록에도 없다(해시조차 shape 에 자리가 없다).
	rec = do(t, h, http.MethodGet, "/api/admin/users", "", withAdmin)
	body := rec.Body.String()
	if strings.Contains(body, pw) || strings.Contains(body, "password") || strings.Contains(body, "$2") {
		t.Fatalf("목록 응답에 비밀번호 흔적이 있다: %s", body)
	}

	ctx := tenant.With(context.Background(), "default")
	u, ok, err := store.GetUser(ctx, "amy")
	if err != nil || !ok {
		t.Fatalf("GetUser: %v ok=%v", err, ok)
	}
	if u.PasswordHash == pw || !strings.HasPrefix(u.PasswordHash, "$2") {
		t.Fatalf("bcrypt 로 저장되지 않았다: %q", u.PasswordHash)
	}
	if !store.VerifyPassword(u.PasswordHash, pw) {
		t.Fatal("저장된 해시로 검증이 안 된다")
	}

	// 재설정 응답·감사 로그에도 평문이 없다.
	const pw2 = "amy-password-secret-10"
	rec = do(t, h, http.MethodPost, "/api/admin/users/password",
		`{"username":"amy","password":"`+pw2+`"}`, withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("재설정: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), pw2) {
		t.Fatalf("재설정 응답에 평문이 있다: %s", rec.Body.String())
	}
	for _, e := range identity.AuditRecent(ctx, 50) {
		raw, _ := json.Marshal(e)
		if strings.Contains(string(raw), pw) || strings.Contains(string(raw), pw2) {
			t.Fatalf("감사 로그에 평문이 남았다: %s", raw)
		}
	}
	// 오류 문구(짧은 비밀번호)에도 그 값이 실리지 않는다.
	rec = do(t, h, http.MethodPost, "/api/admin/users/password",
		`{"username":"amy","password":"tiny1"}`, withAdmin)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("짧은 재설정: code=%d (기대 400)", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "tiny1") {
		t.Fatalf("오류 문구에 입력값이 실렸다: %s", rec.Body.String())
	}
}

/* ── ⑥ 테넌트 스코프 ────────────────────────────────────────────────── */

/*
 * 요청이 **게이트가 정한 tenant** 로 돈다는 것을 잰다.
 *
 * ⚠ 한계: sqlite 백엔드에는 auth_users.tenant_id 가 없다(단일 테넌트 전제라 스키마에 없다).
 *   크로스테넌트 격리 자체는 pg 의 RLS 가 지고, 그 정책은 migrations/pg/0034_auth.sql 소유다 —
 *   이 스위트로는 못 잰다. 여기서 재는 것은 "핸들러가 cfg.Tenant 를 컨텍스트에 싣고 그 값으로
 *   저장 계층을 부른다"까지다. 인제스트 키 쪽 tenant 스코프(orgs.tenant_id)는 sqlite 에서도
 *   실제 격리가 성립하므로 org_test.go 가 값으로 못박는다.
 */
func TestAdminUsersUseRequestTenant(t *testing.T) {
	h := adminEnv(t)
	if rec := do(t, h, http.MethodPost, "/api/admin/users",
		`{"username":"amy","password":"amy-password-1"}`, withAdmin); rec.Code != http.StatusOK {
		t.Fatalf("생성: code=%d body=%s", rec.Code, rec.Body.String())
	}
	// cfg.Tenant("default") 컨텍스트에서 실제로 보인다.
	ctx := tenant.With(context.Background(), "default")
	if _, ok, _ := store.GetUser(ctx, "amy"); !ok {
		t.Fatal("요청 tenant 로 저장되지 않았다")
	}
	// 다른 tenant 컨텍스트에서 조회하면 (pg 라면 RLS 가) 안 보여야 한다. sqlite 는 컬럼이 없어
	// 보이는 것이 정상이므로 여기서는 단정하지 않고 사실만 남긴다.
	other := tenant.With(context.Background(), "other-tenant")
	_, seen, _ := store.GetUser(other, "amy")
	t.Logf("다른 tenant 컨텍스트에서 보임=%v (sqlite 는 단일테넌트라 true 가 정상 — pg RLS 가 격리를 진다)", seen)
}

/* ── ⑦ 감사 로그 ────────────────────────────────────────────────────── */

func TestUserAdminIsAudited(t *testing.T) {
	h := adminEnv(t)
	seedUser(t, "boss", "admin", "boss-password-1")
	seedUser(t, "vice", "admin", "vice-password-1")
	tok := login(t, h, "boss", "boss-password-1")

	steps := []struct{ path, body string }{
		{"/api/admin/users", `{"username":"amy","password":"amy-password-1","role":"member"}`},
		{"/api/admin/users/role", `{"username":"amy","role":"admin"}`},
		{"/api/admin/users/team", `{"username":"amy","team":"플랫폼"}`},
		{"/api/admin/users/password", `{"username":"amy","password":"amy-password-2"}`},
		{"/api/admin/users/delete", `{"username":"amy"}`},
	}
	for _, s := range steps {
		if rec := do(t, h, http.MethodPost, s.path, s.body, sessionCookie(tok)); rec.Code != http.StatusOK {
			t.Fatalf("%s: code=%d body=%s", s.path, rec.Code, rec.Body.String())
		}
	}
	// 키 해지도 감사 대상이다.
	rec := do(t, h, http.MethodPost, "/api/admin/keys", "", sessionCookie(tok))
	var issued keyIssueResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	if rec := do(t, h, http.MethodPost, "/api/admin/keys/revoke",
		`{"id":"`+issued.ID+`"}`, sessionCookie(tok)); rec.Code != http.StatusNoContent {
		t.Fatalf("키 해지: code=%d", rec.Code)
	}

	ctx := tenant.With(context.Background(), "default")
	seen := map[string]string{} // action → actor
	for _, e := range identity.AuditRecent(ctx, 100) {
		seen[e.Action] = e.Actor
	}
	for _, want := range []string{
		"admin.user.create", "admin.user.role", "admin.user.team",
		"admin.user.password", "admin.user.delete",
		"admin.key.issue", "admin.key.revoke",
	} {
		actor, ok := seen[want]
		if !ok {
			t.Fatalf("감사 기록이 없다: %s (있는 것: %v)", want, seen)
		}
		// **누가 했는지**가 남아야 한다 — "어제 보던 이름이 왜 오늘 다른가"에 답하는 표다.
		if actor != "boss" {
			t.Fatalf("%s 의 actor=%q (기대 boss)", want, actor)
		}
	}
}

/* ── ⑧ 레거시 usage_tok 쿠키로는 상태변경이 안 된다 ─────────────────── */

func TestLegacyCookieCannotMutateUsers(t *testing.T) {
	h := adminEnv(t)
	seedUser(t, "amy", "member", "amy-password-1")

	for _, tc := range []struct{ path, body string }{
		{"/api/admin/users", `{"username":"mallory","password":"mallory-password-1","role":"admin"}`},
		{"/api/admin/users/role", `{"username":"amy","role":"admin"}`},
		{"/api/admin/users/password", `{"username":"amy","password":"amy-password-9"}`},
		{"/api/admin/users/delete", `{"username":"amy"}`},
	} {
		rec := do(t, h, http.MethodPost, tc.path, tc.body, withCookie)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s 레거시 쿠키: code=%d (기대 403) body=%s", tc.path, rec.Code, rec.Body.String())
		}
	}
	// 아무것도 바뀌지 않았다.
	ctx := tenant.With(context.Background(), "default")
	if u, ok, _ := store.GetUser(ctx, "amy"); !ok || u.Role != "member" {
		t.Fatalf("레거시 쿠키가 상태를 바꿨다: %+v ok=%v", u, ok)
	}
	if _, ok, _ := store.GetUser(ctx, "mallory"); ok {
		t.Fatal("레거시 쿠키로 사용자가 생성됐다")
	}
	// 조회는 종전대로 열려 있다(회귀 방지 — 이 쿠키는 조회 자격이다).
	if rec := do(t, h, http.MethodGet, "/api/admin/users", "", withCookie); rec.Code != http.StatusOK {
		t.Fatalf("레거시 쿠키 조회: code=%d (기대 200)", rec.Code)
	}
	// 그리고 usage_sess(HttpOnly+SameSite=Strict)는 상태변경을 태운다 — 둘의 차이가 계약이다.
	seedUser(t, "boss", "admin", "boss-password-1")
	tok := login(t, h, "boss", "boss-password-1")
	if rec := do(t, h, http.MethodPost, "/api/admin/users",
		`{"username":"newbie","password":"newbie-password-1"}`, sessionCookie(tok)); rec.Code != http.StatusOK {
		t.Fatalf("세션 쿠키 생성: code=%d body=%s", rec.Code, rec.Body.String())
	}
}
