---
type: hub
tags: [개요, 제품]
updated: 2026-08-12
sources: ["README.md", "docs/PLAN-saas-ingestion.md", "web/components/Dashboard.tsx"]
---

# user-usage — 개요

팀원 PC 에서 올라온 **코딩 에이전트 세션 텔레메트리**를 모아 *누가 · 무엇을 · 얼마나 썼고
얼마어치인가*를 보여주는 조회 도구. 수집 대상은 네 플랫폼 —
[[claude-code]] · [[codex]] · [[gemini-cli]] · [[antigravity]].

## 한 문장 구조

**개발자 PC 의 [[collector]] 가 세션 파일을 읽어 절대값으로 보고 → [[usage-server]] 가
멱등 UPSERT 로 저장 → [[web-dashboard]] 가 조회한다.** 배포 산출물은 프런트를 `go:embed`
로 안은 **단일 실행 파일 하나**다([[webroot-embed]]).

```
[개발자 PC]                              [서버]
 Claude Code ─SessionEnd 훅─┐
 Codex(파일)               ├─▶ usage-collector ──POST /api/usage──▶ usage-server
 Gemini CLI(파일)          │      (증분·멱등)          인제스트 키    │  intake 정규화
 Antigravity ─statusLine──┘                                        │  store UPSERT
              +Stop 훅                                              ▼
                                                          sqlite | PostgreSQL(RLS)
                                                                    │
                                                web(Next.js 정적) ──┘  ID/PW 세션 로그인
```

## 이 도구가 답하는 질문

| 질문 | 화면 |
|---|---|
| 팀이 이번 달 얼마나 썼나 | **사용 추적** |
| 그게 API 종량제였다면 얼마인가 | **사용 관측** → [[cost-model]] |
| 어떤 도구·명령·에이전트를 실제로 쓰나 | **사용 추적** — 7개 축 |
| 어느 플랫폼을 얼마나 쓰나 | **대시보드/플랫폼** → [[platform-coverage]] |
| 어느 PC 가 보고를 멈췄나 | **사용 관측** — 수집 커버리지 |
| 개발자 머신을 어떻게 붙이나 | **연동** → [[installer]] |
| **이 숫자를 믿어도 되나** | 모델별 표의 `근거` 열 → [[honest-uncertainty]] |

마지막 줄이 이 도구의 성격을 정한다. 모델별 값은 정확값과 근사값 **두 근거의 합**이고
([[model-three-paths]]), 화면은 그 비율을 **밝힌다.** 근사를 정확한 값으로 위장하지 않는
것이 여기서는 기능이다.

## 화면 (탭 6개)

`web/components/Dashboard.tsx` 의 `TABS` 가 단일 출처. 해시 라우팅(`#/usage` 등).

| 탭 | 하는 일 | 권한 |
|---|---|---|
| `overview` **대시보드** | 실시간 메트릭 — 드래그로 패널 재배치(Grafana 풍) | 전원 |
| `usage` **사용 추적** | 총계·일별 추이·모델별·축별 | 전원 |
| `usageobs` **사용 관측** | 비용 분해·좌석·팀·분포 | 전원 |
| `onboarding` **연동** | 인제스트 키 발급·원라인 복사·키 관리 (**셀프서비스**) | 전원 |
| `architecture` **아키텍처** | 작동 방식 설명 | 관리자 |
| `admin` **관리** | 사용자·역할·팀·전체 키 현황 | 관리자 |

두 가지 설계 의도가 코드 주석에 박혀 있다:

- **`admin` 은 맨 뒤다.** 하루에 한 번도 안 여는 화면인데 여기에만 되돌릴 수 없는 버튼이
  있다. 자주 쓰는 탭 사이에 끼우면 오조작 거리가 0 이 된다.
- **`onboarding` 은 관리자 전용이 **아니다**.** member 도 자기 키를 발급·해지한다
  (`/api/me/keys`). 관리자 전용으로 되돌리면 member 는 자기 머신을 연동할 방법이 없어진다.

> UI 숨김은 방어가 아니다 — 서버가 `/api/admin/*` 에서 403 을 낸다([[auth-scopes]]).
> 탭 숨김은 클릭 낭비를 줄이는 규율일 뿐이다.

## 스택과 배포

| | |
|---|---|
| 백엔드 | Go — `go/` (패키지 12개, [[go-httpapi]] · [[go-store]] · [[go-intake]] · [[go-cost]] · [[go-db]] …) |
| 프런트 | Next.js App Router + React + TS — `web/`, 정적 export |
| 수집기 | Go 별도 모듈 — `collector/` ([[collector]]) |
| 저장소 | sqlite(local) \| PostgreSQL(remote, RLS) → [[tenancy-rls]] |
| 빌드 | `bash scripts/build.sh` — **유일 빌드 경로** |
| 배포 | 단일 바이너리 · Docker · AWS ECS Fargate ([[deploy-aws]]) |
| 게이트 | 골든 44개 ([[golden-contract]]) + CI ([[ci-gates]]) |

## 지금 상태

- **Node → Go 컷오버 완료**(2026-08-09) — [[node-to-go-port]]
- **Phase 1 멀티테넌트 완료** — 쿼터 하나만 미구현
- **Phase 2 멀티플랫폼 완료** — OTLP 안은 제거됨([[otlp-removal]])
- **Phase 3 미착수** — 셀프서브 가입·미터링/청구·SSO

열린 리스크는 [[risks]] 에 모아 두었다.

## 어디서부터 읽나

- 처음이면 → [[overview]](여기) → [[usage-server]] → [[collector]]
- 숫자를 의심한다면 → [[model-three-paths]] → [[cost-model]] → [[honest-uncertainty]]
- 운영을 맡았다면 → [[runbook]] → [[ingest-keys]] → [[troubleshooting]]
- 코드를 고친다면 → [[golden-contract]] → 해당 [[index]] 의 packages 절
