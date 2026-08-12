---
type: package
tags: [go, 귀속, 감사]
updated: 2026-08-12
sources: ["go/internal/identity/", "go/CONTRACT.md", "docs/OPERATIONS.md"]
---

# `internal/identity` — 머신 → 계정 귀속과 감사

귀속 우선순위 ②(`machine → username` 매핑)와 **모든 관리 작업의 감사 로그**를 담당한다.
개념은 [[attribution]] 이 소유한다.

```go
func Init(ctx, d db.DB) error
func Resolve(ctx, machine, claimed string) (string, error)
func List(ctx) ([]Mapping, error)
func Set(ctx, in SetInput) (SetResult, error)   // 과거 행 소급 재스탬프 포함
func Remove(ctx, machine string) (bool, error)
func Unmapped(ctx) ([]string, error)
func AuditLog(...)                              // audit.go
```

## 계약

- **빈 username 은 거부한다** — 실수로 귀속을 지우지 못하게. 테스트가 그 계약을 잡고 있다.
- `Set` 은 **과거 행을 소급 재스탬프**한다. 어제 보던 이름이 오늘 바뀌므로 감사에 남긴다.
- `Unmapped` 는 결정적 정렬 타이브레이크를 갖는다 — Go 맵 순회가 실행마다 달라 골든이
  흔들렸기 때문 → [[node-to-go-port]].

## ⚠ restamp 가 `usage_series` 를 건드리지 않는다

`usage_sessions`·`usage_counters` 만 재스탬프한다. 그래서 **세션은 새 이름인데 그 세션의
버킷은 옛 OS 계정명을 지닌** 행이 남는다.

실재하는 드리프트이고, [[cleanup]] 의 `usage-rows` 가 이것을 알고 설계돼 있다 — 자식 표를
이름이 아니라 **세션 소유 + 고아 잔여** 두 조건으로 고른다. 이름으로만 좁히면 그 버킷이
살아남아 **"지웠는데 화면에 옛 이름이 있다"** 가 된다.

## API

`GET` · `PUT` · `DELETE` `/api/usage/identity` (관리자 자격).

readOnly(remote) 모드에서는 **등록되지 않는다** — 귀속 교정은 쓰기다 → [[tenancy-rls]].

## 감사 로그 (`audit.go`)

`usage_audit` 표. **기한이 없다** — *"어제 보던 이름이 왜 오늘 다른가에 답하는 표"* 라
기한을 두면 그 답이 먼저 사라진다 → [[data-policy]].

| action | target |
|---|---|
| `usage.identity.set` | machine |
| `admin.user.{create,role,password,team,delete}` | username |
| `admin.key.{issue,revoke}` · `me.key.{issue,revoke}` | key id (= key_hash) |

`actor` 는 로그인 사용자명. 관리자 토큰처럼 **사람 신원이 없는 자격**이면 `usage-admin`.

**비밀번호는 어디에도 남지 않는다** — `detail` 에는 `role`·`team`·`sessionsRevoked` 만.

### ⚠ pg 스키마가 없다

`usage_audit` 은 `migrations/pg/` 어느 파일도 만들지 않는다. **sqlite 쪽 DDL(`audit.go`)만**
갖고 있다.

그래서 [[cleanup]] 의 `usage-rows` 가 감사 기록을 남기지 않는다 — 남기면 local 에는 남고
remote 에는 **조용히 남지 않는다.** *반쪽만 있는 감사 기록은 없는 것보다 나쁘다(있다고 믿게
만든다).* 대신 "출력을 보관하라"고 말한다 → [[risks]].

## 삭제할 때 감사를 남기는 이유

[[cleanup]] 의 `usage-rows` 는 `usage_audit` 을 **남긴다**(지우지 않는다). 삭제의 부수효과로
그 근거를 함께 지우면 **방금 지운 이유를 나중에 아무도 답할 수 없다** — 특히 그 명령의 직전
단계가 보통 귀속 교정이라, 기록이 사라지면 "왜 이 사람 행이 저 이름으로 합쳐졌었나"가
미궁이 된다.

> ⚠ 그래서 **법적 삭제 요구에는 이것으로 충분하지 않다.** `target`·`detail` 에 머신명과
> 계정명이 남는다. 별도의 명시적 결정으로 지운다 → [[cleanup]].

## 관련

[[attribution]] · [[go-org]] · [[cleanup]] · [[data-policy]] · [[user-management]]
