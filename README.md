# user-usage — 코딩 에이전트 사용량 관측 대시보드

팀원 PC 에서 올라온 **코딩 에이전트 세션 텔레메트리**를 모아 *누가 · 무엇을 · 얼마나 썼고
얼마어치인가*를 보여주는 조회 도구입니다. 수집 대상은 네 플랫폼입니다 —
**Claude Code · Codex · Gemini CLI · Antigravity CLI**.

**백엔드는 Go(`go/`), 프런트는 Next.js(`web/`)** 이고, `scripts/build.sh` 가 프런트 산출물을
Go 바이너리에 `go:embed` 로 넣어 **배포는 단일 실행 파일 하나**가 됩니다.

```
bash scripts/build.sh                                   # web 빌드 → webroot 임베드 → go build
USAGE_ADMIN_TOKEN=$(openssl rand -hex 24) \
  USAGE_DATA_DIR=./data ./go/usage-server              # local(sqlite) 기동
# → http://127.0.0.1:4191   ·   컨테이너 기동은 docker-compose.yml / Dockerfile
```

> **도구의 성격과 데이터 정책(무엇을 저장하지 않는가 · 무엇이 언제까지 남는가)은 이 문서가
> 단일 출처입니다.**
>
> 운영 절차(서버 기동 env · 최초 관리자 · 키 발급/해지 · 배포 · 장애 대응 · 되돌리기)는
> [`docs/OPERATIONS.md`](docs/OPERATIONS.md) 에 있습니다.

---

## 이 도구가 답하는 질문

| 질문 | 어디서 |
|---|---|
| 팀이 이번 달 얼마나 썼나 | **사용 추적** — 총계·일별 추이·사용자별·모델별 |
| 그게 API 종량제였다면 얼마인가 | **사용 관측** — 축별 비용 분해(캐시읽기가 보통 최대 축입니다) |
| 어떤 도구·명령·에이전트를 실제로 쓰나 | **사용 추적** — 7개 축(tool·bash·slash·skill·agent·mcp·keyword) |
| 어느 플랫폼을 얼마나 쓰나 | **플랫폼** — 4개 플랫폼별 롤업(수집 범위 차이를 함께 표시) |
| 어느 PC 가 보고를 멈췄나 | **사용 관측** — 수집 커버리지 |
| 개발자 머신을 어떻게 붙이나 | **연동** — 인제스트 키 발급 · 원커맨드 복사 · 키 관리 |
| 이 숫자를 믿어도 되나 | 모델별 표의 **근거** 열 + 사용자별 커버리지 |

마지막 줄이 이 도구의 성격을 정합니다. 모델별 값은 두 근거를 더한 것이고 — 시간×모델 버킷이 있는
세션은 정확값, 없는 세션은 그 세션의 최빈 모델에 통째로 귀속된 근사값입니다 — 화면은 그 비율을
**밝힙니다.** 근사를 정확한 값으로 위장하지 않는 것이 여기서는 기능입니다.

---

## 지원 플랫폼

수집 방식이 **두 가지**입니다. 셋은 CLI 가 디스크에 남기는 세션 파일을 읽고, 하나는
파일에 토큰이 남지 않아 **런타임에 붙잡아야** 합니다.

| 플랫폼 | 방식 | 원천 | 수집기 플래그 |
|---|---|---|---|
| **Claude Code** | 파일 | `~/.claude/projects/**/*.jsonl` | `-dir` |
| **Codex** | 파일 | `~/.codex/sessions/**/*.jsonl` | `-codex-dir` |
| **Gemini CLI**(오픈소스 `google-gemini/gemini-cli`) | 파일 | `~/.gemini/tmp/<슬러그>/chats/*.jsonl` | `-gemini-dir` |
| **Antigravity CLI**(`agy`) | 런타임 캡처 | statusLine → 스풀(`~/.config/claude-usage/antigravity`) | `-antigravity-dir` |

각 플래그에 `""` 를 주면 그 원천만 끕니다. 디렉터리가 없으면 **조용히 건너뜁니다** — Claude 만
쓰는 팀원, Codex 만 쓰는 팀원 모두 아무 설정 없이 그대로 돌아야 하기 때문입니다.

### Antigravity 가 다른 이유

Antigravity 는 **디스크에 토큰을 남기지 않습니다**(훅 protobuf 스키마 · 대화 DB · transcript 를
전수 확인한 결과입니다). 토큰이 보이는 자리는 statusLine 하나뿐이라 연동이 둘로 나뉩니다:

