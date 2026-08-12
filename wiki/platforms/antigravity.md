---
type: platform
tags: [플랫폼, google, 수집기, statusline]
updated: 2026-08-12
sources: ["collector/internal/antigravity/", "README.md", "scripts/install.sh", "docs/OPERATIONS.md"]
---

# Antigravity CLI (`agy`)

**이 원천은 다른 셋과 근본적으로 다르다 — 디스크에 토큰이 없다.**

| | |
|---|---|
| 원천 | statusLine → 스풀 `~/.config/claude-usage/antigravity/<conversation_id>.json` |
| 파서 | `collector/internal/antigravity` (파서가 아니라 **스풀 집계기**) |
| 플래그 | `-antigravity-dir` |
| 트리거 | **statusLine**(캡처) + **Stop 훅**(플러시) — **둘 다** 필요 |
| 공급사 | Google |

## 실측으로 확인한 사실 (추측 아님)

패키지 주석이 전수 확인 결과를 적고 있다:

| 자리 | 토큰 |
|---|---|
| 훅(SessionStart·PreInvocation·PostInvocation·Stop·PostToolUse) | **없다** — 바이너리에 박힌 protobuf 디스크립터 `hooks.proto` 어디에도 토큰 필드가 없고 실제 stdin payload 도 그랬다. 훅이 주는 것은 conversation_id·model_name·transcript_path·workspace_paths 뿐 |
| `brain/<conv>/.../transcript_full.jsonl` | **없다** — 키는 step_index·source·type·status·created_at·content 뿐 |
| `conversations/<uuid>.db` | **없다** (protobuf BLOB 전수 스캔으로 확인) |
| **statusLine** | ★ **여기에만 있다** — `context_window.current_usage` |

## 그래서 연동이 둘로 나뉜다

- **statusLine** 이 렌더될 때마다 stdin 으로 받은 사용량을 스풀에 적는다 (**캡처**)
- **Stop 훅**이 그 스풀을 서버로 민다 (**플러시**)

등록 위치도 둘이다 — `~/.gemini/antigravity-cli/settings.json`(statusLine)과
`~/.gemini/config/hooks.json`(Stop 훅) → [[installer]].

## 왜 스풀이 필요한가

statusLine 은 **렌더마다 불린다**(실측: 한 번의 `--print` 실행에 **14회**). 그리고 프로세스가
끝나면 그 값은 어디에도 남지 않는다. **붙잡아 두지 않으면 사라지는 값**이라 스풀에 적는다.

## 누적 규칙 — 실측으로 갈랐다

같은 대화를 두 턴 돌려 print 모드의 누적값과 대조:

```
        statusLine                      print(대화 누적)
turn1   current_usage.input=17283       usage.input=17380
turn2   current_usage.input=17506       usage.input=34886
        └ 17380 + 17506 = 34886 (정확히 일치)
```

| 필드 | 성질 | 처리 |
|---|---|---|
| `current_usage` | **직전 invocation 하나**의 값 (누적 아님) | **합산**한다 |
| `context_window.total_output_tokens` | 대화 누적 (실측 2/2 정확 일치: 21, 40) | 그대로 쓴다 |
| `total_input_tokens` | 누적이 **아니다** — `used_percentage × context_window_size` 와 정확히 같았다(154, 25286). **지금 컨텍스트에 들어 있는 양**이지 과금된 입력이 아니다 | 쓰지 않는다 (입력은 스냅샷 합산) |

## 알려진 정확도 한계 (위조하지 않고 그대로 둔다)

**입력은 스냅샷 합산이라 하한이다.** 실측에서 turn1 이 17283 으로 남아 print 의 17380 보다
97 적었다(**-0.6%**). `current_usage` 는 "마지막 API 호출"만 보여주므로 한 invocation 안에서
일어난 보조 호출(제목 생성 등)이 빠진다.

출력은 `total_output_tokens` 가 그 보조 호출까지 잡아내므로(21 vs 스냅샷 17) 이 문제가 없다.

→ [[honest-uncertainty]] · [[risks]]

## statusLine 체이닝

statusLine 자리는 **하나뿐**이라 이미 쓰던 상태줄이 있으면 덮지 않고 **체이닝**한다. 원래
명령을 `AGY_PREV_STATUSLINE` 으로 보관해 두고 수집기가 같은 입력을 그 명령에 먹여 **출력을
그대로 통과**시킨다(화면은 그들 것이다). 구현은 `collector/internal/antigravity/chain.go`.

제거할 때 그 값으로 되살린다 → [[installer]].

## 스풀 파일 판정

```go
func Match(path string) bool   // <conversation_id>.json, 단 ".*" 와 "tmp-*" 제외
```

**원자적 쓰기의 임시 파일(`.tmp-*`)을 반드시 제외한다** — 반쯤 쓰인 파일을 읽으면 그 대화의
사용량이 통째로 0 이 된다. **거부가 아니라 침묵이라 더 나쁘다.**

## 왜 `gemini` 를 재사용하지 않나

코드 주석이 명시한다:

> 오픈소스 gemini-cli 와 **수집 가능한 축이 다르다.** 저쪽은 도구·MCP·LOC 를 주지만
> Antigravity 는 토큰·모델·세션만 준다. 한 값으로 섞으면 대시보드의 **"미수집 / 해당없음"
> 표기가 거짓이 된다** — 있는 것만 정직하게 싣는 것이 이 수집기의 규율이므로 값을 따로 쓴다.

→ [[platform-coverage]] · [[gemini-cli]]

## 행동 축은 "미수집"이다 — '준비 중'이 아니다

`tool`·`bash`·`mcp`·`skill`·`agent`·LOC·편집 수락/거부: **수집기를 더 만들어도 올 수 없다.**
그 도구가 기록하지 않는다.

**예외 둘** — `slash`·`keyword` 는 스풀이 아니라
`~/.gemini/antigravity-cli/history.jsonl` 에서 나온다. **그 파일이 없으면 두 축만 조용히
빠지고**, 토큰·모델·세션 사용량 자체는 스풀만으로 온전하다.

## 서버 허용목록과의 관계

`Platform = "antigravity"` 가 서버 허용목록(`store.Platforms`)에 없으면 서버가 `other` 로
정규화한다. **그건 정상 폴백**이고, 허용목록이 붙은 뒤 재전송하면 제자리를 찾는다
(절대값 UPSERT 라 안전) → [[idempotency]].

## 관련

[[collector]] · [[installer]] · [[platform-coverage]] · [[gemini-cli]] · [[honest-uncertainty]]
