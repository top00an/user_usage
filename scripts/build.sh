#!/usr/bin/env bash
#
# 단일 빌드 경로 — Next.js 산출물을 Go 바이너리 안에 넣는다.
#
#   web 빌드  →  web/out/ 을 go/internal/httpapi/webroot/ 로 동기화(먼저 비운다)  →  go build
#
# ── 왜 스크립트인가 ─────────────────────────────────────────────────────────
# go:embed 는 패키지 디렉터리 밖을 참조하지 못하고 심링크를 따라가지 않는다. 그래서 정적 산출물이
# **두 벌 존재하고**(web/out/ 과 webroot/) 갈라질 수 있다.
#
# 동기화를 사람이 기억해야 하면 반드시 잊는 날이 온다. 그리고 그때 증상은 "화면이 옛 버전"이라
# 아무도 빌드를 의심하지 않고 코드를 먼저 뒤진다. 그래서 **이 스크립트가 유일한 빌드 경로다** —
# webroot/ 를 손으로 채우지 마라.
#
#   bash scripts/build.sh              # 전체(web 빌드 포함)
#   SKIP_WEB=1 bash scripts/build.sh   # web/out/ 을 그대로 믿고 동기화+go build 만
#   OUT_BIN=/tmp/x bash scripts/build.sh
#
# SKIP_WEB 은 Go 쪽만 반복해서 고칠 때의 지름길이다. **CI 와 배포에서는 쓰지 마라** —
# 그것을 쓰는 순간 위에서 말한 드리프트가 정확히 되살아난다.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WEB="$ROOT/web"
OUT="$WEB/out"
WEBROOT="$ROOT/go/internal/httpapi/webroot"
OUT_BIN="${OUT_BIN:-$ROOT/go/usage-server}"

say() { printf '\n\033[1m▶ %s\033[0m\n' "$*"; }
die() { printf '\033[31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

command -v go >/dev/null || die 'go 가 PATH 에 없다. export PATH="$HOME/.local/go/bin:$PATH"'

# ── ① web 빌드 ──────────────────────────────────────────────────────────────
if [[ -n "${SKIP_WEB:-}" ]]; then
  say "① web 빌드 건너뜀(SKIP_WEB) — web/out/ 을 그대로 쓴다"
  [[ -f "$OUT/index.html" ]] || die "web/out/index.html 이 없다. SKIP_WEB 없이 한 번 돌려라"
else
  command -v npm >/dev/null || die 'npm 이 PATH 에 없다'
  say "① web 빌드 — next build + 인라인 스크립트 외부화"
  [[ -d "$WEB/node_modules" ]] || (cd "$WEB" && npm ci)
  (cd "$WEB" && npm run build)
  [[ -f "$OUT/index.html" ]] || die "web 빌드가 out/index.html 을 만들지 않았다"
fi

# ── ② webroot 동기화 ────────────────────────────────────────────────────────
# **먼저 비운다.** 덮어쓰기만 하면 이전 빌드의 해시 파일이 남아 표가 계속 자라고, 어떤 것이
# 지금 화면인지 알 수 없게 된다. 단 도트파일(.gitkeep · .gitignore)은 트리 구조를 잡는 것이라
# 지우지 않는다.
say "② webroot 동기화 — $(basename "$WEBROOT")/ 를 비우고 web/out/ 을 복사"
mkdir -p "$WEBROOT"
find "$WEBROOT" -mindepth 1 -maxdepth 1 ! -name '.*' -exec rm -rf {} +
cp -R "$OUT/." "$WEBROOT/"

# 복사가 실제로 됐는지 센다. cp 가 조용히 아무것도 안 한 상태로 go build 가 성공하면
# 바이너리는 셸 없이 뜬다(그쪽은 init 에서 죽지만, 여기서 먼저 말하는 편이 낫다).
src_n=$(find "$OUT" -type f ! -name '.*' | wc -l)
dst_n=$(find "$WEBROOT" -type f ! -name '.*' | wc -l)
[[ "$src_n" -eq "$dst_n" ]] || die "동기화 개수 불일치: web/out=$src_n webroot=$dst_n"
[[ -f "$WEBROOT/index.html" ]] || die "webroot/index.html 이 없다"
# _next/ 가 비면 셸만 나가고 스크립트가 통째로 빠진다 — 그 증상은 404 가 아니라 빈 화면이다.
[[ -d "$WEBROOT/_next" ]] || die "webroot/_next/ 가 없다 — Next 청크가 복사되지 않았다"
printf '   파일 %s개 · _next 청크 %s개\n' \
  "$dst_n" "$(find "$WEBROOT/_next" -type f | wc -l)"

# ── ③ go build ──────────────────────────────────────────────────────────────
# 여기서 webroot/ 가 바이너리에 박힌다. 위 동기화가 끝난 **뒤에** 돌아야 한다.
say "③ go build → $OUT_BIN"
(cd "$ROOT/go" && go build -o "$OUT_BIN" ./cmd/usage-server)
[[ -x "$OUT_BIN" ]] || die "바이너리가 만들어지지 않았다: $OUT_BIN"

printf '\n\033[32m✓ 완료\033[0m  %s  (%s)\n' "$OUT_BIN" "$(du -h "$OUT_BIN" | cut -f1)"
