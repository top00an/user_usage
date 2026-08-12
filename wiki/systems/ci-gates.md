---
type: system
tags: [ci, 게이트, 검증]
updated: 2026-08-12
sources: [".github/workflows/ci.yml", "docs/VERIFICATION.md", "docs/OPERATIONS.md"]
---

# CI 게이트 — 무엇이 머지를 막나

`.github/workflows/ci.yml`. 헤더가 정책을 적어 두었다: **회귀(go test) · 골든 계약
(contract:verify) · 크로스테넌트 격리(pg RLS) 셋이 초록이어야 머지한다.** 로컬에서 도는 것과
같은 명령을 CI 가 강제한다.

## 잡 두 개

### `test`

| 스텝 | 무엇 |
|---|---|
| go vet + test | `cd go && go vet ./... && go test ./... -count=1` |
| collector test | `cd collector && go vet ./... && go test ./...` |
| **collector e2e** | 실제 서버 바이너리 + 실제 수집기로 수집→조회 전 경로 |
| web test | `cd web && npm ci && npm test` (vitest) |
| build | `bash scripts/build.sh` — 유일 빌드 경로 |
| **contract:verify** | 빈 DB·`USAGE_KEYWORD_RETENTION_DAYS=off` 로 띄운 서버에 골든 44 대조 |

### `pg-isolation`

**pg RLS 는 실제 PostgreSQL 에서만 성립한다.** postgres:16 서비스를 띄우고 앱 롤로 돌린다.

```bash
CREATE ROLE usage_app LOGIN NOSUPERUSER NOBYPASSRLS PASSWORD 'app'
CREATE DATABASE usage OWNER usage_app
GRANT ALL ON SCHEMA public TO usage_app     # pg15+ 는 비소유자 public CREATE 를 기본 차단
```

그다음 `cd go && go test ./internal/db/ -run PG -v`.

앱 롤이 테이블 소유자가 되지만 마이그레이션이 `FORCE ROW LEVEL SECURITY` 라 **소유자도 RLS 를
탄다** — `NOSUPERUSER`·`NOBYPASSRLS` 와 합쳐져 크로스테넌트 격리가 성립한다 → [[tenancy-rls]].

## 임베드 드리프트 검사는 걸지 않는다 (의도)

`ci.yml` 주석이 이유를 적고 있다: Next.js/turbopack 이 빌드마다 청크 파일명 해시를
**논결정적으로** 낸다. 커밋된 `webroot/` 는 배포에 쓰는 유효한 산출물이고, 재빌드가 "다른
해시의 같은 내용"을 낼 뿐이라 `git diff` 는 항상 갈린다.

대신 방어는 **내용 수준**에서 한다 — `static_test.go` 의
`TestIndexHTMLReferencesOnlyEmbeddedAssets` → [[webroot-embed]].

> ⚠ **모순 표기.** `PORT-STATUS.md` 리스크 2 는 `npm run verify:embed` 를 "CI 에 걸 것"이라
> 적었지만, `ci.yml` 은 위 이유로 **일부러 걸지 않는다**. `verify:embed` 스크립트는 여전히
> 존재하며 로컬 확인용이다. → [[risks]]

## 로컬에서 같은 것 돌리기

```bash
cd go        && go test ./... && go vet ./...
cd collector && go test ./... && go vet ./...
cd web       && npm test
```

## 옵트인 게이트 (환경이 있을 때만)

없으면 skip 한다 — "안 돌았다"가 초록불로 위장되지 않도록 로그에 남는다.

| 게이트 | 켜는 조건 |
|---|---|
| collector E2E | `USAGE_E2E_SERVER_BIN` + `USAGE_E2E_COLLECTOR_BIN` |
| pg 통합·RLS 위반 롤 대조 | `USAGE_TEST_PG_URL` (앱 롤로) |
| 프런트 실물 왕복 | `cd web && npm run verify:live` (크로미움 필요) |

## 알려진 표시 오류

⚠ `test` 잡의 스텝 이름이 **"백엔드 11 패키지"** 인데 실제는 **12** 다. 표시 문구일 뿐
게이트 동작(`go test ./...`)에는 영향이 없지만, 세는 값이 문구와 어긋나 있다
(`docs/VERIFICATION.md` 가 이 불일치를 스스로 기록해 두었다).

⚠ `pg-isolation` 잡은 **CI 첫 실행에서 role/grant 셋업을 1회 확인할 것** — 로컬은 docker 로
실측 완료지만 CI 경로는 미검증이라고 원천이 밝히고 있다 → [[risks]].

## 관련

[[contract-harness]] · [[golden-contract]] · [[tenancy-rls]] · [[webroot-embed]] · [[risks]]
