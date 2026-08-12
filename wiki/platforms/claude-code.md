---
type: platform
tags: [플랫폼, anthropic, 수집기]
updated: 2026-08-12
sources: ["collector/internal/transcript/transcript.go", "README.md", "scripts/install.sh"]
---

# Claude Code

이 도구의 **기준 플랫폼**. 수집 축이 가장 넓고, 다른 셋의 파서가 이쪽의 `payload.Session`
스키마를 그대로 낸다.

| | |
|---|---|
| 원천 | `~/.claude/projects/<슬러그>/<sessionId>.jsonl` |
| 파서 | `collector/internal/transcript` |
| 플래그 | `-dir` (`""` 로 끄기) |
| 트리거 | **SessionEnd 훅** (설치기가 `~/.claude/settings.json` 에 등록) |
| 공급사 | Anthropic |

## 파서가 하는 두 가지

① **매핑** — `message.usage` 의 토큰을 세션 합계와 시간×모델 버킷으로 접는다.
② **정책** — 집계만 남긴다. 프롬프트 원문·파일경로·명령인자는 **애초에 뽑지 않는다.**
어휘가 열린 축(`keyword`)은 `policy` 패키지가 시크릿·PII 모양을 버린다 → [[data-policy]].

## 왜 세션 단위로 누적하나

세션이 재개되면 **같은 `sessionId` 가 여러 파일에 흩어질 수 있다.** 서버는 `session_id`
절대값으로 덮어쓰므로, 한 세션을 파일마다 따로 보내면 뒤 파일이 앞 파일을 덮어
**과소집계**가 된다. 그래서 `Aggregator` 가 `sessionId` 로 묶어 누적한 뒤 한 번에 절대값을
낸다 → [[idempotency]].

## 함정 둘 (코드 주석이 명시)

### 줄 상한 16MB

트랜스크립트 한 줄에 큰 `tool_result` 가 실릴 수 있어 `bufio` 기본 버퍼(64KB)로는 잘린다.
잘린 줄은 JSON 파싱에 실패해 **조용히 버려지고, 그러면 그 턴의 토큰이 통째로 사라진다.**
`maxLineBytes = 16MB` 가 그 침묵을 없앤다.

### 슬래시 명령은 **이름만**

`<command-name>` 에서 이름만 뽑는다. **인자(`command-args`)는 정책상 절대 남기지 않는다.**

## 토큰 회계 — Anthropic

**축이 서로소다.** `input` 이 캐시를 **제외**한다:

```
input · cacheRead · cacheCreate  — 서로 겹치지 않는다
```

그래서 정규화가 필요 없다. 다른 셋과 정반대라 [[cost-model]] 이 이 차이를 문서화하고 있다.

`cache_creation.ephemeral_1h_input_tokens` → `series[].cc1h`. 이 값이 없는 행이
`ttlUnknownRows`(TTL 미상 · 최대 1.6배 과소)로 화면에 표시된다 → [[honest-uncertainty]].

## 수집 축 — 전부

7축 전부 + LOC + 편집 수락/거부 + 캐시 생성. 이 플랫폼만 캐시 생성을 준다
→ [[platform-coverage]].

## 연동

원커맨드 설치기가 SessionEnd 훅을 등록한다. **훅 하나가 파일 기반 원천 셋
(Claude·[[codex]]·[[gemini-cli]])을 모두 올린다** — 수집기가 돌 때 기본 경로를 함께 훑기
때문이다 → [[installer]].

## 관련

[[collector]] · [[platform-coverage]] · [[cost-model]] · [[installer]] · [[idempotency]]
