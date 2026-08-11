# 운영 가이드

서버를 띄우고, 관리자를 만들고, 개발자 머신을 붙이고, 문제가 났을 때 어디를 보고, 되돌리는
방법. **여기 적힌 명령은 전부 실제로 실행해 출력을 확인한 것입니다.** 검증하지 못한 절차는
"미검증"이라고 밝히고 그 사실을 함께 적었습니다.

- 제품 개요·데이터 정책·API 계약: [`../README.md`](../README.md)
- AWS(ECS Fargate + ALB + RDS) 배포: [`../deploy/README.md`](../deploy/README.md)
- 실측 검증 기록: [`VERIFICATION.md`](VERIFICATION.md)

---

## 1. 서버 기동

### 1-1. 빌드

```bash
bash scripts/build.sh      # web 빌드 → webroot 임베드 → go build (유일 빌드 경로)
```

### 1-2. 최소 기동 (local · sqlite)

```bash
USAGE_ADMIN_TOKEN=$(openssl rand -hex 24) \
USAGE_INTAKE_TOKEN=$(openssl rand -hex 24) \
USAGE_DATA_DIR=./data \
USAGE_PORT=4191 \
./go/usage-server
```

정상 기동이면 stdout 이 이렇게 나옵니다:

```
  · 키워드 보존 정리: 90일
usage-dashboard: http://127.0.0.1:4191  (mode=local, tenant=default)
  · 브라우저에서 열고 ID/PW 로 로그인한다.
  · 단가표: config.json (없으면 시드 단가표를 쓴다)
  · 인테이크 자격: USAGE_INTAKE_TOKEN(보고 전용 — 조회 불가)
```

> 위 블록은 서버가 실제로 찍는 문구 그대로입니다(실측 2026-08-11).

**로그인할 계정이 없으면 그 줄이 경고로 바뀝니다.** 서버가 부팅에서 직접 확인합니다:

```
  ⚠ tenant=default 에 사용자가 없다 — 화면은 뜨지만 로그인이 되지 않는다(401).
    USAGE_BOOTSTRAP_ADMIN_USER·USAGE_BOOTSTRAP_ADMIN_PASSWORD 로 재기동하거나,
    `usage-server user add -tenant <t> -username <u> -role admin` 으로 만들라.
```

계정이 없으면 화면은 정상으로 뜨는데 로그인만 401 이고, 그때 운영자에게 보이는 단서는
"비밀번호가 틀렸나"뿐입니다. 서버는 답을 알고 있으므로 기동에서 말해 줍니다(§2 로 이어집니다).

> ⚠ **이 줄은 탭 이름을 열거하지 않습니다.** 앞 판본이 `두 탭(사용 추적·사용 관측)` 이었고,
> 로그인이 ID/PW 로 바뀌고 탭이 다섯이 된 뒤에도 그대로 남아 **운영자가 처음 보는 안내문이
> 틀린 방식을 지시**했습니다. 낡은 원인은 문구를 안 고쳐서가 아니라 서버가 소유하지 않은
> 사실(UI 탭 구성)을 찍었기 때문입니다 — 화면 구성은 [`../README.md`](../README.md) 가 소유합니다.

**마지막 줄을 반드시 읽으십시오.** `USAGE_INTAKE_TOKEN` 을 안 걸면 대신 이렇게 나옵니다:

```
  · 인테이크 자격: USAGE_ADMIN_TOKEN 겸용 — 수집기에 배포하는 토큰이 곧 전원 열람 토큰이다.
```

그 상태로 수집기를 배포하면 **팀원 수만큼 복제된 토큰 하나하나가 전사 열람 권한**입니다.

### 1-3. 부팅이 거부될 때

서버는 잘못된 설정을 **모아서** 알려주고 종료합니다(exit 2). 거부 조건:

| 증상 | 원인 |
|---|---|
| `USAGE_ADMIN_TOKEN 이 없다` | 필수. 기본값 없음이 의도입니다 |
| `USAGE_ADMIN_TOKEN 이 너무 짧다(N자)` | 최소 16자 |
| `USAGE_INTAKE_TOKEN 이 USAGE_ADMIN_TOKEN 과 같다` | 분리한 것처럼 보이지만 아무것도 분리되지 않은 상태 |
| `USAGE_DB_MODE 가 '...' 다` | `local` \| `remote` 만. 오타를 local 로 접지 않습니다 |
| `remote 모드인데 DATABASE_URL 이 없다` | 붙을 곳이 없음 |
| `USAGE_PORT=N 는 브라우저가 차단하는 포트다` | 4190·6000·6667 등. **curl 은 200 인데 브라우저에서만 죽습니다** |
| `USAGE_MULTITENANT 는 PostgreSQL(remote)에서만 쓸 수 있다` | sqlite 에는 RLS 가 없어 org 데이터가 섞입니다 |

RLS 롤 위반(SUPERUSER·BYPASSRLS)도 부팅에서 거부됩니다. 터널이 안 붙어 **확인 자체가
불가능한** 경우는 거부하지 않고 stderr 에 경고만 남깁니다 — 검사가 돌지 않았다는 사실이
기록돼야 하기 때문입니다.

### 1-4. PostgreSQL / 멀티테넌트

```bash
USAGE_ADMIN_TOKEN=… \
USAGE_DB_MODE=remote \
USAGE_MULTITENANT=1 \
DATABASE_URL=postgres://usage_app:<pw>@<host>:5432/usage \
./go/usage-server
```

- `USAGE_MULTITENANT` 없이 `remote` 만 주면 **읽기 전용**이 되어 인테이크가 아예 등록되지
  않습니다(운영 DB 조회 전용 모드). SaaS 로 쓰려면 반드시 함께 켜십시오.
- 앱 롤은 `CREATE ROLE usage_app LOGIN NOSUPERUSER NOBYPASSRLS` 로 만듭니다.
- **마이그레이션은 자동 실행 경로에 없습니다.** 되돌리기 어려운 작업이라 사람이 명시적으로
  돌립니다. AWS/RDS 절차는 [`../deploy/README.md`](../deploy/README.md) §2-4 가 소유합니다
  (`migrations/pg/*.sql` 를 번호 순으로 psql 적용).

---

## 2. 최초 관리자 만들기

두 방법이 있고 **둘 다 검증됐습니다.** 화면 로그인(ID/PW)에 쓰는 계정입니다.

### 방법 A — 부팅 env (컨테이너 배포에 적합)

```bash
USAGE_ADMIN_TOKEN=… \
USAGE_BOOTSTRAP_ADMIN_USER=ops-admin \
USAGE_BOOTSTRAP_ADMIN_PASSWORD='…' \
./go/usage-server
```

기동 로그에 이 줄이 나오면 생성된 것입니다(비밀번호는 절대 찍히지 않습니다):

```
  · 최초 관리자 생성: tenant=default username=ops-admin role=admin
```

**멱등입니다.** 대상 tenant 에 사용자가 하나라도 있으면 아무것도 하지 않습니다 — 재기동마다
새로 만들거나 기존 비밀번호를 덮지 않습니다. 그래서 이 env 로는 **비밀번호를 바꿀 수
없습니다.** 대상 tenant 는 `USAGE_BOOTSTRAP_TENANT`(기본 `default`)입니다.

### 방법 B — CLI (서버 호스트에서)

