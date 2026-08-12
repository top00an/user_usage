package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/tscorp/user-usage/internal/identity"
	"github.com/tscorp/user-usage/internal/org"
	"github.com/tscorp/user-usage/internal/store"
	"github.com/tscorp/user-usage/internal/tenant"
)

/*
 * routeAdminUsers — 사용자 관리(관리자 전용). `/api/admin/users` 를 소유한다.
 *
 *   GET  /api/admin/users            목록(username·role·createdAt·team)
 *   POST /api/admin/users            생성   {username, password, role}
 *   POST /api/admin/users/role       역할 변경 {username, role}
 *   POST /api/admin/users/password   비밀번호 재설정 {username, password}
 *   POST /api/admin/users/team       팀 배정 {username, team}
 *   POST /api/admin/users/delete     삭제   {username}
 *
 * ⚠ 이 라우트는 routeOnboarding 보다 **앞**에 등록된다 — 그쪽이 `/api/admin` 접두사를 소유하고
 *   안 걸리면 404 를 직접 내기 때문이다(server.go 의 New 주석).
 *
 * ── 이 파일이 지는 네 가지 보안 불변식 ──────────────────────────────────────
 * 전부 "구현했다"가 아니라 "위반이 잡힌다"로만 확인된다(adminusers_test.go):
 *
 *   ① **관리자 전용은 서버가 낸다.** 화면에서 탭을 숨기는 것은 방어가 아니다. 게이트가 이미
 *      member/intake 를 `/api/admin` 에서 막지만, 아래 c.scope 검사가 방어선 한 겹 더다.
 *
 *   ② **마지막 관리자를 강등·삭제할 수 없다.** 하면 아무도 못 들어온다 — 복구는 서버 호스트에
 *      들어가 CLI 로 계정을 다시 만드는 것뿐이다. 거부하고 이유를 말한다.
 *
 *   ③ **자기 자신을 강등·삭제할 수 없다.** 사고의 대부분이 이 모양이고, ②로는 안 막힌다
 *      (관리자가 둘일 때 자기를 지우면 ②는 통과한다).
 *
 *   ④ **역할 변경·삭제·비밀번호 재설정은 세션 무효화와 짝이다.** 안 하면 강등된 사람이 세션
 *      만료까지 관리자로 남는다 — 요청은 200 이고 아무 증상이 없다. 이 침묵이 이 파일에서
 *      가장 위험한 자리다.
 *
 * 비밀번호 평문은 **요청 본문 하나**에서만 존재한다. 응답·감사 로그·오류 문구 어디에도 싣지
 * 않는다(store.CreateUserWithPassword·SetUserPassword 가 bcrypt 로 접고 아무것도 돌려주지 않는다).
 */

// userDetail — 사용자 한 행의 응답 shape. **password_hash 를 담을 자리가 없다.**
// team 은 미배정이면 JSON null 이다(빈 문자열과 "배정 없음"은 다르다).
type userDetail struct {
	Username  string  `json:"username"`
	Role      string  `json:"role"`
	CreatedAt string  `json:"createdAt"`
	Team      *string `json:"team"`
}

type userListResponse struct {
	Users []userDetail `json:"users"`
}

// userMutationResponse — 생성·역할변경·비밀번호재설정·팀배정의 공통 응답.
// sessionsRevoked 는 "이 변경으로 그 사용자의 세션을 끊었는가"다 — 불변식 ④가 실제로 돌았는지
// 화면과 감사자가 응답만 보고 알 수 있어야 한다.
type userMutationResponse struct {
	OK              bool       `json:"ok"`
	User            userDetail `json:"user"`
	SessionsRevoked bool       `json:"sessionsRevoked"`
	/*
	 * ActiveKeys 는 **비밀번호 재설정 응답에만** 실린다(그 외 경로에서는 nil → 필드 자체가 없다).
	 *
	 * 재설정은 키를 자동으로 거두지 않는다 — 이유의 절반이 단순 분실이고, 거기서 키까지 조용히
	 * 죽이면 그 사람의 수집기가 말없이 멈춘다. 대신 "이 사람에게 아직 살아 있는 키가 N개"를
	 * 값으로 말해, 침해 대응으로 재설정한 관리자가 회전 여부를 결정할 근거를 준다(보안검토 M-1).
	 * 0 도 사실이다 — "거둘 것이 없었다"이지 "안 봤다"가 아니다. 그래서 포인터다.
	 */
	ActiveKeys *int `json:"activeKeys,omitempty"`
}

