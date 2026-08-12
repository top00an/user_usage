#!/usr/bin/env node
/*
 * 계약 러너 — 포팅·회귀 게이트.
 *
 *   node contract/run.mjs capture              Go 서버를 격리 기동 → contract/golden/ 갱신
 *   node contract/run.mjs verify --base URL    이미 떠 있는 서버를 시드·캡처 → 골든과 대조
 *   node contract/run.mjs selfcheck            capture 를 두 번 돌려 하네스 자체가 결정적인지 확인
 *
 * capture 는 2026-08-09 컷오버(`server.js` 제거)로 함께 사라졌다가 **Go 바이너리 기동으로
 * 되살아났다.** 되살린 이유는 하나다 — 캡처가 없으면 응답 shape 을 아무도 못 바꾼다. 바꾸면
 * 골든이 갈리고, 골든을 손으로 고치는 것은 이 레포가 금지한 게이트 무력화이므로 변경 자체가
 * 막힌다. 바뀐 것은 **무엇을 어떻게 띄우는가** 하나이고, 무엇을 어떻게 대조하는가(REQUESTS ·
 * normalize · seed)는 harness.mjs 에 그대로 있다.
 *
 * verify 는 서버를 스폰하지 않는다 — 이미 떠 있는 서버(--base)에 물린다. CI 와 운영 점검이
 * 바이너리 위치를 몰라도 돌 수 있어야 하기 때문이다.
 *
 * capture · selfcheck 가 요구하는 것도, verify 가 요구하는 것도 **빈 DB 로 방금 뜬 서버**다.
 * 이미 데이터가 있는 서버에 물리면 시드가 기존 행과 섞여 diff 가 통째로 의미를 잃는다 —
 * 그래서 시작 전에 totals 를 확인하고 비어 있지 않으면 거부한다.
 *
 * 종료코드: 0 일치 · 1 불일치 · 2 실행 실패. CI 에 그대로 걸 수 있다.
 */
import { spawn } from 'node:child_process';
import { mkdtemp, mkdir, writeFile, readFile, readdir, rm, cp } from 'node:fs/promises';
import { existsSync, statSync, readdirSync, accessSync, constants as fsConstants } from 'node:fs';
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

/*
 * 기동할 바이너리. 기본은 scripts/build.sh 의 산출 위치이고, env 로 덮을 수 있다.
 *
 * env 를 열어 둔 이유가 있다: Go 응답 shape 을 고치는 오너는 자기 변경을 캡처해야 하는데
 * `scripts/build.sh` 는 돌릴 수 없다(Next 빌드가 비결정적이라 webroot/ 를 서로 덮어쓴다 —
 * 최종 빌드는 PM 이 한 번만 한다). 그러니 `go build -o /tmp/usage-server ./cmd/usage-server`
 * 로 **자기 바이너리만** 만들어 이 env 로 가리킨다. 정적 산출물은 캡처에 영향이 없다.
 */
const SERVER_BIN = process.env.USAGE_CONTRACT_SERVER_BIN || path.join(REPO, 'go', 'usage-server');

const BOOT_TIMEOUT_MS = 20000;
const PORT_BASE = 45000;
let bootSeq = 0;

// 레포 안이면 상대경로가 읽기 쉽고, 밖이면 ../../.. 가 길어져 오히려 안 읽힌다(env 로 /tmp 를
// 가리키는 경우가 그렇다). 그래서 레포를 벗어나면 절대경로를 그대로 찍는다.
function shortBin(p) {
  const rel = path.relative(REPO, p);
  return !rel || rel.startsWith('..') ? p : rel;
}

/*
 * ── 바이너리 확인 ─────────────────────────────────────────────────────────
 *
 * **없으면 조용히 넘어가지 않고 여기서 죽는다.** 그리고 `scripts/build.sh` 를 부르지 않는다.
 * 캡처가 대신 빌드해 주면 두 가지가 조용히 깨진다 — 최종 빌드를 PM 이 한 번만 한다는 계약이
 * 깨지고, webroot/ 를 동시에 덮어쓴다. 그래서 **안내만 하고 실패한다.**
 */