```bash
USAGE_DATA_DIR=./data ./go/usage-server user add \
  -tenant default -username ops-admin -role admin
# 비밀번호: (프롬프트 — 터미널이면 에코 없음)
# → 사용자 생성됨: tenant=default username=ops-admin role=admin
```

`-password` 로 직접 줄 수도 있지만, 그러면 셸 히스토리에 남습니다. 프롬프트를 권합니다.
역할은 `admin` | `member` 입니다.

**비밀번호는 최소 8자(룬 수)입니다 — API 와 같은 규칙이고, CLI 도 예외가 아닙니다.** 짧으면
계정이 만들어지지 않고 종료코드가 1 입니다:

```
$ ./go/usage-server user add -tenant default -username ops-admin -role admin -password a
user add 실패: store: 비밀번호는 최소 8자여야 합니다
```

### 로그인 확인

```bash
curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"username":"ops-admin","password":"…"}' \
  http://127.0.0.1:4191/api/auth/login -c /tmp/ck.txt
# → {"ok":true,"user":{"username":"ops-admin","role":"admin","tenant":"default"}}

curl -s -b /tmp/ck.txt http://127.0.0.1:4191/api/auth/me
# → {"username":"ops-admin","role":"admin","tenant":"default"}
```

틀린 비밀번호는 **401** 입니다.

---

## 3. 인제스트 키 — 발급 · 확인 · 해지

키는 개발자 머신이 **보고할 때만** 쓰는 자격입니다. 열람은 못 합니다(403).

### 3-1. 대시보드에서 (권장)

관리자로 로그인 → **"연동"** 탭 → 키 발급 → 원라인 복사 → 개발자에게 전달.
평문은 **발급 직후 1회만** 보입니다. 목록에는 마스킹된 값만 남습니다.

해당 API(관리자 자격 필요):

| 메서드 | 경로 | 결과 |
|---|---|---|
| `POST` | `/api/admin/keys` | `{"key":"uu_ing_…","id":…,"createdAt":…,"username":…}` — **평문은 이 응답에서 1회만** |
| `GET` | `/api/admin/keys` | **전체** 키 현황(마스크 + 소유자, 평문 없음) |
| `POST` | `/api/admin/keys/revoke` | `{"id":"…"}` → 204 |

