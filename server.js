'use strict';
/*
 * 사용량 대시보드 — HTTP 진입점.
 *
 * 이 서비스가 하는 일은 하나다: 팀원 PC 의 수집기가 보내는 사용량 텔레메트리를 받아 저장하고
 * 조회용 대시보드로 보여준다. 그 밖의 것(사용자 관리·세션·역할 체계)은 **없다** — 이건 조회
 * 도구이고, 인증은 토큰 하나로 끝난다.
 *
 * ── 이 파일이 지는 세 가지 책임 ─────────────────────────────────────────
 * 각각을 빠뜨리면 조용한 사고가 된다:
 *
 *   ① 인증.  없으면 사람별 사용량·비용이 무인증으로 열린다.
 *      → `USAGE_ADMIN_TOKEN` 게이트. **토큰이 없으면 부팅을 거부한다.** 옵션으로 두면
 *        누군가는 반드시 토큰 없이 띄우고, 그때 아무 에러도 나지 않는다.
 *      ⚠ 세션·역할·MFA 체계를 여기에 만들지 않는다. 조회 도구에 인증 체계를 두 벌 만드는
 *        순간 그중 한 벌만 고쳐지는 날이 온다.
 *
 *   ② CSRF 표면 제거.  쿠키 자격증명 + 상태변경은 곧 CSRF 표면이므로 **쿠키는 조회만**
 *      태운다(상태변경은 Authorization 헤더만 인정 → 403). 브라우저는 임의 헤더를 붙일 수
 *      없으니 화면은 자연히 조회 전용이 되고, double-submit 토큰을 둘 이유가 사라진다.
 *
 *   ③ 테넌트 스코프(tenantStore.run).  pg 백엔드는 매 쿼리에서 currentTenant() 를 RLS 로
 *      주입한다. 감싸지 않으면 'default' 로 흐르는데, remote 로 남의 DB 를 볼 때 그게 맞는다는
 *      보장이 없다 → `USAGE_TENANT` 로 명시할 수 있게 열어 두고 요청 1지점을 감싼다.
 *
 * ── 2모드(USAGE_DB_MODE) ────────────────────────────────────────────────
 *   local(기본)  로컬 sqlite(USAGE_DATA_DIR). 인테이크까지 전부 뜬다.
 *   remote       DATABASE_URL 로 원격 PostgreSQL(SSH 터널 localhost:15432 가정). **읽기 전용** —
 *                인테이크(POST /api/usage)를 등록하지 않고 상태변경 경로를 아예 열지 않는다.
 *                보존 정리기(keyword 삭제)도 띄우지 않는다. 조회 도구가 운영 데이터를
 *                건드릴 이유는 없고, "안 건드릴 것"은 규율이 아니라 배선으로 막아야 한다.
 *
 * 기동: `USAGE_ADMIN_TOKEN=… npm start` — 자세한 절차는 README.md.
 */
const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');

const APP_DIR = __dirname;
const PUBLIC = path.join(APP_DIR, 'public');

/*
 * 기본 포트 — **4190 이 아니다.**
 *
 * 처음 정한 값은 4190 이었는데, 그 포트로는 화면이 뜨지 않는다: 4190(managesieve)은 WHATWG Fetch
 * 명세의 **차단 포트 목록**에 있고 Chrome·Firefox·undici 가 전부 구현한다. 브라우저는 그 포트로의
 * 이동을 ERR_UNSAFE_PORT 로 거부하고, 페이지가 어찌어찌 떠도 동일 출처 fetch 가 통째로 막힌다
 * (실측 2026-08-06: node fetch → `bad port`, 서버는 멀쩡히 응답하는데 클라이언트가 안 보낸다).
 *
 * 서버 로그는 정상이고 curl 도 200 인데 브라우저에서만 아무것도 안 되는 모양이라, 원인을
 * 짚기 전까지는 "대시보드가 깨졌다"로만 보인다. 그래서 기본을 옆 번호로 옮기고,
 * 아래 assertUsablePort 가 사용자가 직접 차단 포트를 지정하는 경우까지 부팅에서 끊는다.
 */