function resolveServerBin() {
  if (!existsSync(SERVER_BIN)) {
    throw new Error(
      `Go 서버 바이너리가 없다: ${SERVER_BIN}\n\n`
      + '  캡처는 이 바이너리를 띄워 응답을 뜬다. 없는데 그냥 넘어가면 골든이 안 생기거나\n'
      + '  낡은 것이 남아 **캡처가 거짓말을 한다** — 그래서 여기서 멈춘다.\n\n'
      + '  이 러너는 빌드하지 않는다(최종 빌드는 PM 이 한 번만 한다 — scripts/build.sh).\n'
      + '  다음 중 하나를 하라:\n'
      + '    · 릴리스 경로:  bash scripts/build.sh        (PM 전용 — webroot/ 까지 함께 만든다)\n'
      + '    · Go 만 고쳤다: (cd go && go build -o /tmp/usage-server ./cmd/usage-server) \\\n'
      + '                     && USAGE_CONTRACT_SERVER_BIN=/tmp/usage-server npm run contract\n',
    );
  }
  try {
    accessSync(SERVER_BIN, fsConstants.X_OK);
  } catch {
    throw new Error(`Go 서버 바이너리에 실행 권한이 없다: ${SERVER_BIN}\n  chmod +x 하거나 다시 빌드하라.`);
  }
  if (statSync(SERVER_BIN).size === 0) {
    throw new Error(`Go 서버 바이너리가 0바이트다: ${SERVER_BIN}\n  빌드가 중간에 죽은 것이다 — 다시 빌드하라.`);
  }
  assertNotStale();
  return SERVER_BIN;
}

/*
 * ── 낡은 바이너리 거부 ────────────────────────────────────────────────────
 *
 * 바이너리가 **있지만 소스보다 오래된** 경우가 실무에서 훨씬 위험하다. 없으면 위에서 잡히지만
 * 낡은 것은 조용히 잘 돌면서 **한 판 전의 응답을 골든에 박는다.** 그러면 골든은 소스가 아니라
 * 사라진 과거를 동결하고, 그 뒤의 verify 빨간불·초록불이 전부 무의미해진다.
 *
 * 그래서 소스가 바이너리보다 새로우면 **거부한다.** 되돌릴 수 없는 산출물(골든)을 만드는
 * 명령이라 경고로 흘리지 않는다 — 경고는 반드시 무시되는 날이 온다.
 * 판단이 틀렸다고 확신하면 USAGE_CONTRACT_ALLOW_STALE=1 로 넘길 수 있다(로그에 남는다).
 */
function assertNotStale() {
  const binMtime = statSync(SERVER_BIN).mtimeMs;
  const newer = [];
  const roots = [path.join(REPO, 'go')];
  for (const root of roots) {
    if (!existsSync(root)) continue;
    for (const rel of readdirSync(root, { recursive: true })) {
      const name = String(rel);
      // 테스트 파일은 바이너리에 안 들어간다. 정적 산출물(webroot·agentbin)도 응답 shape 과 무관하다.
      if (!/\.(go|mod|sum)$/.test(name) || name.endsWith('_test.go')) continue;
      if (name.includes(`webroot${path.sep}`) || name.includes(`agentbin${path.sep}`)) continue;
      const full = path.join(root, name);
      let st;
      try { st = statSync(full); } catch { continue; }
      if (st.isFile() && st.mtimeMs > binMtime) newer.push(path.relative(REPO, full));
    }
  }
  if (!newer.length) return;

  const list = newer.slice(0, 10).map((f) => `      ${f}`).join('\n')
    + (newer.length > 10 ? `\n      … 외 ${newer.length - 10}개` : '');
  if (process.env.USAGE_CONTRACT_ALLOW_STALE) {
    console.warn(`\n⚠ 낡은 바이너리로 캡처한다(USAGE_CONTRACT_ALLOW_STALE) — 소스 ${newer.length}개가 더 새롭다:\n${list}\n`);
    return;
  }
  throw new Error(
    `Go 소스가 바이너리보다 새롭다 — 낡은 바이너리로 캡처하면 골든이 과거를 동결한다.\n\n`
    + `  바이너리: ${shortBin(SERVER_BIN)}\n`
    + `  더 새로운 소스 ${newer.length}개:\n${list}\n\n`
    + '  이 러너는 빌드하지 않는다. 자기 변경을 캡처하려면 자기 바이너리를 만들어 가리켜라:\n'
    + '    (cd go && go build -o /tmp/usage-server ./cmd/usage-server) \\\n'
    + '      && USAGE_CONTRACT_SERVER_BIN=/tmp/usage-server npm run contract\n\n'
    + '  (정말 낡은 것으로 떠야 한다면 USAGE_CONTRACT_ALLOW_STALE=1)\n',
  );
}