- **statusLine** 이 렌더될 때마다 stdin 으로 받은 사용량을 스풀에 적습니다(캡처).
- **Stop 훅**이 그 스풀을 서버로 밉니다(플러시).

그래서 `~/.gemini/antigravity-cli/settings.json` 의 statusLine 자리와
`~/.gemini/config/hooks.json` 의 Stop 훅 **둘 다** 등록됩니다. statusLine 자리는 하나뿐이라
이미 쓰던 상태줄이 있으면 **체이닝**합니다 — 원래 명령을 `AGY_PREV_STATUSLINE` 으로 보관해
두고 수집기가 같은 입력을 그 명령에 먹여 출력을 그대로 통과시킵니다(화면은 그들 것입니다).

### ⚠ 플랫폼마다 수집 범위가 다릅니다

같은 화면에 서지만 **잴 수 있는 것이 다릅니다.** 이것을 0 으로 그리면 화면이 "안 썼다"고
말하게 되므로, 화면은 `미수집`(그 도구가 기록하지 않는다)과 `해당 없음`(개념 자체가 없다)을
갈라 표시합니다.

아래 표는 **수집기가 실제로 보내는 축**입니다(근거: `collector/internal/{transcript,codex,gemini,antigravity}`):

| 축 | Claude | Codex | Gemini CLI | Antigravity |
|---|---|---|---|---|
| 토큰·모델·세션·비용 | ✅ | ✅ | ✅ | ✅ |
| `tool` · `bash` · `mcp` | ✅ | ✅ | ✅ | **미수집** |
| `skill` · `agent` | ✅ | ✅ | ✅ | **미수집** |
| `keyword` | ✅ | ✅ | ✅ | ✅ ※ |
| `slash` | ✅ | **미수집** | **미수집** | ✅ ※ |
| LOC · 편집 수락/거부 | ✅ | ✅ | ✅ | **미수집** |
| 캐시 생성 | ✅ | **해당 없음** | **해당 없음** | **해당 없음** |

- **Codex·Gemini 의 `slash`** — 두 CLI 의 세션 파일에 슬래시 명령 기록이 남지 않습니다.
  수집기가 보낼 값이 없어서 **축 자체를 만들지 않습니다**(0 을 보내지 않습니다).
- **Antigravity 의 행동 축** — '준비 중'이 아니라 **미수집**입니다. 수집기를 더 만들면 언젠가
  온다는 뜻이 아니라, 그 도구가 기록하지 않아 **올 수 없다**는 뜻입니다.
- **※ Antigravity 의 `slash`·`keyword`** — statusLine 스풀이 아니라
  `~/.gemini/antigravity-cli/history.jsonl` 에서 나옵니다. **그 파일이 없으면 두 축만 조용히
  빠지고**, 토큰·모델·세션 사용량 자체는 스풀만으로 온전합니다.
- **캐시 생성이 '해당 없음'인 이유** — OpenAI 는 캐시 **쓰기**에 과금하지 않고, Google 의
  암시적 캐싱에는 캐시 쓰기 과금이라는 개념이 없습니다. 응답에 0 이 와도 그건 관측이 아닙니다.

---

## 개발자 머신 연동 (원커맨드)

관리자가 대시보드 **"연동"** 탭에서 인제스트 키를 발급하고 원라인을 복사해 개발자에게 전달합니다.
개발자는 그 한 줄만 실행합니다:

```sh
curl -fsSL $SERVER/install.sh | sh -s -- --key <인제스트키> --server $SERVER
```

한 줄로 끝나는 것:

1. OS/arch 감지 → 수집기 바이너리 다운로드(`GET /api/agent/collector`, 인제스트 키 필수)
2. 설정 저장 — `~/.config/claude-usage/config.env` (**perms 600**)
3. Claude Code **SessionEnd 훅** 등록(`~/.claude/settings.json`)
4. Antigravity CLI 가 **있으면** statusLine(캡처) + Stop 훅(플러시) 등록 — 없으면 **조용히 스킵**
5. 초기 백필 1회 실행 + 결과 보고

Codex·Gemini 는 별도 등록이 없습니다. 수집기가 돌 때 기본 경로를 함께 훑으므로, 훅 하나가
파일 기반 원천 셋을 모두 올립니다.

### 이 설치기가 지키는 것