type userDeleteResponse struct {
	OK              bool   `json:"ok"`
	Username        string `json:"username"`
	SessionsRevoked bool   `json:"sessionsRevoked"`
	// KeysRevoked 는 이 삭제로 함께 해지한 인제스트 키 수다. sessionsRevoked 와 **같은 규율**이다 —
	// "정리가 실제로 끝났는가"를 응답만 보고 알 수 있어야 한다(보안검토 M-1).
	KeysRevoked int `json:"keysRevoked"`
}

// 요청 본문(신뢰 경계 — 검증 후에만 안으로 흘린다).
type userCreateRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}
type userRoleRequest struct {
	Username string `json:"username"`
	Role     string `json:"role"`
}
type userPasswordRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
type userTeamRequest struct {
	Username string `json:"username"`
	Team     string `json:"team"`
}
type userDeleteRequest struct {
	Username string `json:"username"`
}

// actorOf 는 감사 로그에 남길 주체다. 로그인 사용자면 그 이름, 관리자 토큰(cfg)처럼 사람
// 신원이 없는 자격이면 종전의 고정 주체(usage-admin)다 — "누가 했는지 모른다"를 빈칸이 아니라
// 이름으로 남긴다.
func actorOf(c *rctx) string {
	if c != nil && c.self != "" {
		return c.self
	}
	return actor
}

