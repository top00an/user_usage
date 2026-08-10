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
#   4) Antigravity CLI 가 있으면 statusLine(캡처) + Stop 훅(플러시) 등록 — 없으면 조용히 스킵
#   5) 초기 백필 1회 실행 + 결과 보고
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

# ── Antigravity 감지 ─────────────────────────────────────────────────────────
# Antigravity(`agy`)는 다른 셋과 다르게 **디스크에 토큰을 남기지 않는다.** 토큰이 보이는
# 자리는 statusLine 하나뿐이라, 그 자리를 우리가 받아 스풀에 적고(캡처) Stop 훅에서
# 서버로 밀어야 한다(플러시). 그 두 등록이 ⑤ 에서 일어난다.
#
# 감지 실패는 **완전히 조용하다.** 안 쓰는 사람에게 "Antigravity 없음" 같은 줄을 보이면
# 설치 로그가 남의 도구 목록이 된다 — 기존 원천(claude·codex·gemini)의 스킵 규율과 같다.
AGY_HOME="$HOME/.gemini/antigravity-cli"
AGY_SETTINGS="$AGY_HOME/settings.json"
AGY_HOOKS="$HOME/.gemini/config/hooks.json"
# 우리 것을 담는 네임스페이스 키. hooks.json 은 `orca-status` 같은 **형제 키들의 맵**이라
# 이 키 하나만 통째로 갈아끼우면 남의 키는 한 글자도 건드리지 않는다(그리고 그게 멱등이다).
AGY_NS="claude-usage"
# 멱등 키이자 자기참조 판정 키. 수집기의 antigravity.StatusLineFlag 와 **같은 문자열**이어야
# 한다 — 설치기와 수집기가 같은 근거로 "이미 우리 것인가"를 판정한다.
AGY_MARKER="-antigravity-statusline"

AGY=0
if [ -x "$HOME/.local/bin/agy" ] || [ -d "$AGY_HOME" ]; then
  AGY=1
fi

# agy_read_statusline — settings.json 의 현재 statusLine.command 를 출력한다(없으면 빈 문자열).
#
# 이 값이 이 작업의 전부다. 여기 남의 명령이 있는데 그냥 덮으면 사용자는 매 순간 보던
# 화면을 잃는다. 그래서 읽어서 config.env 로 옮기고(AGY_PREV_STATUSLINE) 수집기가 체인한다.
# 손상 JSON 이면 읽기 단계에서 멈춘다 — 못 읽는 파일은 병합해서도 안 된다.
#
# **객체와 문자열 두 모양을 모두 읽는다.** 실측된 모양은 `{"type":"command","command":"..."}`
# 이지만, 축약형 `"statusLine": "..."` 을 못 읽으면 그 사용자의 상태줄만 조용히 사라진다.
# 못 읽는 쪽으로 틀리는 비용이 너무 크므로 넓게 읽는다.
agy_read_statusline() {
  [ -f "$AGY_SETTINGS" ] || return 0
  if command -v jq >/dev/null 2>&1; then
    jq -r '(if (.statusLine|type)=="object" then .statusLine.command
            elif (.statusLine|type)=="string" then .statusLine
            else null end)
           | if type=="string" then . else "" end' "$AGY_SETTINGS"
  elif command -v python3 >/dev/null 2>&1; then
    python3 - "$AGY_SETTINGS" <<'PY'
import json, sys
txt = open(sys.argv[1]).read().strip()
data = json.loads(txt) if txt else {}
sl = data.get("statusLine") if isinstance(data, dict) else None
if isinstance(sl, dict):
    sl = sl.get("command")
sys.stdout.write(sl if isinstance(sl, str) else "")
PY
  elif command -v node >/dev/null 2>&1; then
    node -e '
      const fs=require("fs");
      const t=fs.readFileSync(process.argv[1],"utf8").trim();
      const d=t?JSON.parse(t):{};
      let sl=(d&&typeof d==="object"&&!Array.isArray(d))?d.statusLine:null;
      if(sl&&typeof sl==="object"&&!Array.isArray(sl)) sl=sl.command;
      process.stdout.write(typeof sl==="string"?sl:"");
    ' "$AGY_SETTINGS"
  else
    die "jq/python3/node 중 하나가 필요하다 — 기존 statusLine 을 안전하게 읽을 수 없어 멈춘다: $AGY_SETTINGS"
  fi
}

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

