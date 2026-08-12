package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/tscorp/user-usage/internal/identity"
	"github.com/tscorp/user-usage/internal/org"
	"github.com/tscorp/user-usage/internal/store"
	"github.com/tscorp/user-usage/internal/tenant"
)

/*
 * 인제스트 키 관리 — 두 표면이 이 파일에 있다.
 *
 * ── routeOnboarding: 관리자용. `/api/admin` 접두사의 주인이라 안 걸리면 404 를 직접 낸다
 *    (usage.go 가 /api/usage 를 소유하는 것과 같은 규율).
 *
 *   POST /api/admin/keys         키 발급(평문 1회 노출). {"username":"amy"} 로 사람에 묶을 수 있다
 *   GET  /api/admin/keys         **전체** 키 현황(마스크 + 소유자, 평문 절대 미포함)
 *   POST /api/admin/keys/revoke  {"id":...} 로 해지 → 204
 *
 * ── routeSelfKeys: 셀프서비스. `/api/me` 접두사의 주인이고 member 도 쓴다(동결 ②).
 *
 *   POST /api/me/keys            **자기** 키 발급(발급자 이름에 묶인다)
 *   GET  /api/me/keys            **자기** 키 목록 — 남의 키는 아예 조회되지 않는다
 *   POST /api/me/keys/revoke     자기 키 해지 → 204. 남의 키·없는 키는 **똑같이** 404
 *
 * 스코프: 관리자 경로는 관리자만. 게이트(server.go)가 이미 intake/member 를 `/api/admin`
 * 접두사에서 거부한다. 상태변경 자격은 Bearer 관리자 토큰과 **로그인 세션**(usage_sess)이고,
 * 레거시 `usage_tok` 쿠키는 게이트가 거부한다 — SameSite 보장이 없어 CSRF 표면이기 때문이다
 * (usage_sess 는 HttpOnly+SameSite=Strict 라 허용된다. 근거는 server.go 의 게이트 주석).
 * 아래 c.scope 검사는 그 위의 방어선 한 겹이다.
 *
 * 평문은 **발급 응답에서 1회만**. 목록은 언제나 마스크다.
 */

// keyIssueResponse — 키 발급 응답(동결 계약). key 는 이 응답에서 1회만.
// username 은 추가 필드다 — 기존 필드의 이름·타입은 그대로다(응답 shape 은 추가만).
type keyIssueResponse struct {
	Key       string `json:"key"`
	ID        string `json:"id"`
	CreatedAt string `json:"createdAt"`
	Username  string `json:"username"` // 묶인 사람. 빈 문자열이면 org 공용 키
}

// keyListItem — 키 목록 항목(동결 계약). masked 만 노출, 평문 없음.
// revokedAt 은 미해지면 JSON null 이라야 하므로 *string 이다. username 은 추가 필드로,
// 묶이지 않은(레거시·org 공용) 키에서는 null 이다 — 빈 문자열과 "묶이지 않음"은 다르다.
type keyListItem struct {
	ID        string  `json:"id"`
	Masked    string  `json:"masked"`
	CreatedAt string  `json:"createdAt"`
	RevokedAt *string `json:"revokedAt"`
	Username  *string `json:"username"`
}

type keyListResponse struct {
	Keys []keyListItem `json:"keys"`
}

// keyRevokeRequest — 해지 요청 본문(신뢰 경계 — 검증 후 안으로).
type keyRevokeRequest struct {
	ID string `json:"id"`
}

// keyIssueRequest — 발급 요청 본문. username 은 선택이고, **관리자 경로에서만** 읽는다
// (셀프서비스는 발급자 본인으로 고정한다 — 본문으로 남의 이름을 넣을 자리를 만들지 않는다).
type keyIssueRequest struct {
	Username string `json:"username"`
}

// toKeyListItems 는 org.KeyInfo 를 응답 항목으로 접는다(두 표면이 같은 모양을 쓰게).
func toKeyListItems(keys []org.KeyInfo) []keyListItem {
	items := make([]keyListItem, 0, len(keys))
	for _, k := range keys {
		var revoked, owner *string
		if k.RevokedAt != "" {
			rv := k.RevokedAt
			revoked = &rv
		}
		if k.Username != "" {
			u := k.Username
			owner = &u
		}
		items = append(items, keyListItem{
			ID: k.ID, Masked: k.Masked, CreatedAt: k.CreatedAt,
			RevokedAt: revoked, Username: owner,
		})
	}
	return items
}

