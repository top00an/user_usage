'use strict';
/*
 * 사용량 관측 라우트(routes/usage-analytics.js) — 비용·분포·슬라이싱·드릴다운.
 *
 * 이 파일이 지키는 불변식 중 ①이 가장 조용히 깨진다:
 *   ① **경로 소유권** — routes/usage.js 가 /api/usage 접두사를 통째로 소유하고 안 걸리면 404 를
 *      직접 낸다. 이 모듈이 자기 것이 아닌 경로에 404 를 내거나 true 를 돌려주면, usage.js 의
 *      기존 화면(/api/usage/summary·/identity)이 통째로 죽는다. 서버를 띄우지 않으면 이 회귀는
 *      전체 스위트를 통과하고 배포 후에야 드러난다.
 *   ② 화이트리스트 — metric·group_by·sort 는 요청 문자열이다. 모르는 값은 400 으로 끊는다.
 *   ③ 없는 정밀도를 만들지 않는다 — 근거 없는 interval 은 400 이다. hour 는 Phase 2 에서
 *      usage_series 라는 근거가 생겨 열렸고, week·minute 는 여전히 닫혀 있다.
 *   ④ 세션 상세는 keyword 축을 내보내지 않는다(정규화 토큰이라도 한 세션치를 모으면 재구성 여지).
 *   ⑤ day 뷰와 hour 뷰는 **다른 테이블**이다(usage_sessions vs usage_series). 한쪽이 비었다고
 *      다른 쪽으로 대체하지 않는다 — 커버리지 차이를 coverage 로 밝힌다.
 */
const { test, describe, beforeEach, before, after } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');

// 어댑터/모듈 로드 전에 데이터 디렉터리를 임시로 돌린다(usage.test.js 와 같은 격리 규율).
process.env.USAGE_DATA_DIR = fs.mkdtempSync(path.join(os.tmpdir(), 'usage-obs-'));
const adapter = require('../lib/db/adapter');
const { pgActive } = require('./helper');
const store = require('../lib/store');
const intake = require('../lib/intake');
const route = require('../routes/usage-analytics');

// 앰비언트 config.json 의 usage.pricing 이 비용 산술을 흔들지 않게 빈 설정을 물린다.
let prevCfg; let tmpCfg;
before(() => {
  prevCfg = process.env.USAGE_CONFIG;
  tmpCfg = path.join(os.tmpdir(), `usage-obs-cfg-${process.pid}.json`);
  fs.writeFileSync(tmpCfg, '{}');
  process.env.USAGE_CONFIG = tmpCfg;
});
after(() => {
  if (prevCfg === undefined) delete process.env.USAGE_CONFIG;
  else process.env.USAGE_CONFIG = prevCfg;
  try { fs.unlinkSync(tmpCfg); } catch { /* 비치명 */ }
});

async function fresh() {
  if (pgActive()) {
    const { q } = require('../lib/db');
    await q('DELETE FROM usage_counters').run();
    await q('DELETE FROM usage_series').run();
    await q('DELETE FROM usage_sessions').run();
    return;
  }
  adapter.close();
  process.env.USAGE_DATA_DIR = fs.mkdtempSync(path.join(os.tmpdir(), 'usage-obs-'));
}

async function ingest(p) {
  for (const { session, rows, series } of intake.normPayload(p)) {
    await store.sessionUpsert(session);
    await store.countersUpsert({
      sessionId: session.sessionId, username: session.username,
      machine: session.machine, startedAt: session.startedAt, rows,
    });
    if (series && series.length) {
      await store.seriesUpsert({
        sessionId: session.sessionId, username: session.username,
        machine: session.machine, project: session.project, rows: series,
      });
    }
  }
}

/* 서버를 띄우지 않고 핸들러를 직접 부른다 — 라우팅·검증 로직이 검증 대상이고 HTTP 는 아니다. */
async function call(pathAndQuery, { role = 'admin' } = {}) {
  const u = new URL(`http://x${pathAndQuery}`);
  const out = { status: 0, body: null, handled: null };
  const ctx = {
    u,
    p: u.pathname,
    sendJson: (res, status, body) => { out.status = status; out.body = body; },
    requireRole: (need) => {
      if (role === need || role === 'admin') return true;
      out.status = 403; out.body = { error: 'forbidden' };
      return false;
    },
  };
  out.handled = await route({ method: 'GET' }, {}, ctx);
  return out;
}

