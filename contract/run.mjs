#!/usr/bin/env node
/*
 * 계약 동결 러너 — 포팅의 합격 게이트.
 *
 *   node contract/run.mjs capture              현행 Node 서버를 격리 기동 → contract/golden/ 갱신
 *   node contract/run.mjs verify --base URL    이미 떠 있는 서버(=Go 포팅본)를 시드·캡처 → 골든과 대조
 *   node contract/run.mjs selfcheck            capture 를 두 번 돌려 하네스 자체가 결정적인지 확인
 *
 * verify 가 요구하는 것은 **빈 DB 로 방금 뜬 서버**다. 이미 데이터가 있는 서버에 물리면
 * 시드가 기존 행과 섞여 diff 가 통째로 의미를 잃는다 — 그래서 시작 전에 totals 를 확인하고
 * 비어 있지 않으면 거부한다.
 *
 * 종료코드: 0 일치 · 1 불일치 · 2 실행 실패. CI 에 그대로 걸 수 있다.
 */
import { spawn } from 'node:child_process';
import { mkdtemp, mkdir, writeFile, readFile, readdir, rm } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { SEED, REPLAY, IDENTITY } from './fixtures.mjs';
import { captureAll, seed, normalize } from './harness.mjs';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO = path.resolve(HERE, '..');
const GOLDEN = path.join(HERE, 'golden');

// 고정 토큰이다 — 골든에 토큰 값이 새지 않고(응답에 안 실린다), 재현이 쉬워야 한다.
const ADMIN = 'contract-admin-token-0123456789';
const INTAKE = 'contract-intake-token-9876543210';

const FIXTURES = { SEED, REPLAY, IDENTITY };

