---
type: operation
tags: [운영, 삭제, 되돌릴수없음]
updated: 2026-08-12
sources: ["docs/OPERATIONS.md", "go/cmd/usage-server/maintenance.go", "migrations/pg/0037_placeholder_model_cleanup.sql"]
---

# `cleanup` — 저장된 데이터 정리

`usage-server cleanup` 아래 두 명령. **둘 다 기본값이 dry-run 이고, 자동 실행 경로에 올라가
있지 않다** — 되돌리기 어려운 작업은 사람이 대상을 지목해 명시적으로 돌린다.

| 명령 | 무엇 | 숫자가 움직이나 | 되돌릴 수 있나 |
|---|---|---|---|
| `cleanup placeholder-models` | 자리표시자 모델 라벨(`<synthetic>` 등)을 `(미상)` 으로 접는다 | **아니오** — 라벨만 | 아니오 |
| `cleanup usage-rows` | 지목한 사용자·머신의 **사용량 행을 지운다** | 예 — 줄어든다 | **아니오** |

둘 다 서버와 **같은 env** 를 본다. 관리자 토큰 게이트가 없는 이유는 **DB 접근 자체가 권한**
이기 때문이다.

---

## ① `placeholder-models`

### 언제

모델 축(비용·모델별 화면)에 **`<synthetic>` 같은 행이 보일 때.** Claude Code 는 중단·오류
메시지 같은 턴에 모델 이름 대신 그 자리표시자를 쓴다.

인테이크는 **이제 그 값을 접는다**([[go-intake]] 의 `normModel`). 앞으로 들어오는 보고에는
생기지 않는다. **수정 이전에 저장된 행**만 이 명령의 대상이다 — **한 번만 돌리면 된다.**

### 숫자는 움직이지 않는다

자리표시자 턴의 토큰은 전부 0 이다. 세션·버킷·턴·품질 카운터를 하나도 버리지 않고
**모델 라벨만** 바꾼다.

실측 — 정리 전후 `/api/usage/quality` 응답이 **바이트 동일**했고,
`①+②+③ == Totals` 불변식도 전후 모두 성립했다 → [[model-three-paths]].

### 절차 (local)

```bash
./go/usage-server cleanup placeholder-models            # ① dry-run — 계획 확인
./go/usage-server cleanup placeholder-models --apply    # ② 적용
./go/usage-server cleanup placeholder-models            # ③ 멱등 확인 → "정리할 행이 없다"
```

서버를 멈추지 않아도 된다(같은 sqlite 파일을 열 뿐).

### 합산 병합 — `usage_series` 의 PK 충돌

`model` 이 PK 의 일부라 라벨만 바꾸면 같은 시각의 기존 버킷과 충돌한다. 컬럼마다 결합이 다르다:

| 컬럼 | 결합 |
|---|---|
| 토큰 4축 · `cc_5m`·`cc_1h`·`*_long` | 합 |
| `turns`·`tool_errors`·`stop_*`·`latency_ms_sum`·`latency_turns` | 합 |
| `latency_ms_max` | **`MAX`** (합이 아니다) |
| `username`·`machine`·`project` | 기존 값 유지(비었을 때만 채운다) |

### `a<b>c` 는 건드리지 않는다

자리표시자는 **꺾쇠로 감싼 값 전체(`<…>`)** 만이다. 꺾쇠가 일부만 있는 값은 실제 모델명일 수
있다. 판정 규칙의 단일 출처는 `go/internal/intake/intake.go` 의 `placeholderModelRe`.

### remote (pg) — 두 경로

**A. 마이그레이션** `migrations/pg/0037_placeholder_model_cleanup.sql` (마스터 롤 = 표 소유자)

⚠ **테이블 잠금이 걸린다.** 트랜잭션 안에서 `FORCE ROW LEVEL SECURITY` 를 잠시 풀고(끝에서
되돌린다) ACCESS EXCLUSIVE 락을 잡는다 — 그동안 **인테이크가 대기한다. 트래픽이 낮을 때
돌려라.**

> FORCE 를 푸는 이유: 마이그레이션은 `app.tenant_id` GUC 없이 도는데, 안 풀면 소유자에게도
> 한 행이 보이지 않아 **오류 없이 0행을 고치고 끝난다**(조용한 미적용) → [[tenancy-rls]].

⚠ `ALTER TABLE` 이 실패하면 그 롤이 표 소유자가 아니다. **앱 롤로 돌리지 마라.**

**B. CLI** — 앱 롤로 붙어도 되고 테이블 잠금도 안 건다. 대신 RLS 가 한 번에 한 테넌트만
보여 주므로 **테넌트마다 한 번씩**:

```bash
USAGE_DB_MODE=remote DATABASE_URL='…' \
  ./go/usage-server cleanup placeholder-models --tenant <tenant-id> --apply
```

---

## ② `usage-rows` — 되돌릴 수 없다

### 언제

**퇴사·삭제 요청에 답할 때.** [[user-management]] 의 사용자 삭제는 계정과 인제스트 키를
거두지만 **그 사람의 사용량 행은 남긴다** — 그래서 삭제 뒤에도 화면에 계정명·머신명·
프로젝트명이 계속 보인다.

`README.md` 의 데이터 정책이 예상해 둔 **(b) 해당 행을 직접 지우는** 길이다
→ [[data-policy]]. **보존 정리기가 아니다** — 기한으로 자동 삭제하는 경로를 만들지 않았고,
부팅·`store.Init` 어디에도 걸려 있지 않다.

