---
type: operation
tags: [운영, 기동]
updated: 2026-08-12
sources: ["docs/OPERATIONS.md", "README.md", "deploy/README.md"]
---

# 운영 런북 — 요약

> **단일 출처는 `docs/OPERATIONS.md` 다.** 이 페이지는 지도이지 사본이 아니다 — 명령어 원문을
> 복제해 두 벌로 만들지 않는다(한쪽만 고쳐지는 날이 온다). 실행할 때는 원문을 보라.
>
> `docs/OPERATIONS.md` 는 **"여기 적힌 명령은 전부 실제로 실행해 출력을 확인한 것"** 이라고
> 스스로 밝히고, 검증하지 못한 절차는 "미검증"이라 표시한다.

## 절 지도

| 절 | 무엇 | 위키 |
|---|---|---|
| §1 | 서버 기동 · 부팅 거부 · pg/멀티테넌트 | [[boot-gates]] · [[tenancy-rls]] |
| §2 | 최초 관리자 만들기 (env / CLI) | 아래 |
| §3 | 인제스트 키 — 발급·확인·해지·회전·스코프 | [[ingest-keys]] |
| §4 | 개발자 머신 연동 (설치·제거) | [[installer]] |
| §5 | 문제 해결 (401/403/데이터없음/브라우저/비용/pg) | [[troubleshooting]] |
| §6 | 되돌리기 (머신 쪽 · 서버 쪽) | 아래 |
| §7 | 정기 점검 · CI 게이트 | 아래 · [[ci-gates]] |
| §8 | 저장 데이터 정리 (`cleanup`) | [[cleanup]] |
| §9 | 사용자 관리 · 셀프서비스 키 | [[user-management]] |

## 기동 (요약)

```bash
bash scripts/build.sh                # 유일 빌드 경로
USAGE_ADMIN_TOKEN=$(openssl rand -hex 24) \
USAGE_INTAKE_TOKEN=$(openssl rand -hex 24) \
USAGE_DATA_DIR=./data USAGE_PORT=4191 ./go/usage-server
```

**기동 로그의 세 줄을 반드시 읽는다:**

1. `· 인테이크 자격:` — `USAGE_ADMIN_TOKEN 겸용` 이면 수집기에 배포하는 토큰이 곧 전원 열람
   토큰이다
2. `⚠ tenant=… 에 사용자가 없다` — 화면은 뜨는데 로그인만 401 인 상태
3. (pg) `· DB 롤 확인 —` — 이 줄이 없으면 **격리가 검증되지 않은 채 뜬 것**

→ [[boot-gates]]

## 최초 관리자 (§2) — 두 방법, 둘 다 검증됨

| | 방법 | 성질 |
|---|---|---|
| A | `USAGE_BOOTSTRAP_ADMIN_USER` + `_PASSWORD` 로 기동 | **멱등** — 대상 tenant 에 사용자가 하나라도 있으면 아무것도 안 한다. 그래서 **비밀번호를 바꿀 수 없다**. 컨테이너 배포에 적합 |
| B | `usage-server user add -tenant … -username … -role admin` | 프롬프트 권장(`-password` 는 셸 히스토리에 남는다) |

비밀번호는 **최소 8자(룬 수)** — API 와 같은 규칙이고 CLI 도 예외가 아니다.

## 되돌리기 (§6)

### 개발자 머신

**권장은 `--uninstall`** ([[installer]]). `.bak` 복구는 실행 횟수에 따라 틀릴 수 있다 —
`.bak` 은 "최초 원본"이 아니라 **"직전 상태"** 다.

### 서버

| 상황 | 수단 |
|---|---|
| 키를 잘못 뿌렸다 | `key revoke --key …` → 즉시 401 |
| 배포를 되돌린다 | 이전 이미지 태그로 롤백. 앱은 스키마를 자동으로 안 바꾸므로 **앱만 되돌리는 것이 안전** |
| **마이그레이션을 되돌린다** | **자동 경로가 없다.** down 마이그레이션 없음 → **스냅샷/PITR 이 유일** |
| 데이터를 지운다 | 보존 정리기는 `keyword` 만. 나머지는 [[cleanup]] |

## 정기 점검 (§7)

| 주기 | 확인 |
|---|---|
| 주 1회 | 보고가 끊긴 머신 — 커버리지 화면의 머신별 마지막 보고 |
| 주 1회 | `unpriced` 모델이 늘었나 — 늘었으면 `config.json` 에 단가 추가 → [[cost-model]] |
| 배포마다 | 인테이크 자격이 분리돼 있나 — 기동 로그 |
| 배포마다 | (pg) RLS 롤 확인이 통과했나 — 기동 로그 |
| **퇴사·이탈 시** | 그 사람 키 해지 → [[user-management]] 의 순서를 지킬 것 |

## 개발자용 게이트

```bash
cd go        && go test ./... && go vet ./...   # 12 패키지
cd collector && go test ./... && go vet ./...
cd web       && npm test
bash scripts/build.sh                            # 유일 빌드 경로
npm run contract:verify -- --base http://127.0.0.1:8080   # 골든 44
```

→ [[golden-contract]] · [[ci-gates]]

## 관련

[[boot-gates]] · [[ingest-keys]] · [[installer]] · [[troubleshooting]] · [[cleanup]] ·
[[user-management]] · [[deploy-aws]]
