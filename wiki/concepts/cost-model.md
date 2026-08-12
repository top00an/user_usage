---
type: concept
tags: [비용, 단가, 정규화]
updated: 2026-08-12
sources: ["go/internal/cost/cost.go", "README.md", "config.example.json", "web/lib/costLabels.ts"]
---

# 비용 모델 — "API 환산 비용"

토큰 → USD 환산의 전부. 단일 출처는 `go/internal/cost/`(순수 패키지, 표준 라이브러리 외
아무것도 import 하지 않는다).

## 왜 필요했나 (실측)

패키지 주석이 근거를 적고 있다. 같은 사용량을 두고 도구마다 **176배** 차이 나는 숫자가
나왔고, 원인은 화면이 가장 크게 띄운 값이 **출력 토큰 하나**였다는 것이다. 실제 비용 구성은
정반대다(한 사용자의 한 달, opus 급):

```
캐시읽기 4,468 MTok → $2,234 (64.5%)   ← 작은 글씨로 밀려 있던 축
캐시생성   136 MTok →   $848 (24.5%)
출력        15 MTok →   $381 (11.0%)   ← 화면이 제일 크게 보여주던 축
입력      0.06 MTok →     $0.31
```

**토큰 수가 큰 축과 비용이 큰 축이 다르다.** 축마다 단가 배수가 다르기 때문이고, 그래서
"총 토큰량" 합산은 비용도 작업량도 대변하지 못한다.

## 원칙 다섯

1. **저장하지 않고 읽을 때마다 계산한다.** 컬럼으로 굳히면 단가가 바뀌었을 때 과거 수치가
   옛 단가에 묶인다. 단가는 실제로 바뀐다.
2. 호출 시점에 설정을 읽는다(캐시 금지 — `USAGE_CONFIG` 테스트 격리 보존).
3. **절대 실패를 밖으로 내지 않는다** — 블록이 없거나 깨져도 시드로 수렴한다.
4. 비밀 없음(순수 정책값).
5. **모르는 모델은 `priced=false` + 이름을 `unpriced` 에 남긴다.** 조용히 $0 으로 처리하지
   않는다 → [[honest-uncertainty]].

## ⚠ cost 는 정규화가 끝난 값을 받는다

공급사마다 usage 필드의 **의미가 다르다.** 그 차이를 흡수하는 것은 **수집기(리더)의 책임**이고,
`cost` 는 집계된 행만 보므로 이 중복을 **탐지할 수 없다.** 전제가 깨지면 비용이 조용히 부푼다.

### ① input 과 캐시가 서로소인가?

| 공급사 | input 이 캐시를 |
|---|---|
| Anthropic | **제외**한다 (input · cacheRead · cacheCreate 가 서로소) |
| OpenAI | **포함**한다 (실측 3,336행 대조로 확인) |
| Google | **포함**한다 (공식 문서 정의) |

→ 리더가 저장 전에 `input = max(0, input − cached)` 로 정규화해야 한다. 안 하면 캐시된
입력이 입력가로 한 번·캐시읽기가로 또 한 번 청구되어 **최대 1.8배** 부푼다.

### ② reasoning / thinking 토큰이 output 에 들어 있는가?

| 공급사 | |
|---|---|
| OpenAI | `reasoning_tokens` 는 output 에 **이미 포함** → 더하면 이중 계상 |
| Google | `thoughtsTokenCount` 는 output 에 **미포함** → 더해야 한다 |

`cost` 는 `row.Output` 을 "청구 대상 출력 토큰 전부"로 읽는다.

## 캐시 배수는 모델별이다

전역 상수 하나로 두면 Anthropic 기준이 OpenAI·Google 에 그대로 적용돼 비용이 조용히 틀린다.

- OpenAI 의 캐시 히트 할인율은 계열마다 **0.10 · 0.25 · 0.50 · 1.00** 으로 갈린다
  (예: o3 의 캐시읽기는 0.1배가 아니라 **0.25배**).
- `*-pro` 계열은 **캐시 할인이 아예 없다**(1.00).

타입은 `cost.Mult{ CacheRead, CacheCreate, CacheCreate1h }`.

> `go/CONTRACT.md` 는 이 타입을 `Multipliers` 로 적었지만 같은 이름의 함수
> `Multipliers()` 와 한 패키지에서 공존할 수 없어(Go 는 타입·함수 이름공간이 같다)
> 타입만 `Mult` 로 줄였다 — 계약 개정 1 로 승인됨.

## 계단(롱컨텍스트) 요금

임계값을 넘는 구간에 다른 단가를 적용하는 공급사가 있다(Google·OpenAI). `Result` 는 세 상태를
**구분**한다:

| 상태 | 뜻 |
|---|---|
| 계단 적용됨 | `LongTokens > 0` — 초과 구간 단가로 계산 |
| **우리 표 기준 계단 없음** | 아는 사실. 표준가로 계산했고 그 값이 정확하다 |
| **계단 여부를 모름** | 표준가로 계산했으므로 **과소일 수 있다** |

flat(계단 없는 모델)은 "모름"에 세지 않는다 — 그건 모르는 게 아니라 **아는 사실**이다.
`longShare` 가 롱 몫이 총량을 넘지 않도록 접는다(넘으면 표준 몫이 음수가 되고, 음수 토큰은
비용을 깎는다).

캐시 **생성**에는 분리분이 없다 — 계단을 적용하는 두 공급사가 캐시 쓰기에 계단을 두지 않는다.

## 단가표

- 시드는 **Anthropic · OpenAI · Google** 셋. 검증일이 공급사별로 다르고 화면이 그 날짜를 표시한다.

  | 공급사 | 검증일 | 출처 |
  |---|---|---|
  | Anthropic | `2026-08-04` | platform.claude.com/docs/ko/about-claude/pricing |
  | OpenAI | `2026-08-10` | developers.openai.com/api/docs/pricing |
  | Google | `2026-08-05` | ai.google.dev/gemini-api/docs/pricing |

  `SeedPricedAtFor(provider)` 로 읽는다. `SeedPricedAt`(하위호환)은 Anthropic 값이다.

- **시드는 낡는다.** `config.json` 의 `usage.pricing` 이 **이긴다**(`config.example.json` 참조).
- 모델명은 `NormalizeModel` 이 정규화한다 — 변형 접미사를 벗기지 않으면 그 ID 들이 전부
  `unpriced` 로 빠진다.

### 현재 unpriced

`gemini-3.1-pro` · `gpt-oss-120b`. **추측 매핑으로 그럴듯한 숫자를 만들지 않는다** —
그만큼 비용 합계가 **과소**이고 화면이 그 사실을 표시한다.

## ⚠ 단가 파일 경로

JS 는 모듈 파일 기준으로 `config.json` 을 찾았지만 컴파일된 바이너리엔 그 기준이 없다.
Go 는 `USAGE_CONFIG` → **작업 디렉터리의 `config.json`** 순으로 읽는다.

**cwd 가 레포 루트가 아니면 단가가 조용히 시드로 떨어진다.** 배포와 골든 실행에서
`USAGE_CONFIG` 를 명시하라. 기동 로그의 `· 단가표: <경로>` 줄로 확인한다.

## 라벨

화면 문구의 단일 출처는 `web/lib/costLabels.ts`. 어디서나 **"API 환산 비용"** 이고
청구액이 아니라는 사실을 함께 말한다 → [[honest-uncertainty]].

## 관련

[[go-cost]] · [[honest-uncertainty]] · [[platform-coverage]] · [[troubleshooting]]
