#!/usr/bin/env bash
#
# 네이티브 스모크 — 바이너리가 **실제로 뜨고 응답하는지**를 본다.
#
#   bash scripts/smoke.sh              # 빌드부터(scripts/build.sh) 전부
#   NO_BUILD=1 bash scripts/smoke.sh   # 이미 만든 go/usage-server 를 그대로 기동
#   BIN=/tmp/x bash scripts/smoke.sh
#
# ── 왜 이 스크립트가 있나 ────────────────────────────────────────────────────
# 이 레포의 배포는 컨테이너인데 **이 머신에는 docker 가 없다.** `docker build` 로 이미지를
# 검증할 수 없으므로, Dockerfile 이 컨테이너 안에서 실행할 것과 같은 계약 — "바이너리를 띄우면
# /healthz 가 200 이고 루트가 HTML 이다" — 을 호스트에서 직접 확인한다.
#
# 컨테이너 healthcheck 가 무엇을 물어보는지와 여기서 확인하는 것이 같아야 한다.
# HEALTHCHECK 를 바꾸면 여기 PROBE 도 같이 바꿔라 — 갈라지면 이 스모크는 아무것도 지키지 않는다.
#
# 검사 대상:
#   ① /healthz          200 · 무인증 · 무DB (기동 프로브 — 컨테이너 HEALTHCHECK 와 동일 경로)
#   ② /                 200 · text/html    (go:embed 된 Next 산출물이 실제로 박혔는가)
#   ③ /api/* 무토큰     401                (게이트가 살아 있는가 — 토큰 없이 데이터가 나오면 사고다)

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${BIN:-$ROOT/go/usage-server}"
PORT="${PORT:-4193}"   # 기본 4191 을 피한다 — 사람이 띄워 둔 서버에 붙어 놓고 통과했다고 착각한다
BASE="http://127.0.0.1:$PORT"

say()  { printf '\n\033[1m▶ %s\033[0m\n' "$*"; }
ok()   { printf '   \033[32m✓\033[0m %s\n' "$*"; }
die()  { printf '\033[31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

# ── ① 빌드 ──────────────────────────────────────────────────────────────────
if [[ -z "${NO_BUILD:-}" ]]; then
  say "① 빌드 — scripts/build.sh (유일한 빌드 경로)"
  OUT_BIN="$BIN" bash "$ROOT/scripts/build.sh" >/dev/null || die "빌드 실패"
fi
[[ -x "$BIN" ]] || die "바이너리가 없다: $BIN  (먼저 bash scripts/build.sh)"
ok "$BIN"

# ── ② 빈 데이터로 기동 ──────────────────────────────────────────────────────
# **빈 디렉터리에서 시작한다.** 남아 있는 sqlite 파일 위에서 뜨면 "스키마 보장"이 도는지 알 수
# 없고, 새 호스트에 처음 배포하는 상황(=컨테이너의 빈 볼륨)과 달라진다.
DATA="$(mktemp -d)"
LOG="$(mktemp)"
cleanup() {
  [[ -n "${PID:-}" ]] && kill "$PID" 2>/dev/null || true
  [[ -n "${PID:-}" ]] && wait "$PID" 2>/dev/null || true
  rm -rf "$DATA" "$LOG"
}
trap cleanup EXIT

say "② 기동 — local(sqlite) · 빈 데이터 디렉터리 · 127.0.0.1:$PORT"
env -i PATH="$PATH" HOME="$HOME" \
  USAGE_HOST=127.0.0.1 \
  USAGE_PORT="$PORT" \
  USAGE_DATA_DIR="$DATA" \
  USAGE_DB_MODE=local \
  USAGE_ADMIN_TOKEN=smoke-admin-token-0123456789 \
  "$BIN" >"$LOG" 2>&1 &
PID=$!

# 뜰 때까지 기다린다. 고정 sleep 을 쓰면 느린 머신에서 위양성 실패가 나고, 빠른 머신에서는
# 그냥 시간을 버린다. 죽었으면 즉시 로그를 뱉고 끝낸다 — 타임아웃까지 기다려 봐야 답은 같다.
for _ in $(seq 1 100); do
  kill -0 "$PID" 2>/dev/null || { cat "$LOG" >&2; die "프로세스가 기동 중 죽었다"; }
  curl -sf -o /dev/null "$BASE/healthz" && break
  sleep 0.1
done
ok "pid $PID"

# ── ③ 계약 확인 ─────────────────────────────────────────────────────────────
say "③ /healthz — 무인증 · 무DB · 200 (컨테이너 HEALTHCHECK 가 두드리는 그 경로)"
code=$(curl -s -o /tmp/smoke.health -w '%{http_code}' "$BASE/healthz")
[[ "$code" == 200 ]] || { cat "$LOG" >&2; die "/healthz 가 $code"; }
printf '   HTTP %s  body=%s\n' "$code" "$(cat /tmp/smoke.health)"
grep -q '"status":"ok"' /tmp/smoke.health || die "/healthz 본문이 계약과 다르다"
ok "200 + {\"status\":\"ok\"}"

say "④ / — 200 · text/html (go:embed 된 대시보드가 실제로 바이너리 안에 있는가)"
read -r code ctype < <(curl -s -o /tmp/smoke.root -w '%{http_code} %{content_type}\n' "$BASE/")
printf '   HTTP %s  content-type=%s  bytes=%s\n' "$code" "$ctype" "$(wc -c </tmp/smoke.root | tr -d ' ')"
[[ "$code" == 200 ]] || die "/ 가 $code"
[[ "$ctype" == text/html* ]] || die "/ 의 content-type 이 $ctype"
# 껍데기만 나가고 스크립트가 통째로 빠지는 사고는 404 가 아니라 **빈 화면**으로 보인다.
# 그래서 200 만으로는 부족하다 — _next 청크 참조가 실제로 들어 있는지까지 본다.
grep -q '_next/static' /tmp/smoke.root || die "/ 에 _next/static 참조가 없다 — 정적 청크가 임베드되지 않았다"
ok "200 + text/html + _next/static 참조"

say "⑤ /api/* 무토큰 — 401 (게이트가 살아 있는가)"
code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/usage")
printf '   HTTP %s\n' "$code"
[[ "$code" == 401 ]] || die "/api/usage 가 토큰 없이 $code — 게이트가 열려 있다"
ok "401"

printf '\n\033[32m✓ 스모크 통과\033[0m  (%s)\n' "$BIN"
