#!/usr/bin/env node
/*
 * ── 실물 검증 ────────────────────────────────────────────────────────────
 *
 * 게이트가 초록불이라고 화면이 뜨는 것은 아니다. `npm run build` 도 `vitest` 도 **브라우저에서
 * 실제로 그려지는가**는 답하지 못한다 — 이 앱은 Go 바이너리가 정적 자산과 `/api` 를 같은 오리진에
 * 서빙하고 CSP(script-src 'self') 아래에서 도는데, 그 배선은 빌드에도 jsdom 에도 없다.
 *
 * 이 통합의 실패 모드는 정확히 **"서버는 200 을 주는데 화면이 에러 없이 빈 채로 뜨는 것"** 이다
 * (청크 하나가 빠지거나 CSP 로 스크립트가 죽는 경우). curl 은 그것을 못 잡는다. 그래서 여기서는
 * 진짜 크로미움으로 열고, 탭마다 **빈 껍데기인지**를 따로 판별하고, 비-`/api` 응답의 4xx/5xx 를
 * 하나라도 잡으면 실패시킨다.
 *
 * ⚠ 이 스크립트가 **화면보다 낡으면 아무것도 지키지 못한다.** 2026-08-07 판(토큰 게이트·탭 2개)을
 *   재던 버전이 08-09 컷오버 때 갱신되지 않아, 그 사이 화면 변경은 어떤 실물 게이트도 통과하지
 *   않았다. 그래서 아래 단정은 **화면 문구가 아니라 구조와 API 응답에 건다**:
 *     · 탭은 `#shelltab-<id>`(role=tab)의 **집합**으로 잰다 — 빠져도, 늘어나도 빨간불이다.
 *     · 사용자 목록·단가 미등록 모델은 **화면이 실제로 부른 API 응답**에서 얻어 화면과 대조한다.
 *       (시드 사용자 이름을 박아 두면 실데이터 서버에 물렸을 때 거짓 실패가 난다.)
 *     · 데이터가 없어 잴 수 없는 항목은 **건너뛴다고 밝힌다.** 조용히 통과시키지 않는다.
 *
 * 실행:
 *   npm run verify:live                     # 서버를 직접 띄운다(빈 데이터 + 계정 2개 + 시드)
 *   VERIFY_BASE=http://host:4291 \          # 이미 떠 있는 서버에 붙는다(실데이터 검증)
 *   VERIFY_USER=u VERIFY_PASS=p \
 *   [VERIFY_MEMBER_USER=m VERIFY_MEMBER_PASS=p] npm run verify:live
 *
 * 환경:
 *   VERIFY_LIBS   크로미움 공유 라이브러리 디렉터리(기본 /tmp/pwlibs/root/usr/lib/x86_64-linux-gnu).
 *                 sudo 없이 푼 libnspr4·libnss3·libasound2 가 있으면 LD_LIBRARY_PATH 에 얹는다.
 */
import { spawn, spawnSync } from 'node:child_process';
import { existsSync } from 'node:fs';
import { mkdir, mkdtemp, rm } from 'node:fs/promises';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
/*
 * 시드는 **계약 픽스처를 그대로** 쓴다(contract/fixtures.mjs). 여기서 따로 만들면 두 벌이 되고,
 * 그 순간부터 실물 검증과 골든이 서로 다른 세상을 재게 된다. 읽기만 한다 — 고치지 않는다.
 */
import { SEED, REPLAY, IDENTITY } from '../../contract/fixtures.mjs';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const WEB = path.resolve(HERE, '..');
const REPO = path.resolve(WEB, '..');
const SHOTS = path.join(WEB, '.verify');
const BIN = path.join(REPO, 'go', 'usage-server');

/* 로그인 전에는 못 여는 탭이 있다. 이 집합이 화면과 어긋나면(빠지든 늘어나든) 빨간불이다. */
const ADMIN_TABS = ['overview', 'usage', 'usageobs', 'onboarding', 'architecture'];
const ADMIN_ONLY_TABS = ['onboarding', 'architecture'];
const MEMBER_TABS = ADMIN_TABS.filter((t) => !ADMIN_ONLY_TABS.includes(t));

