'use strict';
/*
 * 시간 버킷(usage_series) — 수집·저장·가격의 계약.
 *
 * 이 파일이 잡으려는 회귀는 셋이고, 셋 다 **배포 후에야 드러나는** 종류다:
 *
 *   ① 멱등성. 수집기는 트랜스크립트를 다시 읽어 절대값을 재전송한다. 저장이 `+=` 로 바뀌는
 *      순간 훅이 두 번 도는 것만으로 값이 두 배가 되고, 화면에는 "사용량이 늘었다"로 보인다.
 *      0014 가 명시한 실패 모드이고, 같은 규율을 0017 이 승계한다.
 *
 *   ② 세대 공존. 팩은 팀원 머신에 하루 1회 스로틀로 동기화된다. 발행 직후 며칠은 구버전
 *      수집기(series 없음)와 신버전이 **섞여** 들어온다. 구버전을 거절하면 그 기간 동안 그
 *      사람들의 사용량이 통째로 사라진다.
 *
 *   ③ TTL 가격. 캐시 생성은 5분이면 1.25배, 1시간이면 2배다. 실측(2026-08-03)에서 표본의
 *      100%가 1시간이었고, 5분으로 뭉뚱그리던 계산은 실제의 1/1.60 이었다. 배수를 다시
 *      뭉뚱그리는 회귀는 총액이 조용히 낮아지는 형태로만 나타난다.
 */
const { test, describe, beforeEach, before, after } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');

process.env.USAGE_DATA_DIR = fs.mkdtempSync(path.join(os.tmpdir(), 'usage-series-'));
const adapter = require('../lib/db/adapter');
const { pgActive } = require('./helper');
const store = require('../lib/store');
const intake = require('../lib/intake');
const cost = require('../lib/cost');

// 앰비언트 config.json 의 usage.pricing 이 산술을 흔들지 않게 빈 설정을 물린다.
let prevCfg; let tmpCfg;
before(() => {
  prevCfg = process.env.USAGE_CONFIG;
  tmpCfg = path.join(os.tmpdir(), `usage-series-cfg-${process.pid}.json`);
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
    await q('DELETE FROM usage_series').run();
    await q('DELETE FROM usage_sessions').run();
    return;
  }
  adapter.close();
  process.env.USAGE_DATA_DIR = fs.mkdtempSync(path.join(os.tmpdir(), 'usage-series-'));
}

async function ingest(payload) {
  let buckets = 0;
  for (const { session, series } of intake.normPayload(payload)) {
    await store.sessionUpsert(session);
    if (series && series.length) {
      buckets += await store.seriesUpsert({
        sessionId: session.sessionId, username: session.username,
        machine: session.machine, project: session.project, rows: series,
      });
    }
  }
  return buckets;
}

const PAYLOAD = {
  machine: 'pc-1',
  user: 'user-a',
  sessions: [{
    id: 'sess-idem01',
    startedAt: '2026-08-03T09:10:00.000Z',
    endedAt: '2026-08-03T10:20:00.000Z',
    model: 'claude-opus-5',
    input: 250, output: 2700, cacheRead: 1100000, cacheCreate: 18000, turns: 35,
    series: [
      {
        hour: '2026-08-03T09', model: 'claude-opus-5',
        input: 200, output: 2000, cacheRead: 800000, cacheCreate: 12000,
        cc5m: 0, cc1h: 12000, turns: 25,
        toolErrors: 2, stopMaxTokens: 1, stopRefusal: 0,
        latencyMsSum: 125000, latencyMsMax: 30000, latencyTurns: 25,
      },
      {
        hour: '2026-08-03T10', model: 'claude-haiku-4-5',
        input: 50, output: 700, cacheRead: 300000, cacheCreate: 6000,
        cc5m: 6000, cc1h: 0, turns: 10,
        toolErrors: 0, stopMaxTokens: 0, stopRefusal: 1,
        latencyMsSum: 40000, latencyMsMax: 12000, latencyTurns: 10,
      },
    ],
  }],
};

beforeEach(async () => { await fresh(); await store.init(); });

describe('① 멱등성 — 절대값 UPSERT', () => {
  test('같은 페이로드를 두 번 넣어도 값이 변하지 않는다', async () => {
    await ingest(PAYLOAD);
    const once = await store.seriesOf('sess-idem01');
    await ingest(PAYLOAD);
    const twice = await store.seriesOf('sess-idem01');

    assert.equal(twice.length, once.length, '행이 늘었다 — PK 가 안 걸렸다');
    assert.deepEqual(twice, once, '값이 변했다 — 누적(+=)으로 회귀했다');
  });

  test('열 번 넣어도 마찬가지다(훅 재시도의 실제 모양)', async () => {
    for (let i = 0; i < 10; i += 1) await ingest(PAYLOAD);
    const rows = await store.seriesOf('sess-idem01');
    assert.equal(rows.length, 2);
    assert.equal(rows.find((r) => r.hour === '2026-08-03T09').cacheRead, 800000);
    assert.equal(rows.find((r) => r.hour === '2026-08-03T09').turns, 25);
  });

  test('세션이 이어져 값이 커지면 그 값으로 **덮인다**(더해지지 않는다)', async () => {
    await ingest(PAYLOAD);
    // 같은 세션·같은 시간 버킷이 더 자란 뒤 재전송되는 정상 경로.
    const grown = JSON.parse(JSON.stringify(PAYLOAD));
    grown.sessions[0].series[0].turns = 40;
    grown.sessions[0].series[0].cacheRead = 900000;
    await ingest(grown);

    const r = (await store.seriesOf('sess-idem01')).find((x) => x.hour === '2026-08-03T09');
    assert.equal(r.turns, 40, '최신 절대값으로 덮여야 한다');
    assert.equal(r.cacheRead, 900000);
  });
});

