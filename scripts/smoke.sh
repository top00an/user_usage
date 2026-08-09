#!/usr/bin/env bash
#
# 네이티브 스모크 — Go 단일 바이너리가 실제로 뜨고 응답하는지 docker 없이 실증한다.
#
# 이 머신에는 docker 가 없어서 `docker build` / `compose up` 을 돌릴 수 없다. 그래서 배포 이미지가
# 담게 될 것과 **같은 산출물**(scripts/build.sh 가 만드는 바이너리)을 로컬에서 만들어 직접 기동하고,
# Dockerfile 의 HEALTHCHECK 와 compose 의 healthcheck 가 프로브할 바로 그 경로(/healthz)와
# 셸 페이지(/)를 curl 로 때려 본다. 여기서 초록이면 이미지 안에서 달라질 이유는 베이스 OS 뿐이다.
#
#   bash scripts/smoke.sh
#
# 하는 일:
#   ① scripts/build.sh 로 바이너리 빌드(유일 빌드 경로 — SKIP_WEB 을 쓰지 않는다)
#   ② 빈 USAGE_DATA_DIR · local(sqlite) 모드로 바이너리 기동
#   ③ /healthz 가 200 인지(무인증·무DB 프로브 — 컨테이너 healthcheck 가 쓰는 바로 그 경로)
#   ④ 루트 / 가 200 이고 HTML 셸인지(go:embed 된 정적 산출물이 실제로 서빙되는지)
#
# 무엇을 기동하든 스크립트 종료 시 반드시 죽이고 임시 데이터 디렉터리를 지운다(trap).

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# 포트·토큰·모드는 부팅 게이트를 통과할 최소값이다.
#  · PORT 4191 : 기본값이자 브라우저 차단 포트가 아님(config.go 의 badPorts 참조).
#  · TOKEN     : 16자 미만이면 부팅이 거부된다(config.go MinTokenLen). 스모크 전용 더미다.
PORT="${SMOKE_PORT:-4191}"
HOST="127.0.0.1"
TOKEN="smoke-token-0123456789abcdef"

# 바이너리와 데이터는 트리 밖(임시)에 둔다 — 스모크가 레포를 더럽히지 않는다.
WORK="$(mktemp -d "${TMPDIR:-/tmp}/usage-smoke.XXXXXX")"
BIN="$WORK/usage-server"
DATA="$WORK/data"
LOG="$WORK/server.log"
mkdir -p "$DATA"

SRV=""
cleanup() {
  if [[ -n "$SRV" ]] && kill -0 "$SRV" 2>/dev/null; then
    kill "$SRV" 2>/dev/null || true
    wait "$SRV" 2>/dev/null || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

say()  { printf '\n\033[1m▶ %s\033[0m\n' "$*"; }
die()  { printf '\033[31m✗ %s\033[0m\n' "$*" >&2; exit 1; }
pass() { printf '\033[32m✓ %s\033[0m\n' "$*"; }

# ── ① 빌드 ────────────────────────────────────────────────────────────────────
# 유일 빌드 경로. SKIP_WEB 은 쓰지 않는다 — 그걸 쓰면 배포가 실제로 담을 산출물이 아니라
# "지난 번 web/out 을 그대로 믿은" 산출물을 검증하게 된다.
say "① 빌드 — scripts/build.sh (web build → webroot embed → go build)"
OUT_BIN="$BIN" bash "$ROOT/scripts/build.sh"
[[ -x "$BIN" ]] || die "바이너리가 만들어지지 않았다: $BIN"

# ── ② 기동 ────────────────────────────────────────────────────────────────────
# 빈 데이터 디렉터리 · local 모드. 이 조합은 원격 pg 없이 sqlite 한 파일로 완결한다.
say "② 기동 — local(sqlite) · USAGE_DATA_DIR=$DATA · $HOST:$PORT"
USAGE_ADMIN_TOKEN="$TOKEN" \
USAGE_DB_MODE=local \
USAGE_DATA_DIR="$DATA" \
USAGE_HOST="$HOST" \
USAGE_PORT="$PORT" \
  "$BIN" >"$LOG" 2>&1 &
SRV=$!

# 포트가 열릴 때까지 최대 ~10초 기다린다. 이 대기가 곧 컨테이너 healthcheck 의 start_period 다.
up=""
for _ in $(seq 1 50); do
  if ! kill -0 "$SRV" 2>/dev/null; then
    printf '\n--- server.log ---\n'; cat "$LOG"
    die "서버가 기동 중에 죽었다(부팅 거부일 수 있다 — 위 로그 확인)"
  fi
  if curl -fsS -o /dev/null "http://$HOST:$PORT/healthz" 2>/dev/null; then up=1; break; fi
  sleep 0.2
done
[[ -n "$up" ]] || { printf '\n--- server.log ---\n'; cat "$LOG"; die "서버가 제한 시간 안에 뜨지 않았다"; }
pass "기동 확인 — 프로세스 살아 있고 포트 응답"

# ── ③ /healthz ────────────────────────────────────────────────────────────────
# 컨테이너 healthcheck 가 프로브할 바로 그 경로. 무인증·무DB 라 토큰 없이 200 이어야 한다.
say "③ /healthz — 무인증·무DB 프로브(컨테이너 healthcheck 가 쓰는 경로)"
code=$(curl -sS -o "$WORK/health.body" -w '%{http_code}' "http://$HOST:$PORT/healthz")
printf '   HTTP %s · body: %s\n' "$code" "$(cat "$WORK/health.body")"
[[ "$code" == "200" ]] || die "/healthz 가 200 이 아니다: $code"
grep -q '"status"' "$WORK/health.body" || die "/healthz 본문에 status 가 없다"
pass "/healthz 200"

# ── ④ 루트 / ──────────────────────────────────────────────────────────────────
# go:embed 된 index.html 이 실제로 서빙되는지. 셸은 무인증이다(데이터는 /api/* 뒤에 있다).
say "④ / — 임베드된 정적 셸(HTML)"
code=$(curl -sS -o "$WORK/root.body" -w '%{http_code}' -H 'Accept: text/html' "http://$HOST:$PORT/")
ctype=$(curl -sS -o /dev/null -w '%{content_type}' -H 'Accept: text/html' "http://$HOST:$PORT/")
bytes=$(wc -c < "$WORK/root.body" | tr -d ' ')
printf '   HTTP %s · content-type: %s · %s bytes\n' "$code" "$ctype" "$bytes"
[[ "$code" == "200" ]] || die "/ 가 200 이 아니다: $code"
grep -qi '<!doctype html\|<html' "$WORK/root.body" || die "/ 본문이 HTML 이 아니다"
pass "/ 200 · HTML"

printf '\n\033[32m✓ 네이티브 스모크 통과\033[0m\n'
