'use strict';
/*
 * 사용량 → 비용 환산(lib/cost.js).
 *
 * 이 테스트가 지키는 것은 숫자 하나가 아니라 **축의 순위**다.
 *
 * 2026-08-03 실측: 화면이 가장 크게 보여주던 출력 토큰이 비용의 11% 였고, 작은 글씨로 밀려 있던
 * 캐시읽기가 64.5% 였다. 그래서 같은 사용량이 도구마다 176배 차이 나는 숫자로 보였다.
 * 배수(캐시읽기 0.1×·캐시생성 1.25×)를 잘못 건드리면 그 순위가 조용히 뒤집히고, 화면은 다시
 * "제일 큰 축이 제일 안 비싼 축"을 보여주게 된다. 아래 ①이 그 회귀를 잡는다.
 *
 * 검증하는 불변식:
 *   ① 실측 픽스처 — userB 세션 합계로 축별 비중 64.5/24.5/11.0 이 재현된다
 *   ② 단가 미등록 모델을 0 원으로 숨기지 않는다(priced:false 로 올려 보낸다)
 *   ③ 모델 이름 정규화 — 접미사·날짜 스냅샷·벤더 접두사를 떼고 조회한다
 *   ④ config 오버라이드 — 부분 오버라이드 허용, 깨진 항목은 시드 유지
 *   ⑤ 배수 오버라이드 — 1시간 TTL(2×) 로 올릴 수 있다
 */
const { test, describe, before, after } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');

const cost = require('../lib/cost');

/*
 * 앰비언트 config.json 격리 — 라이브 설정에 usage.pricing 이 들어 있으면 시드 기준 산술이
 * 흔들린다. usage 블록이 없는 임시 파일을 물려 시드를 고정한다.
 */
let prevCfg; let tmpDir;
function useConfig(obj) {
  const p = path.join(tmpDir, `cfg-${Math.random().toString(36).slice(2)}.json`);
  fs.writeFileSync(p, JSON.stringify(obj));
  process.env.USAGE_CONFIG = p;
  return p;
}

before(() => {
  prevCfg = process.env.USAGE_CONFIG;
  tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'usage-cost-'));
  useConfig({});
});

after(() => {
  if (prevCfg === undefined) delete process.env.USAGE_CONFIG;
  else process.env.USAGE_CONFIG = prevCfg;
  try { fs.rmSync(tmpDir, { recursive: true, force: true }); } catch { /* 비치명 */ }
});

/* 2026-08-03 라이브에서 뽑은 실제 합계(usage_sessions, username='user-b'). */
const USER_B = Object.freeze({
  model: 'claude-opus-5',
  input: 62082,
  output: 15235576,
  cacheRead: 4468209525,
  cacheCreate: 135610378,
});

describe('① 실측 픽스처 — 축별 비중이 뒤집히지 않는다', () => {
  test('user-b 합계로 캐시읽기 64.5% · 캐시생성 24.5% · 출력 11.0%', () => {
    useConfig({});
    const c = cost.costOf(USER_B);
    assert.equal(c.priced, true);

    const share = (v) => (v / c.usd) * 100;
    // 소수 첫째 자리까지 고정 — 배수나 단가가 바뀌면 여기서 먼저 깨진다.
    assert.equal(share(c.byAxis.cacheRead).toFixed(1), '64.5');
    assert.equal(share(c.byAxis.cacheCreate).toFixed(1), '24.5');
    assert.equal(share(c.byAxis.output).toFixed(1), '11.0');

    // 총액도 자릿수로 묶어 둔다(단가 오타가 10배로 나가는 것을 잡는다).
    assert.ok(c.usd > 3400 && c.usd < 3500, `총액이 예상 범위를 벗어났다: ${c.usd}`);
  });

  test('출력 토큰은 캐시읽기의 1/293 인데 비용은 1/5.9 — 토큰 수와 비용 순위가 다르다', () => {
    useConfig({});
    const c = cost.costOf(USER_B);
    const tokenRatio = USER_B.cacheRead / USER_B.output;      // ≈ 293
    const costRatio = c.byAxis.cacheRead / c.byAxis.output; // ≈ 5.9
    assert.ok(tokenRatio > 250, `토큰 비 ${tokenRatio}`);
    assert.ok(costRatio > 5 && costRatio < 7, `비용 비 ${costRatio}`);
    // 이 둘이 같아지면 배수가 사라진 것이다.
    assert.ok(tokenRatio / costRatio > 40, '토큰 비와 비용 비가 붙었다 — 배수가 적용되지 않았다');
  });
});

