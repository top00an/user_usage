---
type: hub
tags: [리스크, 미검증, 모순]
updated: 2026-08-12
sources: ["PORT-STATUS.md", "docs/VERIFICATION.md", "docs/PLAN-saas-ingestion.md", "docs/OPERATIONS.md", ".github/workflows/ci.yml"]
---

# 열린 리스크 · 미검증 · 모순

원천이 **스스로 밝힌** 것만 모은다. 위키가 추론으로 추가한 항목은 「추정」으로 표시한다.
이 레포의 규율([[honest-uncertainty]])이 이 페이지가 존재하는 이유다.

---

## A. 미검증 — 실행 증거가 없는 것

| # | 무엇 | 원천 | 상태 |
|---|---|---|---|
| A1 | **`cleanup usage-rows` 의 remote(pg) 경로** — sqlite 로만 왕복했다. SQL 은 방언 중립이고 격리는 RLS 가 담당하지만 pg 실측은 안 했다 | `docs/OPERATIONS.md` §8-9 | 열림 → [[cleanup]] |
| A2 | **Docker 이미지 빌드 자체** — docker 미설치 환경이라 네이티브 스모크만 통과 | `PORT-STATUS.md` | 열림 |
| A3 | **런타임 Docker 이미지가 `migrations/` 를 담지 않는다** — remote 모드에서 러너가 파일을 못 찾는다. 조회만 하면 무해할 수 있으나 **확인 요** | `PORT-STATUS.md` | 열림 → [[deploy-aws]] |
| A4 | **CI 의 `pg-isolation` 잡 role/grant 셋업** — 로컬은 docker 실측 완료, CI 첫 실행에서 1회 확인 필요 | `docs/VERIFICATION.md` · `ci.yml` 주석 | 열림 → [[ci-gates]] |
| A5 | **Gemini 의 `tokens.tool`(`toolUsePromptTokenCount`)** — 더하지 않고 있다. 입력에 포함된 내역으로 보이지만 **공식 문서에 명시가 없다.** 코드 주석이 "불확실. 실데이터로 확인 필요"라 적음 | `collector/internal/gemini/gemini.go` | 열림 → [[gemini-cli]] |

> A1·A2·A3 은 `PORT-STATUS.md` 의 리스크 1(pg 왕복 미검증)이 **해소된 뒤 남은 잔여**다.
> 리스크 1 자체는 2026-08-09 pg 실측으로 닫혔다 → [[node-to-go-port]].

---

## B. 의도적으로 남긴 것 — 왜 지금 안 고쳤는지가 함께 적혀 있다

`PORT-STATUS.md` 「남은 리스크」의 현행 상태.

| # | 무엇 | 왜 지금 안 고치나 |
|---|---|---|
| B1 | **계열 합계가 부동소수 덧셈 순서에 의존한다** — `/api/usage/series` 의 칸 합계가 `ORDER BY hour DESC` 순서로 더한 값. 저장 계층이 정렬을 바꾸면 마지막 자리가 바뀐다 | 근본 해결(정렬 후 접기 / `math/big`)은 구 Node 와 어긋나 게이트를 빨갛게 만들었다. **Node 를 지운 지금이 정리할 자리** → [[go-store]] |
| B2 | **`intake` 와 `store` 가 상수를 각자 정의한다** (`CounterKinds`·200·400) | `intake` 는 내부 import 가 금지된 순수 패키지. **갈라지면 인테이크가 저장 계층이 받지 않는 행을 만든다.** 현재 값은 일치 → [[go-intake]] |
| B3 | **pg 커넥션 풀을 손으로 만들었다** | `go.sum` 오너십 때문에 `puddle/v2` 를 못 넣었다. **전환 조건:** 커넥션 고갈·누수가 관측되면 `pgxpool` 로. `internal/db/pg.go` 한 파일만 고치면 된다 → [[go-db]] |
| B4 | **`webroot/` 는 커밋되는 빌드 산출물이다** | `go:embed` 가 트리 안의 파일을 요구한다. 방어 4겹 → [[webroot-embed]] |
| B5 | **`Cache-Control: no-cache` 유지** | `_next/static/*` 는 `immutable` 로 굳혀도 안전하지만, 지금 바꾸면 "옛 화면"이 뜰 때 캐시 탓인지 동기화 누락인지 구분이 어려워진다 |
| B6 | **라이브러리 표면 미포팅** (`noteMcpCall`·`noteRecommendation`·`machineActivity`) | Go 바이너리에는 그 호스트가 없다. 필요해지면 추가 |

---

## C. 알려진 JS↔Go 동작 차이 — 골든에는 안 닿음 (실측 확인)

> 숨기지 않고 적는다. **골든이 안 밟는다는 것이 "없다"는 뜻은 아니다.** (`go/CONTRACT.md`)

| 차이 | 발현 조건 | 골든 |
|---|---|---|
| counters 동점 키의 생존 순서 | 한 축에 동점 **80개 초과** 또는 한 세션 총 **400개 초과** | 안 닿음 (실측 최대 축 3 · 세션 14) |
| 존 없는 타임스탬프 해석 (Go=UTC, JS=로컬) | `started_at` 에 존이 없을 때 | 안 닿음 (시드 8세션 전부 `...Z`) |
| 문자열 길이 단위 (JS=UTF-16, Go=룬) | 상한 40 근처의 **이모지(astral)** 키워드 | 한글·ASCII 는 동일 |
| `unpriced` 정렬 (JS=UTF-16, Go=UTF-8 바이트) | 비-ASCII 모델명 | 현행 모델명 전부 ASCII |