const DEFAULT_PORT = 4191;
const DEFAULT_HOST = '127.0.0.1';   // 기본은 루프백 — 토큰이 있어도 LAN 에 자동 노출하지 않는다
const MODES = Object.freeze(['local', 'remote']);

/*
 * 브라우저가 거부하는 포트(WHATWG Fetch "bad ports"). 이 목록에 있는 포트로 띄우면 서버는
 * 멀쩡히 뜨고 curl 도 통과하는데 **브라우저에서만** 아무것도 안 된다 — 진단이 가장 어려운 모양이다.
 * 그래서 부팅에서 끊는다. 조회 도구라 대체 포트를 고르는 비용이 0 이다.
 */
const BAD_PORTS = new Set([
  1, 7, 9, 11, 13, 15, 17, 19, 20, 21, 22, 23, 25, 37, 42, 43, 53, 69, 77, 79, 87, 95, 101, 102,
  103, 104, 109, 110, 111, 113, 115, 117, 119, 123, 135, 137, 138, 139, 143, 161, 179, 389, 427,
  465, 512, 513, 514, 515, 526, 530, 531, 532, 540, 548, 554, 556, 563, 587, 601, 636, 989, 990,
  993, 995, 1719, 1720, 1723, 2049, 3659, 4045, 4190, 5060, 5061, 6000, 6566, 6665, 6666, 6667,
  6668, 6669, 6679, 6697, 10080,
]);
/*
 * 토큰 하한. 짧은 토큰은 인증이 아니라 설정 실수이고,
 * 그것을 인증으로 취급하면 게이트가 있다는 착각만 남는다.
 */
const MIN_TOKEN_LEN = 16;

/* ── 설정 ────────────────────────────────────────────────────────────
 * 순수 함수다(부작용 없음) — 부팅 거부 판단을 서버 기동 없이 검증할 수 있게.
 * 잘못된 설정은 **모아서** 돌려준다. 하나씩 알려주면 고치고 다시 띄우기를 반복하게 된다.
 */
function readConfig(env = process.env) {
  const errors = [];
  const token = String(env.USAGE_ADMIN_TOKEN || '').trim();
  if (!token) {
    errors.push('USAGE_ADMIN_TOKEN 이 없다 — 이 대시보드는 사람별 사용량·비용을 담고 있어 '
      + '무인증으로 띄우지 않는다(기본값 없음이 의도다).');
  } else if (token.length < MIN_TOKEN_LEN) {
    errors.push(`USAGE_ADMIN_TOKEN 이 너무 짧다(${token.length}자) — 최소 ${MIN_TOKEN_LEN}자.`);
  }

  const mode = String(env.USAGE_DB_MODE || 'local').trim().toLowerCase() || 'local';
  if (!MODES.includes(mode)) {
    errors.push(`USAGE_DB_MODE 가 '${mode}' 다 — ${MODES.join('|')} 중 하나여야 한다(오타를 local 로 접지 않는다).`);
  }

  const databaseUrl = String(env.DATABASE_URL || '').trim();
  if (mode === 'remote' && !databaseUrl) {
    errors.push('remote 모드인데 DATABASE_URL 이 없다 — SSH 터널의 접속 문자열이 필요하다'
      + '(예: postgres://usage:…@127.0.0.1:15432/usage).');
  }

  const rawPort = String(env.USAGE_PORT || '').trim();
  let port = DEFAULT_PORT;
  if (rawPort) {
    const n = Number(rawPort);
    if (!Number.isInteger(n) || n < 1 || n > 65535) errors.push(`USAGE_PORT 가 포트 번호가 아니다: ${rawPort}`);
    else port = n;
  }
  if (BAD_PORTS.has(port)) {
    errors.push(`USAGE_PORT=${port} 는 브라우저가 차단하는 포트다(WHATWG Fetch bad ports) — `
      + '서버는 뜨고 curl 도 통과하지만 브라우저에서는 이동도 fetch 도 막혀 화면이 통째로 죽는다. '
      + `다른 번호를 쓰라(기본 ${DEFAULT_PORT}).`);
  }

  return {
    token,
    mode,
    databaseUrl,
    port,
    host: String(env.USAGE_HOST || DEFAULT_HOST).trim() || DEFAULT_HOST,
    tenant: String(env.USAGE_TENANT || 'default').trim() || 'default',
    // remote = 읽기 전용. 모드와 별개 이름으로 두는 이유: 호출부가 "원격이니까"가 아니라
    // "읽기 전용이니까" 로 분기해야 나중에 모드가 늘어도 규칙이 흐려지지 않는다.
    readOnly: mode === 'remote',
    errors,
  };
}

