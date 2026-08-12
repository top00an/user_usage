---
type: concept
tags: [귀속, 신원, 보안]
updated: 2026-08-12
sources: ["docs/OPERATIONS.md", "go/internal/identity/", "go/internal/org/org.go", "migrations/pg/0038_ingest_key_user.sql"]
---

# 귀속 — 이 보고는 누구 것인가

수집기는 OS 계정명을 `payload.user` 로, 호스트명을 `machine` 으로 보낸다. 서버가 그것을
그대로 믿을지 말지가 이 페이지의 주제다.

## 우선순위 셋

| | 근거 | 언제 이기나 |
|---|---|---|
| ① | **키에 묶인 username** (`ingest_keys.username`) | 있으면 **무조건** — "그 사용자의 키를 실제로 갖고 있음"이 증명된 사실이다 |
| ② | `machine → username` 매핑 (`machine_identity`) | 관리자가 손으로 고친 값(귀속 교정) |
| ③ | `payload.user` | 클라이언트 주장 — **최후** |

**①이 설 때 ②는 아예 조회되지 않는다.** 매핑을 조용히 덮어쓰는 것이 아니다.

## 왜 ①이 생겼나

**지금까지 귀속은 PC 가 주장하는 이름이었다.** 인제스트 키가 org 에만 묶여 있었으므로,
**키 사본을 가진 누구나 남의 이름으로 보고할 수 있었다.** 한 org 키가 팀원 수만큼 복제되어
각자 디스크에 놓이므로 누가 실제로 보고했는지 가릴 방법이 없었고, 화면은 그것을 사실로
표시했다.

키에 사람을 묶으면 그 구멍이 닫힌다. 실측:

```bash
# amy 에게 묶인 키로 user=mallory, machine=pc-mallory 를 주장
POST /api/usage  →  {"ok":true,...}
GET  /api/usage/sessions
# → {"sessionId":"sess-live-01","machine":"pc-mallory","username":"amy", ...
#                                                      ^^^^^^^^^^^^^^^^
```

`machine` 은 보고된 값 그대로 남고 **귀속만** 키 주인으로 간다 — 어느 PC 에서 왔는지는
여전히 보인다.

## 하위호환

지금 배포된 키는 전부 `username` 이 비어 있으므로 종전대로 ②→③ 을 탄다. **이 변경으로 기존
배포의 귀속은 하나도 바뀌지 않는다.** 사람에게 묶고 싶으면 새 키를 발급해 배포한다
([[ingest-keys]] 의 회전 절차).

스키마도 같은 규율이다 — `ingest_keys.username` 은 **nullable** 이고 그게 하위호환의 전부다.
`NOT NULL` 이나 `DEFAULT` 를 걸면 이미 배포된 모든 키의 귀속이 그날로 바뀐다.

| 방언 | 소유자 |
|---|---|
| PostgreSQL | `migrations/pg/0038_ingest_key_user.sql` |
| sqlite | `go/internal/org/org.go` 의 `Init`(멱등 DDL + 컬럼 보강) |

> ⚠ sqlite 는 `CREATE TABLE IF NOT EXISTS` 가 **기존 표에 컬럼을 넣지 않는다.** 그래서
> `org.Init` 이 `PRAGMA table_info` 로 확인하고 `ALTER TABLE ADD COLUMN` 을 건다. 이 보강이
> 없으면 옛 DB 로 뜬 서버에서 키 해석 질의가 통째로 실패하고, **증상은 전 팀원의 보고가 401**
> 이다. 원인이 인증처럼 보이는 자리라 특히 조심할 곳.

## 없는 사용자에게 묶으려 하면 거부한다

```
key issue: 사용자 "amyy" 가 tenant "org_…" 에 없다 — 오타이거나 계정이 아직 없다.
  (없는 이름에 묶은 키의 보고는 영영 아무에게도 귀속되지 않는다)
```

조용히 만들지 않는 이유: 평문 키는 다시 볼 수 없어 **잘못 발급했다는 사실을 알아채는 시점이
"그 사람 데이터가 화면에 안 보인다"** 뿐이다. 그때는 이미 오타 이름으로 쌓인 보고가 유령
사용자로 남고, 되돌리는 길은 해지 후 재발급밖에 없다.

## ② 귀속 교정 (`machine_identity`)

`GET`/`PUT`/`DELETE` `/api/usage/identity` (관리자). `go/internal/identity` 가 소유.

- **빈 username 은 거부한다** — 실수로 귀속을 지우지 못하게.
- **과거 행을 소급 재스탬프한다**(`Set` 이 포함). 어제 보던 이름이 오늘 바뀌므로 그 변경을
  `usage_audit` 에 남긴다.
- `Unmapped(ctx)` 가 아직 매핑되지 않은 머신 목록을 준다(관리 화면의 근거).

### ⚠ restamp 는 `usage_series` 를 건드리지 않는다

`usage_sessions`·`usage_counters` 만 재스탬프한다. 그래서 **세션은 새 이름인데 그 세션의
버킷은 옛 OS 계정명을 지닌** 행이 남는다. 실재하는 드리프트이고, [[cleanup]] 이 이것을
알고 설계돼 있다 → [[model-three-paths]].

## 감사

귀속 교정과 사용자·키 관리는 전부 `usage_audit` 에 남는다(`identity.AuditLog`).
`actor` 는 로그인 사용자명이고, 관리자 토큰처럼 사람 신원이 없는 자격이면 `usage-admin` 이다.

| action | target |
|---|---|
| `usage.identity.set` | machine |
| `admin.user.{create,role,password,team,delete}` | username |
| `admin.key.{issue,revoke}` · `me.key.{issue,revoke}` | key id (= key_hash) |

**비밀번호는 어디에도 남지 않는다** — `detail` 에는 `role`·`team`·`sessionsRevoked` 만.

## 관련

[[ingest-keys]] · [[go-identity]] · [[go-org]] · [[user-management]] · [[cleanup]] · [[auth-scopes]]
