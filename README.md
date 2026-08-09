# user-usage — Claude Code 사용량 관측 대시보드

팀원 PC 에서 올라온 Claude Code 세션 텔레메트리를 모아 **누가 · 무엇을 · 얼마나 썼고 얼마어치인가**를
보여주는 조회 도구입니다. 의존성은 `pg` 하나(원격 PostgreSQL 모드에서만 실제로 로드됩니다).
프런트엔드는 빌드 단계가 없습니다 — 바닐라 ESM 과 CSS 파일 하나입니다.

```
USAGE_ADMIN_TOKEN=$(openssl rand -hex 24) npm start
# → http://127.0.0.1:4191
```

> ⚠ **이 문서는 Node 구현을 설명합니다.** 2026-08-07 에 **Go 백엔드 + Next.js 프런트** 포팅이
> 착지해 지금 두 구현이 공존합니다(`go/` · `web/`). Node 를 남긴 이유는 그것이 포팅 합격 판정의
> 근거(골든 44개)이기 때문입니다 — 무엇이 아직 검증되지 않았는지와 언제 Node 를 지울 수 있는지는
> [`PORT-STATUS.md`](PORT-STATUS.md) 를 보십시오.
>
> **도구의 성격과 데이터 정책(무엇을 저장하지 않는가 · 무엇이 언제까지 남는가)은 이 문서가 계속
> 단일 출처입니다.** 그 결정들은 구현과 무관하고, 두 구현 모두 그것을 따릅니다.

---

## 이 도구가 답하는 질문

| 질문 | 어디서 |
|---|---|
| 팀이 이번 달 얼마나 썼나 | **사용 추적** — 총계·일별 추이·사용자별·모델별 |
| 그게 API 종량제였다면 얼마인가 | **사용 관측** — 축별 비용 분해(캐시읽기가 보통 최대 축입니다) |
| 어떤 도구·명령·에이전트를 실제로 쓰나 | **사용 추적** — 7개 축(tool·bash·slash·skill·agent·mcp·keyword) |
| 어느 PC 가 보고를 멈췄나 | **사용 관측** — 수집 커버리지 |
| 이 숫자를 믿어도 되나 | 모델별 표의 **근거** 열 + 사용자별 커버리지 |

마지막 줄이 이 도구의 성격을 정합니다. 모델별 값은 두 근거를 더한 것이고 — 시간×모델 버킷이 있는
세션은 정확값, 없는 세션은 그 세션의 최빈 모델에 통째로 귀속된 근사값입니다 — 화면은 그 비율을
**밝힙니다.** 근사를 정확한 값으로 위장하지 않는 것이 여기서는 기능입니다.

### 무엇을 저장하지 않는가

수집되는 것은 **집계뿐**입니다. 프롬프트 원문 · 파일 경로 · 명령 인자는 저장하지 않습니다.
`bash` 축은 선두 실행파일만 셉니다(`git`, `npm`, `pytest` — 인자 없음). `keyword` 축만 사람이 입력한
말에서 나오므로 **90일 후 자동 삭제**됩니다(`USAGE_KEYWORD_RETENTION_DAYS`). 화면이 그 정책을
스스로 표시합니다 — 추세가 끊기는 이유를 보는 사람이 알아야 하고, 보관 기간은 팀에게 공개돼야
하는 약속이기 때문입니다.

`keyword` 축은 어휘가 열려 있는 유일한 축이라 서버가 한 번 더 거릅니다. API 키·토큰 모양
(벤더 접두사 · 32자 이상 hex · 대소문자+숫자가 섞인 24자 이상 문자열), 이메일과 접속 문자열
조각, 10자리 이상 연속 숫자, `키=값` 형태는 **저장 전에 버립니다**(`lib/intake.js` 의
`safeKeyword`). 수집기가 클라이언트에서 먼저 거른다는 전제이지만 신뢰하지 않습니다 — 수집기는
팀원 PC 에서 도는 별도 프로세스라 서버보다 낡을 수 있고, 무엇보다 한 번 저장되면 지우는 비용이
훨씬 큽니다. 판정은 언제나 **버리는 쪽으로** 기울입니다.