/*
 * ── Go 서버를 격리 기동 ───────────────────────────────────────────────────
 *
 * env 조건은 골든이 캡처된 그 조건이어야 한다. 특히 두 개가 값을 바꾼다:
 *
 *   USAGE_KEYWORD_RETENTION_DAYS=off  보존 정리기가 캡처 중에 keyword 축을 지우면 결과가
 *                                     흔들린다. 기본값 90 으로 뜨면 retention.keywordDays 가
 *                                     null 이 아니라 90 이 되어 스냅샷 3개가 갈린다.
 *   USAGE_CONFIG=''                   단가표다. 비용은 저장하지 않고 읽을 때마다 계산하므로
 *                                     다른 단가표를 물면 cost 가 전부 달라진다. 비워서
 *                                     레포 기준(cwd/config.json → 없으면 시드 단가표)으로 못 박는다.
 *
 * ⚠ 그래서 **셸에 남아 있던 USAGE_* 를 그대로 물려주지 않는다.** 물려주면 캡처가 개발자
 *   셸 상태에 따라 달라지고, 그 차이는 골든 diff 로만 드러난다(원인을 아무도 못 찾는다).
 *   PATH·HOME 같은 것은 필요하니 남기고, **결과를 바꾸는 것만 명시적으로 못 박는다.**
 */
function bootEnv(dataDir, port) {
  return {
    ...process.env,
    USAGE_ADMIN_TOKEN: ADMIN,
    USAGE_INTAKE_TOKEN: INTAKE,
    USAGE_DATA_DIR: dataDir,
    USAGE_PORT: String(port),
    USAGE_HOST: '127.0.0.1',
    USAGE_DB_MODE: 'local',
    USAGE_KEYWORD_RETENTION_DAYS: 'off',
    // 아래는 전부 "셸에 있었더라도 없는 것으로 친다"는 뜻이다(Go 는 빈 문자열을 미설정으로 읽는다).
    USAGE_TENANT: 'default',
    USAGE_CONFIG: '',
    USAGE_MULTITENANT: '',
    DATABASE_URL: '',
    // 부트스트랩이 걸려 있으면 사용자 1명이 생겨 identity·dispatch 응답이 갈린다.
    USAGE_BOOTSTRAP_ADMIN_USER: '',
    USAGE_BOOTSTRAP_ADMIN_PASSWORD: '',
    USAGE_BOOTSTRAP_TENANT: '',
  };
}

