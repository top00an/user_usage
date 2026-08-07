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
 *   - 세션 하나가 보낼 수 있는 총량에 상한을 둔다 — 키워드 축은 사실상 무제한이라
 *     상한이 없으면 세션 하나가 테이블을 채운다.
 */
const store = require('./store');

const SESSION_ID_RE = /^[A-Za-z0-9._-]{8,120}$/;   // 트랜스크립트 파일명(uuid) 형태만 받는다
const HOUR_RE = /^\d{4}-\d{2}-\d{2}T\d{2}$/;       // 시간 버킷 라벨(UTC). store 와 같은 규칙
const KEY_MAX = 120;
const PER_KIND_MAX = 80;                            // 축마다 상위 N개까지 — 롱테일은 화면에 안 쓴다

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

module.exports = { normPayload, normSession, normKey, MAX_SESSIONS, PER_KIND_MAX };