여전히 남는 것: 계정명 · 머신 이름(`os.hostname()` 원본) · 프로젝트 이름. 이 셋은 대시보드의
목적 자체(누가 얼마나 썼나)라 지울 수 없고, **보존 기한이 없습니다.** 호스트명 규칙에 사번이나
실명이 들어가는 조직이라면 그 사실을 팀에 알리고 쓰십시오.

---

## 기동

### 로컬 (기본, sqlite)

```bash
export USAGE_ADMIN_TOKEN=$(openssl rand -hex 24)
npm start
```

브라우저로 열고 그 토큰을 넣으면 두 탭이 뜹니다. 데이터는 `data/usage.db` 에 쌓입니다.

**토큰이 없으면 부팅을 거부합니다.** 옵션으로 두면 누군가는 반드시 토큰 없이 띄우고, 그때 아무
에러도 나지 않습니다 — 사람별 사용량과 비용이 담긴 화면이라 그 경로를 남기지 않았습니다.

### 원격 PostgreSQL (읽기 전용)

이미 운영 중인 DB 를 들여다보는 용도입니다. SSH 터널을 먼저 뚫습니다:

```bash
ssh -N -L 15432:<db-host>:5432 <user>@<your-host>

USAGE_ADMIN_TOKEN=… \
USAGE_DB_MODE=remote \
DATABASE_URL=postgres://usage_app:<password>@127.0.0.1:15432/usage \
npm start
```

이 모드는 **읽기 전용입니다.** 인테이크(`POST /api/usage`)도, 귀속 교정 쓰기도, 보존 정리기도
아예 등록하지 않습니다. "운영 데이터를 안 건드린다"는 규율이 아니라 배선으로 막았습니다 —
그 경로들은 이 모드에서 막힌 게 아니라 **존재하지 않습니다**(404).

> ⚠ **앱 롤로 붙으세요.** `DATABASE_URL` 이 SUPERUSER 또는 BYPASSRLS 롤이면 RLS 테넌트 격리가
> 통째로 무력화됩니다. 그런데 증상이 없습니다 — 요청은 200 이고 데이터도 잘 보입니다(남의 것까지).
> `CREATE ROLE … NOSUPERUSER NOBYPASSRLS` 로 만든 롤을 쓰세요.
>
> 이건 경고문으로만 남겨 두지 않았습니다. **서버가 부팅에서 롤을 직접 확인하고 위반이면
> 거부합니다**(`lib/db/rlsguard.js` 의 판정을 `server.js` 가 부팅 경로에서 부릅니다). 터널이
> 아직 안 붙어 확인이 불가능한 경우는 거부하지 않되 stderr 로 크게 남깁니다 — 검사가 돌지
> 않았다는 사실 자체가 기록돼야 하기 때문입니다.

### Docker

```bash
cp .env.example .env      # USAGE_ADMIN_TOKEN 을 채운다
docker compose up -d      # http://127.0.0.1:4191
```

포트를 `127.0.0.1` 에 묶어 두었습니다. `"4191:4191"` 로 쓰면 도커가 호스트 방화벽을 우회해
LAN 전체에 열립니다 — 이 화면에는 그 기본값을 쓰지 않습니다.

---

## 설정

전부 환경 변수입니다. 자세한 설명은 [`.env.example`](.env.example) 에 있습니다.

| 변수 | 기본 | 뜻 |
|---|---|---|
| `USAGE_ADMIN_TOKEN` | — | **필수.** 조회 토큰(최소 16자) |
| `USAGE_INTAKE_TOKEN` | — | **권장.** 보고 전용 토큰. `POST /api/usage` 만 열린다(조회 403) |
| `USAGE_PORT` | `4191` | 포트 |
| `USAGE_HOST` | `127.0.0.1` | 바인드 주소 |
| `USAGE_DB_MODE` | `local` | `local`(sqlite) \| `remote`(PostgreSQL, 읽기 전용) |
| `USAGE_DATA_DIR` | `./data` | sqlite 디렉터리 |
| `DATABASE_URL` | — | remote 모드 접속 문자열 |
| `USAGE_TENANT` | `default` | 조회 테넌트(pg RLS) |
| `USAGE_KEYWORD_RETENTION_DAYS` | `90` | 키워드 보존. `off` 면 무기한 |
| `USAGE_CONFIG` | `./config.json` | 단가 오버라이드 파일 |

