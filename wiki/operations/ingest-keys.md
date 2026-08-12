---
type: operation
tags: [운영, 인제스트키, 온보딩]
updated: 2026-08-12
sources: ["docs/OPERATIONS.md", "README.md", "go/internal/org/org.go"]
---

# 인제스트 키 — 발급 · 확인 · 해지 · 회전

키는 개발자 머신이 **보고할 때만** 쓰는 자격이다. 열람은 못 한다(403).
성질은 [[go-org]], 귀속과의 관계는 [[attribution]]. 절차 원문은 `docs/OPERATIONS.md` §3·§9.

## 발급 경로 셋

| 경로 | 누가 | 무엇 |
|---|---|---|
| **대시보드 "연동" 탭** | 관리자 + **member 본인** | 권장. 원라인까지 복사해 준다 |
| admin API | 관리자 | `POST /api/admin/keys` (`{"username":"amy"}` 로 대리발급) |
| 셀프서비스 API | 로그인한 누구나 | `POST /api/me/keys` — 자기 이름에 묶인 키 |
| CLI | 운영자(서버 호스트) | `usage-server key issue --org … [--user …]` |

**평문은 발급 응답에서 1회만** 보인다. 목록에는 마스킹된 값만 남는다.

## 키를 사람에게 묶기 (`--user` / `{"username":…}`)

그 키로 들어온 보고가 **키 주인으로 귀속**된다(우선순위 ①). 생략하면 **종전과 완전히 같은
org 공용 키**이므로 지금 쓰는 명령·스크립트는 그대로 둬도 된다.

묶을 사람은 **그 org 의 tenant 에 실재하는 계정**이어야 한다(`org create` 는 org id 를 그대로
tenant id 로 쓴다).

**없는 사용자에 묶으려 하면 거부한다**(exit 1, 키를 만들지 않는다):

```
key issue: 사용자 "amyy" 가 tenant "org_…" 에 없다 — 오타이거나 계정이 아직 없다.
  (없는 이름에 묶은 키의 보고는 영영 아무에게도 귀속되지 않는다)
```

→ [[attribution]] 이 이 결정의 근거를 담는다.

## 스코프 (실측)

| 요청 | 인제스트 키 | 해지된 키 | 관리자 토큰 |
|---|---|---|---|
| `POST /api/usage` | **200** | **401** | 200 |
| `GET /api/agent/collector` | **200** | **401** | 200 |
| `GET /api/usage/summary` | **403** | **401** | **200** |

키 없이 다운로드 시도, 위조 키(`uu_ing_bogus`) 둘 다 **401**.

**보고 전용인 이유:** 팀원 수만큼 복제되어 각자의 디스크에 놓이므로, 열람까지 겸하면
**사본 하나가 곧 팀 전체의 노출**이 된다 → [[auth-scopes]].

## 회전

**전용 회전 명령이 없다.** 순서가 정해져 있다:

```
새 키 발급  →  배포  →  옛 키 해지
```

반대로 하면 해지와 재설치 사이에 보고가 끊긴다. **데이터가 사라지지는 않는다** — 수집기가
증분 체크포인트를 갖고 있어 다음 성공 실행이 밀린 세션을 함께 올린다 → [[idempotency]].

## 셀프서비스 (`/api/me/keys`)

로그인한 사람은 누구나 **자기** 키를 발급·조회·해지할 수 있다. 남의 키는 **보이지도, 해지되지도**
않는다.

| 메서드 | 경로 |
|---|---|
| `POST` | `/api/me/keys` — 평문 1회 |
| `GET` | `/api/me/keys` — 마스크만 |
| `POST` | `/api/me/keys/revoke` → 204 |

두 가지 자격 규칙이 더 있다:

- **개인 열람 토큰(`uu_mem_…`)으로는 발급·해지가 안 된다(403).** 이름 그대로 조회 자격이고,
  거기에 발급 권한을 얹으면 **나눠 준 조회 토큰이 보고 자격을 찍어 낸다.** 조회는 된다.
- **`USAGE_ADMIN_TOKEN` 으로도 안 된다(403).** 그 토큰에는 **사람 신원이 없어** "누구의 키"인지
  정할 수 없다. 관리자가 대신 발급하려면 `{"username":"…"}` 대리발급을 쓴다.

**남의 키 해지와 없는 키는 같은 404·같은 문구**다 — 갈라 주면 그 차이가 곧 "그 키는
존재한다"는 신호가 된다.

## CLI (서버 호스트)

```bash
usage-server org create --name "Acme"          # → id=org_… tenant=org_…
usage-server key issue --org org_… [--user amy]  # → uu_ing_… (평문 1회)
usage-server org list
usage-server key revoke --key uu_ing_…         # → 해지됨
```

관리자 토큰 게이트가 없는 이유는 **DB 접근 자체가 권한**이기 때문이다.

## 퇴사 처리

**순서가 있다** — §9 먼저, [[cleanup]] 이 나중:

1. `POST /api/admin/users/delete` — 계정을 지우고 **세션·결속 인제스트 키를 함께 거둔다**
   (`keysRevoked` 로 확인) → [[user-management]]
2. `cleanup usage-rows` — 이미 쌓인 사용량 행을 지운다

거꾸로 하면 1과 2 사이에 훅이 한 번 더 돌아 **방금 지운 사람의 행이 다시 생긴다.**

⚠ 연동 제거(`--uninstall`)만으로는 **서버의 키가 계속 살아 있다.** 유출된 키는 다른 곳에서
그대로 쓴다 → [[installer]].

## 관련

[[go-org]] · [[attribution]] · [[auth-scopes]] · [[installer]] · [[user-management]] · [[cleanup]]