- **비파괴·멱등.** 기존 훅·상태줄을 보존합니다. 재실행해도 훅이 중복되지 않습니다(우리 것만
  제거 후 재삽입). 병합은 반드시 JSON 도구(jq → python3 → node)로만 하고, 셋 다 없는데 파일이
  이미 있으면 **덮지 않고 멈춥니다.** 손상 JSON 이면 한 바이트도 건드리지 않습니다.
- **덮어쓰기 전 `.bak` 백업.** `settings.json.bak` · `hooks.json.bak` 로 남습니다(되돌리기는
  [`docs/OPERATIONS.md`](docs/OPERATIONS.md)).
- **토큰은 `config.env`(600)에만.** `settings.json` · `hooks.json` 에는 평문이 들어가지
  않습니다 — 그 파일들은 공유·백업·스크린샷으로 새어나가기 쉽습니다. 훅은 `config.env` 를
  sourcing 해 값을 얻고, 토큰은 argv 가 아니라 `USAGE_INTAKE_TOKEN` 환경변수로 넘깁니다
  (argv 로 넘기면 `ps aux` 에 노출됩니다).
- **https 강제.** 이 스크립트는 서버에서 받은 바이너리를 `chmod +x` 후 실행하므로, 평문 http 는
  중간자가 임의 코드를 심을 수 있는 자리입니다. 예외는 loopback(`127.0.0.1`·`localhost`) 뿐입니다.

### 인제스트 키의 스코프

| | 인제스트 키(`uu_ing_…`) | 관리자 |
|---|---|---|
| `POST /api/usage`(보고) | ✅ | ✅ |
| `GET /api/agent/collector`(수집기 내려받기) | ✅ | ✅(헤더로만) |
| `GET /api/usage/*`(열람) | **403** | ✅ |
| 해지 후 | **401** | — |

키는 **보고 전용**입니다. 팀원 수만큼 복제되어 각자의 디스크에 놓이므로, 열람까지 겸하면
사본 하나가 곧 팀 전체의 노출이 됩니다. 저장은 `sha256` 해시만 하고 **평문은 발급 시 1회만**
보입니다 — 다시 볼 수 없으니 잃어버리면 재발급합니다.

---

## 기동

### 로컬 (기본, sqlite)

```bash
bash scripts/build.sh                         # web 빌드 → webroot 임베드 → go build (유일 빌드 경로)
USAGE_ADMIN_TOKEN=$(openssl rand -hex 24) \
  USAGE_DATA_DIR=./data ./go/usage-server
```

브라우저로 열고 그 토큰을 넣으면 탭이 뜹니다. 데이터는 `./data/usage.db` 에 쌓입니다.

**토큰이 없으면 부팅을 거부합니다.** 옵션으로 두면 누군가는 반드시 토큰 없이 띄우고, 그때 아무
에러도 나지 않습니다 — 사람별 사용량과 비용이 담긴 화면이라 그 경로를 남기지 않았습니다.

### 원격 PostgreSQL (읽기 전용)

이미 운영 중인 DB 를 들여다보는 용도입니다. SSH 터널을 먼저 뚫습니다:

```bash
ssh -N -L 15432:<db-host>:5432 <user>@<your-host>

USAGE_ADMIN_TOKEN=… \
USAGE_DB_MODE=remote \
DATABASE_URL=postgres://usage_app:<password>@127.0.0.1:15432/usage \
./go/usage-server
```

이 모드는 **읽기 전용입니다.** 인테이크(`POST /api/usage`)도, 귀속 교정 쓰기도, 보존 정리기도
아예 등록하지 않습니다. "운영 데이터를 안 건드린다"는 규율이 아니라 배선으로 막았습니다 —
그 경로들은 이 모드에서 막힌 게 아니라 **존재하지 않습니다**(404).

> ⚠ SaaS(멀티테넌트)는 pg 에 **써야** 하므로 `USAGE_MULTITENANT=1` 이 읽기 전용을 풉니다
> (`ReadOnly = remote && !MultiTenant`). 격리는 그때도 RLS 가 집니다.