/* ── HTTP 유틸(모든 응답이 이 shape 를 쓴다) ─────────────────────────── */
function sendJson(res, code, obj, headers) {
  res.writeHead(code, Object.assign({ 'Content-Type': 'application/json; charset=utf-8' }, headers || {}));
  res.end(JSON.stringify(obj));
}

function readBody(req, opts) {
  const limit = (opts && opts.limit) || 5e6;
  return new Promise((resolve, reject) => {
    const chunks = []; let len = 0;
    req.on('data', (c) => {
      chunks.push(c); len += c.length;
      if (len > limit) { chunks.length = 0; req.destroy(); reject(new Error('body too large')); }
    });
    req.on('end', () => resolve(Buffer.concat(chunks).toString('utf8')));
    req.on('error', reject);
  });
}

async function jsonBody(req) { const b = await readBody(req); return b ? JSON.parse(b) : {}; }

function parseCookies(req) {
  const o = {};
  (req.headers.cookie || '').split(';').forEach((p) => {
    const i = p.indexOf('=');
    if (i > 0) o[p.slice(0, i).trim()] = decodeURIComponent(p.slice(i + 1).trim());
  });
  return o;
}

/*
 * 상수시간 비교 — 길이가 달라도 타이밍으로 새지 않게 양쪽을 HMAC(고정 32B)으로 접은 뒤 비교한다.
 * 키는 프로세스마다 무작위라 다이제스트 자체는 비밀이 아니다.
 */
const CMP_KEY = crypto.randomBytes(32);
function safeEqual(a, b) {
  const da = crypto.createHmac('sha256', CMP_KEY).update(String(a), 'utf8').digest();
  const db = crypto.createHmac('sha256', CMP_KEY).update(String(b), 'utf8').digest();
  return crypto.timingSafeEqual(da, db);
}

/*
 * 자격증명 판정. 통과하면 **경로**('header'|'cookie')를 돌려준다 — 호출부가 그 둘을 다르게
 * 취급해야 하기 때문이다(쿠키는 조회만).
 * Authorization 이 있는데 틀렸으면 쿠키로 흘리지 않는다(폴백이 있으면 게이트가 흐려진다).
 */
function authenticate(req, token) {
  const h = String(req.headers.authorization || '');
  if (h.startsWith('Bearer ')) return safeEqual(h.slice(7).trim(), token) ? 'header' : null;
  const c = parseCookies(req).usage_tok;
  if (c) return safeEqual(c, token) ? 'cookie' : null;
  return null;
}

/* ── 정적 서빙 ───────────────────────────────────────────────────────
 * **경로 화이트리스트**다. 디렉터리를 열고 `..` 를 막는 대신, 나갈 수 있는 URL 을 통째로
 * 열거한다 — 셸이 필요로 하는 파일이 열 손가락 안이라 열거가 가능하고, 그러면 경로 탈출이라는
 * 문제 자체가 성립하지 않는다(정규화·심링크·인코딩을 고민할 자리가 없다).
 */
