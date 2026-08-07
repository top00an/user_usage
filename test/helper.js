'use strict';
/*
 * 테스트 하네스 — 이 스위트가 실제로 쓰는 것만 담는다.
 *
 * 여기 있는 것은 셋뿐이다: 레포 루트 경로 · HTTP 클라이언트 · 포트 예약 · pg 모드 판정.
 * "언젠가 쓸지도" 로 헬퍼를 늘리지 않는다 — 하네스가 커지면 테스트가 하네스의 버그를 검증하게 된다.
 */
const net = require('net');
const path = require('path');
const http = require('http');

/** 레포 루트. 자식 프로세스의 cwd·진입점 경로를 만들 때 쓴다. */
const APP = path.join(__dirname, '..');

/*
 * 포트 예약 — 번호를 받아 **놓지 않고 들고 있다가**, 자식을 띄우기 직전에 놓는다.
 *
 * 0번 포트로 listen 해 번호만 받고 곧바로 닫으면, 그 사이 다른 프로세스(또는 같은 스위트의 다른
 * 테스트 파일)가 그 번호를 가져갈 창이 열린다. node --test 는 파일마다 프로세스를 띄우므로
 * 그 창이 실제로 부딪힌다. release() 를 자식 spawn 직전에 부르면 창이 최소가 된다.
 */
function reservePort() {
  return new Promise((resolve, reject) => {
    const s = net.createServer();
    s.on('error', reject);
    s.listen(0, '127.0.0.1', () => {
      const { port } = s.address();
      resolve({
        port,
        release: () => new Promise((done) => { s.close(() => done()); }),
      });
    });
  });
}

/*
 * HTTP 요청 — { status, headers, text, json, raw }.
 *
 * ⚠ agent:false — **연결을 재사용하지 않는다.**
 *
 * Node 19+ 는 http.globalAgent 의 keepAlive 를 기본 on 으로 바꿨다(keepAliveMsecs 1000).
 * 서버는 유휴 소켓을 keepAliveTimeout(기본 5000ms)에 닫는다. 클라이언트가 바로 그 순간
 * 재사용하면 ECONNRESET("socket hang up")이 난다 — 요청은 정상인데 소켓만 죽는, 잘 알려진 경쟁이다.
 * 연속으로 여러 요청을 보내는 테스트가 그 재사용 창을 여러 번 지나가므로 가장 먼저 걸린다.
 *
 * 요청마다 새 소켓을 쓰는 쪽을 고른다. 테스트 클라이언트는 성능이 목적이 아니고, 서버의
 * keepAliveTimeout 을 늘려 창을 좁히는 것은 **운영 코드를 테스트 사정으로 바꾸는** 것이라 방향이 틀렸다.
 */
function request(url, opts = {}) {
  return new Promise((resolve, reject) => {
    const u = new URL(url);
    const payload = opts.body === undefined
      ? null
      : (Buffer.isBuffer(opts.body) || typeof opts.body === 'string' ? opts.body : JSON.stringify(opts.body));
    const headers = { ...(opts.headers || {}) };
    if (payload !== null) {
      if (!Buffer.isBuffer(opts.body)) headers['Content-Type'] = headers['Content-Type'] || 'application/json';
      headers['Content-Length'] = Buffer.byteLength(payload);
    }
    const req = http.request(
      u,
      { method: opts.method || 'GET', headers, timeout: 10000, agent: false },
      (res) => {
        const chunks = [];
        res.on('data', (c) => chunks.push(c));
        res.on('end', () => {
          const raw = Buffer.concat(chunks);
          const text = raw.toString('utf8');
          let json;
          try { json = JSON.parse(text); } catch { /* JSON 이 아닐 수 있다(정적 파일 등) */ }
          resolve({ status: res.statusCode, headers: res.headers, text, json, raw });
        });
      },
    );
    req.on('error', reject);
    req.on('timeout', () => req.destroy(new Error('timeout')));
    if (payload) req.write(payload);
    req.end();
  });
}

/*
 * PostgreSQL 대상 실행인지. 이 스위트의 기본은 sqlite 이고, pg 는 스키마를 격리한 CI 잡에서만 돈다.
 * 방언별로 갈리는 단언(UPSERT 충돌키·타입)은 이 값으로 분기한다.
 */
function pgActive() { return !!process.env.USAGE_PG_SCHEMA; }

module.exports = { APP, request, reservePort, pgActive };