> ⚠ **앱 롤로 붙으세요.** `DATABASE_URL` 이 SUPERUSER 또는 BYPASSRLS 롤이면 RLS 테넌트 격리가
> 통째로 무력화됩니다. 그런데 증상이 없습니다 — 요청은 200 이고 데이터도 잘 보입니다(남의 것까지).
> `CREATE ROLE … NOSUPERUSER NOBYPASSRLS` 로 만든 롤을 쓰세요.
>
> 이건 경고문으로만 남겨 두지 않았습니다. **서버가 부팅에서 롤을 직접 확인하고 위반이면
> 거부합니다**(`go/internal/db/rlsguard.go` 의 판정을 `go/cmd/usage-server/main.go` 가 부팅
> 경로에서 부릅니다). 터널이 아직 안 붙어 확인이 불가능한 경우는 거부하지 않되 stderr 로 크게
> 남깁니다 — 검사가 돌지 않았다는 사실 자체가 기록돼야 하기 때문입니다.

### 멀티테넌트 (SaaS)

`USAGE_MULTITENANT=1` 은 **PostgreSQL 에서만** 쓸 수 있습니다. sqlite 로 켜면 부팅을
**거부합니다** — sqlite 에는 RLS 가 없어 여러 org 의 사용량·비용이 한 파일에 섞이는데,
요청은 200 이라 아무도 눈치채지 못합니다. 경고로 끝낼 문제가 아닙니다.

### Docker

```bash
cp .env.example .env      # USAGE_ADMIN_TOKEN 을 채운다
docker compose up -d      # http://127.0.0.1:4191
```

포트를 `127.0.0.1` 에 묶어 두었습니다. `"4191:4191"` 로 쓰면 도커가 호스트 방화벽을 우회해
LAN 전체에 열립니다 — 이 화면에는 그 기본값을 쓰지 않습니다.

AWS(ECS Fargate + ALB + RDS) Terraform 구성은 [`deploy/README.md`](deploy/README.md) 에 있습니다.

---

## 설정

전부 환경 변수입니다. 자세한 설명은 [`.env.example`](.env.example) 에 있습니다.

| 변수 | 기본 | 뜻 |
|---|---|---|
| `USAGE_ADMIN_TOKEN` | — | **필수.** 조회 토큰(최소 16자) |
| `USAGE_INTAKE_TOKEN` | — | **권장.** 보고 전용 토큰. `POST /api/usage` 만 열린다(조회 403) |
| `USAGE_PORT` | `4191` | 포트 |
| `USAGE_HOST` | `127.0.0.1` | 바인드 주소 |
| `USAGE_DB_MODE` | `local` | `local`(sqlite) \| `remote`(PostgreSQL) |
| `USAGE_DATA_DIR` | `./data` | sqlite 디렉터리 |
| `DATABASE_URL` | — | remote 모드 접속 문자열 |
| `USAGE_MULTITENANT` | off | SaaS 모드. 인테이크를 org 인제스트 키로 인증. **pg 전용**(sqlite 면 부팅 거부) |
| `USAGE_TENANT` | `default` | 조회 테넌트(pg RLS) |
| `USAGE_INTAKE_RATE` | `20` | 테넌트별 인테이크 rate limit(초당 리필). 음수면 무제한 |
| `USAGE_INTAKE_BURST` | `40` | 위 토큰버킷의 상한 |
| `USAGE_SESSION_TTL` | `12h` | 사람 로그인 세션 쿠키 수명(Go duration 문법) |
| `USAGE_TRUSTED_PROXY_COUNT` | `0` | 앞단 신뢰 프록시 홉 수. `0` 이면 XFF 무시. ALB 단독이면 `1` |
| `USAGE_BOOTSTRAP_ADMIN_USER` | — | 최초 관리자 아이디(그 tenant 에 사용자가 없을 때만 생성) |
| `USAGE_BOOTSTRAP_ADMIN_PASSWORD` | — | 최초 관리자 비밀번호(로그에 절대 찍지 않는다) |
| `USAGE_BOOTSTRAP_TENANT` | `default` | 부트스트랩 대상 테넌트 |
| `USAGE_KEYWORD_RETENTION_DAYS` | `90` | 키워드 보존. `off` 면 무기한 |
| `USAGE_CONFIG` | `./config.json` | 단가 오버라이드 파일 |

**포트를 고를 때:** 브라우저가 차단하는 포트(4190·6000·6667 등 WHATWG Fetch "bad ports")를 지정하면
부팅에서 거부합니다. 그 포트에서는 서버가 정상 기동하고 curl 도 200 을 받는데 **브라우저에서만**
아무것도 안 됩니다 — 로그도 테스트도 전부 초록색이라 "대시보드가 깨졌다" 외에는 단서가 남지 않는,
가장 진단하기 어려운 모양입니다.

---

## 비용 — "API 환산 비용"