/*
 * 빈 껍데기 판별 기준. 실측(시드 8세션)에서 가장 얇은 탭이 요소 48개 · 글자 530자 · 높이 462px
 * 였고, 스크립트가 죽어 빈 채로 뜬 화면은 요소 0~3개 · 글자 0자다. 그 사이에 선을 긋는다.
 */
const MIN_ELEMENTS = 25;
const MIN_TEXT = 150;
const MIN_HEIGHT = 200;

/* ── 결과 집계 ────────────────────────────────────────────────────────── */
const results = [];
function check(name, ok, detail = '') {
  results.push({ name, ok, skipped: false, detail });
  console.log(`${ok ? '  ✓' : '  ✗'} ${name}${detail ? ` — ${detail}` : ''}`);
}
/*
 * 잴 근거가 없으면 **건너뛴다고 말한다.** 조용히 통과시키면 "재고 있다"는 착각만 남는다
 * (낡은 판이 `/단가/` 로 매칭해 데이터가 없어도 초록이던 자리가 정확히 그것이었다).
 */
function skip(name, why) {
  results.push({ name, ok: true, skipped: true, detail: why });
  console.log(`  ⃝ ${name} — 건너뜀: ${why}`);
}
const head = (t) => console.log(`\n── ${t} ${'─'.repeat(Math.max(0, 56 - [...t].length))}`);

/* ── 서버 기동 ────────────────────────────────────────────────────────── */
async function freePort() {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.on('error', reject);
    srv.listen(0, '127.0.0.1', () => {
      const { port } = srv.address();
      srv.close(() => resolve(port));
    });
  });
}

async function waitFor(url, ms = 15000) {
  const deadline = Date.now() + ms;
  for (;;) {
    try {
      const r = await fetch(url);
      if (r.ok) return;
    } catch { /* 아직 안 떴다 */ }
    if (Date.now() > deadline) throw new Error(`기동 시간 초과: ${url}`);
    await new Promise((r) => setTimeout(r, 120));
  }
}

/*
 * 시드 날짜를 **오늘 기준으로 당긴다.** 계약 픽스처의 날짜는 골든이 매일 달라지지 않도록 절대값
 * 고정(2026-07-28~08-05)인데, 화면은 "최근 N일" 창으로 조회한다. 그대로 넣으면 몇 달 뒤 이 검증은
 * 데이터가 창 밖으로 밀려나 **화면이 비었다는 이유로** 빨간불이 된다 — 화면 결함이 아닌데도.
 * 일(day) 단위로만 밀어 상대 간격과 시각(hour) 버킷 구조는 그대로 둔다.
 */
function reDate(reports) {
  const stamps = [];
  for (const rep of reports) for (const s of rep.sessions) stamps.push(Date.parse(s.startedAt));
  const target = Date.now() - 24 * 3600 * 1000;
  const offsetDays = Math.floor((target - Math.max(...stamps)) / 86400000);
  if (offsetDays <= 0) return reports;
  const shift = (ms) => new Date(ms + offsetDays * 86400000);
  return reports.map((rep) => ({
    ...rep,
    sessions: rep.sessions.map((s) => ({
      ...s,
      startedAt: shift(Date.parse(s.startedAt)).toISOString(),
      ...(s.series
        ? { series: s.series.map((b) => ({ ...b, hour: shift(Date.parse(`${b.hour}:00:00.000Z`)).toISOString().slice(0, 13) })) }
        : {}),
    })),
  }));
}