const STATIC = new Map([
  ['/', path.join(PUBLIC, 'index.html')],
  ['/index.html', path.join(PUBLIC, 'index.html')],
  ['/app.js', path.join(PUBLIC, 'app.js')],
  ['/app.css', path.join(PUBLIC, 'app.css')],
  ['/favicon.svg', path.join(PUBLIC, 'favicon.svg')],
  ['/js/core.js', path.join(PUBLIC, 'js', 'core.js')],
  ['/js/router.js', path.join(PUBLIC, 'js', 'router.js')],
  ['/js/theme-boot.js', path.join(PUBLIC, 'js', 'theme-boot.js')],
]);

// 뷰만 예외적으로 패턴이다(파일이 늘어난다). 파일명 문자 집합에 `.` 연속도 `/` 도 들어갈 수
// 없어 lib/·routes/ 에는 닿지 않는다.
const VIEW_RE = /^\/views\/([a-z0-9-]+\.js)$/;

const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.woff2': 'font/woff2',
  '.svg': 'image/svg+xml',
  '.ico': 'image/x-icon',
};

// 인라인 스크립트가 없으므로 script-src 는 'self' 로 끝난다
// (뷰가 style="…" 속성을 쓰기 때문에 style-src 만 unsafe-inline 을 남긴다).
const CSP = [
  "default-src 'none'", "script-src 'self'", "style-src 'self' 'unsafe-inline'",
  "img-src 'self' data:", "font-src 'self'", "connect-src 'self'",
  "form-action 'self'", "base-uri 'none'", "object-src 'none'", "frame-ancestors 'none'",
].join('; ');

function resolveStatic(p) {
  const hit = STATIC.get(p);
  if (hit) return hit;
  const m = VIEW_RE.exec(p);
  if (!m) return null;
  const file = path.join(PUBLIC, 'views', m[1]);
  // 정규식이 이미 막지만 이중 방어를 둔다 — 정규식은 언젠가 완화되고, 그때 이 줄이 남는다.
  const viewsDir = path.join(PUBLIC, 'views');
  return file.startsWith(viewsDir + path.sep) ? file : null;
}

function serveFile(req, res, abs) {
  fs.readFile(abs, (err, buf) => {
    if (err) return sendJson(res, 404, { error: 'not found' });
    res.writeHead(200, {
      'Content-Type': MIME[path.extname(abs).toLowerCase()] || 'application/octet-stream',
      'Content-Length': buf.length,
      'Cache-Control': 'no-cache',
      'X-Frame-Options': 'DENY',
      'X-Content-Type-Options': 'nosniff',
      'Referrer-Policy': 'same-origin',
      'Content-Security-Policy': CSP,
    });
    res.end(req.method === 'HEAD' ? undefined : buf);
  });
}

/* ── ctx ─────────────────────────────────────────────────────────────
 * 라우트가 요구하는 것만 정확히 채운다(routes/* 가 쓰는 필드 전부):
 *   u·p·me·sendJson·jsonBody·requireRole·isLanReq·isAdminReq·syncTenant
 *
 * 게이트는 이미 handle() 이 통과시켰으므로 requireRole 은 항상 true 다 — 역할 체계를 여기서
 * 흉내내지 않는다(토큰을 가진 사람 = 이 도구의 유일한 사용자).
 * isLanReq 는 **false** 로 둔다: 인테이크가 "LAN 이면 등록됨"으로 열리는 자리인데, 이 서비스에서
 * 등록 근거는 네트워크 위치가 아니라 토큰 하나여야 한다(isAdminReq 가 그 역할을 진다).
 */
const ME = Object.freeze({ u: 'usage-admin', r: 'admin', role: 'admin' });

function makeCtx(u, p) {
  return {
    u, p, me: ME, role: ME.role,
    sendJson, jsonBody, readBody, parseCookies,
    requireRole: () => true,
    isAdminReq: () => true,
    isLanReq: () => false,
    isLocalReq: () => false,
    syncTenant: null,
  };
}

