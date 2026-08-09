# OTLP 수집 스펙 — `claude.*` 속성

표준 OTel 파이프라인으로 Claude Code 사용량을 이 서비스에 보내는 규약. Phase 2.

## 엔드포인트

```
POST /v1/logs
Content-Type: application/json
Authorization: Bearer <org 인제스트 키(멀티테넌트) 또는 USAGE_INTAKE_TOKEN(단일테넌트)>
```

OTLP/HTTP 의 **JSON 인코딩**(`application/json`)을 받는다. 인증·테넌트 해석·rate-limit 는
퍼스트파티 `/api/usage` 와 같은 게이트를 탄다. 성공은 `200` + `{}`(ExportLogsServiceResponse,
빈 객체 = 전부 수용). 형식 오류는 `400`.

## 모델링

**세션 하나 = 로그 레코드 하나.** 속성으로 토큰·모델·귀속을 싣는다. 리소스 속성은 그 안의 모든
로그 레코드에 상속되고, 레코드 속성이 있으면 덮는다(OTel 관례) — 머신·사용자처럼 세션마다 같은
값은 리소스에 한 번만 둔다.

## 속성 매핑

| 속성 키 | 타입 | → 우리 필드 | 표준? |
|---|---|---|---|
| `claude.session.id` | string | SessionID (필수) | 확장 |
| `gen_ai.request.model` | string | Model | **표준 gen_ai** |
| `gen_ai.usage.input_tokens` | int | Input | **표준 gen_ai** |
| `gen_ai.usage.output_tokens` | int | Output | **표준 gen_ai** |
| `claude.usage.cache_read_tokens` | int | CacheRead | 확장(표준에 없음) |
| `claude.usage.cache_creation_tokens` | int | CacheCreate | 확장 |
| `claude.usage.web_search` | int | WebSearch | 확장 |
| `claude.usage.web_fetch` | int | WebFetch | 확장 |
| `claude.session.turns` | int | Turns | 확장 |
| `claude.session.started_at` | string(ISO) | StartedAt | 확장 |
| `claude.session.ended_at` | string(ISO) | EndedAt | 확장 |
| `claude.project` | string | Project | 확장 |
| `claude.user` | string | Username(서버가 귀속 교정으로 덮을 수 있음) | 확장 |
| `claude.machine` | string | Machine | 확장 |

> OTLP/JSON 은 int64 를 **문자열**로 싣는다(`{"intValue":"100"}`) — 수신구가 그것을 흡수한다.

## 왜 확장이 필요한가 (설계 노트)

표준 gen_ai semantic conventions 는 input/output 토큰·모델까지만 담는다. **캐시 4축(cacheRead·
cacheCreate·cc1h)과 7개 카운터 축(tool·bash·slash·skill·agent·mcp·keyword)은 표준에 자리가 없어**
`claude.*` 확장으로만 온다. 그래서 이 도구의 핵심 화면(비용 축 분해·근거)을 완전히 채우려면
퍼스트파티(`/api/usage`)나 이 확장을 이해하는 클라이언트가 필요하다 — **퍼스트파티가 항상 1급이고,
OTLP 는 표준 파이프라인 호환을 위한 부분집합**이다.

## 현재 범위(Phase 2 S1)와 다음

- **지금**: 세션 집계(토큰·모델·귀속·턴). 로그 레코드 → 세션 저장.
- **다음(S2)**: 카운터 축(tool·bash·… )과 시간 버킷(series)을 OTLP 속성/메트릭으로 확장, 그리고
  (선택) 우리 → OTLP export 로 고객이 자기 백엔드로도 받게.

## 예시 (curl)

```sh
curl -sX POST "$USAGE_SERVER_URL/v1/logs" \
  -H "Authorization: Bearer $USAGE_INTAKE_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"resourceLogs":[{"resource":{"attributes":[
        {"key":"claude.user","value":{"stringValue":"alice"}}]},
      "scopeLogs":[{"logRecords":[{"attributes":[
        {"key":"claude.session.id","value":{"stringValue":"s-1"}},
        {"key":"gen_ai.usage.input_tokens","value":{"intValue":"100"}},
        {"key":"gen_ai.usage.output_tokens","value":{"intValue":"50"}},
        {"key":"claude.session.turns","value":{"intValue":"3"}}]}]}]}]}'
```
