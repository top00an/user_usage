---
type: system
tags: [설치, 온보딩, 훅]
updated: 2026-08-12
sources: ["scripts/install.sh", "docs/OPERATIONS.md", "README.md", "go/internal/httpapi/agent_test.go"]
---

# install.sh — 원커맨드 연동 (설치와 제거)

개발자 머신을 붙이는 유일한 경로. 관리자가 **"연동" 탭**에서 키를 발급하고 원라인을 복사해
전달하면, 개발자는 그 한 줄만 실행한다.

```sh
curl -fsSL $SERVER/install.sh | sh -s -- --key <인제스트키> --server $SERVER
curl -fsSL $SERVER/install.sh | sh -s -- --uninstall            # 반대 방향
```

`$SERVER` 는 **https 여야 한다.** 예외는 loopback 뿐 — 이 스크립트가 서버에서 받은 바이너리를
`chmod +x` 후 실행하므로 평문 http 는 중간자가 임의 코드를 심을 수 있는 자리다.

## 설치가 하는 여섯

1. OS/arch 감지 → 수집기 다운로드(`GET /api/agent/collector`, **인제스트 키 필수**)
2. 설정 저장 — `~/.config/claude-usage/config.env` (**perms 600**)
3. Claude Code **SessionEnd 훅** 등록(`~/.claude/settings.json`)
4. Antigravity CLI 가 **있으면** statusLine(캡처) + Stop 훅(플러시) 등록 — 없으면 **조용히 스킵**
5. 초기 백필 1회
6. 결과 보고

[[codex]]·[[gemini-cli]] 는 별도 등록이 없다. [[collector]] 가 돌 때 기본 경로를 함께 훑으므로
**훅 하나가 파일 기반 원천 셋을 모두 올린다.**

## 이 스크립트가 지키는 것

| 성질 | 어떻게 |
|---|---|
| **비파괴** | 기존 훅·상태줄 보존 |
| **멱등** | 재실행해도 훅이 중복되지 않는다(우리 것만 제거 후 재삽입) |
| **JSON 안전** | 병합은 `jq → python3 → node` 로만. 셋 다 없는데 파일이 있으면 **덮지 않고 멈춘다.** 손상 JSON 이면 한 바이트도 안 건드린다 |
| **백업** | 덮기 전 `.bak` |
| **토큰 격리** | 평문은 `config.env`(600)**에만**. `settings.json`·`hooks.json` 엔 안 들어간다 — 그 파일들은 공유·백업·스크린샷으로 새기 쉽다 |
| **argv 회피** | 토큰은 `USAGE_INTAKE_TOKEN` 환경변수로. argv 는 `ps aux` 에 노출된다 |
| **https 강제** | loopback 만 예외 |

⚠ **`.bak` 은 "최초 원본"이 아니라 "직전 상태"다.** 2회 이상 실행했다면 `.bak` 에도 이미
우리 훅이 들어 있다(실측 확인). 완전한 제거는 `.bak` 복구가 아니라 `--uninstall` 이나
jq 로 우리 그룹만 빼는 쪽이 맞다 → `docs/OPERATIONS.md` §6-1.

## statusLine 체이닝 ([[antigravity]] 전용)

statusLine 자리는 **하나뿐**이라 이미 쓰던 상태줄이 있으면 덮는 대신 **체이닝**한다:
원래 명령을 `AGY_PREV_STATUSLINE` 으로 `config.env` 에 보관해 두고, 수집기가 같은 입력을 그
명령에 먹여 **출력을 그대로 통과**시킨다(화면은 그들 것이다).

제거할 때 그 값으로 되살린다. ⚠ **`config.env` 를 먼저 지우면 보관값도 함께 사라진다.**

## 제거(`--uninstall`)

