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
  · 브라우저에서 열고 토큰을 입력하면 두 탭(사용 추적·사용 관측)이 뜬다.
  · 단가표: config.json (없으면 시드 단가표를 쓴다)
  · 인테이크 자격: USAGE_INTAKE_TOKEN(보고 전용 — 조회 불가)
```

> 위 블록은 서버가 실제로 찍는 문구 그대로입니다. `두 탭(사용 추적·사용 관측)` 은 **기동
> 메시지가 낡은 것**이고, 실제 화면 탭은 다섯입니다 — 대시보드 · 사용 추적 · 사용 관측 ·
> 연동 · 아키텍처.

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
| `POST` | `/api/admin/keys` | `{"key":"uu_ing_…","id":…,"createdAt":…}` — **평문은 이 응답에서 1회만** |
| `GET` | `/api/admin/keys` | 목록(마스크만, 평문 없음) |
| `POST` | `/api/admin/keys/revoke` | `{"id":"…"}` → 204 |

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