func (s *server) routeOnboarding(w http.ResponseWriter, r *http.Request, c *rctx) (bool, error) {
	if !hasPrefix(c.path, "/api/admin/") {
		return false, nil
	}
	// 방어선: 관리자 스코프만. 게이트가 이미 non-admin 을 이 접두사에서 막지만, 여기서 한 번 더
	// 확인해 라우트 단독으로도 안전하게 둔다.
	if c.scope != ScopeAdmin {
		sendError(w, http.StatusForbidden, "관리자 스코프가 필요합니다")
		return true, nil
	}

	ctx := r.Context()
	// 게이트가 요청 1지점에서 컨텍스트에 실은 tenant(cfg.Tenant 또는 세션 tenant)를 그대로 쓴다.
	tnt := tenant.From(ctx)

	switch {
	case c.path == "/api/admin/keys" && r.Method == http.MethodPost:
		/*
		 * username 은 **선택**이다. 안 보내면 종전과 같은 org 공용 키다 — 기존 화면·설치
		 * 스크립트가 그대로 돈다(응답 shape 은 추가만, 요청도 추가만).
		 *
		 * 있으면 그 사람에게 묶인 키를 대리발급한다. 존재하는 계정인지 확인하는 이유:
		 * 오타 하나로 아무에게도 닿지 않는 이름에 귀속되는 키가 생기면, 그 보고는 화면에
		 * 유령 사용자로 쌓이고 아무도 그것이 오타였다는 것을 모른다.
		 */
		var body keyIssueRequest
		_ = decodeJSONBody(r, &body)
		owner := strings.TrimSpace(body.Username)
		if owner != "" {
			if _, ok, err := store.GetUser(ctx, owner); err != nil {
				return true, err
			} else if !ok {
				sendError(w, http.StatusNotFound, "사용자를 찾을 수 없습니다")
				return true, nil
			}
		}
		issued, err := org.IssueForTenantUser(ctx, tnt, tnt, owner)
		if err != nil {
			return true, onboardErr(w, err)
		}
		identity.AuditLog(ctx, actorOf(c), "admin.key.issue", issued.ID,
			map[string]any{"username": owner})
		writeJSON(w, http.StatusOK, keyIssueResponse{
			Key: issued.Plain, ID: issued.ID, CreatedAt: issued.CreatedAt, Username: issued.Username,
		}, nil)
		return true, nil

	case c.path == "/api/admin/keys" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		// 관리자는 tenant 의 **전체** 키 현황을 본다(누구 키인지 포함). 평문은 없다.
		keys, err := org.ListKeys(ctx, tnt)
		if err != nil {
			return true, onboardErr(w, err)
		}
		writeJSON(w, http.StatusOK, keyListResponse{Keys: toKeyListItems(keys)}, nil)
		return true, nil

	case c.path == "/api/admin/keys/revoke" && r.Method == http.MethodPost:
		var body keyRevokeRequest
		// 본문이 깨졌으면 빈 값으로 간다 — 아래 id 검사가 400 안내를 낸다.
		_ = decodeJSONBody(r, &body)
		if strings.TrimSpace(body.ID) == "" {
			sendError(w, http.StatusBadRequest, "id 가 필요합니다")
			return true, nil
		}
		if err := org.RevokeByID(ctx, tnt, body.ID); err != nil {
			return true, onboardErr(w, err)
		}
		identity.AuditLog(ctx, actorOf(c), "admin.key.revoke", body.ID, map[string]any{})
		w.WriteHeader(http.StatusNoContent)
		return true, nil
	}

	// 접두사를 소유하므로 여기까지 오면 404 를 직접 낸다.
	sendError(w, http.StatusNotFound, "not found")
	return true, nil
}