`--key`·`--server` 가 **필요 없다** — 제거에 필요한 값(설치 경로·보관된 상태줄)은
`config.env` 에 이미 있다. 설치와 제거가 한 파일에 있는 이유도 이것이다: 제거는 설치가
무엇을 어떤 키로 넣었는지 알아야 하는데, 그 지식이 두 파일로 갈리면 한쪽만 고쳐진 채 어긋난다.

**선행 점검이 먼저다.** 도구 유무·JSON 유효성을 전부 확인하고 **나서야** 지우기 시작한다.
바이너리를 먼저 지운 뒤 JSON 에서 실패하면 "훅은 남았는데 수집기는 없는" 반쪽 상태가 되고,
그 상태는 매 세션 조용히 실패한다.

### 실측된 성질 (2026-08-12 · linux/amd64)

| 성질 | 결과 |
|---|---|
| 남의 훅·`theme`·`PreToolUse` 보존 | 셋 다 그대로 |
| statusLine 체이닝 복원 | 원래 명령으로 되살아남(형제 키도 보존) |
| **남의 statusLine 무변경** | 파일 **바이트 동일** · `.bak` 도 안 생김 · 그래도 키·바이너리는 지워짐 |
| 남의 네임스페이스 보존 | `hooks.json` 의 `orca-status` 는 남고 `claude-usage` 만 빠짐 |
| 멱등(2·3회) | 2회차부터 **한 바이트도 안 바꿈** |
| 미설치 HOME | `제거할 것이 없다 ✓` · exit 0 · **파일도 디렉터리도 안 만듦** |
| 키 잔존 | HOME 전체 검색 **0건** |
| JSON 도구 없음 / 손상 JSON | 중단(exit 1) · 파일 md5 동일 · **바이너리·키도 안 지움** |
| jq 1.7.1 / python3 3.14.4 / node v22.22.1 **각각만** | 세 경로 모두 같은 결과 |

### 제거가 하지 않는 일 — 서버의 키 해지

운영자 자격이 필요한 서버 쪽 작업이라 개발자 PC 의 스크립트가 대신할 수 없다.
**퇴사·이탈이면 반드시 해지까지** → [[ingest-keys]]. 제거는 그 PC 에서 보고를 멈출 뿐,
유출된 키는 다른 곳에서 그대로 쓴다.

## ⚠ 이 파일은 레포에 두 벌 있다

| 경로 | 성격 |
|---|---|
| `scripts/install.sh` | **원본. 항상 여기를 고친다** |
| `go/internal/httpapi/install.sh` | 빌드 산출물 — `//go:embed` 가 패키지 밖을 못 읽어서 두는 사본. `scripts/build.sh` 가 `cp` 로 덮는다 |

사본만 고치면 **다음 빌드까지는 조용히 잘 돌아가다가** 빌드가 원본으로 덮는 순간 그 수정이
사라진다(실제로 한 번 사라졌다). [[webroot-embed]] 와 같은 구조의 함정이다.

## 회귀 방어 — `go/internal/httpapi/agent_test.go`

문자열 grep 이 아니라 **임시 HOME 에 실제로 설치하고 실제로 제거한다**(수집기는 가짜 셸
스크립트로 내려받는다). CI 에서 매번 돈다.

| 테스트 | 지키는 것 |
|---|---|
| `TestInstallScriptRoundTrip` | 설치→제거 왕복 · 남의 설정 보존 · statusLine 복원 · 2회차 무변경 · 미설치 HOME 안전 |
| `TestInstallScriptUninstallLeavesForeignStatusLine` | 남의 statusLine 바이트 동일, 그래도 키·바이너리는 삭제 |
| `TestInstallScriptUninstallStopsWithoutJSONTool` | JSON 도구 없는 PATH 에서 중단 · 무변경 |
| `TestEmbeddedInstallScriptMatchesSource` | **임베드 사본이 원본과 바이트 동일** |

## 관련

[[collector]] · [[ingest-keys]] · [[antigravity]] · [[webroot-embed]] · [[runbook]]
