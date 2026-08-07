#!/usr/bin/env node
/*
 * ── 실물 검증 ────────────────────────────────────────────────────────────
 *
 * 게이트가 초록불이라고 화면이 뜨는 것은 아니다. `npm run build` 도 `vitest` 도 **브라우저에서
 * 실제로 그려지는가**는 답하지 못한다 — 특히 이 앱은 CSP(script-src 'self') 아래에서 도는데,
 * 그 헤더는 빌드에도 jsdom 에도 없다. 여기서 확인하는 것은 그 둘이 답하지 못하는 것들이다:
 *
 *   ① 실제 크로미움 · 실제 CSP 헤더 · 실제 쿠키에서 화면이 뜨는가
 *   ② 토큰 게이트 → 열기 → 탭 전환 → 401 복구 → 토큰 지우기 왕복
 *   ③ "근사값을 정확한 값으로 위장하지 않는다"의 6개 항목이 **실제로 화면에 있는가**
 *   ④ 탭을 빠르게 오갈 때 앞 탭의 응답이 현재 탭을 덮지 않는가
 *
 * 실행: node scripts/verify-live.mjs   (out/ 이 이미 빌드돼 있어야 한다)
 */
import { spawn } from 'node:child_process';
import { mkdir } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { startSeededApi, ADMIN_TOKEN, KEYWORD_DAYS } from './seeded-api.mjs';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const WEB = path.resolve(HERE, '..');
const SHOTS = path.join(WEB, '.verify');

const API_PORT = 4192;
const WEB_PORT = 4300;
const ORIGIN = `http://127.0.0.1:${WEB_PORT}`;

const results = [];
function check(name, ok, detail = '') {
  results.push({ name, ok, detail });
  console.log(`${ok ? '  ✓' : '  ✗'} ${name}${detail ? ` — ${detail}` : ''}`);
}

async function startPreview() {
  const child = spawn(process.execPath, [path.join(HERE, 'preview.mjs'), '--port', String(WEB_PORT), '--api', `http://127.0.0.1:${API_PORT}`], {
    cwd: WEB, stdio: ['ignore', 'pipe', 'pipe'],
  });
  child.stdout.on('data', (b) => process.stdout.write(`[preview] ${b}`));
  child.stderr.on('data', (b) => process.stderr.write(`[preview!] ${b}`));
  const deadline = Date.now() + 10000;
  for (;;) {
    try { const r = await fetch(ORIGIN); if (r.ok) break; } catch { /* 아직 */ }
    if (Date.now() > deadline) throw new Error('preview 기동 시간 초과');
    await new Promise((r) => setTimeout(r, 100));
  }
  return { stop: () => child.kill('SIGTERM') };
}

const api = await startSeededApi({ port: API_PORT });
const preview = await startPreview();
await mkdir(SHOTS, { recursive: true });

const browser = await chromium.launch();
const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
const page = await ctx.newPage();

/* CSP 위반과 콘솔 오류를 전부 모은다 — 하나라도 있으면 "떠 보이지만 죽은 화면"이다. */
const consoleErrors = [];
const cspViolations = [];
/*
 * 일부러 낸 401(틀린 토큰 단계)은 브라우저가 "Failed to load resource ... 401" 로 남긴다.
 * 그건 화면이 의도한 경로이므로 오류로 세지 않는다 — 세면 이 검증이 늘 빨간불이라
 * 아무도 안 보게 되고, 그러면 진짜 오류도 같이 묻힌다.
 */
const EXPECTED = /Failed to load resource.*40[13]/i;
page.on('console', (m) => {
  if (m.type() !== 'error') return;
  const t = m.text();
  if (/Content Security Policy|Refused to (execute|load)/i.test(t)) { cspViolations.push(t); consoleErrors.push(t); return; }
  if (EXPECTED.test(t)) return;
  consoleErrors.push(t);
});
page.on('pageerror', (e) => consoleErrors.push(`pageerror: ${e.message}`));

const text = () => page.locator('#app').innerText();
const has = async (s) => (await text()).includes(s);