describe('② 세대 공존 — 구버전 수집기를 거절하지 않는다', () => {
  test('series 가 없는 페이로드도 세션은 정상 저장된다', async () => {
    const buckets = await ingest({
      machine: 'pc-old',
      user: 'legacy',
      sessions: [{
        id: 'sess-old001', startedAt: '2026-08-03T09:00:00.000Z', model: 'claude-opus-5',
        input: 10, output: 100, cacheRead: 5000, cacheCreate: 500, turns: 3,
      }],
    });
    assert.equal(buckets, 0, '없는 버킷을 지어내지 않는다');
    const s = await store.sessionById('sess-old001');
    assert.ok(s, '구버전 보고가 거절되면 그 사람 사용량이 통째로 사라진다');
    assert.equal(s.turns, 3);
    assert.equal(s.endedAt, null, '안 보낸 값은 "모른다"로 남는다 — 0 으로 지어내지 않는다');
  });

  test('series 가 배열이 아니어도 죽지 않는다(팩 드리프트 흡수)', async () => {
    for (const bad of [null, 'x', 42, {}]) {
      const [n] = intake.normPayload({
        sessions: [{ id: 'sess-bad0001', startedAt: '2026-08-03T09:00:00.000Z', series: bad }],
      });
      assert.deepEqual(n.series, [], `series=${JSON.stringify(bad)} 에서 깨졌다`);
    }
  });

  test('시간 라벨 형식이 아닌 버킷은 조용히 버린다', async () => {
    const [n] = intake.normPayload({
      sessions: [{
        id: 'sess-bad0002',
        startedAt: '2026-08-03T09:00:00.000Z',
        series: [
          { hour: '2026-08-03', model: 'm', turns: 1 },          // 날짜만 — 시간이 없다
          { hour: '2026-08-03T09:00:00Z', model: 'm', turns: 1 }, // 너무 길다
          { hour: 'yesterday', model: 'm', turns: 1 },
          { hour: '2026-08-03T09', model: 'm', turns: 1 },        // 유일하게 유효
        ],
      }],
    });
    assert.equal(n.series.length, 1);
    assert.equal(n.series[0].hour, '2026-08-03T09');
  });

  test('같은 (시간, 모델)이 중복으로 오면 하나만 남는다', async () => {
    const [n] = intake.normPayload({
      sessions: [{
        id: 'sess-dup00001',
        startedAt: '2026-08-03T09:00:00.000Z',
        series: [
          { hour: '2026-08-03T09', model: 'a', turns: 1 },
          { hour: '2026-08-03T09', model: 'a', turns: 9 },
          { hour: '2026-08-03T09', model: 'b', turns: 1 },
        ],
      }],
    });
    assert.equal(n.series.length, 2, '(시간,모델)은 PK 다 — 중복이 행 수를 부풀리면 안 된다');
  });

  test('모델이 비면 (미상) 으로 모은다 — 빈 문자열 키를 만들지 않는다', async () => {
    const [n] = intake.normPayload({
      sessions: [{
        id: 'sess-nomodel1',
        startedAt: '2026-08-03T09:00:00.000Z',
        series: [{ hour: '2026-08-03T09', turns: 1 }],
      }],
    });
    assert.equal(n.series[0].model, '(미상)');
  });
});