본문에 `{"username":"amy"}` 를 실으면 **그 사람에게 묶인 키**를 대리발급합니다(없는 계정이면
404). 본문을 비우면 종전과 같은 org 공용 키입니다. 키를 사람에게 묶으면 그 키로 들어온 보고가
**키 주인으로 귀속**됩니다 — 자세한 것은 [§9](#9-사용자-관리--셀프서비스-인제스트-키) 입니다.
사용자가 스스로 발급하는 경로도 §9 에 있습니다.

### 3-2. CLI 에서 (서버 호스트)

```bash
# org 생성 (멀티테넌트에서 조직 하나 = 테넌트 하나)
USAGE_DATA_DIR=./data ./go/usage-server org create --name "Acme"
# → org 생성됨: id=org_3e1d42dacbf9b427 tenant=org_3e1d42dacbf9b427 name="Acme"

# 키 발급 — 이 평문은 다시 볼 수 없다
USAGE_DATA_DIR=./data ./go/usage-server key issue --org org_3e1d42dacbf9b427
# → uu_ing_5a5bff08fdc370ef413a75aeae8e3b18bfa9ad1d704f0385

# 목록
USAGE_DATA_DIR=./data ./go/usage-server org list
# → org_3e1d42dacbf9b427	active	Acme

# 해지
USAGE_DATA_DIR=./data ./go/usage-server key revoke --key uu_ing_…
# → 해지됨
```

CLI 는 서버와 **같은 env** 를 봅니다(`USAGE_DB_MODE`·`DATABASE_URL`·`USAGE_DATA_DIR`).
관리자 토큰 게이트가 없는 이유는 이것이 배포 호스트에서 운영자가 직접 부르는 명령이고,
**DB 접근 자체가 권한**이기 때문입니다.

#### 키를 사람에게 묶기 (`--user`)

`--user` 를 주면 그 키로 들어온 보고가 **키 주인으로 귀속**됩니다(귀속 우선순위 ① —
[§9](#9-사용자-관리--셀프서비스-인제스트-키)). 화면·API 의 대리발급(§3-1)과 같은 일을 CLI 에서
합니다. **`--user` 를 생략하면 종전과 완전히 같은 org 공용 키**이므로, 지금 쓰는 명령·스크립트는
그대로 둬도 됩니다.

묶을 사람은 **그 org 의 tenant 에 실재하는 계정**이어야 합니다(`org create` 는 org id 를 그대로
tenant id 로 씁니다 — 위 출력의 `tenant=…`):

```bash
# 그 org 의 tenant 에 계정을 먼저 만든다 (§2 의 user add 와 같은 명령)
USAGE_DATA_DIR=./data ./go/usage-server \
  user add -tenant org_ca02e173c51bf68e -username amy -role member -password '********'
# → 사용자 생성됨: tenant=org_ca02e173c51bf68e username=amy role=member

# amy 에게 묶인 키 — 평문은 여기서 1회만
USAGE_DATA_DIR=./data ./go/usage-server key issue --org org_ca02e173c51bf68e --user amy
# → uu_ing_7a52397dd4c80eb6b36d123856346b2574946161e3769592
```

**없는 사용자에 묶으려 하면 거부합니다**(exit 1, 키를 만들지 않습니다):

```bash
USAGE_DATA_DIR=./data ./go/usage-server key issue --org org_ca02e173c51bf68e --user amyy
# → key issue: 사용자 "amyy" 가 tenant "org_ca02e173c51bf68e" 에 없다 — 오타이거나 계정이 아직 없다.
#     먼저 계정을 만들고 다시 발급해라:
#       usage-server user add -tenant org_ca02e173c51bf68e -username amyy -role member
#     (없는 이름에 묶은 키의 보고는 영영 아무에게도 귀속되지 않는다)
```

조용히 만들지 않는 이유: 평문 키는 다시 볼 수 없어 **잘못 발급했다는 사실을 알아채는 시점이
"그 사람 데이터가 화면에 안 보인다"** 뿐입니다. 그때는 이미 오타 이름으로 쌓인 보고가 유령
사용자로 남고, 되돌리는 길은 해지 후 재발급밖에 없습니다.

실측 — `--user amy` 로 발급한 키로 `user=mallory` 를 주장해 보고했을 때:

```bash
curl -s -X POST -H "Authorization: Bearer uu_ing_7a52…" -H 'Content-Type: application/json' \
  -d '{"user":"mallory","machine":"pc-mallory","sessions":[{"id":"sess-bound-01",
       "model":"claude-sonnet-4","output":1234,"startedAt":"2026-08-11T09:00:00.000Z"}]}' \
  http://127.0.0.1:4297/api/usage
# → {"ok":true,"sessions":1,"counters":0,"buckets":0}

curl -s -b /tmp/admin.ck 'http://127.0.0.1:4297/api/usage/sessions?days=365'
# → {"sessions":[{"sessionId":"sess-bound-01","machine":"pc-mallory","username":"amy",…
#                                                                   ^^^^^^^^^^^^^^^^^
```

같은 주장을 **`--user` 없이 발급한 키**로 보내면 종전대로 `username:"mallory"` 입니다
(하위호환 — 이미 배포된 키의 귀속은 이 변경으로 하나도 바뀌지 않습니다).

### 3-3. 키 회전

전용 회전 명령은 없습니다. **새 키 발급 → 배포 → 옛 키 해지** 순서로 합니다. 반대로 하면
해지와 재설치 사이에 보고가 끊깁니다(데이터가 사라지지는 않습니다 — 수집기가 증분
체크포인트를 갖고 있어 다음 성공 실행이 밀린 세션을 함께 올립니다).

### 3-4. 스코프 실측

아래는 실제로 확인한 응답 코드입니다:

| 요청 | 인제스트 키 | 해지된 키 |
|---|---|---|
| `POST /api/usage` | **200** | **401** |
| `GET /api/agent/collector?os=&arch=` | **200** | **401** |
| `GET /api/usage/summary` | **403** | **401** |

관리자 토큰으로 `GET /api/usage/summary` 는 **200** 입니다.

---

## 4. 개발자 머신 연동 (원커맨드)

개발자에게 전달할 한 줄:

```sh
curl -fsSL $SERVER/install.sh | sh -s -- --key <인제스트키> --server $SERVER
```

`$SERVER` 는 **https 여야 합니다.** 예외는 loopback(`127.0.0.1`·`localhost`)뿐이고, 그건 로컬
테스트용입니다. 이 스크립트가 서버에서 받은 바이너리를 실행하기 때문에 평문 http 는 중간자가
임의 코드를 심을 수 있는 자리입니다.

정상 실행 출력(실측):

```
▶ ① 플랫폼 감지
  OS=Darwin arch=arm64 → darwin/arm64
▶ ② 수집기 다운로드
  설치: /Users/…/.local/bin/usage-collector
▶ ③ 설정 저장
  ~/.config/claude-usage/config.env (perms 600) — SERVER·KEY·COLLECTOR_BIN
▶ ④ Claude Code SessionEnd 훅 등록
  병합 도구: jq
  백업: ~/.claude/settings.json.bak
  훅 등록: ~/.claude/settings.json
▶ ⑥ 초기 백필
연동 완료 ✓ — N 세션 전송
```

Antigravity CLI 가 설치돼 있으면 `⑤ Antigravity CLI 연동` 단계가 추가로 나오고
`~/.gemini/antigravity-cli/settings.json`(statusLine)과 `~/.gemini/config/hooks.json`(Stop 훅)이
등록됩니다. 없으면 **그 단계가 통째로 조용히 빠집니다** — 안 쓰는 사람의 설치 로그를 남의 도구
목록으로 만들지 않기 위해서입니다.

### 검증된 성질

| 성질 | 확인 방법 | 결과 |
|---|---|---|
| 기존 훅·설정 보존 | 다른 `SessionEnd` 훅 + `theme` 가 있는 `settings.json` 으로 설치 | 둘 다 그대로 남음 |
| 멱등 | 같은 명령 2회 실행 | 우리 훅 그룹은 **1개**(중복 없음) |
| 훅에 평문 토큰 없음 | `settings.json` 에서 키 문자열 검색 | **0건** |
| 설정 파일 권한 | `ls -l ~/.config/claude-usage/config.env` | `-rw-------` (600) |
| 덮기 전 백업 | 설치 로그 | `settings.json.bak` 생성 |

### 요구 사항

`curl` 과 **JSON 도구 하나**(`jq` \| `python3` \| `node`)가 필요합니다. 셋 다 없고 설정 파일이
이미 존재하면 설치기는 **덮지 않고 멈춥니다.** 손상 JSON 이어도 마찬가지로 한 바이트도
건드리지 않습니다 — 남의 설정을 깨뜨리느니 설치를 포기하는 쪽입니다.

---

## 5. 문제 해결

### 5-1. 401 이 나온다

401 은 **자격이 안 먹었다**는 뜻입니다. 나오는 자리가 셋이고 원인이 다릅니다.

**① 설치 중 `다운로드 실패` (`/api/agent/collector`)**

```bash
# 키가 살아 있는지 직접 확인
curl -s -o /dev/null -w "%{http_code}\n" \
  -H "Authorization: Bearer <키>" \
  "$SERVER/api/agent/collector?os=darwin&arch=arm64"
```

| 코드 | 뜻 | 할 일 |
|---|---|---|
| `401` | 키가 없거나·틀리거나·**해지됨** | 대시보드 "연동" 탭에서 키 목록 확인 → 새로 발급 |
| `404` | os/arch 조합이 화이트리스트 밖 | `darwin`\|`linux` × `amd64`\|`arm64` 만 지원 |
| `503` | 그 플랫폼 수집기 바이너리가 서버에 없음 | 서버를 `scripts/build.sh` 로 다시 빌드·배포 |
| `200` | 키는 정상 | 원인은 네트워크·프록시 쪽 |

**② 보고가 안 올라간다 (`POST /api/usage` 401)**

```bash
# 개발자 머신에서 — 토큰 값은 출력하지 않는다
grep -c KEY ~/.config/claude-usage/config.env     # 1 이어야 한다
USAGE_INTAKE_TOKEN="$(. ~/.config/claude-usage/config.env; printf %s "$KEY")" \
  ~/.local/bin/usage-collector -server "$SERVER"
```

가장 흔한 원인은 **키 해지**입니다(운영자가 회전시켰는데 재설치가 안 됨). 원커맨드를 새 키로
다시 실행하면 됩니다 — 멱등이라 훅이 중복되지 않습니다.

**③ 로그인이 401**

계정·비밀번호 문제입니다. 부팅 env 로 만든 관리자는 **이미 사용자가 있으면 만들어지지
않는다**는 점을 먼저 의심하십시오(§2 방법 A). 확실히 하려면 `user add` 로 새 계정을 만듭니다.

### 5-2. 403 이 나온다

**자격은 맞는데 범위 밖**입니다. 거의 항상 원인 하나입니다 — **인제스트 키·인테이크 토큰으로
조회를 시도**한 것. 조회에는 관리자 토큰이나 로그인 세션을 쓰십시오.

상태변경(`POST`/`PUT`/`DELETE`)이 403 이면 **쿠키로 시도**한 경우입니다. 쿠키로는 조회만
됩니다 — 상태변경은 `Authorization` 헤더 인증만 인정합니다(CSRF 표면 제거).

### 5-3. 화면이 뜨는데 데이터가 없다

1. `GET /healthz` 가 200 인가 (무인증·무DB 프로브)
2. 커버리지 화면에서 **머신별 마지막 보고 시각**을 본다 — 언제 끊겼는지가 여기 있습니다
3. 개발자 머신에서 수집기를 손으로 한 번 돌려 stderr 를 본다:
   - `세션 파일(*.jsonl)이 없다` → 그 CLI 를 안 썼거나 원천 경로가 다름
   - `보낼 것이 없다(모든 세션이 마지막 전송 이후 그대로다)` → 정상. 새 활동이 없을 뿐
   - `⚠ 압축된 롤아웃 N개를 건너뛴다(.zst 해제 미지원)` → Codex 가 7일 지난 세션을 압축한 것.
     **그만큼 사용량이 빠집니다**(알려진 한계)

### 5-4. 브라우저에서만 아무것도 안 된다

`USAGE_PORT` 가 브라우저 차단 포트(4190·6000·6667 등)일 때의 증상입니다. 서버는 정상 기동하고
curl 도 200 을 받는데 브라우저만 죽습니다. 지금은 **부팅에서 거부**하므로 이 상태로 뜨지
않지만, 옛 배포 설정을 그대로 쓰다 부팅이 거부되면 이 항목을 보십시오.

### 5-5. 비용이 이상하다

- **`unpriced` 목록을 먼저 보십시오.** 공식 단가가 없는 모델(현재 `gemini-3.1-pro`·
  `gpt-oss-120b`)은 비용에 잡히지 않습니다. 추측 단가를 넣지 않는 것이 의도이고, 그만큼
  합계는 **과소**입니다.
- 기동 로그의 `· 단가표: <경로>` 를 확인하십시오. cwd 가 레포 루트가 아니면 `config.json` 을
  못 찾아 **조용히 시드 단가로 떨어집니다.** 배포에서는 `USAGE_CONFIG` 를 명시하는 편이 안전합니다.
- 화면 값은 **"API 환산 비용"** 이지 청구액이 아닙니다. 구독 요금제(ChatGPT Plus·Claude·
  Antigravity)는 토큰당 과금이 없습니다.

### 5-6. pg 인데 남의 데이터가 보인다

**즉시 멈추십시오.** `DATABASE_URL` 이 SUPERUSER 또는 BYPASSRLS 롤이면 RLS 격리가 통째로
무력화되는데 증상이 없습니다(요청은 200). 지금은 부팅 프로브가 이걸 거부하지만, 프로브가
"판정 불가"로 접힌 채 떴을 수 있습니다. 기동 로그에서 다음 줄을 확인하십시오:

```
  · DB 롤 확인 — 비-슈퍼·비-BYPASSRLS(RLS 테넌트 격리 성립)
```

이 줄이 없고 `⚠ DB 롤을 확인하지 못했다` 가 있으면, **격리가 검증되지 않은 채 뜬 것**입니다.

---

## 6. 되돌리기

### 6-1. 개발자 머신에서 연동 해제

⚠ **`.bak` 은 "최초 원본"이 아니라 "직전 상태"입니다.** 설치기는 덮어쓸 때마다 백업을
새로 만들므로, 2회 이상 실행했다면 `.bak` 에도 이미 우리 훅이 들어 있습니다(실측 확인).
그래서 **완전한 제거에는 `.bak` 복구가 아니라 아래 ②를 쓰십시오.**

**① 직전 상태로 되돌리기** (설치를 방금 1회 했고 그걸 취소하는 경우)

```sh
cp -f ~/.claude/settings.json.bak ~/.claude/settings.json
# Antigravity 도 연동했다면
cp -f ~/.gemini/antigravity-cli/settings.json.bak ~/.gemini/antigravity-cli/settings.json
cp -f ~/.gemini/config/hooks.json.bak            ~/.gemini/config/hooks.json
```

**② 우리 훅만 확실히 제거**(권장 — 실행 횟수와 무관하게 맞습니다)

```sh
jq '.hooks.SessionEnd = [.hooks.SessionEnd[]
      | select(([.hooks[].command] | map(contains("claude-usage/config.env")) | any) | not)]' \
   ~/.claude/settings.json > /tmp/s.json && mv /tmp/s.json ~/.claude/settings.json
```

실측: 남의 훅(`echo 남의-훅`)과 `theme` 는 그대로 남고 우리 그룹만 빠집니다.

**③ 나머지 정리**

```sh
rm -f  ~/.config/claude-usage/config.env      # 키 폐기 (가장 중요)
rm -f  ~/.local/bin/usage-collector           # 바이너리
rm -rf ~/.config/claude-usage/antigravity     # Antigravity 스풀
rm -f  ~/.claude/usage-collector-state.json   # 증분 체크포인트
```

Antigravity 를 연동했다면 `~/.gemini/antigravity-cli/settings.json` 의 `statusLine` 도
지우거나, 설치 전 명령(설치기가 `config.env` 의 `AGY_PREV_STATUSLINE` 으로 보관해 둔 값)으로
되돌리십시오. **`config.env` 를 먼저 지우면 그 보관값도 함께 사라지므로**, 원래 상태줄을
복구할 생각이면 지우기 전에 값을 확인하십시오.

> 체크포인트를 지워도 값이 부풀지 않습니다 — 서버가 세션 절대값으로 UPSERT 하므로 전량
> 재전송이 멱등입니다. 지우면 다음 실행이 한 번 오래 걸릴 뿐입니다.

### 6-2. 서버 쪽 되돌리기

- **키를 잘못 뿌렸다** → `key revoke --key …`. 즉시 401 이 됩니다(실측).
- **배포를 되돌린다** → 이전 이미지 태그로 서비스 롤백. 앱은 스키마를 자동으로 바꾸지 않으므로
  (마이그레이션은 수동) 앱만 되돌리는 것이 안전합니다.
- **마이그레이션을 되돌린다** → **자동 롤백 경로가 없습니다.** down 마이그레이션이 없으므로
  스냅샷/PITR 복구가 유일한 수단입니다. 적용 전에 백업을 확인하십시오.
- **데이터를 지운다** → 보존 정리기는 `keyword` 축만 지웁니다. 그 외는 무기한이고 삭제 명령이
  없습니다. 특정 사람·머신을 지워야 하면 해당 행을 직접 지우거나 귀속 교정으로 이름을 바꿉니다
  (근거: [`../README.md`](../README.md) "무엇이 언제까지 남는가").

---

## 7. 정기 점검

| 주기 | 확인 | 어떻게 |
|---|---|---|
| 주 1회 | 보고가 끊긴 머신 | 커버리지 화면의 머신별 마지막 보고 |
| 주 1회 | `unpriced` 모델이 늘었는가 | 비용 화면의 unpriced 목록 — 늘었으면 `config.json` 에 단가 추가 |
| 배포마다 | 인테이크 자격이 분리돼 있는가 | 기동 로그의 `· 인테이크 자격:` 줄 |
| 배포마다 | (pg) RLS 롤 확인이 통과했는가 | 기동 로그의 `· DB 롤 확인` 줄 |
| 퇴사·이탈 시 | 그 사람 머신의 키 | 해당 키 해지 후 재발급 배포 |

### 상시 게이트 (CI)

[`.github/workflows/ci.yml`](../.github/workflows/ci.yml):

- `test` 잡 — go vet+test, collector 단위 테스트, **collector E2E**, web 테스트,
  빌드(+임베드 드리프트), **contract:verify 골든 44**
- `pg-isolation` 잡 — 실제 PostgreSQL + 앱 롤(NOSUPERUSER·NOBYPASSRLS)로 **크로스테넌트 격리**

로컬에서 같은 것을 돌리려면:

```bash
cd go        && go test ./... && go vet ./...
cd collector && go test ./... && go vet ./...
cd web       && npm test
```

---

## 8. 저장된 자리표시자 모델 라벨 정리 (`<synthetic>`)

### 8-1. 언제 필요한가

모델 축(비용·모델별 화면)에 **`<synthetic>` 같은 행이 보일 때**입니다. Claude Code 는 중단·오류
메시지 같은 턴에 모델 이름 대신 그 자리표시자를 쓰고, 그것이 그대로 저장돼 있던 것입니다.

인테이크는 **이제 그 값을 접습니다**(`go/internal/intake/intake.go` 의 `normModel`). 그래서
**앞으로 들어오는 보고에는 생기지 않습니다.** 그러나 그 수정 이전에 저장된 행은 그대로 남으므로,
이미 쌓인 데이터를 되돌리려면 아래를 한 번 돌리십시오.

> **숫자는 움직이지 않습니다.** 자리표시자 턴의 토큰은 전부 0 입니다. 세션·버킷·턴·품질 카운터를
> 하나도 버리지 않고 **모델 라벨만** 바꿉니다. 아래 8-4 가 그 실측입니다.

**한 번만 돌리면 됩니다.** 정기 점검(§7)에 넣을 일이 아니고, 자동 실행 경로에도 올라가 있지
않습니다 — 되돌리기 어려운 작업은 사람이 명시적으로 돌립니다.

### 8-2. local (sqlite) — CLI

CLI 는 서버와 **같은 env** 를 봅니다(`USAGE_DB_MODE`·`DATABASE_URL`·`USAGE_DATA_DIR`).
**기본값은 dry-run** 이라 그냥 부르면 아무것도 바꾸지 않고 몇 행이 바뀔지만 보여 줍니다.

```bash
# ① 먼저 무엇이 바뀌는지 본다 (아무것도 바꾸지 않는다)
USAGE_DB_MODE=local USAGE_DATA_DIR=./data ./go/usage-server cleanup placeholder-models
```

```
cleanup placeholder-models (dry-run · 아무것도 바꾸지 않았다)
  · usage_sessions : 2행 → model=NULL
  · usage_series   : 3행 → model=(미상) (개명 1 · 기존 버킷과 합산 병합 2)
  · 자리표시자 값  : <none>(2) <synthetic>(3)
  · 토큰 합계는 움직이지 않는다 — 자리표시자 턴의 토큰은 0 이고, 라벨만 바꾼다.
  · 실제로 바꾸려면 --apply 를 붙여라.
```

```bash
# ② 그 계획이 맞으면 실제로 바꾼다
USAGE_DB_MODE=local USAGE_DATA_DIR=./data ./go/usage-server cleanup placeholder-models --apply
```

```
cleanup placeholder-models (--apply · 실제로 바꿨다)
  · usage_sessions : 2행 → model=NULL
  · usage_series   : 3행 → model=(미상) (개명 1 · 기존 버킷과 합산 병합 2)
  · 자리표시자 값  : <none>(2) <synthetic>(3)
  · 토큰 합계는 움직이지 않는다 — 자리표시자 턴의 토큰은 0 이고, 라벨만 바꾼다.
```

```bash
# ③ 멱등 확인 — 두 번째는 0행이다
USAGE_DB_MODE=local USAGE_DATA_DIR=./data ./go/usage-server cleanup placeholder-models
```

```
cleanup placeholder-models (dry-run · 아무것도 바꾸지 않았다)
  · 정리할 행이 없다.
```

> 서버를 멈추지 않아도 됩니다(같은 sqlite 파일을 열 뿐입니다). 다만 화면은 캐시 없이 매번
> 질의하므로, 새로고침하면 곧 반영됩니다.

### 8-3. remote (PostgreSQL) — 마이그레이션

`migrations/pg/0037_placeholder_model_cleanup.sql` 을 §2-4 ②와 **같은 절차·같은 자격**으로
번호 순서대로 적용하십시오(마스터 롤 = 표 소유자).

```bash
PGPASSWORD='<master-password>' psql \
  "host=$RDS_HOST port=5432 dbname=usage user=usage_admin sslmode=require" \
  -v ON_ERROR_STOP=1 -f migrations/pg/0037_placeholder_model_cleanup.sql
```

```
BEGIN
ALTER TABLE
ALTER TABLE
UPDATE 3
INSERT 0 3
ALTER TABLE
ALTER TABLE
COMMIT
```

두 번째 실행은 `UPDATE 0` / `INSERT 0 0` 입니다(멱등).

⚠ **주의 두 가지.**

- **테이블 잠금이 걸립니다.** 이 파일은 `usage_sessions`·`usage_series` 의 `FORCE ROW LEVEL
  SECURITY` 를 트랜잭션 안에서만 잠시 풀고(끝에서 되돌립니다) 그 사이 ACCESS EXCLUSIVE 락을
  잡습니다 — 그동안 인테이크가 대기합니다. **트래픽이 낮을 때 돌리십시오.**
  FORCE 를 푸는 이유: 마이그레이션은 `app.tenant_id` GUC 없이 돌기 때문에, 풀지 않으면 소유자
  에게도 한 행이 보이지 않아 **오류 없이 0행을 고치고 끝납니다**(조용한 미적용).
  앱 롤 `usage_app` 의 테넌트 격리는 내내 그대로입니다(`ENABLE` 은 건드리지 않습니다).
- **`ALTER TABLE` 이 실패하면** 그 롤이 표 소유자가 아닙니다. 앱 롤(`usage_app`)로 돌리지
  마십시오 — §2-4 ①의 마스터 자격으로 돌려야 합니다.

적용 후 확인(마스터 자격):

```sql
SELECT relname, relrowsecurity, relforcerowsecurity FROM pg_class
 WHERE relname IN ('usage_sessions','usage_series');   -- 둘 다 t | t 여야 합니다
```

**psql 을 쓸 수 없다면 CLI 도 됩니다** — 8-2 와 같은 명령이 `USAGE_DB_MODE=remote` 에서도
그대로 듣습니다. 이쪽은 앱 롤(`usage_app`)로 붙어도 되고 테이블 잠금도 걸지 않습니다. 대신
RLS 가 한 번에 한 테넌트만 보여 주므로 **테넌트마다 한 번씩** 돌려야 합니다:

```bash
USAGE_DB_MODE=remote DATABASE_URL='postgres://usage_app:<PW>@host:5432/usage?sslmode=require' \
  ./go/usage-server cleanup placeholder-models --tenant <tenant-id> --apply
```

단일테넌트 배포라면 `--tenant` 를 생략하십시오(기본 테넌트 `default`).

### 8-4. 무엇이 보존되는지 (실측)

`usage_series` 는 `model` 이 PK 의 일부라, 라벨만 바꾸면 같은 시각의 기존 버킷과 충돌합니다.
그래서 **합산 병합**합니다 — 컬럼마다 결합 방식이 다릅니다.

| 컬럼 | 결합 |
|---|---|
| `input`·`output`·`cache_read`·`cache_create`·`cc_5m`·`cc_1h`·`*_long` | 합 |
| `turns`·`tool_errors`·`stop_max_tokens`·`stop_refusal`·`latency_ms_sum`·`latency_turns` | 합 |
| `latency_ms_max` | **`MAX`**(합이 아닙니다) |
| `username`·`machine`·`project` | 기존 값 유지(비었을 때만 채웁니다) |

sqlite 실측 — 정리 전후 `/api/usage/quality` 응답이 **바이트 동일**했습니다:

```
정리 전: {"turns":49,"toolErrors":7,"stopMaxTokens":3,"stopRefusal":4,"latencyMaxMs":4321,"latencyTurns":9,…}
정리 후: {"turns":49,"toolErrors":7,"stopMaxTokens":3,"stopRefusal":4,"latencyMaxMs":4321,"latencyTurns":9,…}
```

`/api/usage/summary` 의 `①+②+③ == Totals` 불변식도 정리 전후 모두 성립했고, 모델 축에서만
자리표시자가 사라졌습니다:

```
정리 전  byModel: claude-opus-4-8 · a<b>c · (미상) · <none> · <synthetic>
정리 후  byModel: claude-opus-4-8 · a<b>c · (미상)          ← 두 자리표시자가 (미상)으로 합류
totals   정리 전후 동일 (sessions 3 · input 6050 · output 9060 · cacheRead 300 · cacheCreate 400)
```

> `a<b>c` 는 **건드리지 않습니다.** 자리표시자는 꺾쇠로 감싼 값 **전체**(`<…>`)만이고, 꺾쇠가
> 일부만 있는 값은 실제 모델명일 수 있습니다. 판정 규칙의 단일 출처는
> `go/internal/intake/intake.go` 의 `placeholderModelRe` 입니다.

### 8-5. 되돌리기

**전용 되돌리기 명령은 없습니다** — §6-2 와 같습니다. 이 작업은 자리표시자 라벨을 "모른다"로
접는 것이고, 어느 행이 원래 `<synthetic>` 이었는지는 남지 않습니다(그 값을 남기는 것이 이
작업의 목적에 반합니다). 되돌려야 한다면 **스냅샷/PITR 복구가 유일한 수단**입니다.

그래서 순서가 이렇습니다:

1. `--apply` 없이 먼저 돌려 계획을 확인한다(8-2 ①).
2. pg 는 **적용 전에 백업/스냅샷을 확인한다**(§6-2 와 같은 규율).
3. 그다음 `--apply` / `psql -f` 를 돌린다.

숫자가 움직이지 않는 작업이라 되돌릴 실익은 사실상 라벨뿐입니다. 그래도 되돌릴 수 없다는
사실은 먼저 말해 두는 편이 맞습니다.

---

## 9. 사용자 관리 · 셀프서비스 인제스트 키

§2 가 **최초** 관리자를 만드는 방법이라면, 여기는 그 뒤의 일상 운영입니다 — 사람을 더 만들고,
역할을 바꾸고, 내보내고, 각자가 자기 인제스트 키를 갖게 하는 절차.

아래 명령은 전부 실제로 실행해 출력을 확인한 것입니다(local·sqlite, `USAGE_PORT=4292`).
`$B` 는 서버 주소이고, 관리자 세션 쿠키를 `/tmp/admin.ck` 에 받아 씁니다.

```bash
B=http://127.0.0.1:4292
curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"username":"ops-admin","password":"…"}' $B/api/auth/login -c /tmp/admin.ck
# → {"ok":true,"user":{"username":"ops-admin","role":"admin","tenant":"default"}}
```

### 9-1. 왜 키를 사람에게 묶는가

**지금까지 귀속은 PC 가 주장하는 이름이었습니다.** 수집기는 OS 계정명을 `payload.user` 로,
호스트명을 `machine` 으로 보냅니다. 인제스트 키는 org 에만 묶여 있었으므로, **키 사본을 가진
누구나 남의 이름으로 보고할 수 있었습니다.** 한 org 키가 팀원 수만큼 복제되어 각자 디스크에
놓이므로 누가 실제로 보고했는지 가릴 방법이 없었고, 화면은 그것을 사실로 표시했습니다.

키에 사람을 묶으면 그 구멍이 닫힙니다. 귀속 우선순위는 이렇습니다:

| | 근거 | 언제 |
|---|---|---|
| ① | **키에 묶인 username** | 있으면 무조건 이깁니다 — "그 사용자의 키를 실제로 갖고 있음"이 증명된 사실입니다 |
| ② | `machine → username` 매핑 | 관리자가 손으로 고친 값(§5-3 의 귀속 교정) |
| ③ | `payload.user` | 클라이언트 주장 — 최후 |

**①이 설 때 ②는 아예 조회되지 않습니다.** 매핑을 조용히 덮어쓰는 것이 아닙니다.

**하위호환:** 지금 배포된 키는 전부 `username` 이 비어 있으므로 종전대로 ②→③ 을 탑니다.
이 변경으로 기존 배포의 귀속은 **하나도 바뀌지 않습니다.** 사람에게 묶고 싶으면 새 키를
발급해 배포하면 됩니다(§3-3 의 회전 절차 그대로).

실측 — amy 에게 묶인 키로 `user=mallory`, `machine=pc-mallory` 를 주장해 보고했을 때:

```bash
curl -s -X POST -H "Authorization: Bearer $PLAIN" -H 'Content-Type: application/json' \
  -d '{"user":"mallory","machine":"pc-mallory","sessions":[{"id":"sess-live-01",
       "model":"claude-sonnet-4","output":1234,"startedAt":"2026-08-11T09:00:00.000Z"}]}' \
  $B/api/usage
# → {"ok":true,"sessions":1,"counters":1,"buckets":1}

curl -s -b /tmp/admin.ck "$B/api/usage/sessions?days=365"
# → {"sessions":[{"sessionId":"sess-live-01","machine":"pc-mallory","username":"amy",…
#                                                                  ^^^^^^^^^^^^^^^^^
```

`machine` 은 보고된 값 그대로 남고 **귀속만** 키 주인으로 갑니다 — 어느 PC 에서 왔는지는
여전히 보입니다.

### 9-2. 사용자 관리 API (관리자 전용)

| 메서드 | 경로 | 본문 | 결과 |
|---|---|---|---|
| `GET` | `/api/admin/users` | — | `{"users":[{username, role, createdAt, team}]}` |
| `POST` | `/api/admin/users` | `{username, password, role?}` | 생성. `role` 기본은 `member` |
| `POST` | `/api/admin/users/role` | `{username, role}` | 역할 변경 |
| `POST` | `/api/admin/users/password` | `{username, password}` | 비밀번호 재설정 |
| `POST` | `/api/admin/users/team` | `{username, team}` | 팀 배정 |
| `POST` | `/api/admin/users/delete` | `{username}` | 삭제 |

```bash
# 생성 — 비밀번호는 최소 8자(룬 수). 평문은 요청 본문에만 존재하고 응답·로그에 남지 않습니다.
curl -s -X POST -b /tmp/admin.ck -H 'Content-Type: application/json' \
  -d '{"username":"amy","password":"amy-password-1","role":"member"}' $B/api/admin/users
# → {"ok":true,"user":{"username":"amy","role":"member","createdAt":"2026-08-11T04:42:00Z","team":null},"sessionsRevoked":false}

# 목록
curl -s -b /tmp/admin.ck $B/api/admin/users
# → {"users":[{"username":"amy","role":"member","createdAt":"2026-08-11T04:42:00Z","team":null},
#             {"username":"ops-admin","role":"admin","createdAt":"2026-08-11T04:41:46Z","team":null}]}

# 팀 배정 (기존 team_members 를 그대로 씁니다 — 팀별 롤업 화면의 근거)
curl -s -X POST -b /tmp/admin.ck -H 'Content-Type: application/json' \
  -d '{"username":"amy","team":"플랫폼"}' $B/api/admin/users/team
# → {"ok":true,"user":{…,"team":"플랫폼"},"sessionsRevoked":false}

# 역할 변경
curl -s -X POST -b /tmp/admin.ck -H 'Content-Type: application/json' \
  -d '{"username":"amy","role":"admin"}' $B/api/admin/users/role
# → {"ok":true,"user":{…,"role":"admin"},"sessionsRevoked":false}

# 비밀번호 재설정 — 그 사용자의 세션이 **전부** 끊깁니다.
# activeKeys 는 "이 사람에게 아직 살아 있는 인제스트 키가 몇 개인가"입니다(재설정은 키를 거두지
# 않습니다 — 아래 설명 참조).
curl -s -X POST -b /tmp/admin.ck -H 'Content-Type: application/json' \
  -d '{"username":"amy","password":"amy-password-2"}' $B/api/admin/users/password
# → {"ok":true,"user":{…},"sessionsRevoked":true,"activeKeys":1}

# 삭제 — 세션과 **인제스트 키를 함께** 거둡니다
curl -s -X POST -b /tmp/admin.ck -H 'Content-Type: application/json' \
  -d '{"username":"bob"}' $B/api/admin/users/delete
# → {"ok":true,"username":"bob","sessionsRevoked":true,"keysRevoked":1}
```

**`sessionsRevoked` 와 `keysRevoked` 를 읽으세요.** "그 변경으로 그 사람의 자격을 실제로 거뒀는가"
입니다. 안 거뒀다면 강등된 사람이 세션 만료까지 옛 권한을 그대로 들고 있거나, 지워진 사람의
수집기가 계속 보고하는데, 요청은 200 이고 화면에는 아무 증상이 없습니다 — 이 두 응답 필드가
그 침묵을 깨는 유일한 신호입니다.

#### 삭제(퇴사 처리)가 실제로 거두는 것

계정 삭제는 **세 가지를 한 번에** 처리합니다. 셋 다 응답에 값으로 나타납니다:

| 거두는 것 | 응답 필드 | 안 거두면 무슨 일이 |
|---|---|---|
| 계정 (`auth_users`) | — (`ok:true`) | 로그인이 계속 됩니다 |
| 세션 (`auth_sessions`) | `sessionsRevoked` | 세션 만료(12h)까지 옛 권한으로 삽니다 |
| 결속 인제스트 키 | `keysRevoked` | **삭제된 이름으로 사용량이 계속 쌓입니다** |

`keysRevoked` 가 0 이면 "거둘 키가 없었다"는 뜻이지 "안 봤다"가 아닙니다. 실측:

```bash
# 1) leaver 에게 결속 키를 발급하고, 그 키로 보고가 통하는 것을 먼저 확인합니다
KEY=$(curl -s -X POST -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' \
        -d '{"username":"leaver"}' $B/api/admin/keys | sed 's/.*"key":"\([^"]*\)".*/\1/')
curl -s -w ' [%{http_code}]\n' -X POST -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"user":"leaver","machine":"pc-leaver","sessions":[{"id":"sess-before-off",
       "model":"claude-sonnet-4","output":10,"startedAt":"2026-08-03T09:00:00.000Z"}]}' $B/api/usage
# → {"ok":true,"sessions":1,"counters":0,"buckets":0} [200]

# 2) 퇴사 처리
curl -s -w ' [%{http_code}]\n' -X POST -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' -d '{"username":"leaver"}' $B/api/admin/users/delete
# → {"ok":true,"username":"leaver","sessionsRevoked":true,"keysRevoked":1} [200]

# 3) ★ 같은 키로 다시 보고 — 이제 거부됩니다
curl -s -w ' [%{http_code}]\n' -X POST -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"user":"leaver","machine":"pc-leaver","sessions":[{"id":"sess-after-off",
       "model":"claude-sonnet-4","output":10,"startedAt":"2026-08-03T09:00:00.000Z"}]}' $B/api/usage
# → {"error":"unauthorized"} [401]
```

거두는 범위는 **그 사람에게 묶인 키만**입니다. org 공용(레거시) 키와 남의 키는 건드리지 않습니다 —
퇴사자 한 명이 팀 전체의 수집기를 멈추면 안 되기 때문입니다.

#### 비밀번호 재설정은 키를 거두지 않습니다 (의도)

재설정 이유의 절반은 단순 분실입니다. 거기서 키까지 조용히 죽이면 그 사람의 수집기가 **아무 신호
없이** 멈추고, 아무도 그것이 재설정 때문이라는 것을 모릅니다. 그래서 재설정은 세션만 끊고,
살아 있는 키 개수를 `activeKeys` 로 알려 줍니다.

**침해가 의심되어 재설정한 것이라면 `activeKeys` 가 0 이 아닐 때 키도 함께 회전하세요** —
그 키는 비밀번호와 무관하게 계속 보고할 수 있는 쓰기 자격입니다:

```bash
# 그 사람의 키 목록에서 id 를 찾아 해지합니다(9-4 의 관리자 현황과 같은 표)
curl -s -H "Authorization: Bearer $ADMIN_TOKEN" $B/api/admin/keys
curl -s -w ' [%{http_code}]\n' -X POST -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' -d '{"id":"<key id>"}' $B/api/admin/keys/revoke
# → [204]
```

### 9-3. 서버가 거부하는 것 (실측)

화면에서 버튼을 숨기는 것은 방어가 아닙니다. 아래는 **서버가** 내는 응답입니다:

| 시도 | 응답 |
|---|---|
| 마지막 관리자 강등·삭제 | **409** `마지막 관리자는 강등·삭제할 수 없습니다 — 먼저 다른 관리자를 만드세요` |
| 자기 자신 강등·삭제 | **409** `자기 자신은 강등할 수 없습니다 — 다른 관리자에게 요청하세요` |
| member 가 `/api/admin/users` | **403** `개인 열람 토큰은 자기 데이터만 볼 수 있습니다 — …` |
| 레거시 `usage_tok` 쿠키로 상태변경 | **403** `쿠키 인증으로는 상태변경을 할 수 없습니다 — Authorization: Bearer 를 사용하세요` |
| 없는 사용자 대상 | **404** `사용자를 찾을 수 없습니다` |
| 중복 사용자 생성 | **409** `이미 있는 사용자입니다` |
| 8자 미만 비밀번호 | **400** `store: 비밀번호는 최소 8자여야 합니다` |

```bash
# 마지막 관리자 강등 시도
curl -s -w ' [%{http_code}]\n' -X POST -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"username":"ops-admin","role":"member"}' $B/api/admin/users/role
# → {"error":"마지막 관리자는 강등·삭제할 수 없습니다 — 먼저 다른 관리자를 만드세요"} [409]

# 강등된 사용자의 세션은 즉시 죽습니다
curl -s -w ' [%{http_code}]\n' -b /tmp/amy.ck $B/api/auth/me
# → {"error":"unauthorized"} [401]

# 레거시 쿠키로 사용자 생성
curl -s -w ' [%{http_code}]\n' -X POST -H "Cookie: usage_tok=$ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"username":"mallory","password":"mallory-password-1","role":"admin"}' $B/api/admin/users
# → {"error":"쿠키 인증으로는 상태변경을 할 수 없습니다 — …"} [403]
```

**마지막 관리자 보호는 동시 요청에서도 섭니다.** 판정("지금 관리자가 몇 명인가")과 변경(강등·삭제)이
**한 트랜잭션**에서 일어납니다. 두 관리자를 동시에 강등·삭제하려 해도 하나만 통과하고 나머지는
409 입니다 — 예전에는 둘 다 세고 둘 다 바꿔 **관리자 0명**이 될 수 있었고, 그때 두 응답 모두 200
이라 화면·감사 로그 어디에도 사고로 보이지 않았습니다.

```bash
# 관리자 2명(a1·a2)일 때 두 요청을 동시에 보냅니다
curl -s -X POST -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"username":"a1"}' $B/api/admin/users/delete &
curl -s -X POST -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"username":"a2"}' $B/api/admin/users/delete &
wait
# → {"ok":true,"username":"a1","sessionsRevoked":true,"keysRevoked":0}
# → {"error":"마지막 관리자는 강등·삭제할 수 없습니다 — 먼저 다른 관리자를 만드세요"}
#   남은 관리자 수: 1   (어느 쪽이 통과할지는 경쟁이지만, 남는 수는 항상 1 입니다)
```

> **잠겼다면.** 마지막 관리자 보호를 API 로 우회할 방법은 없습니다(그것이 목적입니다) — 순차든
> 동시든 마지막 한 명은 남습니다. 그래도 잠겼다면(예: DB 를 직접 손댔거나 마이그레이션 사고)
> 서버 호스트에서 §2 방법 B(`user add`)로 관리자를 하나 더 만드세요 — DB 접근 자체가 권한입니다.
> 그 명령도 비밀번호 최소 8자를 강제합니다(복구 자리에 약한 관리자 비밀번호가 들어가면 안 됩니다):
>
> ```
> $ usage-server user add -tenant default -username okcli -role admin -password a
> user add 실패: store: 비밀번호는 최소 8자여야 합니다        # rc=1
> $ usage-server user add -tenant default -username okcli -role admin -password eight888
> 사용자 생성됨: tenant=default username=okcli role=admin      # rc=0
> ```

### 9-4. 셀프서비스 인제스트 키 (`member` 포함)

로그인한 사람은 누구나 **자기** 키를 발급·조회·해지할 수 있습니다. 남의 키는 보이지도, 해지되지도
않습니다.

| 메서드 | 경로 | 결과 |
|---|---|---|
| `POST` | `/api/me/keys` | 자기 이름에 묶인 키 발급 — **평문은 이 응답에서 1회만** |
| `GET` | `/api/me/keys` | 자기 키 목록(마스크만) |
| `POST` | `/api/me/keys/revoke` | `{"id":"…"}` → 204 |

```bash
curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"username":"amy","password":"amy-password-2"}' $B/api/auth/login -c /tmp/amy.ck

curl -s -X POST -b /tmp/amy.ck $B/api/me/keys
# → {"key":"uu_ing_176c7b32…","id":"195cbbae4c41…","createdAt":"2026-08-11T04:42:11Z","username":"amy"}

curl -s -b /tmp/amy.ck $B/api/me/keys
# → {"keys":[{"id":"195cbbae4c41…","masked":"uu_ing_…e358","createdAt":"…","revokedAt":null,"username":"amy"}]}

curl -s -o /dev/null -w '%{http_code}\n' -X POST -b /tmp/amy.ck \
  -H 'Content-Type: application/json' -d '{"id":"195cbbae4c41…"}' $B/api/me/keys/revoke
# → 204
```

경계도 실측했습니다:

```bash
# 남의 키 해지 시도와 없는 키는 **같은 404·같은 문구**입니다 —
# 갈라 주면 그 차이가 곧 "그 키는 존재한다"는 신호가 됩니다.
curl -s -w ' [%{http_code}]\n' -X POST -b /tmp/bob.ck \
  -H 'Content-Type: application/json' -d '{"id":"<amy 의 키 id>"}' $B/api/me/keys/revoke
# → {"error":"키를 찾을 수 없습니다"} [404]
curl -s -w ' [%{http_code}]\n' -X POST -b /tmp/bob.ck \
  -H 'Content-Type: application/json' -d '{"id":"nosuchkey"}' $B/api/me/keys/revoke
# → {"error":"키를 찾을 수 없습니다"} [404]

# 남의 목록에는 아예 안 나옵니다
curl -s -b /tmp/bob.ck $B/api/me/keys
# → {"keys":[]}
```

두 가지 자격 규칙이 더 있습니다:

- **개인 열람 토큰(`Bearer uu_mem_…`)으로는 발급·해지가 안 됩니다(403).** 이름 그대로 조회
  자격이고, 거기에 키 발급 권한을 얹으면 나눠 준 조회 토큰이 보고 자격을 찍어 냅니다. 조회
  (`GET /api/me/keys`)는 됩니다.
- **`USAGE_ADMIN_TOKEN` 으로도 안 됩니다(403).** 그 토큰에는 사람 신원이 없어 "누구의 키"인지
  정할 수 없습니다. 관리자가 대신 발급하려면 §3-1 의 `{"username":"…"}` 대리발급을 쓰세요.

### 9-5. 스키마 (`ingest_keys.username`)

| 방언 | 소유자 |
|---|---|
| PostgreSQL | [`migrations/pg/0038_ingest_key_user.sql`](../migrations/pg/0038_ingest_key_user.sql) |
| sqlite | `go/internal/org/org.go` 의 `Init`(멱등 DDL + 컬럼 보강) |

```bash
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/pg/0038_ingest_key_user.sql
# → BEGIN / ALTER TABLE / CREATE INDEX / COMMENT / COMMIT
# 두 번째 실행은 NOTICE("already exists, skipping") 뒤 같은 결과 — 멱등입니다.
```

`nullable` 이 하위호환의 전부입니다. **기존 행을 한 줄도 건드리지 않습니다** — `NOT NULL` 이나
`DEFAULT` 를 걸면 이미 배포된 모든 키의 귀속이 그날로 바뀝니다.

되돌리기:

```sql
ALTER TABLE ingest_keys DROP COLUMN IF EXISTS username;
-- 결속이 사라지고 귀속은 ②→③ 으로 돌아갑니다(잃는 것은 결속 정보뿐입니다).
```

> sqlite 는 `CREATE TABLE IF NOT EXISTS` 가 **기존 표에 컬럼을 넣지 않습니다.** 그래서
> `org.Init` 이 `PRAGMA table_info` 로 확인하고 `ALTER TABLE ADD COLUMN` 을 겁니다 —
> 이 보강이 없으면 옛 DB 로 뜬 서버에서 키 해석 질의가 통째로 실패하고, 증상은
> **전 팀원의 보고가 401** 입니다. 원인이 인증처럼 보이는 자리라 특히 조심할 곳입니다.

### 9-6. 감사 로그

사용자 생성·역할 변경·비밀번호 재설정·팀 배정·삭제와 키 발급·해지는 전부 `usage_audit` 에
남습니다(`identity.AuditLog`). `actor` 는 로그인 사용자명이고, 관리자 토큰처럼 사람 신원이 없는
자격이면 `usage-admin` 입니다.

| action | target |
|---|---|
| `admin.user.create` · `admin.user.role` · `admin.user.password` · `admin.user.team` · `admin.user.delete` | username |
| `admin.key.issue` · `admin.key.revoke` | key id(=key_hash) |
| `me.key.issue` · `me.key.revoke` | key id |

**비밀번호는 어디에도 남지 않습니다** — `detail` 에는 `role`·`team`·`sessionsRevoked` 만 들어갑니다.