try {
  console.log('\n── ① 토큰 게이트 ─────────────────────────────────────────');
  await page.goto(ORIGIN, { waitUntil: 'networkidle' });
  await page.waitForSelector('form', { timeout: 10000 });
  check('토큰 없이 열면 게이트가 뜬다', await has('토큰이 필요합니다') || await has('사용량 대시보드'));
  check('게이트에 토큰 입력칸이 있다', await page.locator('input[type=password]').count() === 1);
  await page.screenshot({ path: path.join(SHOTS, '01-gate.png') });

  console.log('\n── ② 틀린 토큰 → 401 복구 ────────────────────────────────');
  await page.fill('input[type=password]', 'wrong-token-but-long-enough');
  await page.click('button[type=submit]');
  await page.waitForFunction(() => document.body.innerText.includes('토큰이 올바르지 않거나 만료'), null, { timeout: 10000 });
  check('401 이면 게이트로 되돌아오고 이유를 말한다', await has('토큰이 올바르지 않거나 만료'));
  const cookieAfter401 = (await ctx.cookies()).find((c) => c.name === 'usage_tok');
  check('401 이면 쿠키를 지운다', !cookieAfter401 || cookieAfter401.value === '');

  console.log('\n── ③ 올바른 토큰 → 사용 추적 ─────────────────────────────');
  await page.fill('input[type=password]', ADMIN_TOKEN);
  await page.click('button[type=submit]');
  await page.waitForFunction(() => document.body.innerText.includes('사용자별'), null, { timeout: 15000 });

  const tok = (await ctx.cookies()).find((c) => c.name === 'usage_tok');
  check('토큰이 쿠키(usage_tok)로 저장된다', !!tok, tok ? `SameSite=${tok.sameSite} Secure=${tok.secure} path=${tok.path}` : '');
  check('http 에서는 Secure 가 붙지 않는다(로컬 기동이 성립해야 한다)', tok?.secure === false);
  check('SameSite=Strict', tok?.sameSite === 'Strict');

  const t1 = await text();
  check('실제 데이터가 렌더된다 — 보고된 세션 8', /보고된 세션\s*\n?\s*8/.test(t1), t1.match(/보고된 세션\s*\n?\s*\d+/)?.[0]);
  check('사용자별 표에 시드 사용자(alice·bob·carol)가 있다',
    t1.includes('alice') && t1.includes('bob') && t1.includes('carol'));
  check('모델별 표에 시드 모델이 있다', t1.includes('claude-opus-5') && t1.includes('claude-sonnet-5'));

  // ④-1 근거 열
  check('[④-1] 모델별 표에 「근거」 열이 있다', await page.locator('th', { hasText: '근거' }).count() > 0);
  check('[④-1] 근거 열이 series 정확값과 세션 최빈 근사를 갈라 말한다',
    t1.includes('series — 모델별 정확') && /세션 최빈 기준\s*[\d.]+%/.test(t1));
  // ④-2 series 커버리지(modelAxis)
  check('[④-2] 사용자별 series 커버리지 표가 있다',
    t1.includes('사용자별 series 커버리지') && t1.includes('series 있음') && t1.includes('모델별 값의 근거'));
  check('[④-2] 커버리지가 낮은 이유를 단정 없이 말한다(시각 없는 턴)', t1.includes('시각 없는 턴'));
  // ④-5 키워드 보존
  check('[④-5] 키워드 보존 기한이 화면에 있다', t1.includes(`키워드는 ${KEYWORD_DAYS}일 보관 후 자동 삭제`));

  await page.screenshot({ path: path.join(SHOTS, '02-usagetrack.png'), fullPage: true });

  console.log('\n── ④ 축 전환 · 사람별 활용 ──────────────────────────────');
  await page.locator('#axis-agent').click();
  await page.waitForFunction(() => document.body.innerText.includes('사람별 활용'), null, { timeout: 10000 });
  const tAgent = await text();
  check('서브에이전트 축에서 사람별 활용이 뜬다', tAgent.includes('사람별 활용') && tAgent.includes('general-purpose'));

  console.log('\n── ⑤ 탭 전환 → 사용 관측 ────────────────────────────────');
  await page.locator('#shelltab-usageobs').click();
  await page.waitForFunction(() => document.body.innerText.includes('API 환산액'), null, { timeout: 15000 });
  const t2 = await text();

  check('해시 딥링크가 갱신된다', page.url().endsWith('#/usageobs'), page.url());
  check('비용의 축별 분해가 있다', t2.includes('캐시 읽기') && t2.includes('캐시 생성') && t2.includes('비중'));
  // ④-3 unpriced
  check('[④-3] 단가 미등록 모델 이름이 화면에 있다',
    t2.includes('단가 미등록 모델') && t2.includes('some-unreleased-model-x'));
  // ④-4 ttlUnknownRows
  check('[④-4] TTL 미상 행 수와 과소 추정 사실이 화면에 있다',
    /TTL 미상\s*5행/.test(t2) && t2.includes('최대 1.6배'));
  // ④-6 수집 커버리지
  check('[④-6] 수집 상태(발신처별 마지막 보고)가 있다',
    t2.includes('수집 상태') && t2.includes('host-a') && t2.includes('마지막 보고'));
  check('품질·분포·상위 세션 카드가 있다',
    t2.includes('품질') && t2.includes('분포') && t2.includes('상위 세션'));

  await page.screenshot({ path: path.join(SHOTS, '03-usageobs.png'), fullPage: true });

  console.log('\n── ⑥ 세션 드릴다운 · 사용자 상세 모달 ───────────────────');
  await page.locator('tr.rowlink').first().click();
  await page.waitForFunction(() => document.body.innerText.includes('시간 버킷') || document.body.innerText.includes('이 세션의 도구 사용 기록'), null, { timeout: 10000 });
  check('세션 상세가 열린다', (await text()).includes('S3-carol-mixed-models') || (await text()).includes('시간 버킷'));

  await page.locator('button:has-text("상세")').first().click();
  await page.waitForSelector('[role=dialog]', { timeout: 10000 });
  await page.waitForFunction(() => document.querySelector('[role=dialog]')?.innerText.includes('최근 7일'), null, { timeout: 10000 });
  check('사용자 상세 모달이 열린다', await page.locator('[role=dialog]').isVisible());
  await page.screenshot({ path: path.join(SHOTS, '04-modal.png') });
  await page.keyboard.press('Escape');
  await page.waitForFunction(() => !document.querySelector('[role=dialog]'), null, { timeout: 5000 });
  check('Escape 로 모달이 닫힌다', await page.locator('[role=dialog]').count() === 0);

  console.log('\n── ⑦ 낡은 응답 폐기 (탭 빠른 전환) ──────────────────────');
  /*
   * 사용 추적의 summary 응답만 3초 늦춘다. 그 사이에 탭을 관측으로 옮기면, 늦게 도착한
   * 추적 응답이 관측 화면을 덮어쓰는가를 본다. 덮어쓰면 화면이 틀린 값을 보여주는데
   * 아무 에러도 나지 않는다 — 이 검증이 없으면 그 사고는 사람이 우연히 보기 전까지 안 잡힌다.
   */
  let aborted = 0;
  await page.route('**/api/usage/summary*', async (route) => {
    await new Promise((r) => setTimeout(r, 3000));
    try { await route.continue(); } catch { aborted += 1; }
  });
  await page.locator('#shelltab-usage').click();
  await page.waitForTimeout(300);                 // 응답이 오기 전에
  await page.locator('#shelltab-usageobs').click();
  await page.waitForFunction(() => document.body.innerText.includes('API 환산액'), null, { timeout: 15000 });
  await page.waitForTimeout(4000);                // 늦은 응답이 도착하고도 남을 시간
  const after = await text();
  check('늦게 도착한 앞 탭 응답이 현재 탭을 덮지 않는다',
    after.includes('API 환산액') && !after.includes('보고된 세션'),
    `요청 취소 ${aborted}건`);
  check('탭 상태와 화면이 일치한다',
    await page.locator('#shelltab-usageobs').getAttribute('aria-selected') === 'true');
  await page.unroute('**/api/usage/summary*');

  console.log('\n── ⑧ 토큰 지우기 ────────────────────────────────────────');
  await page.locator('#signout').click();
  await page.waitForFunction(() => document.body.innerText.includes('토큰을 지웠습니다'), null, { timeout: 10000 });
  check('토큰 지우기 → 게이트 복귀', await has('토큰을 지웠습니다'));
  const gone = (await ctx.cookies()).find((c) => c.name === 'usage_tok');
  check('쿠키가 지워진다', !gone || gone.value === '');

  console.log('\n── ⑨ 다크 · 라이트 테마 · 좁은 화면 ─────────────────────');
  /* 이 도구의 기본은 다크다 — 두 테마 모두 실제로 그려 보고 남긴다(토큰 한 벌만 갈아끼우는 구조). */
  await page.evaluate(() => localStorage.setItem('usage-theme', 'dark'));
  await ctx.addCookies([{ name: 'usage_tok', value: ADMIN_TOKEN, url: ORIGIN, sameSite: 'Strict' }]);
  /*
   * ⚠ 해시만 다른 URL 로 goto 하면 브라우저는 **다시 읽지 않는다**(같은 문서의 해시 변경).
   *   그러면 방금 심은 쿠키·테마가 반영되지 않아 게이트가 그대로 남는다 — 실제로 한 번 겪었다.
   *   그래서 문서를 먼저 새로 열고, 해시는 그다음에 바꾼다.
   */
  await page.goto(ORIGIN, { waitUntil: 'networkidle' });
  await page.evaluate(() => { location.hash = '#/usageobs'; });
  await page.waitForFunction(() => document.body.innerText.includes('API 환산액'), null, { timeout: 15000 });
  check('다크 테마가 페인트 전에 적용된다',
    await page.evaluate(() => document.documentElement.getAttribute('data-theme')) === 'dark');
  await page.screenshot({ path: path.join(SHOTS, '05-dark-usageobs.png'), fullPage: true });
  await page.evaluate(() => { location.hash = '#/usage'; });
  await page.waitForFunction(() => document.body.innerText.includes('보고된 세션'), null, { timeout: 15000 });
  await page.screenshot({ path: path.join(SHOTS, '05-dark-usagetrack.png'), fullPage: true });

  await page.evaluate(() => localStorage.setItem('usage-theme', 'light'));
  await ctx.addCookies([{ name: 'usage_tok', value: ADMIN_TOKEN, url: ORIGIN, sameSite: 'Strict' }]);
  await page.goto(ORIGIN, { waitUntil: 'networkidle' });
  await page.waitForFunction(() => document.body.innerText.includes('사용자별'), null, { timeout: 15000 });
  check('라이트 테마가 페인트 전에 적용된다',
    await page.evaluate(() => document.documentElement.getAttribute('data-theme')) === 'light');
  await page.screenshot({ path: path.join(SHOTS, '06-light.png'), fullPage: true });

  await page.setViewportSize({ width: 390, height: 844 });
  await page.waitForTimeout(400);
  const overflow = await page.evaluate(() => ({
    scrollW: document.documentElement.scrollWidth,
    clientW: document.documentElement.clientWidth,
  }));
  check('390px 에서 본문이 가로로 밀리지 않는다',
    overflow.scrollW <= overflow.clientW + 1, `scrollW=${overflow.scrollW} clientW=${overflow.clientW}`);
  await page.screenshot({ path: path.join(SHOTS, '07-mobile.png'), fullPage: true });

  console.log('\n── ⑩ CSP · 콘솔 ────────────────────────────────────────');
  check('CSP(script-src \'self\') 위반이 없다', cspViolations.length === 0, cspViolations.slice(0, 3).join(' | '));
  check('콘솔 오류가 없다', consoleErrors.length === 0, consoleErrors.slice(0, 3).join(' | '));
} catch (e) {
  check('검증 스크립트가 끝까지 돌았다', false, String(e && e.message));
  await page.screenshot({ path: path.join(SHOTS, '99-failure.png'), fullPage: true }).catch(() => {});
} finally {
  await browser.close();
  preview.stop();
  await api.stop();
}

const failed = results.filter((r) => !r.ok);
console.log(`\n${'='.repeat(60)}`);
console.log(`검증 ${results.length}건 · 통과 ${results.length - failed.length} · 실패 ${failed.length}`);
console.log(`스크린샷: ${SHOTS}`);
if (failed.length) {
  console.log('\n실패:');
  for (const f of failed) console.log(`  ✗ ${f.name}${f.detail ? ` — ${f.detail}` : ''}`);
}
process.exit(failed.length ? 1 : 0);
