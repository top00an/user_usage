---
type: hub
tags: [로그]
updated: 2026-08-12
---

# 로그

append-only. 항목 접두사는 `## [YYYY-MM-DD] <종류> | <제목>` 으로 고정한다 —
`grep '^## \[' wiki/log.md | tail -5` 로 최근 이력을 뽑을 수 있다.

종류: `ingest`(원천 반영) · `query`(질의 결과 편입) · `lint`(점검) · `meta`(위키 자체)

---

## [2026-08-12] ingest | UI 텍스트 정리 4건 + 테마 토글

브라우저에서 지목받은 요소를 걷어내고, 그 과정에서 드러난 테마 동작을 정리했다.

**제거 4건**

| 무엇 | 어디 |
|---|---|
| 사이드바 역할 배지(`관리자`) | `Dashboard.tsx` — `roleLabel` 도 함께 |
| 탭 설명문(`content-desc`) | `Dashboard.tsx` — `TABS[].desc` 는 남았고 이제 **미사용** |
| 공통 코어 비교 표의 caption | `platform/PlatformSummary.tsx` |
| **플랫폼 지원표 전체**(details+표) | `platform/PlatformSummary.tsx` |

**추가** — `lib/theme.ts` + `components/ThemeToggle.tsx`(사이드바 발치).
흰 화면의 원인은 버그가 아니라 `theme-boot.js` 의 OS 추종이었다. 토글은 그 위에 **명시적
선택**을 얹는다(고른 뒤로는 OS 를 안 따른다).

**영향 — [[honest-uncertainty]] 가 한 화면에서 약해졌다.** 지원표가 대시보드 플랫폼 섹션에서
`미수집` 을 보여주던 **유일한 자리**였다. 그 섹션의 축은 전부 네 플랫폼이 수집하므로 배지가
붙을 자리가 남지 않았다. 표기는 아키텍처 탭과 사용 추적 탭 축 패널에만 남는다.
갱신: [[honest-uncertainty]] · [[platform-coverage]] · [[web-dashboard]]

**테스트** — 지원표를 읽던 2건과 `supportCellText` 헬퍼, 역할 배지 1건을 걷어내고 테마 토글
3건을 추가했다. 판정(`supportOf`) 단위 테스트와 아키텍처 탭 렌더 테스트가 그대로라 **판정
커버리지는 잃지 않았다.**

**게이트** — web lint 0 · **web 233/233** · `go vet`+`go test` 통과 ·
`scripts/build.sh` 재빌드 · **contract:verify 골든 44/44**.

⚠ 실행 중인 `:4191` 서버는 **옛 바이너리**다(10:13 기동). 임베드 산출물이라 **재기동해야**
화면에 반영된다 → [[webroot-embed]].

---

## [2026-08-12] meta | 위키 생성 — 초기 컴파일

레포 전체를 원천으로 읽어 위키 **35 페이지**를 만들었다.

**읽은 원천**: `README.md`(496줄) · `docs/OPERATIONS.md`(1311줄) · `PORT-STATUS.md` ·
`go/CONTRACT.md` · `contract/README.md` · `web/README.md` · `deploy/README.md` ·
`docs/VERIFICATION.md` · `docs/PLAN-{saas-ingestion,phase1-multitenant}.md` ·
`.github/workflows/ci.yml` · `package.json` · `.env.example` · git 로그 50건 ·
`go/internal/{cost,intake,store}` 와 `collector/internal/{transcript,codex,gemini,antigravity,policy}`
의 패키지 주석 · `web/components/Dashboard.tsx`

**만든 것**: [[index]] · [[overview]] · [[risks]] · [[sources]] · [[CLAUDE|스키마]] ·
systems 7 · packages 7 · platforms 5 · concepts 10 · operations 5 · decisions 4

**점검에서 발견한 모순 3건** → [[risks]] §F:

- F1 — `ci.yml` 스텝 이름 "백엔드 11 패키지" vs 실제 12 (원천이 이미 자각)
- F2 — `PORT-STATUS.md` 리스크 2 는 `verify:embed` 를 "CI 에 걸 것"이라 하지만 `ci.yml` 은
  **일부러 걸지 않는다**(turbopack 해시 비결정성). 결정이 바뀌었는데 PORT-STATUS 미갱신
- F3 — `.env.example` 의 `USAGE_PG_POOL_MAX`·`USAGE_RETENTION_INTERVAL_H` 가 README 환경변수
  표에 없다 (「추정」 — 누락으로 보임)

**설계 판단**: 명령어 원문은 복제하지 않았다. `docs/OPERATIONS.md` 가 단일 출처이고, 두 벌이
되면 한쪽만 고쳐지는 날이 온다 — 이 레포가 부팅 로그 문구에서 실제로 겪은 실패다
([[boot-gates]] 의 "탭 이름을 열거하지 않는다" 항목). 위키의 operations 페이지들은
**지도이지 사본이 아니다**.

---

# 레포 타임라인 (git 로그에서 재구성)