func (s *server) routeAdminUsers(w http.ResponseWriter, r *http.Request, c *rctx) (bool, error) {
	if c.path != "/api/admin/users" && !hasPrefix(c.path, "/api/admin/users/") {
		return false, nil
	}
	// ① 방어선: 관리자 스코프만. 게이트가 이미 막지만 라우트 단독으로도 안전해야 한다.
	if c.scope != ScopeAdmin {
		sendError(w, http.StatusForbidden, "관리자 스코프가 필요합니다")
		return true, nil
	}

	ctx := r.Context()

	switch {
	case c.path == "/api/admin/users" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		users, err := store.ListUsers(ctx)
		if err != nil {
			return true, err
		}
		teams, err := store.TeamOf(ctx)
		if err != nil {
			return true, err
		}
		out := make([]userDetail, 0, len(users))
		for _, u := range users {
			out = append(out, detailOf(u, teams))
		}
		writeJSON(w, http.StatusOK, userListResponse{Users: out}, nil)
		return true, nil

	case c.path == "/api/admin/users" && r.Method == http.MethodPost:
		var body userCreateRequest
		_ = decodeJSONBody(r, &body)
		username := strings.TrimSpace(body.Username)
		role := strings.TrimSpace(body.Role)
		if username == "" {
			sendError(w, http.StatusBadRequest, "username 이 필요합니다")
			return true, nil
		}
		if role == "" {
			role = "member" // 기본은 최소 권한이다. 승격은 명시적으로만.
		}
		if err := store.CreateUserWithPassword(ctx, username, role, body.Password); err != nil {
			return true, userErr(w, err)
		}
		identity.AuditLog(ctx, actorOf(c), "admin.user.create", username,
			map[string]any{"role": role})
		writeJSON(w, http.StatusOK, userMutationResponse{
			OK: true, User: mustDetail(ctx, username),
		}, nil)
		return true, nil

	case c.path == "/api/admin/users/role" && r.Method == http.MethodPost:
		var body userRoleRequest
		_ = decodeJSONBody(r, &body)
		username := strings.TrimSpace(body.Username)
		role := strings.TrimSpace(body.Role)
		if username == "" {
			sendError(w, http.StatusBadRequest, "username 이 필요합니다")
			return true, nil
		}
		target, ok, err := store.GetUser(ctx, username)
		if err != nil {
			return true, err
		}
		if !ok {
			sendError(w, http.StatusNotFound, msgUserNotFound)
			return true, nil
		}
		// ③ 자기 자신 — **②보다 먼저** 본다(guardSelf 주석 참조). 요청 신원만 보므로 DB 상태에
		// 의존하지 않고, 그래서 경쟁 조건이 없다.
		if target.Role == "admin" && role != "admin" {
			if code, msg := guardSelf(c, username, msgSelfDemote); code != 0 {
				sendError(w, code, msg)
				return true, nil
			}
		}
		/*
		 * ② 마지막 관리자 — **판정과 변경이 한 트랜잭션이다**(store.SetUserRoleGuarded).
		 * 예전에는 여기서 CountAdmins 로 센 뒤 SetUserRole 을 불렀는데, 그 사이가 열려 있어
		 * 동시 강등 2건이 둘 다 통과했다(보안검토 H-1 — 관리자 0명 잠금). 회귀 방지는
		 * adminusers_test.go 의 TestConcurrentDemotionCannotLockOutAllAdmins 다.
		 *
		 * from 은 **그 트랜잭션이 읽은** 이전 role 이다. 위의 target.Role 은 트랜잭션 밖에서
		 * 읽은 값이라 감사 로그·세션 무효화 판정의 근거로 쓰지 않는다.
		 */
		from, err := store.SetUserRoleGuarded(ctx, username, role)
		if err != nil {
			return true, userErr(w, err)
		}
		/*
		 * ④ 세션 무효화. **강등일 때만** 끊는다 — 승격에서 끊으면 방금 승격된 사람이 이유
		 * 없이 로그아웃되고, 그 자체는 아무것도 지키지 않는다(권한이 늘어난 세션은 위험이
		 * 아니다). 실패하면 그대로 오류다: 여기서 삼키면 "역할은 바뀌었는데 옛 권한이 살아
		 * 있는" 상태가 200 으로 보고된다.
		 */
		revoked := from == "admin" && role != "admin"
		if revoked {
			if err := store.DeleteSessionsForUser(ctx, username); err != nil {
				return true, err
			}
		}
		identity.AuditLog(ctx, actorOf(c), "admin.user.role", username,
			map[string]any{"from": from, "to": role, "sessionsRevoked": revoked})
		writeJSON(w, http.StatusOK, userMutationResponse{
			OK: true, User: mustDetail(ctx, username), SessionsRevoked: revoked,
		}, nil)
		return true, nil

	case c.path == "/api/admin/users/password" && r.Method == http.MethodPost:
		var body userPasswordRequest
		_ = decodeJSONBody(r, &body)
		username := strings.TrimSpace(body.Username)
		if username == "" {
			sendError(w, http.StatusBadRequest, "username 이 필요합니다")
			return true, nil
		}
		if err := store.SetUserPassword(ctx, username, body.Password); err != nil {
			return true, userErr(w, err)
		}
		/*
		 * ④ 재설정은 언제나 세션을 끊는다. 재설정하는 이유의 절반은 "그 계정이 털린 것 같다"
		 * 이고, 그때 살아 있는 세션을 남겨 두면 재설정이 아무것도 막지 못한다.
		 * ⚠ 감사 로그에 비밀번호를 싣지 않는다 — detail 은 DB 에 그대로 남는 문자열이다.
		 */
		if err := store.DeleteSessionsForUser(ctx, username); err != nil {
			return true, err
		}
		/*
		 * ★ 재설정은 세션을 끊지만 **인제스트 키는 거두지 않는다** — 대신 몇 개가 살아 있는지
		 * 말한다(보안검토 M-1 · org.RevokeAllForUser 위의 주석에 그 선택의 근거가 있다).
		 * 세는 데 실패하면 그대로 오류다: 여기서 삼켜 nil 로 두면 "키가 없다"와 "못 셌다"가
		 * 화면에서 같아 보이고, 그 침묵이 곧 M-1 이 위험했던 이유다.
		 */
		activeKeys, err := org.CountActiveKeysForUser(ctx, tenant.From(ctx), username)
		if err != nil {
			return true, err
		}
		identity.AuditLog(ctx, actorOf(c), "admin.user.password", username,
			map[string]any{"sessionsRevoked": true, "activeKeys": activeKeys})
		writeJSON(w, http.StatusOK, userMutationResponse{
			OK: true, User: mustDetail(ctx, username), SessionsRevoked: true, ActiveKeys: &activeKeys,
		}, nil)
		return true, nil

	case c.path == "/api/admin/users/team" && r.Method == http.MethodPost:
		var body userTeamRequest
		_ = decodeJSONBody(r, &body)
		username := strings.TrimSpace(body.Username)
		team := strings.TrimSpace(body.Team)
		if username == "" || team == "" {
			sendError(w, http.StatusBadRequest, "username·team 이 필요합니다")
			return true, nil
		}
		// 팀 배정은 사용량 귀속의 이름표라 계정이 없어도 성립할 수 있지만(과거 보고자), 관리
		// 화면에서 오타로 유령 팀원을 만들지 않도록 계정 존재를 요구한다.
		if _, ok, err := store.GetUser(ctx, username); err != nil {
			return true, err
		} else if !ok {
			sendError(w, http.StatusNotFound, msgUserNotFound)
			return true, nil
		}
		if err := store.AssignTeam(ctx, username, team); err != nil {
			return true, err
		}
		identity.AuditLog(ctx, actorOf(c), "admin.user.team", username, map[string]any{"team": team})
		writeJSON(w, http.StatusOK, userMutationResponse{
			OK: true, User: mustDetail(ctx, username),
		}, nil)
		return true, nil

	case c.path == "/api/admin/users/delete" && r.Method == http.MethodPost:
		var body userDeleteRequest
		_ = decodeJSONBody(r, &body)
		username := strings.TrimSpace(body.Username)
		if username == "" {
			sendError(w, http.StatusBadRequest, "username 이 필요합니다")
			return true, nil
		}
		if _, ok, err := store.GetUser(ctx, username); err != nil {
			return true, err
		} else if !ok {
			sendError(w, http.StatusNotFound, msgUserNotFound)
			return true, nil
		}
		// ③ 자기 자신 — ②보다 먼저. 대상이 관리자든 아니든 자기 자신은 못 지운다.
		if code, msg := guardSelf(c, username, msgSelfDelete); code != 0 {
			sendError(w, code, msg)
			return true, nil
		}
		// ② 마지막 관리자 — 판정과 삭제가 한 트랜잭션이다(H-1). role 은 그 트랜잭션이 읽은 값이다.
		role, err := store.DeleteUserGuarded(ctx, username)
		if err != nil {
			return true, userErr(w, err)
		}
		/*
		 * ★ 퇴사 처리는 **자격을 전부** 거둔다 — 세션과 인제스트 키 둘 다다(보안검토 M-1).
		 *
		 * 세션만 끊던 때는 지워진 사람의 결속 키가 `revoked_at IS NULL` 로 살아남아 POST /api/usage
		 * 를 계속 통과했다. 명부에 없는 이름으로 사용량이 계속 쌓이는데 응답의 sessionsRevoked:true
		 * 가 "정리가 끝났다"로 읽히는 것이 그 건의 진짜 위험이었다. 그래서 응답에 keysRevoked 를
		 * 함께 실어 **무엇을 거뒀는지 수를 말한다**(0 도 사실이다 — 키가 없었다는 뜻).
		 *
		 * 순서: 삭제 → 키 해지 → 세션 무효화. 키 해지를 먼저 하면 ②가 거부한 요청이 살아 있는
		 * 관리자의 키를 이미 해지한 뒤가 된다.
		 */
		keysRevoked, err := org.RevokeAllForUser(ctx, tenant.From(ctx), username)
		if err != nil {
			return true, err
		}
		/*
		 * ④ auth_sessions 는 auth_users 를 FK 로 참조하지 않는다 — 사용자를 지워도 세션 행은
		 * 남고 ResolveSession 은 그 행만 보고 통과시킨다. **삭제된 계정이 만료까지 살아 있다.**
		 */
		if err := store.DeleteSessionsForUser(ctx, username); err != nil {
			return true, err
		}
		identity.AuditLog(ctx, actorOf(c), "admin.user.delete", username,
			map[string]any{"role": role, "sessionsRevoked": true, "keysRevoked": keysRevoked})
		writeJSON(w, http.StatusOK, userDeleteResponse{
			OK: true, Username: username, SessionsRevoked: true, KeysRevoked: keysRevoked,
		}, nil)
		return true, nil
	}

	// `/api/admin/users…` 를 소유하므로 여기까지 오면 404 를 직접 낸다.
	sendError(w, http.StatusNotFound, "not found")
	return true, nil
}