describe('② 단가 미등록 모델을 0 원으로 숨기지 않는다', () => {
  test('사내 모델은 priced:false 로 나간다', () => {
    useConfig({});
    const c = cost.costOf({ model: 'nvidia/nemotron-3-ultra-550b-a55b', output: 1e9 });
    assert.equal(c.priced, false);
    assert.equal(c.usd, 0);
    assert.equal(c.model, 'nvidia/nemotron-3-ultra-550b-a55b');
  });

  test('summarize 가 미등록 모델을 목록으로 올려 보낸다', () => {
    useConfig({});
    const s = cost.summarize([
      USER_B,
      { model: 'nemotron', output: 5e8 },
      { model: 'nemotron', output: 1e8 },   // 중복은 한 번만
      { model: '', output: 1e8 },           // 모델 미상은 목록에 넣지 않는다(이름이 없다)
    ]);
    assert.deepEqual(s.unpriced, ['nemotron']);
    assert.ok(s.usd > 3400 && s.usd < 3500, '미등록 모델이 합계를 오염시켰다');
    assert.equal(typeof s.pricedAt, 'string');
  });
});

describe('③ 모델 이름 정규화', () => {
  test('접미사·날짜 스냅샷·벤더 접두사를 떼고 같은 단가로 조회한다', () => {
    useConfig({});
    const base = cost.costOf({ model: 'claude-opus-5', output: 1e6 }).usd;
    for (const m of ['claude-opus-5[1m]', 'anthropic.claude-opus-5', 'CLAUDE-OPUS-5', '  claude-opus-5  ']) {
      assert.equal(cost.costOf({ model: m, output: 1e6 }).usd, base, `정규화 실패: ${m}`);
    }
    assert.equal(cost.normalizeModel('claude-opus-4-5-20251101'), 'claude-opus-4-5');
  });
});

describe('④ config 오버라이드', () => {
  test('부분 오버라이드 — 지정한 모델만 바뀌고 나머지는 시드를 유지한다', () => {
    useConfig({ usage: { pricing: { 'claude-opus-5': { input: 10, output: 50 } } } });
    const t = cost.pricing();
    assert.deepEqual(t['claude-opus-5'], { input: 10, output: 50 });
    assert.deepEqual(t['claude-haiku-4-5'], cost.SEED['claude-haiku-4-5']);
    // 단가를 2배로 올렸으니 총액도 2배가 되어야 한다.
    assert.ok(Math.abs(cost.costOf(USER_B).usd / 3462.87 - 2) < 0.01);
  });

  test('한쪽만 온 항목은 통째로 무시하고 시드를 유지한다', () => {
    useConfig({ usage: { pricing: { 'claude-opus-5': { input: 99 } } } }); // output 없음
    assert.deepEqual(cost.pricing()['claude-opus-5'], cost.SEED['claude-opus-5']);
  });

  test('pricedAt 은 config 가 이긴다 — 낡은 단가가 화면에 보이게', () => {
    useConfig({ usage: { pricedAt: '2026-09-01' } });
    assert.equal(cost.pricedAt(), '2026-09-01');
    useConfig({});
    assert.equal(cost.pricedAt(), cost.SEED_PRICED_AT);
  });
});

describe('⑤ 배수 오버라이드 — 1시간 TTL', () => {
  test('캐시생성 배수를 2 로 올리면 그 축만 비례해 오른다', () => {
    useConfig({});
    const base = cost.costOf(USER_B).byAxis;
    useConfig({ usage: { cacheCreateMult: 2 } });
    const raised = cost.costOf(USER_B).byAxis;

    assert.ok(Math.abs(raised.cacheCreate / base.cacheCreate - (2 / 1.25)) < 0.001);
    // 나머지 세 축은 손대지 않아야 한다.
    assert.equal(raised.output, base.output);
    assert.equal(raised.input, base.input);
    assert.equal(raised.cacheRead, base.cacheRead);
  });

  /*
   * 2단 방어(cost.js clampMult 참조). 음수를 0 으로 클램프하면 캐시읽기가 공짜가 되어 비용이
   * 조용히 사라진다 — 그래서 무효값은 클램프가 아니라 기본값으로 수렴한다.
   */
  test('무효 배수는 기본값으로, 범위 초과만 클램프된다', () => {
    useConfig({ usage: { cacheReadMult: -5, cacheCreateMult: 9999 } });
    const m = cost.multipliers();
    assert.equal(m.cacheRead, cost.CACHE_READ_MULT, '음수를 0 으로 클램프해 캐시읽기를 공짜로 만들었다');
    assert.equal(m.cacheCreate, 10, '범위 초과가 상한으로 잘리지 않았다');

    useConfig({ usage: { cacheReadMult: 'abc' } });
    assert.equal(cost.multipliers().cacheRead, cost.CACHE_READ_MULT);
  });
});

/*
 * ⑥ 공식 가격표 대조 — 2026-08-04, platform.claude.com/docs/ko/about-claude/pricing
 *
 * 이 블록의 존재 이유: 단가가 틀려도 화면은 멀쩡한 숫자를 보여준다. 틀린 것을 알아채는 유일한
 * 방법이 공식 표와의 대조인데, 그 대조를 사람의 기억에 맡기면 다음 모델 추가 때 조용히 어긋난다.
 *
 * 특히 **기간 한정 도입가**가 위험하다. Sonnet 5 는 2026-08-31 까지 $2/$10, 그 뒤 $3/$15 다.
 * 어느 쪽을 상수로 박아도 한쪽 기간에는 틀린다 — 정가를 박으면 도입 기간 중 1.5배 과대,
 * 도입가를 박으면 9/1 부터 33% 과소다. 그래서 날짜로 갈리고, 그 경계를 여기서 못 박는다.
 */