/* ── 현행 Node 서버를 격리 기동 ───────────────────────────────────────── */
async function bootNode() {
  const dataDir = await mkdtemp(path.join(tmpdir(), 'usage-contract-'));
  // 포트 0 은 서버가 지원하지 않으므로(고정 번호를 찍는다) 충돌 확률이 낮은 대역을 쓴다.
  const port = 45000 + Math.floor(process.pid % 2000);
  const child = spawn(process.execPath, ['server.js'], {
    cwd: REPO,
    env: {
      ...process.env,
      USAGE_ADMIN_TOKEN: ADMIN,
      USAGE_INTAKE_TOKEN: INTAKE,
      USAGE_DATA_DIR: dataDir,
      USAGE_PORT: String(port),
      USAGE_HOST: '127.0.0.1',
      USAGE_DB_MODE: 'local',
      // 보존 정리기가 캡처 중에 keyword 를 지우면 결과가 흔들린다. 무기한으로 못 박는다.
      USAGE_KEYWORD_RETENTION_DAYS: 'off',
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  });

  const log = [];
  child.stdout.on('data', (b) => log.push(String(b)));
  child.stderr.on('data', (b) => log.push(String(b)));

  const base = `http://127.0.0.1:${port}`;
  const deadline = Date.now() + 15000;
  for (;;) {
    if (child.exitCode !== null) throw new Error(`서버가 기동 중 죽었다(code=${child.exitCode})\n${log.join('')}`);
    try {
      const r = await fetch(`${base}/healthz`);
      if (r.ok) break;
    } catch { /* 아직 안 떴다 */ }
    if (Date.now() > deadline) { child.kill('SIGKILL'); throw new Error(`서버 기동 시간 초과\n${log.join('')}`); }
    await new Promise((r) => setTimeout(r, 120));
  }

  return {
    base,
    async stop() {
      child.kill('SIGTERM');
      await new Promise((r) => { child.once('exit', r); setTimeout(r, 3000).unref?.(); });
      await rm(dataDir, { recursive: true, force: true });
    },
  };
}

/* ── 빈 DB 확인 ───────────────────────────────────────────────────────── */
async function assertEmpty(base, admin) {
  const r = await fetch(`${base}/api/usage/summary?days=365`, { headers: { Authorization: `Bearer ${admin}` } });
  if (!r.ok) throw new Error(`대상 서버 조회 실패 ${r.status} — 토큰이 맞는지 확인하라`);
  const j = await r.json();
  const n = (j.totals && j.totals.sessions) || 0;
  if (n !== 0) {
    throw new Error(`대상 서버에 이미 세션 ${n}건이 있다 — 시드가 섞여 대조가 의미를 잃는다. `
      + '빈 데이터 디렉터리로 다시 띄우고 물려라.');
  }
}

/* ── 캡처 ─────────────────────────────────────────────────────────────── */
async function runCapture(base, tokens, outDir) {
  await assertEmpty(base, tokens.admin);
  const seeded = await seed(base, tokens.admin, FIXTURES);
  const snaps = await captureAll(base, tokens);

  await mkdir(outDir, { recursive: true });
  // 파일을 나눠 쓴다 — 한 덩어리면 diff 에서 무엇이 깨졌는지 눈으로 못 찾는다.
  for (const [name, snap] of Object.entries(snaps)) {
    await writeFile(path.join(outDir, `${name}.json`), JSON.stringify(snap, null, 2) + '\n', 'utf8');
  }
  await writeFile(path.join(outDir, '_intake.json'),
    JSON.stringify(normalize(seeded), null, 2) + '\n', 'utf8');
  return Object.keys(snaps).length + 1;
}

/* ── 대조 ─────────────────────────────────────────────────────────────── */
function diffValue(a, b, p, out) {
  if (JSON.stringify(a) === JSON.stringify(b)) return;
  const aObj = a && typeof a === 'object';
  const bObj = b && typeof b === 'object';
  if (!aObj || !bObj || Array.isArray(a) !== Array.isArray(b)) {
    out.push({ path: p, golden: a, actual: b });
    return;
  }
  if (Array.isArray(a)) {
    if (a.length !== b.length) { out.push({ path: `${p}.length`, golden: a.length, actual: b.length }); return; }
    a.forEach((v, i) => diffValue(v, b[i], `${p}[${i}]`, out));
    return;
  }
  for (const k of new Set([...Object.keys(a), ...Object.keys(b)])) {
    if (!(k in a)) { out.push({ path: `${p}.${k}`, golden: '<없음>', actual: b[k] }); continue; }
    if (!(k in b)) { out.push({ path: `${p}.${k}`, golden: a[k], actual: '<없음>' }); continue; }
    diffValue(a[k], b[k], `${p}.${k}`, out);
  }
}

async function compare(goldenDir, actualDir) {
  const names = (await readdir(goldenDir)).filter((f) => f.endsWith('.json')).sort();
  const failures = [];
  for (const f of names) {
    const g = JSON.parse(await readFile(path.join(goldenDir, f), 'utf8'));
    const aPath = path.join(actualDir, f);
    if (!existsSync(aPath)) { failures.push({ file: f, diffs: [{ path: '<파일>', golden: '있음', actual: '없음' }] }); continue; }
    const a = JSON.parse(await readFile(aPath, 'utf8'));
    const diffs = [];
    diffValue(g, a, '', diffs);
    if (diffs.length) failures.push({ file: f, diffs });
  }
  return { total: names.length, failures };
}

function report({ total, failures }) {
  if (!failures.length) {
    console.log(`\n✅ 계약 일치 — 스냅샷 ${total}개 전부 동일`);
    return 0;
  }
  console.log(`\n❌ 계약 불일치 — 스냅샷 ${total}개 중 ${failures.length}개 어긋남\n`);
  for (const f of failures) {
    console.log(`── ${f.file}  (${f.diffs.length}건)`);
    // 전부 찍으면 스크롤이 흘러 아무도 안 본다. 앞 8건만 보이고 나머지는 세어서 알린다.
    for (const d of f.diffs.slice(0, 8)) {
      console.log(`   ${d.path || '<root>'}`);
      console.log(`     golden: ${JSON.stringify(d.golden)}`);
      console.log(`     actual: ${JSON.stringify(d.actual)}`);
    }
    if (f.diffs.length > 8) console.log(`   … 외 ${f.diffs.length - 8}건`);
    console.log('');
  }
  return 1;
}

/* ── CLI ──────────────────────────────────────────────────────────────── */
function arg(name, dflt) {
  const i = process.argv.indexOf(`--${name}`);
  return i > 0 && process.argv[i + 1] ? process.argv[i + 1] : dflt;
}

async function main() {
  const cmd = process.argv[2] || 'capture';

  if (cmd === 'capture') {
    const srv = await bootNode();
    try {
      await rm(GOLDEN, { recursive: true, force: true });
      const n = await runCapture(srv.base, { admin: ADMIN, intake: INTAKE }, GOLDEN);
      console.log(`✅ 골든 ${n}개 기록 → contract/golden/`);
      return 0;
    } finally { await srv.stop(); }
  }

  if (cmd === 'selfcheck') {
    // 하네스가 스스로 결정적인지 확인한다. 여기서 흔들리면 verify 의 빨간불은 전부 거짓이다.
    const tmp = await mkdtemp(path.join(tmpdir(), 'usage-selfcheck-'));
    try {
      for (const round of ['a', 'b']) {
        const srv = await bootNode();
        try { await runCapture(srv.base, { admin: ADMIN, intake: INTAKE }, path.join(tmp, round)); }
        finally { await srv.stop(); }
      }
      const r = await compare(path.join(tmp, 'a'), path.join(tmp, 'b'));
      if (!r.failures.length) console.log(`\n✅ 하네스 결정적 — 두 번 캡처 결과 동일(${r.total}개)`);
      return report(r);
    } finally { await rm(tmp, { recursive: true, force: true }); }
  }

  if (cmd === 'verify') {
    const base = arg('base');
    if (!base) { console.error('사용법: node contract/run.mjs verify --base http://127.0.0.1:PORT [--admin T] [--intake T]'); return 2; }
    if (!existsSync(GOLDEN)) { console.error('골든이 없다 — 먼저 `node contract/run.mjs capture` 를 돌려라.'); return 2; }
    const tokens = { admin: arg('admin', ADMIN), intake: arg('intake', INTAKE) };
    const tmp = await mkdtemp(path.join(tmpdir(), 'usage-verify-'));
    try {
      await runCapture(base, tokens, tmp);
      return report(await compare(GOLDEN, tmp));
    } finally { await rm(tmp, { recursive: true, force: true }); }
  }

  console.error(`알 수 없는 명령: ${cmd} (capture | verify | selfcheck)`);
  return 2;
}

main().then((c) => { process.exitCode = c; }).catch((e) => {
  console.error('계약 러너 실패:', (e && e.stack) || e);
  process.exitCode = 2;
});
