'use strict';
/*
 * 모델별 집계(usageByModel) — 오귀속을 고치면서 **아무것도 잃지 않는가**.
 *
 * 배경(2026-08-05 실측, 세 갈래 점검이 각자 다른 방법으로 같은 결론에 닿았다):
 * `usage_sessions.model` 은 모델 축이 아니라 그 세션의 **최빈 모델 1개**다. 그것으로 GROUP BY
 * 하면 모델이 섞인 세션의 토큰이 통째로 한 칸에 들어간다 — 실측 수치 8개가 이 왜곡으로 오차 0
 * 재현됐다. 총합은 맞고 행만 틀린 결함이라, 사람에게는 "서버가 더 최신인데 값이 적다"(=유실)로
 * 보였다. 유실이 아니었다.
 *
 * 이 파일이 못박는 불변식은 넷이고, 넷 다 **깨져도 화면은 멀쩡해 보인다**:
 *
 *   ① 총합 불변. series 몫 + 세션 폴백 몫 = 종전 총합(= usage_sessions 합 = totals 카드).
 *      series 로 그냥 갈아타면 이게 조용히 줄어든다. 같은 화면의 다른 카드와 어긋나는 순간
 *      그 차이가 다시 "유실"로 읽힌다.
 *   ② 다중모델 세션이 **정확히** 쪼개진다(usage_series 는 PK 에 model 이 있다).
 *   ③ series 가 없는 세션이 **사라지지 않는다**. 커버리지는 사람마다 다르다(실측: 91.3% ·
 *      100% · **2.2%**). 버리면 커버리지 낮은 사람의 모델별이 통째로 사라진다 — 오귀속을
 *      고치면서 더 큰 거짓말을 만드는 것이다.
 *   ④ 근사의 **몫이 밝혀진다**(fromSeries/fromSession + 사람별 커버리지). 밝히지 않으면
 *      오귀속을 정확한 값으로 위장하는 것이고, 그게 이번 결함의 본질이었다.
 *
 * 스키마는 건드리지 않는다 — 두 테이블 다 그대로 두고 읽는 방법만 바꿨다. 그래서 이 교정은
 * 과거분까지 소급된다(pruneSeries 는 호출부가 없어 usage_series 가 온전하다).
 */
const { test, describe, beforeEach } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');

// 어댑터/모듈 로드 전에 데이터 디렉터리를 임시로 돌린다(usage.test.js 와 같은 격리 규율).
process.env.USAGE_DATA_DIR = fs.mkdtempSync(path.join(os.tmpdir(), 'usage-bymodel-'));
const adapter = require('../lib/db/adapter');
const { pgActive } = require('./helper');
const store = require('../lib/store');
const intake = require('../lib/intake');
const usageRoutes = require('../routes/usage');

/*
 * 브라우저의 URL 루트를 노드에서 흉내낸다.
 *
 * 뷰(public/views/*.js)는 공용 모듈을 `import { api } from '/js/core.js'` 로 — 절대 URL 로 —
 * 부른다. 정적 루트가 public/ 이라 URL 에서 그 한 칸이 생략되고, 그래서 브라우저와 노드 양쪽에서
 * 동시에 맞는 상대경로가 존재하지 않기 때문이다.
 * 브라우저에는 그게 정답이지만 노드는 `/js/core.js` 를 **파일시스템 루트**로 읽어 죽는다.
 * 그래서 서버의 정적 매핑과 같은 규칙(`/js/**` → `<repo>/public/js/**`)만 되돌려 준다.
 */
const { pathToFileURL } = require('url');
require('node:module').registerHooks({
  resolve(spec, ctx, next) {
    if (spec.startsWith('/js/') && spec.endsWith('.js') && !spec.split('/').includes('..')) {
      return { url: pathToFileURL(path.join(__dirname, '..', 'public', spec.slice(1))).href, shortCircuit: true };
    }
    return next(spec, ctx);
  },
});

async function fresh() {
  if (pgActive()) {
    const { q } = require('../lib/db');
    await q('DELETE FROM usage_series').run();
    await q('DELETE FROM usage_counters').run();
    await q('DELETE FROM usage_sessions').run();
    return;
  }
  adapter.close();
  process.env.USAGE_DATA_DIR = fs.mkdtempSync(path.join(os.tmpdir(), 'usage-bymodel-'));
}