화면의 비용 라벨은 어디서나 **"API 환산 비용"** 이고, 그것이 **청구액이 아니라는 사실을 함께
말합니다.** 구독 요금제(ChatGPT Plus · Claude · Antigravity)는 토큰당 과금이 없어서, 여기 나오는
숫자는 "같은 토큰을 API 종량제로 썼다면 얼마였을까"입니다. 라벨의 단일 출처는
[`web/lib/costLabels.ts`](web/lib/costLabels.ts) 입니다.

쓸모는 그대로입니다 — 사용자·모델·플랫폼 간 **비교**에는 그대로 쓰이고, 그게 이 화면이
답하려는 질문입니다. 다만 이 값을 경리에 청구서로 넘기면 안 됩니다.

### 단가

- 시드 단가표는 **Anthropic · OpenAI · Google** 셋을 담습니다. 검증일은 공급사별로 다르고
  (`SeedPricedAtFor`), 화면이 그 날짜를 표시합니다.
- **캐시 배수는 모델별입니다.** 전역 상수 하나로 두면 Anthropic 기준이 OpenAI·Google 에
  잘못 적용됩니다. 예를 들어 OpenAI 의 캐시 히트 할인율은 계열마다 0.10 · 0.25 · 0.50 · 1.00
  으로 갈리고, `*-pro` 계열은 **캐시 할인이 아예 없습니다**(1.00).
- **공식 단가가 없는 모델은 `unpriced` 로 정직하게 드러냅니다.** 현재 `gemini-3.1-pro` 와
  `gpt-oss-120b` 가 그렇습니다 — 추측 매핑으로 그럴듯한 숫자를 만들지 않습니다. 응답의
  `unpriced` 배열이 그 목록이고, 화면이 그 사실을 표시합니다.
- 시드는 **낡습니다.** `config.json` 의 `usage.pricing` 이 이깁니다
  ([`config.example.json`](config.example.json) 참조). 비용은 저장하지 않고 **읽을 때마다 계산**합니다 —
  컬럼으로 굳히면 단가가 바뀌었을 때 과거 수치가 옛 단가에 묶입니다.

---

## 데이터 정책 — 무엇을 저장하지 않는가

수집되는 것은 **집계뿐**입니다. 프롬프트 원문 · 파일 경로 · 명령 인자는 저장하지 않습니다.
`bash` 축은 선두 실행파일만 셉니다(`git`, `npm`, `pytest` — 인자 없음). `keyword` 축만 사람이 입력한
말에서 나오므로 **90일 후 자동 삭제**됩니다(`USAGE_KEYWORD_RETENTION_DAYS`). 화면이 그 정책을
스스로 표시합니다 — 추세가 끊기는 이유를 보는 사람이 알아야 하고, 보관 기간은 팀에게 공개돼야
하는 약속이기 때문입니다.

`keyword` 축은 어휘가 열려 있는 유일한 축이라 서버가 한 번 더 거릅니다. API 키·토큰 모양
(벤더 접두사 · 32자 이상 hex · 대소문자+숫자가 섞인 24자 이상 문자열), 이메일과 접속 문자열
조각, 10자리 이상 연속 숫자, `키=값` 형태는 **저장 전에 버립니다**(`go/internal/intake` 의
`safeKeyword`). 수집기가 클라이언트에서 먼저 거른다는 전제이지만 신뢰하지 않습니다 — 수집기는
팀원 PC 에서 도는 별도 프로세스라 서버보다 낡을 수 있고, 무엇보다 한 번 저장되면 지우는 비용이
훨씬 큽니다. 판정은 언제나 **버리는 쪽으로** 기울입니다.

여전히 남는 것: 계정명 · 머신 이름(`os.Hostname()` 원본) · 프로젝트 이름. 이 셋은 대시보드의
목적 자체(누가 얼마나 썼나)라 지울 수 없고, **보존 기한이 없습니다.** 호스트명 규칙에 사번이나
실명이 들어가는 조직이라면 그 사실을 팀에 알리고 쓰십시오.

---

## API

인증은 `Authorization: Bearer <토큰>` 또는 쿠키 `usage_tok` 입니다.
**쿠키로는 조회만 됩니다** — 상태변경은 헤더 인증만 인정합니다(403). 브라우저는 임의 헤더를 붙일 수
없으므로 화면은 자연히 조회 전용이 되고, CSRF 표면이 아예 생기지 않습니다.

