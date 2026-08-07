'use strict';
/*
 * 사용량 텔레메트리 — 인테이크 정규화·멱등성·집계·공백 탐지.
 *
 * 이 기능이 지켜야 하는 불변식은 셋이고, 셋 다 깨져도 화면은 멀쩡해 보인다:
 *   ① **원문이 저장되지 않는다.** 트랜스크립트에는 대화가 통째로 있다. 축소해서 보낸다는
 *      약속이 코드 어딘가에서 풀리면 그날부터 서버 DB 에 팀원 프롬프트가 쌓인다.
 *   ② **멱등하다.** 훅은 실패하면 다음 세션에 다시 보낸다(재전송이 정상 동작이다).
 *      누적(+=)으로 구현되면 같은 세션이 두 번 세어져 사용량이 조용히 부풀려진다.
 *   ③ **모르는 축을 받지 않는다.** 클라이언트가 보고하는 값이라 오타 하나가 집계 축을 늘린다.
 */
const { test, describe, beforeEach } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');

// 어댑터/모듈 로드 전에 데이터 디렉터리를 임시로 돌린다(learning-scope-stamp 와 같은 격리 규율).
process.env.USAGE_DATA_DIR = fs.mkdtempSync(path.join(os.tmpdir(), 'usage-store-'));
const adapter = require('../lib/db/adapter');
const { pgActive } = require('./helper');
const store = require('../lib/store');
const intake = require('../lib/intake');

const SID = 'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee';

async function fresh() {
  if (pgActive()) {
    const { q } = require('../lib/db');
    await q('DELETE FROM usage_counters').run();
    await q('DELETE FROM usage_sessions').run();
    await q('DELETE FROM usage_recommendations').run();
    /*
     * machine_identity 도 함께 비운다 — 이게 빠져 있어서 pg 모드에서만 ⑥ 이 깨졌다.
     *
     * sqlite 모드의 fresh() 는 데이터 디렉터리를 통째로 갈아타므로 이 표도 사라진다. pg 모드는
     * DELETE 로 흉내내는데 이 표가 목록에서 빠져 있었다 → ⑥ 의 앞 테스트가 걸어 둔 pc-1 매핑이
     * 남아, 뒤 테스트의 unmapped() 가 빈 목록을 받았다. 같은 파일이 sqlite 로는 통과하므로
     * 전체 스위트가 green 인 채로 이 결함이 숨어 있었다(실측 2026-08-04).
     */
    await q('DELETE FROM machine_identity').run();
    return;
  }
  adapter.close();
  process.env.USAGE_DATA_DIR = fs.mkdtempSync(path.join(os.tmpdir(), 'usage-store-'));
}

function payload(over = {}) {
  return {
    machine: 'pc-1', user: 'user-a',
    sessions: [Object.assign({
      id: SID, startedAt: '2026-08-03T09:00:00.000Z', model: 'claude-opus-5',
      input: 10, output: 2000, cacheRead: 90000, cacheCreate: 500, turns: 30,
      counters: { bash: { git: 12 }, tool: { Bash: 40 } },
    }, over)],
  };
}

async function ingest(p) {
  for (const { session, rows } of intake.normPayload(p)) {
    await store.sessionUpsert(session);
    await store.countersUpsert({
      sessionId: session.sessionId, username: session.username,
      machine: session.machine, startedAt: session.startedAt, rows,
    });
  }
}

describe('① 인테이크 정규화 — 순수', () => {
  test('모르는 축은 버린다(오타가 집계 축을 늘리지 못하게)', () => {
    const [r] = intake.normPayload(payload({ counters: { bogus: { x: 9 }, bash: { git: 1 } } }));
    assert.ok(r.rows.every((x) => x.kind !== 'bogus'));
    assert.equal(r.rows.filter((x) => x.kind === 'bash').length, 1);
  });

  test('bash 키는 경로를 벗겨 실행파일만 남긴다', () => {
    const [r] = intake.normPayload(payload({ counters: { bash: { '/usr/bin/git': 3 } } }));
    assert.deepEqual(r.rows.find((x) => x.kind === 'bash'), { kind: 'bash', key: 'git', count: 3 });
  });

  test('세션 id 형식이 아니면 통째로 버린다', () => {
    assert.equal(intake.normPayload(payload({ id: '../../etc/passwd' })).length, 0);
    assert.equal(intake.normPayload(payload({ id: 'x' })).length, 0);
  });

  test('0 이하·비수치 카운트는 버린다', () => {
    const [r] = intake.normPayload(payload({ counters: { bash: { git: 0, npm: -3, jest: 'x', node: 2 } } }));
    assert.deepEqual(r.rows.filter((x) => x.kind === 'bash').map((x) => x.key), ['node']);
  });

  test('음수 사용량은 0 으로 접는다', () => {
    const [r] = intake.normPayload(payload({ output: -5, input: 'abc' }));
    assert.equal(r.session.output, 0);
    assert.equal(r.session.input, 0);
  });

  test('축마다 상위 N개만 남긴다(키워드 롱테일이 테이블을 채우지 못하게)', () => {
    const many = {};
    for (let i = 0; i < 300; i++) many[`k${i}`] = 300 - i;
    const [r] = intake.normPayload(payload({ counters: { keyword: many } }));
    const kw = r.rows.filter((x) => x.kind === 'keyword');
    assert.equal(kw.length, intake.PER_KIND_MAX);
    assert.equal(kw[0].key, 'k0', '빈도 높은 것부터 남아야 한다');
  });

  test('배열 형태 counters 도 받는다(팩 버전 드리프트 흡수)', () => {
    const [r] = intake.normPayload(payload({ counters: [{ kind: 'bash', key: 'git', count: 4 }] }));
    assert.deepEqual(r.rows, [{ kind: 'bash', key: 'git', count: 4 }]);
  });

  test('페이로드가 깨져도 throw 하지 않고 빈 배열을 준다', () => {
    assert.deepEqual(intake.normPayload(null), []);
    assert.deepEqual(intake.normPayload({ sessions: 'nope' }), []);
  });
});

