// Package otlp 는 OTLP/HTTP(JSON) 로그를 우리 인테이크 세션으로 접는다.
//
// 왜 로그인가: 세션 하나 = 로그 레코드 하나가 자연스럽다(속성에 토큰·모델·귀속을 싣는다).
// 왜 JSON 인가: protobuf 는 OTLP proto 의존을 끌어오는데, 이 프로젝트는 얇은 의존을 지킨다.
// OTLP/HTTP 의 application/json 인코딩은 구조가 단순해 표준 encoding/json 으로 직접 읽는다.
//
// 표준 gen_ai.* 속성 위에 Claude 특유 축을 claude.* 네임스페이스로 얹는다(docs/SPEC-otlp-claude.md).
// 표준에 없는 것(cacheRead·cacheCreate·cc1h·7개 카운터 축)은 claude.* 확장으로만 온다 —
// 그래서 퍼스트파티(/api/usage)가 항상 1급이고, OTLP 는 그 부분집합을 실어 나른다.
package otlp

import (
	"encoding/json"
	"fmt"

	"github.com/tscorp/user-usage/internal/intake"
)

// 속성 키(계약). 표준 → gen_ai.*, 확장 → claude.*.
const (
	attrSessionID = "claude.session.id"
	attrProject   = "claude.project"
	attrUser      = "claude.user"
	attrMachine   = "claude.machine"
	attrModel     = "gen_ai.request.model"
	attrInput     = "gen_ai.usage.input_tokens"
	attrOutput    = "gen_ai.usage.output_tokens"
	attrCacheRead = "claude.usage.cache_read_tokens"
	attrCacheCrt  = "claude.usage.cache_creation_tokens"
	attrWebSearch = "claude.usage.web_search"
	attrWebFetch  = "claude.usage.web_fetch"
	attrTurns     = "claude.session.turns"
	attrStartedAt = "claude.session.started_at"
	attrEndedAt   = "claude.session.ended_at"
)

// OTLP/JSON 로그 페이로드의 최소 구조. 우리가 읽는 필드만 정의한다(나머지는 무시).
type logsPayload struct {
	ResourceLogs []struct {
		Resource struct {
			Attributes []kv `json:"attributes"`
		} `json:"resource"`
		ScopeLogs []struct {
			LogRecords []struct {
				Attributes []kv `json:"attributes"`
			} `json:"logRecords"`
		} `json:"scopeLogs"`
	} `json:"resourceLogs"`
}

// OTLP AnyValue — 우리는 stringValue·intValue(문자열로 옴)·doubleValue 만 읽는다.
type kv struct {
	Key   string `json:"key"`
	Value struct {
		StringValue *string  `json:"stringValue"`
		IntValue    *string  `json:"intValue"` // OTLP/JSON 은 int64 를 **문자열**로 싣는다
		DoubleValue *float64 `json:"doubleValue"`
	} `json:"value"`
}

// Parse 는 OTLP/JSON 로그 본문을 intake.Payload 로 접는다. 리소스 속성은 그 안의 모든 로그
// 레코드에 상속되고, 레코드 속성이 있으면 덮는다(OTel 관례).
func Parse(raw []byte) (intake.Payload, error) {
	var p logsPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return intake.Payload{}, fmt.Errorf("otlp: JSON 파싱 실패: %w", err)
	}
	var out intake.Payload
	for _, rl := range p.ResourceLogs {
		base := index(rl.Resource.Attributes)
		for _, sl := range rl.ScopeLogs {
			for _, rec := range sl.LogRecords {
				attrs := merge(base, rec.Attributes)
				out.Sessions = append(out.Sessions, sessionFrom(attrs))
			}
		}
	}
	return out, nil
}

func sessionFrom(a map[string]kv) intake.Session {
	s := intake.Session{
		SessionID:   str(a, attrSessionID),
		Input:       i64(a, attrInput),
		Output:      i64(a, attrOutput),
		CacheRead:   i64(a, attrCacheRead),
		CacheCreate: i64(a, attrCacheCrt),
		WebSearch:   i64(a, attrWebSearch),
		WebFetch:    i64(a, attrWebFetch),
		Turns:       i64(a, attrTurns),
	}
	s.Project = strp(a, attrProject)
	s.Username = strp(a, attrUser)
	s.Machine = strp(a, attrMachine)
	s.Model = strp(a, attrModel)
	s.StartedAt = strp(a, attrStartedAt)
	s.EndedAt = strp(a, attrEndedAt)
	return s
}

func index(attrs []kv) map[string]kv {
	m := make(map[string]kv, len(attrs))
	for _, a := range attrs {
		m[a.Key] = a
	}
	return m
}

func merge(base map[string]kv, over []kv) map[string]kv {
	m := make(map[string]kv, len(base)+len(over))
	for k, v := range base {
		m[k] = v
	}
	for _, a := range over {
		m[a.Key] = a
	}
	return m
}

func str(a map[string]kv, key string) string {
	if v, ok := a[key]; ok && v.Value.StringValue != nil {
		return *v.Value.StringValue
	}
	return ""
}

func strp(a map[string]kv, key string) *string {
	if v, ok := a[key]; ok && v.Value.StringValue != nil && *v.Value.StringValue != "" {
		s := *v.Value.StringValue
		return &s
	}
	return nil
}

func i64(a map[string]kv, key string) int64 {
	v, ok := a[key]
	if !ok {
		return 0
	}
	if v.Value.IntValue != nil {
		var n int64
		if _, err := fmt.Sscan(*v.Value.IntValue, &n); err == nil {
			return n
		}
	}
	if v.Value.DoubleValue != nil {
		return int64(*v.Value.DoubleValue)
	}
	return 0
}
