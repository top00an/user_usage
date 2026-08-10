# Phase 1 구현계획 — 멀티테넌트 코어 (판매 가능 SaaS의 첫 단계)

> 목표: 한 배포에 **여러 org 를 격리 수용**하는 멀티테넌트 SaaS 코어. 현재는 인프라(RLS·
> `app.tenant_id`)가 멀티테넌트지만 `server.go:145` 가 모든 요청에 **단일 `cfg.Tenant`** 를
> 주입해 실질 싱글테넌트다. 이 스위치를 **키/토큰 → org·tenant 해석**으로 바꾼다.

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
- 인증 게이트: `go/internal/httpapi/auth.go`·`server.go`(테넌트 해석 지점만)
- 프로비저닝: `go/cmd/usage-server`(서브커맨드) 또는 `go/internal/httpapi`(admin API)
- 설정: `go/internal/config/config.go`(멀티테넌트 모드 플래그)

## 슬라이스 (각각 test-first · 격리 회귀 게이트로 닫는다)

### S1 — org·ingest key 데이터 모델 + 해석기 (이 슬라이스부터 시작)
- `orgs(id, tenant_id, name, created_at, status)`, `ingest_keys(key_hash, org_id, created_at, revoked_at, last_used_at)`.
- key 는 평문 저장 금지 — `sha256(key)` 만 저장. 발급 시 1회 평문 노출.
- `org.ResolveIngestKey(ctx, db, plaintext) -> (tenant, orgID, ok)` — 상수시간·해지 확인.
- AC: 단위 테스트(발급→해석→해지 후 거부, 잘못된 키 거부, 상수시간). sqlite+pg 양쪽. `go test` green.

### S2 — 인테이크 게이트를 키→tenant 로 전환
- `Authenticate` 가 intake 스코프일 때 **어느 org 인지** 를 실어 준다(Auth.Tenant/OrgID).
- `server.go:145` 를 "고정 cfg.Tenant" → "해석된 tenant(없으면 default)" 로. 열람도 org 로그인/토큰 → tenant.
- **하위호환**: 단일 `USAGE_INTAKE_TOKEN`/`USAGE_ADMIN_TOKEN` 모드(현행)도 유지(멀티테넌트 모드 플래그로 분기) — 골든 44/44 안 깨지게.
- AC: 멀티테넌트 모드에서 org A 키로 넣은 사용량이 org B 열람에 **안 보임**(크로스테넌트 0). 기존 골든 게이트 유지.

### S3 — 프로비저닝
- `usage-server org create --name X` → org+tenant 생성, `usage-server key issue --org X` → 평문 키 1회 출력. (또는 admin API + 슈퍼관리자 토큰.)
- AC: CLI 로 org 2개·키 2개 만들고 S2 격리 테스트가 실제 발급 키로 통과.

### S4 — rate-limit + 쿼터 + 격리 CI 게이트
- per-key/tenant rate-limit(토큰버킷), per-tenant 세션/토큰 쿼터.
- 크로스테넌트 격리 회귀를 **CI 필수 게이트**로.
- AC: 초과 시 429, 격리 테스트 CI 통과.

### S5 — Claude Code 훅 배포물
- `SessionEnd` 훅 스크립트(세션 집계→org 키로 `POST /api/usage`) + 설치 스니펫. 백그라운드 collector 와 같은 페이로드.
- AC: 훅으로 넣은 세션이 대시보드에 반영(E2E).

## 다음 페이즈(요약)
- **Phase 2**: ~~OTLP `/v1/logs` 수신구 + `claude.*` 속성 스펙 + (선택) export~~ → **제거됨(2026-08-10).**
  사유: 공식 Claude Code OTel(`claude_code.*`)과 비호환인 자체 규약(`claude.*`)이라 제품에서 제외.
  퍼스트파티 수집기 단일 경로 + 멀티플랫폼(Codex·Gemini) 확장으로 전환한다.
- **Phase 3**: 셀프서브 온보딩 UI + 미터링/청구(결제 대행 연동) + 플랜/쿼터 + 엔터프라이즈(SSO·데이터 거주).

## 진행 방식
- 위임 서브시스템이 현재 다운 → **순차(메인 세션)**. 복구되면 슬라이스를 오너별 병렬로 전환.
- 매 슬라이스: 테스트 먼저 → 구현 → `go test`·`contract:verify`(회귀) → 크로스테넌트 격리 실측.
- 되돌리기 어려운/외부 결정(결제·호스팅·SSO) 지점에서 멈춰 확인.
