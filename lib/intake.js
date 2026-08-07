'use strict';
/*
 * 사용량 보고 인테이크 — 클라이언트 페이로드를 저장 가능한 형태로 좁히는 **순수** 계층.
 *
 * 왜 별도 모듈인가: 이 지점이 신뢰 경계다. 보고를 보내는 것은 팀원 PC 에서 도는 훅이고,
 * 수집기는 팀원 PC 에 따로 배포되므로 서버보다 낡을 수 있다(구버전이 새 필드를 안 보내거나, 신버전이
 * 아직 서버가 모르는 축을 보낸다). 그래서 라우트가 아니라 여기서, DB 를 켜지 않고 단위로
 * 검증할 수 있는 순수 함수로 둔다.
 *
 * 규율:
 *   - 모르는 축·빈 키·0 이하 카운트는 **조용히 버린다**(400 을 내지 않는다). 텔레메트리 실패가
 *     팀원 세션 시작을 흔들면 안 되고, 훅은 어차피 응답을 안 본다.
 *   - 원문 텍스트는 애초에 받지 않는다. keyword 축은 이미 토큰화된 것만 온다는 전제이고,
 *     여기서 공백·길이·개수로 한 번 더 좁힌다(훅이 못 자른 것을 서버가 자른다).
 *   - keyword 축은 추가로 **시크릿·PII 모양을 버린다**(safeKeyword). 이 축만 사람이 입력한
 *     말에서 나오므로 API 키·이메일·사번이 섞여 들어올 수 있는 유일한 자리다.
 *   - 세션 하나가 보낼 수 있는 총량에 상한을 둔다 — 키워드 축은 사실상 무제한이라
 *     상한이 없으면 세션 하나가 테이블을 채운다.
 */
const store = require('./store');

const SESSION_ID_RE = /^[A-Za-z0-9._-]{8,120}$/;   // 트랜스크립트 파일명(uuid) 형태만 받는다
const HOUR_RE = /^\d{4}-\d{2}-\d{2}T\d{2}$/;       // 시간 버킷 라벨(UTC). store 와 같은 규칙
const KEY_MAX = 120;
const PER_KIND_MAX = 80;                            // 축마다 상위 N개까지 — 롱테일은 화면에 안 쓴다

/*
 * ── keyword 축 전용 방어 ─────────────────────────────────────────────
 *
 * 이 축만 **사람이 입력한 말**에서 나온다. 나머지 축은 도구명·명령어·에이전트명이라 어휘가
 * 닫혀 있지만, 여기는 열려 있다 — 그래서 프롬프트에 섞여 들어간 API 키·토큰·이메일·사번이
 * 토큰화를 통과하면 그대로 상위 키워드가 되어 화면에 걸린다(토큰을 가진 전원이 본다).
 *
 * 훅이 클라이언트에서 한 번 거른다는 전제이지만 신뢰하지 않는다. 수집기는 팀원 PC 에서 도는
 * 별도 프로세스라 서버보다 낡을 수 있고(구버전엔 이 필터가 아예 없다), 무엇보다 **한 번 저장되면
 * 지우는 비용이 훨씬 크다.** 인테이크가 유일한 진입점이라 여기서 거르면 빠뜨릴 자리가 없다.
 *
 * 판정은 전부 "버리는" 방향으로만 작동한다 — 애매하면 남기지 않고 버린다. 키워드 하나가
 * 집계에서 빠지는 손실은 0 에 가깝고, 시크릿 하나가 남는 손실은 되돌릴 수 없다.
 *
 * 길이 상한을 축 전체(120)보다 훨씬 낮게 잡는 이유: 사람이 쓰는 낱말은 40자를 넘지 않는다.
 * 넘는 것은 사실상 식별자·해시·키다.
 */
const KEYWORD_MAX = 40;

/*
 * 벤더 자격증명 접두사. 이것들은 **정상 낱말이 될 수 없어서** 접두사만 맞으면 뒤를 안 본다.
 *
 * 여기에 `token`·`password` 같은 **라벨 낱말은 넣지 않는다.** 그건 값이 아니라 이름이라
 * 버려도 얻는 보안이 없고, 하필 이 대시보드에서 'token' 은 정상 어휘다(토큰 사용량).
 * 값이 붙은 형태(`token=abc`)는 아래 ASSIGN_RE 가 잡는다 — 그쪽이 정확한 신호다.
 */
const SECRET_PREFIX_RE = new RegExp(
  '^(?:'
  + 'sk-|sk_live|sk_test|pk_live|rk_live|'
  + 'ghp_|gho_|ghu_|ghs_|ghr_|github_pat_|glpat-|'
  + 'xox[abprs]-|akia|asia|aiza|ya29\\.|eyj|npm_|dop_v1_|shpat_|shpss_|hf_|'
  + 'aws_secret|aws_access|private_key|-----begin'
  + ')', 'i',
);

const ASSIGN_RE = /[=:]/;                           // token=abc · user:pass — 값이 붙은 모양
const HEX_RE = /^[0-9a-f]{32,}$/i;                  // md5·sha·랜덤 hex
const LONG_DIGITS_RE = /\d{10,}/;                   // 전화번호·카드번호·사번 형태
const URLISH_RE = /:\/\/|@/;                        // 이메일·접속문자열 조각

