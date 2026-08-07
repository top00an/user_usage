'use strict';
/*
 * 사용량 관측(admin) — 비용·분포·차원 슬라이싱·드릴다운.
 *
 * routes/usage.js 가 합계와 상위 키를 담당한다면, 여기는 **"왜 그 숫자인가"** 를 담당한다.
 * 합계만으로는 답이 안 나오는 세 질문을 연다:
 *   지금 비싼 게 뭔가        → /api/usage/series?metric=cost&group_by=…
 *   몇 시에 튀었나           → /api/usage/series?interval=hour   (usage_series)
 *   평균 뒤에 뭐가 숨었나    → /api/usage/distribution
 *   그 세션에서 무슨 일이    → /api/usage/sessions[/:id]
 *
 * ⚠ **등록 순서가 동작의 일부다.** routes/usage.js 는 `/api/usage` 접두사를 통째로 소유하고
 *   안 걸리면 404 를 직접 낸다. 그래서 이 모듈은 server.js 의 AUTHED 체인에서 usage.js **앞에**
 *   와야 하고, 자기 경로가 아니면 404 가 아니라 **false 를 반환해 흘려보내야** 한다.
 *   (같은 이유로 plans 가 projects 앞에 있다 — server.js 의 그 주석 참조.)
 *
 * 권한은 usage.js 의 조회 화면과 같은 admin 경계를 쓴다. 사람별 사용량과 비용이 나가는 자리라
 * 더 낮출 이유가 없다.
 */
const store = require('../lib/store');
const cost = require('../lib/cost');
const stats = require('../lib/stats');
const tz = require('../lib/tz');

/*
 * 그룹 축 화이트리스트 — 요청 문자열이 SQL 이나 필드 접근에 그대로 닿지 않게 한다.
 * 값은 store.sessionRows() 가 돌려주는 필드명이고, 키는 화면·URL 이 쓰는 이름이다.
 */
const GROUP_FIELDS = Object.freeze({ user: 'username', model: 'model', project: 'project', machine: 'machine' });
const MAX_GROUP_AXES = 3;

const METRICS = Object.freeze(['cost', 'tokens', 'sessions', 'turns']);

/* 세션 목록 정렬축. 여기서도 문자열을 SQL 로 흘리지 않는다 — JS 쪽에서만 쓴다. */
const SORTS = Object.freeze(['cost', 'cacheRead', 'output', 'turns', 'startedAt']);

const DAY_RE = /^\d{4}-\d{2}-\d{2}$/;

const HOUR_RE = /^\d{4}-\d{2}-\d{2}T\d{2}$/;

/*
 * 복합 그룹 키를 잇는 구분자. 사용자명·모델명 같은 자유 문자열을 이어 붙이는 자리라,
 * 값에 나타날 수 없는 문자여야 한다. 평범한 구분자(빈 문자열·하이픈)를 쓰면 ('a','bc') 와
 * ('ab','c') 가 같은 계열로 합쳐진다. U+0000 은 이 데이터에 올 수 없어 그 충돌이 불가능하다.
 */
const KEY_SEP = '\u0000';

function parseGroupBy(raw) {
  const parts = String(raw || '').split(',').map((s) => s.trim()).filter(Boolean);
  const out = [];
  for (const p of parts) {
    if (!GROUP_FIELDS[p]) return { error: `알 수 없는 그룹 축: ${p}` };
    if (!out.includes(p)) out.push(p);
  }
  if (out.length > MAX_GROUP_AXES) return { error: `그룹 축은 최대 ${MAX_GROUP_AXES}개입니다` };
  return { axes: out };
}

/*
 * 한 행이 기여하는 지표값. 행은 세션(일 단위)일 수도 시간 버킷(시간 단위)일 수도 있다 —
 * 두 테이블이 같은 필드명을 쓰므로 이 함수는 그대로 쓰인다.
 *
 * ⚠ interval=day 경로에서 이 값은 세션이 **시작한 날**에 전부 귀속된다. 5시간짜리 세션은
 *   시작일 한 칸에 전량 쌓인다(2026-08-03 실측: 4,314턴 세션 하나가 23억 토큰). 응답의
 *   `attribution` 이 그 사실을 화면에 실어 보낸다. interval=hour 는 그 근사가 없다.
 *
 * sessions 지표만 여기서 계산하지 않는다 — 시간 단위에서는 한 세션이 여러 버킷에 걸치므로
 * 행을 세면 중복이 된다. 호출부가 세션 id 집합으로 센다.
 */