/* 세 세션: 사용자 2명 × 모델 2종, 하루 차이. 그룹 조합과 날짜 버킷을 함께 본다. */
async function seed() {
  await ingest({
    machine: 'pc-1',
    user: 'user-a',
    sessions: [
      {
        id: 'sess-aaaa', startedAt: '2026-08-02T09:00:00.000Z', model: 'claude-opus-5',
        input: 100, output: 2000, cacheRead: 900000, cacheCreate: 5000, turns: 30,
        counters: { bash: { git: 12 }, tool: { Bash: 40 }, keyword: { 배포: 7 } },
      },
      {
        id: 'sess-bbbb', startedAt: '2026-08-03T09:00:00.000Z', model: 'claude-sonnet-5',
        input: 50, output: 1000, cacheRead: 300000, cacheCreate: 2000, turns: 10,
        counters: { tool: { Read: 5 } },
      },
    ],
  });
  await ingest({
    machine: 'pc-2',
    user: 'user-b',
    sessions: [{
      id: 'sess-cccc', startedAt: '2026-08-03T11:00:00.000Z', model: 'claude-opus-5',
      input: 10, output: 500, cacheRead: 100000, cacheCreate: 1000, turns: 5,
      counters: { tool: { Edit: 3 } },
    }],
  });
}

/*
 * 신버전 수집기가 보낸 세션 — 시간 버킷이 붙는다. **별도 시드**로 둔다.
 *
 * seed() 에 합치면 기존 테스트의 세션 수 가정이 전부 흔들리고, 그러면 이 파일이 고정하던
 * 회귀(세션 3건 기준의 정렬·분포·드릴다운)를 새 픽스처가 덮어쓴다. 두 세대가 섞여 들어오는
 * 상황을 보고 싶은 블록만 이걸 추가로 부른다.
 */
async function seedSeries() {
  // sess-dddd 는 모델이 섞인 세션이다(09시 opus, 10시 opus+haiku) — 세션 행의 '최빈 모델'
  // 근사가 무엇을 감췄는지 보여주는 자리다.
  await ingest({
    machine: 'pc-3',
    user: 'jdoe',
    sessions: [{
      id: 'sess-dddd',
      startedAt: '2026-08-03T09:30:00.000Z',
      endedAt: '2026-08-03T10:45:00.000Z',
      model: 'claude-opus-5',
      input: 300, output: 3000, cacheRead: 1200000, cacheCreate: 20000, turns: 40,
      counters: { tool: { Bash: 20 } },
      series: [
        {
          hour: '2026-08-03T09', model: 'claude-opus-5',
          input: 200, output: 2000, cacheRead: 800000, cacheCreate: 12000,
          cc5m: 0, cc1h: 12000, turns: 25,
          toolErrors: 2, stopMaxTokens: 1, stopRefusal: 0,
          latencyMsSum: 125000, latencyMsMax: 30000, latencyTurns: 25,
        },
        {
          hour: '2026-08-03T10', model: 'claude-opus-5',
          input: 50, output: 700, cacheRead: 300000, cacheCreate: 6000,
          cc5m: 6000, cc1h: 0, turns: 10,
          toolErrors: 0, stopMaxTokens: 0, stopRefusal: 0,
          latencyMsSum: 40000, latencyMsMax: 12000, latencyTurns: 10,
        },
        {
          hour: '2026-08-03T10', model: 'claude-haiku-4-5',
          input: 50, output: 300, cacheRead: 100000, cacheCreate: 2000,
          cc5m: 2000, cc1h: 0, turns: 5,
          toolErrors: 0, stopMaxTokens: 0, stopRefusal: 1,
          latencyMsSum: 8000, latencyMsMax: 3000, latencyTurns: 5,
        },
      ],
    }],
  });

  // 시간 버킷이 하나뿐인 신버전 세션 — coverage 가 2가 되는 근거.
  await ingest({
    machine: 'pc-3',
    user: 'jdoe',
    sessions: [{
      id: 'sess-eeee',
      startedAt: '2026-08-02T14:00:00.000Z',
      model: 'claude-opus-5',
      input: 20, output: 200, cacheRead: 50000, cacheCreate: 1000, turns: 4,
      counters: { tool: { Read: 2 } },
      series: [{
        hour: '2026-08-02T14', model: 'claude-opus-5',
        input: 20, output: 200, cacheRead: 50000, cacheCreate: 1000,
        cc5m: 0, cc1h: 1000, turns: 4,
        latencyMsSum: 16000, latencyMsMax: 6000, latencyTurns: 4,
      }],
    }],
  });
}

// fresh() 는 SQLite 모드에서 새 데이터 디렉터리로 갈아탄다 → 테이블이 없는 상태로 시작한다.
// store.init() 이 그 자리에서 DDL 을 심는다(usage.test.js 와 같은 순서).
beforeEach(async () => { await fresh(); await store.init(); await seed(); });