describe('② 멱등성 — 재전송이 값을 부풀리지 않는다', () => {
  beforeEach(async () => { await fresh(); await store.init(); });

  test('같은 세션을 세 번 보내도 사용량과 카운터가 그대로다', async () => {
    await ingest(payload());
    await ingest(payload());
    await ingest(payload());
    const t = await store.totals();
    assert.equal(t.sessions, 1);
    assert.equal(t.output, 2000, '재전송이 누적되면 안 된다');
    assert.deepEqual((await store.topKeys('bash'))[0], { key: 'git', count: 12, sessions: 1, users: 1 });
  });

  test('세션이 이어져 값이 커지면 최신값으로 갱신된다(덮어쓰기)', async () => {
    await ingest(payload());
    await ingest(payload({ output: 5000, counters: { bash: { git: 20 } } }));
    const t = await store.totals();
    assert.equal(t.output, 5000);
    assert.equal((await store.topKeys('bash'))[0].count, 20);
  });

  test('서버측 계측은 누적이다(호출마다 +1)', async () => {
    await store.counterBump({ kind: 'mcp', key: 'search_kb' });
    await store.counterBump({ kind: 'mcp', key: 'search_kb' });
    await store.counterBump({ kind: 'mcp', key: 'get_kb' });
    const rows = await store.topKeys('mcp');
    assert.equal(rows.find((r) => r.key === 'search_kb').count, 2);
    assert.equal(rows.find((r) => r.key === 'get_kb').count, 1);
  });

  test('모르는 축은 counterBump 도 거부한다', async () => {
    assert.equal(await store.counterBump({ kind: 'bogus', key: 'x' }), false);
  });
});

describe('③ 집계', () => {
  beforeEach(async () => { await fresh(); await store.init(); });

  test('사람·모델·일별로 나뉘고 캐시읽기가 입력과 분리된다', async () => {
    await ingest(payload());
    await ingest({
      machine: 'pc-2', user: 'kim',
      sessions: [{ id: 'ffffffff-1111-2222-3333-444444444444', startedAt: '2026-08-02T09:00:00.000Z',
        model: 'claude-sonnet-5', input: 5, output: 100, cacheRead: 7, cacheCreate: 1, turns: 3, counters: {} }],
    });
    const byUser = await store.usageByUser();
    assert.equal(byUser.length, 2);
    const userA = byUser.find((r) => r.username === 'user-a');
    assert.equal(userA.cacheRead, 90000);
    assert.equal(userA.input, 10, '캐시읽기가 입력에 합산되면 비용이 왜곡된다');
    assert.equal((await store.usageByModel()).length, 2);
    assert.deepEqual((await store.usageByDay()).map((r) => r.day), ['2026-08-03', '2026-08-02']);
  });
});

