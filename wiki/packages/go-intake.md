---
type: package
tags: [go, 신뢰경계, 순수패키지, 보안]
updated: 2026-08-12
sources: ["go/internal/intake/intake.go", "go/CONTRACT.md", "README.md"]
---

# `internal/intake` — 신뢰 경계

클라이언트 보고를 정규화하는 **순수 패키지**. 표준 라이브러리 외 아무것도 import 하지 않는다
(내부 import 금지).

```go
func NormPayload(raw []byte) (Payload, error)
func NormSession(raw map[string]any, ctx ...Ctx) (Session, bool)
func NormKey(s string) string
func NormKeyOf(kind, raw string) string     // 축마다 규칙이 다르다
func SafeKeyword(s string) (string, bool)   // false = 버린다
```

## ★ `SafeKeyword` 가 이 레포의 신뢰 경계다

`keyword` 는 **어휘가 열려 있는 유일한 축**이라 서버가 한 번 더 거른다. 저장 전에 버리는 것:

- 벤더 접두사가 붙은 문자열
- 32자 이상 hex
- 대소문자+숫자가 섞인 24자 이상
- 이메일 · 접속 문자열 조각
- 10자리 이상 연속 숫자
- `키=값` 형태

**판정은 언제나 버리는 쪽으로 기운다** — 수집기가 먼저 거른다는 전제이지만 신뢰하지 않고,
한 번 저장되면 지우는 비용이 훨씬 크다 → [[data-policy]].

> 포팅 당시 현행 정규식에 lookahead·backreference 가 **없음을 확인했다** → Go 의 `regexp`
> (RE2)로 그대로 옮겨진다. **이 축만은 "대충 비슷하게"가 허용되지 않는다** (`go/CONTRACT.md`).

## `NormKeyOf` — 축마다 규칙이 다르다

| 축 | 정규화 |
|---|---|
| `bash` | basename (선두 실행파일만 — 인자 없음) |
| `slash` | 첫 토큰 |
| `keyword` | 소문자 |
| `tool` | 공백 유지 |

> `go/CONTRACT.md` 개정 1 — 계약은 `NormKey(s)` 만 적었지만 실제 JS 는 `normKey(kind, raw)`
> 였고 축마다 규칙이 달라 `NormKeyOf` 를 추가했다.

## `normModel` — 자리표시자 접기

Claude Code 는 중단·오류 메시지 같은 턴에 모델 이름 대신 `<synthetic>` 같은 자리표시자를
쓴다. 인테이크가 그것을 접는다.

판정 규칙의 단일 출처는 `placeholderModelRe`. **꺾쇠로 감싼 값 전체(`<…>`)만** 자리표시자로
보고, `a<b>c` 처럼 꺾쇠가 일부만 있는 값은 **실제 모델명일 수 있으므로 건드리지 않는다.**

수정 이전에 저장된 행은 그대로 남으므로 [[cleanup]] 의 `placeholder-models` 가 그것을
정리한다.

## 상한 (⚠ 중복 정의)

내부 import 가 금지되어 이 값들을 **자기 패키지에 다시 정의한다**:

| 상수 | 값 |
|---|---|
| `CounterKinds` | tool · bash · slash · skill · agent · mcp · keyword |
| `MaxSeriesPerSession` | 200 |
| `MaxCountersPerSession` | 400 |

**갈라지면 인테이크가 [[go-store]] 가 받지 않는 행을 만든다.** 둘 중 하나를 고칠 때 반드시
함께 고친다. 현재 값은 일치한다 → [[risks]].

같은 값을 [[collector]] 의 `internal/policy` 도 세 번째로 갖고 있다(별도 모듈이라 import
불가). 그쪽은 `drift_test.go` 가 **서버 소스를 직접 읽어** 감시한다.

## 하위호환 두 가지

- **`series` 가 없어도 거절하지 않는다** — 거절하면 그 사람의 사용량이 통째로 사라진다.
  대신 그 세션의 모델별 값이 근사가 된다 → [[model-three-paths]]
- **`platform` 이 없으면 `claude`, 목록 밖이면 `other`** — 거부하지도 `claude` 로 접지도
  않는다 → [[platform-coverage]]

## 왜 순수 패키지인가

`cost`·`stats`·`tz`·`intake` 넷은 아무것도 import 하지 않는다. **테이블 테스트로 완결되고,
그것이 이 포팅에서 가장 싸게 확신을 얻는 자리**였기 때문이다(`go/CONTRACT.md`).

대가가 위의 상수 중복이다 — 의도적으로 지불한 비용이고, 감시 장치를 붙여 두었다.

## 관련

[[data-policy]] · [[go-store]] · [[collector]] · [[idempotency]] · [[cleanup]]