describe('① 경로 소유권 — 우리 것이 아니면 흘려보낸다', () => {
  test('usage.js 소유 경로에 손대지 않는다(false 를 돌려준다)', async () => {
    for (const p of ['/api/usage/summary', '/api/usage/identity', '/api/usage']) {
      const r = await call(p);
      assert.equal(r.handled, false, `${p} 를 가로챘다 — usage.js 화면이 죽는다`);
      assert.equal(r.status, 0, `${p} 에 응답을 써버렸다`);
    }
  });

  test('전혀 다른 접두사도 흘려보낸다', async () => {
    const r = await call('/api/projects');
    assert.equal(r.handled, false);
  });

  test('우리 경로는 처리한다', async () => {
    for (const p of ['/api/usage/series', '/api/usage/distribution', '/api/usage/sessions']) {
      const r = await call(p);
      assert.equal(r.handled, true, `${p} 를 처리하지 않았다`);
      assert.equal(r.status, 200, `${p} → ${r.status} ${JSON.stringify(r.body)}`);
    }
  });
});

describe('② 화이트리스트 — 모르는 값은 400', () => {
  test('metric·group_by·sort 오타를 끊는다', async () => {
    assert.equal((await call('/api/usage/series?metric=nope')).status, 400);
    assert.equal((await call('/api/usage/series?group_by=password')).status, 400);
    assert.equal((await call('/api/usage/sessions?sort=DROP')).status, 400);
  });

  test('그룹 축 개수 상한', async () => {
    assert.equal((await call('/api/usage/series?group_by=user,model,project')).status, 200);
    assert.equal((await call('/api/usage/series?group_by=user,model,project,machine')).status, 400);
  });

  test('날짜 형식', async () => {
    assert.equal((await call('/api/usage/series?from=2026-08-01')).status, 200);
    assert.equal((await call('/api/usage/series?from=8/1/2026')).status, 400);
  });
});

describe('③ 없는 정밀도를 만들지 않는다', () => {
  /*
   * 규율: **없는 정밀도를 만들지 않는다.** 두 방향을 구분해야 이 원칙이 정확해진다.
   *
   *   day 보다 **잘게** 쪼개려면 근거 데이터가 있어야 한다 — hour 는 Phase 2 에서
   *     usage_series 라는 근거가 생겨 열렸고, minute 은 그런 데이터가 아예 없어 닫혀 있다.
   *   day 보다 **굵게** 접는 것은 근거가 필요 없다 — 이미 있는 값을 합칠 뿐이다.
   *     week 가 2026-08-04 에 그렇게 열렸다(사용자별 상세의 주간 축).
   *
   * month 도 같은 논리로 열 수 있지만 **쓰는 곳이 없어서** 닫아 둔다 — 안 쓰는 API 표면은
   * 그 자체가 비용이다. 근거가 없어서가 아니라 수요가 없어서라는 점이 minute 과 다르다.
   */
  test('interval=hour 는 200 — usage_series 가 근거를 제공한다', async () => {
    const r = await call('/api/usage/series?interval=hour');
    assert.equal(r.status, 200);
    assert.equal(r.body.interval, 'hour');
  });

  test('interval=week 는 200 — day 를 접는 롤업이라 새 근거가 필요 없다', async () => {
    const r = await call('/api/usage/series?interval=week');
    assert.equal(r.status, 200);
    assert.equal(r.body.interval, 'week');
    // 커버리지는 day 와 같다(usage_sessions 전량) — hour 만 신수집기로 제한된다.
    assert.equal(r.body.attribution, 'session-start-day');
  });

  test('주 라벨은 월요일(UTC)로 접힌다 — 같은 주는 한 칸이다', async () => {
    const r = await call('/api/usage/series?interval=week&group_by=user');
    // 시드는 2026-08-02(일)·08-03(월) 세션을 담는다. 08-02 는 07-27 주, 08-03 은 08-03 주다.
    const labels = [...new Set(r.body.series.flatMap((s) => s.points.map((p) => p.t)))].sort();
    assert.ok(labels.length, '주 라벨이 하나도 없다');
    for (const t of labels) {
      const dow = new Date(`${t}T00:00:00Z`).getUTCDay();
      assert.equal(dow, 1, `${t} 가 월요일이 아니다 — 주 경계가 어긋났다`);
    }
  });

  test('주간 합계는 일별 합계와 같다 — 접기가 값을 잃거나 만들지 않는다', async () => {
    const day = await call('/api/usage/series?interval=day&from=2026-08-01&to=2026-08-31');
    const week = await call('/api/usage/series?interval=week&from=2026-08-01&to=2026-08-31');
    const sum = (r) => r.body.series.reduce((a, s) => a + s.total, 0);
    assert.ok(sum(day) > 0, '일별 합계가 0 이면 이 비교가 성립하지 않는다');
    assert.ok(Math.abs(sum(day) - sum(week)) < 1e-9, '주간으로 접으면서 값이 달라졌다');
  });

  test('근거 없는·수요 없는 정밀도는 400 (minute·month)', async () => {
    for (const iv of ['minute', 'month']) {
      const r = await call(`/api/usage/series?interval=${iv}`);
      assert.equal(r.status, 400, `interval=${iv} 는 거절돼야 한다`);
      assert.match(r.body.error, /day\|hour\|week/);
    }
  });

  test('응답이 귀속 한계를 스스로 밝힌다 — 축마다 다른 값', async () => {
    assert.equal((await call('/api/usage/series')).body.attribution, 'session-start-day');
    assert.equal((await call('/api/usage/series?interval=hour')).body.attribution, 'turn-hour');
  });

  test('모델 귀속의 정확도도 응답이 밝힌다', async () => {
    // day 는 세션 최빈 모델 근사, hour 는 턴별 정확값이다. 화면이 이걸 구분해 말해야 한다.
    assert.equal((await call('/api/usage/series')).body.modelAttribution, 'session-dominant');
    assert.equal((await call('/api/usage/series?interval=hour')).body.modelAttribution, 'exact');
  });
});

