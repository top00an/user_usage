# 기획 — 플랫폼별 미수집 지표 보완

> 목적: Claude 개발 세션에서 이 문서만 읽고 미수집 지표의 조사·구현·검증을 순서대로 진행할 수 있게 한다.
>
> 범위: Claude Code, Codex, Gemini CLI, Antigravity CLI의 미수집 지표 보완.
>
> 원칙: 관측되지 않은 값을 추정하지 않는다. 원문 프롬프트·tool 인자·파일 경로는 저장하지 않는다.

## 0. 현재 판정

현재 지원표의 기준 구현은 [`web/lib/platforms.ts`](../web/lib/platforms.ts)와
[`web/test/platforms.test.tsx`](../web/test/platforms.test.tsx)다.

> **실측 판정 반영(2026-08-21).** W1·W5 는 실데이터로 판정했다 — §W1·§W5 참고.
> W0 에 한 줄 덧붙인다: **「수집됨」이라 적힌 축도 실측으로 재확인한다.** 이 표가 Codex `bash` 를
> 수집됨으로 적고 있었지만 현행 Codex 에서 실제로는 0 건이었다(이벤트 모양이 바뀌어 파서가
> 침묵했고, 실데이터를 보기 전까지 아무도 몰랐다 — 2026-08-21 수정). 낡은 「수집됨」이 「미수집」
> 보다 위험하다.

| 지표 | 현재 상태 | 목표 |
|---|---|---|
| Codex `cacheCreate` | 미수집 | **BLOCKED — 원천에 필드가 없다(실측). 미수집 유지** |
| Codex `slash` | 미수집 | **BLOCKED(잠정) — 구조화 필드 없음. 슬래시를 쓴 세션 샘플 필요** |
| Gemini `slash` | 미수집 | hook/telemetry/session에 명령명이 남는지 검증 |
| Antigravity tool/bash/MCP | 미수집 | 공식 hook 기반 수집 |
| Antigravity skill/agent | 미수집 | 명시적 이벤트가 있을 때만 수집 |
| Antigravity LOC/편집량 | 미수집 | transcript 또는 edit 결과 검증 후 수집 |
| Gemini/Codex/Claude의 기존 수집 축 | 수집됨 | 회귀 방지 |
| Gemini/Antigravity `cacheCreate` | 해당 없음 | 수집 대상으로 변경하지 않음 |

`미수집`은 원천에 값이 있더라도 아직 수집하지 않는 상태이고, `해당 없음`은 해당 비용·개념이
원천의 과금 모델에 존재하지 않는 상태다. `해당 없음`을 0 또는 `수집됨`으로 바꾸지 않는다.

## 1. 개발 규칙

1. 기존 작업 트리 변경을 되돌리거나 덮어쓰지 않는다.
2. 먼저 실제 원천 샘플을 확보하고, 샘플이 없으면 parser를 작성하지 않는다.
3. 원문 대신 allowlist 필드만 local spool/보고 payload에 넣는다.
4. 필드가 없으면 0으로 채우지 않고 `unmeasured` 또는 기존 미수집 상태를 유지한다.
5. 플랫폼 지원표는 fixture와 통합 검증이 통과한 뒤에만 변경한다.
6. 새 공개 OTLP ingest API는 만들지 않는다. 필요한 경우 공식 telemetry를 local adapter로 변환한다.

## 2. 작업 순서

### W0 — 현황 및 버전 고정

- [ ] `git status --short`로 기존 사용자 변경을 기록한다.
- [ ] 설치된 각 CLI의 버전과 실제 데이터 루트를 기록한다.
- [ ] 현재 테스트를 먼저 실행하고 기준 결과를 저장한다.

```bash
go test ./collector/...
go test ./go/...
npm test -- --runInBand
```

완료 조건: 기준 테스트 결과가 남고, 작업 전부터 존재한 실패와 신규 실패가 구분된다.

### W1 — Codex `cacheCreate` 검증 및 구현

대상: `collector/internal/codex/`

1. 실제 Codex session JSONL에서 `token_count` 이벤트를 찾는다.
2. 다음 필드의 실제 이름과 위치를 확인한다.

```text
input_tokens
cached_input_tokens
cache_write_input_tokens
output_tokens
reasoning_output_tokens
```

3. 실제 필드가 확인될 때만 optional parser field를 추가한다.
4. 구버전 로그와 필드가 없는 로그의 기존 결과가 변하지 않는지 fixture를 추가한다.
5. 필드가 없는 세션은 `cacheCreate=0` 수집됨이 아니라 `미측정`으로 유지한다.

검증:

- 실제 샘플 1개 이상
- 필드 있음/없음 fixture 각 1개
- 기존 Codex parser 회귀 테스트
- 총 토큰 불변 검증

완료 조건: 설치된 CLI 버전에서 값이 실제로 존재할 때만 지원표를 변경한다.

#### 판정 (2026-08-21 · 실측)

