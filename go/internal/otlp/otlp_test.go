package otlp

import "testing"

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