위키 이전의 이력. 원천은 커밋 메시지와 각 문서의 날짜 표기다.

## [2026-08-07] 포팅 착지 — Node 와 Go 공존

백엔드 Go · 프런트 Next.js 전환이 착지했고 Node 서버가 그대로 살아 있던 시점.
Node 를 남긴 이유는 **골든의 출처**였기 때문 → [[node-to-go-port]].

검증: 골든 44/44 × 3회 · `go test` 11 패키지 · `npm test` 208 · web 76/76 ·
브라우저 왕복 20/20(Go 단독) · 34/34(Node+프록시).

## [2026-08-09] ★ 컷오버 — Node 제거, 골든 동결

- pg(remote) 실측 검증 통과 → **pg 전용 버그 1건 수정**(`SUM/AVG(bigint)` 스캔 결함)
- 수집기(`collector/`) 추가
- 부동소수 파리티 잔여 2건 해소(FMA 융합 차단 · 결정적 정렬 타이브레이크 2곳)
- **Node 구현 제거** → `contract/golden/` **동결**
- 멀티테넌트 격리 pg RLS 실측 → `ReadOnly = remote && !MultiTenant` 결함 수정
- web: ECharts 대시보드 · 드래그 재배치 · LOC/편집 결정 수집 · 차트 빌더

→ [[node-to-go-port]] · [[golden-contract]] · [[tenancy-rls]]

## [2026-08-10] 온보딩 · OTLP 제거 · 멀티플랫폼 (PR #14~#24)

하루에 11개 PR 이 들어간 날.

| PR | 무엇 |
|---|---|
| #14 | **ID/PW 세션 로그인** · AWS 운영 IaC · **온보딩 UI + 원커맨드 연동** |
| #15 | 아키텍처 탭 |
| **#16** | ★ **OTLP 수집·내보내기 경로 제거** → [[otlp-removal]] |
| #17 | **멀티플랫폼 수집** — `platform` 축 · 멀티공급사 단가 · Codex 리더 |
| #18 | **Gemini CLI 리더** — 전체 리플레이 · 토큰 정규화 · 함정 10종 방어 |
| #19 | Gemini LOC · `platform` 필터를 5개 엔드포인트로 |
| #20 | `lineCount` 통일 · **계단(롱컨텍스트) 요금 서버측** · CI 마이그레이션 게이트 복구 |
| #21 | **Antigravity CLI 수집** — statusLine 경로 · platform 분리 |
| #22 | Antigravity 원커맨드 통합 — 캡처·플러시·**상태줄 체이닝** |
| #23 | 인제스트 키가 단일테넌트에서도 동작(온보딩이 반쪽으로 끝나던 결함) |
| #24 | 키 스코프 표시 · 지원표 정정 · 문서 4플랫폼 · 브루트포스 방어 |

→ [[platform-coverage]] · [[installer]] · [[cost-model]]

## [2026-08-11] 자리표시자 · 사용자 관리 · 셀프서비스

- `<synthetic>` 이 모델 이름으로 새던 것 수정(**7개 엔드포인트**) + `cleanup placeholder-models`
- 기동 로그가 로그인 방식을 바로 지시하게 → [[boot-gates]]
- 데이터 없을 때 **가짜 비율을 그리던 차트** 수정 · 실물 하네스 재작성
- **인제스트 키를 사용자에 묶고 귀속을 서버가 정한다** → [[attribution]]
- 사용자·역할 관리 API — **마지막 관리자 보호를 원자적으로** → [[user-management]]
- 관리 탭 · 연동 탭을 셀프서비스로 (실물 게이트 33→62)

## [2026-08-12] 계약 게이트 복원 · 삭제 수단 · uninstall

- **`contract capture` 를 되살렸다** — 컷오버로 사라져 그동안 **응답 shape 을 아무도 바꿀 수
  없었다** → [[contract-harness]]
- `cleanup usage-rows` — 수집된 사용량을 지우는 수단 → [[cleanup]]
- 세션 응답에 `platform` 을 싣는다 — **골든을 정식으로 갱신한 첫 사례**
- `install.sh --uninstall` — 연동을 걷어내는 경로 + 사본 동기 가드 → [[installer]]
- Merge: *Go/Next.js 포팅 이후의 정합성 · 관리 카테고리 · 계약 게이트 복원*

---

## 다음에 볼 것 (lint 가 제안하는 것)

- **A5** — Gemini `tokens.tool` 을 실데이터로 확인 → [[risks]]
- **B1** — Node 가 사라졌으니 부동소수 덧셈 순서를 정리할 자리가 열렸다 → [[go-store]]
- **F2** — `PORT-STATUS.md` 리스크 2 를 현행 CI 결정에 맞게 갱신할 것인지 결정
- **E1** — `usage_audit` 의 pg 스키마를 만들 것인지 결정 → [[go-identity]]
- **D** — per-tenant 쿼터(Phase 1 의 마지막 미구현) → [[tenancy-rls]]
