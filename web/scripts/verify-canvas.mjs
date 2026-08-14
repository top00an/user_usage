#!/usr/bin/env node
/*
 * ── 배치 편집 실물 검증 ──────────────────────────────────────────────────
 *
 * 대시보드 캔버스에서 패널을 **진짜 크로미움으로 끌어 보고**, 놓은 자리에 있는지 잰다.
 *
 * 왜 vitest 로는 안 되는가. jsdom 은 레이아웃을 하지 않는다 — 요소가 화면 어디에 그려졌는지
 * 자체가 없어서, 단위 테스트는 "우리가 계산한 x·y"만 확인할 수 있다. 그런데 사람이 겪는 증상은
 * **"옮기면 위치가 혼자 바뀐다"** 이고, 그건 계산과 CSS Grid 와 포인터 이벤트가 만나는 자리에서만
 * 드러난다. 실제로 2026-08-14 의 자동 compact 사고가 그랬다: 단위 테스트는 전부 초록이었고,
 * 그 초록이 잘못된 계약(끌어올리기)을 **지키고** 있었다.
 *
 * verify-live.mjs 와 다른 점은 서빙 경로다. 저쪽은 Go 바이너리에 go:embed 된 화면을 재므로
 * 프론트를 고친 사람은 Go 툴체인으로 다시 빌드해야 한다. 여기서는 방금 빌드한 web/out 을
 * preview.mjs 로 띄우고 /api 만 기존 바이너리로 프록시한다 — **화면만 고친 사람이 Go 없이도
 * 실물을 밟을 수 있게.** (같은 오리진이라 로그인 쿠키도 실제와 같다.)
 *
 * 실행:
 *   npm run build && npm run verify:canvas
 *
 * 환경:
 *   VERIFY_BIN    /api 를 줄 서버 바이너리(기본 ../go/usage-server)
 *   VERIFY_LIBS   크로미움 공유 라이브러리 디렉터리(기본 /tmp/pwlibs/root/usr/lib/x86_64-linux-gnu)
 *   SHOT_DIR      스크린샷 위치(기본 web/.verify-canvas)
 */
import { spawn, spawnSync } from 'node:child_process';
import { existsSync } from 'node:fs';
import { mkdir, mkdtemp, rm } from 'node:fs/promises';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const WEB = path.resolve(HERE, '..');
const REPO = path.resolve(WEB, '..');
const BIN = process.env.VERIFY_BIN || path.join(REPO, 'go', 'usage-server');
const SHOT = process.env.SHOT_DIR || path.join(WEB, '.verify-canvas');

/*
 * 크로미움이 필요로 하는 libnspr4·libnss3·libasound2 를 sudo 없이 푼 디렉터리가 있으면 얹는다.
 * verify-live.mjs 와 **같은 규약**이다 — 두 스크립트가 다른 환경을 요구하면 하나는 반드시 안 돈다.
 */
const libs = process.env.VERIFY_LIBS || '/tmp/pwlibs/root/usr/lib/x86_64-linux-gnu';
if (existsSync(libs) && !(process.env.LD_LIBRARY_PATH || '').includes(libs)) {
  process.env.LD_LIBRARY_PATH = process.env.LD_LIBRARY_PATH ? `${process.env.LD_LIBRARY_PATH}:${libs}` : libs;
}
const { chromium } = await import('playwright');

/** 한 행의 피치(px). lib/dashLayout.ts 의 ROW_H + GRID_GAP 과 같아야 한다. */
const ROW_STEP = 56 + 12;

const freePort = () => new Promise((res, rej) => {
  const s = net.createServer();
  s.on('error', rej);
  s.listen(0, '127.0.0.1', () => { const { port } = s.address(); s.close(() => res(port)); });
});

