---
type: package
tags: [go, 비용, 순수패키지]
updated: 2026-08-12
sources: ["go/internal/cost/", "go/CONTRACT.md"]
---

# `internal/cost` — 토큰 → USD

순수 패키지(표준 라이브러리만). 개념·정책은 [[cost-model]] 이 소유하고, 이 페이지는 **표면**만
적는다.

```go
func CostOf(s Usage, table Table, mult Mult) Result
func Summarize(buckets []Usage) Result
func Pricing(today time.Time) Table
func PricedAt() string
func SeedPricedAtFor(provider string) string
func Multipliers() Mult
func NormalizeModel(m string) string
func LongContextPrice(model string, p Price) (float64, bool)
```

## 타입

```go
type Price struct { Input, Output float64 }        // USD / 1M 토큰
type Table map[string]Price                        // 정규화된 모델명 → 단가
type Mult struct { CacheRead, CacheCreate, CacheCreate1h float64 }   // 입력가 대비 배수
type Axis struct { Input, Output, CacheRead, CacheCreate … float64 } // 축별 비용
```

> **`Mult` vs `Multipliers`** — `go/CONTRACT.md` 는 타입을 `Multipliers` 로 적었지만 같은
> 이름의 함수와 한 패키지에서 공존할 수 없다(Go 는 타입·함수 이름공간이 같다). 호출부가
> 타이핑하는 것은 함수 이름이라 **함수를 계약대로 두고 타입만 줄였다** — 개정 1 로 승인.

## 파일

| 파일 | 무엇 |
|---|---|
| `cost.go` | 본체 — 환산·시드(Anthropic)·정규화·계단 |
| `seed_openai.go` · `seed_google.go` | 공급사별 시드 단가표 |
| `*_test.go` | 회귀 · 롱컨텍스트 · 멀티프로바이더 · 변형 접미사 |

## 원칙 다섯 (패키지 주석)

1. **저장하지 않고 읽을 때마다 계산한다**
2. 호출 시점에 설정을 읽는다(캐시 금지 — `USAGE_CONFIG` 테스트 격리 보존)
3. **절대 실패를 밖으로 내지 않는다** — 블록이 없거나 깨져도 시드로 수렴
4. 비밀 없음(순수 정책값)
5. **모르는 모델은 `priced=false` + `unpriced` 에 이름을 남긴다**

## ⚠ 이 패키지가 받는 토큰은 **이미 정규화된 값**이다

공급사마다 usage 필드의 의미가 다르고, 그 차이를 흡수하는 것은 **수집기(리더)의 책임**이다.
`cost` 는 집계된 행만 보므로 이 중복을 **탐지할 수 없다.**

전체 표는 [[cost-model]]. 요약: input 이 캐시를 포함하는가(OpenAI·Google ✅, Anthropic ❌),
reasoning 이 output 에 포함되는가(OpenAI ✅, Google ❌).

## 계단(롱컨텍스트)

세 상태를 **구분**한다: 계단 적용됨 / 우리 표 기준 계단 없음(아는 사실) / 계단 여부를 모름
(과소일 수 있음). `longShare` 가 롱 몫이 총량을 넘지 않게 접는다 — 넘으면 표준 몫이 음수가
되고 음수 토큰은 비용을 깎는다.

`regression_test.go` · `longcontext_test.go` 가 못박는다.

## 짝 패키지 — `internal/stats`

```go
func Summarize(xs []float64) Summary          // n, min, p50, p95, p99, max, avg
func SummarizeAny(xs []any, ...) Summary      // ★ dropped 를 센다
func QuantileSorted(sorted []float64, p float64) float64
```

⚠ **`store`·`httpapi` 는 분포 계산에 `SummarizeAny` 를 써야 한다.** `[]float64` 로 먼저
변환하면 `dropped`(관측이 아닌 값의 개수)가 0 으로 나가고, **화면이 표본이 깎인 사실을 말할
수 없게 된다.** JS 의 규율이 `Number(null)===0` 이라 null 을 "0 이라는 관측"으로 둔갑시키지
않는 것인데, `[]float64` 경계를 넘는 순간 그 구분이 사라진다 → [[honest-uncertainty]].

⚠ `QuantileSorted` 는 빈 슬라이스에서 **0** 을 돌려준다(반환형이 `float64` 라 null 을 낼 수
없다). "표본 없음"을 구분해야 하면 `Summarize` 를 쓸 것 — 그쪽은 `*float64`(null)로 낸다.

부동소수 출력이 골든과 **바이트 단위로** 같아야 하므로 분위수 보간식을 그대로 옮겼다.
"같은 뜻의 다른 식"은 마지막 자리에서 갈린다 → [[golden-contract]].

## 짝 패키지 — `internal/tz`

```go
const DefaultOffsetMin = …
func OffsetMin() int
func LocalDay(iso string, offsetMin int) string    // "YYYY-MM-DD"
func LocalHour(iso string, offsetMin int) string   // "YYYY-MM-DDTHH"
func WeekStart · WidenUTCRange · InRange · Label   // 개정 1 에서 추가
```

**고정 오프셋이다. IANA 시간대를 쓰지 않는다** — 매 행마다 시간대 변환을 하는 비용을 서머타임
없는 지역에 지불할 이유가 없다. 서머타임 지역으로 옮길 일이 생기면 그때 바꾼다.

⚠ 존 없는 타임스탬프 해석이 JS(로컬)와 Go(UTC)에서 다르다 — 골든은 안 밟는다(시드 8세션이
전부 `...Z`) → [[risks]].

## 관련

[[cost-model]] · [[honest-uncertainty]] · [[golden-contract]] · [[platform-coverage]]
