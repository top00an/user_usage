---
type: platform
tags: [플랫폼, openai, 수집기]
updated: 2026-08-12
sources: ["collector/internal/codex/codex.go", "README.md"]
---

# Codex CLI

| | |
|---|---|
| 원천 | `~/.codex/sessions/YYYY/MM/DD/rollout-<ISO ts>-<uuidv7>.jsonl` |
| 파서 | `collector/internal/codex` |
| 플래그 | `-codex-dir` |
| 트리거 | 없음 — Claude 훅이 돌 때 함께 훑린다 |
| 공급사 | OpenAI |

**파일 하나가 세션 하나**다. 재개하면 **같은 파일에 append** 되므로 세션이 수일에 걸칠 수 있다.

한 줄은 `{"timestamp","type","payload"}` 이고 `type` 은 `session_meta` · `response_item` ·
`event_msg` · `turn_context` · `compacted` · `inter_agent_communication` 중 하나.

## ⚠ 토큰이 이 파일에서 가장 위험한 부분

`event_msg/token_count` 의 `info.total_token_usage` 는 **세션 누적(단조증가)** 이다.
**이걸 이벤트마다 더하면 총량이 배로 부풀고, 그대로 비용이 된다.**

그래서:

- 세션 합계 = **마지막 누적값**만 접는다
- 시계열 = **연속 누적값의 차분**으로 만든다
- `last_token_usage` 는 **쓰지 않는다** — 합산하면 실측 **2.9% 과대계상**이 난다

## 토큰 정규화 — OpenAI 회계

```
Input       = max(0, input_tokens - cached_input_tokens)   ← cached 는 input 의 부분집합
CacheRead   = cached_input_tokens
Output      = output_tokens                                ← reasoning 이 이미 포함돼 있다
CacheCreate = 0                                            ← 관측 불가 / 해당 없음
```

두 가지가 [[gemini-cli]] 와 **정반대**다:

- OpenAI 의 `reasoning_tokens` 는 output 에 **이미 포함** → 더하면 이중 계상
- Gemini 의 `thoughtsTokenCount` 는 output 에 **미포함** → 더해야 한다

정규화는 **리더의 책임**이다. [[go-cost]] 는 집계된 행만 보므로 이 중복을 **탐지할 수 없다**
→ [[cost-model]].

## 캐시 생성이 "해당 없음"인 이유

OpenAI 는 캐시 **쓰기**에 과금하지 않는다. 응답에 0 이 와도 그건 관측이 아니다
→ [[platform-coverage]].

## `slash` 축이 없다

세션 파일에 슬래시 명령 기록이 남지 않는다. **수집기가 보낼 값이 없어서 축 자체를 만들지
않는다** — 0 을 보내지 않는다. 0 을 보내면 화면이 "안 썼다"고 말하게 된다.

## 알려진 한계 — 압축 롤아웃

Codex 가 **7일 지난 세션을 `.zst` 로 압축**하는데 수집기가 해제를 지원하지 않는다.

```
⚠ 압축된 롤아웃 N개를 건너뛴다(.zst 해제 미지원)
```

**그만큼 사용량이 빠진다.** 침묵하지 않고 stderr 로 찍는다 → [[honest-uncertainty]] ·
[[troubleshooting]].

## 같은 스키마를 낸다

[[claude-code]] 파서와 **같은 `payload.Session`** 을 낸다. 서버가 두 플랫폼을 한 스키마로
저장하기 때문이다. 축 이름·키 정규화·상한은 전부 `internal/policy` 를 공유한다 —
**축은 같은데 키 규칙만 갈라지면 대시보드의 상위 N 이 플랫폼마다 다른 뜻을 갖게 된다.**

## 관련

[[collector]] · [[platform-coverage]] · [[cost-model]] · [[gemini-cli]]