describe('④ 카탈로그 공백 탐지', () => {
  beforeEach(async () => { await fresh(); await store.init(); });

  test('매칭이 약한 목표의 반복 토큰만 공백으로 올라온다', async () => {
    await store.recommendationAdd({ goalTokens: ['쿠버네티스', '비용'], score: 0, source: 'mcp' });
    await store.recommendationAdd({ goalTokens: ['쿠버네티스', '모니터링'], score: 0, source: 'mcp' });
    await store.recommendationAdd({ goalTokens: ['일회성단어'], score: 0, source: 'web' });
    const gaps = await store.recommendationGaps({});
    assert.deepEqual(gaps.map((g) => g.token), ['쿠버네티스']);
    assert.equal(gaps[0].count, 2);
  });

  test('매칭이 잘 된 목표는 공백에 들어가지 않는다', async () => {
    await store.recommendationAdd({ goalTokens: ['보안', '리뷰'], agent: 'security-reviewer', score: 9, source: 'mcp' });
    await store.recommendationAdd({ goalTokens: ['보안', '점검'], agent: 'security-reviewer', score: 9, source: 'mcp' });
    assert.deepEqual(await store.recommendationGaps({}), []);
  });

  test('조사를 뗀 어휘로 묶인다 — 클라이언트 키워드 축과 같은 규칙', async () => {
    // '스코프로'/'스코프를' 이 따로 세어지면 상위권이 흩어져 공백이 드러나지 않는다.
    await store.recommendationAdd({ goalTokens: ['스코프로'], score: 0, source: 'mcp' });
    await store.recommendationAdd({ goalTokens: ['스코프를'], score: 0, source: 'mcp' });
    assert.deepEqual(await store.recommendationGaps({}), [{ token: '스코프', count: 2 }]);
  });

  test('요약은 점수 0 만 실패로 센다', async () => {
    await store.recommendationAdd({ goalTokens: ['a'], score: 0 });
    await store.recommendationAdd({ goalTokens: ['b'], score: 3 });
    assert.deepEqual(await store.recommendationSummary(), { total: 2, miss: 1 });
  });
});

describe('⑤ 키워드 보존 — 무한히 자라지 않는다', () => {
  beforeEach(async () => { await fresh(); await store.init(); });

  // 특정 날짜의 키워드 행을 심는다(day 는 startedAt 에서 유도된다).
  async function seed(day, key, sid) {
    await store.countersUpsert({
      sessionId: sid, username: 'user-a', machine: 'pc-1',
      startedAt: `${day}T00:00:00.000Z`, rows: [{ kind: 'keyword', key, count: 3 }],
    });
  }

  test('기한 지난 키워드만 지우고 최근 것은 남긴다', async () => {
    const now = new Date('2026-08-03T00:00:00.000Z');
    await seed('2026-01-01', '오래된말', 's-old');        // 214일 전
    await seed('2026-07-20', '최근말', 's-new');          // 14일 전
    const r = await store.pruneKeywords({ days: 90, now });
    assert.equal(r.days, 90);
    assert.equal(r.cutoff, '2026-05-05');
    assert.equal(r.removed, 1);
    assert.deepEqual((await store.topKeys('keyword')).map((x) => x.key), ['최근말']);
  });

  test('다른 축은 기한이 지나도 지우지 않는다', async () => {
    const now = new Date('2026-08-03T00:00:00.000Z');
    await store.countersUpsert({
      sessionId: 's-old2', username: 'user-a', machine: 'pc-1', startedAt: '2026-01-01T00:00:00.000Z',
      rows: [{ kind: 'bash', key: 'git', count: 5 }, { kind: 'keyword', key: '옛말', count: 5 }],
    });
    await store.pruneKeywords({ days: 90, now });
    assert.equal((await store.topKeys('bash')).length, 1, 'bash 축은 보존 대상이 아니다');
    assert.equal((await store.topKeys('keyword')).length, 0);
  });

  test('기본 보존은 90일이고 하한·상한으로 클램프된다', () => {
    assert.equal(store.KEYWORD_RETENTION_DEFAULT, 90);
    assert.equal(store.retentionDays(undefined), 90);
    assert.equal(store.retentionDays('이상한값'), 90);
    assert.equal(store.retentionDays(1), store.KEYWORD_RETENTION_MIN, '너무 짧으면 축이 무의미해진다');
    assert.equal(store.retentionDays(99999), store.KEYWORD_RETENTION_MAX);
  });

  test('나이를 알 수 없는 행(day NULL)도 지운다 — 개인 발화를 영구 보관하지 않는다', async () => {
    const { q } = require('../lib/db');
    await q("INSERT INTO usage_counters(session_id,kind,key,count,day) VALUES('s-null','keyword','미상말',2,NULL)").run();
    assert.equal((await store.topKeys('keyword')).length, 1);
    await store.pruneKeywords({ days: 90, now: new Date('2026-08-03T00:00:00.000Z') });
    assert.equal((await store.topKeys('keyword')).length, 0);
  });

  test('정리기 설정 — off 면 아예 돌지 않는다', async () => {
    const retention = require('../lib/retention');
    const prev = process.env.USAGE_KEYWORD_RETENTION_DAYS;
    try {
      process.env.USAGE_KEYWORD_RETENTION_DAYS = 'off';
      assert.equal(retention.configuredDays(), null);
      assert.equal(retention.start(), null, 'off 면 타이머를 걸지 않는다');
      assert.equal(await retention.runOnce(), null);
      process.env.USAGE_KEYWORD_RETENTION_DAYS = '30';
      assert.equal(retention.configuredDays(), 30);
      delete process.env.USAGE_KEYWORD_RETENTION_DAYS;
      assert.equal(retention.configuredDays(), 90);
    } finally {
      if (prev === undefined) delete process.env.USAGE_KEYWORD_RETENTION_DAYS;
      else process.env.USAGE_KEYWORD_RETENTION_DAYS = prev;
      retention.stop();
    }
  });
});