**BLOCKED — 필드가 없다. 미수집을 유지한다.**

실세션 8개의 `event_msg/token_count` **69건 전수**를 훑었다. `info.total_token_usage` ·
`info.last_token_usage` 두 버킷에 실린 키는 다음 **다섯 개뿐**이다(각 138회):

```text
input_tokens
cached_input_tokens
output_tokens
reasoning_output_tokens
total_tokens
```

`cache_write_input_tokens` 를 비롯해 write·create 계열 키는 **하나도 없다.** 기획서 §5 의 중단
기준("공식 또는 실제 원천에 구조화된 필드가 없음")에 해당하므로 parser 를 추가하지 않는다.

재판정 조건: Codex 버전이 올라간 뒤 같은 조회로 키 목록이 늘었을 때. 확인 명령은 이 문서의
검증 절과 같은 방식(세션 JSONL 을 훑어 `token_count` 의 usage 키를 세는 것)이다.

### W2 — Antigravity hook 수집

대상: `collector/internal/antigravity/`, 설치 스크립트 및 hook 등록 코드

공식 hook의 `PreToolUse`/`PostToolUse` 이벤트를 사용한다. 기존 statusLine과 Stop hook을 제거하거나
덮어쓰지 말고, 기존 chain에 멱등적으로 추가한다.

local spool에 저장 가능한 필드:

```text
event_id
session_id
conversation_id
tool_name
tool_type
success
duration_ms
timestamp
added_lines
removed_lines
```

저장 금지:

```text
prompt
toolCall.args 원문
tool response 원문
파일 절대 경로
환경변수·토큰
```

구현 순서:

1. hook stdin 원본을 파일에 남기지 않는 임시 디버그 경로로 확인한다.
2. 필드 allowlist 변환 함수를 순수 함수로 작성한다.
3. spool 파일은 append-only로 쓰고 partial write와 재시작을 처리한다.
4. 기존 Stop flush 경로로 `POST /api/usage`에 전달한다.
5. 중복 event ID가 재전송되어도 합계가 중복되지 않게 한다.

완료 조건: tool 이름과 성공 여부가 실제 hook 샘플에서 확인되고, 기존 statusLine 수집이 깨지지 않는다.

### W3 — Antigravity transcript와 LOC/편집량

statusline의 `transcript_path`가 실제 존재하는지 확인하고, 실제 transcript JSONL 샘플을 확보한다.
샘플에서 명시적인 edit 결과와 추가/삭제 줄 수가 확인될 때만 parser를 추가한다.

다음은 추정 금지 대상이다.

- tool 호출 횟수로 LOC 계산
- artifact 개수로 수정량 계산
- tool 인자 문자열의 숫자를 LOC로 오인
- transcript에 없는 skill/agent 타입 생성

완료 조건: 동일한 edit에 대해 원천의 added/removed 값과 collector 결과가 일치한다.

### W4 — Gemini slash command

Gemini CLI에서 다음을 실행하고 세 원천을 비교한다.

```text
/stats
/resume
/commands
```

확인 대상:

- session JSONL
- 공식 hook stdin
- local telemetry output

명령명이 명시적으로 남아 있으면 명령명만 allowlist로 수집한다. 명령명이 남지 않으면 현재
`미수집`을 유지한다. 사용자 prompt 전체를 저장해 slash를 추출하는 방식은 사용하지 않는다.

### W5 — Codex slash command

Codex rollout/session JSONL에 slash command가 실제로 기록되는지 확인한다. 기록되지 않으면 parser를
추가하지 않는다. app-server 또는 공식 lifecycle event가 실제 명령명을 제공하는 경우에만 별도
adapter를 검토한다.

완료 조건: 과거 파일에서 재현 가능한 이벤트 형식이 있고, TUI 표시만을 근거로 수집됨 판정을 하지 않는다.

#### 판정 (2026-08-21 · 실측, 잠정)

**BLOCKED — 구조화된 필드가 없다. 다만 결정적 반례 샘플이 없다.**

실세션 8개를 훑은 결과:

- `slash` · `command_name` · `custom_prompt` · `prompt_name` · `builtin` 계열 키가 **한 건도 없다.**
- `event_msg/user_message` 20건 중 `/` 로 시작하는 것이 **0건**이다.

⚠ 둘째 항목이 이 판정을 **잠정**으로 만든다. 이 8개 세션에서는 사용자가 슬래시 명령을 아예 쓰지
않았으므로, "형식에 자리가 없다"와 "이번 표본에 안 나왔다"를 가르지 못한다. 전자여야 BLOCKED 가
확정된다.

확정에 필요한 것(값싸다): Codex 를 한 번 열어 슬래시 명령을 하나 쓰고 그 세션 파일에서 명령명이
남는지 본다. 남으면 명령명만 allowlist 로 수집하고, 남지 않으면 미수집으로 확정한다.
**사용자 prompt 전체를 저장해 슬래시를 추출하는 방식은 쓰지 않는다**(이 문서 §W4 와 같은 규율).