**포트를 고를 때:** 브라우저가 차단하는 포트(4190·6000·6667 등 WHATWG Fetch "bad ports")를 지정하면
부팅에서 거부합니다. 그 포트에서는 서버가 정상 기동하고 curl 도 200 을 받는데 **브라우저에서만**
아무것도 안 됩니다 — 로그도 테스트도 전부 초록색이라 "대시보드가 깨졌다" 외에는 단서가 남지 않는,
가장 진단하기 어려운 모양입니다.

**단가:** 시드 단가표는 코드에 있지만 **낡습니다.** `config.json` 의 `usage.pricing` 이 이깁니다
([`config.example.json`](config.example.json) 참조). 비용은 저장하지 않고 **읽을 때마다 계산**합니다 —
컬럼으로 굳히면 단가가 바뀌었을 때 과거 수치가 옛 단가에 묶입니다.

---

## API

인증은 `Authorization: Bearer <토큰>` 또는 쿠키 `usage_tok` 입니다.
**쿠키로는 조회만 됩니다** — 상태변경은 헤더 인증만 인정합니다(403). 브라우저는 임의 헤더를 붙일 수
없으므로 화면은 자연히 조회 전용이 되고, CSRF 표면이 아예 생기지 않습니다.

토큰은 두 종류이고 **여는 범위가 다릅니다**:

| 토큰 | 여는 것 | 누가 갖나 |
|---|---|---|
| `USAGE_ADMIN_TOKEN` | 전부 | 대시보드를 여는 사람 |
| `USAGE_INTAKE_TOKEN` | `POST /api/usage` 만 (그 외 403) | 팀원 PC 의 수집기 |

수집기에는 **인테이크 토큰만** 배포하세요. 그 토큰은 팀원 수만큼 복제되어 각자의 디스크에
놓이므로, 열람까지 겸하면 사본 하나가 곧 팀 전체의 노출이 됩니다. 인테이크 토큰은 쿠키로는
인정되지 않습니다 — 그 보고자는 수집기이지 브라우저가 아니고, 쿠키로 받아 주면 브라우저를 꾀어
임의 사용량을 밀어 넣는 자리가 생깁니다.

| 메서드 | 경로 | 하는 일 |
|---|---|---|
| `GET` | `/healthz` | 기동 확인(무인증·무DB) |
| `POST` | `/api/usage` | 수집기 보고 인테이크 |
| `GET` | `/api/usage/summary` | 총계·일별·사용자별·모델별·축별 상위 |
| `GET` | `/api/usage/series` | 시계열(비용·토큰), 그룹핑·기간 지정 |
| `GET` | `/api/usage/distribution` | 세션 단위 분포(p50/p95/p99) |
| `GET` | `/api/usage/sessions` | 상위 세션 드릴다운 |
| `GET` | `/api/usage/quality` | 도구 오류율·거부율·지연 |
| `GET` | `/api/usage/coverage` | 수집 커버리지(머신별 마지막 보고) |
| `GET` | `/api/usage/leaderboard` | 사용자·모델 순위 |
| `GET` | `/api/usage/dispatch` | 사용자별 에이전트·스킬 활용 |
| `GET` `PUT` `DELETE` | `/api/usage/identity` | 머신 → 계정 귀속 교정 |

### 인테이크 페이로드

```jsonc
{
  "user": "alice",              // 세션에 username 이 없을 때의 기본값
  "machine": "host-a",
  "sessions": [{
    "sessionId": "0f3a…",       // 8~120자, [A-Za-z0-9._-]
    "model": "claude-sonnet-5", // 그 세션의 최빈 모델(모델 축이 아니다)
    "input": 100, "output": 250, "cacheRead": 9000, "cacheCreate": 400,
    "turns": 3, "startedAt": "2026-08-07T01:00:00.000Z",
    "noTsTurns": 0,             // 시각이 없어 시간 버킷에 못 올린 턴 수(모르면 생략)
    "counters": { "bash": { "pytest": 4 }, "agent": { "backend-engineer": 2 } },
    "series": [{ "hour": "2026-08-07T01", "model": "claude-sonnet-5",
                 "input": 100, "output": 250, "cacheRead": 9000, "cacheCreate": 400,
                 "cc1h": 400, "turns": 3 }]
  }]
}
```