function metricValue(metric, row, c) {
  if (metric === 'cost') return c.priced ? c.usd : 0;
  if (metric === 'tokens') return row.input + row.output + row.cacheRead + row.cacheCreate;
  if (metric === 'turns') return row.turns;
  return 0; // sessions — 호출부가 distinct 로 센다
}

function keyOf(axes, row) {
  const key = {};
  for (const a of axes) key[a] = row[GROUP_FIELDS[a]] || '(미상)';
  return key;
}

/* 표본이 잘렸는지 — 화면이 "이 수치는 최근 N건 기준"이라고 말할 수 있어야 한다. */
function sampleInfo(rows, limit) {
  return { rows: rows.length, limit, truncated: rows.length >= limit };
}

module.exports = async function usageAnalyticsRoutes(req, res, ctx) {
  const { u, p, sendJson, requireRole } = ctx;
  // 우리 경로가 아니면 **흘려보낸다**(404 를 내지 않는다 — usage.js 가 접두사 주인이다).
  if (req.method !== 'GET' || !p.startsWith('/api/usage/')) return false;

  const isSeries = p === '/api/usage/series';
  const isDist = p === '/api/usage/distribution';
  const isList = p === '/api/usage/sessions';
  const isQuality = p === '/api/usage/quality';
  const isCoverage = p === '/api/usage/coverage';
  const isLeaderboard = p === '/api/usage/leaderboard';
  const isDispatch = p === '/api/usage/dispatch';
  const mDetail = /^\/api\/usage\/sessions\/([A-Za-z0-9._-]{1,200})$/.exec(p);
  if (!isSeries && !isDist && !isList && !isQuality && !isCoverage && !isLeaderboard && !isDispatch && !mDetail) return false;

  // 관측(비용·분포·품질·드릴다운)은 운영자+어드민(observe). 관리작업이 아니라 조회라 admin 아래로 연다.
  if (!requireRole('observe')) return true;

  const from = u.searchParams.get('from') || '';
  const to = u.searchParams.get('to') || '';
  for (const [name, v] of [['from', from], ['to', to]]) {
    if (v && !DAY_RE.test(v)) { sendJson(res, 400, { error: `${name} 은 YYYY-MM-DD 형식이어야 합니다` }); return true; }
  }
  const limit = Math.max(1, Math.min(store.SESSION_ROWS_MAX,
    Math.floor(Number(u.searchParams.get('limit'))) || store.SESSION_ROWS_DEFAULT));

  /*
   * ── 서브에이전트·스킬 활용 ──────────────────────────────────────────────
   *
   * 왜(실측): 같은 지침이 모든 세션에 실리는데 실제 사용은 사람마다 갈렸다 —
   * 한 사람은 역할 에이전트를 65회 썼고, 다른 사람은 0회에 general-purpose 만 25회였다.
   * 지침만 있고 측정이 없어 아무도 몰랐다. 강제하지 않는다 — 보이면 사람이 판단한다.
   *
   * generic 은 역할이 없는 범용 에이전트다. 이 비율이 이번에 문제를 드러낸 축이다.
   */
  if (isDispatch) {
    const [agents, skills] = await Promise.all([store.byUser('agent'), store.byUser('skill')]);
    const GENERIC = new Set(['general-purpose', 'Explore', 'Plan', 'claude']);
    sendJson(res, 200, {
      agents: agents.map((u) => {
        const generic = u.items.filter((i) => GENERIC.has(i.key)).reduce((a, i) => a + i.count, 0);
        return { ...u, generic, role: u.total - generic };
      }),
      skills,
      generic: [...GENERIC],
    });
    return true;
  }

  // ── 세션 상세(드릴다운) ────────────────────────────────────────────────
  if (mDetail) {
    const id = mDetail[1];
    const row = await store.sessionById(id);
    if (!row) { sendJson(res, 404, { error: '없는 세션' }); return true; }
    /*
     * keyword 축은 내보내지 않는다. 정규화 토큰이라도 한 세션치를 모아 놓으면 그 사람이 무엇을
     * 물었는지 재구성할 여지가 생긴다 — 집계(topKeys)로는 공개하되 세션 단위로는 닫는다.
     */
    const kinds = store.COUNTER_KINDS.filter((k) => k !== 'keyword');
    const counters = await store.countersOf(id, kinds);
    const buckets = await store.seriesOf(id);
    /*
     * 세션 비용은 시간 버킷이 있으면 **그쪽에서** 합산한다. 세션 행의 model 은 최빈 모델
     * 하나라 모델이 섞인 세션을 한 단가로 계산하고, TTL 분해도 세션 행에는 없다.
     * 버킷이 없으면(구버전 수집기) 세션 행으로 계산하고 approx 로 표시한다.
     */
    const c = buckets.length
      ? cost.summarize(buckets)
      : cost.costOf(row);
    sendJson(res, 200, {
      session: row,
      cost: {
        usd: c.usd,
        byAxis: c.byAxis,
        priced: buckets.length ? true : c.priced,
        exact: buckets.length > 0,
        pricedAt: cost.pricedAt(),
      },
      series: buckets,
      counters,
      note: 'keyword 축은 세션 단위로 제공하지 않는다(프롬프트 재구성 방지).',
    });
    return true;
  }

  const username = u.searchParams.get('user') || '';
  const model = u.searchParams.get('model') || '';
  const rows = await store.sessionRows({ from, to, username, model, limit });
  const table = cost.pricing();
  const mult = cost.multipliers();
  const costs = rows.map((r) => cost.costOf(r, table, mult));
  const unpriced = [...new Set(costs.filter((c) => !c.priced && c.model).map((c) => c.model))].sort();

  // ── 분포 ───────────────────────────────────────────────────────────────
  if (isDist) {
    const perTurnCacheRead = [];
    const sessionCost = [];
    const turns = [];
    const hitRate = [];
    rows.forEach((r, i) => {
      if (r.turns > 0) perTurnCacheRead.push(r.cacheRead / r.turns);
      if (costs[i].priced) sessionCost.push(costs[i].usd);
      turns.push(r.turns);
      const denom = r.cacheRead + r.cacheCreate + r.input;
      if (denom > 0) hitRate.push(r.cacheRead / denom);
    });
    sendJson(res, 200, {
      distributions: {
        cacheReadPerTurn: stats.summarize(perTurnCacheRead),
        sessionCostUsd: stats.summarize(sessionCost),
        turnsPerSession: stats.summarize(turns),
        cacheHitRate: stats.summarize(hitRate),
      },
      pricedAt: cost.pricedAt(),
      unpriced,
      sample: sampleInfo(rows, limit),
    });
    return true;
  }

  // ── 세션 목록 ──────────────────────────────────────────────────────────
  if (isList) {
    const sortRaw = u.searchParams.get('sort') || 'cost';
    if (!SORTS.includes(sortRaw)) { sendJson(res, 400, { error: `정렬축은 ${SORTS.join('|')} 중 하나입니다` }); return true; }
    const top = Math.max(1, Math.min(500, Math.floor(Number(u.searchParams.get('top'))) || 50));
    const merged = rows.map((r, i) => Object.assign({}, r, {
      usd: costs[i].priced ? costs[i].usd : null,
      priced: costs[i].priced,
    }));
    merged.sort((a, b) => {
      if (sortRaw === 'startedAt') return String(b.startedAt || '').localeCompare(String(a.startedAt || ''));
      if (sortRaw === 'cost') return (b.usd || 0) - (a.usd || 0);
      return (b[sortRaw] || 0) - (a[sortRaw] || 0);
    });
    /*
     * 비용 카드의 축별 분해(캐시읽기·생성·출력·입력)는 **전 윈도우 합계**라야 한다 — sessions 는
     * top-N 만 담으므로 슬라이스 이전의 `costs`(전 rows) 를 그대로 reduce 한다(costOf 재호출 없음).
     * priced 행만 더해 usd 계약과 정합. 이 필드가 없으면 화면이 축별 $0 으로 떨어진다.
     */
    const byAxis = { input: 0, output: 0, cacheRead: 0, cacheCreate: 0 };
    let usdTotal = 0;
    for (const c of costs) {
      if (!c.priced) continue;
      usdTotal += c.usd;
      byAxis.input += c.byAxis.input;
      byAxis.output += c.byAxis.output;
      byAxis.cacheRead += c.byAxis.cacheRead;
      byAxis.cacheCreate += c.byAxis.cacheCreate;
    }
    sendJson(res, 200, {
      sessions: merged.slice(0, top),
      sort: sortRaw,
      usd: usdTotal,
      byAxis,
      pricedAt: cost.pricedAt(),
      unpriced,
      /*
       * TTL 을 모르는 행 수. 구버전 수집기가 캐시 생성 TTL 을 안 보내서 5분(1.25배)으로
       * 가정한 행이고, 실제가 1시간이면 그만큼(최대 1.6배) 과소 추정이다. 화면이 이 숫자로
       * "이 합계 중 N건은 TTL 미상"이라고 말한다 — 합계만 보여주면 누락이 보이지 않는다.
       */
      ttlUnknownRows: costs.filter((c, i) => c.priced && !c.ttlKnown && rows[i].cacheCreate > 0).length,
      sample: sampleInfo(rows, limit),
    });
    return true;
  }

  // ── 수집 커버리지 ──────────────────────────────────────────────────────
  // "왜 데이터가 없나" 를 화면이 답한다 — 발신처(머신)별 마지막 보고 시각·세션 수·신수집기 여부.
  if (isCoverage) {
    sendJson(res, 200, { reporters: await store.reporterCoverage(), now: new Date().toISOString() });
    return true;
  }

  // ── 품질축 ─────────────────────────────────────────────────────────────
  // usage_series 에 이미 쌓이는 도구오류·거부·최대토큰중단·지연을 기간 합으로 노출한다.
  // 신수집기만 시간 버킷을 보내므로 coverage(세션 N/M)를 함께 실어 "이 지표가 얼마나 덮나"를 밝힌다.
  if (isQuality) {
    const qt = await store.seriesQualityTotals({ from, to, username });
    const rate = (a, b) => (b > 0 ? a / b : 0);
    sendJson(res, 200, {
      coverage: { sessionsWithSeries: qt.sessionsWithSeries, sessionsTotal: new Set(rows.map((r) => r.sessionId)).size },
      turns: qt.turns,
      toolErrors: qt.toolErrors, toolErrorRate: rate(qt.toolErrors, qt.turns),
      stopMaxTokens: qt.stopMaxTokens, stopMaxRate: rate(qt.stopMaxTokens, qt.turns),
      stopRefusal: qt.stopRefusal, refusalRate: rate(qt.stopRefusal, qt.turns),
      latencyAvgMs: qt.latencyTurns > 0 ? qt.latencyMsSum / qt.latencyTurns : 0,
      latencyMaxMs: qt.latencyMsMax,
      latencyTurns: qt.latencyTurns,
      sample: sampleInfo(rows, limit),
    });
    return true;
  }

  // ── 사람별 리더보드 ────────────────────────────────────────────────────
  // 전 윈도우 rows/costs 를 사용자별로 접는다(top-N 슬라이스 없음 — 전원 집계). 비용 내림차순.
  if (isLeaderboard) {
    const by = new Map();
    rows.forEach((r, i) => {
      const key = r.username || '(미상)';
      let e = by.get(key);
      if (!e) { e = { username: key, sessions: 0, turns: 0, tokens: 0, usd: 0, priced: false, cacheRead: 0, denom: 0 }; by.set(key, e); }
      e.sessions += 1;
      e.turns += r.turns || 0;
      e.tokens += (r.input || 0) + (r.output || 0) + (r.cacheRead || 0) + (r.cacheCreate || 0);
      e.cacheRead += r.cacheRead || 0;
      e.denom += (r.cacheRead || 0) + (r.cacheCreate || 0) + (r.input || 0);
      if (costs[i].priced) { e.usd += costs[i].usd; e.priced = true; }
    });
    const users = [...by.values()].map((e) => ({
      username: e.username, sessions: e.sessions, turns: e.turns, tokens: e.tokens,
      usd: e.usd, priced: e.priced,
      cacheHitRate: e.denom > 0 ? e.cacheRead / e.denom : 0,
      usdPerSession: e.sessions > 0 ? e.usd / e.sessions : 0,
    })).sort((a, b) => b.usd - a.usd || b.tokens - a.tokens);
    sendJson(res, 200, { users, pricedAt: cost.pricedAt(), unpriced, sample: sampleInfo(rows, limit) });
    return true;
  }

  // ── 시계열 ─────────────────────────────────────────────────────────────
  const metric = u.searchParams.get('metric') || 'cost';
  if (!METRICS.includes(metric)) { sendJson(res, 400, { error: `metric 은 ${METRICS.join('|')} 중 하나입니다` }); return true; }
  const interval = u.searchParams.get('interval') || 'day';
  /*
   * week 를 여는 근거: 이건 **없는 정밀도를 만드는 게 아니라 있는 정밀도를 접는 것**이다.
   * day 라벨을 그 주의 월요일로 내리는 순수 롤업이라 새 데이터가 필요 없다.
   * minute 이 여전히 닫혀 있는 이유와 정확히 반대다 — 그쪽은 근거 데이터가 아예 없다.
   *
   * 커버리지도 day 와 같다(usage_sessions 전량). hour 만 신버전 수집기로 제한된다.
   */
  if (!['day', 'hour', 'week'].includes(interval)) {
    sendJson(res, 400, { error: 'interval 은 day|hour|week 중 하나입니다' });
    return true;
  }
  const g = parseGroupBy(u.searchParams.get('group_by'));
  if (g.error) { sendJson(res, 400, { error: g.error }); return true; }

  /*
   * 두 축은 **다른 테이블**에서 온다. 섞지 않는 것이 핵심이다.
   *
   *   day  → usage_sessions. 모든 수집기가 보내므로 커버리지가 완전하다. 대신 값이 세션
   *          시작일에 전량 귀속되고, 모델은 세션 최빈 모델 1개로 근사된다.
   *   hour → usage_series.  턴이 일어난 시각·모델에 정확히 귀속된다. 대신 **신버전 수집기를
   *          가진 사람만** 보낸다 — 수집기가 하루 1회 스로틀로 갱신되므로 배포 후 며칠은 일부만 찬다.
   *
   * 한쪽이 비었다고 다른 쪽으로 조용히 대체하지 않는다. 그러면 같은 화면의 두 눈금이 서로 다른
   * 모집단을 그리게 되고, 그 사실이 어디에도 표시되지 않는다. 대신 hour 응답이 `coverage` 로
   * "지금 이 뷰가 몇 개 세션을 덮는가"를 스스로 말한다.
   */
  const byHour = interval === 'hour';
  /*
   * SQL 필터는 UTC 라벨 위에서 돈다. 로컬 날짜를 그대로 넘기면 경계가 오프셋만큼 어긋나
   * 요청한 하루의 앞뒤가 잘린다 — 그래서 **하루씩 넓혀 뜨고** 위 루프에서 로컬 라벨로 정확히
   * 거른다. (넓히지 않고 거르기만 하면 경계 행이 애초에 조회되지 않아 조용히 사라진다.)
   */
  const w = tz.widenUtcRange({ from, to });
  const srcRows = byHour
    ? await store.seriesRows({ from: w.from, to: w.to, username, model, limit: store.SERIES_ROWS_DEFAULT })
    : await store.sessionRows({ from: w.from, to: w.to, username, model, limit });
  /*
   * ⚠ 비용은 **srcRows 기준으로 다시** 계산한다. 종전에는 day 경로가 위쪽 `costs`(넓히기 전
   * rows)를 그대로 썼는데, 이제 두 배열의 길이·순서가 달라 srcCosts[i] 가 srcRows[i] 와
   * 다른 행을 가리키게 된다 — 비용이 엉뚱한 계열에 붙는 조용한 오염이다.
   */
  const srcCosts = srcRows.map((r) => cost.costOf(r, table, mult));
  const srcUnpriced = [...new Set(srcCosts.filter((c) => !c.priced && c.model).map((c) => c.model))].sort();

  const buckets = new Map();  // seriesKey -> Map(t -> value)
  const sessionSets = new Map(); // seriesKey -> Map(t -> Set(sessionId)) — sessions 지표 전용
  const keys = new Map();     // seriesKey -> key 객체
  srcRows.forEach((r, i) => {
    /*
     * ⚠ 저장은 UTC, **집계 라벨은 로컬(KST)** 이다(lib/usage/tz.js 헤더가 근거의 단일 소스).
     * 여기서 옮기지 않으면 00:00~09:00 KST 에 한 일이 전날로 집계되고, 시간축 그래프가
     * 9시간 밀린다 — "몇 시에 몰리나" 를 보려고 만든 축이 그 질문에 거짓말을 한다.
     */
    let t = byHour ? tz.localHour(r.hour) : tz.localDay(r.startedAt);
    // 시각 라벨이 없는 행은 시계열에 올릴 자리가 없다.
    if (byHour ? !HOUR_RE.test(t) : !DAY_RE.test(t)) return;
    /*
     * 조회 범위는 UTC 로 하루씩 넓혀 떴다(아래 seriesRows/sessionRows 호출부). 넓힌 만큼
     * 여기서 **로컬 라벨 기준으로 정확히** 걸러낸다 — 안 걸러내면 요청한 하루에 앞뒤 날의
     * 일부가 섞여 들어온다.
     */
    if (!tz.inRange(t, { from, to })) return;
    // week: 라벨을 그 주의 **월요일**로 내린다(ISO 주 시작). 로컬로 옮긴 뒤에 접어야
    // 주 경계가 오프셋만큼 밀린 채로 굳지 않는다.
    if (interval === 'week') t = tz.weekStart(t);
    const key = keyOf(g.axes, r);
    const sk = g.axes.length ? g.axes.map((a) => key[a]).join(KEY_SEP) : '전체';
    if (!buckets.has(sk)) { buckets.set(sk, new Map()); sessionSets.set(sk, new Map()); keys.set(sk, key); }
    const m = buckets.get(sk);
    m.set(t, (m.get(t) || 0) + metricValue(metric, r, srcCosts[i]));
    if (metric === 'sessions') {
      const ss = sessionSets.get(sk);
      if (!ss.has(t)) ss.set(t, new Set());
      ss.get(t).add(r.sessionId);
    }
  });

  const series = [...buckets.entries()].map(([sk, m]) => {
    /*
     * sessions 는 행 수가 아니라 **서로 다른 세션 수**다. 시간 단위에서 한 세션이 여러 칸에
     * 걸치므로 두 군데에서 각각 중복을 걷어내야 한다:
     *   칸 값  — 그 시간에 살아 있던 서로 다른 세션 수
     *   합계   — 기간 전체의 서로 다른 세션 수. **칸 값의 합이 아니다.**
     * 합으로 두면 2시간 걸친 세션이 2로 세어진다 — 이 화면이 없애려는 바로 그 이중 계산이다.
     * (다른 지표는 가산적이라 합이 곧 합계다.)
     */
    const isSessions = metric === 'sessions';
    const vals = isSessions
      ? new Map([...sessionSets.get(sk).entries()].map(([t, set]) => [t, set.size]))
      : m;
    const total = isSessions
      ? new Set([...sessionSets.get(sk).values()].flatMap((set) => [...set])).size
      : [...vals.values()].reduce((a, b) => a + b, 0);
    return {
      key: keys.get(sk),
      label: g.axes.length ? g.axes.map((a) => keys.get(sk)[a]).join(' · ') : '전체',
      points: [...vals.entries()].sort((a, b) => a[0].localeCompare(b[0])).map(([t, v]) => ({ t, v })),
      total,
    };
  }).sort((a, b) => b.total - a.total);

  const body = {
    metric,
    interval,
    groupBy: g.axes,
    series,
    pricedAt: cost.pricedAt(),
    unpriced: srcUnpriced,
    /*
     * 라벨이 어느 시간대 기준인가. 저장은 UTC 인데 라벨은 로컬이라, 이걸 안 밝히면 화면과 원본
     * 데이터를 대조하는 사람이 9시간 어긋난 값을 보고 버그로 오해한다.
     */
    timezone: tz.label(),
    sample: sampleInfo(srcRows, byHour ? store.SERIES_ROWS_DEFAULT : limit),
    /*
     * 이 응답의 한계를 응답이 스스로 말한다. 화면이 이걸 표시해야 "어제 스파이크가 없었다"를
     * 데이터의 진실로 오해하지 않는다.
     */
    attribution: byHour ? 'turn-hour' : 'session-start-day',
  };

  if (byHour) {
    /*
     * 시간 뷰가 몇 개 세션을 덮는지. 이 값이 낮으면 화면은 "아직 일부만 시간 버킷을 보낸다"고
     * 말해야 한다 — 안 그러면 수집기가 덜 퍼진 기간의 낮은 수치를 사용량 감소로 오해한다.
     */
    body.coverage = {
      sessionsWithSeries: new Set(srcRows.map((r) => r.sessionId)).size,
      sessionsTotal: new Set(rows.map((r) => r.sessionId)).size,
    };
    // 모델 귀속이 정확한 경로라는 사실도 함께 내보낸다(day 는 최빈 모델 근사다).
    body.modelAttribution = 'exact';
  } else {
    body.modelAttribution = 'session-dominant';
  }

  sendJson(res, 200, body);
  return true;
};
