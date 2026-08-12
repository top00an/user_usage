---
type: system
tags: [go, 서버, 백엔드]
updated: 2026-08-12
sources: ["README.md", "go/CONTRACT.md", "docs/OPERATIONS.md", "go/cmd/usage-server/"]
---

# usage-server — 단일 실행 파일

Go 백엔드와 Next.js 프런트를 `go:embed` 로 하나로 묶은 **배포 산출물 한 개**. HTTP 서버이자
운영 CLI다.

## 두 얼굴

```bash
./go/usage-server                      # ① HTTP 서버
./go/usage-server org create --name X  # ② 프로비저닝·유지보수 CLI
```

CLI 는 서버와 **같은 env** 를 본다(`USAGE_DB_MODE`·`DATABASE_URL`·`USAGE_DATA_DIR`).
관리자 토큰 게이트가 없는 이유는 **DB 접근 자체가 권한**이기 때문이다 — 배포 호스트에서
운영자가 직접 부르는 명령이다.

서브커맨드: `org` · `key` · `user` · `team` · `member` · `cleanup`
→ [[ingest-keys]] · [[user-management]] · [[cleanup]]

## 기동

```bash
bash scripts/build.sh                                  # 유일 빌드 경로
USAGE_ADMIN_TOKEN=$(openssl rand -hex 24) \
  USAGE_DATA_DIR=./data ./go/usage-server              # → http://127.0.0.1:4191
```

세 가지 모드가 있고 **배선이 다르다**:

| 모드 | env | 성질 |
|---|---|---|
| local | (기본) `USAGE_DB_MODE=local` | sqlite. 쓰기 가능 |
| remote 조회 | `USAGE_DB_MODE=remote` + `DATABASE_URL` | **읽기 전용** — 인테이크·귀속 교정·보존 정리기가 **등록되지 않는다**(404, 405 가 아니다) |
| SaaS | 위 + `USAGE_MULTITENANT=1` | pg 쓰기. 격리는 RLS 가 진다 → [[tenancy-rls]] |

핵심 한 줄: `ReadOnly = remote && !MultiTenant`. 이 식이 없어서 **멀티테넌트 인테이크가
막혔던 실결함**이 pg 실측에서 발견돼 고쳐졌다(`docs/VERIFICATION.md` §3).

## 부팅에서 거부하는 것

설정 실수를 런타임까지 끌고 가지 않는다 → [[boot-gates]] 가 전체 목록.
잘못된 설정은 **모아서** 보고하고 exit 2.

## 패키지 구조

```
go/
  cmd/usage-server/   진입점 — 부팅 게이트·시그널·보존 정리기·프로비저닝 CLI·cleanup
  internal/
    httpapi/          [[go-httpapi]]  라우터·인증 게이트·정적 서빙·인테이크/관측/온보딩
    store/            [[go-store]]    사용량 저장·집계 (가장 큰 패키지)
    intake/           [[go-intake]]   클라이언트 보고 정규화 — 신뢰 경계(순수)
    org/              [[go-org]]      org·인제스트 키 (해시 저장)
    identity/         [[go-identity]] 머신→계정 귀속 + 감사
    cost/  stats/     [[go-cost]] · 분포(p95/p99)
    tz/  tenant/      집계 시간대(고정 KST) · 테넌트 컨텍스트
    db/               [[go-db]]       sqlite|pg 어댑터 · 마이그레이션 러너 · RLS 가드
    config/           부팅 설정 · 거부 게이트
```

**의존 방향은 한 방향뿐이다**(`go/CONTRACT.md`). 역방향 import 는 순환이 되어 Go 가
컴파일을 거부한다:

```
httpapi ──▶ store ──▶ db ──▶ tenant
   │          │
   └──▶ cost, stats, tz, intake, identity
```

`cost`·`stats`·`tz`·`intake` 는 **아무것도 import 하지 않는다**(표준 라이브러리 제외).
순수 함수만 담아 테이블 테스트로 완결되게 한 것이 이 설계에서 가장 싸게 확신을 얻는 자리다.

## 저장 테이블

| 테이블 | 담는 것 | 보존 |
|---|---|---|
| `usage_sessions` | 세션당 1행 — 토큰 4축·턴·시각·플랫폼 | 무기한 |
| `usage_series` | (세션, 시간, 모델)당 1행 — 모델별 정확값의 근거 | **무기한(프루닝 일부러 안 함)** |
| `usage_counters` | (세션, 축, 키)당 1행 — 7축 | `keyword` 만 90일 |
| `usage_recommendations` | 추천 호출 관측 — 카탈로그 공백 탐지 | 무기한 |
| `machine_identity` | 머신 → 계정 귀속 교정표 | 무기한 |
| `usage_audit` | 귀속·관리 감사 이력 | 무기한 |
| `orgs` · `ingest_keys` | org ↔ tenant 매핑, 키(해시만) | — |
| `auth_users` · `auth_sessions` | 사람 로그인 계정·세션 | — |
| `team_members` · `member_tokens` | 팀 배정 · 개인 열람 토큰 | — |

무엇을 왜 안 지우는지는 [[data-policy]]. 지우는 수단은 [[cleanup]].

## 스키마 소유권

- **sqlite** — 로드 시점에 DDL 을 직접 건다(멱등).
- **PostgreSQL** — `migrations/pg/*.sql` 이 소유. 러너는 `go/internal/db/migrate.go` 인데
  **자동 실행 경로에 올라가 있지 않다.** 되돌리기 어려운 작업은 사람이 명시적으로 돌린다.

> 마이그레이션 번호에 공백이 있다(0014·0015·0017·0026…). 의도된 것 — 기존 DB 의
> `schema_migrations` 와 대조할 수 있어야 하고, 러너는 없는 번호를 신경 쓰지 않는다.

⚠ `usage_audit` 은 **pg 스키마가 없다.** sqlite 쪽 DDL(`internal/identity/audit.go`)만
갖고 있다 → [[risks]].

## 관련

[[go-httpapi]] · [[auth-scopes]] · [[boot-gates]] · [[tenancy-rls]] · [[runbook]] ·
[[webroot-embed]] · [[golden-contract]]