/*
 * ⑤ 시간 버킷 — Phase 2 의 본체.
 *
 * 여기서 지키는 것은 "시간별로 나온다"가 아니라 **두 테이블을 섞지 않는다**는 규율이다.
 * hour 뷰는 usage_series 만 읽는다. 구버전 수집기를 쓰는 사람은 그 뷰에 안 나타나고,
 * 그 사실이 coverage 로 화면에 실려 나간다 — 조용히 day 데이터로 메우면 같은 화면의 두 눈금이
 * 서로 다른 모집단을 그리게 된다.
 */
describe('⑤ 시간 버킷', () => {
  // 이 블록만 신·구 세대가 섞인 상태를 본다. 다른 블록의 세션 수 가정을 건드리지 않는다.
  beforeEach(async () => { await seedSeries(); });

  test('한 세션의 시간 버킷이 여러 칸으로 갈린다', async () => {
    const r = await call('/api/usage/series?interval=hour&metric=tokens');
    assert.equal(r.status, 200);
    const all = r.body.series.flatMap((s) => s.points.map((p) => p.t));
    /*
     * seed 의 sess-dddd 는 **UTC** 09시·10시 버킷을 보낸다. 라벨은 로컬(KST, +9)로 옮겨
     * 나가므로 18시·19시가 된다 — 저장은 UTC, 집계는 로컬이라는 계약이 여기서 눈에 보인다.
     */
    assert.equal(r.body.timezone, 'KST', '응답이 시간대를 밝혀야 한다');
    assert.ok(all.includes('2026-08-03T18'), `UTC 09시 → KST 18시: ${all.join(',')}`);
    assert.ok(all.includes('2026-08-03T19'), `UTC 10시 → KST 19시: ${all.join(',')}`);
    assert.ok(!all.includes('2026-08-03T09'), 'UTC 라벨이 그대로 새어 나왔다');
  });

  test('모델이 섞인 세션이 모델별로 갈린다 — 최빈 모델 근사가 사라진다', async () => {
    const r = await call('/api/usage/series?interval=hour&group_by=model&metric=tokens');
    const labels = r.body.series.map((s) => s.label);
    assert.ok(labels.includes('claude-opus-5'), labels.join(','));
    assert.ok(labels.includes('claude-haiku-4-5'),
      `한 세션이 두 모델을 썼으면 둘 다 보여야 한다: ${labels.join(',')}`);
  });

  test('sessions 지표는 행 수가 아니라 서로 다른 세션 수다', async () => {
    /*
     * sess-dddd 는 버킷 3개(09시 opus, 10시 opus, 10시 haiku)를 만들고 09·10시 두 칸에 걸친다.
     * 중복을 걷어내야 하는 자리가 둘이다:
     *   칸 값  — 10시에 버킷이 2개(모델 2종)지만 그 시간에 살아 있던 세션은 1개다.
     *   합계   — 두 시간에 걸쳐도 세션은 1개다. 칸 값을 더하면 2가 되어 부풀어 오른다.
     */
    const r = await call('/api/usage/series?interval=hour&metric=sessions');
    const s = r.body.series.find((x) => x.label === '전체');
    const at = (t) => (s.points.find((p) => p.t === t) || {}).v;
    // UTC 10시 → KST 19시.
    assert.equal(at('2026-08-03T19'), 1, '한 시간에 모델 2종을 써도 세션은 1개다');
    assert.equal(s.total, 2, '기간 전체 세션 수는 칸 값의 합이 아니다(dddd·eeee 둘뿐)');
  });

  test('coverage 가 "이 뷰가 몇 개 세션을 덮는가"를 밝힌다', async () => {
    const r = await call('/api/usage/series?interval=hour');
    // 시간 버킷을 보낸 세션은 2개(dddd·eeee), 전체 세션은 5개.
    assert.equal(r.body.coverage.sessionsWithSeries, 2);
    assert.equal(r.body.coverage.sessionsTotal, 5);
    assert.ok(r.body.coverage.sessionsTotal > r.body.coverage.sessionsWithSeries,
      '아직 안 덮인 세션이 있다는 사실이 보여야 한다');
  });

  test('day 뷰는 시간 버킷이 없는 세션도 그대로 센다 — 두 뷰는 다른 테이블이다', async () => {
    const day = await call('/api/usage/series?metric=sessions');
    const dayTotal = day.body.series.reduce((a, s) => a + s.total, 0);
    assert.equal(dayTotal, 5, 'day 는 usage_sessions 전량을 본다(시간 버킷 유무와 무관)');
  });
});