async function waitFor(url, ms = 20000) {
  const end = Date.now() + ms;
  for (;;) {
    try { const r = await fetch(url); if (r.ok || r.status === 401 || r.status === 404) return; } catch { /* 아직 안 떴다 */ }
    if (Date.now() > end) throw new Error(`기동 시간 초과: ${url}`);
    await new Promise((r) => setTimeout(r, 150));
  }
}

const results = [];
const check = (name, ok, detail = '') => {
  results.push({ name, ok, detail });
  console.log(`  ${ok ? '✓' : '✗'} ${name}${detail ? ` — ${detail}` : ''}`);
};

/** 패널이 **실제로 그려진** 격자 좌표. 인라인 스타일이 아니라 계산된 값을 읽는다. */
const boxOf = (page, pid) => page.$eval(`.dc-cell[data-pid="${pid}"]`, (el) => {
  const s = getComputedStyle(el);
  return { row: s.gridRow, col: s.gridColumn };
});

/** 지금 캔버스의 모든 패널 자리 — "한 장을 옮겼는데 몇 장이 움직였나"를 세는 근거다. */
const snapshot = (page) => page.$$eval('.dc-cell[data-pid]', (els) => Object.fromEntries(
  els.map((e) => [e.dataset.pid, `${getComputedStyle(e).gridRow}|${getComputedStyle(e).gridColumn}`]),
));

/** 패널을 세로로 n 행만큼 끈다(4px 임계값을 넘기려 중간 지점을 거친다). */
async function dragRows(page, pid, rows) {
  const el = await page.$(`.dc-cell[data-pid="${pid}"]`);
  const b = await el.boundingBox();
  const x = b.x + b.width / 2;
  const y = b.y + 12;   // 제목 줄을 잡는다 — 안쪽 버튼에서 시작하면 드래그가 아니다.
  await page.mouse.move(x, y);
  await page.mouse.down();
  await page.mouse.move(x, y + (ROW_STEP * rows) / 2, { steps: 6 });
  await page.mouse.move(x, y + ROW_STEP * rows, { steps: 12 });
  await page.mouse.up();
  await page.waitForTimeout(300);
}