/*
 * 무작위로 보이는 문자열(키·해시의 일반형). 길이 24 이상이면서 소문자·대문자·숫자가 모두
 * 섞여 있으면 사람의 낱말이 아니다. 한글·공백이 섞인 토큰은 이 검사에 걸리지 않는다.
 *
 * ⚠ **대소문자 혼합은 정규화 전에만 보인다.** normKey 가 keyword 를 소문자로 접으므로,
 *   정규화된 키만 보면 이 판정은 영원히 거짓이 된다(즉 벤더 접두사에 안 걸리는 임의 키가
 *   그대로 통과한다). 그래서 safeKeyword 가 원본도 함께 받는다.
 */
function looksRandom(k) {
  if (!k || k.length < 24 || !/^[A-Za-z0-9_+/=-]+$/.test(k)) return false;
  return /[a-z]/.test(k) && /[A-Z]/.test(k) && /[0-9]/.test(k);
}

/*
 * keyword 로 남겨도 되는가. false 면 그 키는 통째로 버린다(카운트도 남기지 않는다).
 *   k    normKey 를 통과한 정규화 키(소문자)
 *   raw  정규화 전 원본(선택) — 대소문자 정보가 여기에만 남아 있다.
 */
function safeKeyword(k, raw) {
  if (!k || k.length > KEYWORD_MAX) return false;
  if (SECRET_PREFIX_RE.test(k)) return false;
  if (ASSIGN_RE.test(k)) return false;
  if (URLISH_RE.test(k)) return false;
  if (LONG_DIGITS_RE.test(k)) return false;
  if (HEX_RE.test(k)) return false;
  if (looksRandom(k)) return false;
  if (raw != null && looksRandom(String(raw).trim())) return false;
  return true;
}

function s(v, n) { const t = v == null ? '' : String(v).trim(); return t.length > n ? t.slice(0, n) : t; }
function nat(v) { const n = Math.floor(Number(v)); return Number.isFinite(n) && n > 0 ? n : 0; }

/*
 * 키 정규화. 축마다 다른 이유가 있다:
 *   bash    — 경로가 붙어 오면(예: /usr/bin/git) basename 만 남긴다. 사람마다 PATH 가 달라
 *             같은 도구가 여러 키로 갈라지는 것을 막는다.
 *   slash   — 선행 슬래시를 유지하되 인자는 이미 훅이 잘랐다고 보고 첫 토큰만 취한다.
 *   keyword — 소문자·공백 제거. 수집기가 같은 규칙으로 토큰화한다고 전제하되 신뢰하지는 않는다.
 */
function normKey(kind, raw) {
  let k = s(raw, KEY_MAX);
  if (!k) return '';
  if (kind === 'bash') k = k.split(/[\\/]/).pop();
  if (kind === 'slash') k = k.split(/\s+/)[0];
  if (kind === 'keyword') k = k.toLowerCase();
  if (/\s/.test(k) && kind !== 'tool') k = k.split(/\s+/)[0];
  return s(k, KEY_MAX);
}

/*
 * 세션 하나의 보고를 정규화한다.
 * 반환 { session, rows } — 유효하지 않으면 null(호출부가 그 세션만 건너뛴다).
 */
