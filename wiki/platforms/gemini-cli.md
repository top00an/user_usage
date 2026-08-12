---
type: platform
tags: [플랫폼, google, 수집기]
updated: 2026-08-12
sources: ["collector/internal/gemini/gemini.go", "README.md", "docs/VERIFICATION.md"]
---

# Gemini CLI

오픈소스 `google-gemini/gemini-cli`. [[antigravity]] 와 **다른 플랫폼**으로 센다 — 둘 다
Google 이지만 수집 가능한 축이 완전히 다르기 때문이다.

| | |
|---|---|
| 원천 | `<geminiDir>/tmp/<projectSlug>/chats/session-<ts>-<id8>.jsonl` |
| | 서브에이전트: `<parentSessionId>/<sessionId>.jsonl` |
| 파서 | `collector/internal/gemini` |
| 플래그 | `-gemini-dir` |
| 트리거 | 없음 — Claude 훅이 돌 때 함께 훑린다 |
| 공급사 | Google |

## ⚠ 위험한 곳 ① — 리플레이 (tail 파싱 불가)

세션 파일은 **append-only 로그이지 메시지 목록이 아니다.** 세 종류의 레코드가 섞여 있고
**그중 둘은 이미 읽은 것을 되돌린다**:

| 레코드 | 효과 |
|---|---|
| `{"$rewindTo":"<id>"}` | 그 id **를 포함해** 뒤를 전부 지운다. id 가 없으면 **전부** 지운다 |
| `{"$set":{...}}` | 메타 갱신. `messages` 가 실려 있으면 목록을 비우고 그 배열로 **재구축** |

그래서 **마지막 N줄만 읽는 tail 파싱이 불가능하다** — 항상 파일 처음부터 전부 리플레이한다.
증분은 "파일이 바뀌었나"로만 판단하고(state 지문), 바뀌었으면 그 파일을 통째로 다시 읽는다.

## ⚠ 위험한 곳 ② — 토큰 정규화

Gemini 회계는 [[claude-code]]·[[codex]] 와 **또 다르다**:

```
Input       = max(0, tokens.input - tokens.cached)   ← promptTokenCount 는 cached 를 포함한다
CacheRead   = tokens.cached
Output      = tokens.output + tokens.thoughts        ← thoughts 는 candidatesTokenCount 에 미포함인데
                                                        출력 단가로 과금된다
CacheCreate = 0                                      ← "미지원"이 아니라 해당 없음
```

- `thoughts` 를 **안 더하면 추론이 무거운 세션이 통째로 과소계산**된다.
- ⚠ **Codex 와 정반대다** — Codex 의 reasoning 은 output 에 이미 포함이라 더하면 이중 계상.
- `CacheCreate = 0` 은 **해당 없음**이다. Gemini 는 암시적 캐싱이라 캐시 쓰기 과금 개념
  자체가 없다 → [[platform-coverage]].

### 열린 불확실성 — `tokens.tool`

`toolUsePromptTokenCount` 는 **더하지 않는다.** 입력에 포함된 내역으로 보이지만 **공식
문서에 명시가 없다.** 코드 주석이 스스로 "불확실. 실데이터로 확인 필요"라고 적고 있다
→ [[risks]] · [[honest-uncertainty]].

## `slash` 축이 없다

[[codex]] 와 같은 이유 — 세션 파일에 슬래시 명령 기록이 남지 않으므로 **축 자체를 만들지
않는다**(0 을 보내지 않는다).

## 테스트를 흔든 적이 있다

`collector/e2e_test.go` 는 합성 픽스처만 읽어야 하는데 `-gemini-dir ""` 가 빠져 있어서
**실제 `~/.gemini` 세션이 있는 머신에서는 그 세션까지 올라가** 세션 수 단정이 흔들렸다
(`docs/VERIFICATION.md` §2, 2026-08-10 보강).

```
-gemini-dir <경로>  → [gemini] …/tmp — 세션 1개 · 바뀐 세션 1개
-gemini-dir ""      → (gemini 원천이 아예 스캔되지 않음)
```

"디렉터리가 없으면 조용히 건너뛴다"는 편의가 테스트에서는 오염이 된다 → [[collector]].

## 관련

[[collector]] · [[antigravity]] · [[platform-coverage]] · [[cost-model]] · [[codex]]
