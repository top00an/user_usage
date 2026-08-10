package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/tscorp/user-usage/internal/org"
	"github.com/tscorp/user-usage/internal/tenant"
)

/*
 * routeOnboarding — 관리자 대시보드의 인제스트 키 관리. `/api/admin` 접두사의 주인이라 안
 * 걸리면 404 를 직접 낸다(usage.go 가 /api/usage 를 소유하는 것과 같은 규율).
 *
 *   POST /api/admin/keys         현재 tenant 의 org 를 보장하고 키 발급(평문 1회 노출)
 *   GET  /api/admin/keys         키 목록(마스크만, 평문 절대 미포함)
 *   POST /api/admin/keys/revoke  {"id":...} 로 해지 → 204
 *
 * 스코프: 관리자만. 게이트(server.go)가 이미 intake/member 를 이 접두사에서 거부하고, 쿠키/
 * 세션 자격의 상태변경(POST)도 거부한다(Bearer 관리자 토큰만 쓴다 — CSRF 표면 제거). 아래
 * c.scope 검사는 그 위의 방어선 한 겹이다.
 */

// keyIssueResponse — POST /api/admin/keys 응답(동결 계약). key 는 이 응답에서 1회만.
type keyIssueResponse struct {
	Key       string `json:"key"`
	ID        string `json:"id"`
	CreatedAt string `json:"createdAt"`
}

// keyListItem — GET /api/admin/keys 항목(동결 계약). masked 만 노출, 평문 없음.
// revokedAt 은 미해지면 JSON null 이라야 하므로 *string 이다.
type keyListItem struct {
	ID        string  `json:"id"`
	Masked    string  `json:"masked"`
	CreatedAt string  `json:"createdAt"`
	RevokedAt *string `json:"revokedAt"`
}

type keyListResponse struct {
	Keys []keyListItem `json:"keys"`
}

// keyRevokeRequest — POST /api/admin/keys/revoke 본문(신뢰 경계 — 검증 후 안으로).
type keyRevokeRequest struct {
	ID string `json:"id"`
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
		issued, err := org.IssueForTenant(ctx, tnt, tnt)
		if err != nil {
			return true, onboardErr(w, err)
		}
		writeJSON(w, http.StatusOK, keyIssueResponse{
			Key: issued.Plain, ID: issued.ID, CreatedAt: issued.CreatedAt,
		}, nil)
		return true, nil

	case c.path == "/api/admin/keys" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		keys, err := org.ListKeys(ctx, tnt)
		if err != nil {
			return true, onboardErr(w, err)
		}
		items := make([]keyListItem, 0, len(keys))
		for _, k := range keys {
			var revoked *string
			if k.RevokedAt != "" {
				rv := k.RevokedAt
				revoked = &rv
			}
			items = append(items, keyListItem{
				ID: k.ID, Masked: k.Masked, CreatedAt: k.CreatedAt, RevokedAt: revoked,
			})
		}
		writeJSON(w, http.StatusOK, keyListResponse{Keys: items}, nil)
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
