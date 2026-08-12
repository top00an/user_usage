---
type: concept
tags: [멱등, 인테이크, 불변식]
updated: 2026-08-12
sources: ["README.md", "collector/cmd/usage-collector/main.go", "contract/README.md", "go/internal/store/write.go"]
---

# 멱등 — 중복 전송이 정상 동작에 포함된다

## 규칙

**수집기는 세션 절대값을 보낸다(델타가 아니다). 서버는 `(세션, 축, 키)` 로 덮어쓴다.**

누적(`+=`)을 쓰면 수집기가 두 번 도는 순간 값이 두 배가 되는데, 수집기는 실패해도 재시도하는
**best-effort 경로**라 중복 전송이 정상 동작에 포함된다.

## 그래서 안전해지는 것들

| 상황 | 결과 |
|---|---|
| 체크포인트를 지우고 전량 재전송 | 값 불변 — 다음 실행이 한 번 오래 걸릴 뿐 |
| 훅이 두 번 발화 | 값 불변 |
| 설치기 재실행 후 백필 | 값 불변 |
| 골든 하네스가 시드를 **두 번** 보냄 | 값 불변 (일부러 두 번 보낸다) |

마지막 줄이 방어다 — 멱등이 누적으로 퇴화하면 값이 두 배가 되어 [[golden-contract]] 가
즉시 잡는다.

## 체크포인트는 최적화이지 정확성의 근거가 아니다

`~/.claude/usage-collector-state.json` 이 "바뀐 세션만" 고르게 해서 전송량을 줄인다. 정확성은
**서버 키가 진다** — 그래서 체크포인트가 사라지거나 틀려도 데이터가 망가지지 않는다.

체크포인트 키는 **파일 절대경로**다. 원천이 달라도 섞일 수 없으므로 플랫폼 접두사를 붙이지
않는다 — 붙이면 기존 체크포인트가 통째로 무효가 되어 전량 재전송이 한 번 더 일어날 뿐이고
얻는 것이 없다 → [[collector]].

## 인테이크 페이로드

```jsonc
{
  "user": "alice",              // 세션에 username 이 없을 때의 기본값
  "machine": "host-a",
  "sessions": [{                // 한 보고당 최대 50
    "id": "0f3a…",              // 8~120자, [A-Za-z0-9._-]
    "platform": "claude",       // claude|codex|gemini|antigravity (없으면 claude)
    "model": "claude-sonnet-5", // 그 세션의 최빈 모델(모델 축이 아니다)
    "input": 100, "output": 250, "cacheRead": 9000, "cacheCreate": 400,
    "turns": 3, "startedAt": "2026-08-07T01:00:00.000Z",
    "noTsTurns": 0,             // 시각이 없어 시간 버킷에 못 올린 턴 수
    "linesAdded": 12, "linesRemoved": 3,        // 줄 수만 — 코드 내용은 없다
    "editsAccepted": 2, "editsRejected": 0,
    "counters": { "bash": { "pytest": 4 }, "agent": { "backend-engineer": 2 } },
    "series": [{ "hour": "2026-08-07T01", "model": "claude-sonnet-5",
                 "input": 100, "output": 250, "cacheRead": 9000, "cacheCreate": 400,
                 "cc1h": 400, "turns": 3 }]
  }]
}
```

응답: `{"ok":true,"sessions":N,"counters":N,"buckets":N}`.

> `go/CONTRACT.md` 개정 2 — 골든이 이 개수를 대조하므로 `httpapi` 는 `SeriesUpsertN`·
> `CountersUpsertN`(`(int, error)`) 쪽을 불러야 한다.

## 하위호환 두 가지 — 같은 규율

### `series` 가 없어도 거절하지 않는다

신버전 수집기만 보낸다. **거절하면 그 사람의 사용량이 통째로 사라진다.** 대신 그 세션의
모델별 값은 근사가 되고 화면이 그 사실을 밝힌다 → [[model-three-paths]].

### `platform` 이 없으면 `claude`, 목록 밖이면 `other`

거부하지도 `claude` 로 접지도 않는다. 다른 도구의 사용량이 claude 로 계산되는 **조용한
오분류**보다 "모르는 플랫폼이 있다"가 화면에 보이는 편이 낫다 → [[honest-uncertainty]].

## 상한

인테이크가 거부하기 전에 자르는 값들. 수집기(`collector/internal/policy`)와 서버
(`go/internal/intake`)가 **같은 값**을 각자 갖고 있고 `drift_test.go` 가 감시한다.

| | 값 |
|---|---|
| 한 보고당 세션 | 50 |
| 세션당 series | 200 |
| 세션당 counters | 400 |
| 축당 키 | 80 |
| 키 길이 / 키워드 길이 | 120 / 40 |

> ⚠ `PORT-STATUS.md` 리스크 7 — `intake` 는 내부 import 가 금지된 순수 패키지라 이 상수들을
> 다시 정의한다. **갈라지면 인테이크가 저장 계층이 받지 않는 행을 만든다.** 둘 중 하나를
> 고칠 때 반드시 함께 고친다. 현재 값은 일치한다.

## 실측

`docs/VERIFICATION.md` §1 — 실제 `~/.claude/projects`(143 세션) 중 5개를 올린 뒤 `-all` 로
재실행: **값 불변**. `collector/e2e_test.go` 가 이것을 CI 에서 상시 재검증한다.

## 관련

[[collector]] · [[go-intake]] · [[go-store]] · [[golden-contract]] · [[model-three-paths]]
