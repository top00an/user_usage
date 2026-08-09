#!/usr/bin/env sh
# Claude Code SessionEnd 훅 — 방금 끝난 세션을 포함해 새/변경 세션 집계를 SaaS 로 올린다.
#
# 수집기(usage-collector)를 부른다. 수집기는 증분 체크포인트가 있어 매번 전량이 아니라 **델타만**
# 보낸다(재실행 안전·서버 멱등). 훅은 세션 흐름을 **막으면 안 되므로** 오류를 삼키고 조용히 나간다 —
# 놓친 것은 다음 SessionEnd(또는 백그라운드 에이전트)가 백필한다.
#
# 필요한 설정(둘 중 한 방법):
#   1) 이 훅이 읽는 env 파일:  $HOME/.config/usage-collector.env  (usage-collector.env.example 참고)
#   2) 또는 셸 환경에 직접:     USAGE_SERVER_URL, USAGE_INTAKE_TOKEN(=org 인제스트 키)
#
# 설치: settings.snippet.json 을 Claude Code settings.json 의 hooks 에 병합한다.

set -eu

ENV_FILE="${USAGE_COLLECTOR_ENV:-$HOME/.config/usage-collector.env}"
[ -f "$ENV_FILE" ] && . "$ENV_FILE"

BIN="${USAGE_COLLECTOR_BIN:-usage-collector}"

# 설정이 없으면 조용히 나간다(오설치 상태에서 매 세션 에러를 내지 않는다).
[ -n "${USAGE_SERVER_URL:-}" ] && [ -n "${USAGE_INTAKE_TOKEN:-}" ] || exit 0

"$BIN" >/dev/null 2>&1 || true
exit 0
