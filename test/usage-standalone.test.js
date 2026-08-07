'use strict';
/*
 * 서버 진입점(server.js) — 게이트·모드·정적 서빙.
 *
 * 이 파일이 지키는 것 셋. 셋 다 **깨져도 화면은 멀쩡해 보인다**:
 *
 *   ① **인증.** 사람별 사용량·비용이 담긴 화면이라 무인증 노출은 곧 사고다. 그래서 토큰이
 *      없으면 **부팅 자체를 거부**한다 — "토큰을 안 넣으면 무인증으로 뜬다"가 가능한 순간,
 *      누군가는 반드시 그렇게 띄운다(그리고 아무 에러도 나지 않는다).
 *
 *   ② **원격 DB 읽기 전용.** remote 모드는 운영 PostgreSQL 을 SSH 터널로 본다. 조회 도구가
 *      운영 데이터를 건드릴 이유는 없다. 인테이크(POST /api/usage)를 **등록하지 않고**,
 *      그 밖의 상태변경 경로도 열지 않는다(404 — 그 경로는 없다).
 *
 *   ③ **정적 서빙.** 뷰는 `/js/core.js` 를 절대 URL 로 부르므로 서버가 그 URL 공간을 정확히
 *      제공해야 한다. 직접 서빙하는 순간 경로 탈출이 우리 몫이 된다.
 *
 * 하네스는 자식 프로세스를 실제로 띄운다 — 게이트는 "함수가 true 를 돌려주는가"가 아니라
 * "그 포트에 붙었을 때 무엇이 오는가"로만 증명된다.
 */
const { test, describe, before, after } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawn } = require('node:child_process');

const { request, reservePort, APP } = require('./helper');

const ENTRY = path.join(APP, 'server.js');
// 16자 하한(약한 토큰 거부)을 넘는 값. 테스트 전용이라 비밀이 아니다.
const TOKEN = 'usage-standalone-test-token-abcdef0123456789';
const BOOT_MARK = 'usage-dashboard:';   // 기동 완료 표식(서버가 stdout 에 찍는다)
const BOOT_TIMEOUT_MS = 20_000;

/*
 * 자식 env 는 **명시로 조립한다.** 개발자 셸이나 CI 잡이 들고 있는 값(USAGE_PG_URL·USAGE_*)이
 * 새어 들어오면, 무토큰 부팅 거부 같은 음성 대조가 조용히 성립하지 않는다.
 */
function childEnv(over, dataDir) {
  const env = { ...process.env, USAGE_DATA_DIR: dataDir };
  for (const k of ['USAGE_PG_URL', 'USAGE_ADMIN_TOKEN', 'USAGE_INTAKE_TOKEN', 'USAGE_PORT', 'USAGE_HOST',
    'USAGE_DB_MODE', 'USAGE_TENANT', 'DATABASE_URL', 'USAGE_KEYWORD_RETENTION_DAYS']) delete env[k];
  return { ...env, ...over };
}

function tmpDataDir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'usage-standalone-'));
}

/** 부팅이 **실패**하기를 기대한다. { code, log } 를 돌려준다(정상 기동이면 테스트가 죽인다). */
function bootFails(over) {
  const dataDir = tmpDataDir();
  const child = spawn(process.execPath, [ENTRY], {
    cwd: APP, env: childEnv(over, dataDir), stdio: ['ignore', 'pipe', 'pipe'],
  });
  let log = '';
  child.stdout.on('data', (b) => { log += b; });
  child.stderr.on('data', (b) => { log += b; });
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      child.kill('SIGKILL');
      reject(new Error(`부팅이 거부되지 않고 계속 살아 있다:\n${log}`));
    }, BOOT_TIMEOUT_MS);
    child.once('exit', (code) => {
      clearTimeout(timer);
      try { fs.rmSync(dataDir, { recursive: true, force: true }); } catch { /* 비치명 */ }
      resolve({ code, log });
    });
  });
}