describe('④ 시계열', () => {
  test('그룹 없이 — 날짜별 한 계열', async () => {
    const r = await call('/api/usage/series?metric=sessions');
    assert.equal(r.body.series.length, 1);
    const pts = r.body.series[0].points;
    assert.deepEqual(pts.map((x) => x.t), ['2026-08-02', '2026-08-03']);
    assert.deepEqual(pts.map((x) => x.v), [1, 2]);
  });

  test('사용자×모델 조합 — 계열이 갈린다', async () => {
    const r = await call('/api/usage/series?metric=sessions&group_by=user,model');
    const labels = r.body.series.map((s) => s.label).sort();
    assert.deepEqual(labels, ['user-a · claude-opus-5', 'user-a · claude-sonnet-5', 'user-b · claude-opus-5']);
    assert.deepEqual(r.body.groupBy, ['user', 'model']);
  });

  test('비용 지표 — 캐시읽기가 지배한다', async () => {
    const r = await call('/api/usage/series?metric=cost');
    const total = r.body.series[0].total;
    // opus-5 900k 캐시읽기 = 900000*5*0.1/1e6 = $0.45 만으로도 출력 전부보다 크다
    assert.ok(total > 0.5, `비용이 계산되지 않았다: ${total}`);
    assert.equal(typeof r.body.pricedAt, 'string');
  });

  test('표본이 잘렸는지 알려준다', async () => {
    const r = await call('/api/usage/series?limit=1');
    assert.equal(r.body.sample.truncated, true);
    assert.equal(r.body.sample.rows, 1);
  });
});

describe('⑤ 분포', () => {
  test('턴당 캐시읽기·세션 비용·턴수·적중률', async () => {
    const r = await call('/api/usage/distribution');
    const d = r.body.distributions;
    assert.equal(d.cacheReadPerTurn.n, 3);
    assert.equal(d.turnsPerSession.n, 3);
    assert.equal(d.sessionCostUsd.n, 3);
    // 턴당 캐시읽기: 30000(=900000/30) · 30000(=300000/10) · 20000(=100000/5)
    assert.equal(d.cacheReadPerTurn.max, 30000);
    assert.equal(d.cacheReadPerTurn.min, 20000);
    // 적중률은 0~1 범위
    assert.ok(d.cacheHitRate.max <= 1 && d.cacheHitRate.min > 0);
  });
});