### W6 — 지원표와 문서 갱신

다음 조건을 모두 만족한 항목만 `web/lib/platforms.ts`와 관련 설명을 변경한다.

- 실제 샘플 fixture 존재
- parser 단위 테스트 존재
- 빈 필드/구버전 회귀 테스트 존재
- collector → API → store 통합 확인
- 개인정보 leak 검사 통과
- 재전송 시 중복 집계 없음

## 3. 내부 이벤트 계약 제안

기존 usage payload를 깨지 않도록 기존 세션 usage와 별도 event 배열을 사용한다. 실제 코드의 payload
구조와 충돌하면 기존 계약을 우선한다.

```json
{
  "source": "antigravity",
  "session_id": "opaque-session-id",
  "events": [
    {
      "event_id": "stable-id",
      "kind": "tool",
      "name": "shell",
      "success": true,
      "duration_ms": 1200,
      "added_lines": 3,
      "removed_lines": 1
    }
  ]
}
```

`name`은 tool/command의 식별자만 허용한다. 경로, 인자, 응답 내용은 서버로 보내지 않는다.

## 4. 검증 명령

작업 중에는 변경 범위에 맞춰 실행하고, 최종적으로 아래 명령을 모두 통과시킨다.

```bash
# collector 는 **별도 모듈**이다(collector/go.mod) — 레포 루트에서 ./collector/... 는 돌지 않는다.
go -C collector test ./... -count=1
go -C collector vet ./...
go -C go test ./... -count=1
go -C go vet ./...

# 프런트는 vitest 다(jest 가 아니다 — `--runInBand` 는 이 레포에 없는 플래그다).
cd web && npx vitest run && npx tsc --noEmit && npx eslint . --max-warnings=0

# 골든 44개는 **서버가 떠 있어야** 한다(--base 필수). CI 의 contract 스텝과 같은 방식이다.
npm run contract:verify -- --base http://127.0.0.1:<port>
```

추가 수동 검증:

- 파일이 아직 쓰이는 중일 때 collector를 실행해 partial JSONL을 무시하는지 확인
- 같은 spool을 두 번 flush해 합계가 증가하지 않는지 확인
- hook/telemetry 원문에 prompt, args, path, token이 보고 payload에 들어가지 않는지 확인
- CLI 버전별 field presence matrix 작성
- 기존 4개 플랫폼의 수집 결과가 미수집 보완 전후로 변하지 않는지 비교

## 5. 우선순위와 중단 기준

### P0

- 실제 Codex `cache_write_input_tokens` 확인
- Antigravity hook 샘플 확보
- 개인정보 제거 및 중복 방지 검증

### P1

- Antigravity tool/bash/MCP 수집
- Antigravity transcript 기반 LOC/편집량
- Gemini local hook/telemetry 보완

### P2

- Gemini slash
- Codex slash
- Antigravity skill/agent

다음 경우 해당 항목은 구현을 중단하고 `미수집`으로 유지한다.

- 공식 또는 실제 원천에 구조화된 필드가 없음
- 원문 prompt/args를 저장해야만 계산 가능함
- 버전별 의미가 달라 값의 정확성을 보장할 수 없음
- 0과 미수집을 구분할 수 없음

## 6. Claude 개발 세션 시작 프롬프트

아래 내용을 Claude 개발 세션에 그대로 전달한다.

```text
docs/PLAN-uncollected-coverage.md를 기준으로 작업한다.

1. 먼저 git status와 현재 테스트를 확인한다.
2. 기존 사용자 변경을 절대 되돌리거나 덮어쓰지 않는다.
3. W1부터 순서대로 진행하되, 실제 원천 샘플이 없는 항목은 구현하지 말고 BLOCKED로 기록한다.
4. 원문 prompt, tool args, 응답, 파일 경로, 토큰은 저장하거나 전송하지 않는다.
5. 값이 없는 필드를 0으로 채우지 않는다.
6. 각 작업은 fixture, unit test, integration test, privacy 검증을 함께 추가한다.
7. 테스트가 통과하기 전에는 web/lib/platforms.ts의 지원 판정을 변경하지 않는다.
8. 완료 시 변경 파일, 실행한 테스트, 실제로 수집 가능해진 지표, 여전히 미수집인 지표를 보고한다.
```

## 7. 참고 구현 위치

- 플랫폼 판정표: `web/lib/platforms.ts`
- 플랫폼 판정 테스트: `web/test/platforms.test.tsx`
- Codex parser: `collector/internal/codex/`
- Gemini parser: `collector/internal/gemini/`
- Antigravity runtime capture: `collector/internal/antigravity/`
- 수집기 진입점: `collector/cmd/usage-collector/main.go`
- 기존 로컬 LLM 기획: `docs/PLAN-local-llm.md`

