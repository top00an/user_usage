---
type: hub
tags: [인덱스]
updated: 2026-08-12
---

# 인덱스 — 전체 카탈로그

질의는 **여기서 시작한다.** 각 줄은 링크 + 한 줄 요약이다. 후보를 고른 뒤 그 페이지를 읽는다.
규약은 [[CLAUDE|스키마]], 원천 목록은 [[sources]].

## 입구

| 페이지 | 무엇 |
|---|---|
| [[overview]] | 제품 한 장 요약 — 구조·화면 6탭·스택·현재 상태. **처음이면 여기** |
| [[risks]] | 열린 리스크 · 미검증 · 문서 간 모순. **원천이 스스로 밝힌 것만** |
| [[sources]] | 원천 문서 목록과 각각이 무엇의 단일 출처인가 |
| [[log]] | 이 위키의 시간순 기록 |

## systems — 돌아가는 것

| 페이지 | 무엇 |
|---|---|
| [[usage-server]] | 단일 실행 파일 — HTTP 서버 + 운영 CLI. 세 모드·패키지 구조·저장 테이블 |
| [[collector]] | 팀원 PC 의 수집기 — 원천 4종·증분 체크포인트·정책 이중 방어 |
| [[web-dashboard]] | Next.js 프런트 — 정적 export·`lib/api.ts` 단일 호출구·근사를 말하는 6곳 |
| [[installer]] | 원커맨드 `install.sh` — 설치와 제거·비파괴 멱등·statusLine 체이닝 |
| [[contract-harness]] | `contract/` — capture·verify·정규화·전제 |
| [[ci-gates]] | CI 잡 둘(test · pg-isolation)·옵트인 게이트·표시 오류 |
| [[deploy-aws]] | Docker · AWS ECS Fargate + ALB + RDS · 비밀 취급 · 신뢰 프록시 |

## packages — Go 패키지 경계

| 페이지 | 무엇 |
|---|---|
| [[go-httpapi]] | HTTP 표면 — 응답 shape 소유·라우트 순서 계약·정적 화이트리스트 |
| [[go-store]] | 저장·집계(가장 큰 패키지) — `UsageByModel` 세 경로·부동소수 순서 의존 |
| [[go-intake]] | **신뢰 경계** — `SafeKeyword`·`NormKeyOf`·자리표시자 접기·상수 중복 |
| [[go-cost]] | 토큰→USD 표면 + 짝 패키지 `stats`·`tz` |
| [[go-db]] | sqlite\|pg 어댑터 · `?→$n` · rlsguard · 수제 커넥션 풀 |
| [[go-org]] | org·인제스트 키 — 해시 저장·상수시간 해석·sqlite 컬럼 보강 |
| [[go-identity]] | 머신→계정 귀속 + 감사 로그 · restamp 드리프트 |

## platforms — 수집 대상 4종

| 페이지 | 무엇 |
|---|---|
| [[platform-coverage]] | **축 × 플랫폼 표** — `미수집` vs `해당 없음` 을 가르는 규율 |
| [[claude-code]] | 기준 플랫폼 — 축 최다·16MB 줄 상한·세션 단위 누적 |
| [[codex]] | OpenAI — **누적값 차분**(더하면 배로 부푼다)·`.zst` 한계 |
| [[gemini-cli]] | Google 오픈소스 — **리플레이(tail 파싱 불가)**·thoughts 를 더한다 |
| [[antigravity]] | **디스크에 토큰이 없다** — statusLine 캡처 + Stop 훅 플러시 |

## concepts — 결정과 불변식

| 페이지 | 무엇 |
|---|---|
| [[honest-uncertainty]] | ★ **관통 규율** — 근사를 정확한 값으로 위장하지 않는다 |
| [[model-three-paths]] | ★ `①+②+③ == Totals` — 모델별 집계의 세 경로 |
| [[golden-contract]] | ★ 골든 44 — 합격 기준·두 가지 금기·시드 8세션의 함정 |
| [[cost-model]] | "API 환산 비용" — 공급사별 정규화·모델별 캐시 배수·계단 요금 |
| [[data-policy]] | 무엇을 저장하지 않는가 — 7축·keyword 90일·무기한 보관의 근거 |
| [[attribution]] | 귀속 우선순위 ①키 결속 ②머신 매핑 ③클라이언트 주장 |
| [[auth-scopes]] | 자격 5종 · 규칙 5개 · 서버가 거부하는 것 · 엔드포인트 표 |
| [[idempotency]] | 절대값 UPSERT — 중복 전송이 정상 동작에 포함된다 |
| [[tenancy-rls]] | 세 모드 · `ReadOnly = remote && !MultiTenant` · RLS 롤 강제 |
| [[boot-gates]] | 부팅 거부 8종 · bad ports · 기동 로그가 말해 주는 것 · env 전체 |

## operations — 실행 절차

> 명령 원문의 단일 출처는 `docs/OPERATIONS.md`. 아래는 지도다.

| 페이지 | 무엇 |
|---|---|
| [[runbook]] | 절 지도 · 기동 요약 · 최초 관리자 · 되돌리기 · 정기 점검 |
| [[ingest-keys]] | 발급 4경로 · 스코프 실측 · 회전 순서 · 셀프서비스 · 퇴사 순서 |
| [[user-management]] | 관리 API · `sessionsRevoked`/`keysRevoked` · 마지막 관리자 보호 |
| [[cleanup]] | `placeholder-models`(라벨만) · `usage-rows`(**되돌릴 수 없다**) |
| [[troubleshooting]] | 증상 → 원인 색인 (401/403/데이터없음/브라우저/비용/pg/골든) |

## decisions — 되돌리기 어려운 선택

| 페이지 | 언제 | 무엇 |
|---|---|---|
| [[node-to-go-port]] | 2026-08-09 | Node → Go+Next.js 컷오버 · 검증 철학의 기원 |
| [[otlp-removal]] | 2026-08-10 | OTLP 제거 · 퍼스트파티 단일 경로로 |
| [[webroot-embed]] | 포팅 중 | `webroot/` 를 커밋한다 · 방어 4겹 · `all:` 접두사 |
| [[unified-install-script]] | Phase 1 S5 | 설치와 제거를 한 스크립트에 |

---

## 주제별 진입로

**"이 숫자를 믿어도 되나"**
→ [[model-three-paths]] → [[cost-model]] → [[honest-uncertainty]] → [[platform-coverage]]

**"누구 사용량인가"**
→ [[attribution]] → [[ingest-keys]] → [[go-identity]]

**"안전한가"**
→ [[auth-scopes]] → [[data-policy]] → [[tenancy-rls]] → [[boot-gates]]

**"고쳐도 되나"**
→ [[golden-contract]] → [[contract-harness]] → [[ci-gates]] → 해당 packages 페이지

**"지워야 한다"**
→ [[data-policy]] → [[user-management]] → [[cleanup]]

**"개발자 PC 를 붙인다 / 뗀다"**
→ [[installer]] → [[ingest-keys]] → [[collector]]