# jsonquote — JSON 문자열 **본문**으로 안전하게 만든다(감싸는 따옴표는 호출부가 붙인다).
#
# 왜 필요한가: JSON 도구가 하나도 없을 때 우리는 설정 파일을 `printf` 로 직접 만든다.
# 그런데 훅 명령에는 `"$HOME/..."` 처럼 **큰따옴표가 들어 있다.** 그대로 박으면 문자열이
# 거기서 끊겨 파일 전체가 깨진 JSON 이 되고, 도구가 없으니 재검증도 못 해 그대로 저장된다.
# 결과는 "설치는 성공했다고 나오는데 설정 파일이 죽어 있는" 최악의 조용한 실패다.
# 우리 명령에는 제어문자·개행이 없으므로 백슬래시와 큰따옴표 둘만 이스케이프하면 충분하다.
jsonquote() { printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'; }

# ── 기존 statusLine 보존 결정 (Antigravity 가 있을 때만) ─────────────────────
# 이 파일은 아래에서 **통째로 다시 쓰인다.** 그래서 재실행 때 AGY_PREV_STATUSLINE 을
# 먼저 건져내지 않으면, 2회차 실행이 사용자의 원래 상태줄을 영구히 지운다
# (그 시점엔 settings.json 에 우리 명령이 박혀 있어 원본을 되찾을 곳이 없다).
AGY_PREV=""
if [ "$AGY" = "1" ]; then
  agy_saved=""
  if [ -f "$cfg" ]; then
    # 우리가 쓴 600 파일이다. 셸 인용을 되돌리는 가장 정확한 방법이 sourcing 이라
    # **서브셸에서** 읽어 값만 꺼낸다(이 셸의 변수는 오염되지 않는다).
    agy_saved=$( . "$cfg" >/dev/null 2>&1 || true; printf '%s' "${AGY_PREV_STATUSLINE:-}" )
  fi
  agy_cur=$(agy_read_statusline) \
    || die "Antigravity settings.json 을 읽지 못했다(손상 JSON 이거나 jq/python3/node 가 없다) — 아무것도 건드리지 않았다: $AGY_SETTINGS"
  case "$agy_cur" in
    "")            AGY_PREV="$agy_saved" ;;   # 상태줄이 원래 없었다 → 보관값 그대로
    *"$AGY_MARKER"*) AGY_PREV="$agy_saved" ;; # 이미 우리 것 → 보관값을 덮지 않는다(재실행)
    *)             AGY_PREV="$agy_cur" ;;     # 남의 상태줄 → 지금 보관한다
  esac
fi

