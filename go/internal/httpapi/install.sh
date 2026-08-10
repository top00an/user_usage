#!/bin/sh
#
# install.sh — "진짜 설치 하나로 연동".
#
#   curl -fsSL $SERVER/install.sh | sh -s -- --key <ingestkey> --server $SERVER
#
# 한 줄로 다음이 끝난다:
#   1) OS/arch 감지 → 수집기 바이너리 다운로드(Authorization: Bearer)
#   2) 설정 저장(~/.config/claude-usage/config.env, perms 600)
#   3) Claude Code SessionEnd 훅 등록(~/.claude/settings.json, 비파괴·멱등)
#   4) 초기 백필 1회 실행 + 결과 보고
#
# ── 왜 POSIX sh 인가 ─────────────────────────────────────────────────────────
# 원라인이 `| sh` 로 넘긴다. bash 가 아니라 dash/ash 에서도 그대로 돌아야 하므로
# 배열·[[ ]]·local 같은 bashism 을 쓰지 않는다. 문법검사는 `bash -n` 으로 한다.
#
# ── 멱등성 ───────────────────────────────────────────────────────────────────
# 재실행해도 안전하다. 훅은 "usage-collector" 를 참조하는 기존 그룹을 먼저 제거하고
# 새로 하나만 넣으므로 중복되지 않는다. 바이너리·설정은 덮어쓴다. settings.json 은
# 병합 전 .bak 로 백업하고, JSON 도구(jq/python3/node)로만 병합한다 — 절대 통째로 덮지 않는다.

set -eu

# ── 유틸 ─────────────────────────────────────────────────────────────────────
say()  { printf '\033[1m▶ %s\033[0m\n' "$*"; }
info() { printf '  %s\n' "$*"; }
die()  { printf '\033[31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

# ── 인자 파싱 ────────────────────────────────────────────────────────────────
KEY=""
SERVER=""
DIR="$HOME/.local/bin"

while [ $# -gt 0 ]; do
  case "$1" in
    --key)    KEY="${2:-}"; shift 2 || die "--key 값이 없다" ;;
    --key=*)  KEY="${1#--key=}"; shift ;;
    --server) SERVER="${2:-}"; shift 2 || die "--server 값이 없다" ;;
    --server=*) SERVER="${1#--server=}"; shift ;;
    --dir)    DIR="${2:-}"; shift 2 || die "--dir 값이 없다" ;;
    --dir=*)  DIR="${1#--dir=}"; shift ;;
    -h|--help)
      cat <<'USAGE'
사용법: install.sh --key <ingestkey> --server <url> [--dir <설치경로>]
  --key     인테이크 키(필수)
  --server  서버 주소(필수, 예: https://usage.example.com)
  --dir     수집기 설치 위치(기본 $HOME/.local/bin)
USAGE
      exit 0 ;;
    *) die "알 수 없는 인자: $1 (--help 참고)" ;;
  esac
done

[ -n "$KEY" ]    || die "--key 가 필요하다"
[ -n "$SERVER" ] || die "--server 가 필요하다"

# 후행 슬래시 정리(URL 조립에서 // 가 생기지 않게).
SERVER=$(printf '%s' "$SERVER" | sed 's:/*$::')

# ── https 강제 (MITM 바이너리 교체 RCE 차단) ─────────────────────────────────
# 이 스크립트는 $SERVER 에서 실행할 바이너리를 받아 chmod +x 후 실행한다. 서버 채널이
# 평문(http)이면 중간자가 응답을 바꿔 임의 코드를 심을 수 있으므로 https 만 허용한다.
# 예외는 loopback(127.0.0.1·localhost) 뿐이다 — 로컬 테스트에서만 http 를 쓴다.
case "$SERVER" in
  https://*) : ;;
  http://127.0.0.1|http://127.0.0.1:*|http://127.0.0.1/*) : ;;
  http://localhost|http://localhost:*|http://localhost/*) : ;;
  *) die "연동 서버는 https 여야 합니다(MITM 방지) — 평문 http 는 loopback 만 허용합니다.
    거부됨: $SERVER
    허용 예: --server https://usage.example.com
    로컬 테스트: --server http://127.0.0.1:4191 또는 --server http://localhost:4191" ;;
