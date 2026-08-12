---
type: concept
tags: [집계, 불변식, 모델]
updated: 2026-08-12
sources: ["go/internal/store/aggregate.go", "go/CONTRACT.md", "contract/README.md", "README.md"]
---

# 모델별 집계의 세 경로 — `①+②+③ == Totals`

`UsageByModel` 은 **세 경로를 더한** 값이다. `go/CONTRACT.md` 가 이것을 "이 포팅의 최난도"라
불렀고, 실제로 이 레포에서 가장 미묘한 로직이다.

| 경로 | 근거 | 무엇 |
|---|---|---|
| ① | `fromSeries` | `usage_series`(시간×모델 버킷)가 있는 세션 — **정확값** |
| ② | `fromSession` | series 가 아예 없는 세션 — 그 세션의 **최빈 모델에 통째로 귀속**(근사) |
| ③ | `fromSession` | series 가 **못 덮은 잔여**(`noTsTurns` 등) — 최빈 모델에 귀속 |

## 불변식

```
① + ② + ③  ==  Totals
```

**깨지면 모델별 합만 작아지고 총계는 그대로**라, 사람에게는 "데이터 유실"로 보인다.
실제로 한 번 일어났던 결함이고, 그래서 [[golden-contract]] 에 이 불변식이 박혀 있다.

## 왜 ②를 버리면 안 되나

②는 근사값이다. "정확하지 않으니 빼자"는 유혹이 있는데, 빼면 **모델별 표의 합계가 총계보다
작아진다.** 화면에는 "왜 모델별을 다 더해도 총계가 안 나오지?"만 남고, 그 답이 어디에도 없다.

구 Node `lib/store.js` 의 주석(포팅 계약이 "반드시 읽고 시작하라"고 지목한 자리)이 왜 ②를
버리면 안 되는지를 실측치와 함께 적고 있었다. 현재는 `go/internal/store/aggregate.go` 가
그 자리다.

## 왜 세션 `model` 로 GROUP BY 하면 안 되나

**한 세션 안에서 모델이 바뀔 수 있다.** 세션 행의 `model` 은 "그 세션의 최빈 모델"일 뿐이고,
실제 토큰은 여러 모델에 나뉘어 있다. 세션 `model` 로 묶으면 그 전부가 최빈 모델 하나로
오귀속된다.

골든 시드의 **S3** 가 정확히 이 함정을 밟는다 — `series` 안에 모델이 혼합된 세션.

## 화면이 이 사실을 말한다

근사를 정확한 값으로 위장하지 않는 것이 [[honest-uncertainty]] 의 핵심이고, 여기가 그
규율의 원산지다. `web/` 에서 두 곳이 이것을 노출한다:

1. 모델별 표의 **`근거` 열** — `fromSeries` / `fromSession`
2. 모델별 카드 하단 **사용자별 series 커버리지** (`modelAxis` = `UsageModelAxis`)

## 왜 `usage_series` 를 프루닝하지 않나

`README.md` 의 보존 표가 명시한다: **시리즈 프루닝을 일부러 하지 않는다.** 모델별 값의
소급 교정이 이 표가 온전하다는 데 기댄다. 단일 출처는 `go/internal/store` 의 주석.

`store.PruneSeries` 는 **포팅은 됐지만 호출부를 만들지 않았다**(`go/CONTRACT.md`).

## `series` 가 없을 수 있는 이유

- **구버전 수집기** — `series`(시간×모델 버킷)는 신버전만 보낸다. 없어도 **거절하지 않는다** —
  거절하면 그 사람의 사용량이 통째로 사라진다. 대신 그 세션의 모델별 값은 근사가 되고
  화면이 그 사실을 밝힌다.
- **`noTsTurns`** — 시각이 없어 시간 버킷에 못 올린 턴. ③ 경로가 이것을 처리한다.

## 관련 함정 — 귀속 교정과 series 의 드리프트

귀속 교정(restamp)은 `usage_sessions`·`usage_counters` 만 재스탬프하고 **`usage_series` 는
건드리지 않는다.** 그래서 세션은 새 이름인데 그 세션의 버킷은 옛 OS 계정명을 지닌 행이 남는다.

[[cleanup]] 의 `usage-rows` 가 이 드리프트를 알고 있어서, 자식 표를 **이름이 아니라 세션
소유 + 고아 잔여** 두 조건으로 고른다. 이름으로만 좁히면 그 버킷이 살아남아 **"지웠는데
화면에 옛 이름이 있다"** 가 된다.

## 관련

[[golden-contract]] · [[honest-uncertainty]] · [[go-store]] · [[attribution]] · [[cleanup]]