// 고정 문구 — 화면이 이 문자열에 기대므로 한 곳에 둔다.
const (
	msgUserNotFound = "사용자를 찾을 수 없습니다"
	msgLastAdmin    = "마지막 관리자는 강등·삭제할 수 없습니다 — 먼저 다른 관리자를 만드세요"
	msgSelfDemote   = "자기 자신은 강등할 수 없습니다 — 다른 관리자에게 요청하세요"
	msgSelfDelete   = "자기 자신은 삭제할 수 없습니다 — 다른 관리자에게 요청하세요"
)

/*
 * guardSelf 는 ③(자기 자신 강등·삭제 금지)만 본다. code==0 이면 통과다.
 *
 * ③을 ②(마지막 관리자)보다 **먼저** 본다: 관리자가 혼자일 때 자기를 강등하면 둘 다 걸리는데,
 * 그때 사람에게 더 정확한 안내는 "당신이 마지막 관리자다"가 아니라 "자기 자신은 못 바꾼다"이다
 * (다른 관리자를 만들어도 여전히 못 하는 동작이다).
 *
 * ②가 여기 없는 이유가 이 파일에서 가장 중요한 변경이다: 관리자 수를 세고 나서 바꾸면 그 사이가
 * 열려 동시 요청 2건이 둘 다 통과한다(보안검토 H-1 — 관리자 0명 잠금). 그래서 ②는 판정과 변경을
 * 한 트랜잭션에 넣을 수 있는 자리, 곧 저장 계층으로 내려갔다(store.SetUserRoleGuarded·
 * DeleteUserGuarded). ③은 반대로 **DB 상태를 전혀 안 보므로** 여기 남는 것이 맞다.
 */
