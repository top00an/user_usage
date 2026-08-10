# Phase 1 구현계획 — 멀티테넌트 코어 (판매 가능 SaaS의 첫 단계)

> ## ✅ 이 계획은 완료됐다 (2026-08-10) — 쿼터 하나만 남았다
>
> **이 문서는 계획서다.** 아래 슬라이스별 상태가 실제 구현 현황이고, 본문의 미래형 서술
> ("~로 바꾼다"·"~를 만든다")은 **착수 시점의 계획**이지 현재 동작 설명이 아니다.
> 현재 동작의 사용법은 [`../README.md`](../README.md) 과 [`OPERATIONS.md`](OPERATIONS.md) 가 소유한다.
>
> | 슬라이스 | 상태 |
> |---|---|
> | S1 org·ingest key 데이터 모델 + 해석기 | **구현 완료** |
> | S2 인테이크 게이트를 키→tenant 로 전환 | **구현 완료** |
> | S3 프로비저닝 CLI | **구현 완료**(계획보다 넓어짐 — team·member·user 까지) |
> | S4 rate-limit + 쿼터 + 격리 CI 게이트 | **부분** — rate-limit ✅ · 격리 CI 게이트 ✅ · **쿼터 미구현** |
> | S5 훅 배포물 | **구현 완료**(계획을 넘어섬 — 원커맨드 설치기로 대체) |
>
> 당시 목표: 한 배포에 **여러 org 를 격리 수용**하는 멀티테넌트 SaaS 코어. 착수 시점에는
> 인프라(RLS·`app.tenant_id`)가 멀티테넌트인데도 서버가 모든 요청에 **단일 `cfg.Tenant`** 를
> 주입해 실질 싱글테넌트였다. 그 스위치를 **키/토큰 → org·tenant 해석**으로 바꾸는 것이 이 계획이다.
>
> ⚠ 본문의 `server.go:145` 는 **착수 당시의 줄 번호**다. 그 뒤 파일이 바뀌어 지금은 다른 코드를
> 가리킨다 — 줄 번호로 찾지 말고 `go/internal/httpapi/server.go` 의 테넌트 해석 지점을 볼 것.

## "판매 가능 SaaS"의 정직한 범위

코드로 만드는 것(내가 함) ↔ 사업·운영(사람/외부 필요, 코드 밖):

| 코드로 (이 프로그램) | 사업·운영 (여러분/외부) |
|---|---|
| 멀티테넌트 인제스트·격리·온보딩 API | 호스팅(클라우드 계정·도메인·TLS·배포) |
| org·키·열람 인증, rate-limit | 결제 대행(Stripe 등) 계정·웹훅 시크릿 |
| Claude Code 훅 배포물, 퍼스트파티 수집기 | SSO IdP(Google/Okta) 등록·정책 |
| 미터링/쿼터 로직, 셀프서브 온보딩 UI | 약관·개인정보처리방침(법무), 고객지원 |

→ 나는 **엔지니어링을 검증 가능한 증분으로** 만들고, 각 외부 결정(결제 대행·호스팅·SSO 공급자)이
필요한 지점에서 **멈춰 확인**한다. 코드가 준비돼도 "판매"는 그 외부 셋업이 있어야 켜진다.

## 계약 동결 (파일 오너)
- 데이터/스토어: `migrations/pg/003x_orgs.sql` + `go/internal/org/**`(신규 패키지)
  → 실제 파일: [`migrations/pg/0030_orgs.sql`](../migrations/pg/0030_orgs.sql) · `go/internal/org/org.go`
- 인증 게이트: `go/internal/httpapi/auth.go`·`server.go`(테넌트 해석 지점만)
- 프로비저닝: `go/cmd/usage-server`(서브커맨드) 또는 `go/internal/httpapi`(admin API)
  → **둘 다 만들어졌다**: CLI(`go/cmd/usage-server/provision.go`) + admin API(`go/internal/httpapi/onboarding.go`)
- 설정: `go/internal/config/config.go`(멀티테넌트 모드 플래그) → `USAGE_MULTITENANT`

## 슬라이스 (각각 test-first · 격리 회귀 게이트로 닫는다)

### S1 — org·ingest key 데이터 모델 + 해석기 ✅ **구현 완료**
- `orgs(id, tenant_id, name, created_at, status)`, `ingest_keys(key_hash, org_id, created_at, revoked_at, last_used_at)`.
- key 는 평문 저장 금지 — `sha256(key)` 만 저장. 발급 시 1회 평문 노출.
- `org.ResolveIngestKey(ctx, db, plaintext) -> (tenant, orgID, ok)` — 상수시간·해지 확인.
- AC: 단위 테스트(발급→해석→해지 후 거부, 잘못된 키 거부, 상수시간). sqlite+pg 양쪽. `go test` green.
- **결과**: 완료. 공개 해석 함수의 실제 이름은 `org.Resolve` 다(계획의 `ResolveIngestKey` 가 아니다).
  키 형식은 `uu_ing_<rand>` — **org 식별자를 키에 박지 않는다**(박으면 키 하나가 조직명을 흘린다).