자격은 셋이고 **여는 범위가 다릅니다**:

| 자격 | 여는 것 | 누가 갖나 |
|---|---|---|
| `USAGE_ADMIN_TOKEN` | 전부 | 대시보드를 여는 사람 |
| `USAGE_INTAKE_TOKEN` | `POST /api/usage` 만 (그 외 403) | 팀원 PC 의 수집기 |
| org 인제스트 키(`uu_ing_…`) | `POST /api/usage` + 수집기 내려받기 | 원커맨드로 연동한 개발자 머신 |

인테이크 자격은 쿠키로는 인정되지 않습니다 — 그 보고자는 수집기이지 브라우저가 아니고,
쿠키로 받아 주면 브라우저를 꾀어 임의 사용량을 밀어 넣는 자리가 생깁니다.

| 메서드 | 경로 | 하는 일 | 자격 |
|---|---|---|---|
| `GET` | `/healthz` | 기동 확인(무인증·무DB) | — |
| `GET` | `/install.sh` | 원커맨드 부트스트랩 스크립트 | 무인증(키가 인증을 대신) |
| `GET` | `/api/agent/collector?os=&arch=` | 수집기 바이너리 | 인제스트 키 |
| `POST` | `/api/usage` | 수집기 보고 인테이크 | 인테이크 |
| `GET` | `/api/usage/summary` | 총계·일별·사용자별·모델별·축별 상위 | 열람 |
| `GET` | `/api/usage/series` | 시계열(비용·토큰), 그룹핑·기간 지정 | 열람 |
| `GET` | `/api/usage/distribution` | 세션 단위 분포(p50/p95/p99) | 열람 |
| `GET` | `/api/usage/sessions` | 상위 세션 드릴다운 | 열람 |
| `GET` | `/api/usage/quality` | 도구 오류율·거부율·지연 | 열람 |
| `GET` | `/api/usage/coverage` | 수집 커버리지(머신별 마지막 보고) | 열람 |
| `GET` | `/api/usage/leaderboard` | 사용자·모델 순위 | 열람 |
| `GET` | `/api/usage/dispatch` | 사용자별 에이전트·스킬 활용 | 열람 |
| `GET` | `/api/usage/platforms` | 플랫폼별 롤업(+모델 분해) | 열람 |
| `GET` | `/api/usage/seats` | 좌석별 사용·비용 | 열람 |
| `GET` | `/api/usage/teams` | 팀별 집계 | 열람 |
| `GET` | `/api/usage/dev` | 개발 지표(LOC·편집 수락/거부) | 열람 |
| `GET` `PUT` `DELETE` | `/api/usage/identity` | 머신 → 계정 귀속 교정 | 관리자 |
| `POST` `GET` | `/api/admin/keys` | 인제스트 키 발급(평문 1회) · 목록(마스크만) | 관리자 |
| `POST` | `/api/admin/keys/revoke` | 키 해지 | 관리자 |
| `POST` | `/api/auth/login` `/api/auth/logout` | 사람 로그인(ID/PW) 세션 | — |
| `GET` | `/api/auth/me` | 현재 세션 확인 | 세션 |

`/api/usage/*` 의 조회 엔드포인트는 `?platform=claude|codex|gemini|antigravity|other` 필터를
받습니다. **미지정이면 전체**이고, 목록 밖 값은 400 입니다 — 오타를 `other` 로 접으면 요청한 것과
다른 모집단이 요청한 이름으로 조용히 돌아옵니다.

### 인테이크 페이로드

