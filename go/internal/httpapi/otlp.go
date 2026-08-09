package httpapi

import (
	"io"
	"net/http"

	"github.com/tscorp/user-usage/internal/intake"
	"github.com/tscorp/user-usage/internal/otlp"
	"github.com/tscorp/user-usage/internal/store"
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

/*
 * routeOTLPExport — 우리 → OTLP. 조회한 세션을 OTLP/JSON 로그로 내보낸다. 고객이 자기 관측
 * 백엔드로도 사용량을 받게 하는 경로다(판매 포인트). admin(열람) 스코프만 — 데이터를 읽어 내보낸다.
 *
 *   GET /api/usage/export/otlp?days=30[&user=&model=&limit=]
 */
func (s *server) routeOTLPExport(w http.ResponseWriter, r *http.Request, c *rctx) (bool, error) {
	if r.Method != http.MethodGet || c.path != "/api/usage/export/otlp" {
		return false, nil
	}
	rows, err := store.SessionRows(r.Context(), store.Filter{
		Username: c.query.Get("user"),
		Model:    c.query.Get("model"),
		Limit:    store.SessionRowsMax,
	})
	if err != nil {
		return true, err
	}
	sessions := make([]intake.Session, 0, len(rows))
	for _, row := range rows {
		sessions = append(sessions, intake.Session{
			SessionID: row.SessionID,
			Username:  nilIfEmpty(row.Username), Machine: nilIfEmpty(row.Machine),
			Project: nilIfEmpty(row.Project), Model: nilIfEmpty(row.Model),
			StartedAt: row.StartedAt, EndedAt: row.EndedAt,
			Input: row.Input, Output: row.Output,
			CacheRead: row.CacheRead, CacheCreate: row.CacheCreate,
			WebSearch: row.WebSearch, WebFetch: row.WebFetch, Turns: row.Turns,
		})
	}
	body, err := otlp.Export(sessions)
	if err != nil {
		return true, err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	return true, nil
}

// nilIfEmpty 는 빈 문자열을 nil 로 접는다("" 와 NULL 을 가르는 세션 필드 규율).
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