**멱등입니다.** 수집기는 세션 절대값을 보내고(델타가 아닙니다), 서버는 `(세션, 축, 키)`로
덮어씁니다. 누적(`+=`)을 쓰면 수집기가 두 번 도는 순간 값이 두 배가 되는데, 수집기는 실패해도
재시도하는 best-effort 경로라 **중복 전송이 정상 동작에 포함됩니다.**

`series`(시간×모델 버킷)는 신버전 수집기만 보냅니다. 없어도 거절하지 않습니다 — 구버전 보고를
거절하면 그 사람의 사용량이 통째로 사라집니다. 대신 그 세션의 모델별 값은 근사가 되고, 화면이
그 사실을 밝힙니다.

> 수집기(팀원 PC 에서 도는 클라이언트)는 이 레포에 없습니다. 위 페이로드를 `POST /api/usage` 로
> 보내는 것이 전부이므로, 훅이든 크론이든 원하는 방식으로 붙이면 됩니다.

---

## 라이브러리로 쓰기

다른 Node 서버에 라우트를 얹을 수 있습니다. **공개 API 는 `index.js` 하나입니다** —
`lib/*` 를 직접 require 하지 마세요. 내부를 뒤지기 시작하면 컬럼 이름과 상수가 호스트 코드에 새고,
그러면 스키마를 바꿀 때마다 호스트가 **컴파일 에러 없이 조용히** 깨집니다.

```js
const usage = require('user-usage');

await usage.init();                  // 테이블 보장(멱등). sqlite 는 DDL, pg 는 migrations 소유
const stop = usage.startRetention(); // 보존이 꺼져 있으면 null

// 라우트 체인에 끼운다. **순서가 계약이다** — analytics 가 admin 보다 앞이어야 한다
// (admin 이 /api/usage 접두사를 통째로 소유하고 안 걸리면 404 를 직접 낸다).
const routes = [usage.routes.intake, usage.routes.analytics, usage.routes.admin];
for (const route of routes) if (await route(req, res, ctx)) return;
```

`ctx` 가 요구하는 필드는 `server.js` 의 `makeCtx()` 가 단일 출처입니다.

---

## 데이터

| 테이블 | 담는 것 |
|---|---|
| `usage_sessions` | 세션당 1행 — 토큰 4축·턴·시각 |
| `usage_series` | (세션, 시간, 모델)당 1행 — 모델별 정확값의 근거 |
| `usage_counters` | (세션, 축, 키)당 1행 — 도구·명령·스킬·에이전트·키워드 |
| `usage_recommendations` | 추천 호출 관측 — 카탈로그 공백 탐지용 |
| `machine_identity` | 머신 → 계정 귀속 교정표 |
| `usage_audit` | 귀속 교정 이력(누가·언제·무엇을) |

### 무엇이 언제까지 남는가

지우는 것은 `keyword` 축 하나뿐입니다. 나머지는 **무기한 보관하기로 결정한 것**이지 빠뜨린 것이
아니므로, 그 결정을 여기 적어 둡니다.

| 대상 | 보존 | 왜 |
|---|---|---|
| `usage_counters` 의 `keyword` | **90일** | 유일하게 사람이 입력한 말에서 나온다 |
| `usage_series` | 무기한 | `pruneSeries` 가 있으나 **일부러 호출하지 않습니다.** 모델별 값의 소급 교정이 이 표가 온전하다는 데 기댑니다(`lib/store.js` 의 주석이 단일 출처) |
| `usage_sessions` · `usage_counters`(그 외 축) | 무기한 | 계정명·머신명·프로젝트명이 여기 있고, 그게 이 도구의 목적 자체(누가 얼마나 썼나)라 지우면 도구가 없어집니다 |
| `usage_audit` | 무기한 | "어제 보던 이름이 왜 오늘 다른가"에 답하는 표입니다. 기한을 두면 그 답이 먼저 사라집니다 |