async function ingest(payload) {
  for (const { session, rows, series } of intake.normPayload(payload)) {
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

const AXES = ['input', 'output', 'cacheRead', 'cacheCreate'];
function sumAxes(rows, pick = (r) => r) {
  const out = { input: 0, output: 0, cacheRead: 0, cacheCreate: 0 };
  for (const r of rows) for (const a of AXES) out[a] += Number(pick(r)[a]) || 0;
  return out;
}
function byModel(rows, model) {
  const r = rows.find((x) => x.model === model);
  assert.ok(r, `${model} 행이 없다 — 그 모델의 사용량이 통째로 사라졌다`);
  return r;
}

/*
 * 픽스처 — 세 세대가 섞여 들어오는 실제 모양이다.
 *
 *   userA  sess-mixed  series 가 세션을 100% 덮는다. **모델 3종이 섞였다** — 종전 집계는
 *                      3,000 출력 전부를 최빈 모델(opus-4-8) 한 칸에 넣었다.
 *   userA  sess-part   series 가 60% 만 덮는다(시각이 파싱 안 된 턴이 있는 세션의 모양).
 *                      나머지 40% 를 버리면 총합이 줄어든다.
 *   userB  no-series ×2 + 있음 ×1  → 커버리지 33.3%. series 로 갈아타기만 하면 이 사람의
 *                      모델별 값 대부분이 사라진다.
 */
const MIXED = {
  machine: 'pc-a', user: 'user-a',
  sessions: [{
    id: 'sess-mixed-0001', startedAt: '2026-08-03T09:00:00.000Z', endedAt: '2026-08-03T10:30:00.000Z',
    // 수집기가 고른 최빈 모델. 세션 행의 토큰은 **세 모델의 합**이다.
    model: 'claude-opus-4-8',
    input: 300, output: 3000, cacheRead: 30000, cacheCreate: 3000, turns: 30,
    series: [
      { hour: '2026-08-03T09', model: 'claude-opus-4-8', input: 100, output: 1000, cacheRead: 10000, cacheCreate: 1000, turns: 10 },
      { hour: '2026-08-03T09', model: 'claude-fable-5', input: 150, output: 1500, cacheRead: 15000, cacheCreate: 1500, turns: 12 },
      { hour: '2026-08-03T10', model: 'claude-haiku-4-5', input: 50, output: 500, cacheRead: 5000, cacheCreate: 500, turns: 8 },
    ],
  }],
};
const PARTIAL = {
  machine: 'pc-a', user: 'user-a',
  sessions: [{
    id: 'sess-part-0002', startedAt: '2026-08-03T11:00:00.000Z', model: 'claude-opus-5',
    input: 100, output: 1000, cacheRead: 10000, cacheCreate: 1000, turns: 20,
    series: [
      { hour: '2026-08-03T11', model: 'claude-opus-5', input: 60, output: 600, cacheRead: 6000, cacheCreate: 600, turns: 12 },
    ],
  }],
};
const OLDGEN = {
  machine: 'pc-b', user: 'user-b',
  sessions: [
    { id: 'sess-nos-0003', startedAt: '2026-08-03T12:00:00.000Z', model: 'claude-sonnet-5', input: 10, output: 500, cacheRead: 1000, cacheCreate: 100, turns: 2 },
    { id: 'sess-nos-0004', startedAt: '2026-08-03T13:00:00.000Z', model: 'claude-fable-5', input: 20, output: 700, cacheRead: 2000, cacheCreate: 200, turns: 3 },
    {
      id: 'sess-ys-0005', startedAt: '2026-08-03T14:00:00.000Z', model: 'claude-opus-5',
      input: 5, output: 100, cacheRead: 500, cacheCreate: 50, turns: 2,
      series: [{ hour: '2026-08-03T14', model: 'claude-opus-5', input: 5, output: 100, cacheRead: 500, cacheCreate: 50, turns: 2 }],
    },
  ],
};
// 종전 집계(= usage_sessions 합)의 진실값. ①의 기준선이라 손으로 적는다 — 코드로 계산하면
// 같은 결함을 공유해 서로를 통과시킨다.
const LEGACY_TOTAL = { input: 435, output: 5300, cacheRead: 43500, cacheCreate: 4350 };

async function seedAll() {
  await ingest(MIXED);
  await ingest(PARTIAL);
  await ingest(OLDGEN);
}

describe('① 총합 불변 — series 몫 + 세션 폴백 몫 = 종전 총합', () => {
  beforeEach(async () => { await fresh(); await store.init(); });

  test('모델별 합이 usage_sessions 합과 정확히 같다', async () => {
    await seedAll();
    const rows = await store.usageByModel();
    assert.deepEqual(sumAxes(rows), LEGACY_TOTAL,
      '모델별 총합이 세션 총합과 다르다 — series 로 갈아타며 폴백/잔여를 버렸다');
  });

  test('세션 행이 없는 고아 버킷은 총합을 부풀리지 않는다', async () => {
    await seedAll();
    // 라이브에 실재하는 조건이다(인테이크가 세션 행만 실패하고 버킷은 들어가는 자리 —
    // routes/usage.js 의 세션 단위 catch). 이걸 더하면 모델별만 totals 카드보다 커진다.
    await store.seriesUpsert({
      sessionId: 'sess-orphan-9999', username: 'ghost', machine: 'pc-ghost',
      rows: [{ hour: '2026-08-03T09', model: 'claude-orphan-9', input: 7, output: 7000, cacheRead: 70, cacheCreate: 7 }],
    });
    const rows = await store.usageByModel();
    assert.deepEqual(sumAxes(rows), LEGACY_TOTAL, '고아 버킷이 총합에 섞였다');
    assert.equal(rows.find((r) => r.model === 'claude-orphan-9'), undefined,
      '세션 행이 없는 모델 행이 생겼다');
  });

  test('같은 화면의 totals 카드와도 어긋나지 않는다', async () => {
    await seedAll();
    const [rows, t] = await Promise.all([store.usageByModel(), store.totals()]);
    for (const a of AXES) {
      assert.equal(sumAxes(rows)[a], Number(t[a]) || 0, `${a} 가 totals 카드와 다르다`);
    }
  });

  test('행마다 fromSeries + fromSession = 그 행의 값', async () => {
    await seedAll();
    for (const r of await store.usageByModel()) {
      for (const a of AXES) {
        assert.equal(r.fromSeries[a] + r.fromSession[a], r[a],
          `${r.model}.${a} 의 몫 합이 행 값과 다르다`);
        assert.ok(r.fromSeries[a] >= 0 && r.fromSession[a] >= 0, `${r.model}.${a} 에 음수 몫이 나왔다`);
      }
    }
  });
});

describe('② 다중모델 세션이 정확히 쪼개진다', () => {
  beforeEach(async () => { await fresh(); await store.init(); });

  test('세션 전량이 최빈 모델로 가지 않는다 — series 의 모델별 값 그대로다', async () => {
    await ingest(MIXED);
    const rows = await store.usageByModel();

    // 종전이라면 이 한 행이 3,000 을 통째로 가졌다(그리고 나머지 두 모델은 행조차 없었다).
    assert.equal(byModel(rows, 'claude-opus-4-8').output, 1000, '최빈 모델이 남의 토큰을 먹고 있다');
    assert.equal(byModel(rows, 'claude-fable-5').output, 1500);
    assert.equal(byModel(rows, 'claude-haiku-4-5').output, 500);
    assert.equal(byModel(rows, 'claude-haiku-4-5').cacheRead, 5000);

    // 그리고 그 값들은 전부 series 근거다 — 근사가 섞이지 않았다.
    for (const m of ['claude-opus-4-8', 'claude-fable-5', 'claude-haiku-4-5']) {
      assert.equal(byModel(rows, m).fromSession.output, 0, `${m} 이 근사 몫을 갖고 있다`);
    }
    assert.deepEqual(sumAxes(rows), { input: 300, output: 3000, cacheRead: 30000, cacheCreate: 3000 });
  });

  test('series 가 덮지 못한 잔여는 최빈 모델에 남고 근사로 표시된다', async () => {
    await ingest(PARTIAL);
    const r = byModel(await store.usageByModel(), 'claude-opus-5');
    assert.equal(r.output, 1000, '잔여 40% 를 버렸다 — 총합이 줄었다');
    assert.equal(r.fromSeries.output, 600);
    assert.equal(r.fromSession.output, 400, '잔여가 근사 몫으로 밝혀지지 않았다');
  });
});

describe('③ series 없는 세션이 사라지지 않는다', () => {
  beforeEach(async () => { await fresh(); await store.init(); });

  test('구세대 보고만 있는 사람의 모델별이 통째로 남는다', async () => {
    await ingest(OLDGEN);
    const rows = await store.usageByModel();
    assert.equal(byModel(rows, 'claude-sonnet-5').output, 500);
    assert.equal(byModel(rows, 'claude-sonnet-5').fromSession.sessions, 1);
    assert.equal(byModel(rows, 'claude-fable-5').output, 700);
    assert.deepEqual(sumAxes(rows), { input: 35, output: 1300, cacheRead: 3500, cacheCreate: 350 });
  });

  test('신·구 세대가 섞여도 같은 모델 행에서 두 근거가 나란히 더해진다', async () => {
    await seedAll();
    const r = byModel(await store.usageByModel(), 'claude-fable-5');
    assert.equal(r.fromSeries.output, 1500, 'series 몫(mixed 세션)');
    assert.equal(r.fromSession.output, 700, '폴백 몫(userB 구세대 세션)');
    assert.equal(r.output, 2200);
  });

  test('기존 응답 필드는 이름·타입 그대로다(다른 화면이 함께 깨지지 않게)', async () => {
    await seedAll();
    for (const r of await store.usageByModel()) {
      assert.equal(typeof r.model, 'string');
      for (const k of [...AXES, 'sessions']) assert.equal(typeof r[k], 'number', `${k} 타입이 바뀌었다`);
    }
  });
});

describe('④ 근거를 밝힌다 — 사람별 커버리지', () => {
  beforeEach(async () => { await fresh(); await store.init(); });

  test('사용자별 series 커버리지가 응답에 나온다', async () => {
    await seedAll();
    const ax = await store.usageModelAxis();
    const userA = ax.users.find((u) => u.username === 'user-a');
    const userB = ax.users.find((u) => u.username === 'user-b');
    assert.deepEqual({ s: userA.sessions, w: userA.withSeries }, { s: 2, w: 2 });
    assert.deepEqual({ s: userB.sessions, w: userB.withSeries }, { s: 3, w: 1 },
      '커버리지가 낮은 사람이 그대로 드러나야 한다 — 지금은 DB 를 열어야만 보인다');
    assert.equal(ax.sessions, 5);
    assert.equal(ax.withSeries, 3);
    assert.equal(ax.overSessions, 0);
  });

  test('series 합이 세션 행보다 크면 0 에서 끊고 그 건수를 센다(조용히 덮지 않는다)', async () => {
    await ingest({
      machine: 'pc-x', user: 'over',
      sessions: [{
        id: 'sess-over-0006', startedAt: '2026-08-03T09:00:00.000Z', model: 'claude-opus-5',
        input: 10, output: 100, cacheRead: 1000, cacheCreate: 10, turns: 2,
        series: [{ hour: '2026-08-03T09', model: 'claude-opus-5', input: 99, output: 999, cacheRead: 9999, cacheCreate: 99, turns: 2 }],
      }],
    });
    const [rows, ax] = await Promise.all([store.usageByModel(), store.usageModelAxis()]);
    assert.equal(ax.overSessions, 1, '초과분이 조용히 사라졌다');
    const r = byModel(rows, 'claude-opus-5');
    assert.equal(r.fromSession.output, 0, '잔여가 음수로 새어 나왔다');
    for (const a of AXES) assert.ok(r[a] >= 0, `${a} 가 음수다`);
  });
});

describe('⑤ 요약 응답 — 화면이 근거를 말할 수 있는가', () => {
  beforeEach(async () => { await fresh(); await store.init(); });

  async function summary() {
    const u = new URL('http://x/api/usage/summary');
    const out = { status: 0, body: null };
    await usageRoutes({ method: 'GET' }, {}, {
      u,
      p: u.pathname,
      sendJson: (res, status, body) => { out.status = status; out.body = body; },
      requireRole: () => true,
    });
    return out;
  }

  test('byModel 에 몫이, modelAxis 에 커버리지가 실린다', async () => {
    await seedAll();
    const { status, body } = await summary();
    assert.equal(status, 200);
    const r = byModel(body.byModel, 'claude-opus-4-8');
    assert.equal(r.fromSeries.output, 1000);
    assert.ok(body.modelAxis && Array.isArray(body.modelAxis.users), 'modelAxis 가 없다');
    assert.equal(body.modelAxis.withSeries, 3);
    // 화면의 다른 카드와 같은 값이어야 한다(①을 응답 층에서 한 번 더).
    assert.deepEqual(sumAxes(body.byModel), LEGACY_TOTAL);
  });

  test('보고가 하나도 없어도 응답이 성립한다', async () => {
    const { status, body } = await summary();
    assert.equal(status, 200);
    assert.deepEqual(body.byModel, []);
    assert.deepEqual(body.modelAxis.users, []);
    assert.equal(body.modelAxis.sessions, 0);
  });
});

/*
 * ⑥ 화면 문구 — `입력` 이 무엇을 세는지.
 *
 * `input_tokens` 는 **캐시에 걸리지 않은 새 입력만** 센다. 열 이름이 그냥 `입력` 이면 "받은
 * 프롬프트 전체"로 읽히고, 캐싱이 잘 도는 사람이 열심히 안 쓴 것처럼 보인다(사용자가 실제로
 * 그렇게 물었다). 이름에서 조건이 사라지는 회귀는 화면을 열지 않으면 아무도 모른다 — 그래서
 * 문구를 여기서 고정한다(렌더 자체는 위 ⑤ 가 실제로 호출해 확인한다).
 */
describe('⑥ 화면이 입력 축의 뜻을 말한다', () => {
  const SRC = fs.readFileSync(path.join(__dirname, '..', 'public', 'views', 'usagetrack.js'), 'utf8');

  test('열 이름에 조건이 박혀 있고 뜻이 title 에 있다', () => {
    // 라벨 상수를 직접 못박는다 — 본문 어딘가에 '입력(비캐시)' 라는 글자가 있는 것만 보면
    // 열 이름이 `입력` 로 되돌아가도 통과한다(첫 판이 실제로 그렇게 통과했다).
    /*
     * ⚠ 이 단정은 한 번 뒤집혔다(2026-08-05). 첫 판은 "조건 없는 `입력` 헤더 금지" 였다 —
     * 조각(비캐시)을 `입력` 이라 부르지 말라는 뜻이었고 그때는 옳았다. 그런데 사용자가 다시 물었다:
     * "실제값이 진짜 6.9만이야? 입력대비 출력이 엄청난데". 그 표대로면 출력이 입력의 328배인데
     * **물리적으로 불가능하다** — 분모가 입력측의 0.001% 짜리 조각이었기 때문이다.
     *
     * 그래서 라벨을 고치는 대신 **열의 정의를 바꿨다**: 표의 `입력` 은 이제 세 축의 합이다.
     * 그러면 `입력` 이라는 이름이 사실이 되므로 옛 금지는 성립하지 않는다.
     * 대신 **더 강한 것**을 요구한다 — 그 열이 정말 합계인가.
     */
    assert.match(SRC, /const IN_LABEL = '입력\(비캐시\)';/,
      '조각의 이름이 사라졌다 — 총계 타일이 세 축을 분해할 때 쓰는 이름이다');
    assert.match(SRC, /캐시에 걸리지 않은 새 입력 토큰만/, 'title 이 조각의 뜻을 말하지 않는다');
    assert.match(SRC, /캐시읽기 \+ 캐시생성/, '세 입력 축의 관계를 한 줄로 말하지 않는다');
    assert.match(SRC, /\$\{inputNote\(\)\}/, '관계 한 줄을 화면에 그리지 않는다');
    /*
     * ⚠ 이 단정은 두 번 뒤집혔다. 여기가 최종 상태다(2026-08-05, 세 번째 오독 이후).
     *
     * 직전 판은 "합계 함수가 세 축을 다 더하는가" 였다. 합계 열을 주 열로 냈기 때문이다.
     * 그런데 사용자가 그 열을 보고 물었다 — "입력 161억 출력 7683만인데 이게 말이되냐
     * 내가 어떻게 입력을 161억을 해". 합계는 산술적으로 맞지만 `입력` 이라는 이름 아래에서는
     * 거짓말이다: 98% 가 사람이 타이핑한 것이 아니라 매 턴 다시 보낸 맥락(캐시읽기)이다.
     *
     * 그래서 이제 **합계 헬퍼가 없어야** 한다. 세 축을 한 열로 뭉치는 순간 그 열은 이름이
     * 무엇이든 "내가 넣은 입력" 으로 읽힌다.
     * 값 검사는 ⑦ 이 렌더로 한다 — 이름 검사만으로는 공허하다(이 파일이 두 번 겪었다).
     */
    assert.ok(!/const inSum\s*=/.test(SRC),
      '입력 세 축을 하나로 합치는 헬퍼가 되살아났다 — 합계 열은 세 번째 오독의 원인이었다');
    /*
     * ⚠ 여기서 소스 grep 을 멈춘다. 처음엔 `inSum(` 이 함수 블록에 있는지, `inRatio` 라는 글자가
     * 있는지로 검사했는데 **둘 다 공허했다**(변이 실측: 합계를 조각으로 되돌려도, 배수 정의를
     * 지워도 green). 이유는 같다 — 그 이름이 title·호출부에 여전히 남아 문자열 검사를 통과한다.
     * 표시되는 **값**을 봐야 한다. 그건 아래 별도 테스트가 렌더로 확인한다.
     */
  });

  test('근거(fromSession)가 없는 응답에도 열을 요구하지 않는다', () => {
    // 점진적 강화 — 구 서버·구 스텁의 byModel 행에는 fromSeries/fromSession 이 없다.
    assert.match(SRC, /rows\.some\(\(r\) => r && r\.fromSession\)/,
      '새 필드를 조건 없이 읽으면 구 응답에서 화면이 죽는다');
  });
});

/*
 * ── 표시되는 **값**을 읽는다 (소스 grep 으로는 못 잡는다) ───────────────────
 *
 * 위 ⑥ 에서 소스 문자열 검사를 시도했다가 두 번 공허했다(변이 실측 2026-08-05):
 *   · 합계를 조각으로 되돌려도 green — `inSum(` 이 title 문구에 남아 있었다
 *   · (당시 있던 배수 열도 정의를 지워도 green — 이름이 호출부에 남아 있었다. 그 열은 이후 제거)
 * 이름이 어딘가에 남아 있는지 보는 검사는 **값이 바뀌어도 통과한다.** 그래서 실제로 렌더해
 * 화면에 나온 숫자를 읽는다.
 *
 * 이 자리가 지키는 것(2026-08-05, 오독 세 번 끝의 결론): 성질이 다른 입력 세 축이 **각자의
 * 열**로 나와야 한다. 어느 조합이든 한 열로 뭉치면 사람이 그 열을 "내가 넣은 입력" 으로 읽고,
 * 조각만 내면 328배가, 합계를 내면 161억이 나온다. 둘 다 실제로 물어본 숫자다.
 */
describe('⑦ 입력 세 축이 각자의 열로 찍힌다 (렌더 검증)', () => {
  // 실측값을 픽스처로 — 합계 5,176,699,683 · 출력 22,747,067 · 배수 228.
  const ROW = {
    username: 'user-b', input: 69154, cacheCreate: 189192166, cacheRead: 4987438363,
    output: 22747067, turns: 13635, sessions: 847,
  };
  const SUM = ROW.input + ROW.cacheCreate + ROW.cacheRead;

  /* DOM·fetch 스텁은 test/frontend.test.js 와 같은 방식이다(그 파일의 makeEl/installDom).
     여기 필요한 최소만 둔다 — 새 하네스를 발명하지 않는다. */
  function el(tag = 'div') {
    const o = {
      tagName: String(tag).toUpperCase(), style: {}, dataset: {}, children: [],
      innerHTML: '', textContent: '', className: '', value: '',
      classList: { add() {}, remove() {}, toggle() {}, contains: () => false },
      appendChild(c) { o.children.push(c); return c; }, replaceChildren() {}, remove() {},
      setAttribute() {}, getAttribute: () => null, removeAttribute() {},
      addEventListener() {}, removeEventListener() {}, closest: () => null,
      querySelector: () => el(), querySelectorAll: () => [], focus() {}, isConnected: true,
    };
    return o;
  }
  function installDom() {
    globalThis.document = {
      documentElement: el('html'), body: el('body'), activeElement: null,
      getElementById: () => el(), querySelector: () => el(), querySelectorAll: () => [],
      createElement: (t) => el(t), createTextNode: () => el(),
      addEventListener() {}, removeEventListener() {},
    };
    const loc = { href: 'http://localhost/', pathname: '/', search: '', hash: '' };
    globalThis.window = { addEventListener() {}, removeEventListener() {}, location: loc,
      matchMedia: () => ({ matches: false, addEventListener() {} }) };
    globalThis.location = loc;
    const m = new Map();
    globalThis.localStorage = { getItem: (k) => (m.has(k) ? m.get(k) : null),
      setItem: (k, v) => m.set(k, String(v)), removeItem: (k) => m.delete(k) };
    globalThis.requestAnimationFrame = (fn) => setTimeout(fn, 0);
    globalThis.setInterval = () => 0; globalThis.clearInterval = () => {};
  }

  async function render() {
    installDom();
    globalThis.fetch = async (url) => {
      const q = String(url);
      const body = q.startsWith('/api/me')
        ? { user: { username: 'tester', role: 'admin', mustChangePassword: false, mustEnrollMfa: false } }
        : {
          totals: { sessions: ROW.sessions, turns: ROW.turns, input: ROW.input, output: ROW.output,
            cacheRead: ROW.cacheRead, cacheCreate: ROW.cacheCreate },
          byUser: [ROW], byModel: [Object.assign({ model: 'claude-sonnet-5' }, ROW)],
          byDay: [], top: {}, retention: {}, recommendation: null, modelAxis: {},
        };
      return { ok: true, status: 200, headers: { get: () => 'application/json' }, json: async () => body };
    };
    const JS = path.join(__dirname, '..', 'public', 'js');
    const VIEWS = path.join(__dirname, '..', 'public', 'views');
    const core = await import(pathToFileURL(path.join(JS, 'core.js')).href);
    core.setME({ username: 'tester', role: 'admin' });
    const router = await import(pathToFileURL(path.join(JS, 'router.js')).href);
    let seq = 0; for (let i = 0; i < 10000; i += 1) if (!router.isStale(i)) { seq = i; break; }
    const mod = await import(pathToFileURL(path.join(VIEWS, 'usagetrack.js')).href);
    const pane = el();
    await mod.renderUsage(pane, seq);
    return String(pane.innerHTML || '');
  }

  const short = (v) => (v >= 1e8 ? `${(v / 1e8).toFixed(1)}억` : `${(v / 1e4).toFixed(1)}만`);

  // 셀 값만 본다 — title·총계 타일의 같은 숫자에 걸리지 않도록 `>값<` 형태로 못 박는다.
  const cell = (v) => new RegExp(`>\\s*${short(v).replace('.', '\\.')}\\s*<`);

  test('세 축이 각자의 셀로 찍힌다', async () => {
    const html = await render();
    assert.match(html, cell(ROW.input), '입력(비캐시)이 셀로 찍히지 않는다');
    assert.match(html, cell(ROW.cacheRead), '캐시읽기가 셀로 찍히지 않는다');
    assert.match(html, cell(ROW.cacheCreate), '캐시생성이 셀로 찍히지 않는다');
  });

  test('세 축을 합친 열은 없다', async () => {
    assert.ok(!cell(SUM).test(await render()),
      `세 축 합(${short(SUM)})이 셀로 찍힌다 — 합계 열이 돌아왔다.`
      + ' 그 열은 이름이 무엇이든 "내가 넣은 입력"으로 읽힌다(2026-08-05 세 번째 오독)');
  });

  test('열 머리가 세 축의 이름을 각각 말한다', async () => {
    const html = await render();
    for (const th of ['입력(비캐시)', '캐시읽기', '캐시생성']) {
      assert.ok(html.includes(`>${th}<`), `열 머리에 ${th} 가 없다 — 축이 다시 뭉쳐졌다`);
    }
  });

  /*
   * 배수 열은 **뺐다**(2026-08-05, 사용자 판단: "이건 의미 없는거 같아 삭제해도 될거 같아").
   * 그 열의 존재 이유는 328배 오독을 막는 것이었는데, 그 오독은 분모가 조각이었기 때문이다.
   * 축마다 이름이 붙은 지금은 나눌 두 수를 사람이 고르게 되므로 배수는 화면이 정할 일이 아니다.
   * 그래서 단정을 **뒤집는다**: 열이 조용히 되살아나지 않는지 본다.
   */
  test('입력/출력 배수 열은 없다', async () => {
    const html = await render();
    assert.ok(!html.includes('입력/출력'), '배수 열 헤더가 되살아났다');
    assert.ok(!/228\s*배/.test(html), '배수 값이 여전히 셀에 찍힌다');
  });
});

/*
 * ── D3 관측 체인 — 클라이언트가 보낸 값이 화면까지 닿는가 ────────────────────
 *
 * 왜 이 파일에 있나: series 버킷은 `hourOf(timestamp)` 가 성공할 때만 만들어진다. 세션 합계는
 * 시각이 필요 없으므로, 어떤 PC 의 기록에 시각이 없으면 **합계는 맞고 series 만 빈다.**
 * 그 상태가 어디에도 안 남아서 userB 커버리지 2.2%(847세션 중 19)를 PM 이 DB 를 열어야 알았다.
 *
 * 체인이 넷이다: 수집기 → intake 화이트리스트 → 컬럼·upsert → 화면.
 * **어느 한 칸만 끊겨도 값은 조용히 사라진다** — intake 는 모르는 필드를 400 이 아니라 조용히
 * 버리는 규율이라(이 레포 계약) 끊긴 것이 아무 데도 안 남는다. 그래서 네 칸을 다 못박는다.
 */
describe('⑧ D3 — 시각 없는 턴 수가 수집기에서 화면까지 닿는다', () => {
  /*
   * ⚠ 소스 검사에서 이 세션에 네 번 헛디뎠다. 두 함정이 반복됐다:
   *   ① **주석이 통과시킨다** — 주석이 그 단어를 설명하면 실행 코드에서 지워도 매치된다.
   *   ② **부분 문자열이 통과시킨다** — `noTsTurns` 를 `noTsTurnsX` 로 바꿔도 /noTsTurns/ 는 맞는다.
   * 그래서 검사 전에 **주석을 걷어내고**, 식별자는 **경계까지** 본다. 이 두 장치가 없으면
   * 아래 단정들은 전부 "이름이 어딘가에 남아 있는지" 만 보는 공허한 게이트다(변이로 확인했다).
   */
  const strip = (t) => String(t).replace(/\/\*[\s\S]*?\*\//g, ' ').replace(/(^|[^:])\/\/[^\n]*/g, '$1 ');
  const SRC = (rel) => strip(fs.readFileSync(path.join(__dirname, '..', rel), 'utf8'));
  // 식별자 경계 — 뒤에 영숫자·_ 가 붙으면 다른 이름이다(noTsTurnsX 를 통과시키지 않는다).
  const ident = (name) => new RegExp(`\\b${name}(?![A-Za-z0-9_])`);

  /*
   * 수집기(팀원 PC 쪽 클라이언트)가 이 값을 세어 보내는지는 **여기서 검증할 수 없다** —
   * 수집기는 이 레포에 없고 따로 배포된다. 서버가 할 수 있는 것은 "보내오면 잃지 않는다" 까지다.
   * 그 경계를 아래 셋이 지킨다: intake 가 받는가 · 컬럼에 남는가 · 화면이 말하는가.
   */
  test('intake 가 받는다 — 화이트리스트에 없으면 조용히 버려진다', () => {
    const c = SRC('lib/intake.js');
    assert.match(c, ident('noTsTurns'), 'intake 가 이 필드를 모른다 — 조용히 버려진다(400 도 안 난다)');
    // NULL 과 0 을 갈라야 한다. 0 은 "전 턴에 시각이 있었다", NULL 은 "모른다"다.
    assert.match(c, /raw\.noTsTurns == null \? null/,
      '구버전 수집기의 미전송을 0 으로 접는다 — "모른다"가 "문제 없음"이 된다');
  });

  test('컬럼에 저장되고 NULL 이 0 으로 접히지 않는다', () => {
    const c = SRC('lib/store.js');
    assert.match(c, ident('no_ts_turns'), '컬럼이 없다');
    assert.match(c, /s\.noTsTurns == null \? null : int\(s\.noTsTurns\)/,
      'int() 로 감싸 NULL 이 0 이 됐다 — 구버전 PC 가 "시각 문제 없음"으로 단정된다');
    assert.match(c, /ensureColumn\('usage_sessions', 'no_ts_turns'/, 'sqlite 쪽 컬럼 보장이 없다');
  });

  test('pg 마이그레이션이 있고 NOT NULL 을 걸지 않는다', () => {
    const f = 'migrations/pg/0026_usage_no_ts_turns.sql';
    assert.ok(fs.existsSync(path.join(__dirname, '..', f)), `${f} 이 없다 — pg 에서만 조용히 깨진다`);
    const sql = SRC(f);
    assert.match(sql, /ADD COLUMN IF NOT EXISTS no_ts_turns integer/, '컬럼 정의가 다르다');
    /*
     * ⚠ 파일 전체에서 /NOT NULL/ 을 찾으면 **주석에 걸린다** — 이 마이그레이션의 주석이
     * "NOT NULL 을 걸지 않는다" 는 이유를 적고 있기 때문이다(실측: 이 단정이 거짓 red 를 냈다).
     * 이 세션에서 같은 함정을 세 번 밟았다. 그래서 **실행되는 문장만** 본다.
     */
    const stmts = sql.split('\n').filter((l) => !/^\s*--/.test(l)).join('\n');
    assert.ok(!/NOT NULL/i.test(stmts),
      'NOT NULL 을 걸었다 — 구버전 수집기의 미전송이 0 으로 강제되어 "모른다"가 사라진다');
  });

  test('화면이 그 값을 낸다 — 커버리지만 보면 "보고가 안 온다"로 오독된다', () => {
    const v = SRC('public/views/usagetrack.js');
    assert.match(v, ident('noTsTurns'), '화면이 이 값을 읽지 않는다');
    assert.match(v, /시각 없는 턴/, '이유를 사람이 읽을 말로 적지 않는다');
    assert.match(v, ident('noTsUnknown'), '구버전 수집기(모름)를 0 과 갈라 말하지 않는다');
    // 원인을 단정하면 안 된다 — 서버가 아는 것은 "시각이 없었다" 까지다.
    for (const guess of ['수집기 미설치', '토큰이 없', '서버 주소가']) {
      assert.ok(!v.includes(guess), `화면이 원인을 단정했다('${guess}') — 사람이 엉뚱한 데를 판다`);
    }
  });
});