describe('⑥ 드릴다운', () => {
  test('세션 상세 — 비용과 축별 카운터', async () => {
    const r = await call('/api/usage/sessions/sess-aaaa');
    assert.equal(r.status, 200);
    assert.equal(r.body.session.sessionId, 'sess-aaaa');
    assert.equal(r.body.cost.priced, true);
    assert.ok(r.body.cost.byAxis.cacheRead > 0);
    assert.deepEqual(r.body.counters.bash, [{ key: 'git', count: 12 }]);
  });

  test('keyword 축은 세션 단위로 나가지 않는다', async () => {
    const r = await call('/api/usage/sessions/sess-aaaa');
    assert.equal(r.body.counters.keyword, undefined,
      '세션 단위 keyword 가 노출됐다 — 프롬프트 재구성 여지가 생긴다');
  });

  test('없는 세션은 404', async () => {
    assert.equal((await call('/api/usage/sessions/nope-nope')).status, 404);
  });

  test('세션 목록 — 비용 내림차순', async () => {
    const r = await call('/api/usage/sessions?sort=cost');
    const ids = r.body.sessions.map((s) => s.sessionId);
    assert.equal(ids[0], 'sess-aaaa', '가장 비싼 세션이 맨 위가 아니다');
    assert.equal(r.body.sessions.length, 3);
  });

  test('세션 목록 — 축별 합계(byAxis)와 총 usd 는 top-N 이전의 전 윈도우 합이다', async () => {
    const r = await call('/api/usage/sessions?sort=cost');
    const b = r.body.byAxis;
    assert.ok(b && typeof b === 'object', 'byAxis 가 응답에 없다 — 비용 카드가 축별 $0 으로 떨어진다');
    for (const k of ['input', 'output', 'cacheRead', 'cacheCreate']) {
      assert.equal(typeof b[k], 'number', `byAxis.${k} 가 숫자가 아니다`);
      assert.ok(b[k] >= 0);
    }
    assert.ok(b.cacheRead > 0, '캐시읽기 축 비용이 계산되지 않았다');
    // usd ≈ 4축 합
    const axisSum = b.input + b.output + b.cacheRead + b.cacheCreate;
    assert.ok(Math.abs(r.body.usd - axisSum) < 1e-9, `usd(${r.body.usd}) ≠ 축별 합(${axisSum})`);
    // 개별 세션 usd 합과도 일치(전 윈도우 집계임을 확인)
    const perSessionSum = r.body.sessions.reduce((a, s) => a + (s.usd || 0), 0);
    assert.ok(Math.abs(r.body.usd - perSessionSum) < 1e-9, '집계 usd 가 세션 usd 합과 다르다');
  });

  test('세션 목록 — top-N 으로 잘라도 집계 usd·byAxis 는 전량 그대로다', async () => {
    const full = await call('/api/usage/sessions?sort=cost');
    const one = await call('/api/usage/sessions?sort=cost&top=1');
    assert.equal(one.body.sessions.length, 1, 'top=1 은 세션을 1개만 담아야 한다');
    // 목록은 잘려도 합계는 전 윈도우 기준이라 동일해야 한다(top-N 오염 방지 계약)
    assert.ok(Math.abs(one.body.usd - full.body.usd) < 1e-9, 'top-N 이 집계 총액을 오염시켰다');
    assert.deepEqual(one.body.byAxis, full.body.byAxis, 'top-N 이 축별 합계를 오염시켰다');
  });
});

describe('⑦ store 신규 조회구', () => {
  test('sessionRows 필터 — 사용자·모델·날짜', async () => {
    assert.equal((await store.sessionRows({ username: 'user-b' })).length, 1);
    assert.equal((await store.sessionRows({ model: 'claude-opus-5' })).length, 2);
    assert.equal((await store.sessionRows({ from: '2026-08-03' })).length, 2);
    // 상한 경계 — to 가 그날 00:00 이후를 잘라먹지 않아야 한다
    assert.equal((await store.sessionRows({ to: '2026-08-03' })).length, 3);
    assert.equal((await store.sessionRows({ to: '2026-08-02' })).length, 1);
  });

  test('sessionById 는 전량을 훑지 않고 한 건을 집는다', async () => {
    const row = await store.sessionById('sess-bbbb');
    assert.equal(row.model, 'claude-sonnet-5');
    assert.equal(await store.sessionById('nope'), null);
    assert.equal(await store.sessionById(''), null);
  });

  test('countersOf 는 요청한 축만 준다', async () => {
    const only = await store.countersOf('sess-aaaa', ['bash']);
    assert.deepEqual(Object.keys(only), ['bash']);
    const bogus = await store.countersOf('sess-aaaa', ['bogus']);
    assert.deepEqual(bogus, {}, '화이트리스트 밖 축이 통과했다');
  });
});

describe('⑧ 사람별 리더보드', () => {
  test('전 윈도우 사용자별 집계 — top-N 에 안 잘리고 비용순', async () => {
    const r = await call('/api/usage/leaderboard');
    assert.equal(r.status, 200);
    const names = r.body.users.map((u) => u.username);
    assert.deepEqual(names.slice().sort(), ['user-a', 'user-b'], '두 사용자 전원이 잡혀야 한다');
    // userA 는 두 세션(aaaa+bbbb), userB 은 한 세션(cccc)
    const userA = r.body.users.find((u) => u.username === 'user-a');
    assert.equal(userA.sessions, 2);
    assert.equal(userA.turns, 40, '30+10 턴 합');
    assert.ok(userA.usd > 0 && userA.priced, '비용이 계산돼야 한다');
    assert.ok(userA.cacheHitRate > 0 && userA.cacheHitRate <= 1);
    // 비용순 정렬 — userA(캐시읽기 120만) > userB(10만)
    assert.equal(r.body.users[0].username, 'user-a', '더 비싼 사용자가 위');
  });
});