/*
 * routeSelfKeys — 셀프서비스 인제스트 키(동결 ②). `/api/me` 접두사의 주인이다.
 *
 * 여기 닿는 자격은 게이트가 이미 좁혔다: member 는 로그인 세션일 때만 상태변경이 온다.
 * 이 라우트가 지는 것은 **소유 경계** 하나다 — 무엇이 "자기 것"인지는 소유자를 아는 쪽이 정한다.
 *
 * ⚠ c.self 가 비면(=관리자 토큰·인테이크처럼 사람 신원이 없는 자격) 403 이다. 빈 이름으로
 *   내려가면 org.ListKeysForUser 가 빈 목록을, RevokeByIDForUser 가 owned=false 를 돌려주므로
 *   사고는 아니지만, 그 상태를 200 으로 덮으면 "내 키가 하나도 없네"로 보여 원인이 흐려진다.
 */
func (s *server) routeSelfKeys(w http.ResponseWriter, r *http.Request, c *rctx) (bool, error) {
	if !hasPrefix(c.path, "/api/me/") {
		return false, nil
	}
	if c.scope != ScopeAdmin && c.scope != ScopeMember {
		sendError(w, http.StatusForbidden, "로그인이 필요합니다")
		return true, nil
	}
	if c.self == "" {
		sendError(w, http.StatusForbidden,
			"이 경로는 로그인한 사용자만 쓸 수 있습니다 — 관리자 토큰에는 사용자 신원이 없습니다")
		return true, nil
	}

	ctx := r.Context()
	tnt := tenant.From(ctx)

	switch {
	case c.path == "/api/me/keys" && r.Method == http.MethodPost:
		// 소유자는 **언제나 요청자 본인**이다. 본문의 username 은 읽지 않는다 — 읽는 순간
		// "남의 이름으로 발급"이 한 줄 실수로 열린다.
		issued, err := org.IssueForTenantUser(ctx, tnt, tnt, c.self)
		if err != nil {
			return true, onboardErr(w, err)
		}
		identity.AuditLog(ctx, c.self, "me.key.issue", issued.ID, map[string]any{"username": c.self})
		writeJSON(w, http.StatusOK, keyIssueResponse{
			Key: issued.Plain, ID: issued.ID, CreatedAt: issued.CreatedAt, Username: issued.Username,
		}, nil)
		return true, nil

	case c.path == "/api/me/keys" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		keys, err := org.ListKeysForUser(ctx, tnt, c.self)
		if err != nil {
			return true, onboardErr(w, err)
		}
		writeJSON(w, http.StatusOK, keyListResponse{Keys: toKeyListItems(keys)}, nil)
		return true, nil

	case c.path == "/api/me/keys/revoke" && r.Method == http.MethodPost:
		var body keyRevokeRequest
		_ = decodeJSONBody(r, &body)
		if strings.TrimSpace(body.ID) == "" {
			sendError(w, http.StatusBadRequest, "id 가 필요합니다")
			return true, nil
		}
		owned, err := org.RevokeByIDForUser(ctx, tnt, c.self, body.ID)
		if err != nil {
			return true, onboardErr(w, err)
		}
		if !owned {
			/*
			 * ⚠ 남의 키와 없는 키는 **같은 응답**이다. 갈라 주면 그 차이가 곧 "그 키는
			 * 존재한다"는 신호가 되어, 아무나 키 id 를 넣어 보며 남의 키 유무를 캘 수 있다.
			 */
			sendError(w, http.StatusNotFound, "키를 찾을 수 없습니다")
			return true, nil
		}
		identity.AuditLog(ctx, c.self, "me.key.revoke", body.ID, map[string]any{})
		w.WriteHeader(http.StatusNoContent)
		return true, nil
	}

	// 접두사를 소유하므로 여기까지 오면 404 를 직접 낸다.
	sendError(w, http.StatusNotFound, "not found")
	return true, nil
}

/*
 * onboardErr — org 헬퍼 오류를 응답으로 접는다.
 *   · ErrNotInit(온보딩 미초기화) → 503(운영자가 고칠 상태 문제이지 요청 잘못이 아니다).
 *     응답을 여기서 냈으므로 nil 을 돌려준다(호출부는 handled 로 끝낸다).
 *   · 그 밖(대개 DB 오류) → err 을 그대로 돌려 호출부가 failRequest 로 접는다(원문 미유출).
 */
func onboardErr(w http.ResponseWriter, err error) error {
	if errors.Is(err, org.ErrNotInit) {
		sendError(w, http.StatusServiceUnavailable,
			"온보딩 서브시스템이 초기화되지 않았습니다 — org 초기화(멀티테넌트/부팅 배선)를 확인하세요")
		return nil
	}
	return err
}
