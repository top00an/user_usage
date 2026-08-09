package otlp

import (
	"testing"

	"github.com/tscorp/user-usage/internal/intake"
)

func TestParseLogs(t *testing.T) {
	body := []byte(`{"resourceLogs":[{"resource":{"attributes":[
	  {"key":"claude.user","value":{"stringValue":"alice"}},
	  {"key":"claude.machine","value":{"stringValue":"host-a"}}
	]},"scopeLogs":[{"logRecords":[{"attributes":[
	  {"key":"claude.session.id","value":{"stringValue":"sess-1"}},
	  {"key":"gen_ai.request.model","value":{"stringValue":"claude-opus-4"}},
	  {"key":"gen_ai.usage.input_tokens","value":{"intValue":"100"}},
	  {"key":"gen_ai.usage.output_tokens","value":{"intValue":"50"}},
	  {"key":"claude.usage.cache_read_tokens","value":{"intValue":"2000"}},
	  {"key":"claude.session.turns","value":{"intValue":"3"}}
	]}]}]}]}`)
	p, err := Parse(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(p.Sessions) != 1 {
		t.Fatalf("세션 수 = %d (기대 1)", len(p.Sessions))
	}
	s := p.Sessions[0]
	if s.SessionID != "sess-1" || s.Input != 100 || s.Output != 50 || s.CacheRead != 2000 || s.Turns != 3 {
		t.Fatalf("매핑 불일치: %+v", s)
	}
	if s.Username == nil || *s.Username != "alice" {
		t.Fatal("resource 속성 상속 실패(user)")
	}
	if s.Machine == nil || *s.Machine != "host-a" {
		t.Fatal("machine 상속 실패")
	}
	if s.Model == nil || *s.Model != "claude-opus-4" {
		t.Fatal("model 매핑 실패")
	}
}

// 레코드 속성이 리소스 속성을 덮는다(OTel 관례).
func TestRecordOverridesResource(t *testing.T) {
	body := []byte(`{"resourceLogs":[{"resource":{"attributes":[
	  {"key":"claude.user","value":{"stringValue":"resource-user"}}
	]},"scopeLogs":[{"logRecords":[{"attributes":[
	  {"key":"claude.session.id","value":{"stringValue":"s"}},
	  {"key":"claude.user","value":{"stringValue":"record-user"}}
	]}]}]}]}`)
	p, _ := Parse(body)
	if p.Sessions[0].Username == nil || *p.Sessions[0].Username != "record-user" {
		t.Fatal("레코드 속성이 리소스를 덮어야 한다")
	}
}

func TestParseEmpty(t *testing.T) {
	p, err := Parse([]byte(`{}`))
	if err != nil || len(p.Sessions) != 0 {
		t.Fatalf("빈 입력: err=%v n=%d", err, len(p.Sessions))
	}
}

func TestParseBadJSON(t *testing.T) {
	if _, err := Parse([]byte(`{bad`)); err == nil {
		t.Fatal("깨진 JSON 은 err 를 내야 한다")
	}
}

// 카운터·series 는 JSON 문자열 속성으로 실린다.
func TestParseCountersAndSeries(t *testing.T) {
	body := []byte(`{"resourceLogs":[{"scopeLogs":[{"logRecords":[{"attributes":[
	  {"key":"claude.session.id","value":{"stringValue":"s"}},
	  {"key":"claude.counters.json","value":{"stringValue":"[{\"kind\":\"tool\",\"key\":\"Read\",\"count\":5}]"}},
	  {"key":"claude.series.json","value":{"stringValue":"[{\"hour\":\"2026-08-09T10\",\"model\":\"claude-opus-4\",\"input\":100}]"}}
	]}]}]}]}`)
	p, err := Parse(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	s := p.Sessions[0]
	if len(s.Counters) != 1 || s.Counters[0].Kind != "tool" || s.Counters[0].Key != "Read" || s.Counters[0].Count != 5 {
		t.Fatalf("counters 매핑 불일치: %+v", s.Counters)
	}
	if len(s.Series) != 1 || s.Series[0].Model != "claude-opus-4" || s.Series[0].Input != 100 {
		t.Fatalf("series 매핑 불일치: %+v", s.Series)
	}
}

// Export → Parse 왕복이 세션 값을 보존한다(우리 → OTLP → 우리).
func TestExportParseRoundtrip(t *testing.T) {
	in := intake.Session{
		SessionID: "rt-1", Input: 100, Output: 50, CacheRead: 2000, CacheCreate: 300,
		WebSearch: 2, WebFetch: 1, Turns: 7,
		Counters: []intake.Counter{{Kind: "tool", Key: "Read", Count: 5}},
		Series:   []intake.Bucket{{Hour: "2026-08-09T10", Model: "claude-opus-4", Input: 100, Output: 50}},
	}
	body, err := Export([]intake.Session{in})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	p, err := Parse(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(p.Sessions) != 1 {
		t.Fatalf("왕복 세션 수 = %d", len(p.Sessions))
	}
	got := p.Sessions[0]
	if got.SessionID != in.SessionID || got.Input != in.Input || got.CacheRead != in.CacheRead || got.Turns != in.Turns {
		t.Fatalf("왕복 스칼라 불일치: %+v", got)
	}
	if len(got.Counters) != 1 || got.Counters[0].Count != 5 {
		t.Fatalf("왕복 counters 불일치: %+v", got.Counters)
	}
	if len(got.Series) != 1 || got.Series[0].Model != "claude-opus-4" {
		t.Fatalf("왕복 series 불일치: %+v", got.Series)
	}
}