describe('⑨ 품질축', () => {
  test('시간 버킷 없으면 turns=0 (커버리지만)', async () => {
    const r = await call('/api/usage/quality');
    assert.equal(r.status, 200);
    assert.equal(r.body.turns, 0, '신수집기 데이터가 없으면 품질 지표를 만들지 않는다');
    assert.equal(r.body.coverage.sessionsWithSeries, 0);
    assert.equal(r.body.coverage.sessionsTotal, 3);
  });

  test('시간 버킷이 있으면 오류·거부·지연을 합산한다', async () => {
    await seedSeries();
    const r = await call('/api/usage/quality');
    // seedSeries: toolErrors 2, stopRefusal 1, stopMaxTokens 1 (dddd 3버킷) + eeee
    assert.equal(r.body.toolErrors, 2);
    assert.equal(r.body.stopRefusal, 1);
    assert.equal(r.body.stopMaxTokens, 1);
    assert.ok(r.body.toolErrorRate > 0 && r.body.toolErrorRate < 1);
    // 평균 지연 = latency_ms_sum / latency_turns, 양수
    assert.ok(r.body.latencyAvgMs > 0);
    assert.ok(r.body.latencyMaxMs >= 30000, '최대 지연은 버킷 최대값(30s) 이상');
    assert.equal(r.body.coverage.sessionsWithSeries, 2, 'dddd·eeee 두 세션이 시간 버킷을 보냈다');
  });
});

describe('⑩ 수집 커버리지', () => {
  test('발신처(머신)별 마지막 보고·세션 수·신수집기 여부', async () => {
    await seedSeries();
    const r = await call('/api/usage/coverage');
    assert.equal(r.status, 200);
    const byMachine = Object.fromEntries(r.body.reporters.map((x) => [x.machine, x]));
    assert.ok(byMachine['pc-1'] && byMachine['pc-2'] && byMachine['pc-3']);
    assert.equal(byMachine['pc-1'].sessions, 2, 'pc-1 은 두 세션');
    // pc-3 만 시간 버킷(신수집기)을 보냈다
    assert.equal(byMachine['pc-3'].sendsSeries, true);
    assert.equal(byMachine['pc-1'].sendsSeries, false);
    assert.ok(byMachine['pc-1'].lastReportedAt, '마지막 보고 시각이 있어야 한다');
    assert.ok(r.body.now, 'now 기준 시각을 함께 준다');
  });
});

/*
 * ⑫ 시간대 — 저장은 UTC, 집계 라벨은 로컬(KST).
 *
 * 이 블록이 지키는 것은 **심야 귀속**이다. KST 하루는 UTC [D-1 15:00, D 15:00) 이라,
 * 00:00~09:00 KST 에 한 일이 UTC 기준으로는 전날이다. 개발자가 가장 흔하게 일하는 시간대라
 * 이게 틀리면 "어제 아무도 일 안 했네" 같은 거짓 결론이 나온다(2026-08-04 실측 결함).
 */
describe('⑫ 시간대 — 심야 KST 가 전날로 밀리지 않는다', () => {
  beforeEach(async () => {
    await ingest({
      machine: 'pc-tz', user: 'tzuser',
      sessions: [
        {
          // 2026-08-04 01:00 KST — UTC 로는 08-03 이다. **08-04 로 집계돼야 한다.**
          id: 'sess-late0001', startedAt: '2026-08-03T16:00:00.000Z',
          model: 'claude-opus-5', input: 10, output: 100, cacheRead: 1000, cacheCreate: 10, turns: 1,
        },
        {
          // 2026-08-03 23:59 KST — UTC 로는 08-03 14:59. 08-03 그대로여야 한다(경계 직전).
          id: 'sess-eve00001', startedAt: '2026-08-03T14:59:00.000Z',
          model: 'claude-opus-5', input: 20, output: 200, cacheRead: 2000, cacheCreate: 20, turns: 1,
        },
      ],
    });
  });

  test('UTC 15:00 이후 세션이 다음 날로 집계된다', async () => {
    const r = await call('/api/usage/series?metric=tokens&group_by=user');
    const s = r.body.series.find((x) => x.label === 'tzuser');
    const days = s.points.map((p) => p.t).sort();
    assert.deepEqual(days, ['2026-08-03', '2026-08-04'],
      `심야 세션이 전날로 밀렸다(또는 경계 직전이 밀렸다): ${days.join(',')}`);
    const at = (t) => s.points.find((p) => p.t === t).v;
    assert.equal(at('2026-08-04'), 1120, '01:00 KST 세션(합 1120)이 08-04 에 없다');
    assert.equal(at('2026-08-03'), 2240, '23:59 KST 세션(합 2240)이 08-03 에 없다');
  });

  test('하루만 조회해도 경계 행이 사라지지 않는다 — 넓혀 뜨고 정확히 거른다', async () => {
    /*
     * SQL 필터는 UTC 라벨 위에서 돈다. 넓히지 않으면 08-04 를 요청했을 때 UTC 08-03 인
     * 심야 세션이 **애초에 조회되지 않아** 조용히 사라진다. 이 검사가 그 회귀를 잡는다.
     */
    const r = await call('/api/usage/series?metric=tokens&group_by=user&from=2026-08-04&to=2026-08-04');
    const s = r.body.series.find((x) => x.label === 'tzuser');
    assert.ok(s, '심야 세션이 통째로 사라졌다 — 조회 범위를 넓히지 않았다');
    assert.deepEqual(s.points.map((p) => p.t), ['2026-08-04']);
    assert.equal(s.points[0].v, 1120);
  });

  test('넓혀 뜬 앞뒤 날이 결과에 새어 들어오지 않는다', async () => {
    const r = await call('/api/usage/series?metric=tokens&group_by=user&from=2026-08-04&to=2026-08-04');
    const all = r.body.series.flatMap((x) => x.points.map((p) => p.t));
    assert.ok(all.every((t) => t === '2026-08-04'),
      `범위 밖 날짜가 섞였다: ${[...new Set(all)].join(',')}`);
  });

  test('주간도 로컬 기준으로 접힌다', async () => {
    const r = await call('/api/usage/series?metric=tokens&interval=week&group_by=user');
    const s = r.body.series.find((x) => x.label === 'tzuser');
    // 08-03(월)·08-04(화) 둘 다 08-03 주다 — 한 칸으로 합쳐져야 한다.
    assert.equal(s.points.length, 1, `같은 주가 두 칸으로 갈렸다: ${s.points.map((p) => p.t).join(',')}`);
    assert.equal(s.points[0].t, '2026-08-03');
    assert.equal(s.points[0].v, 1120 + 2240, '주간 합계가 일별 합과 다르다');
  });

  test('응답이 시간대를 밝힌다 — 원본과 대조하는 사람이 오해하지 않게', async () => {
    const r = await call('/api/usage/series');
    assert.equal(r.body.timezone, 'KST');
  });
});

