---
type: concept
tags: [게이트, 골든, 규율]
updated: 2026-08-12
sources: ["contract/README.md", "contract/fixtures.mjs", "PORT-STATUS.md", "go/CONTRACT.md"]
---

# 골든 44 — 이 레포의 합격 기준

`contract/golden/` 의 **스냅샷 44개**. 상태코드 · `content-type` · `WWW-Authenticate` ·
JSON 본문 전체를 대조한다. 하네스는 [[contract-harness]].

> **Go 쪽 단위 테스트가 아무리 초록색이어도 이게 빨간불이면 완료가 아니다. 반대도 참이다** —
> 이 게이트가 초록불이면 내부 구조는 오너 마음이다. (`go/CONTRACT.md`)

## 두 가지 금기

1. **골든을 고쳐서 통과시키는 것은 게이트를 무력화하는 것이다.** 골든이 틀렸다고 판단되면
   근거를 들고 사람에게 간다.
2. **한 번의 초록불은 증거가 아니다.** Go 의 맵 순회는 실행마다 순서가 달라서, 최소 3회
   **매 회차 새 포트·새 빈 DB** 로 돌린다.

두 번째 규율이 실제로 버그를 잡았다 → 아래.

## 골든이 실제로 잡은 것

Go 맵 순회 순서로 부동소수를 더했더니 `0.13329999999999997` 이 `0.1333` 으로 나왔다.
**Go 단위 테스트 270개는 전부 초록불이었다.**

이 사건이 이 레포의 검증 철학을 정한다 — 단위 테스트는 "각 조각이 뜻대로 도는가"를 보고,
골든은 "합쳐진 결과가 어제와 같은가"를 본다. 후자는 전자로 대체되지 않는다.

## 무엇을 대조하나

| 묶음 | 담는 것 |
|---|---|
| 총계·시계열 | `summary` 2종, `series` 간격 3종 × 지표 4종 + 그룹핑 3종 |
| 세션 | 정렬축 5개 전부, 필터, top-1 경계, 상세 6종 |
| 관측 | `distribution`·`quality`·`coverage`·`leaderboard`·`dispatch`·`identity` |
| **오류 계약** | 401×2 · 403×2 · 400×5 · 404×2 + 쿠키 조회 통과 |

**오류 계약을 따로 세는 이유:** 화면이 문자열이 아니라 `status` 로 분기한다
([[web-dashboard]]). 코드가 다르면 예외가 나는 게 아니라 화면이 **조용히 틀린 쪽으로** 넘어간다.

## 시드 8세션 — 각각이 함정 하나

`contract/fixtures.mjs`. **지우면 그 함정이 검증에서 빠진다.**

| | 함정 |
|---|---|
| S1 | `series` 완전 — 모델별 정확값(`fromSeries`) |
| S2 | `series` 없음 — 최빈 모델 귀속(`fromSession`). 버리면 총합이 준다 |
| S3 | `series` 안에 **모델 혼합** — 세션 `model` 로 GROUP BY 하면 오귀속되는 바로 그 행 |
| S4 | `noTsTurns > 0` — 버킷이 못 덮은 잔여를 최빈 모델에 귀속(③ 경로) |
| S5 | 단가표에 없는 모델 — `unpriced` 에 떠야 한다(조용한 $0 금지) |
| S6 | `cacheCreate` 있고 `cc1h` 없음 — `ttlUnknownRows` 대상 |
| S7 | 턴 0 · 토큰 0 — 0 나눗셈 방어 |
| S8 | `username` 없음 — `(미상)` 폴백 |

**`(①+②+③) == totals` 불변식이 골든에 박혀 있다** → [[model-three-paths]]. 깨지면 모델별만
작아져 사람에게는 "유실"로 보인다 — 실제로 한 번 일어났던 결함이다.

## 시드는 두 번 전송된다

멱등(`(세션,축,키)` upsert)이 누적(`+=`)으로 퇴화하면 값이 두 배가 되어 **즉시 잡힌다.**
수집기는 재시도하는 best-effort 경로라 중복 전송이 정상 동작에 포함된다 → [[idempotency]].

## 동결 시점

**2026-08-09**, Node 구현 제거 시점. 캡처는 Node 가 하던 것이라 그 시점의 응답이 기준이 되고,
이후로는 **Go 서버가 계속 그것을 재현하는지** 보는 회귀 게이트다 → [[node-to-go-port]].

골든을 정식으로 갱신한 사례는 지금까지 **한 번** — 세션 응답에 `platform` 필드를 싣는
변경(커밋 `cbcf650`). 그 커밋 메시지가 "골든을 정식으로 갱신한 첫 사례"라고 스스로 밝힌다.

## 실행

```bash
bash scripts/build.sh
USAGE_ADMIN_TOKEN=contract-admin-token-0123456789 \
USAGE_INTAKE_TOKEN=contract-intake-token-9876543210 \
USAGE_DATA_DIR=$(mktemp -d) USAGE_PORT=8080 USAGE_KEYWORD_RETENTION_DAYS=off \
  ./go/usage-server &
npm run contract:verify -- --base http://127.0.0.1:8080
```

⚠ `USAGE_KEYWORD_RETENTION_DAYS=off` 를 빠뜨리면 스냅샷 3개가 갈린다.

## 관련

[[contract-harness]] · [[ci-gates]] · [[model-three-paths]] · [[idempotency]] ·
[[node-to-go-port]] · [[honest-uncertainty]]