⚠ 그래서 **계정명 · 머신 이름(`os.hostname()` 원본) · 프로젝트 이름은 지워지지 않습니다.**
호스트명 규칙에 사번이나 실명이 들어가는 조직이라면 그 사실을 팀에 알리고 쓰십시오. 지워야 할
사정이 생기면 보존 정리기를 늘리는 것이 아니라 (a) 귀속 교정으로 이름을 바꾸거나 (b) 해당 행을
직접 지우는 쪽이 맞습니다 — 이 축들은 "오래되면 값어치가 준다"가 아니라 "있거나 없거나"입니다.

sqlite 는 로드 시점에 DDL 을 직접 겁니다(멱등). PostgreSQL 은 `migrations/pg/*.sql` 이 스키마를
소유하며, 러너는 `lib/db/migrate.js` 입니다 — **자동 실행 경로에 올리지 않았습니다.** 되돌리기
어려운 작업은 사람이 명시적으로 돌립니다.

> 마이그레이션 번호에 공백이 있습니다(0014·0015·0017·0026). 의도된 것입니다 — 기존 DB 의
> `schema_migrations` 와 대조할 수 있어야 하고, 러너는 없는 번호를 신경 쓰지 않습니다.

---

## 개발

```bash
npm test              # 208 tests — 순수 로직·집계·라우트·뷰 렌더·RLS 판정·키워드 필터
npm run test:standalone   # 34 tests — 자식 프로세스를 실제로 띄워 게이트·모드·정적 서빙 검증
npm run test:all
node test/require-all.mjs  # 모든 서버측 .js 를 실제 require — 끊어진 경로 탐지
```

**RLS 위반 롤 대조는 진짜 PostgreSQL 이 있을 때만 돕니다.** 기본 스위트는 닿지 않는 URL 을 써서
"검사가 돌았다"까지만 증명하고, "위반을 잡는다"는 증명하지 않습니다 — 둘을 묶으면 가드가 아무것도
안 잡는 상태로 퇴화해도 초록색입니다. 클러스터가 있으면 두 URL 을 넘겨 관문을 켜십시오:

```bash
USAGE_TEST_PG_SUPER_URL=postgres://<슈퍼유저>@127.0.0.1:5432/usage \
USAGE_TEST_PG_APP_URL=postgres://usage_app:<pw>@127.0.0.1:5432/usage \
npm run test:standalone
```

앱 롤은 `CREATE ROLE usage_app LOGIN NOSUPERUSER NOBYPASSRLS` 로 만듭니다. 스키마는 필요 없습니다 —
remote 부팅은 원격 DB 에 아무것도 쓰지 않고 프로브는 `pg_roles` 만 읽습니다.

테스트는 `npm install` 없이 돕니다(sqlite 는 node 내장, `pg` 는 remote 모드에서만 로드).
프로세스마다 임시 데이터 디렉터리가 물립니다(`test/sqlite-setup.mjs`) — 안 그러면 여러 테스트
프로세스가 같은 sqlite 파일을 열어 `database is locked` 가 **매번 다른 파일에서** 터집니다.

```
server.js          HTTP 진입점 — 인증 게이트·정적 서빙·모드 분기
index.js           라이브러리 공개 API(호스트가 통과하는 유일한 문)
lib/
  store.js         사용량 저장·집계(이 레포에서 가장 큰 파일)
  intake.js        클라이언트 보고 정규화 — 신뢰 경계
  identity.js      머신 → 계정 귀속(+ 과거 행 소급 재스탬프)
  cost.js          토큰 → 비용 환산(읽을 때마다 계산)
  stats.js  tz.js  분포 · 집계 시간대
  retention.js     키워드 보존 정리기
  audit.js         관리 동작 감사 로그
  tenant.js        AsyncLocalStorage 테넌트 컨텍스트
  db/              sqlite | pg 어댑터(동일한 q/tx/dialect 계약)
routes/            usage.js(관리·인테이크) · usage-analytics.js(관측)
public/            셸 · 뷰 2개 · core.js(빌드 없음)
migrations/pg/     PostgreSQL 스키마
```