describe('⑥ 공식 가격표 대조', () => {
  // 공식 표 그대로. 이 표를 고칠 때는 반드시 위 URL 을 다시 열어 확인할 것.
  const OFFICIAL = {
    'claude-fable-5': { input: 10, output: 50 },
    'claude-mythos-5': { input: 10, output: 50 },
    'claude-opus-5': { input: 5, output: 25 },
    'claude-opus-4-8': { input: 5, output: 25 },
    'claude-opus-4-7': { input: 5, output: 25 },
    'claude-opus-4-6': { input: 5, output: 25 },
    'claude-opus-4-5': { input: 5, output: 25 },
    'claude-sonnet-4-6': { input: 3, output: 15 },
    'claude-sonnet-4-5': { input: 3, output: 15 },
    'claude-haiku-4-5': { input: 1, output: 5 },
  };

  test('기간과 무관한 모델은 공식 단가와 정확히 일치한다', () => {
    useConfig({});
    const t = cost.pricing('2026-08-04');
    for (const [model, p] of Object.entries(OFFICIAL)) {
      assert.ok(t[model], `${model} 이 단가표에 없다 — 그 모델 사용량이 비용에서 빠진다`);
      assert.equal(t[model].input, p.input, `${model} 입력가가 공식과 다르다`);
      assert.equal(t[model].output, p.output, `${model} 출력가가 공식과 다르다`);
    }
  });

  test('Sonnet 5 도입가는 날짜로 갈린다 — 경계 포함/제외까지', () => {
    useConfig({});
    const s5 = (day) => cost.pricing(day)['claude-sonnet-5'];
    // "2026년 8월 31일까지" — 만료일 당일은 아직 도입가다.
    assert.deepEqual(s5('2026-08-04'), { input: 2, output: 10 }, '도입 기간인데 정가를 쓴다(1.5배 과대)');
    assert.deepEqual(s5('2026-08-31'), { input: 2, output: 10 }, '만료일 당일이 이미 정가로 넘어갔다');
    assert.deepEqual(s5('2026-09-01'), { input: 3, output: 15 }, '도입가가 만료 후에도 남아 있다(33% 과소)');
  });

  test('캐시 배수는 공식 배수와 일치한다 — 여기가 비용의 79%다', () => {
    useConfig({});
    // 공식: 5분 쓰기 1.25배 · 1시간 쓰기 2배 · 캐시 히트 0.1배 (기본 입력가 대비)
    assert.equal(cost.CACHE_READ_MULT, 0.1);
    assert.equal(cost.CACHE_CREATE_MULT, 1.25);
    assert.equal(cost.CACHE_CREATE_1H_MULT, 2);
  });

  test('공식 표의 캐시 단가와 우리 계산이 같은 값을 낸다', () => {
    useConfig({});
    const t = cost.pricing('2026-08-04');
    const m = cost.multipliers();
    // 공식 표는 캐시 단가를 절대값으로도 적어 둔다 — 배수 계산이 그 값과 맞는지 교차 검증한다.
    // Opus 5: 5m $6.25 · 1h $10 · hit $0.50
    const opus = t['claude-opus-5'].input;
    assert.equal(opus * m.cacheCreate, 6.25, '5분 캐시 쓰기 단가가 공식($6.25)과 다르다');
    assert.equal(opus * m.cacheCreate1h, 10, '1시간 캐시 쓰기 단가가 공식($10)과 다르다');
    assert.equal(opus * m.cacheRead, 0.5, '캐시 히트 단가가 공식($0.50)과 다르다');
    // Haiku 4.5: 5m $1.25 · 1h $2 · hit $0.10
    const haiku = t['claude-haiku-4-5'].input;
    assert.equal(haiku * m.cacheCreate, 1.25);
    assert.equal(haiku * m.cacheCreate1h, 2);
    assert.ok(Math.abs(haiku * m.cacheRead - 0.1) < 1e-12);
  });

  test('실제 사용량 한 건이 손으로 계산한 값과 일치한다', () => {
    useConfig({});
    // Opus 5 기준: 입력 1M·출력 1M·캐시읽기 10M·캐시생성 1M(1시간)
    const c = cost.costOf({
      model: 'claude-opus-5',
      input: 1e6, output: 1e6, cacheRead: 1e7,
      cacheCreate: 1e6, cacheCreate5m: 0, cacheCreate1h: 1e6,
    }, cost.pricing('2026-08-04'));
    assert.equal(c.byAxis.input, 5, '1M 입력 = $5');
    assert.equal(c.byAxis.output, 25, '1M 출력 = $25');
    assert.equal(c.byAxis.cacheRead, 5, '10M 캐시읽기 = 10 × $0.50 = $5');
    assert.equal(c.byAxis.cacheCreate, 10, '1M 1시간 캐시생성 = $10');
    assert.equal(c.usd, 45);
    assert.equal(c.ttlKnown, true);
  });
});