```
usage-server cleanup usage-rows (--user <u> | --machine <m>) [--apply] [--tenant <t>]
```

대상은 **정확히 하나**다. 없거나 둘 다면 거부한다(exit 2) — **빈 대상을 통과시키면 그 순간
"전부 삭제"가 되고, 그것이 기본 동작인 명령은 언젠가 반드시 사고를 낸다.**

### 왜 머신 축(`--machine`)도 있나

계정이 붙지 않은 보고가 실재한다. 수집기가 `user` 를 보내지 않고 머신 매핑도 없으면
`username` 이 NULL 로 저장되는데, 그 행에는 **`--user` 로 잡을 손잡이가 없다.**
공용·반납·폐기 PC 가 그 경우다.

### 무엇을 지우고 무엇을 남기나

| 표 | `--user` | `--machine` |
|---|---|---|
| `usage_sessions` · `usage_series` · `usage_counters` | 지움 | 지움 |
| `usage_recommendations` | 지움 | **건너뜀** — `username` 만 있고 `session_id`·`machine` 이 없다. 조용히 0행으로 접지 않고 **건너뛴 이유를 찍는다** |
| `machine_identity` | 지움 | 지움 |
| **`usage_audit`** | **남김** | **남김** |
| `auth_users`·`auth_sessions`·`member_tokens`·`team_members`·`ingest_keys` | **남김** | **남김** |

**`usage_audit` 을 남기는 이유:** 삭제의 부수효과로 그 근거를 함께 지우면 **방금 지운 이유를
나중에 아무도 답할 수 없다** — 특히 직전 단계가 보통 귀속 교정이라 "왜 이 사람 행이 저
이름으로 합쳐졌었나"가 미궁이 된다 → [[go-identity]].

**계정·자격 표를 남기는 이유:** 그 회수는 **사용자 관리 API 가 소유한다.** 사용량 행을 지우는
명령이 계정까지 지우면 같은 표에 소유자가 둘이 되고, 그때는 어느 쪽이 지웠는지 알 수 없다.

> ⚠ **법적 삭제 요구에는 부족하다.** 감사 로그의 `target`·`detail` 에 머신명·계정명이 남는다.
> 별도의 명시적 결정으로:
> ```sql
> DELETE FROM usage_audit WHERE target = 'pc-leaver' OR detail LIKE '%"leaver"%';
> ```

### 무엇을 근거로 행을 고르나 (빠뜨리지 않는 이유)

자식 표는 **두 조건의 합**으로 고른다:

- **세션 소유** — 대상 세션에 딸린 행. 자식 표의 `username` 이 **낡아 있어도** 빠뜨리지 않는다.
  실재하는 드리프트다: 귀속 교정은 `usage_series` 를 재스탬프하지 않으므로, 세션은 새 이름인데
  버킷은 옛 이름을 지닌 행이 남는다. 이름으로만 좁히면 **"지웠는데 화면에 옛 이름이 있다"**.
- **고아 잔여** — 세션 행이 **없는데** 대상 이름을 지닌 행. 인테이크가 세션 행만 실패하고
  버킷은 들어가는 자리가 실재한다. **고아로 한정한다** — 안 하면 이름만 낡은 행 하나 때문에
  **다른 사람의 살아 있는 세션**에서 버킷을 뽑아 간다.

출력에 `(세션 소유 N · 고아 잔여 M)` 으로 갈라 찍는다.

> 그래서 **낡은 이름 자체를 정리하는 수단은 이 명령이 아니다.** 그 경우는 **(a) 귀속 교정**
> → [[attribution]].

### 한 트랜잭션이다

표 다섯을 지우다 중간에 끊기면 "세션은 없는데 카운터가 남은" 상태가 되고, **고아 버킷은
모델별 집계에서 아예 빠지므로 숫자만 보고는 알아챌 수 없다.**
`maintenance_test.go` 의 `TestPurgeIsOneTransaction` 이 결함을 주입해 롤백을 못박는다.

### 순서

1. `--apply` 없이 먼저 돌려 표별 행 수를 확인 — **이 한 번이 유일한 확인 절차다**
2. pg 는 **적용 전에 백업/스냅샷을 확인**
3. `--apply`
4. **출력을 보관한다** — 이 명령은 감사 표에 기록을 남기지 않으므로, 그 출력이 "무엇을 얼마나
   지웠나"의 **유일한 기록**이다

> **왜 감사 표에 기록하지 않나.** `usage_audit` 은 **pg 스키마가 없다.** 여기서 기록을 남기면
> local 에는 남고 remote 에는 **조용히 남지 않는다.** *반쪽만 있는 감사 기록은 없는 것보다
> 나쁘다(있다고 믿게 만든다).* → [[risks]]

### 되돌리기

**없다.** 지운 행의 내용을 어딘가 남겨 두면 그게 곧 "지우지 않은 것"이기 때문이다.
**스냅샷/PITR 복구가 유일한 수단.**

> ⚠ **미검증:** remote 절차는 sqlite 로만 왕복했다. SQL 은 방언 중립이고 격리는 RLS 가
> 담당하지만 **pg 실측은 하지 않았다**고 원천이 밝힌다 → [[risks]].

## 관련

[[data-policy]] · [[user-management]] · [[attribution]] · [[go-identity]] · [[tenancy-rls]]