func guardSelf(c *rctx, username, msg string) (int, string) {
	if c != nil && c.self != "" && c.self == username {
		return http.StatusConflict, msg
	}
	return 0, ""
}

/*
 * userErr 은 store 의 관리 오류를 상태코드로 접는다. **원문을 그대로 쓰는 것이 계약이다** —
 * 이 문구들은 사용자가 고칠 수 있는 안내이고(짧은 비밀번호·중복 이름), 그 안에 비밀번호나
 * DB 내부가 실리지 않는다는 것이 store/users.go 의 규율이다.
 *
 * 접히지 않은 오류(대개 DB 실패)는 err 을 그대로 돌려 호출부가 failRequest 로 접는다(원문 미유출).
 */
func userErr(w http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, store.ErrUserNotFound):
		sendError(w, http.StatusNotFound, msgUserNotFound)
		return nil
	case errors.Is(err, store.ErrUserExists):
		sendError(w, http.StatusConflict, "이미 있는 사용자입니다")
		return nil
	case errors.Is(err, store.ErrWeakPassword):
		sendError(w, http.StatusBadRequest, err.Error())
		return nil
	case errors.Is(err, store.ErrLastAdmin):
		// ② — store 가 **변경과 같은 트랜잭션에서** 낸 판정이다(H-1). 문구는 종전 그대로다.
		sendError(w, http.StatusConflict, msgLastAdmin)
		return nil
	case errors.Is(err, store.ErrAdminCountUnavailable):
		// 셀 수 없으면 막는다 — 통과시키면 조회 실패 한 번이 관리자 0명 잠금으로 이어진다.
		sendError(w, http.StatusServiceUnavailable,
			"관리자 수를 확인할 수 없어 거부했습니다 — 잠시 후 다시 시도하세요")
		return nil
	case err != nil && strings.Contains(err.Error(), "role"):
		// 알 수 없는 role — store 가 admin|member 안내를 문장에 담아 준다.
		sendError(w, http.StatusBadRequest, err.Error())
		return nil
	}
	return err
}

// detailOf 는 목록 행을 응답 shape 으로 접는다.
func detailOf(u store.UserSummary, teams map[string]string) userDetail {
	d := userDetail{Username: u.Username, Role: u.Role, CreatedAt: u.CreatedAt}
	if t := teams[u.Username]; t != "" {
		d.Team = &t
	}
	return d
}

/*
 * mustDetail 은 변경 직후의 사용자 한 행을 읽어 응답에 싣는다. 조회가 실패하면 **아는 만큼만**
 * 담는다(username 만) — 변경은 이미 성공했으므로 응답 조립 실패로 500 을 내면 화면이 "실패한 줄
 * 알고" 재시도하게 되고, 그 재시도가 두 번째 변경이 된다.
 */
func mustDetail(ctx context.Context, username string) userDetail {
	u, ok, err := store.GetUserSummary(ctx, username)
	if err != nil {
		logf("사용자 상세 조회 실패(%s): %v", username, err)
	}
	if err != nil || !ok {
		return userDetail{Username: username}
	}
	teams, terr := store.TeamOf(ctx)
	if terr != nil {
		logf("팀 조회 실패(%s): %v", username, terr)
		teams = nil
	}
	return detailOf(u, teams)
}