/*
 * ⑥ 서브에이전트·스킬 활용 — 사람별로 갈려 보이는가.
 *
 * 왜(2026-08-04 실측): tech-lead 지침은 모든 세션에 실리는데 팬아웃은 사람마다 갈렸다 —
 * 한 사람은 역할 에이전트 65회, 다른 사람은 0회에 general-purpose 만 25회였다.
 * 전체 합(topKeys)으로는 이 차이가 보이지 않아 아무도 몰랐다. 측정이 없으면 개선도 없다.
 */
describe('⑥ 팬아웃 측정 — /api/usage/dispatch', () => {
  test('경로를 처리하고 agent·skill 축을 사람별로 돌려준다', async () => {
    const r = await call('/api/usage/dispatch');
    assert.equal(r.handled, true, '경로를 처리하지 않았다');
    assert.equal(r.status, 200, JSON.stringify(r.body));
    assert.ok(Array.isArray(r.body.agents), 'agents 축이 없다');
    assert.ok(Array.isArray(r.body.skills), 'skills 축이 없다');
    // 범용 목록을 함께 내려야 화면이 "무엇이 범용인가"를 스스로 정하지 않는다.
    assert.ok(Array.isArray(r.body.generic) && r.body.generic.includes('general-purpose'),
      '범용 에이전트 목록이 없다 — 화면과 서버의 판정이 갈린다');
  });

  test('역할 수와 범용 수가 갈려 나온다 — 합만 주면 문제를 못 본다', async () => {
    const store = require('../lib/store');
    await store.countersUpsert({
      sessionId: 'sess-fanout-a', username: 'does-fanout', startedAt: '2026-08-04T00:00:00Z',
      rows: [
        { kind: 'agent', key: 'backend-engineer', count: 7 },
        { kind: 'agent', key: 'general-purpose', count: 2 },
      ],
    });
    await store.countersUpsert({
      sessionId: 'sess-fanout-b', username: 'no-fanout', startedAt: '2026-08-04T00:00:00Z',
      rows: [{ kind: 'agent', key: 'general-purpose', count: 9 }],
    });

    const r = await call('/api/usage/dispatch');
    const byName = new Map((r.body.agents || []).map((u) => [u.username, u]));
    const doer = byName.get('does-fanout');
    const none = byName.get('no-fanout');
    assert.ok(doer && none, `두 사용자가 갈려 나오지 않았다: ${JSON.stringify(r.body.agents)}`);
    assert.equal(doer.role, 7, '역할 에이전트 수가 틀렸다');
    assert.equal(doer.generic, 2, '범용 수가 틀렸다');
    assert.equal(none.role, 0, '팬아웃 0 이 0 으로 나오지 않으면 문제를 못 본다');
    assert.equal(none.generic, 9);
  });
});