umask_old=$(umask); umask 077
{
  printf 'SERVER=%s\n'        "$(shquote "$SERVER")"
  printf 'KEY=%s\n'           "$(shquote "$KEY")"
  printf 'COLLECTOR_BIN=%s\n' "$(shquote "$BIN")"
  # 있을 때만 적는다. 빈 값을 적어 두면 수집기가 "체인이 있다"고 착각할 여지를 남긴다.
  # `[ ] && printf` 로 쓰지 않는다 — 조건이 거짓이면 그룹의 종료코드가 1 이 되어
  # `set -e` 가 설치를 통째로 죽인다(조용히 죽어서 원인 찾기가 최악이다).
  if [ -n "$AGY_PREV" ]; then
    printf 'AGY_PREV_STATUSLINE=%s\n' "$(shquote "$AGY_PREV")"
  fi
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
  # 명령에 `"$HOME/..."` 같은 큰따옴표가 들어 있으므로 반드시 jsonquote 를 거친다
  # (여기서는 재검증할 도구조차 없어서, 깨뜨리면 아무도 못 잡는다).
  cat > "$tmpout" <<EOF
{
  "hooks": {
    "SessionEnd": [
      {
        "hooks": [
          { "type": "command", "command": "$(jsonquote "$HOOK_CMD")" }
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

# ── ⑤ Antigravity 연동 (statusLine 캡처 + Stop 훅 플러시) ────────────────────
#
# 왜 Claude 훅만으로는 안 되나: Claude 의 SessionEnd 훅이 수집기를 돌리면 **파일 기반**
# 원천(claude·codex·gemini)이 한 번에 훑인다. Antigravity 는 파일에 토큰이 없어서 그 스캔에
# 걸릴 것이 없다 — 누군가 statusLine 에서 값을 붙잡아 스풀에 적어 두어야 비로소 훑을 것이 생긴다.
# 그래서 등록이 둘이다: 값을 **만드는** statusLine, 그걸 **보내는** Stop 훅.
if [ "$AGY" = "1" ]; then
say "⑤ Antigravity CLI 연동"

# 두 명령 모두 비밀을 담지 않는다. config.env(600)를 sourcing 해 필요한 값을 얻는다 —
# settings.json·hooks.json 은 공유·백업·스크린샷으로 새어나가기 쉬운 파일이라 토큰을 넣지 않는다.
#
# statusLine: AGY_PREV_STATUSLINE 을 **export** 해야 수집기(자식 프로세스)가 본다.
# `.` sourcing 은 셸 변수를 만들 뿐 환경변수로 올려주지 않는다.
AGY_STATUSLINE_CMD='sh -c '\''. "$HOME/.config/claude-usage/config.env" >/dev/null 2>&1 || exit 0; export AGY_PREV_STATUSLINE; exec "$COLLECTOR_BIN" '"$AGY_MARKER"''\'''
# Stop 훅: 스풀을 서버로 민다. **원천을 좁히지 않는다** — Claude 를 전혀 안 쓰고
# Antigravity+Codex 만 쓰는 사용자는 Claude SessionEnd 훅이 없어서, 여기서 좁히면
# codex·gemini 가 영영 안 올라간다(복구 불가). 전량을 훑어도 체크포인트가 있어
# 바뀐 파일이 없으면 stat 만 하고 지나간다(실측: 2회차 "바뀐 세션 0").
# 무슨 일이 있어도 0 으로 끝낸다(`; exit 0`) — 우리 실패가 남의 CLI 에 에러로 뜨면 안 된다.
AGY_HOOK_CMD='sh -c '\''. "$HOME/.config/claude-usage/config.env" >/dev/null 2>&1 || exit 0; USAGE_INTAKE_TOKEN="$KEY" "$COLLECTOR_BIN" -server "$SERVER" >/dev/null 2>&1; exit 0'\'''

# agy_merge FILE OP ARG — JSON-aware 병합 → 검증 → .bak 백업 → 원자적 교체.
#
#   OP=statusline  ARG=우리 statusLine 명령  (statusLine 키만 교체, 형제 키 보존)
#   OP=hooks       ARG=우리 Stop 훅 명령      ($AGY_NS 키만 교체, 형제 키 보존)
#
# 규율은 ④ 와 같다: jq → python3 → node, 셋 다 없는데 파일이 이미 있으면 **덮지 말고 중단**,
# 손상 JSON 이면 중단하고 원본은 한 바이트도 건드리지 않는다.
agy_merge() {
  am_file="$1"; am_op="$2"; am_arg="$3"
  am_tmp=$(mktemp "${TMPDIR:-/tmp}/agy-merge.XXXXXX") || die "임시 파일 생성 실패"
  am_ok=0
  am_tool=""

  if command -v jq >/dev/null 2>&1; then
    if [ -f "$am_file" ]; then am_in="$am_file"; else printf '{}' > "$am_tmp.in"; am_in="$am_tmp.in"; fi
    case "$am_op" in
      statusline)
        # 기존 statusLine 객체의 다른 키(패딩·색 등)는 살린다 — 우리가 아는 두 키만 덮는다.
        am_prog='(if type=="object" then . else error("최상위가 객체가 아니다") end)
                 | .statusLine = ((if (.statusLine|type)=="object" then .statusLine else {} end)
                                  + {"type":"command","command":$cmd})' ;;
      hooks)
        am_prog='(if type=="object" then . else error("최상위가 객체가 아니다") end)
                 | .[$ns] = {"Stop":[{"type":"command","command":$cmd,"timeout":30}]}' ;;
      *) rm -f "$am_tmp" "$am_tmp.in"; die "agy_merge: 알 수 없는 op($am_op)" ;;
    esac
    if jq --arg cmd "$am_arg" --arg ns "$AGY_NS" "$am_prog" "$am_in" > "$am_tmp"; then
      am_ok=1; am_tool=jq
    fi
    rm -f "$am_tmp.in"
    [ "$am_ok" = "1" ] || { rm -f "$am_tmp"; die "jq 병합 실패(손상 JSON 가능) — $am_file 을 건드리지 않았다"; }

  elif command -v python3 >/dev/null 2>&1; then
    if python3 - "$am_file" "$am_op" "$am_arg" "$AGY_NS" > "$am_tmp" <<'PY'
import json, os, sys
path, op, cmd, ns = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
data = {}
if os.path.exists(path):
    with open(path) as f:
        txt = f.read().strip()
    if txt:
        data = json.loads(txt)   # 손상 JSON 이면 여기서 예외 → 종료(원본 보존)
if not isinstance(data, dict):
    raise SystemExit("최상위가 객체가 아니다")
if op == "statusline":
    sl = data.get("statusLine")
    if not isinstance(sl, dict):
        sl = {}
    sl["type"] = "command"
    sl["command"] = cmd
    data["statusLine"] = sl
elif op == "hooks":
    data[ns] = {"Stop": [{"type": "command", "command": cmd, "timeout": 30}]}
else:
    raise SystemExit("알 수 없는 op")
json.dump(data, sys.stdout, indent=2, ensure_ascii=False)
PY
    then
      am_ok=1; am_tool=python3
    else
      rm -f "$am_tmp"; die "python3 병합 실패(손상 JSON 가능) — $am_file 을 건드리지 않았다"
    fi

  elif command -v node >/dev/null 2>&1; then
    if node -e '
        const fs=require("fs");
        const [path,op,cmd,ns]=process.argv.slice(1);
        let data={};
        if(fs.existsSync(path)){const t=fs.readFileSync(path,"utf8").trim(); if(t) data=JSON.parse(t);}
        if(typeof data!=="object"||Array.isArray(data)||data===null) throw new Error("최상위가 객체가 아니다");
        if(op==="statusline"){
          let sl=(data.statusLine&&typeof data.statusLine==="object"&&!Array.isArray(data.statusLine))?data.statusLine:{};
          sl.type="command"; sl.command=cmd; data.statusLine=sl;
        } else if(op==="hooks"){
          data[ns]={Stop:[{type:"command",command:cmd,timeout:30}]};
        } else throw new Error("알 수 없는 op");
        process.stdout.write(JSON.stringify(data,null,2));
      ' "$am_file" "$am_op" "$am_arg" "$AGY_NS" > "$am_tmp"; then
      am_ok=1; am_tool=node
    else
      rm -f "$am_tmp"; die "node 병합 실패(손상 JSON 가능) — $am_file 을 건드리지 않았다"
    fi

  else
    if [ -f "$am_file" ]; then
      rm -f "$am_tmp"
      die "jq/python3/node 중 하나가 필요하다 — 기존 $am_file 을 안전하게 병합할 수 없어 멈춘다"
    fi
    # 파일이 없을 때만: 도구 없이도 안전하게 새로 만든다(병합할 남의 값이 애초에 없다).
    # 명령에 큰따옴표가 들어 있으므로 jsonquote 필수 — 이 경로엔 재검증 도구가 없다.
    am_jarg=$(jsonquote "$am_arg")
    case "$am_op" in
      statusline) printf '{\n  "statusLine": {\n    "type": "command",\n    "command": "%s"\n  }\n}\n' "$am_jarg" > "$am_tmp" ;;
      hooks)      printf '{\n  "%s": {\n    "Stop": [\n      { "type": "command", "command": "%s", "timeout": 30 }\n    ]\n  }\n}\n' "$AGY_NS" "$am_jarg" > "$am_tmp" ;;
    esac
    am_ok=1; am_tool="없음 → 새로 생성"
  fi

  [ -s "$am_tmp" ] || { rm -f "$am_tmp"; die "병합 결과가 비어 있다 — $am_file 을 건드리지 않았다"; }

  # 유효 JSON 재검증. 여기를 통과하기 전에는 원본에 손대지 않는다.
  if command -v jq >/dev/null 2>&1; then
    jq -e . "$am_tmp" >/dev/null 2>&1 || { rm -f "$am_tmp"; die "병합 결과가 유효 JSON 이 아니다 — $am_file 을 건드리지 않았다"; }
  elif command -v python3 >/dev/null 2>&1; then
    python3 -c 'import json,sys; json.load(open(sys.argv[1]))' "$am_tmp" >/dev/null 2>&1 \
      || { rm -f "$am_tmp"; die "병합 결과가 유효 JSON 이 아니다 — $am_file 을 건드리지 않았다"; }
  fi

  mkdir -p "$(dirname "$am_file")"
  if [ -f "$am_file" ]; then
    cp -f "$am_file" "$am_file.bak"
    info "백업: $am_file.bak"
  fi
  mv -f "$am_tmp" "$am_file"
  info "병합 도구: $am_tool → $am_file"
}

# statusLine — 캡처 자리. 여기가 Antigravity 토큰을 볼 수 있는 **유일한** 지점이다.
agy_merge "$AGY_SETTINGS" statusline "$AGY_STATUSLINE_CMD"
if [ -n "$AGY_PREV" ]; then
  info "기존 상태줄 보관: AGY_PREV_STATUSLINE (config.env) — 수집기가 그대로 통과시킨다"
else
  info "기존 상태줄 없음 — 수집기 요약을 표시한다"
fi

# Stop 훅 — 플러시 자리. hooks.json 은 네임스페이스 맵이라 우리 키만 갈아끼운다.
agy_merge "$AGY_HOOKS" hooks "$AGY_HOOK_CMD"
info "훅 등록: $AGY_HOOKS [$AGY_NS.Stop]"
fi

# ── ⑥ 초기 백필 + 검증 ───────────────────────────────────────────────────────
say "⑥ 초기 백필"
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