---

## D. 미구현 — Phase 3 이전

| 무엇 | 상태 | 영향 |
|---|---|---|
| **per-tenant 누적 쿼터** | 미구현 | rate-limit 은 순간 폭주만 막는다. 한 org 의 **누적** 상한이 없어 스토리지·비용이 무제한으로 늘 수 있다. **Phase 1 에서 유일하게 닫히지 않은 항목** → [[tenancy-rls]] |
| 미터링 · 청구 | 미구현 | 좌석 조회(`/api/usage/seats`)까지는 서 있다 |
| 셀프서브 가입 | 미구현 | 지금은 운영자가 `org create` + `key issue` 로 만들어 전달 |
| SSO(Google/Okta) · 데이터 거주 | 미구현 | 엔터프라이즈 |
| **테넌트 접근 감사 로그** | 부분 | 귀속 교정 감사(`usage_audit`)는 있으나 테넌트 접근 감사는 없다 |
| 고객 이탈 시 데이터 파기 | 미구현 | [[cleanup]] 이 수동 수단은 제공한다 |

---

## E. 스키마·구조의 구멍

| # | 무엇 | 영향 |
|---|---|---|
| E1 | **`usage_audit` 은 pg 스키마가 없다** — `migrations/pg/` 어느 파일도 만들지 않고 sqlite DDL(`identity/audit.go`)만 갖고 있다 | [[cleanup]] 의 `usage-rows` 가 감사 기록을 남기지 않는 이유. *반쪽만 있는 감사 기록은 없는 것보다 나쁘다* → [[go-identity]] |
| E2 | **`install.sh` 가 두 벌** (`scripts/` 원본 + `httpapi/` 임베드 사본) | 사본만 고치면 다음 빌드에서 사라진다. `TestEmbeddedInstallScriptMatchesSource` 가 지킨다 → [[installer]] |
| E3 | **마이그레이션에 down 이 없다** | 되돌리기는 **스냅샷/PITR 이 유일** → [[runbook]] |

---

## F. 문서 간 모순 — 위키가 발견한 것

| # | 어긋난 곳 | 내용 |
|---|---|---|
| F1 | `ci.yml` 스텝 이름 vs 실제 | 스텝 이름은 "백엔드 **11** 패키지", 실제는 **12**. 게이트 동작에는 영향 없음 (`docs/VERIFICATION.md` 가 스스로 기록) |
| F2 | `PORT-STATUS.md` 리스크 2 vs `ci.yml` | 전자는 `verify:embed` 를 "CI 에 걸 것"이라 하고, 후자는 **일부러 걸지 않는다**(turbopack 해시 비결정성). **결정이 바뀌었는데 PORT-STATUS 가 갱신되지 않았다** → [[webroot-embed]] |
| F3 | `.env.example` vs `README.md` 환경변수 표 | `.env.example` 에 `USAGE_PG_POOL_MAX`·`USAGE_RETENTION_INTERVAL_H` 가 있으나 README 표에는 없다. 「추정」 — 누락이지 폐기는 아닌 것으로 보인다 |

---

## G. 알려진 수집 한계 — 고칠 수 없는 것

| 무엇 | 성질 |
|---|---|
| **Codex 압축 롤아웃(.zst)** 을 건너뛴다 | 7일 지난 세션. **그만큼 사용량이 빠진다.** 침묵하지 않고 stderr 로 찍는다 → [[codex]] |
| **Antigravity 입력은 하한이다** (실측 -0.6%) | `current_usage` 가 "마지막 API 호출"만 보여줘 한 invocation 안의 보조 호출이 빠진다 → [[antigravity]] |
| **Antigravity 행동 축은 미수집** | 그 도구가 기록하지 않아 **올 수 없다** → [[platform-coverage]] |
| **Codex·Gemini 의 `slash` 축 없음** | 세션 파일에 기록이 없다 |
| **`unpriced` 모델의 비용 과소** | `gemini-3.1-pro`·`gpt-oss-120b`. 추측 매핑을 넣지 않는 것이 의도 → [[cost-model]] |

---

## H. 정책적 열린 항목

| 무엇 | |
|---|---|
| **훅 계약 안정성** | 훅 이벤트/페이로드 스키마에 의존한다. 증분 백필이 안전망. Antigravity 는 **statusLine 출력 형식**에도 의존해 표면이 하나 더 넓다 |
| **구독 요금제와 비용 라벨** | 화면 값은 "API 환산 비용"이지 청구액이 아니다. 라벨로 방어하고 있으나 **경리에 그대로 넘기는 오용은 문서·라벨 밖에서 막을 수단이 없다** |
| **계정명·머신명·프로젝트명은 지워지지 않는다** | 호스트명 규칙에 사번·실명이 들어가는 조직은 그 사실을 팀에 알리고 써야 한다 → [[data-policy]] |
| **법적 삭제 요구** | [[cleanup]] 만으로는 부족하다 — `usage_audit` 에 이름이 남는다 |

## 관련

[[honest-uncertainty]] · [[node-to-go-port]] · [[ci-gates]] · [[cleanup]] · [[tenancy-rls]]