async function bootOnce(bin, port) {
  const dataDir = await mkdtemp(path.join(tmpdir(), 'usage-contract-'));
  const child = spawn(bin, [], {
    // cwd 가 레포 루트여야 단가표 탐색(cwd/config.json)이 운영과 같아진다.
    cwd: REPO,
    env: bootEnv(dataDir, port),
    stdio: ['ignore', 'pipe', 'pipe'],
  });

  const log = [];
  child.stdout.on('data', (b) => log.push(String(b)));
  child.stderr.on('data', (b) => log.push(String(b)));
  // spawn 자체가 실패하는 경우(권한·ENOEXEC)를 삼키지 않는다.
  let spawnErr = null;
  child.on('error', (e) => { spawnErr = e; });

  const cleanup = async () => { await rm(dataDir, { recursive: true, force: true }); };

  const base = `http://127.0.0.1:${port}`;
  const deadline = Date.now() + BOOT_TIMEOUT_MS;
  for (;;) {
    if (spawnErr) { await cleanup(); throw new Error(`서버를 실행할 수 없다(${bin}): ${spawnErr.message}`); }
    if (child.exitCode !== null) {
      await cleanup();
      throw new Error(`서버가 기동 중 죽었다(code=${child.exitCode})\n${log.join('')}`);
    }
    try {
      const r = await fetch(`${base}/healthz`);
      if (r.ok) break;
    } catch { /* 아직 안 떴다 */ }
    if (Date.now() > deadline) {
      child.kill('SIGKILL');
      await cleanup();
      throw new Error(`서버 기동 시간 초과(${BOOT_TIMEOUT_MS}ms)\n${log.join('')}`);
    }
    await new Promise((r) => setTimeout(r, 120));
  }

  return {
    base,
    async stop() {
      // SIGTERM 이면 graceful shutdown 한다(main.go). 안 죽으면 포트를 안 놓아 다음 기동이 막히므로
      // 유예 뒤에 SIGKILL 로 확실히 끝낸다 — selfcheck 는 연달아 두 번 띄운다.
      child.kill('SIGTERM');
      const exited = await new Promise((resolve) => {
        let done = false;
        const t = setTimeout(() => { if (!done) { done = true; resolve(false); } }, 5000);
        child.once('exit', () => { if (!done) { done = true; clearTimeout(t); resolve(true); } });
      });
      if (!exited) {
        child.kill('SIGKILL');
        await new Promise((resolve) => {
          const t = setTimeout(resolve, 2000);
          child.once('exit', () => { clearTimeout(t); resolve(); });
        });
      }
      await cleanup();
    },
  };
}

/*
 * 포트는 고정 대역에서 고른다(서버가 포트 0 을 지원하지 않는다 — 번호를 찍고 그 번호로 뜬다).
 * 같은 워킹트리를 여러 사람이 쓰면 충돌하므로 **바인드 실패는 다음 포트로 재시도한다.**
 * 재시도가 없으면 남의 게이트 때문에 내 캡처가 실패하고, 그 실패는 원인이 안 보인다.
 */
async function bootGo() {
  const bin = resolveServerBin();
  let last;
  for (let attempt = 0; attempt < 5; attempt++) {
    const port = PORT_BASE + ((process.pid + bootSeq * 37 + attempt * 419) % 2000);
    bootSeq += 1;
    try {
      return await bootOnce(bin, port);
    } catch (e) {
      last = e;
      const msg = String((e && e.message) || e);
      // 포트 충돌만 재시도한다. 설정 거부·패닉은 재시도해도 같으니 그대로 올린다.
      if (!/들을 수 없다|EADDRINUSE|address already in use/i.test(msg)) throw e;
    }
  }
  throw last;
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

/* ── 시드 → 캡처 (대상 서버에 대고) ──────────────────────────────────── */
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
  const cmd = process.argv[2] || 'verify';

  if (cmd === 'capture') {
    const srv = await bootGo();
    // 골든을 **먼저 지우지 않는다.** 임시로 다 뜨고 성공했을 때만 갈아끼운다 — 중간에 실패하면
    // 동결 골든이 사라져 사람이 git checkout 으로 복구해야 하고, 그동안 게이트가 없어진다.
    const stage = await mkdtemp(path.join(tmpdir(), 'usage-capture-'));
    try {
      const n = await runCapture(srv.base, { admin: ADMIN, intake: INTAKE }, stage);
      await rm(GOLDEN, { recursive: true, force: true });
      await mkdir(GOLDEN, { recursive: true });
      await cp(stage, GOLDEN, { recursive: true });
      console.log(`✅ 골든 ${n}개 기록 → contract/golden/  (바이너리: ${shortBin(SERVER_BIN)})`);
      console.log('   변경이 있다면 `git diff -- contract/golden/` 로 무엇이 왜 달라졌는지 사람이 검토하라.');
      return 0;
    } finally {
      await srv.stop();
      await rm(stage, { recursive: true, force: true });
    }
  }

  if (cmd === 'selfcheck') {
    // 하네스가 스스로 결정적인지 확인한다. 여기서 흔들리면 verify 의 빨간불은 전부 거짓이다.
    const tmp = await mkdtemp(path.join(tmpdir(), 'usage-selfcheck-'));
    try {
      for (const round of ['a', 'b']) {
        const srv = await bootGo();
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
    if (!existsSync(GOLDEN)) { console.error('골든이 없다(contract/golden/) — 먼저 `npm run contract` 로 캡처하라.'); return 2; }
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