/*
 * ⑥ 서버 권위 귀속 — 클라이언트가 무엇을 보내든 매핑이 이긴다.
 *
 * 이 계층이 있는 이유: 팀원 PC 는 자기 신원을 스스로 보고하고 그 기본값은 OS 계정명이다.
 * 어긋난 것을 팩 재배포 + 그 PC 재설치로 고치는 방식은 반복 비용이 크고, 실제로 같은 누락을
 * 세 번 반복했다(부트스트랩·관제보고·사용량 수집기). 서버가 권위를 가지면 관리자가 화면에서
 * 한 줄 고치는 것으로 끝나고, 클라이언트 경로가 몇 개든 인테이크 한 곳에서 수렴한다.
 */
describe('⑥ 머신 → 계정 매핑', () => {
  const identity = require('../lib/identity');
  beforeEach(async () => { await fresh(); await store.init(); await identity.init(); });

  test('매핑이 없으면 클라이언트가 보낸 이름을 그대로 쓴다', async () => {
    assert.equal(await identity.resolve('PC-A'), null);
  });

  test('매핑을 걸면 과거 데이터가 함께 옮겨진다(소급)', async () => {
    await ingest(payload({ counters: { bash: { git: 3 } } }));
    assert.equal((await store.usageByUser())[0].username, 'user-a');

    const r = await identity.set({ machine: 'pc-1', username: 'user-b', actor: 'admin' });
    assert.equal(r.moved.sessions, 1);
    assert.ok(r.moved.counters >= 1);
    assert.equal((await store.usageByUser())[0].username, 'user-b', '과거 행이 안 옮겨지면 한 사람이 두 줄로 보인다');
  });

  test('매핑 후 같은 세션이 옛 이름으로 재보고돼도 되돌아가지 않는다', async () => {
    // 이게 이 계층의 핵심이다 — 서버 UPSERT 는 재보고 때 username 을 덮어쓰므로,
    // DB 를 손으로 고쳐 두는 방식은 다음 보고 한 번에 무너진다(2026-08-03 실측).
    await ingest(payload());
    await identity.set({ machine: 'pc-1', username: 'user-b', actor: 'admin' });

    // 인테이크가 하는 일을 그대로 재현: 매핑을 먼저 적용하고 저장한다.
    for (const { session, rows } of intake.normPayload(payload({ output: 9999 }))) {
      const mapped = await identity.resolve(session.machine);
      if (mapped) session.username = mapped;
      await store.sessionUpsert(session);
      await store.countersUpsert({
        sessionId: session.sessionId, username: session.username,
        machine: session.machine, startedAt: session.startedAt, rows,
      });
    }
    const rows = await store.usageByUser();
    assert.equal(rows.length, 1);
    assert.equal(rows[0].username, 'user-b', '재보고가 매핑을 무너뜨리면 안 된다');
    assert.equal(rows[0].output, 9999, '값 자체는 최신으로 갱신돼야 한다');
  });

  test('미매핑 머신 목록이 고칠 대상을 알려준다', async () => {
    await ingest(payload());
    const before = await identity.unmapped();
    assert.deepEqual(before.map((x) => x.machine), ['pc-1']);
    await identity.set({ machine: 'pc-1', username: 'user-b', actor: 'admin' });
    assert.deepEqual(await identity.unmapped(), [], '매핑된 머신은 목록에서 빠져야 한다');
  });

  test('해제하면 이후 보고는 클라이언트 값을 쓰되 과거는 그대로 둔다', async () => {
    await ingest(payload());
    await identity.set({ machine: 'pc-1', username: 'user-b', actor: 'admin' });
    assert.equal(await identity.remove('pc-1'), true);
    assert.equal(await identity.resolve('pc-1'), null);
    assert.equal((await store.usageByUser())[0].username, 'user-b', '과거는 되돌리지 않는다(되돌릴 원본이 없다)');
  });

  test('빈 값은 거부한다 — 실수로 귀속을 지우지 못하게', async () => {
    await assert.rejects(() => identity.set({ machine: '', username: 'user-b' }));
    await assert.rejects(() => identity.set({ machine: 'pc-1', username: '  ' }));
  });
});