/*
 * 라우트 체인. 순서가 계약이다 —
 * **analytics 가 admin 보다 앞**이어야 한다(admin 이 /api/usage 접두사를 통째로 소유하고
 * 안 걸리면 404 를 직접 내므로, 뒤로 가면 관측 화면이 통째로 404 가 된다).
 *
 * readOnly(=remote)에서는 인테이크를 빼는 것으로 끝나지 않는다. admin 라우트 안에는 귀속 교정
 * 쓰기(PUT/DELETE /api/usage/identity)가 있어서, 그대로 두면 운영 DB 에 쓴다.
 * 그래서 조회 메서드만 통과시키고 나머지는 404 로 끊는다 — 405 가 아니라 404 인 이유는,
 * 이 모드에서 그 엔드포인트는 "지금은 막혔다"가 아니라 **존재하지 않기** 때문이다.
 */
function buildRoutes(usage, readOnly) {
  if (!readOnly) return [usage.routes.intake, usage.routes.analytics, usage.routes.admin];

  const readOnlyAdmin = async (req, res, ctx) => {
    if (!ctx.p.startsWith('/api/usage')) return false;
    if (req.method !== 'GET' && req.method !== 'HEAD') {
      sendJson(res, 404, { error: 'not found' });
      return true;
    }
    return usage.routes.admin(req, res, ctx);
  };
  return [usage.routes.analytics, readOnlyAdmin];
}

/* ── 라우터 ──────────────────────────────────────────────────────────── */
function createApp(cfg, usage) {
  const routes = buildRoutes(usage, cfg.readOnly);
  const { runWithTenant } = require('./lib/tenant');

  async function handle(req, res) {
    const u = new URL(req.url || '/', `http://${cfg.host}:${cfg.port}`);
    const p = u.pathname;

    // 무인증·무DB — 기동 확인용. 데이터가 없으므로 게이트 위에 둔다.
    if (p === '/healthz') return sendJson(res, 200, { status: 'ok' });

    if (req.method === 'GET' || req.method === 'HEAD') {
      const abs = resolveStatic(p);
      // 셸·뷰는 무인증이다. 데이터는 전부 /api/* 로 오고 그쪽에 게이트가 있다 —
      // 화면 껍데기를 가리면 "토큰을 어디에 넣어야 하는가"를 안내할 자리가 사라진다.
      if (abs) return serveFile(req, res, abs);
    }

    if (!p.startsWith('/api/')) return sendJson(res, 404, { error: 'not found' });

    const via = authenticate(req, cfg.token);
    if (!via) {
      return sendJson(res, 401, { error: 'unauthorized' }, { 'WWW-Authenticate': 'Bearer realm="usage"' });
    }
    // 쿠키 자격증명으로는 상태변경을 태우지 않는다(CSRF 표면 제거 — 파일 상단 ② 참조).
    if (req.method !== 'GET' && req.method !== 'HEAD' && via === 'cookie') {
      return sendJson(res, 403, {
        error: '쿠키 인증으로는 상태변경을 할 수 없습니다 — Authorization: Bearer 를 사용하세요',
      });
    }

    const ctx = makeCtx(u, p);
    return runWithTenant(cfg.tenant, async () => {
      try {
        for (const route of routes) {
          if (await route(req, res, ctx)) return;
        }
        sendJson(res, 404, { error: 'not found' });
      } catch (err) {
        if (!res.headersSent) sendJson(res, 400, { error: String((err && err.message) || err) });
        else res.end();
      }
    });
  }

  // 핸들러 밖으로 새는 거부(잘못된 JSON 본문 등)가 프로세스를 죽이지 않게 하는 마지막 안전망.
  return http.createServer((req, res) => {
    Promise.resolve().then(() => handle(req, res)).catch((err) => {
      try {
        if (!res.headersSent) sendJson(res, 400, { error: String((err && err.message) || err) });
        else res.end();
      } catch { /* 응답이 이미 끊긴 경우 */ }
    });
  });
}

