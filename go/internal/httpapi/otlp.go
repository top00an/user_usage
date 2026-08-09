package httpapi

import (
	"io"
	"net/http"

	"github.com/tscorp/user-usage/internal/otlp"
)

/*
 * routeOTLP — OTLP/HTTP(JSON) 로그 수신구. 표준 OTel 파이프라인이 그대로 쏠 수 있는 경로다.
 *
 *   POST /v1/logs   (Authorization: Bearer <org 인제스트 키 또는 인테이크 토큰>)
 *
 * 인증·테넌트 해석·rate-limit 는 /api/usage 와 **같은 게이트**를 탄다(server.go). 여기서는
 * OTLP/JSON 을 우리 세션으로 접어 storeSessions 로 넘긴다 — 퍼스트파티와 같은 저장 규율.
 */
func (s *server) routeOTLP(w http.ResponseWriter, r *http.Request, c *rctx) (bool, error) {
	if r.Method != http.MethodPost || c.path != "/v1/logs" {
		return false, nil
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, bodyLimit+1))
	if err != nil || len(raw) > bodyLimit {
		sendError(w, http.StatusRequestEntityTooLarge, "본문이 너무 큽니다")
		return true, nil
	}

	payload, err := otlp.Parse(raw)
	if err != nil {
		// 표준 OTLP 클라이언트는 형식을 지켜야 한다 — 깨진 본문은 400(수집기 best-effort 와 다르다).
		sendError(w, http.StatusBadRequest, "OTLP 로그를 파싱할 수 없습니다")
		return true, nil
	}

	s.storeSessions(r.Context(), payload.Sessions)

	// OTLP 성공 응답은 ExportLogsServiceResponse — 빈 객체가 "전부 수용"이다.
	writeJSON(w, http.StatusOK, map[string]any{}, nil)
	return true, nil
}