describe('③ TTL 가격 — 5분과 1시간을 뭉뚱그리지 않는다', () => {
  const M = 1e6;

  test('1시간 TTL 은 5분보다 정확히 1.6배 비싸다', async () => {
    const base = { model: 'claude-opus-5', input: 0, output: 0, cacheRead: 0 };
    const c5 = cost.costOf({ ...base, cacheCreate: M, cacheCreate5m: M, cacheCreate1h: 0 });
    const c1 = cost.costOf({ ...base, cacheCreate: M, cacheCreate5m: 0, cacheCreate1h: M });

    // opus-5 입력가 $5/MTok → 5분 $6.25, 1시간 $10.00
    assert.equal(Number(c5.byAxis.cacheCreate.toFixed(4)), 6.25);
    assert.equal(Number(c1.byAxis.cacheCreate.toFixed(4)), 10);
    assert.equal(Number((c1.usd / c5.usd).toFixed(2)), 1.6,
      '이 비율이 1 로 돌아오면 TTL 을 다시 뭉뚱그린 것이다');
  });

  test('분해값이 있으면 총량(cacheCreate)이 아니라 분해값을 쓴다', async () => {
    // 총량이 0 이어도 분해값이 있으면 가격이 매겨져야 한다(실측 표본에 4건 있던 모양).
    const c = cost.costOf({
      model: 'claude-opus-5', cacheCreate: 0, cacheCreate5m: 0, cacheCreate1h: M,
    });
    assert.equal(c.ttlKnown, true);
    assert.equal(Number(c.byAxis.cacheCreate.toFixed(4)), 10);
  });

  test('분해값이 없으면 5분으로 가정하되 ttlKnown:false 로 밝힌다', async () => {
    const c = cost.costOf({ model: 'claude-opus-5', cacheCreate: M });
    assert.equal(c.ttlKnown, false, '모르는 것을 안다고 말하면 안 된다');
    assert.equal(Number(c.byAxis.cacheCreate.toFixed(4)), 6.25);
  });

  test('summarize 가 TTL 미상 행 수를 세어 올린다', async () => {
    const s = cost.summarize([
      { model: 'claude-opus-5', cacheCreate: M, cacheCreate1h: M },  // 앎
      { model: 'claude-opus-5', cacheCreate: M },                     // 모름
      { model: 'claude-opus-5', cacheCreate: M },                     // 모름
      { model: 'claude-opus-5', input: 10 },                          // 캐시생성 자체가 없다 → 세지 않는다
    ]);
    assert.equal(s.ttlUnknownRows, 2,
      '이 값이 0 으로 굳으면 화면이 과소 추정을 과소 추정이라 말하지 못한다');
  });

  test('저장을 왕복해도 TTL 분해가 살아남는다', async () => {
    await ingest(PAYLOAD);
    const rows = await store.seriesOf('sess-idem01');
    const h9 = rows.find((r) => r.hour === '2026-08-03T09');
    const h10 = rows.find((r) => r.hour === '2026-08-03T10');
    assert.equal(h9.cacheCreate1h, 12000);
    assert.equal(h9.cacheCreate5m, 0);
    assert.equal(h10.cacheCreate5m, 6000);
    assert.equal(h10.cacheCreate1h, 0);

    // 그리고 그 분해값이 실제로 가격에 반영된다.
    const c = cost.costOf(h9);
    assert.equal(c.ttlKnown, true);
  });
});

describe('④ 품질 축과 지연', () => {
  test('오류·stop_reason·지연이 왕복에서 보존된다', async () => {
    await ingest(PAYLOAD);
    const h9 = (await store.seriesOf('sess-idem01')).find((r) => r.hour === '2026-08-03T09');
    assert.equal(h9.toolErrors, 2);
    assert.equal(h9.stopMaxTokens, 1);
    assert.equal(h9.latencyTurns, 25);
    assert.equal(h9.latencyMsMax, 30000);
    assert.equal(Math.round(h9.latencyMsSum / h9.latencyTurns), 5000);
  });

  test('세션 종료 시각이 저장된다 — duration 을 물을 수 있다', async () => {
    await ingest(PAYLOAD);
    const s = await store.sessionById('sess-idem01');
    assert.equal(s.endedAt, '2026-08-03T10:20:00.000Z');
  });
});

describe('⑤ 상한과 보존', () => {
  test('세션당 버킷 수에 상한이 있다', async () => {
    const many = [];
    for (let d = 1; d <= 20; d += 1) {
      for (let h = 0; h < 24; h += 1) {
        many.push({
          hour: `2026-08-${String(d).padStart(2, '0')}T${String(h).padStart(2, '0')}`,
          model: 'claude-opus-5', turns: 1, input: 1,
        });
      }
    }
    const [n] = intake.normPayload({
      sessions: [{ id: 'sess-many0001', startedAt: '2026-08-01T00:00:00.000Z', series: many }],
    });
    assert.ok(n.series.length <= store.MAX_SERIES_PER_SESSION,
      `상한이 없으면 세션 하나가 테이블을 채운다 (${n.series.length})`);
  });

  test('보존 기한이 지난 버킷은 지운다 — 키워드보다 훨씬 길게 둔다', async () => {
    await ingest(PAYLOAD);
    await ingest({
      machine: 'pc-1',
      user: 'user-a',
      sessions: [{
        id: 'sess-old00002', startedAt: '2020-01-01T00:00:00.000Z',
        series: [{ hour: '2020-01-01T00', model: 'claude-opus-5', turns: 1, input: 1 }],
      }],
    });
    const before2 = await store.seriesRows({});
    assert.equal(before2.length, 3);

    const r = await store.pruneSeries({ now: new Date('2026-08-03T00:00:00.000Z') });
    assert.equal(r.removed, 1, '2020년 버킷만 지워져야 한다');
    assert.equal((await store.seriesRows({})).length, 2);
    assert.ok(r.days >= 365, `기본 보존 기한이 짧아졌다(${r.days}일) — 비용 추세는 길게 봐야 한다`);
  });
});
