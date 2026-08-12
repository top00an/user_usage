---
type: concept
tags: [설정, 부팅, 게이트]
updated: 2026-08-12
sources: ["go/internal/config/config.go", "go/CONTRACT.md", "docs/OPERATIONS.md", ".env.example"]
---

# 부팅 거부 게이트 — 하나라도 빠지면 조용한 사고다

서버는 잘못된 설정을 **모아서** 알려주고 종료한다(exit 2). `config.Read(env)` 가
`(Config, []error)` 를 돌려주는 이유가 이것 — 하나씩 고치며 여러 번 재기동하지 않게.

## 거부 조건

| 조건 | 왜 |
|---|---|
| `USAGE_ADMIN_TOKEN` 없음 | 사람별 사용량·비용이 **무인증으로 열린다** |
| 토큰 16자 미만 | 짧은 토큰은 인증이 아니라 **설정 실수**다 |
| `USAGE_INTAKE_TOKEN == USAGE_ADMIN_TOKEN` | 분리한 것처럼 보이지만 **아무것도 분리되지 않았다** |
| `USAGE_DB_MODE` 오타 | local 로 **조용히 접지 않는다** |
| remote 인데 `DATABASE_URL` 없음 | 붙을 곳이 없다 |
| 포트가 WHATWG **bad ports** | 아래 |
| `USAGE_MULTITENANT` 인데 sqlite | RLS 가 없어 org 데이터가 섞인다 → [[tenancy-rls]] |
| RLS 롤 위반(SUPERUSER·BYPASSRLS) | 격리가 무력화되는데 **증상이 없다** |

## 왜 admin 토큰을 옵션으로 두지 않았나

> 옵션으로 두면 누군가는 반드시 토큰 없이 띄우고, 그때 아무 에러도 나지 않는다 — 사람별
> 사용량과 비용이 담긴 화면이라 그 경로를 남기지 않았다. (`README.md`)

## bad ports — 가장 진단하기 어려운 모양

브라우저가 차단하는 포트(**4190** · 6000 · 6667 등 WHATWG Fetch "bad ports")를 지정하면
부팅에서 거부한다.

그 포트에서는 **서버가 정상 기동하고 curl 도 200 을 받는데 브라우저에서만 아무것도 안 된다.**
로그도 테스트도 전부 초록색이라 "대시보드가 깨졌다" 외에는 단서가 남지 않는다.

⚠ 기본 포트는 **4191** 이다 — 4190 이 아니다. 한 글자 차이로 차단 포트가 된다.

## 거부하지 않는 하나 — RLS 판정 불가

터널이 안 붙어 **확인 자체가 불가능한** 경우는 거부하지 않고 stderr 에 경고만 남긴다.
붙지 못한 DB 는 노출도 없고, 여기서 죽이면 "터널을 먼저 뚫는다"는 정상 절차가 부팅 실패로
보인다. 판정 분기는 반드시 `Verdict.Rejects()` 로 → [[tenancy-rls]].

## 부팅이 말해 주는 것 (거부는 아니지만 중요)

### ① 인테이크 자격 분리 여부

```
  · 인테이크 자격: USAGE_INTAKE_TOKEN(보고 전용 — 조회 불가)          ← 정상
  · 인테이크 자격: USAGE_ADMIN_TOKEN 겸용 — 수집기에 배포하는 토큰이 곧 전원 열람 토큰이다.
```

두 번째 상태로 수집기를 배포하면 **팀원 수만큼 복제된 토큰 하나하나가 전사 열람 권한**이다
→ [[auth-scopes]].

### ② 로그인할 계정이 없음

```
  ⚠ tenant=default 에 사용자가 없다 — 화면은 뜨지만 로그인이 되지 않는다(401).
    USAGE_BOOTSTRAP_ADMIN_USER·USAGE_BOOTSTRAP_ADMIN_PASSWORD 로 재기동하거나,
    `usage-server user add -tenant <t> -username <u> -role admin` 으로 만들라.
```

계정이 없으면 화면은 정상으로 뜨는데 로그인만 401 이고, 그때 운영자에게 보이는 단서는
"비밀번호가 틀렸나"뿐이다. **서버는 답을 알고 있으므로 기동에서 말해 준다.**

> ⚠ 이 줄은 **탭 이름을 열거하지 않는다.** 앞 판본이 `두 탭(사용 추적·사용 관측)` 이었고,
> 로그인이 ID/PW 로 바뀌고 탭이 늘어난 뒤에도 그대로 남아 **운영자가 처음 보는 안내문이 틀린
> 방식을 지시**했다. 낡은 원인은 문구를 안 고쳐서가 아니라 **서버가 소유하지 않은 사실**
> (UI 탭 구성)을 찍었기 때문이다 — 화면 구성은 `README.md` 가 소유한다.
>
> 이 교훈이 이 위키의 스키마 규칙 4 로 이어진다 → [[CLAUDE]].

### ③ 단가표 경로 · 키워드 보존 · DB 롤 확인

```
  · 키워드 보존 정리: 90일
  · 단가표: config.json (없으면 시드 단가표를 쓴다)
  · DB 롤 확인 — 비-슈퍼·비-BYPASSRLS(RLS 테넌트 격리 성립)
```

cwd 가 레포 루트가 아니면 `config.json` 을 못 찾아 **조용히 시드 단가로 떨어진다** →
[[cost-model]].

## 환경변수 전체

| 변수 | 기본 | 뜻 |
|---|---|---|
| `USAGE_ADMIN_TOKEN` | — | **필수.** 조회 토큰(최소 16자) |
| `USAGE_INTAKE_TOKEN` | — | **권장.** 보고 전용 |
| `USAGE_PORT` / `USAGE_HOST` | `4191` / `127.0.0.1` | |
| `USAGE_DB_MODE` | `local` | `local`(sqlite) \| `remote`(pg) |
| `USAGE_DATA_DIR` | `./data` | sqlite 디렉터리 |
| `DATABASE_URL` | — | remote 접속 문자열 |
| `USAGE_MULTITENANT` | off | SaaS. **pg 전용** |
| `USAGE_TENANT` | `default` | 조회 테넌트 |
| `USAGE_INTAKE_RATE` / `_BURST` | `20` / `40` | 테넌트별 rate limit |
| `USAGE_SESSION_TTL` | `12h` | 로그인 세션 쿠키 수명 |
| `USAGE_TRUSTED_PROXY_COUNT` | `0` | 앞단 신뢰 프록시 홉. ALB 단독이면 `1` → [[deploy-aws]] |
| `USAGE_BOOTSTRAP_ADMIN_USER` / `_PASSWORD` / `_TENANT` | — / — / `default` | 최초 관리자(멱등) |
| `USAGE_KEYWORD_RETENTION_DAYS` | `90` | `off` 면 무기한 → [[data-policy]] |
| `USAGE_RETENTION_INTERVAL_H` | `24` | 정리기 주기 |
| `USAGE_PG_POOL_MAX` | `10` | pg 커넥션 상한 |
| `USAGE_CONFIG` | `./config.json` | 단가 오버라이드 |

상세는 `.env.example`.

## 관련

[[usage-server]] · [[tenancy-rls]] · [[auth-scopes]] · [[runbook]] · [[cost-model]]