```jsonc
{
  "user": "alice",              // 세션에 username 이 없을 때의 기본값
  "machine": "host-a",
  "sessions": [{                // 한 보고당 최대 50
    "id": "0f3a…",              // 8~120자, [A-Za-z0-9._-]
    "platform": "claude",       // claude|codex|gemini|antigravity (없으면 claude)
    "model": "claude-sonnet-5", // 그 세션의 최빈 모델(모델 축이 아니다)
    "input": 100, "output": 250, "cacheRead": 9000, "cacheCreate": 400,
    "turns": 3, "startedAt": "2026-08-07T01:00:00.000Z",
    "noTsTurns": 0,             // 시각이 없어 시간 버킷에 못 올린 턴 수(모르면 생략)
    "linesAdded": 12, "linesRemoved": 3,          // 개발 지표(줄 수만 — 코드 내용은 없다)
    "editsAccepted": 2, "editsRejected": 0,
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

`platform` 도 같은 규율입니다. 없으면 `claude` 로 보고(구버전 수집기 호환), 허용목록 밖 이름은
거부하지도 `claude` 로 접지도 않고 `other` 로 남깁니다 — 다른 도구의 사용량이 claude 로 계산되는
**조용한 오분류**보다 "모르는 플랫폼이 있다"가 화면에 보이는 편이 낫습니다.

---

## 데이터

| 테이블 | 담는 것 |
|---|---|
| `usage_sessions` | 세션당 1행 — 토큰 4축·턴·시각·플랫폼 |
| `usage_series` | (세션, 시간, 모델)당 1행 — 모델별 정확값의 근거 |
| `usage_counters` | (세션, 축, 키)당 1행 — 도구·명령·스킬·에이전트·키워드 |
| `usage_recommendations` | 추천 호출 관측 — 카탈로그 공백 탐지용 |
| `machine_identity` | 머신 → 계정 귀속 교정표 |
| `usage_audit` | 귀속 교정 이력(누가·언제·무엇을) |
| `orgs` · `ingest_keys` | org ↔ tenant 매핑과 인제스트 키(해시만) |
| `auth_users` · `auth_sessions` | 사람 로그인(ID/PW) 계정과 세션 |

### 무엇이 언제까지 남는가

지우는 것은 `keyword` 축 하나뿐입니다. 나머지는 **무기한 보관하기로 결정한 것**이지 빠뜨린 것이
아니므로, 그 결정을 여기 적어 둡니다.

| 대상 | 보존 | 왜 |
|---|---|---|
| `usage_counters` 의 `keyword` | **90일** | 유일하게 사람이 입력한 말에서 나온다 |
| `usage_series` | 무기한 | 시리즈 프루닝을 **일부러 하지 않습니다.** 모델별 값의 소급 교정이 이 표가 온전하다는 데 기댑니다(`go/internal/store` 의 주석이 단일 출처) |
| `usage_sessions` · `usage_counters`(그 외 축) | 무기한 | 계정명·머신명·프로젝트명이 여기 있고, 그게 이 도구의 목적 자체(누가 얼마나 썼나)라 지우면 도구가 없어집니다 |
| `usage_audit` | 무기한 | "어제 보던 이름이 왜 오늘 다른가"에 답하는 표입니다. 기한을 두면 그 답이 먼저 사라집니다 |

⚠ 그래서 **계정명 · 머신 이름 · 프로젝트 이름은 지워지지 않습니다.** 지워야 할 사정이 생기면
보존 정리기를 늘리는 것이 아니라 (a) 귀속 교정으로 이름을 바꾸거나 (b) 해당 행을 직접 지우는
쪽이 맞습니다 — 이 축들은 "오래되면 값어치가 준다"가 아니라 "있거나 없거나"입니다.

sqlite 는 로드 시점에 DDL 을 직접 겁니다(멱등). PostgreSQL 은 `migrations/pg/*.sql` 이 스키마를
소유하며, 러너는 `go/internal/db/migrate.go` 입니다 — **자동 실행 경로에 올리지 않았습니다.** 되돌리기
어려운 작업은 사람이 명시적으로 돌립니다.

> 마이그레이션 번호에 공백이 있습니다(0014·0015·0017·0026). 의도된 것입니다 — 기존 DB 의
> `schema_migrations` 와 대조할 수 있어야 하고, 러너는 없는 번호를 신경 쓰지 않습니다.

---

## 개발

```bash
cd go && go test ./... && go vet ./...          # 백엔드 — 12 패키지
cd collector && go test ./... && go vet ./...   # 수집기 — 4 플랫폼 파서
cd web && npm test                              # 프런트 — vitest
bash scripts/build.sh                           # 유일 빌드 경로(web → webroot 임베드 → go build)
```

**계약 회귀(골든 44개)** — Go 서버를 **빈 DB** 로 띄우고 대조합니다(골든은 동결됨):

```bash
bash scripts/build.sh
USAGE_ADMIN_TOKEN=… USAGE_INTAKE_TOKEN=… USAGE_DATA_DIR=$(mktemp -d) \
  USAGE_PORT=8080 USAGE_KEYWORD_RETENTION_DAYS=off ./go/usage-server &
npm run contract:verify -- --base http://127.0.0.1:8080
```

**수집기 E2E** — 합성 트랜스크립트로 수집→조회 왕복을 실증합니다. 바이너리 경로가 주어질 때만
돕니다(없으면 skip):

```bash
bash scripts/build.sh
go -C collector build -o /tmp/usage-collector ./cmd/usage-collector
USAGE_E2E_SERVER_BIN="$PWD/go/usage-server" USAGE_E2E_COLLECTOR_BIN=/tmp/usage-collector \
  go -C collector test -run E2E -v
```

**RLS 위반 롤 대조는 진짜 PostgreSQL 이 있을 때만 돕니다.** pg 통합 테스트는 URL 을 주면
켜지고(없으면 skip), 앱 롤로 크로스테넌트 격리·`?→$n`·커넥션 풀을 실측합니다:

```bash
USAGE_TEST_PG_URL=postgres://usage_app:<pw>@127.0.0.1:5432/<db> \
  go test ./go/internal/db/ -run PG -v
```

앱 롤은 `CREATE ROLE usage_app LOGIN NOSUPERUSER NOBYPASSRLS` 로 만듭니다. remote 부팅은 원격 DB 에
아무것도 쓰지 않고 프로브는 `pg_roles` 만 읽습니다.

### 구조

```
go/
  cmd/usage-server/   실행 진입점 — 부팅 게이트·시그널·보존 정리기·프로비저닝 CLI
  internal/
    httpapi/          HTTP — 라우터·인증 게이트·정적 서빙·인테이크/관측/온보딩 라우트
    store/            사용량 저장·집계(가장 큰 패키지)
    intake/           클라이언트 보고 정규화 — 신뢰 경계(순수)
    org/              org·인제스트 키 — 발급·해석·해지(해시 저장)
    identity/         머신 → 계정 귀속(+ 소급 재스탬프)·감사
    cost/  stats/     토큰 → 비용(3사 시드·모델별 캐시 배수) · 분포(p95/p99)
    tz/  tenant/      집계 시간대(고정 KST) · 테넌트 컨텍스트
    db/               sqlite | pg 어댑터 · 마이그레이션 러너 · RLS 가드
    config/           부팅 설정 · 거부 게이트
web/                  Next.js 프런트 — 빌드 산출물이 webroot 로 임베드됨
collector/            수집기 — 4개 플랫폼 → POST /api/usage
  internal/transcript/  Claude Code 파서
  internal/codex/       Codex 파서
  internal/gemini/      Gemini CLI 파서
  internal/antigravity/ Antigravity 스풀·statusLine 캡처·체이닝
contract/             계약 검증 하네스 + 동결 골든 44개
migrations/pg/        PostgreSQL 스키마
scripts/install.sh    원커맨드 설치기(서버가 /install.sh 로 서빙)
deploy/               AWS Terraform · 훅 배포물
```

---

## 문서

| 문서 | 무엇 |
|---|---|
| [`docs/OPERATIONS.md`](docs/OPERATIONS.md) | **운영 가이드** — 기동 env·최초 관리자·키 발급/해지·배포·문제 해결·되돌리기 |
| [`docs/VERIFICATION.md`](docs/VERIFICATION.md) | 실측 검증 기록(E2E·멀티테넌트 격리) |
| [`docs/PLAN-saas-ingestion.md`](docs/PLAN-saas-ingestion.md) | SaaS 인제스트 아키텍처 기획(+ 구현 현황) |
| [`docs/PLAN-phase1-multitenant.md`](docs/PLAN-phase1-multitenant.md) | Phase 1 멀티테넌트 구현계획(+ 슬라이스별 현황) |
| [`deploy/README.md`](deploy/README.md) | AWS 배포(Terraform) |
| [`PORT-STATUS.md`](PORT-STATUS.md) | Node → Go 컷오버 경위와 잔여 리스크 |

> **OTLP 경로는 제거됐습니다(PR #16).** 공식 Claude Code OTel 규약(`claude_code.*`)과 비호환인
> 자체 규약(`claude.*`)이라 "표준처럼 보이지만 표준이 아닌" 반쪽 자산이 됐기 때문입니다.
> 제거된 것: `POST /v1/logs` 수신구, `GET /api/usage/export/otlp`, `go/internal/otlp` 패키지,
> `docs/SPEC-otlp-claude.md`. 확장 방향은 OTLP 가 아니라 **멀티플랫폼 퍼스트파티 수집기**이고,
> 지금 4개 플랫폼이 그 결과입니다. 경위는 [`docs/PLAN-saas-ingestion.md`](docs/PLAN-saas-ingestion.md) §4.
