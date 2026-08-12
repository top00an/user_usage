---
type: concept
tags: [인증, 권한, 보안]
updated: 2026-08-12
sources: ["README.md", "go/CONTRACT.md", "docs/OPERATIONS.md", "go/internal/httpapi/auth.go"]
---

# 자격과 스코프 — 무엇이 무엇을 여나

인증은 `Authorization: Bearer <토큰>` 또는 쿠키 `usage_tok`.

## 자격 다섯

| 자격 | 여는 것 | 누가 갖나 |
|---|---|---|
| `USAGE_ADMIN_TOKEN` | 전부 | 대시보드를 여는 사람(레거시 경로) |
| `USAGE_INTAKE_TOKEN` | `POST /api/usage` **만** (그 외 403) | 팀원 PC 의 수집기(단일테넌트) |
| org 인제스트 키 `uu_ing_…` | `POST /api/usage` + 수집기 내려받기 | 원커맨드로 연동한 개발자 머신 |
| 개인 열람 토큰 `uu_mem_…` | 자기 데이터 조회 (**키 발급·해지는 403**) | member |
| 로그인 세션 쿠키 | 역할에 따라 | 사람 |

## 규칙 다섯 — 전부 골든의 오류 스냅샷이 잡는다

1. **상수시간 비교.** 양쪽을 프로세스마다 무작위인 키로 HMAC-SHA256 접은 뒤 `hmac.Equal`.
   길이가 달라도 타이밍으로 새지 않게.
2. **`Authorization` 이 있는데 틀렸으면 쿠키로 흘리지 않는다.** 폴백이 있으면 게이트가 흐려진다.
3. **인테이크 토큰은 쿠키로 인정하지 않는다.** 그 보고자는 수집기이지 브라우저가 아니고,
   쿠키로 받아 주면 브라우저를 꾀어 임의 사용량을 밀어 넣는 자리가 생긴다.
4. **쿠키 자격증명으로는 상태변경을 태우지 않는다(403).** 브라우저는 임의 헤더를 붙일 수
   없으므로 화면은 자연히 조회 전용이 되고 **CSRF 표면이 아예 생기지 않는다.**
5. **인테이크 스코프는 `POST /api/usage` 하나만 연다**(그 외 403).

## 인제스트 키 스코프 (실측)

| 요청 | 인제스트 키 | 해지된 키 | 관리자 토큰 |
|---|---|---|---|
| `POST /api/usage` | **200** | **401** | 200 |
| `GET /api/agent/collector` | **200** | **401** | 200(헤더로만) |
| `GET /api/usage/summary` | **403** | **401** | **200** |

키는 **보고 전용**이다. 팀원 수만큼 복제되어 각자의 디스크에 놓이므로, 열람까지 겸하면
**사본 하나가 곧 팀 전체의 노출**이 된다 → [[ingest-keys]].

## 라우트 순서는 계약이다

`analytics` 가 `admin` 보다 **앞**이어야 한다. admin 이 `/api/usage` 접두사를 통째로 소유하고
안 걸리면 404 를 직접 내므로, 뒤로 가면 **관측 화면이 통째로 404** 가 된다.

## readOnly(remote) 모드

인테이크를 **등록하지 않고**, admin 라우트도 GET/HEAD 만 통과시킨다. 나머지는
**405 가 아니라 404** 다 — 이 모드에서 그 엔드포인트는 "지금은 막혔다"가 아니라
**존재하지 않는다** → [[tenancy-rls]].

## 서버가 거부하는 것 (실측)

화면에서 버튼을 숨기는 것은 방어가 아니다. 아래는 **서버가** 내는 응답이다.

| 시도 | 응답 |
|---|---|
| 마지막 관리자 강등·삭제 | **409** `마지막 관리자는 강등·삭제할 수 없습니다` |
| 자기 자신 강등·삭제 | **409** `자기 자신은 강등할 수 없습니다` |
| member 가 `/api/admin/users` | **403** |
| 레거시 `usage_tok` 쿠키로 상태변경 | **403** `쿠키 인증으로는 상태변경을 할 수 없습니다` |
| 개인 열람 토큰으로 키 발급 | **403** — 조회 자격에 발급 권한을 얹으면 나눠 준 조회 토큰이 보고 자격을 찍어 낸다 |
| `USAGE_ADMIN_TOKEN` 으로 `/api/me/keys` 발급 | **403** — 그 토큰에는 **사람 신원이 없어** "누구의 키"인지 정할 수 없다 |
| 남의 키 해지 / 없는 키 | **404, 같은 문구** — 갈라 주면 그 차이가 곧 "그 키는 존재한다"는 신호가 된다 |
| 없는 사용자 / 중복 생성 / 8자 미만 비밀번호 | 404 / 409 / 400 |

**마지막 관리자 보호는 동시 요청에서도 선다.** 판정("지금 관리자가 몇 명인가")과 변경이
**한 트랜잭션**이다. 예전에는 둘 다 세고 둘 다 바꿔 **관리자 0명**이 될 수 있었고, 그때 두
응답 모두 200 이라 화면·감사 로그 어디에도 사고로 보이지 않았다 → [[user-management]].

## 엔드포인트 × 자격

| 메서드 | 경로 | 자격 |
|---|---|---|
| `GET` | `/healthz` | — (무인증·무DB) |
| `GET` | `/install.sh` | 무인증 (키가 인증을 대신) |
| `GET` | `/api/agent/collector?os=&arch=` | 인제스트 키 |
| `POST` | `/api/usage` | 인테이크 |
| `GET` | `/api/usage/{summary,series,distribution,sessions,quality,coverage,leaderboard,dispatch,platforms,seats,teams,dev}` | 열람 |
| `GET`·`PUT`·`DELETE` | `/api/usage/identity` | 관리자 |
| `POST`·`GET` | `/api/admin/keys` · `POST /api/admin/keys/revoke` | 관리자 |
| `GET`·`POST` | `/api/admin/users{,/role,/password,/team,/delete}` | 관리자 |
| `POST`·`GET` | `/api/me/keys` · `POST /api/me/keys/revoke` | 로그인 세션 |
| `POST` | `/api/auth/login` · `/api/auth/logout` | — |
| `GET` | `/api/auth/me` | 세션 |

`/api/usage/*` 조회는 `?platform=claude|codex|gemini|antigravity|other` 필터를 받는다.
**미지정이면 전체**, 목록 밖 값은 **400** — 오타를 `other` 로 접으면 요청한 것과 다른 모집단이
요청한 이름으로 조용히 돌아온다 → [[honest-uncertainty]].

## 오류 응답 규율

예상 못 한 예외의 **원문을 클라이언트로 보내지 않는다.** 대개 DB 드라이버 에러(테이블·컬럼명,
제약 이름, 때로는 접속 정보 조각)다. 원문은 stderr 로 — 그쪽은 운영자만 본다. 라우트가
의도해서 내는 400(검증 메시지)은 이 경로로 오지 않으므로 안내는 그대로 남는다.

## 관련

[[go-httpapi]] · [[ingest-keys]] · [[user-management]] · [[attribution]] · [[boot-gates]]