/* ── 부팅 ────────────────────────────────────────────────────────────── */
function help() {
  return [
    '',
    '사용법: USAGE_ADMIN_TOKEN=<토큰> node server.js',
    '',
    '  USAGE_ADMIN_TOKEN  (필수) 조회 토큰. Authorization: Bearer <토큰> 또는 쿠키 usage_tok.',
    `  USAGE_PORT         포트(기본 ${DEFAULT_PORT}). 브라우저 차단 포트(4190·6000 등)는 거부한다`,
    `  USAGE_HOST         바인드 주소(기본 ${DEFAULT_HOST} — 루프백)`,
    '  USAGE_DB_MODE      local(기본, 로컬 sqlite) | remote(DATABASE_URL 로 원격 pg, 읽기 전용)',
    '  DATABASE_URL       remote 모드의 접속 문자열(SSH 터널 예: postgres://…@127.0.0.1:15432/usage)',
    '  USAGE_TENANT       조회 테넌트(기본 default)',
    '  USAGE_DATA_DIR     local 모드의 sqlite 디렉터리(기본 <repo>/data)',
    '',
    '자세한 절차: README.md',
  ].join('\n');
}

async function main(env = process.env) {
  const cfg = readConfig(env);
  if (cfg.errors.length) {
    console.error('사용량 대시보드 기동을 거부한다:');
    for (const e of cfg.errors) console.error(`  · ${e}`);
    console.error(help());
    process.exitCode = 2;
    return null;
  }

  /*
   * DB 백엔드 선택은 **lib 를 require 하기 전에** 끝나야 한다. lib/db/adapter 가 로드 시점의
   * USAGE_PG_URL 로 드라이버를 고르고, sqlite 라면 그 자리에서 파일을 연다.
   * 그래서 index.js 는 여기서(설정 확정 이후) 늦게 집는다.
   */
  if (cfg.mode === 'remote') process.env.USAGE_PG_URL = cfg.databaseUrl;
  const usage = require('./index');

  // 스키마 보장. sqlite 면 DDL(멱등), pg 면 조기 반환한다(스키마는 migrations 소유) —
  // 즉 remote 모드의 부팅은 원격 DB 에 아무것도 쓰지 않는다.
  await usage.init();

  const disposers = [];
  if (cfg.readOnly) {
    console.log('  · remote 모드 — 읽기 전용(인테이크·귀속 쓰기·보존 정리 모두 미등록)');
  } else {
    const ret = usage.startRetention();
    if (ret) { disposers.push(() => ret.stop()); console.log(`  · 키워드 보존 정리: ${ret.days}일`); }
  }

  const server = createApp(cfg, usage);
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(cfg.port, cfg.host, resolve);
  });

  // 기동 표식 — 하네스가 "이 자식이 그 포트를 잡았다"를 확인하는 근거다(test/helper.js 와 같은 규율).
  // 토큰은 절대 찍지 않는다.
  console.log(`usage-dashboard: http://${cfg.host}:${cfg.port}  (mode=${cfg.mode}, tenant=${cfg.tenant})`);
  console.log('  · 브라우저에서 열고 토큰을 입력하면 두 탭(사용 추적·사용 관측)이 뜬다.');

  const shutdown = () => {
    for (const d of disposers) { try { d(); } catch { /* 정리 실패가 종료를 막지 않는다 */ } }
    server.close(() => process.exit(0));
    // 열린 keep-alive 커넥션이 종료를 무한정 잡지 않게 한다.
    setTimeout(() => process.exit(0), 3000).unref();
  };
  process.on('SIGTERM', shutdown);
  process.on('SIGINT', shutdown);

  return { server, cfg };
}

module.exports = { readConfig, createApp, authenticate, resolveStatic, buildRoutes, main };

if (require.main === module) {
  main().catch((e) => {
    console.error('사용량 대시보드 기동 실패:', (e && e.stack) || e);
    process.exitCode = 1;
  });
}
