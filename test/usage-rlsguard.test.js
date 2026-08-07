'use strict';
/*
 * RLS 성립 전제 가드 — 판정(lib/db/rlsguard.js)과 **배선**(server.js 의 assertRlsSafe).
 *
 * 왜 이 파일이 필요한가: rlsguard.js 는 스스로 "양성·음성 양쪽을 단위 테스트로 못박는다"고
 * 적어 두었지만 그 테스트가 없었고, 더 나쁘게는 **그 모듈을 부르는 코드가 아무 데도 없었다.**
 * 판정 로직이 맞는지와 그것이 실제로 부팅 경로에 걸려 있는지는 다른 사실이고, 둘 다 깨져도
 * 화면은 멀쩡하다 — 요청은 200 이고 데이터도 잘 보인다(남의 테넌트 것까지).
 *
 * 그래서 여기서 검증하는 것은 셋이다:
 *   ① 판정   슈퍼·BYPASSRLS 롤을 위반으로 부르고, 평범한 앱 롤은 통과시킨다.
 *   ② 배선   server.js 가 그 판정을 실제로 쓰고, 위반이면 부팅을 거부한다.
 *   ③ 불가   DB 에 닿지 못하는 경우를 위반과 **갈라서** 다룬다(거부하지 않되 조용하지도 않다).
 */
const { test, describe } = require('node:test');
const assert = require('node:assert/strict');

const rlsguard = require('../lib/db/rlsguard');
const { assertRlsSafe, RLS_PROBE_SQL } = require('../server');

describe('① rlsguard.check — 판정', () => {
  test('평범한 앱 롤은 통과한다', () => {
    const r = rlsguard.check({ role: 'usage_app', rolsuper: false, rolbypassrls: false });
    assert.equal(r.ok, true);
  });

  test('SUPERUSER 는 위반이다 — FORCE RLS 조차 무시된다', () => {
    const r = rlsguard.check({ role: 'postgres', rolsuper: true, rolbypassrls: false });
    assert.equal(r.ok, false);
    assert.match(r.message, /SUPERUSER/);
    assert.match(r.message, /postgres/, '어느 롤이 문제인지 말하지 않으면 고칠 수가 없다');
  });

  test('BYPASSRLS 단독도 위반이다 — 슈퍼가 아니어도 격리가 깨진다', () => {
    const r = rlsguard.check({ role: 'admin_role', rolsuper: false, rolbypassrls: true });
    assert.equal(r.ok, false);
    assert.match(r.message, /BYPASSRLS/);
  });

  test('둘 다면 둘 다 말한다', () => {
    const r = rlsguard.check({ role: 'root', rolsuper: true, rolbypassrls: true });
    assert.equal(r.ok, false);
    assert.match(r.message, /SUPERUSER\+BYPASSRLS/);
  });

  /*
   * 속성이 문자열 'f'/'t' 나 undefined 로 와도 참으로 접히면 안 된다 — 드라이버 타입 파서가
   * 바뀌면 조용히 전수 위반이 되어 아무도 못 뜨거나, 반대로 전수 통과가 된다.
   */
  test('boolean true 가 아닌 값은 위반으로 세지 않는다(=== 비교)', () => {
    assert.equal(rlsguard.check({ role: 'x', rolsuper: 'f', rolbypassrls: 'f' }).ok, true);
    assert.equal(rlsguard.check({ role: 'x' }).ok, true);
  });

  test('행이 없으면 판정하지 않는다 — 호출부가 모드별로 정한다', () => {
    assert.equal(rlsguard.check(null).ok, true);
  });

  test('remedy 는 고치는 방법을 문장에 담는다', () => {
    const msg = rlsguard.remedy('앱 DB 롤이 SUPERUSER 입니다');
    assert.match(msg, /NOSUPERUSER/);
    assert.match(msg, /NOBYPASSRLS/);
    assert.match(msg, /USAGE_PG_URL/);
  });
});

describe('② assertRlsSafe — 부팅 경로 배선', () => {
  test('probe 로 얻은 정상 롤은 ok + 롤 이름을 돌려준다', async () => {
    const v = await assertRlsSafe(async () => ({ role: 'usage_app', rolsuper: false, rolbypassrls: false }));
    assert.equal(v.ok, true);
    assert.equal(v.role, 'usage_app');
  });

  test('위반 롤은 ok:false 이고 메시지에 해결 문장이 붙는다', async () => {
    const v = await assertRlsSafe(async () => ({ role: 'postgres', rolsuper: true, rolbypassrls: false }));
    assert.equal(v.ok, false);
    assert.match(v.message, /SUPERUSER/);
    assert.match(v.message, /NOSUPERUSER/, 'remedy 를 거치지 않았다 — 사람이 고칠 방법을 못 받는다');
  });

  /*
   * 프로브 SQL 이 실제로 필요한 세 컬럼을 묻는가. 이 문장이 조용히 바뀌면 rlsguard 가 늘
   * undefined 를 받아 **전부 통과**시킨다 — 가드가 있는데 아무것도 안 잡는 상태가 된다.
   */
  test('프로브 SQL 이 판정에 필요한 세 값을 묻는다', () => {
    assert.match(RLS_PROBE_SQL, /current_user/);
    assert.match(RLS_PROBE_SQL, /rolsuper/);
    assert.match(RLS_PROBE_SQL, /rolbypassrls/);
    assert.match(RLS_PROBE_SQL, /pg_roles/);
  });
});

describe('③ 판정 불가 — 위반과 갈라서 다룬다', () => {
  test('접속 실패는 inconclusive 다(부팅을 거부하지 않는다)', async () => {
    const v = await assertRlsSafe(async () => { throw new Error('ECONNREFUSED 127.0.0.1:15432'); });
    assert.equal(v.inconclusive, true);
    assert.notEqual(v.ok, false, '접속 실패를 위반으로 취급하면 터널을 뚫기 전 기동이 통째로 막힌다');
    assert.match(v.message, /ECONNREFUSED/, '왜 확인 못 했는지가 남아야 한다');
  });

  test('행이 비면 inconclusive 다 — 통과로 접지 않는다', async () => {
    const v = await assertRlsSafe(async () => null);
    assert.equal(v.inconclusive, true);
    assert.notEqual(v.ok, true, '확인 못 한 것을 "안전하다"로 기록하면 가드가 거짓말을 한다');
  });
});
