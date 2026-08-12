---
type: package
tags: [go, 저장, 집계]
updated: 2026-08-12
sources: ["go/internal/store/", "go/CONTRACT.md"]
---

# `internal/store` — 저장과 집계

**가장 큰 패키지.** 인테이크 쓰기와 모든 조회 집계를 담는다.

## 계약 — JSON 태그를 달지 않는다

반환은 도메인 구조체이며 **JSON 태그를 달지 않는다.** 응답 shape 는 [[go-httpapi]] 가
소유한다. 여기서 태그를 달면 저장 계층 변경이 곧 API 변경이 되어, 스키마를 고칠 때마다
화면이 조용히 깨진다.

## 파일

| 파일 | 무엇 |
|---|---|
| `store.go` · `types.go` · `rows.go` | 기반 · 도메인 타입 · 행 스캔 |
| `write.go` | 인테이크 쓰기(UPSERT) |
| `aggregate.go` | ★ 조회 집계 — `UsageByModel` 세 경로가 여기 |
| `platform.go` | 플랫폼 축·허용목록 |
| `dev.go` | LOC·편집 수락/거부 |
| `teams.go` · `members.go` · `users.go` · `auth.go` | 팀·멤버·계정·세션 |
| `gate.go` | 스코프 판정 보조 |
| `retention.go` | `PruneKeywords` (그리고 호출부 없는 `PruneSeries`) |

## 주요 표면

```go
func Init(ctx, d db.DB) error

// 인테이크(쓰기)
func SessionUpsert(ctx, s SessionInput) error
func SeriesUpsertN(ctx, in SeriesInput)   (int, error)   // 골든이 개수를 대조한다
func CountersUpsertN(ctx, in CountersInput) (int, error)
func CounterBump(ctx, in CounterBumpInput) error

// 집계(읽기)
func Totals(ctx) (TotalsResult, error)
func UsageByDay(ctx, days int) ([]DayRow, error)      // days 는 1..365 로 클램프
func UsageByUser(ctx) ([]UserRow, error)
func UsageByModel(ctx) ([]ModelRow, error)            // ★ ①fromSeries ②③fromSession
func UsageModelAxis(ctx) (ModelAxis, error)           // 사용자별 series 커버리지
func TopKeys(ctx, kind string, limit int) ([]KeyRow, error)
func ByUser(ctx, kind string, limit int) ([]UserKeys, error)
func ReporterCoverage(ctx) ([]Reporter, error)
func SeriesQualityTotals(ctx, f Filter) (QualityTotals, error)
func SessionRows / SessionByID / SeriesRows / SeriesOf / CountersOf
func RecommendationGaps / RecommendationSummary
func PruneKeywords(ctx, days int, now time.Time) (int, error)

var CounterKinds = []string{"tool","bash","slash","skill","agent","mcp","keyword"}
```

각 조회에는 `…WithFilter` 짝이 있다(플랫폼·기간 필터).

> `go/CONTRACT.md` 개정 2 — 타입만 `TotalsResult` 로 바뀌었다. Go 는 한 패키지에서 같은
> 이름의 타입과 함수를 공존시키지 못하고, 호출부가 타이핑하는 것은 함수 이름이라 함수를
> 남겼다([[go-cost]] 의 `Mult` 와 같은 사정).

## `UsageByModel` — 이 레포의 최난도

세 경로를 더하고 **`①+②+③ == Totals` 불변식**을 지켜야 한다. 전용 페이지:
[[model-three-paths]].

`seriesPerSession`(세션당 series 합계)은 **한 곳에만 둔다** — ①의 잔여(③)와 커버리지를
같은 정의로 계산해야 하는데, 두 곳에 쓰면 한쪽만 고쳐지는 날이 온다(코드 주석).

## 부동소수 순서 의존 (열린 리스크)

`/api/usage/series` 의 칸 합계는 **원행이 온 순서**(`SeriesRows` 의 `ORDER BY hour DESC`)로
더한 값이다. 구 Node 와 바이트 단위로 맞추려고 그 순서를 그대로 옮겼다.

**저장 계층이 정렬을 바꾸면 이 합계의 마지막 자리가 바뀐다**(값은 실질적으로 같지만 골든은
갈린다). 근본 해결은 정렬된 순서로 접거나 `math/big` 을 쓰는 것 → [[risks]].

> 이 리스크는 **골든이 실제로 잡아낸 것**이다. Go 맵 순회 순서로 더했더니
> `0.13329999999999997` 이 `0.1333` 으로 나왔고 **Go 단위 테스트 270개는 전부 초록불이었다.**

## 결정적 정렬 타이브레이크

`UsageModelAxis` 와 `identity.Unmapped` 두 곳에 타이브레이크를 넣었다. Go 의 맵 순회가
실행마다 달라 골든이 흔들렸기 때문 → [[node-to-go-port]].

## 보존

- `PruneKeywords` — `keyword` 축만. `USAGE_KEYWORD_RETENTION_DAYS`(기본 90, `off` 면 무기한)
- `PruneSeries` — **포팅했지만 호출부를 만들지 않았다.** 모델별 값의 소급 교정이 이 표가
  온전하다는 데 기댄다 → [[data-policy]]

## 상수 중복 감시

`intake` 는 순수 패키지라 `CounterKinds`·`MaxSeriesPerSession(200)`·
`MaxCountersPerSession(400)` 을 **자기 패키지에 다시 정의한다.** 갈라지면 **인테이크가 저장
계층이 받지 않는 행을 만든다.** 둘 중 하나를 고칠 때 반드시 함께 → [[go-intake]] · [[risks]].

## 관련

[[model-three-paths]] · [[go-httpapi]] · [[go-db]] · [[go-intake]] · [[data-policy]]