### S2 — 인테이크 게이트를 키→tenant 로 전환 ✅ **구현 완료**
- `Authenticate` 가 intake 스코프일 때 **어느 org 인지** 를 실어 준다(Auth.Tenant/OrgID).
- `server.go:145` 를 "고정 cfg.Tenant" → "해석된 tenant(없으면 default)" 로. 열람도 org 로그인/토큰 → tenant.
- **하위호환**: 단일 `USAGE_INTAKE_TOKEN`/`USAGE_ADMIN_TOKEN` 모드(현행)도 유지(멀티테넌트 모드 플래그로 분기) — 골든 44/44 안 깨지게.
- AC: 멀티테넌트 모드에서 org A 키로 넣은 사용량이 org B 열람에 **안 보임**(크로스테넌트 0). 기존 골든 게이트 유지.
- **결과**: 완료. 하위호환 유지됨(골든 44/44). 크로스테넌트 0 은 pg 로 실측
  ([`VERIFICATION.md`](VERIFICATION.md) §3) + CI `pg-isolation` 잡이 상시 재검증.
  **추가로 배운 것**: sqlite + 멀티테넌트는 격리가 성립하지 않아 **부팅을 거부**하도록 했다
  (경고로 두면 여러 org 데이터가 한 파일에 조용히 섞인다).

### S3 — 프로비저닝 ✅ **구현 완료 (계획보다 넓다)**
- `usage-server org create --name X` → org+tenant 생성, `usage-server key issue --org X` → 평문 키 1회 출력. (또는 admin API + 슈퍼관리자 토큰.)
- AC: CLI 로 org 2개·키 2개 만들고 S2 격리 테스트가 실제 발급 키로 통과.
- **결과**: 완료. 계획의 "또는"이 아니라 **둘 다** 만들어졌다 — 운영자용 CLI 와 대시보드용
  admin API(`/api/admin/keys`). 서브커맨드도 `org`·`key` 를 넘어 `team`·`member`·`user` 까지 늘었다.

### S4 — rate-limit + 쿼터 + 격리 CI 게이트 ⚠ **부분 구현**
- per-key/tenant rate-limit(토큰버킷), per-tenant 세션/토큰 쿼터.
- 크로스테넌트 격리 회귀를 **CI 필수 게이트**로.
- AC: 초과 시 429, 격리 테스트 CI 통과.
- **결과**:
  - rate-limit — **구현 완료**(`USAGE_INTAKE_RATE` 기본 20/초 · `USAGE_INTAKE_BURST` 기본 40).
  - 격리 CI 게이트 — **구현 완료**(`.github/workflows/ci.yml` 의 `pg-isolation` 잡).
  - **per-tenant 쿼터 — 미구현.** rate-limit 은 순간 폭주만 막고 **누적 상한은 없다.**
    이것이 Phase 1 에서 유일하게 닫히지 않은 항목이다.

### S5 — 훅 배포물 ✅ **구현 완료 (계획을 넘어섬)**
- `SessionEnd` 훅 스크립트(세션 집계→org 키로 `POST /api/usage`) + 설치 스니펫. 백그라운드 collector 와 같은 페이로드.
- AC: 훅으로 넣은 세션이 대시보드에 반영(E2E).
- **결과**: 완료. 계획의 "훅 스크립트 + 붙여넣을 스니펫"은 **원커맨드 설치기로 대체**됐다
  (`scripts/install.sh`, 서버가 `/install.sh` 로 서빙). 한 줄이 다운로드·설정(600)·훅 등록·
  초기 백필까지 한다. 등록은 비파괴·멱등이고 덮기 전 `.bak` 을 남긴다.
  **범위도 넓어졌다**: Claude `SessionEnd` 에 더해 Antigravity `statusLine`+`Stop` 훅까지 등록하고,
  파일 기반 원천은 Claude 하나가 아니라 Codex·Gemini CLI 를 포함한 셋이다.

## 다음 페이즈(요약)
- **Phase 2** — **완료.** ~~OTLP `/v1/logs` 수신구 + `claude.*` 속성 스펙 + (선택) export~~ →
  **제거됨(2026-08-10, PR #16).** 사유: 공식 Claude Code OTel(`claude_code.*`)과 비호환인 자체
  규약(`claude.*`)이라 제품에서 제외. 대신 퍼스트파티 수집기 단일 경로 + 멀티플랫폼 확장으로
  전환했고, **Claude·Codex·Gemini CLI·Antigravity 4종이 그 결과다.**
- **Phase 3** — **미착수.** 셀프서브 온보딩 UI + 미터링/청구(결제 대행 연동) + 플랜/쿼터 +
  엔터프라이즈(SSO·데이터 거주). 대부분 **코드 밖 결정**(결제 대행·IdP·데이터 거주 정책)이 먼저다.

## 진행 방식(당시)
- 위임 서브시스템이 다운이라 **순차(메인 세션)** 로 진행했다.
- 매 슬라이스: 테스트 먼저 → 구현 → `go test`·`contract:verify`(회귀) → 크로스테넌트 격리 실측.
- 되돌리기 어려운/외부 결정(결제·호스팅·SSO) 지점에서 멈춰 확인.

> 이 방식은 지켜졌고, 골든 44/44 는 Phase 1 전 구간에서 깨지지 않았다.
> 실측 기록은 [`VERIFICATION.md`](VERIFICATION.md) 에 있다.