async function seedServer(base, { intake, admin }) {
  const post = async (payload) => {
    const r = await fetch(`${base}/api/usage`, {
      // 보고는 인테이크 토큰으로 넣는다 — 수집기가 실제로 쓰는 자격이다.
      method: 'POST',
      headers: { Authorization: `Bearer ${intake}`, 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
    if (!r.ok) throw new Error(`인테이크 실패 ${r.status}: ${(await r.text()).slice(0, 200)}`);
  };
  const seed = reDate(SEED);
  for (const p of seed) await post(p);
  // 같은 페이로드를 한 번 더 — 멱등이 누적으로 퇴화하면 값이 두 배가 되어 화면에서 드러난다.
  for (const p of reDate(REPLAY)) await post(p);
  // 귀속 교정은 조회 자격(admin)이 필요하다 — 인테이크 토큰은 보고만 할 수 있다(403).
  const r = await fetch(`${base}/api/usage/identity`, {
    method: 'PUT',
    headers: { Authorization: `Bearer ${admin}`, 'Content-Type': 'application/json' },
    body: JSON.stringify(IDENTITY),
  });
  if (!r.ok) throw new Error(`귀속 교정 실패 ${r.status}: ${(await r.text()).slice(0, 200)}`);
}

async function startOwnServer() {
  if (!existsSync(BIN)) {
    throw new Error(`서버 바이너리가 없다: ${BIN} — scripts/build.sh 로 먼저 만든다.`);
  }
  const port = await freePort();
  const dataDir = await mkdtemp(path.join(os.tmpdir(), 'verify-live-'));
  const admin = { user: 'verify-admin', pass: 'Verify-Passw0rd!1' };
  const member = { user: 'verify-member', pass: 'Verify-Passw0rd!2' };
  const token = 'verify-live-admin-token-0123456789';
  const intake = 'verify-live-intake-token-9876543210';
  const env = {
    ...process.env,
    USAGE_DATA_DIR: dataDir,
    USAGE_DB_MODE: 'local',
    USAGE_HOST: '127.0.0.1',
    USAGE_PORT: String(port),
    USAGE_ADMIN_TOKEN: token,
    USAGE_INTAKE_TOKEN: intake,
  };

  /*
   * 계정은 **서버를 띄우기 전에** CLI 로 만든다. member 계정이 있어야 "관리자 전용 탭이 닫히는가"를
   * 실제로 밟을 수 있다 — 없으면 그 항목은 영원히 확인되지 않은 채 남는다(직전 판이 그랬다).
   */
  for (const u of [{ ...admin, role: 'admin' }, { ...member, role: 'member' }]) {
    const r = spawnSync(BIN, ['user', 'add', '-username', u.user, '-role', u.role, '-password', u.pass], { env, encoding: 'utf8' });
    if (r.status !== 0) throw new Error(`계정 생성 실패(${u.role}): ${r.stderr || r.stdout}`);
  }

  const child = spawn(BIN, [], { env, cwd: REPO, stdio: ['ignore', 'pipe', 'pipe'] });
  const log = [];
  child.stdout.on('data', (b) => log.push(String(b)));
  child.stderr.on('data', (b) => log.push(String(b)));
  const base = `http://127.0.0.1:${port}`;
  const stop = async () => {
    child.kill('SIGTERM');
    await new Promise((r) => setTimeout(r, 300));
    child.kill('SIGKILL');
    await rm(dataDir, { recursive: true, force: true });
  };
  try {
    await waitFor(`${base}/healthz`);
    await seedServer(base, { intake, admin: token });
  } catch (e) {
    // 여기서 접지 않으면 서버 프로세스와 임시 DB 가 남는다.
    await stop();
    throw new Error(`${e.message}\n${log.join('')}`);
  }
  return { base, admin, member, seeded: true, stop };
}

function attachExternal() {
  const base = process.env.VERIFY_BASE.replace(/\/$/, '');
  const user = process.env.VERIFY_USER;
  const pass = process.env.VERIFY_PASS;
  if (!user || !pass) throw new Error('VERIFY_BASE 를 쓰려면 VERIFY_USER·VERIFY_PASS 도 필요하다.');
  const mu = process.env.VERIFY_MEMBER_USER;
  const mp = process.env.VERIFY_MEMBER_PASS;
  return {
    base,
    admin: { user, pass },
    member: mu && mp ? { user: mu, pass: mp } : null,
    seeded: false,
    stop: async () => {},
  };
}

/* ── 브라우저 계측 ────────────────────────────────────────────────────── */
/*
 * CSP 위반과 그 밖의 콘솔 오류를 **갈라서** 센다. 뭉치면 "콘솔에 뭔가 있다"까지만 알게 되는데,
 * CSP 위반은 화면이 통째로 죽는 사고이고 나머지는 그렇지 않다 — 대응이 다르므로 따로 센다.
 * 일부러 낸 401(로그인 전·틀린 비밀번호)은 브라우저가 오류로 남기지만 의도한 경로라 세지 않는다.
 */
const CSP_RE = /Content Security Policy|Refused to (execute|load|apply)/i;
const EXPECTED_RE = /Failed to load resource.*40[13]/i;

function instrument(page, base, sink) {
  page.on('console', (m) => {
    if (m.type() !== 'error') return;
    const t = m.text();
    if (CSP_RE.test(t)) { sink.csp.push(t); return; }
    if (EXPECTED_RE.test(t)) return;
    sink.console.push(t);
  });
  page.on('pageerror', (e) => sink.console.push(`pageerror: ${e.message}`));
  page.on('response', (r) => {
    const u = r.url();
    // 누락된 청크는 화면에 아무 에러도 안 내고 404 하나로만 드러난다.
    if (u.startsWith(base) && !u.includes('/api/') && r.status() >= 400) sink.static.push(`${r.status()} ${u}`);
  });
}

/* 화면이 실제로 부른 응답을 가로챈다 — 우리가 따로 부르면 화면과 다른 것을 잴 수 있다. */
function captureApi(page, store) {
  page.on('response', async (r) => {
    const u = r.url();
    if (!u.includes('/api/usage/')) return;
    if (r.status() !== 200) return;
    const key = u.includes('/api/usage/summary') ? 'summary' : u.includes('/api/usage/sessions?') ? 'sessions' : null;
    if (!key) return;
    try { store[key] = await r.json(); } catch { /* 본문이 아니면 무시 */ }
  });
}

async function login(page, base, user, pass) {
  await page.goto(base, { waitUntil: 'networkidle' });
  await page.waitForSelector('input[type=password]', { timeout: 15000 });
  await page.fill('input[name=username]', user);
  await page.fill('input[type=password]', pass);
  await page.click('button[type=submit]');
}

const tabIds = (page) => page.evaluate(() =>
  [...document.querySelectorAll('[role=tab]')].map((b) => b.id.replace(/^shelltab-/, '')).sort());

/* 탭 패널이 **실제로 그려졌는지**. "200 인데 빈 화면"을 여기서 가른다. */
const panelStats = (page) => page.evaluate(() => {
  const p = document.getElementById('tabpanel');
  const selected = document.querySelector('[role=tab][aria-selected=true]')?.id.replace(/^shelltab-/, '') ?? null;
  /*
   * 패널이 아예 없을 때도 **같은 shape 를 돌려준다.** 여기서 필드를 빠뜨리면 뒷 단계가 undefined 를
   * 만져 예외로 죽고, 그러면 남은 항목이 통째로 안 돌아 "무엇이 깨졌는지"가 아니라 "스크립트가
   * 죽었다"만 남는다 — 결함을 심어 본 뒤에야 드러난 자리다.
   */
  if (!p) return { missing: true, els: 0, textLen: 0, height: 0, graphics: 0, stuck: false, error: false, selected, text: '' };
  const r = p.getBoundingClientRect();
  const text = (p.innerText || '').replace(/\s+/g, ' ').trim();
  return {
    missing: false,
    els: p.querySelectorAll('*').length,
    textLen: text.length,
    height: Math.round(r.height),
    graphics: [...p.querySelectorAll('canvas, svg')].filter((g) => {
      const b = g.getBoundingClientRect();
      return b.width > 40 && b.height > 40;
    }).length,
    stuck: /^(불러오는 중|로딩)/.test(text),
    error: /(불러오지 못했|오류가 발생|요청이 실패)/.test(text),
    selected,
    text,
  };
});

function substantive(s) {
  return !s.missing && !s.stuck && !s.error
    && s.els >= MIN_ELEMENTS && s.textLen >= MIN_TEXT && s.height >= MIN_HEIGHT;
}

/* ── 본 검증 ──────────────────────────────────────────────────────────── */
const libs = process.env.VERIFY_LIBS || '/tmp/pwlibs/root/usr/lib/x86_64-linux-gnu';
if (existsSync(libs) && !(process.env.LD_LIBRARY_PATH || '').includes(libs)) {
  // sudo 없이 푼 크로미움 의존 라이브러리가 있으면 얹는다(없으면 아무 일도 하지 않는다).
  process.env.LD_LIBRARY_PATH = process.env.LD_LIBRARY_PATH ? `${process.env.LD_LIBRARY_PATH}:${libs}` : libs;
}

const server = process.env.VERIFY_BASE ? attachExternal() : await startOwnServer();
const BASE = server.base;
console.log(`\n검증 대상: ${BASE}${server.seeded ? ' (이 스크립트가 띄운 서버 · 계약 시드)' : ' (외부 서버)'}`);
await mkdir(SHOTS, { recursive: true });

const browser = await chromium.launch();
const ctx = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
const page = await ctx.newPage();
const sink = { csp: [], console: [], static: [] };
const api = {};
instrument(page, BASE, sink);
captureApi(page, api);

try {
  head('① 로그인 화면 (무인증)');
  await page.goto(BASE, { waitUntil: 'networkidle' });
  await page.waitForSelector('form', { timeout: 15000 });
  check('로그인 폼이 그려진다 = 하이드레이션이 살아 있다',
    await page.locator('input[name=username]').count() === 1 && await page.locator('input[type=password]').count() === 1);
  const theme = await page.getAttribute('html', 'data-theme');
  check('테마가 페인트 전에 적용된다', !!theme, `data-theme=${theme}`);
  check('로그인 전에는 셸(탭)이 없다', (await tabIds(page)).length === 0);
  await page.screenshot({ path: path.join(SHOTS, '01-login.png') });

  head('② 틀린 비밀번호 → 이유가 남는다');
  await page.fill('input[name=username]', server.admin.user);
  await page.fill('input[type=password]', `${server.admin.pass}-wrong`);
  await page.click('button[type=submit]');
  await page.waitForTimeout(1500);
  const bad = (await page.locator('body').innerText()).replace(/\s+/g, ' ');
  check('틀린 비밀번호로는 들어가지 못한다', await page.locator('input[type=password]').count() > 0);
  check('왜 안 되는지 화면에 남는다', /(틀|올바르|실패|확인|다시|잘못)/.test(bad),
    bad.match(/[^.]*(틀|올바르|실패|잘못)[^.]*/)?.[0]?.trim().slice(0, 60));
  check('틀린 비밀번호에는 세션 쿠키를 주지 않는다',
    !(await ctx.cookies()).some((c) => c.name === 'usage_sess' && c.value));

  head('③ 올바른 로그인 · 세션 쿠키');
  await login(page, BASE, server.admin.user, server.admin.pass);
  await page.waitForSelector('[role=tab]', { timeout: 20000 });
  const sess = (await ctx.cookies()).find((c) => c.name === 'usage_sess');
  check('세션 쿠키(usage_sess)가 발급된다', !!sess);
  check('세션 쿠키가 HttpOnly 다 — 스크립트가 읽지 못한다', sess?.httpOnly === true, `httpOnly=${sess?.httpOnly}`);
  check('SameSite 가 걸려 있다', sess?.sameSite === 'Strict' || sess?.sameSite === 'Lax', `SameSite=${sess?.sameSite}`);
  check('http 로컬에서는 Secure 가 붙지 않는다(기동이 성립해야 한다)', sess?.secure === false,
    BASE.startsWith('https') ? 'https 대상이면 이 항목은 반대여야 한다' : '');
  const adminTabs = await tabIds(page);
  check('관리자에게 탭 5개가 전부 보인다', JSON.stringify(adminTabs) === JSON.stringify([...ADMIN_TABS].sort()),
    `[${adminTabs.join(' ')}]`);

  head('④ 탭 5개 — 딥링크로 열고 각각 실제로 그려지는지');
  for (const id of ADMIN_TABS) {
    await page.goto(`${BASE}/#/${id}`, { waitUntil: 'networkidle' });
    await page.waitForTimeout(2000);
    const s = await panelStats(page);
    check(`#/${id} 이 열리고 그 탭이 선택된다`, s.selected === id, `선택된 탭=${s.selected}`);
    check(`#/${id} 이 빈 껍데기가 아니다`, substantive(s),
      s.missing ? '#tabpanel 자체가 없다' : `요소 ${s.els} · ${s.textLen}자 · ${s.height}px${s.stuck ? ' · 로딩에서 멈춤' : ''}${s.error ? ' · 오류 문구' : ''}`);
    await page.screenshot({ path: path.join(SHOTS, `02-tab-${id}.png`), fullPage: true });
  }

  head('⑤ 화면이 API 응답과 같은 것을 말하는가');
  await page.goto(`${BASE}/#/usage`, { waitUntil: 'networkidle' });
  await page.waitForTimeout(2500);
  const usage = await panelStats(page);
  /*
   * "응답이 비었다"와 "화면이 그 API 를 아예 부르지 않았다"는 다른 사건이다. 뒤엣것은 **결함**이라
   * 건너뛰면 안 된다 — 화면이 죽어 아무 요청도 못 보내는 상태가 조용히 통과해 버린다.
   */
  const byUser = api.summary?.byUser ?? [];
  if (!api.summary) {
    check('화면이 summary API 를 부른다', false, '/api/usage/summary 응답을 한 번도 받지 못했다');
  } else if (!byUser.length) {
    const why = '조회 창에 사용자 집계가 없다(summary.byUser 가 비었다)';
    if (server.seeded) check('시드를 넣은 서버인데 사용자 집계가 비어 있다', false, why);
    else skip('사용자별 표가 API 와 일치한다', why);
  } else {
    /* 이름을 박지 않는다 — 화면이 방금 부른 응답에서 얻어 대조한다(실데이터 서버에서도 성립). */
    const names = byUser.map((u) => u.username).filter((n) => n && n !== '(미상)').slice(0, 5);
    const missing = names.filter((n) => !usage.text.includes(n));
    check('사용자별 표의 이름이 API 응답과 일치한다', missing.length === 0,
      missing.length ? `화면에 없는 이름: ${missing.join(', ')}` : `${names.length}명 대조: ${names.join(', ')}`);
  }
  check('근사를 정확한 값으로 위장하지 않는다 — 모델별 표에 「근거」 열이 있다',
    await page.locator('#tabpanel th', { hasText: '근거' }).count() > 0);

  await page.goto(`${BASE}/#/usageobs`, { waitUntil: 'networkidle' });
  await page.waitForTimeout(2500);
  const obs = await panelStats(page);
  const unpriced = api.sessions?.unpriced ?? [];
  if (!api.sessions) {
    check('화면이 sessions API 를 부른다', false, '/api/usage/sessions 응답을 한 번도 받지 못했다');
  } else if (!unpriced.length) {
    /*
     * 예전 판은 `/단가/` 로 매칭했다. 그 낱말은 비용 안내문에도 있어 **단가 미등록 모델이 하나도
     * 없어도 초록**이었다 — 아무것도 지키지 못하는 단정이다. 근거가 없으면 건너뛴다고 말한다.
     */
    skip('단가 미등록 모델이 화면에 드러난다', '이 창에 unpriced 모델이 없다(API 가 빈 배열)');
  } else {
    const hidden = unpriced.filter((m) => !obs.text.includes(m));
    check('단가 미등록 모델 이름이 화면에 그대로 드러난다(조용한 $0 금지)', hidden.length === 0,
      hidden.length ? `화면에 없는 모델: ${hidden.join(', ')}` : `${unpriced.length}개 대조: ${unpriced.join(', ')}`);
  }

  head('⑥ 관리자 전용 탭이 member 에게 닫히는가');
  if (!server.member) {
    skip('member 에게 관리자 전용 탭이 보이지 않는다', 'VERIFY_MEMBER_USER·VERIFY_MEMBER_PASS 가 없다');
  } else {
    const mctx = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
    const mpage = await mctx.newPage();
    instrument(mpage, BASE, sink);
    await login(mpage, BASE, server.member.user, server.member.pass);
    await mpage.waitForSelector('[role=tab]', { timeout: 20000 });
    const mTabs = await tabIds(mpage);
    check('member 에게는 관리자 전용 탭이 보이지 않는다',
      JSON.stringify(mTabs) === JSON.stringify([...MEMBER_TABS].sort()), `[${mTabs.join(' ')}]`);
    for (const id of ADMIN_ONLY_TABS) {
      // 버튼만 감추고 딥링크로는 열리면 가린 것이지 닫은 것이 아니다.
      await mpage.goto(`${BASE}/#/${id}`, { waitUntil: 'networkidle' });
      await mpage.waitForTimeout(1500);
      const s = await panelStats(mpage);
      check(`member 가 #/${id} 로 직접 들어가도 그 탭이 열리지 않는다`, s.selected !== id, `선택된 탭=${s.selected}`);
    }
    await mpage.screenshot({ path: path.join(SHOTS, '03-member.png'), fullPage: true });
    await mctx.close();
  }

  head('⑦ 로그아웃');
  await page.goto(`${BASE}/#/overview`, { waitUntil: 'networkidle' });
  await page.waitForTimeout(1200);
  const signout = page.locator('#signout');
  if (await signout.count()) {
    await signout.first().click();
    await page.waitForTimeout(1800);
    check('로그아웃하면 로그인 화면으로 돌아온다',
      await page.locator('input[type=password]').count() > 0 && (await tabIds(page)).length === 0);
    const left = (await ctx.cookies()).find((c) => c.name === 'usage_sess');
    check('로그아웃하면 세션 쿠키가 남지 않는다', !left || !left.value, left ? `value 길이 ${left.value.length}` : '');
  } else {
    check('로그아웃 버튼(#signout)이 있다', false);
  }

  head('⑧ CSP · 콘솔 · 정적 응답');
  check("CSP(script-src 'self') 위반이 없다", sink.csp.length === 0, `${sink.csp.length}건`);
  sink.csp.slice(0, 3).forEach((v) => console.log(`      ${v}`));
  check('콘솔 오류가 없다(의도된 401 제외)', sink.console.length === 0, `${sink.console.length}건`);
  sink.console.slice(0, 5).forEach((v) => console.log(`      ${v}`));
  check('정적 요청에 4xx/5xx 가 없다 = 누락된 청크가 없다', sink.static.length === 0, `${sink.static.length}건`);
  sink.static.slice(0, 5).forEach((v) => console.log(`      ${v}`));

  head('⑨ 좁은 화면(390px)');
  await login(page, BASE, server.admin.user, server.admin.pass);
  await page.waitForSelector('[role=tab]', { timeout: 20000 });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(`${BASE}/#/usage`, { waitUntil: 'networkidle' });
  await page.waitForTimeout(1500);
  const m = await page.evaluate(() => ({
    scrollW: document.documentElement.scrollWidth,
    clientW: document.documentElement.clientWidth,
  }));
  check('390px 에서 본문이 가로로 밀리지 않는다', m.scrollW <= m.clientW + 1,
    `scrollW=${m.scrollW} clientW=${m.clientW}`);
  await page.screenshot({ path: path.join(SHOTS, '04-mobile.png'), fullPage: true });
} catch (e) {
  check('검증 스크립트가 끝까지 돌았다', false, String(e && e.message));
  await page.screenshot({ path: path.join(SHOTS, '99-failure.png'), fullPage: true }).catch(() => {});
} finally {
  await browser.close();
  await server.stop();
}

const failed = results.filter((r) => !r.ok);
const skipped = results.filter((r) => r.skipped);
console.log(`\n${'='.repeat(60)}`);
console.log(`검증 ${results.length}건 · 통과 ${results.length - failed.length - skipped.length} · 건너뜀 ${skipped.length} · 실패 ${failed.length}`);
console.log(`스크린샷: ${SHOTS}`);
if (skipped.length) {
  console.log('\n건너뛴 항목(잴 근거가 없었다 — 통과가 아니다):');
  for (const s of skipped) console.log(`  ⃝ ${s.name} — ${s.detail}`);
}
if (failed.length) {
  console.log('\n실패:');
  for (const f of failed) console.log(`  ✗ ${f.name}${f.detail ? ` — ${f.detail}` : ''}`);
}
process.exit(failed.length ? 1 : 0);
