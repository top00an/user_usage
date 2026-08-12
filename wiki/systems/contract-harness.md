---
type: system
tags: [게이트, 테스트, 골든]
updated: 2026-08-12
sources: ["contract/README.md", "contract/harness.mjs", "contract/fixtures.mjs", "package.json"]
---

# contract/ — 응답 계약 하네스

서버가 **어제와 같은 것을 내는가**를 기계로 판정하는 하네스. 판정 기준은 [[golden-contract]]
(동결된 스냅샷 44개).

```bash
npm run contract            # go/usage-server 를 격리 기동 → golden/ 재캡처
npm run contract:selfcheck  # 하네스가 스스로 결정적인지 확인
npm run contract:verify -- --base http://127.0.0.1:8080   # 떠 있는 서버를 골든과 대조
```

종료코드: `0` 일치 · `1` 불일치 · `2` 실행 실패. CI 에 그대로 건다([[ci-gates]]).

## 왜 이게 필요한가

가장 비싼 실패는 **"돌아가긴 하는데 값이 미묘하게 다른"** 상태다. 이 레포는 특히 그렇다 —
모델별 값이 두 근거의 합이고([[model-three-paths]]), 비용은 저장하지 않고 읽을 때마다
계산한다([[cost-model]]). **눈으로는 절대 못 잡는다.** 그래서 사람의 검토가 아니라 대조로 막는다.

## capture — 되살린 경로

원래는 Go 포팅본을 Node 서버와 대조하는 게이트였다([[node-to-go-port]]). 컷오버로 `server.js`
가 사라질 때 캡처 경로가 함께 없어졌고, 그동안 **응답 shape 을 아무도 바꿀 수 없었다** —
바꾸면 골든이 갈리고, 골든을 손으로 고치는 것은 금지이기 때문이다. 그래서 Go 바이너리를
띄우는 방식으로 되살렸다.

**캡처는 빌드하지 않는다.** 최종 빌드는 한 사람이 한 번만 한다(Next 빌드가 비결정적이라
동시에 돌리면 `webroot/` 를 서로 덮어쓴다). 그래서 두 경우에 **조용히 넘어가지 않고 실패한다**:

| 상황 | 왜 실패시키나 |
|---|---|
| 바이너리가 없다 | 낡은 것이 남아 있으면 **캡처가 거짓말을 한다** |
| `go/**.go` 가 바이너리보다 새롭다 | 낡은 바이너리로 뜨면 골든이 **사라진 과거**를 동결한다 |

자기 Go 변경을 캡처할 때:

```bash
(cd go && go build -o /tmp/usage-server ./cmd/usage-server) \
  && USAGE_CONTRACT_SERVER_BIN=/tmp/usage-server npm run contract
```

`USAGE_CONTRACT_ALLOW_STALE=1` 은 낡음 판정만 경고로 낮춘다(로그에 남는다).
`verify` 는 서버를 스폰하지 않으므로 이 판정과 무관하다 — CI 는 바이너리 위치를 몰라도 돈다.

캡처는 임시 디렉터리에 **전부 뜬 뒤에** `golden/` 을 갈아끼운다. 중간에 실패해도 동결 골든이
사라지지 않는다 — **게이트가 없어진 채로 남는 것이 가장 위험하다.**

## 환경 격리

셸에 남은 `USAGE_*` 는 물려주지 않는다. 특히 두 개가 값을 바꾼다:

- `USAGE_CONFIG` — 단가표가 바뀐다
- `USAGE_KEYWORD_RETENTION_DAYS` — 기본값 90 으로 띄우면 `retention.keywordDays` 가
  `null` 이 아니라 `90` 이 되어 **스냅샷 3개가 갈린다**

그래서 `verify` 로 물릴 서버도 **골든이 캡처된 조건**으로 띄워야 한다
(특히 `USAGE_KEYWORD_RETENTION_DAYS=off`).

## 정규화 — 시점 의존 값

`now`·`reportedAt`·`lastReportedAt`·`updatedAt`·`at`·`ts` 는 `<string>` 처럼 **타입으로
접는다.** 값을 지우지 않고 접는 이유: **필드가 통째로 사라지는 회귀는 여전히 잡혀야 하기
때문이다.** 객체 키는 정렬해서 비교한다(Go 의 `map` 은 순서가 없다).

목록은 `harness.mjs` 의 `VOLATILE` 이 단일 출처. ⚠ 늘릴 때는 **정말 시점 의존인지** 확인하라.
결정적인 값을 여기 넣으면 **그 필드는 그 순간부터 검증되지 않는다.**

## 전제

세 명령 모두 **빈 DB 로 방금 뜬 서버**를 요구한다. 데이터가 있으면 시드가 섞여 대조가 의미를
잃으므로 시작 전에 `totals.sessions != 0` 이면 **거부한다**. `capture`·`selfcheck` 는 임시
데이터 디렉터리로 직접 띄우니 저절로 만족하고, `verify` 로 물릴 서버는 사람이 빈 디렉터리로
띄워야 한다(한 번 verify 한 서버에 다시 물리면 시드 8건 때문에 거부된다).

토큰은 고정값(`contract-admin-token-…`)이고 응답에 실리지 않아 골든에 새지 않는다.

## 관련

[[golden-contract]] · [[ci-gates]] · [[node-to-go-port]] · [[model-three-paths]]