async function main() {
  if (!existsSync(BIN)) throw new Error(`서버 바이너리가 없다: ${BIN} — bash scripts/build.sh 로 먼저 만든다.`);
  if (!existsSync(path.join(WEB, 'out', 'index.html'))) throw new Error('web/out 이 없다 — npm run build 를 먼저 돌린다.');
  await mkdir(SHOT, { recursive: true });

  const apiPort = await freePort();
  const webPort = await freePort();
  const dataDir = await mkdtemp(path.join(os.tmpdir(), 'verify-canvas-'));
  const user = { u: 'canvas-admin', p: 'Canvas-Passw0rd!1' };
  const env = {
    ...process.env,
    USAGE_DATA_DIR: dataDir,
    USAGE_DB_MODE: 'local',
    USAGE_HOST: '127.0.0.1',
    USAGE_PORT: String(apiPort),
    USAGE_ADMIN_TOKEN: 'verify-canvas-admin-token-0123456789',
    USAGE_INTAKE_TOKEN: 'verify-canvas-intake-token-987654321',
  };

  // 계정은 서버를 띄우기 전에 CLI 로 만든다(verify-live.mjs 와 같은 순서).
  const add = spawnSync(BIN, ['user', 'add', '-username', user.u, '-role', 'admin', '-password', user.p], { env, encoding: 'utf8' });
  if (add.status !== 0) throw new Error(`계정 생성 실패: ${add.stderr || add.stdout}`);

  const api = spawn(BIN, [], { env, cwd: REPO, stdio: ['ignore', 'pipe', 'pipe'] });
  const web = spawn('node', [path.join(WEB, 'scripts', 'preview.mjs'), '--port', String(webPort), '--api', `http://127.0.0.1:${apiPort}`], {
    cwd: WEB, stdio: ['ignore', 'pipe', 'pipe'],
  });
  const logs = [];
  for (const c of [api, web]) {
    c.stdout.on('data', (b) => logs.push(String(b)));
    c.stderr.on('data', (b) => logs.push(String(b)));
  }

  const base = `http://127.0.0.1:${webPort}`;
  let browser;
  const stop = async () => {
    if (browser) await browser.close().catch(() => {});
    for (const c of [api, web]) c.kill('SIGTERM');
    await new Promise((r) => setTimeout(r, 300));
    for (const c of [api, web]) c.kill('SIGKILL');
    await rm(dataDir, { recursive: true, force: true });
  };

  try {
    await waitFor(`http://127.0.0.1:${apiPort}/healthz`);
    await waitFor(base);

    browser = await chromium.launch();
    const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
    /*
     * 로그인 **전**의 401 은 정상이다 — 셸이 세션을 물어보는 호출이고 그 실패가 곧 로그인 화면이다.
     * 그래서 로그인 뒤부터만 센다.
     */
    let watching = false;
    const bad = [];
    page.on('console', (m) => { if (watching && m.type() === 'error') bad.push(m.text()); });
    page.on('pageerror', (e) => { if (watching) bad.push(String(e)); });
    page.on('response', (r) => { if (watching && r.status() >= 400) bad.push(`${r.status()} ${r.url()}`); });

    await page.goto(base, { waitUntil: 'domcontentloaded' });
    await page.fill('input[type="text"]', user.u);
    await page.fill('input[type="password"]', user.p);
    await page.click('button[type="submit"]');
    await page.waitForSelector('.dashcanvas', { timeout: 20000 });
    check('로그인 후 대시보드 캔버스가 뜬다', true);
    watching = true;

    await page.click('button:has-text("배치 편집")');
    await page.waitForSelector('.dashcanvas.editing');

    const target = 'live-cost';
    const other = 'live-tokens';

    /* ── ① 아래 빈 곳에 놓으면 그 자리에 남는가 (자동 끌어올리기 없음) ── */
    const before = await boxOf(page, target);
    const otherBefore = await boxOf(page, other);
    await dragRows(page, target, 5);
    const dropped = await boxOf(page, target);
    const rowOf = (b) => Number(b.row.split('/')[0].trim());
    check('아래로 끌어 놓으면 그 자리에 남는다 (위로 안 튄다)',
      rowOf(dropped) === rowOf(before) + 5, `${rowOf(before)}행 → ${rowOf(dropped)}행`);
    check('겹치지 않은 옆 패널은 안 움직인다',
      JSON.stringify(await boxOf(page, other)) === JSON.stringify(otherBefore), `${other}: ${otherBefore.row}`);
    await page.screenshot({ path: path.join(SHOT, '1-dropped.png') });

    /* ── ② 한 장을 옮겼을 때 움직이는 것이 그 한 장뿐인가 ─────────────── */
    const snapBefore = await snapshot(page);
    await dragRows(page, target, 2);
    const snapAfter = await snapshot(page);
    const moved = Object.keys(snapAfter).filter((k) => snapAfter[k] !== snapBefore[k]);
    check('한 장을 옮기면 자리가 바뀌는 패널도 그 한 장뿐이다',
      moved.length === 1 && moved[0] === target, `바뀐 패널: ${moved.join(', ') || '없음'}`);

    /* ── ②-b 남의 자리에 통째로 겹쳐 놓아도 그 패널이 안 움직이는가 ───── */
    const overlapSnap = await snapshot(page);
    const otherBox = await page.$eval(`.dc-cell[data-pid="${other}"]`, (el) => {
      const r = el.getBoundingClientRect();
      return { x: r.x + r.width / 2, y: r.y + 14 };
    });
    const me = await page.$eval(`.dc-cell[data-pid="${target}"]`, (el) => {
      const r = el.getBoundingClientRect();
      return { x: r.x + r.width / 2, y: r.y + 14 };
    });
    await page.mouse.move(me.x, me.y);
    await page.mouse.down();
    await page.mouse.move((me.x + otherBox.x) / 2, (me.y + otherBox.y) / 2, { steps: 8 });
    await page.mouse.move(otherBox.x, otherBox.y, { steps: 10 });
    await page.mouse.up();
    await page.waitForTimeout(300);
    const afterOverlap = await snapshot(page);
    const movedByOverlap = Object.keys(afterOverlap).filter((k) => afterOverlap[k] !== overlapSnap[k]);
    check('남의 자리에 겹쳐 놓아도 그 패널은 안 밀린다 (겹침 허용)',
      !movedByOverlap.includes(other), `바뀐 패널: ${movedByOverlap.join(', ') || '없음'}`);
    check('겹친 뒤 방금 놓은 패널이 위에 있다 (다시 잡을 수 있다)',
      await page.$eval(`.dc-cell[data-pid="${target}"]`, (el) => el.style.zIndex === '2'));
    await page.screenshot({ path: path.join(SHOT, '3-overlap.png') });
    await page.keyboard.press('Control+z');   // 겹친 것은 되돌려 두고 다음 항목으로
    await page.waitForTimeout(250);

    /* ── ②-c 크기를 **줄일 때도** 미리보기가 보이는가 ─────────────────── */
    const handle = await page.$(`.dc-cell[data-pid="${target}"] .dc-handle`);
    const hb = await handle.boundingBox();
    await page.mouse.move(hb.x + hb.width / 2, hb.y + hb.height / 2);
    await page.mouse.down();
    await page.mouse.move(hb.x - 60, hb.y - 40, { steps: 8 });
    const ghost = await page.$('.dc-cell.ghost');
    const seen = ghost ? await ghost.evaluate((el) => {
      const r = el.getBoundingClientRect();
      const s = getComputedStyle(el);
      // 크기가 있고, 끌고 있는 패널(z-index 3)보다 위에 있어야 실제로 눈에 보인다.
      return { text: el.textContent, visible: r.width > 0 && r.height > 0 && Number(s.zIndex) > 3 };
    }) : null;
    await page.screenshot({ path: path.join(SHOT, '4-shrink-preview.png') });
    await page.mouse.up();
    await page.waitForTimeout(250);
    check('줄이는 중에도 미리보기가 보인다', !!seen?.visible, seen ? `배지="${seen.text}"` : 'ghost 없음');
    check('줄어드는 크기를 숫자로 말한다', /칸 × \d+행$/.test(seen?.text || ''), seen?.text || '(없음)');
    await page.keyboard.press('Control+z');
    await page.waitForTimeout(250);

    /* ── ③ Ctrl+Z ────────────────────────────────────────────────────── */
    await page.keyboard.press('Control+z');
    await page.waitForTimeout(250);
    check('Ctrl+Z 로 직전 자리로 돌아온다', (await boxOf(page, target)).row === dropped.row,
      `${dropped.row} ← ${(await boxOf(page, target)).row}`);

    /* ── ④ 빈 줄 정리는 **누를 때만** 돈다 ───────────────────────────── */
    const beforeCompact = await boxOf(page, target);
    await page.click('button:has-text("겹침·빈 줄 정리")');
    await page.waitForTimeout(250);
    check('빈 줄 정리를 누르면 그때 올라온다',
      (await boxOf(page, target)).row !== beforeCompact.row,
      `${beforeCompact.row} → ${(await boxOf(page, target)).row}`);
    await page.keyboard.press('Control+z');
    await page.waitForTimeout(250);
    check('빈 줄 정리도 되돌려진다', (await boxOf(page, target)).row === beforeCompact.row);
    await page.screenshot({ path: path.join(SHOT, '2-after-undo.png') });

    /* ── ⑤ 새로고침해도 그 자리인가(서버 저장) ───────────────────────── */
    const beforeReload = await boxOf(page, target);
    await page.waitForTimeout(900);   // 저장 디바운스(SAVE_DEBOUNCE_MS)
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.dashcanvas');
    await page.waitForTimeout(500);
    check('새로고침해도 같은 자리다 (서버에 저장됐다)',
      JSON.stringify(await boxOf(page, target)) === JSON.stringify(beforeReload), `${beforeReload.row}`);

    /* ── ⑥ 겹침의 뒤처리: 안내 · 앞뒤 유지 · 정리 ─────────────────────── */
    await page.click('button:has-text("배치 편집")');
    await page.waitForSelector('.dashcanvas.editing');
    // target 을 other 위로 통째로 겹친다.
    const dst = await page.$eval(`.dc-cell[data-pid="${other}"]`, (el) => {
      const r = el.getBoundingClientRect();
      return { x: r.x + r.width / 2, y: r.y + 14 };
    });
    const src = await page.$eval(`.dc-cell[data-pid="${target}"]`, (el) => {
      const r = el.getBoundingClientRect();
      return { x: r.x + r.width / 2, y: r.y + 14 };
    });
    await page.mouse.move(src.x, src.y);
    await page.mouse.down();
    await page.mouse.move((src.x + dst.x) / 2, (src.y + dst.y) / 2, { steps: 8 });
    await page.mouse.move(dst.x, dst.y, { steps: 10 });
    await page.mouse.up();
    await page.waitForTimeout(300);

    const notice = await page.textContent('.dc-warn').catch(() => null);
    check('겹치면 몇 장이 가려졌는지 말한다', /패널 \d+장이 겹쳐 있습니다/.test(notice || ''), notice || '(안내 없음)');

    const domOrder = () => page.$$eval('.dc-cell[data-pid]', (els) => els.map((e) => e.dataset.pid));
    const beforeOrder = await domOrder();
    check('겹친 뒤 방금 놓은 패널이 맨 뒤(=맨 위)에 그려진다',
      beforeOrder.indexOf(target) > beforeOrder.indexOf(other),
      `${other}(${beforeOrder.indexOf(other)}) < ${target}(${beforeOrder.indexOf(target)})`);

    await page.waitForTimeout(900);   // 저장 디바운스
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.dashcanvas');
    await page.waitForTimeout(500);
    const afterOrder = await domOrder();
    check('새로고침해도 앞뒤가 유지된다 (배열 순서가 저장된다)',
      afterOrder.indexOf(target) > afterOrder.indexOf(other),
      `${other}(${afterOrder.indexOf(other)}) < ${target}(${afterOrder.indexOf(target)})`);
    check('새로고침 뒤에도 겹침 안내가 남아 있다',
      /패널 \d+장이 겹쳐 있습니다/.test((await page.textContent('.dc-warn').catch(() => '')) || ''));
    await page.screenshot({ path: path.join(SHOT, '5-overlap-notice.png') });

    await page.click('button:has-text("배치 편집")');
    await page.waitForSelector('.dashcanvas.editing');

    /*
     * ⑥-b 클릭하면 앞으로.
     *
     * 지금은 두 패널이 **통째로** 겹쳐 있다. 이 상태에서 아래 카드를 클릭하는 것은 원리적으로
     * 불가능하다 — 클릭은 위 카드에 맞는다. 그것부터 사실로 못 박고(그래서 Enter 경로가 필요하다),
     * 그다음 한 칸 어긋난 **부분 겹침**에서 클릭이 실제로 꺼내 오는지 잰다.
     */
    const fullyCovered = await domOrder();
    const centre = await page.$eval(`.dc-cell[data-pid="${other}"]`, (el) => {
      const r = el.getBoundingClientRect();
      return { x: r.x + r.width / 2, y: r.y + r.height / 2 };
    });
    await page.mouse.click(centre.x, centre.y);
    await page.waitForTimeout(300);
    check('완전히 덮인 패널은 클릭으로 못 꺼낸다 (알려진 한계 — Enter 가 그 길이다)',
      JSON.stringify(await domOrder()) === JSON.stringify(fullyCovered));

    await dragRows(page, target, 1);   // 한 칸 내려 other 의 윗줄이 드러나게
    const beforeClick = await boxOf(page, target);
    const strip = await page.$eval(`.dc-cell[data-pid="${other}"]`, (el) => {
      const r = el.getBoundingClientRect();
      return { x: r.x + r.width / 2, y: r.y + 6 };   // 안 덮인 윗변
    });
    await page.mouse.click(strip.x, strip.y);
    await page.waitForTimeout(300);
    const clicked = await domOrder();
    check('부분만 가려진 패널은 클릭으로 앞에 온다',
      clicked.indexOf(other) > clicked.indexOf(target),
      `${target}(${clicked.indexOf(target)}) < ${other}(${clicked.indexOf(other)})`);
    check('그 클릭은 자리를 한 칸도 바꾸지 않는다',
      JSON.stringify(await boxOf(page, target)) === JSON.stringify(beforeClick));

    // 키보드 경로도 같은 값을 낸다 — 완전히 덮인 패널의 유일한 길이다.
    await page.focus(`.dc-cell[data-pid="${target}"]`);
    await page.keyboard.press('Enter');
    await page.waitForTimeout(300);
    const entered = await domOrder();
    check('Enter 로도 앞으로 온다',
      entered.indexOf(target) > entered.indexOf(other),
      `${other}(${entered.indexOf(other)}) < ${target}(${entered.indexOf(target)})`);

    await page.click('button:has-text("겹침·빈 줄 정리")');
    await page.waitForTimeout(300);
    check('"겹침·빈 줄 정리"가 겹침을 풀고 안내가 사라진다',
      (await page.$('.dc-warn')) === null);

    /* ── ⑦ 플랫폼 섹션 접기(캐럿이 있으면 실제로 접혀야 한다) ────────── */
    const head = await page.$('.gsect-h');
    /*
     * "펼쳐졌는가"는 **머리글의 상태와 실제 자식 수**로 함께 잰다. 태그 이름으로 본문을 찾으면
     * 그 컴포넌트의 마크업이 바뀌는 날 조용히 통과한다(그게 낡은 실물 검증의 전형이다).
     */
    const bodyVisible = () => page.$eval('section.gsect', (el) => ({
      expanded: el.querySelector('.gsect-h')?.getAttribute('aria-expanded') === 'true',
      children: el.childElementCount,
    }));
    const openBefore = await bodyVisible();
    await head.click();
    await page.waitForTimeout(200);
    const openAfter = await bodyVisible();
    check('플랫폼 머리글을 누르면 접힌다',
      openBefore.expanded && openBefore.children > 1 && !openAfter.expanded && openAfter.children === 1,
      `열림(${openBefore.expanded}, 자식 ${openBefore.children}) → 접힘(${openAfter.expanded}, 자식 ${openAfter.children})`);
    check('접히면 캐럿이 돌아간다',
      await page.$eval('.gsect-h .caret', (el) => getComputedStyle(el).transform !== 'none'));
    await head.click();
    await page.waitForTimeout(200);
    const reopened = await bodyVisible();
    check('다시 누르면 펼쳐진다', reopened.expanded && reopened.children > 1,
      `${reopened.expanded}, 자식 ${reopened.children}`);
    await page.screenshot({ path: path.join(SHOT, '6-collapsed.png') });

    check('콘솔·네트워크 에러 없음', bad.length === 0, bad.slice(0, 3).join(' | '));
  } finally {
    const failed = results.filter((r) => !r.ok);
    console.log(`\n${results.length - failed.length}/${results.length} 통과 · 스크린샷 ${SHOT}`);
    if (failed.length) console.log(logs.join('').slice(-2000));
    await stop();
    process.exit(failed.length ? 1 : 0);
  }
}

main().catch((e) => { console.error('하네스 실패:', e); process.exit(2); });
