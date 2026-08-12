---
type: decision
tags: [이력, 포팅, 아키텍처]
updated: 2026-08-12
sources: ["PORT-STATUS.md", "go/CONTRACT.md", "contract/README.md"]
---

# 결정 — Node → Go + Next.js 포팅 (2026-08-09 완료)

## 무엇을 했나

| 이전 | 이후 |
|---|---|
| Node 서버 `server.js`·`lib/`·`routes/`·`public/` + `npm test` 208개 | Go `go/` + Next.js `web/`, **단일 실행 파일** |

Node 구현은 **제거됐다**(2026-08-09).

## 왜 Node 를 그때까지 살려 뒀나

Node 는 **골든의 출처**였다. 44개 스냅샷이 그 서버의 응답이고, Go 포팅본은 그것과 대조해
합격 판정을 받았다. **지우면 판정 근거가 사라진다.**

컷오버 조건은 하나였다 — **pg 왕복이 실측으로 검증된 뒤.**

## 컷오버 게이트 (통과 근거)

- `contract:verify` 골든 **44/44 × 3회** (새 포트·새 빈 DB) — sqlite local 실측
- **PostgreSQL(remote) 실측 검증** — 앱 롤(NOSUPERUSER·NOBYPASSRLS)로 마이그레이션·RLS
  테넌트 격리·`?→$n`·수제 커넥션 풀 PASS
- **pg 전용 버그 1건 수정** — `SUM/AVG(bigint)` → `numeric` 을 조용히 0 으로 떨구던 스캔
  결함(`db/pg.go`). sqlite 로만 돌던 동안에는 드러나지 않았다
- **수집기 추가** — `collector/` ([[collector]])
- 부동소수 파리티 잔여 2건 해소:
  - 분위수 `idx` 도 값 배리어로 반올림(**FMA 융합 차단**)
  - 결정적 정렬 타이브레이크 2곳(`store.UsageModelAxis`·`identity.Unmapped`)

## 배운 것 — 이 레포의 검증 철학이 여기서 나왔다

> Go 맵 순회 순서로 부동소수를 더했더니 `0.13329999999999997` 이 `0.1333` 으로 나왔고,
> **Go 단위 테스트 270개는 전부 초록불이었다.**

그래서 두 규율이 생겼다:

1. **한 번의 초록불은 증거가 아니다** — 최소 3회, 매 회차 새 포트·새 빈 DB
2. **골든을 고쳐서 통과시키는 것은 게이트를 무력화하는 것이다**

→ [[golden-contract]]

## 포팅 계약이 남긴 것

`go/CONTRACT.md` 는 **역사 문서이면서 유효 계약**이다. 본문의 `lib/*.js`·`server.js` 참조는
삭제된 구 Node 소스를 가리키는 이력이지만, **Go 패키지 경계·시그니처의 계약으로는 여전히
유효하다.**

개정 이력 3건이 그대로 남아 있다:

| 개정 | 언제 | 무엇 |
|---|---|---|
| 1 | go-pure 웨이브 후 | `Mult`(타입/함수 이름 충돌) · `NormKeyOf` · `SummarizeAny` · tz 4함수 추가 |
| 2 | go-core 웨이브 후 | `TotalsResult` · `…UpsertN` · 반드시 배선할 것 2가지 · pg 풀 보류 |
| 3 | go-embed 웨이브 후 | 정적 서빙 손 표 → embed FS 순회 · 오너십 이탈 1건 승인 |

이 이력 자체가 규율이다 — **계약은 코드보다 먼저 바뀐다.** 오너가 발견한 불일치를 PM 이
여기 기록한다.

## 포팅 당시의 오너십 규율

**파일 하나에 오너는 정확히 한 명이다. 남의 파일은 읽기만 한다.** 네 명이 같은 워킹트리에서
동시에 움직이므로, 시그니처가 조용히 바뀌면 다른 오너의 컴파일이 이유 없이 깨진다.

개정 3 이 **오너십 이탈 1건을 승인**한 기록이 남아 있다 — go-embed 가 go-http 소유 파일의
정적 테스트 2개를 삭제했다. 그 테스트들이 **구 바닐라 프런트의 파일 목록을 손으로 열거**해서
`webroot/` 를 Next.js 산출물로 교체하면 **반드시 빨개졌기** 때문이다.

승인 근거가 정직하다: *"보장은 약해지지 않고 강해졌다"* — 손 표가 **생성 표**로 바뀌었고,
같은 축을 `static_test.go` 가 생성된 표에 대해 잰다.

## 남은 리스크 9건

`PORT-STATUS.md` 가 "고치지 않고 **의도적으로 남긴 것**"으로 열거하고 각각 왜 지금 안
고쳤는지 적는다. 현황은 [[risks]] 에 통합해 두었다.

## 하지 않은 것

- **라이브러리 표면 미포팅** — `index.js` 의 임베드용 표면(`noteMcpCall`·`noteRecommendation`·
  `machineActivity`)은 옮기지 않았다. Node 는 다른 서버에 라우트를 얹는 라이브러리로 쓸 수
  있었지만 Go 바이너리에는 그 호스트가 없다. 필요해지면 추가할 자리다.
- **`requireRole` 미재현** — 현행은 항상 `true` 이고 스코프 판정은 게이트 한 곳
  (`httpapi/server.go`)이 진다. 응답은 같다.

## 관련

[[golden-contract]] · [[contract-harness]] · [[webroot-embed]] · [[risks]] · [[usage-server]]