function normSession(raw, ctx = {}) {
  if (!raw || typeof raw !== 'object') return null;
  const sessionId = s(raw.id || raw.sessionId, 120);
  if (!SESSION_ID_RE.test(sessionId)) return null;

  const username = s(raw.username || ctx.username, 200) || null;
  const machine = s(raw.machine || ctx.machine, 200) || null;
  const startedAt = s(raw.startedAt, 40) || null;

  const session = {
    sessionId, username, machine, startedAt,
    endedAt: s(raw.endedAt, 40) || null,
    project: s(raw.project, 200) || null,
    model: s(raw.model, 120) || null,
    input: nat(raw.input),
    output: nat(raw.output),
    cacheRead: nat(raw.cacheRead),
    cacheCreate: nat(raw.cacheCreate),
    webSearch: nat(raw.webSearch),
    webFetch: nat(raw.webFetch),
    turns: nat(raw.turns),
    /*
     * 시각이 없어 시계열(series)에 올릴 자리가 없던 턴 수. **D3 의 관측 축이다.**
     *
     * 왜 필요한가(2026-08-05): series 버킷은 `hourOf(timestamp)` 가 성공할 때만 만들어진다.
     * 세션 합계는 시각이 필요 없어 정상이므로, 시각이 없으면 **합계는 맞는데 series 만 빈다** —
     * 그 상태가 어디에도 안 남아서 사용자별 series 커버리지가 2.2% 인 것을 PM 이 DB 를 뒤져야
     * 알았다(한 사용자의 847세션 중 19건). 이 숫자가 있으면 "왜 없는지" 를 화면이 말할 수 있다.
     *
     * 구버전 수집기는 안 보낸다 → 0 이 아니라 **NULL 로 남는다**(아래 store 가 undefined 를 넘긴다).
     * 0 과 NULL 은 다른 사실이다: 0 은 "전 턴에 시각이 있었다", NULL 은 "모른다".
     */
    noTsTurns: raw.noTsTurns == null ? null : nat(raw.noTsTurns),
  };

  /*
   * 시간 × 모델 버킷. **없으면 그냥 없는 것이다** — 400 을 내지 않는다.
   *
   * 이건 선택이 아니라 필수다. 수집기는 팀원 머신에 하루 1회 스로틀로 갱신되므로, 서버가 새
   * 수집기를 발행한 뒤에도 며칠 동안 구버전과 신버전 보고가 **섞여서** 들어온다. 구버전을
   * 거절하면 그 기간 동안 그 사람들의 사용량이 통째로 사라진다.
   *
   * hour 형식이 아닌 행은 조용히 버린다. 시간 라벨이 없는 버킷은 시계열에 올릴 자리가 없고,
   * 형식이 깨진 값을 통과시키면 화면에 정체불명 칸이 생긴다.
   */
  const series = [];
  const rawSeries = Array.isArray(raw.series) ? raw.series : [];
  const seen = new Set();
  for (const b of rawSeries) {
    if (!b || typeof b !== 'object') continue;
    const hour = s(b.hour, 13);
    if (!HOUR_RE.test(hour)) continue;
    const model = s(b.model, 120) || '(미상)';
    const key = `${hour}|${model}`;
    if (seen.has(key)) continue;   // 중복 키는 UPSERT 가 삼키지만 행 수를 정직하게 세려면 여기서 막는다
    seen.add(key);
    series.push({
      hour,
      model,
      input: nat(b.input),
      output: nat(b.output),
      cacheRead: nat(b.cacheRead),
      cacheCreate: nat(b.cacheCreate),
      cc5m: nat(b.cc5m),
      cc1h: nat(b.cc1h),
      turns: nat(b.turns),
      toolErrors: nat(b.toolErrors),
      stopMaxTokens: nat(b.stopMaxTokens),
      stopRefusal: nat(b.stopRefusal),
      latencyMsSum: nat(b.latencyMsSum),
      latencyMsMax: nat(b.latencyMsMax),
      latencyTurns: nat(b.latencyTurns),
    });
    if (series.length >= store.MAX_SERIES_PER_SESSION) break;
  }

  // counters 는 { kind: { key: count } } 또는 [{kind,key,count}] 둘 다 받는다.
  // 수집기는 객체 형태가 짜기 쉬워 그쪽을 기본으로 두되, 배열도 거절하지 않는다(버전 드리프트 흡수).
  const rows = [];
  const push = (kind, key, count) => {
    if (!store.COUNTER_KINDS.includes(kind)) return;
    const k = normKey(kind, key);
    const c = nat(count);
    if (!k || !c) return;
    // 사람이 입력한 말에서 나오는 축만 시크릿·PII 검사를 통과해야 한다(위 safeKeyword 주석).
    // 원본을 함께 넘긴다 — 대소문자 혼합(키의 일반형)은 정규화 전에만 보인다.
    if (kind === 'keyword' && !safeKeyword(k, key)) return;
    rows.push({ kind, key: k, count: c });
  };
  const c = raw.counters;
  if (Array.isArray(c)) {
    for (const r of c) if (r) push(s(r.kind, 40), r.key, r.count);
  } else if (c && typeof c === 'object') {
    for (const kind of Object.keys(c)) {
      const bucket = c[kind];
      if (!bucket || typeof bucket !== 'object') continue;
      // 축마다 상위 N개만 — 훅이 안 잘랐어도 서버에서 자른다.
      const entries = Object.entries(bucket)
        .map(([k, v]) => [k, nat(v)])
        .filter(([, v]) => v > 0)
        .sort((a, b) => b[1] - a[1])
        .slice(0, PER_KIND_MAX);
      for (const [k, v] of entries) push(s(kind, 40), k, v);
    }
  }

  return { session, rows: rows.slice(0, store.MAX_COUNTERS_PER_SESSION), series };
}

/*
 * 페이로드 전체 → 세션 목록. 한 세션이 깨져도 나머지는 살린다.
 * 상한(MAX_SESSIONS)은 훅이 오래 꺼져 있다가 한 번에 밀어 넣는 경우를 막는다 — 그 경우
 * 오래된 것부터가 아니라 **최근 것부터** 살아야 하므로 호출부가 정렬해 보낸다는 전제는 두지 않고
 * 여기서 자르기만 한다(훅은 최근 세션부터 담는다).
 */
const MAX_SESSIONS = 50;
function normPayload(body) {
  const b = body && typeof body === 'object' ? body : {};
  const ctx = { username: s(b.user || b.username, 200), machine: s(b.machine, 200) };
  const arr = Array.isArray(b.sessions) ? b.sessions.slice(0, MAX_SESSIONS) : [];
  const out = [];
  for (const raw of arr) {
    const n = normSession(raw, ctx);
    if (n) out.push(n);
  }
  return out;
}

module.exports = {
  normPayload, normSession, normKey, safeKeyword,
  MAX_SESSIONS, PER_KIND_MAX, KEYWORD_MAX,
};
