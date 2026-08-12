---
type: package
tags: [go, http, 라우터]
updated: 2026-08-12
sources: ["go/internal/httpapi/", "go/CONTRACT.md"]
---

# `internal/httpapi` — HTTP 표면

라우터 · 인증 게이트 · 정적 서빙 · 인테이크/관측/온보딩 라우트. **응답 shape 을 소유한다.**

오너 표기는 `go-http`([[node-to-go-port]] 당시). 의존 최상단이라 `store`·`cost`·`stats`·
`tz`·`intake`·`identity` 를 모두 부른다.

## 파일

| 파일 | 무엇 |
|---|---|
| `server.go` | 라우터·스코프 게이트·테넌트 해석 지점 |
| `auth.go` · `authsession.go` | 자격 판정 · ID/PW 세션 |
| `analytics.go` | `/api/usage/*` 조회 |
| `usage.go` | `POST /api/usage` 인테이크 |
| `onboarding.go` | `/api/admin/keys` · `/api/me/keys` |
| `adminusers.go` | `/api/admin/users*` |
| `agent.go` | `/install.sh` · `/api/agent/collector` (+ `install.sh` 임베드 사본) |
| `seats.go` | 좌석 집계 |
| `static.go` | `go:embed all:webroot` 정적 서빙 |
| `ratelimit.go` | 테넌트별 토큰버킷 |
| `dto.go` · `json.go` · `util.go` | 응답 타입 · 직렬화 |

## 응답 shape 은 여기가 소유한다

[[go-store]] 의 반환 구조체에는 **JSON 태그를 달지 않는다.** 거기서 태그를 달면 저장 계층
변경이 곧 API 변경이 되어, 스키마를 고칠 때마다 화면이 조용히 깨진다.

그래서 골든이 대조하는 것은 이 패키지의 출력이다 → [[golden-contract]].

## 라우트 순서는 계약이다

`analytics` 가 `admin` 보다 **앞**이어야 한다. admin 이 `/api/usage` 접두사를 통째로 소유하고
안 걸리면 404 를 직접 내므로, 뒤로 가면 **관측 화면이 통째로 404** 가 된다.

## readOnly(remote)

인테이크를 **등록하지 않고** admin 라우트도 GET/HEAD 만 통과시킨다. 나머지는 **405 가 아니라
404** — 이 모드에서 그 엔드포인트는 "지금은 막혔다"가 아니라 **존재하지 않는다**
→ [[tenancy-rls]].

## 인증

```go
type Auth struct { Via string; Scope string }   // Via: "header"|"cookie"  Scope: "admin"|"intake"
func Authenticate(r *http.Request, cfg Config) *Auth
```

규칙 다섯은 [[auth-scopes]] 에 모아 두었다. 전부 골든의 오류 스냅샷이 잡는다.

## 반드시 배선할 것 둘 (`go/CONTRACT.md` 개정 2)

**둘 다 안 걸면 조용히 틀린다** — 요청은 200 이고 화면도 정상으로 보인다.

1. `db.SetTenantResolver(tenant.From)` — 안 걸면 pg 의 모든 쿼리가 `default` 로 흐른다
2. RLS 판정은 `Verdict.Rejects()` 로만 분기 — `!v.OK` 로 하면 터널 미개통에서 부팅이 막힌다

또한 인테이크 응답이 저장 개수를 실으므로 **`SeriesUpsertN`·`CountersUpsertN`(`(int,error)`)
쪽을 불러야 한다** — 골든이 그 숫자를 대조한다.

## 정적 서빙 — 경로 화이트리스트

디렉터리를 열고 `..` 를 막는 대신 **나갈 수 있는 URL 을 통째로 열거한다.** 그러면 경로
탈출이라는 문제 **자체가 성립하지 않는다** — 정규화·심링크·인코딩을 고민할 자리가 없다.

`static.go` 는 `init` 에서 `fs.WalkDir` 로 화이트리스트를 만든다(Next 산출물의 파일명이
콘텐츠 해시라 손 표는 빌드마다 깨진다). **정규화하지 않으므로** `/%2e%2e/x` 와 `/../x` 가
같은 것으로 접히는 자리가 없다 — 판정은 서버가 실제로 받은 `EscapedPath` 위의 맵 조회 한 번.

⚠ **`//go:embed all:webroot` 의 `all:` 은 필수다.** go:embed 는 `_`·`.` 로 시작하는 이름을
기본 건너뛰는데 Next 산출물의 본체가 전부 `_next/` 아래다. 빼면 셸만 나가고 스크립트가 통째로
빠지며 **그 증상은 404 가 아니라 빈 화면이다.** `TestEmbedIncludesNextUnderscoreDirs` 가 지킨다.

`index.html` 이 임베드에 없으면 **init 에서 죽는다** — 셸 없는 바이너리가 조용히 떠서 404 만
내는 것보다 부팅 실패가 낫다 → [[webroot-embed]].

보안 헤더: CSP · `X-Frame-Options` · `nosniff` · `Referrer-Policy`.

## 오류 응답

예상 못 한 예외의 **원문을 클라이언트로 보내지 않는다.** 대개 DB 드라이버 에러(테이블·컬럼명,
제약 이름, 접속 정보 조각). 원문은 stderr 로. 라우트가 의도해서 내는 400 은 이 경로로 오지
않으므로 안내는 그대로 남는다.

## 테스트가 지키는 것

`agent_test.go` 가 특히 무겁다 — **임시 HOME 에 실제로 설치하고 실제로 제거한다**
→ [[installer]]. `static_test.go` 가 임베드 드리프트를 내용 수준에서 잡는다 → [[ci-gates]].

## 관련

[[usage-server]] · [[auth-scopes]] · [[golden-contract]] · [[webroot-embed]] · [[go-store]]
