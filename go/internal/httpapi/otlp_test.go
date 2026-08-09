package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

const otlpBody = `{"resourceLogs":[{"resource":{"attributes":[
  {"key":"claude.user","value":{"stringValue":"alice"}},
  {"key":"claude.machine","value":{"stringValue":"host-a"}}
]},"scopeLogs":[{"logRecords":[{"attributes":[
  {"key":"claude.session.id","value":{"stringValue":"otlp-sess-1"}},
  {"key":"gen_ai.request.model","value":{"stringValue":"claude-opus-4"}},
  {"key":"gen_ai.usage.input_tokens","value":{"intValue":"100"}},
  {"key":"gen_ai.usage.output_tokens","value":{"intValue":"50"}},
  {"key":"claude.usage.cache_read_tokens","value":{"intValue":"2000"}},
  {"key":"claude.session.turns","value":{"intValue":"3"}}
]}]}]}]}`

// Phase2 — OTLP /v1/logs 가 우리 store 로 흘러 조회에 반영된다(퍼스트파티와 같은 경로).
func TestOTLPLogsIngest(t *testing.T) {
	_ = openDB(t) // store·identity 전역 핸들(단일테넌트 sqlite)
	h := New(testCfg(false))

	// 인테이크 토큰으로 OTLP 보고 → 200
	if rec := do(t, h, http.MethodPost, "/v1/logs", otlpBody, withIntake); rec.Code != http.StatusOK {
		t.Fatalf("OTLP 인테이크: code=%d body=%s", rec.Code, rec.Body.String())
	}

	// 조회에 세션이 보인다(단일테넌트 sqlite 는 tenant 무시).
	rec := do(t, h, http.MethodGet, "/api/usage/sessions?limit=1000", "", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("sessions 조회: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "otlp-sess-1") {
		t.Fatalf("OTLP 세션이 조회에 안 보인다: %s", rec.Body.String())
	}

	// 인테이크 토큰으로는 조회 불가(403) — 스코프 유지.
	if rec := do(t, h, http.MethodGet, "/api/usage/summary?days=30", "", withIntake); rec.Code != http.StatusForbidden {
		t.Fatalf("intake 로 조회: %d (기대 403)", rec.Code)
	}

	// 깨진 OTLP 본문 → 400
	if rec := do(t, h, http.MethodPost, "/v1/logs", "{bad", withIntake); rec.Code != http.StatusBadRequest {
		t.Fatalf("깨진 OTLP: %d (기대 400)", rec.Code)
	}
}
