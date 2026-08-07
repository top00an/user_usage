#!/usr/bin/env node
/*
 * 검증용 API 서버 기동 — **격리된 임시 DB** 에 계약 시드를 넣고 현행 Node 서버를 띄운다.
 *
 * 왜 이미 떠 있는 서버에 붙지 않는가: 그 서버의 데이터는 그때그때 다르다. 화면 검증에서
 * 확인해야 하는 것들(단가 미등록 모델 · TTL 미상 행 · series 커버리지가 갈리는 사용자 ·
 * 이름 없는 세션)은 `contract/fixtures.mjs` 의 8개 세션이 **일부러** 밟는 함정이다.
 * 데이터가 얇으면 "화면이 뜬다"까지만 확인되고 정작 확인해야 할 자리가 전부 빈칸으로 지나간다.
 *
 * ⚠ contract/ 와 server.js 는 **읽기만** 한다(다른 오너 소유). 여기서는 자식 프로세스로 띄우고
 *   임시 디렉터리에 DB 를 만들 뿐이다.
 */
import { spawn } from 'node:child_process';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO = path.resolve(HERE, '..', '..');

export const ADMIN_TOKEN = 'verify-admin-token-0123456789';
export const INTAKE_TOKEN = 'verify-intake-token-9876543210';
export const KEYWORD_DAYS = 90;

export async function startSeededApi({ port = 4192 } = {}) {
  const dataDir = await mkdtemp(path.join(tmpdir(), 'usage-web-verify-'));
  const child = spawn(process.execPath, ['server.js'], {
    cwd: REPO,
    env: {
      ...process.env,
      USAGE_ADMIN_TOKEN: ADMIN_TOKEN,
      USAGE_INTAKE_TOKEN: INTAKE_TOKEN,
      USAGE_DATA_DIR: dataDir,
      USAGE_PORT: String(port),
      USAGE_HOST: '127.0.0.1',
      USAGE_DB_MODE: 'local',
      // 보존 문구가 실제 숫자로 그려지는지 보려면 기한이 켜져 있어야 한다.
      USAGE_KEYWORD_RETENTION_DAYS: String(KEYWORD_DAYS),
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  });

  const log = [];
  child.stdout.on('data', (b) => log.push(String(b)));
  child.stderr.on('data', (b) => log.push(String(b)));

  const base = `http://127.0.0.1:${port}`;
  const deadline = Date.now() + 20000;
  for (;;) {
    if (child.exitCode !== null) throw new Error(`API 서버가 기동 중 죽었다(code=${child.exitCode})\n${log.join('')}`);
    try {
      const r = await fetch(`${base}/healthz`);
      if (r.ok) break;
    } catch { /* 아직 안 떴다 */ }
    if (Date.now() > deadline) { child.kill('SIGKILL'); throw new Error(`API 기동 시간 초과\n${log.join('')}`); }
    await new Promise((r) => setTimeout(r, 120));
  }

  // 시드는 계약 하네스가 쓰는 것과 같은 것을 쓴다 — 화면 검증과 계약 검증이 같은 데이터를 본다.
  const { SEED, REPLAY, IDENTITY } = await import(path.join(REPO, 'contract', 'fixtures.mjs'));
  const { seed } = await import(path.join(REPO, 'contract', 'harness.mjs'));
  await seed(base, ADMIN_TOKEN, { SEED, REPLAY, IDENTITY });   // 인테이크·귀속교정 둘 다 admin 으로 넣는다(하네스와 같은 경로)

  return {
    base,
    log,
    async stop() {
      // SIGTERM 을 무시하는 경우가 있어 반드시 SIGKILL 로 마무리한다 — 여기서 멈추면
      // 검증 스크립트가 결과를 출력하지 못하고 매달린다(실제로 한 번 그랬다).
      if (child.exitCode === null) {
        child.kill('SIGTERM');
        await new Promise((resolve) => {
          const t = setTimeout(() => { child.kill('SIGKILL'); resolve(); }, 2000);
          child.once('exit', () => { clearTimeout(t); resolve(); });
        });
      }
      await rm(dataDir, { recursive: true, force: true });
    },
  };
}

if (import.meta.url === `file://${process.argv[1]}`) {
  const api = await startSeededApi({ port: Number(process.argv[2] || 4192) });
  console.log(`[seeded-api] ${api.base} · 토큰 ${ADMIN_TOKEN}`);
  process.on('SIGINT', async () => { await api.stop(); process.exit(0); });
}