/** 정상 기동. { base, stop, log } — 포트 예약은 helper 의 reservePort 규율을 그대로 쓴다. */
async function boot(over = {}) {
  const dataDir = tmpDataDir();
  const held = await reservePort();
  const port = held.port;
  await held.release();

  const child = spawn(process.execPath, [ENTRY], {
    cwd: APP,
    env: childEnv({ USAGE_ADMIN_TOKEN: TOKEN, USAGE_PORT: String(port), USAGE_HOST: '127.0.0.1', ...over }, dataDir),
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  let log = '';
  child.stdout.on('data', (b) => { log += b; });
  child.stderr.on('data', (b) => { log += b; });

  const stop = () => new Promise((resolve) => {
    const done = () => {
      try { fs.rmSync(dataDir, { recursive: true, force: true }); } catch { /* 비치명 */ }
      resolve();
    };
    if (child.exitCode !== null) return done();
    child.once('exit', done);
    child.kill('SIGKILL');
  });

  /*
   * 준비 판정은 **자식 자신의 stdout 표식**으로 한다.
   * HTTP 응답만 보면 같은 포트를 잡은 남의 서버로 만족할 수 있다 — 그러면 이 파일의
   * 게이트 검증이 다른 프로세스의 설정을 검증하게 된다.
   */
  const until = Date.now() + BOOT_TIMEOUT_MS;
  while (Date.now() < until) {
    if (log.includes(BOOT_MARK) && log.includes(String(port))) {
      return { base: `http://127.0.0.1:${port}`, stop, log: () => log, port };
    }
    if (child.exitCode !== null) {
      await stop();
      throw new Error(`기동 중 죽었다(code=${child.exitCode}):\n${log}`);
    }
    await new Promise((r) => setTimeout(r, 50));
  }
  await stop();
  throw new Error(`기동 표식('${BOOT_MARK}')을 못 봤다:\n${log}`);
}

const bearer = (t = TOKEN) => ({ Authorization: `Bearer ${t}` });

// 보고 전용 토큰. admin 과 **다른 값**이어야 한다(같으면 서버가 부팅을 거부한다).
const INTAKE_TOKEN = 'usage-standalone-intake-token-9876543210fedcba';

/* ────────────────────────────────────────────────────────────────── */

describe('① 부팅 — 무인증 노출을 만들 수 있는 설정은 뜨지 않는다', () => {
  test('USAGE_ADMIN_TOKEN 이 없으면 부팅을 거부한다', async () => {
    const { code, log } = await bootFails({});
    assert.notEqual(code, 0, '토큰 없이도 떴다 — 사람별 사용량이 무인증으로 열린다');
    assert.match(log, /USAGE_ADMIN_TOKEN/,
      '거부 사유에 환경변수 이름이 없다 — 띄우려는 사람이 무엇을 넣어야 하는지 모른다');
  });

  test('짧은 토큰(<16자)도 거부한다 — 설정 실수를 인증으로 취급하지 않는다', async () => {
    const { code, log } = await bootFails({ USAGE_ADMIN_TOKEN: 'short' });
    assert.notEqual(code, 0);
    assert.match(log, /16/);
  });

  test('remote 모드인데 DATABASE_URL 이 없으면 거부한다', async () => {
    const { code, log } = await bootFails({ USAGE_ADMIN_TOKEN: TOKEN, USAGE_DB_MODE: 'remote' });
    assert.notEqual(code, 0);
    assert.match(log, /DATABASE_URL/);
  });

  test('모르는 USAGE_DB_MODE 는 거부한다 — 오타가 조용히 local 로 접히면 안 된다', async () => {
    const { code } = await bootFails({ USAGE_ADMIN_TOKEN: TOKEN, USAGE_DB_MODE: 'производство' });
    assert.notEqual(code, 0);
  });

  /*
   * 브라우저 차단 포트(WHATWG Fetch "bad ports")를 거부한다.
   *
   * 이건 이론이 아니라 실측이다(2026-08-06): 처음 기본값이 4190(managesieve)이었는데, 그 포트에서는
   * 서버가 정상 기동하고 curl 도 200 을 받는데 **브라우저에서만** 아무것도 안 된다 — 이동은
   * ERR_UNSAFE_PORT, 동일 출처 fetch 는 발신 자체가 막힌다. 서버 로그·curl·테스트가 전부 green 이라
   * "대시보드가 깨졌다" 외에는 단서가 남지 않는다. 그래서 부팅에서 끊는다.
   */
  test('브라우저 차단 포트는 거부한다 — 서버는 뜨는데 화면만 죽는 모양을 만들지 않는다', async () => {
    for (const p of ['4190', '6000', '6667']) {
      const { code, log } = await bootFails({ USAGE_ADMIN_TOKEN: TOKEN, USAGE_PORT: p });
      assert.notEqual(code, 0, `USAGE_PORT=${p} 로 떴다 — 브라우저에서는 열 수 없는 서버다`);
      assert.match(log, /브라우저/, `${p} 거부 사유가 원인을 말하지 않는다`);
    }
  });

  test('기본 포트가 차단 목록에 없다 — 기본값으로 띄운 사람이 곧장 막히면 안 된다', () => {
    const { readConfig } = require('../server');
    const cfg = readConfig({ USAGE_ADMIN_TOKEN: TOKEN });
    assert.deepEqual(cfg.errors, [], `기본 설정이 스스로 거부된다: ${cfg.errors.join(' / ')}`);
    // 명세 목록 중 이 대역에서 실제로 밟기 쉬운 값들.
    assert.ok(![4045, 4190, 6000].includes(cfg.port), `기본 포트 ${cfg.port} 가 차단 포트다`);
  });
});

describe('② local 모드 — 토큰 게이트', () => {
  let s;
  before(async () => { s = await boot(); });
  after(async () => { if (s) await s.stop(); });

  test('토큰 없이 /api/usage/summary 는 401', async () => {
    const r = await request(`${s.base}/api/usage/summary`);
    assert.equal(r.status, 401, `무토큰이 통과했다: ${r.text}`);
  });

  test('틀린 토큰은 401', async () => {
    const r = await request(`${s.base}/api/usage/summary`, { headers: bearer('wrong-token-but-long-enough-xxxx') });
    assert.equal(r.status, 401);
  });

  test('올바른 Bearer 토큰이면 /api/usage/summary 200', async () => {
    const r = await request(`${s.base}/api/usage/summary`, { headers: bearer() });
    assert.equal(r.status, 200, r.text);
    // 화면이 첫 카드로 읽는 축들 — 빈 DB 라도 shape 은 있어야 한다.
    assert.ok(r.json && r.json.totals, '응답에 totals 가 없다');
    assert.ok(Array.isArray(r.json.byUser), 'byUser 가 배열이 아니다');
    assert.ok(r.json.top && typeof r.json.top === 'object', 'top 축 묶음이 없다');
  });

  test('쿠키(usage_tok)로도 조회된다 — 브라우저가 헤더를 붙일 수 없다', async () => {
    const r = await request(`${s.base}/api/usage/summary`, { headers: { Cookie: `usage_tok=${TOKEN}` } });
    assert.equal(r.status, 200, r.text);
  });

  /*
   * 쿠키는 **조회만** 태운다. 쿠키 자격증명 + 상태변경은 곧 CSRF 표면이다 —
   * double-submit 토큰을 두는 대신 아예 헤더 인증만 인정한다.
   */
  test('쿠키 인증으로는 상태변경을 못 한다(403) — CSRF 표면을 만들지 않는다', async () => {
    const r = await request(`${s.base}/api/usage`, {
      method: 'POST', headers: { Cookie: `usage_tok=${TOKEN}` }, body: { machine: 'pc-x', sessions: [] },
    });
    assert.equal(r.status, 403, `쿠키만으로 쓰기가 통과했다: ${r.status} ${r.text}`);
  });

  test('local 모드에는 인테이크가 등록돼 있다(Bearer POST /api/usage → 200)', async () => {
    const r = await request(`${s.base}/api/usage`, {
      method: 'POST', headers: bearer(), body: { machine: 'pc-x', user: 'tester', sessions: [] },
    });
    assert.equal(r.status, 200, r.text);
    assert.equal(r.json.ok, true);
  });

  test('/healthz 는 무인증(기동 확인용, 데이터 없음)', async () => {
    const r = await request(`${s.base}/healthz`);
    assert.equal(r.status, 200);
    assert.equal(r.json.status, 'ok');
  });
});

/*
 * 보고 자격과 열람 자격의 분리.
 *
 * 왜 필요한가: 인테이크의 보고자는 팀원 PC 마다 깔린 수집기다 — 그 토큰은 **팀원 수만큼
 * 복제되어 각자의 디스크에 놓인다.** 그것이 곧 전원의 사용량·비용·머신명을 읽는 토큰이기도
 * 하면, 사본 하나만 새도 팀 전체가 열린다. 그래서 보고 전용 토큰은 `POST /api/usage` 하나만
 * 열어야 하고, 그 경계는 "함수가 무엇을 돌려주는가"가 아니라 **그 포트에 붙었을 때 무엇이
 * 오는가**로만 증명된다.
 */
describe('②-b 인테이크 토큰 — 보고는 되고 열람은 안 된다', () => {
  let s;
  before(async () => { s = await boot({ USAGE_INTAKE_TOKEN: INTAKE_TOKEN }); });
  after(async () => { if (s) await s.stop(); });

  test('인테이크 토큰으로 POST /api/usage 는 200', async () => {
    const r = await request(`${s.base}/api/usage`, {
      method: 'POST', headers: bearer(INTAKE_TOKEN), body: { machine: 'pc-i', user: 'tester', sessions: [] },
    });
    assert.equal(r.status, 200, `보고 전용 토큰으로 보고가 안 된다: ${r.status} ${r.text}`);
    assert.equal(r.json.ok, true);
  });

  test('인테이크 토큰으로 조회하면 403 — 사람별 비용이 수집기 토큰으로 열리면 안 된다', async () => {
    for (const p of ['/api/usage/summary', '/api/usage/sessions', '/api/usage/identity', '/api/usage/series']) {
      const r = await request(`${s.base}${p}`, { headers: bearer(INTAKE_TOKEN) });
      assert.equal(r.status, 403, `${p} 가 인테이크 토큰에 열렸다: ${r.status} ${r.text}`);
      assert.ok(!/username|byUser|totals/.test(r.text), `${p} 가 데이터를 흘렸다: ${r.text}`);
    }
  });

  test('인테이크 토큰으로 귀속 교정(PUT)도 못 한다 — 보고 외에는 아무것도 아니다', async () => {
    const r = await request(`${s.base}/api/usage/identity`, {
      method: 'PUT', headers: bearer(INTAKE_TOKEN), body: { machine: 'pc-i', username: 'someone' },
    });
    assert.equal(r.status, 403, `인테이크 토큰이 쓰기를 했다: ${r.status} ${r.text}`);
  });

  test('인테이크 토큰은 쿠키로 인정되지 않는다 — 보고자는 브라우저가 아니다', async () => {
    const r = await request(`${s.base}/api/usage/summary`, { headers: { Cookie: `usage_tok=${INTAKE_TOKEN}` } });
    assert.equal(r.status, 401, `인테이크 토큰이 쿠키로 통과했다: ${r.status}`);
  });

  test('admin 토큰은 그대로 전부 된다 — 분리가 기존 경로를 막지 않는다', async () => {
    assert.equal((await request(`${s.base}/api/usage/summary`, { headers: bearer() })).status, 200);
    const r = await request(`${s.base}/api/usage`, {
      method: 'POST', headers: bearer(), body: { machine: 'pc-x', sessions: [] },
    });
    assert.equal(r.status, 200);
  });

  test('기동 로그가 어느 자격으로 보고를 받는지 말한다(토큰 값은 찍지 않는다)', () => {
    assert.match(s.log(), /USAGE_INTAKE_TOKEN/);
    assert.ok(!s.log().includes(INTAKE_TOKEN), '기동 로그에 토큰 값이 찍혔다');
    assert.ok(!s.log().includes(TOKEN), '기동 로그에 admin 토큰 값이 찍혔다');
  });
});

describe('②-c 인테이크 토큰 — 분리한 척하는 설정은 뜨지 않는다', () => {
  test('admin 과 같은 값이면 부팅을 거부한다', async () => {
    const { code, log } = await bootFails({ USAGE_ADMIN_TOKEN: TOKEN, USAGE_INTAKE_TOKEN: TOKEN });
    assert.notEqual(code, 0, '같은 값인데 떴다 — 분리한 것처럼 보이지만 아무것도 분리되지 않았다');
    assert.match(log, /USAGE_INTAKE_TOKEN/);
  });

  test('짧은 인테이크 토큰도 거부한다 — admin 과 같은 하한을 쓴다', async () => {
    const { code, log } = await bootFails({ USAGE_ADMIN_TOKEN: TOKEN, USAGE_INTAKE_TOKEN: 'short' });
    assert.notEqual(code, 0);
    assert.match(log, /16/);
  });

  test('안 걸면 종전대로 admin 하나로 동작한다(하위호환)', () => {
    const { readConfig } = require('../server');
    const cfg = readConfig({ USAGE_ADMIN_TOKEN: TOKEN });
    assert.deepEqual(cfg.errors, []);
    assert.equal(cfg.intakeToken, '');
  });
});

describe('③ remote 모드 — 운영 DB 에 쓰기 경로가 열리지 않는다', () => {
  let s;
  // 접속하지 않는 URL 로 충분하다: 이 절의 검증은 **라우팅**이지 조회가 아니다.
  // (부팅도 쿼리를 날리지 않는다 — pg 스키마는 migrations 소유라 init 이 조기 반환한다.)
  before(async () => {
    s = await boot({ USAGE_DB_MODE: 'remote', DATABASE_URL: 'postgres://u:p@127.0.0.1:15432/usage' });
  });
  after(async () => { if (s) await s.stop(); });

  test('POST /api/usage(인테이크)가 404 — remote 에서는 등록되지 않는다', async () => {
    const r = await request(`${s.base}/api/usage`, {
      method: 'POST', headers: bearer(), body: { machine: 'pc-x', sessions: [] },
    });
    assert.equal(r.status, 404, `remote 모드에서 인테이크가 살아 있다: ${r.status} ${r.text}`);
  });

  test('귀속 교정 쓰기(PUT /api/usage/identity)도 404 — 읽기 전용이다', async () => {
    const r = await request(`${s.base}/api/usage/identity`, {
      method: 'PUT', headers: bearer(), body: { machine: 'pc-x', username: 'someone' },
    });
    assert.equal(r.status, 404, `remote 모드에서 쓰기가 열려 있다: ${r.status} ${r.text}`);
  });

  test('무토큰이면 remote 에서도 401(라우팅보다 게이트가 먼저)', async () => {
    const r = await request(`${s.base}/api/usage`, { method: 'POST', body: {} });
    assert.equal(r.status, 401);
  });

  test('기동 로그가 remote·읽기전용임을 말한다', () => {
    assert.match(s.log(), /remote/);
    assert.match(s.log(), /읽기 전용|read-only/i);
  });

  /*
   * RLS 성립 전제 검사가 **실제로 돌았는가.**
   *
   * 원래 결함이 정확히 이 자리였다: lib/db/rlsguard.js 는 판정을 갖고 있는데 그것을 부르는
   * 코드가 아무 데도 없었다. 판정이 맞는지는 usage-rlsguard.test.js 가 보고, 여기서는 그것이
   * 부팅 경로에 걸려 있는지를 본다 — 둘은 다른 사실이고, 둘 다 깨져도 화면은 멀쩡하다.
   *
   * 이 하네스의 DATABASE_URL 은 아무도 듣지 않는 포트라 판정은 '확인 불가'로 끝난다. 그것이
   * 정상 동작이다(터널을 뚫기 전 기동을 막지 않는다). 검증하는 것은 **검사가 침묵하지
   * 않았다**는 사실이다 — 확인 못 했다는 것 자체가 기록돼야 한다.
   *
   * ⚠ 여기서 검증되지 않는 것: 위반 롤(SUPERUSER·BYPASSRLS)이 실제로 부팅을 끊는가.
   *   그건 진짜 슈퍼유저 롤이 붙은 pg 가 있어야 하고, 그 조합은 assertRlsSafe 단위 테스트가
   *   주입으로 덮는다.
   */
  test('RLS 전제 검사가 부팅 경로에서 실제로 돈다 — 확인 못 했으면 그 사실을 남긴다', () => {
    assert.match(s.log(), /DB 롤/,
      'RLS 검사 흔적이 기동 로그에 없다 — rlsguard 가 다시 배선에서 떨어졌다');
    assert.match(s.log(), /확인하지 못했다|RLS 테넌트 격리 성립/);
  });
});

/*
 * ③-b RLS 위반 롤이 **실제로** 부팅을 끊는가 — 진짜 PostgreSQL 이 붙었을 때만 돈다.
 *
 * 왜 별도 관문인가: ③ 은 닿지 않는 URL 을 쓰므로 판정이 늘 '확인 불가'로 끝난다. 그 경로는
 * "검사가 돌았다"만 증명하고, **위반을 잡는다**는 것은 증명하지 않는다. 이 둘을 한 관문으로
 * 묶으면 가드가 아무것도 안 잡는 상태로 퇴화해도 스위트는 초록색이다(원래 결함이 정확히
 * 그 모양이었다 — 판정 로직은 멀쩡했고 부르는 코드가 없었다).
 *
 * 돌리는 법(로컬 클러스터 예):
 *   USAGE_TEST_PG_SUPER_URL=postgres://pgsuper@127.0.0.1:15433/usage \
 *   USAGE_TEST_PG_APP_URL=postgres://usage_app:<pw>@127.0.0.1:15433/usage \
 *   npm run test:standalone
 *
 * 앱 롤은 `CREATE ROLE … LOGIN NOSUPERUSER NOBYPASSRLS` 로 만든다. 스키마는 필요 없다 —
 * remote 부팅은 원격 DB 에 아무것도 쓰지 않고, 프로브는 pg_roles 만 읽는다.
 */
const PG_SUPER_URL = process.env.USAGE_TEST_PG_SUPER_URL || '';
const PG_APP_URL = process.env.USAGE_TEST_PG_APP_URL || '';
const pgMatrix = (PG_SUPER_URL && PG_APP_URL) ? describe : describe.skip;

pgMatrix('③-b RLS 위반 롤 — 실 PostgreSQL 대조', () => {
  test('SUPERUSER·BYPASSRLS 롤이면 부팅을 거부한다', async () => {
    const { code, log } = await bootFails({
      USAGE_ADMIN_TOKEN: TOKEN, USAGE_DB_MODE: 'remote', DATABASE_URL: PG_SUPER_URL,
    });
    assert.notEqual(code, 0, '격리가 성립하지 않는 롤로 떴다 — 전 테넌트 데이터가 서로 보인다');
    assert.match(log, /RLS 테넌트 격리가 성립하지 않습니다/);
    assert.match(log, /NOSUPERUSER/, '고치는 방법이 없으면 거부가 막다른 길이 된다');
  });

  /*
   * 거부가 **즉시** 끝나야 한다.
   *
   * 실측 회귀(고치기 전): 거부 메시지를 찍고도 10.5초를 더 살았다. 프로브가 성공한 뒤
   * 거부하는 경로라 pg.Pool 의 유휴 커넥션이 이벤트 루프를 붙잡았기 때문이다(idleTimeoutMillis
   * 기본 10초). 사람이 보기엔 "에러를 냈는데 안 죽는다"이고, 재시작을 거는 감독 프로세스에겐
   * 매 기동마다 10초 지연이다. 종료 자체는 되므로 exit code 만 보는 검사로는 안 잡힌다.
   */
  test('거부가 풀을 붙잡고 늘어지지 않는다(유휴 커넥션 회귀 방지)', async () => {
    const t0 = Date.now();
    const { code } = await bootFails({
      USAGE_ADMIN_TOKEN: TOKEN, USAGE_DB_MODE: 'remote', DATABASE_URL: PG_SUPER_URL,
    });
    const elapsed = Date.now() - t0;
    assert.notEqual(code, 0);
    assert.ok(elapsed < 5000, `거부에 ${elapsed}ms 걸렸다 — 풀을 닫지 않아 유휴 커넥션이 루프를 잡고 있다`);
  });

  test('비-슈퍼·비-BYPASSRLS 앱 롤은 뜨고, 격리 성립을 로그로 말한다', async () => {
    const s = await boot({ USAGE_DB_MODE: 'remote', DATABASE_URL: PG_APP_URL });
    try {
      assert.match(s.log(), /RLS 테넌트 격리 성립/);
      assert.ok(!/확인하지 못했다/.test(s.log()), '앱 롤이 붙었는데 확인 불가로 접혔다');
      // 게이트는 remote 에서도 그대로다 — 롤이 정상이라고 무인증이 되지 않는다.
      assert.equal((await request(`${s.base}/api/usage/summary`)).status, 401);
    } finally {
      await s.stop();
    }
  });
});

describe('④ 정적 셸 — 뷰는 수정하지 않고 그대로 뜬다', () => {
  let s;
  before(async () => { s = await boot(); });
  after(async () => { if (s) await s.stop(); });

  test('셸 HTML 200(무인증) · 뷰가 요구하는 마운트 지점을 갖췄다', async () => {
    const r = await request(`${s.base}/`);
    assert.equal(r.status, 200, r.text);
    assert.match(r.headers['content-type'] || '', /text\/html/);
    // core.js 는 로드 시점에 #app 을 잡고, toast() 는 #toast 를 찾는다 — 없으면 뷰가 죽는다.
    assert.match(r.text, /id="app"/, '#app 이 없다 — core.js 가 로드 즉시 null 을 잡는다');
    assert.match(r.text, /id="toast"/, '#toast 가 없다 — usageobs 의 오류 안내가 예외로 바뀐다');
    assert.match(r.text, /<script type="module" src="\/app\.js">/, '셸 스크립트를 물지 않는다');
    // 인라인 스크립트가 하나라도 생기면 CSP script-src 'self' 를 포기해야 한다.
    // 주석은 먼저 걷어낸다 — 이 문서의 주석이 그 규율을 설명하며 <script> 를 인용하고 있어서,
    // 안 걷으면 규율을 적어 둔 것 자체가 위반으로 잡힌다.
    const markup = r.text.replace(/<!--[\s\S]*?-->/g, '');
    assert.ok(!/<script(?![^>]*\bsrc=)[^>]*>[\s\S]*?\S[\s\S]*?<\/script>/.test(markup),
      '인라인 <script> 가 생겼다 — CSP script-src 를 풀지 않으면 그 스크립트가 죽는다');
  });

  /*
   * 탭 두 개가 **셸에 배선돼 있는가.** 라벨·id 는 app.js 가 단일 소스라(HTML 에 복제하지 않는다)
   * 여기서 소스 문자열을 본다. 탭이 사라져도 다른 어떤 검사도 울지 않는다 — 뷰 자체는
   * 멀쩡하고 화면에서만 없어진다.
   */
  test('셸이 두 탭(사용 추적·사용 관측)을 뷰 URL 로 물고 있다', async () => {
    const r = await request(`${s.base}/app.js`);
    assert.equal(r.status, 200, r.text);
    for (const [id, label, file, fn] of [
      ['usage', '사용 추적', 'usagetrack.js', 'renderUsage'],
      ['usageobs', '사용 관측', 'usageobs.js', 'renderUsageObs'],
    ]) {
      assert.match(r.text, new RegExp(`id: '${id}'`), `'${label}' 탭 정의가 사라졌다`);
      assert.match(r.text, new RegExp(`label: '${label}'`), `'${label}' 라벨이 바뀌었다 — 탭 계약이다`);
      assert.match(r.text, new RegExp(`import\\('/views/${file}'\\)`),
        `${file} 를 서버가 서빙하는 URL 로 부르지 않는다`);
      assert.match(r.text, new RegExp(`m\\.${fn}`), `${fn} 를 꺼내 쓰는 자리가 없다`);
    }
  });

  for (const v of ['usagetrack.js', 'usageobs.js']) {
    test(`뷰 ${v} 가 /views/ URL 로 200`, async () => {
      const r = await request(`${s.base}/views/${v}`);
      assert.equal(r.status, 200, r.text);
      assert.match(r.headers['content-type'] || '', /javascript/);
      // 뷰는 공용 모듈을 절대경로로 부른다 — 서버가 그 URL 을 제공하지 못하면 화면이 통째로 빈다.
      assert.match(r.text, /from '\/js\/core\.js'/);
    });
  }

  test('뷰가 기대하는 /js/core.js · /js/router.js 가 200', async () => {
    for (const p of ['/js/core.js', '/js/router.js']) {
      const r = await request(`${s.base}${p}`);
      assert.equal(r.status, 200, `${p}: ${r.status}`);
      assert.match(r.headers['content-type'] || '', /javascript/);
    }
    // router 는 뷰가 쓰는 isStale 을 반드시 내보낸다(없으면 뷰 import 가 죽는다).
    const router = await request(`${s.base}/js/router.js`);
    assert.match(router.text, /export const isStale|export function isStale/);
  });

  test('경로 탈출이 막힌다 — 화이트리스트 밖은 무엇도 나가지 않는다', async () => {
    const escapes = [
      '/js/%2e%2e/%2e%2e/server.js',
      '/views/%2e%2e/%2e%2e/index.js',
      '/views/../../lib/store.js',
      '/%2e%2e/config.json',
      '/js/core.js%00',
      '/views/sub/dir.js',
    ];
    for (const p of escapes) {
      const r = await request(`${s.base}${p}`);
      assert.ok(r.status === 404 || r.status === 403,
        `${p} 가 ${r.status} 로 응답했다 — 정적 매핑 밖이 열려 있다`);
      assert.ok(!/USAGE_ADMIN_TOKEN|module\.exports/.test(r.text),
        `${p} 가 서버 소스를 흘렸다`);
    }
  });

  test('서버 코드는 어떤 URL 로도 서빙되지 않는다', async () => {
    for (const p of ['/index.js', '/lib/store.js', '/server.js', '/routes/usage.js', '/package.json']) {
      const r = await request(`${s.base}${p}`);
      assert.equal(r.status, 404, `${p} 가 열려 있다`);
    }
  });
});