esac

# 설치 경로를 절대경로로 — 훅은 로그인 셸이 아닌 곳에서 실행되므로 PATH 에 기대지 않는다.
case "$DIR" in
  /*) : ;;
  *)  DIR="$(pwd)/$DIR" ;;
esac
BIN="$DIR/usage-collector"

command -v curl >/dev/null 2>&1 || die "curl 이 필요하다"

# ── ① OS/arch 감지 → goos/goarch ────────────────────────────────────────────
say "① 플랫폼 감지"
uname_s=$(uname -s)
uname_m=$(uname -m)
case "$uname_s" in
  Darwin) goos=darwin ;;
  Linux)  goos=linux ;;
  *) die "지원하지 않는 OS: $uname_s (darwin/linux 만 지원)" ;;
esac
case "$uname_m" in
  arm64|aarch64) goarch=arm64 ;;
  x86_64|amd64)  goarch=amd64 ;;
  *) die "지원하지 않는 아키텍처: $uname_m (arm64/amd64 만 지원)" ;;
esac
info "OS=$uname_s arch=$uname_m → $goos/$goarch"

# ── ② 수집기 다운로드 ────────────────────────────────────────────────────────
URL="$SERVER/api/agent/collector?os=$goos&arch=$goarch"
say "② 수집기 다운로드"
info "$URL"
mkdir -p "$DIR"
tmpbin=$(mktemp "${TMPDIR:-/tmp}/usage-collector.XXXXXX") || die "임시 파일 생성 실패"
trap 'rm -f "$tmpbin"' EXIT INT TERM
if ! curl -fsSL -H "Authorization: Bearer $KEY" "$URL" -o "$tmpbin"; then
  die "다운로드 실패 — 서버/키/네트워크를 확인하라: $URL"
fi
[ -s "$tmpbin" ] || die "다운로드된 바이너리가 비어 있다: $URL"
chmod +x "$tmpbin"
mv -f "$tmpbin" "$BIN"
trap - EXIT INT TERM
# 실행 가능성(=arch 일치) 확인. -h 는 도움말을 내고 0 으로 끝난다.
if ! "$BIN" -h >/dev/null 2>&1; then
  die "다운로드된 바이너리를 실행할 수 없다 — arch 불일치 가능성($goos/$goarch): $BIN"
fi
info "설치: $BIN"

# ── ③ 설정 저장 ──────────────────────────────────────────────────────────────
# config.env 는 이제 유일한 비밀 보관처다. SERVER·KEY 에 더해 COLLECTOR_BIN 을 담아
# 훅이 이 파일만 sourcing 해서 실행에 필요한 모든 값을 얻는다(settings.json 엔 비밀 없음).
# 이 파일은 훅에서 `. config.env` 로 sourcing 되므로 값은 셸 안전하게 작은따옴표로 감싼다
# (경로에 공백/특수문자가 있어도 sourcing 이 깨지지 않게).
say "③ 설정 저장"
cfg_dir="$HOME/.config/claude-usage"
cfg="$cfg_dir/config.env"
mkdir -p "$cfg_dir"
# 값을 작은따옴표로 감싸고 내부 작은따옴표는 '\'' 로 이스케이프한다(POSIX 안전 인용).
shquote() { printf "'"; printf '%s' "$1" | sed "s/'/'\\\\''/g"; printf "'"; }
umask_old=$(umask); umask 077
{
  printf 'SERVER=%s\n'        "$(shquote "$SERVER")"
  printf 'KEY=%s\n'           "$(shquote "$KEY")"
  printf 'COLLECTOR_BIN=%s\n' "$(shquote "$BIN")"
} > "$cfg"
umask "$umask_old"
chmod 600 "$cfg"
info "$cfg (perms 600) — SERVER·KEY·COLLECTOR_BIN"

# ── ④ SessionEnd 훅 등록 (비파괴·멱등) ───────────────────────────────────────
say "④ Claude Code SessionEnd 훅 등록"
claude_dir="$HOME/.claude"
settings="$claude_dir/settings.json"
mkdir -p "$claude_dir"
# 훅 명령엔 비밀을 넣지 않는다. config.env(600)를 sourcing 해서 SERVER·KEY·COLLECTOR_BIN
# 을 얻어 실행하는 래퍼만 박는다. 경로는 $HOME 상대로 두어(런타임에 해석) settings.json
# 이 유출·공유돼도 토큰이 새지 않는다.
# 토큰은 argv 가 아니라 USAGE_INTAKE_TOKEN 환경변수로 넘긴다(수집기가 기본으로 읽는다).
# argv 로 넘기면 `ps aux` 에 키가 노출되므로 env 로만 주입한다.
HOOK_CMD='sh -c '\''. "$HOME/.config/claude-usage/config.env" && USAGE_INTAKE_TOKEN="$KEY" exec "$COLLECTOR_BIN" -server "$SERVER"'\'''
# 멱등 키: 새 훅 명령에 실재하는 안정 문자열. 이 문자열을 담은 기존 훅 그룹을 제거 후 재삽입.
MARKER="claude-usage/config.env"
# 레거시 키: 구버전 훅 명령($DIR/usage-collector … 형태, 토큰 평문)을 업그레이드 시 함께
# 제거해 평문 토큰이 남지 않게 한다.
LEGACY_MARKER="usage-collector"

tmpout=$(mktemp "${TMPDIR:-/tmp}/settings.XXXXXX") || die "임시 파일 생성 실패"
merge_trap() { rm -f "$tmpout"; }
trap 'merge_trap' EXIT INT TERM

# 병합기 선택: jq → python3 → node. 어느 것도 없고 파일이 이미 있으면, 손상 위험을
# 감수하고 덮느니 멈춘다(settings.json 파괴 절대 금지).
merged=0
if command -v jq >/dev/null 2>&1; then
  base="$settings"; [ -f "$base" ] || base=/dev/null
  # /dev/null 은 빈 입력이라 jq 가 죽는다 → 파일 없으면 빈 객체를 만든다.
  if [ -f "$settings" ]; then jq_in="$settings"; else printf '{}' > "$tmpout.in"; jq_in="$tmpout.in"; fi
  if jq --arg cmd "$HOOK_CMD" --arg marker "$MARKER" --arg legacy "$LEGACY_MARKER" '
        .hooks = (.hooks // {})
        | .hooks.SessionEnd = (
            (.hooks.SessionEnd // [])
            | map(select(
                ([ .hooks[]? | (.command // "") ] | map(contains($marker) or contains($legacy)) | any) | not
              ))
          )
        | .hooks.SessionEnd += [ { "hooks": [ { "type": "command", "command": $cmd } ] } ]
      ' "$jq_in" > "$tmpout"; then
    rm -f "$tmpout.in"
    merged=1
    info "병합 도구: jq"
  else
    rm -f "$tmpout.in"
    die "jq 병합 실패 — settings.json 을 건드리지 않았다"
  fi
elif command -v python3 >/dev/null 2>&1; then
  if python3 - "$settings" "$MARKER" "$LEGACY_MARKER" "$HOOK_CMD" > "$tmpout" <<'PY'
import json, os, sys
path, marker, legacy, cmd = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
data = {}
if os.path.exists(path):
    with open(path) as f:
        txt = f.read().strip()
    if txt:
        data = json.loads(txt)   # 손상 JSON 이면 여기서 예외 → 종료(파일 보존)
if not isinstance(data, dict):
    raise SystemExit("settings.json 최상위가 객체가 아니다")
hooks = data.get("hooks")
if not isinstance(hooks, dict):
    hooks = {}
se = hooks.get("SessionEnd")
if not isinstance(se, list):
    se = []
def is_ours(group):
    for h in (group.get("hooks") or []):
        c = h.get("command") or ""
        if marker in c or legacy in c:
            return True
    return False
se = [g for g in se if not is_ours(g)]
se.append({"hooks": [{"type": "command", "command": cmd}]})
hooks["SessionEnd"] = se
data["hooks"] = hooks
json.dump(data, sys.stdout, indent=2, ensure_ascii=False)
PY
  then
    merged=1
    info "병합 도구: python3"
  else
    die "python3 병합 실패(손상 JSON 가능) — settings.json 을 건드리지 않았다"
  fi
elif command -v node >/dev/null 2>&1; then
  if node -e '
      const fs=require("fs");
      const [path,marker,legacy,cmd]=process.argv.slice(1);
      let data={};
      if(fs.existsSync(path)){const t=fs.readFileSync(path,"utf8").trim(); if(t) data=JSON.parse(t);}
      if(typeof data!=="object"||Array.isArray(data)||data===null) throw new Error("최상위가 객체가 아니다");
      let hooks=(data.hooks&&typeof data.hooks==="object"&&!Array.isArray(data.hooks))?data.hooks:{};
      let se=Array.isArray(hooks.SessionEnd)?hooks.SessionEnd:[];
      const ours=g=>((g.hooks||[]).some(h=>{const c=String(h.command||""); return c.includes(marker)||c.includes(legacy);}));
      se=se.filter(g=>!ours(g));
      se.push({hooks:[{type:"command",command:cmd}]});
      hooks.SessionEnd=se; data.hooks=hooks;
      process.stdout.write(JSON.stringify(data,null,2));
    ' "$settings" "$MARKER" "$LEGACY_MARKER" "$HOOK_CMD" > "$tmpout"; then
    merged=1
    info "병합 도구: node"
  else
    die "node 병합 실패(손상 JSON 가능) — settings.json 을 건드리지 않았다"
  fi
else
  if [ -f "$settings" ]; then
    die "jq/python3/node 중 하나가 필요하다 — 기존 settings.json 을 안전하게 병합할 수 없어 멈춘다"
  fi
  # 파일이 없을 때만: JSON 도구 없이도 안전하게 새로 만든다.
  cat > "$tmpout" <<EOF
{
  "hooks": {
    "SessionEnd": [
      {
        "hooks": [
          { "type": "command", "command": "$HOOK_CMD" }
        ]
      }
    ]
  }
}
EOF
  merged=1
  info "병합 도구: 없음 → 새 settings.json 생성"
fi

[ "$merged" = "1" ] || die "훅 병합에 실패했다"
[ -s "$tmpout" ]     || die "병합 결과가 비어 있다 — settings.json 을 건드리지 않았다"

# 병합 결과가 유효 JSON 인지 최종 검증(도구가 있으면 그것으로, 없으면 생략).
if command -v jq >/dev/null 2>&1; then
  jq -e . "$tmpout" >/dev/null 2>&1 || die "병합 결과가 유효 JSON 이 아니다 — settings.json 을 건드리지 않았다"
elif command -v python3 >/dev/null 2>&1; then
  python3 -c 'import json,sys; json.load(open(sys.argv[1]))' "$tmpout" >/dev/null 2>&1 || die "병합 결과가 유효 JSON 이 아니다"
fi

# 여기까지 왔으면 병합 결과가 안전하다. 원본을 백업하고 원자적으로 바꾼다.
if [ -f "$settings" ]; then
  cp -f "$settings" "$settings.bak"
  info "백업: $settings.bak"
fi
mv -f "$tmpout" "$settings"
trap - EXIT INT TERM
info "훅 등록: $settings"
info "명령: $HOOK_CMD"

# ── ⑤ 초기 백필 + 검증 ───────────────────────────────────────────────────────
say "⑤ 초기 백필"
set +e
# 토큰은 argv 가 아니라 USAGE_INTAKE_TOKEN env 로 넘긴다(ps aux 노출 차단).
backfill_out=$(USAGE_INTAKE_TOKEN="$KEY" "$BIN" -all -server "$SERVER" 2>&1)
rc=$?
set -e
printf '%s\n' "$backfill_out" | sed 's/^/  /'
if [ "$rc" -ne 0 ]; then
  die "초기 백필 실패(exit $rc) — 위 출력의 HTTP 코드/네트워크 오류를 확인하라"
fi
# "전송 완료 — 서버 기준 세션 N …" 또는 "보낼 것이 없다"(=0) 에서 세션 수를 뽑는다.
n=$(printf '%s' "$backfill_out" | sed -n 's/.*서버 기준 세션 \([0-9][0-9]*\).*/\1/p' | head -1)
[ -n "$n" ] || n=0
printf '\033[32m연동 완료 ✓ — %s 세션 전송\033[0m\n' "$n"
