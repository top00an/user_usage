---
type: decision
tags: [빌드, 임베드, 구조적리스크]
updated: 2026-08-12
sources: ["PORT-STATUS.md", "go/CONTRACT.md", "scripts/build.sh", ".github/workflows/ci.yml"]
---

# 결정 — `webroot/` 는 커밋되는 빌드 산출물이다

## 제약

`go:embed` 는 **패키지 디렉터리 밖을 참조하지 못하고 심링크를 따라가지 않는다.** 그래서
`web/out/` 을 `go/internal/httpapi/webroot/` 로 **복사하는 것이 유일한 길**이고, 두 벌이 존재한다.

같은 제약이 설치 스크립트에도 적용된다 — `scripts/install.sh` 와
`go/internal/httpapi/install.sh` 두 벌 → [[installer]].

## 이것이 왜 위험한가

`.gitignore` 의 주석이 정확히 이렇게 적고 있다:

> *"빌드로 재현되는 것을 커밋하면 **소스와 어긋난 채 배포되는 날이 온다**"*

`webroot/` 가 바로 그것이다. 그리고 실제로 `install.sh` 쪽에서는 **한 번 일어났다** — 사본만
고쳤다가 빌드가 원본으로 덮는 순간 그 수정이 사라졌다.

## 방어 넷 겹

| | 방어 | 무엇을 막나 |
|---|---|---|
| 1 | `scripts/build.sh` — **유일한 빌드 경로**. 동기화 후 파일 수를 세고 어긋나면 멈춘다 | 부분 동기화 |
| 2 | `static.go` — `index.html` 이 임베드에 없으면 **init 에서 죽는다** | 셸 없는 바이너리가 조용히 떠서 404 만 내는 것 |
| 3 | `static_test.go` 의 `TestIndexHTMLReferencesOnlyEmbeddedAssets` | 드리프트를 **화면에서 보이기 전에** |
| 4 | `npm run verify:embed` — 재빌드 후 `git diff --exit-code -- go/internal/httpapi/webroot` | 커밋 누락 |

**부팅 실패가 조용한 404 보다 낫다** — 2번의 근거다.

## ⚠ 4번은 CI 에 걸려 있지 않다 (모순)

`PORT-STATUS.md` 리스크 2 는 *"4번을 CI 에 겁니다"* 를 할 일로 적었다. 그런데 `ci.yml` 은
**일부러 걸지 않고** 주석으로 이유를 적었다:

> Next.js/turbopack 이 빌드마다 청크 파일명 해시를 **논결정적으로** 낸다. 커밋된 `webroot/`
> 는 배포에 쓰는 유효한 산출물이고, 재빌드가 "다른 해시의 같은 내용"을 낼 뿐이라 diff 는
> 항상 갈린다. 드리프트 방어는 `static_test.go` 가 **내용 수준**에서 잡는다.

즉 **결정이 바뀌었는데 `PORT-STATUS.md` 가 갱신되지 않았다.** 위키는 둘 다 적는다
→ [[risks]] · [[ci-gates]].

## `all:` 접두사는 필수다

```go
//go:embed all:webroot
```

go:embed 는 `_`·`.` 로 시작하는 이름을 **기본적으로 건너뛴다.** Next 산출물의 본체가 전부
`_next/` 아래다.

빼면 셸만 나가고 스크립트가 통째로 빠지며, **그 증상은 404 가 아니라 빈 화면이다.**
`TestEmbedIncludesNextUnderscoreDirs` 가 지킨다.

## 정적 서빙 설계도 함께 바뀌었다

`go/CONTRACT.md` 개정 3: **손 표 → embed FS 순회.** Next 산출물의 파일명이 콘텐츠 해시라
손으로 적은 화이트리스트는 빌드마다 깨진다.

**화이트리스트의 성질은 그대로다** — 나갈 수 있는 것은 바이너리에 박힌 파일뿐이고, 판정은
서버가 실제로 받은 `EscapedPath` 위의 맵 조회 한 번이다. **정규화하지 않으므로** `/%2e%2e/x`
와 `/../x` 가 같은 것으로 접히는 자리가 없다 — **경로 탈출이라는 문제가 성립하지 않는다**
→ [[go-httpapi]].

## 리뷰 규칙

PR 에 `webroot/` diff 가 있으면 **`build.sh` 로 만든 것인지 확인한다.** 커밋 로그에
`chore(build): webroot 리빌드 — <이유>` 형태가 반복되는 것이 그 규율의 흔적이다.

## 별건으로 미룬 것

`Cache-Control: no-cache` 를 유지했다. `_next/static/*` 는 파일명이 콘텐츠 해시라
`immutable` 로 굳혀도 안전하고 성능상 맞지만, **지금 바꾸면 "옛 화면"이 뜰 때 캐시 탓인지
동기화 누락인지 구분이 어려워진다.** 통합이 안정된 뒤 나눈다 → [[risks]].

## 관련

[[go-httpapi]] · [[web-dashboard]] · [[installer]] · [[ci-gates]] · [[node-to-go-port]]
