---
type: concept
tags: [플랫폼, 수집범위, 규율]
updated: 2026-08-12
sources: ["README.md", "collector/internal/", "web/components/platform/SupportBadge.tsx"]
---

# 플랫폼별 수집 범위 — 같은 화면, 다른 측정치

네 플랫폼이 같은 화면에 서지만 **잴 수 있는 것이 다르다.** 이것을 0 으로 그리면 화면이
"안 썼다"고 말하게 되므로, 화면은 두 상태를 **가른다**:

> 이 표를 **어느 화면이 그리는가**는 2026-08-12 에 바뀌었다. 대시보드 탭의 플랫폼 섹션에서
> 지원표를 걷어냈고, 지금 이 표를 렌더하는 곳은 **아키텍처 탭**(`components/Architecture.tsx`)
> 하나다. 축별 단건 표기는 **사용 추적 탭의 축 패널**(`usagetrack/AxisExplorer.tsx`)에 남아
> 있다. 판정은 세 화면 모두 `lib/platforms.ts` 의 `supportOf()` 하나를 부른다 — 표를 옮겨도
> 판정이 갈리지 않는 이유다. → [[honest-uncertainty]]

| 표기 | 뜻 |
|---|---|
| **미수집** | 그 도구가 **기록하지 않는다** — 수집기를 더 만들어도 올 수 없다 |
| **해당 없음** | 개념 자체가 없다 |

"준비 중"이라는 표기는 없다. 그건 거짓말이 되기 때문이다 → [[honest-uncertainty]].

## 축 × 플랫폼

근거는 `collector/internal/{transcript,codex,gemini,antigravity}` — **수집기가 실제로 보내는
축**이다.

| 축 | [[claude-code]] | [[codex]] | [[gemini-cli]] | [[antigravity]] |
|---|---|---|---|---|
| 토큰·모델·세션·비용 | ✅ | ✅ | ✅ | ✅ |
| `tool` · `bash` · `mcp` | ✅ | ✅ | ✅ | **미수집** |
| `skill` · `agent` | ✅ | ✅ | ✅ | **미수집** |
| `keyword` | ✅ | ✅ | ✅ | ✅ ※ |
| `slash` | ✅ | **미수집** | **미수집** | ✅ ※ |
| LOC · 편집 수락/거부 | ✅ | ✅ | ✅ | **미수집** |
| 캐시 생성 | ✅ | **해당 없음** | **해당 없음** | **해당 없음** |

### 각 공백의 이유

- **Codex·Gemini 의 `slash`** — 두 CLI 의 세션 파일에 슬래시 명령 기록이 남지 않는다.
  수집기가 보낼 값이 없어서 **축 자체를 만들지 않는다**(0 을 보내지 않는다).
- **Antigravity 의 행동 축** — '준비 중'이 아니라 **미수집**이다. 그 도구가 기록하지 않아
  **올 수 없다**.
- **※ Antigravity 의 `slash`·`keyword`** — statusLine 스풀이 아니라
  `~/.gemini/antigravity-cli/history.jsonl` 에서 나온다. **그 파일이 없으면 두 축만 조용히
  빠지고**, 토큰·모델·세션 사용량 자체는 스풀만으로 온전하다.
- **캐시 생성이 '해당 없음'인 이유** — OpenAI 는 캐시 **쓰기**에 과금하지 않고, Google 의
  암시적 캐싱에는 캐시 쓰기 과금이라는 개념이 없다. **응답에 0 이 와도 그건 관측이 아니다.**

## 수집 방식 두 가지

셋은 CLI 가 디스크에 남기는 세션 파일을 읽고, 하나는 파일에 토큰이 남지 않아 **런타임에
붙잡아야** 한다.

| 플랫폼 | 방식 | 원천 | 플래그 |
|---|---|---|---|
| Claude Code | 파일 | `~/.claude/projects/**/*.jsonl` | `-dir` |
| Codex | 파일 | `~/.codex/sessions/**/*.jsonl` | `-codex-dir` |
| Gemini CLI | 파일 | `~/.gemini/tmp/<슬러그>/chats/*.jsonl` | `-gemini-dir` |
| Antigravity CLI | **런타임 캡처** | statusLine → 스풀 | `-antigravity-dir` |

## `platform` 필드의 규율

- 없으면 `claude` 로 본다 (구버전 수집기 호환)
- 허용목록(`claude|codex|gemini|antigravity`) 밖 이름은 거부하지도 `claude` 로 접지도 않고
  **`other` 로 남긴다**
- 조회 필터 `?platform=` 의 목록 밖 값은 **400**

이유는 하나다 — 다른 도구의 사용량이 claude 로 계산되는 **조용한 오분류**보다 "모르는
플랫폼이 있다"가 화면에 보이는 편이 낫다 → [[idempotency]] · [[honest-uncertainty]].

`platform` 필터는 조회 엔드포인트에 걸린다. ⚠ **카운터 축에서 볼 수 있는 것은
`Filter.Platform` 뿐**이라는 제약이 `go/internal/store/aggregate.go` 주석에 적혀 있다.

## 단가 공급사

플랫폼과 단가 공급사는 다른 축이다 → [[cost-model]].

| 플랫폼 | 모델 공급사 |
|---|---|
| Claude Code | Anthropic |
| Codex | OpenAI |
| Gemini CLI · Antigravity | Google |

**구독 요금제(ChatGPT Plus · Claude · Antigravity)는 토큰당 과금이 없다** — 화면의 값은
"API 환산 비용"이지 청구액이 아니다.

## 관련

[[claude-code]] · [[codex]] · [[gemini-cli]] · [[antigravity]] · [[collector]] ·
[[cost-model]] · [[honest-uncertainty]]
