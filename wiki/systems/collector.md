---
type: system
tags: [go, 수집기, 클라이언트]
updated: 2026-08-12
sources: ["collector/cmd/usage-collector/main.go", "collector/internal/", "README.md", "docs/PLAN-saas-ingestion.md"]
---

# usage-collector — 팀원 PC 에서 도는 수집기

네 플랫폼의 세션 기록을 훑어 `POST /api/usage` 로 **절대값**을 보고하는 단일 바이너리.
Go 별도 모듈(`collector/go.mod`)이라 서버의 `internal` 패키지를 import 할 수 없다.

## 흐름 넷

```
원천마다 세션 파일 탐색 → 증분(바뀐 세션만) 선별·파싱·매핑
                        → POST /api/usage (절대값)
                        → 보낸 세션의 지문을 체크포인트에 기록
```

재실행은 **언제나 안전하다.** 서버가 `session_id` 절대값으로 UPSERT 하므로 체크포인트를
지우고 전량을 다시 보내도 값이 부풀지 않는다 → [[idempotency]].

## 원천 넷과 플래그

| 플랫폼 | 방식 | 경로 | 플래그 |
|---|---|---|---|
| [[claude-code]] | 파일 | `~/.claude/projects/**/*.jsonl` | `-dir` |
| [[codex]] | 파일 | `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl` | `-codex-dir` |
| [[gemini-cli]] | 파일 | `~/.gemini/tmp/<슬러그>/chats/session-*.jsonl` | `-gemini-dir` |
| [[antigravity]] | **런타임 캡처** | 스풀 `~/.config/claude-usage/antigravity` | `-antigravity-dir` |

각 플래그에 `""` 를 주면 그 원천만 끈다. **디렉터리가 없으면 조용히 건너뛴다** — Claude 만
쓰는 팀원, Codex 만 쓰는 팀원 모두 아무 설정 없이 그대로 돌아야 하기 때문이다.

> 이 "조용히 건너뜀"이 테스트를 한 번 흔들었다. `collector/e2e_test.go` 에 `-gemini-dir ""`
> 가 빠져 있어 **실제 `~/.gemini` 세션이 있는 머신에서는 그 세션까지 올라가** 세션 수 단정이
> 흔들렸다(`docs/VERIFICATION.md` §2). 지금은 합성 픽스처만 읽도록 나머지를 전부 끈다.

## 패키지

```
collector/internal/
  transcript/   Claude Code 파서
  codex/        Codex 파서
  gemini/       Gemini CLI 파서
  antigravity/  스풀 · statusLine 캡처 · 상태줄 체이닝
  policy/       ★ 전송 전 값 좁히기 — 데이터 정책의 클라이언트 집행 지점(순수)
  payload/      인테이크 페이로드 조립
  sender/       HTTP 전송
  state/        증분 체크포인트
```

`main.go` 가 하는 일은 **배선과 설정뿐**이다. 판정 로직은 전부 internal 안에 있다.

## 체크포인트

두 원천이 **한 파일을 같이 쓴다.** 키가 파일 절대경로라 원천이 달라도 섞일 수 없다
(`~/.claude/...` 와 `~/.codex/...` 는 같은 키가 될 수 없다). 그래서 키에 플랫폼 접두사를
붙이지 않는다 — 붙이면 기존 체크포인트가 통째로 무효가 되어 전량 재전송이 한 번 더 일어날
뿐이고 얻는 것이 없다.

위치: `~/.claude/usage-collector-state.json`. 지워도 값이 부풀지 않는다(멱등) — 다음 실행이
한 번 오래 걸릴 뿐이다.

## 이중 방어 — `policy` 패키지

서버(`go/internal/intake`)도 같은 검사를 하는데 클라이언트에서 한 번 더 하는 이유가 둘이다
(패키지 주석이 단일 출처):

1. **네트워크에 나가지 않은 값은 되돌릴 수 있다.** 서버가 걸러도 그 값은 이미 팀원 PC 를
   떠나 TLS 종단·프록시 로그·서버 접근 로그를 지나간 뒤다. 유일하게 새지 않는 값은 애초에
   보내지 않은 값이다.
2. **서버가 우리보다 낡을 수 있다.** 서버는 수집기가 낡을 것을 걱정하고, 수집기는 서버가
   낡을 것을 걱정한다 — 양쪽이 각자 거르면 어느 쪽이 낡아도 시크릿은 안 나간다.

판정은 전부 **버리는 방향으로만** 작동한다 → [[data-policy]].

### 상수 드리프트 감시

별도 모듈이라 서버 상수를 import 할 수 없어 `policy.go` 가 값을 다시 적는다. 갈라지면
수집기가 **서버가 버릴 행**을 만들어 보낸다. `collector/internal/policy/drift_test.go` 가
서버 소스를 직접 읽어 감시한다.

| 상수 | 값 |
|---|---|
| `MaxSessions` | 50 (한 보고당) |
| `MaxSeriesPerSession` | 200 |
| `MaxCountersPerSession` | 400 |
| `PerKindMax` | 80 (한 축당) |
| `KeyMax` / `KeywordMax` | 120 / 40 |
| `CounterKinds` | tool · bash · slash · skill · agent · mcp · keyword |

## 실행

훅이 부른다(설치기가 등록 → [[installer]]). 손으로 돌릴 때:

```sh
USAGE_INTAKE_TOKEN="$(. ~/.config/claude-usage/config.env; printf %s "$KEY")" \
  ~/.local/bin/usage-collector -server "$SERVER"
```

토큰은 **argv 가 아니라 환경변수**로 넘긴다 — argv 는 `ps aux` 에 노출된다.

## 알려진 한계

- **Codex 의 압축 롤아웃** — 7일 지난 세션을 `.zst` 로 압축하는데 해제를 지원하지 않는다.
  `⚠ 압축된 롤아웃 N개를 건너뛴다` 를 찍고 건너뛴다. **그만큼 사용량이 빠진다.**
- **Antigravity 의 행동 축** — 그 도구가 기록하지 않아 올 수 없다 → [[platform-coverage]].

## 관련

[[installer]] · [[idempotency]] · [[data-policy]] · [[platform-coverage]] · [[go-intake]]
