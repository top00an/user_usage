'use strict';
/*
 * 경량 마이그레이션 러너(제로 의존, ~50줄) — 양 방언 공통.
 *   - schema_migrations(version bigint PK, applied_at text) 버전 테이블.
 *   - migrations/<dialect>/NNNN_*.sql 를 버전 순 적용, 적용본은 기록·스킵(멱등).
 *   - 각 파일은 BEGIN … <body> … INSERT version … COMMIT 을 한 execMulti 호출로 원자 적용.
 *     (pg 는 execMulti 가 커넥션 1개를 checkout → 전체가 하나의 트랜잭션이 된다.)
 * 되돌리기 어려운 작업이다 — 자동 실행 경로에 올리지 않는다(운영자가 명시적으로 돌린다).
 */
const fs = require('fs');
const path = require('path');

function migrationsDir(dialect) {
  return path.join(__dirname, '..', '..', 'migrations', dialect);
}

async function migrate(backend, opts = {}) {
  const dir = opts.dir || migrationsDir(backend.dialect);
  await backend.execMulti(
    'CREATE TABLE IF NOT EXISTS schema_migrations (version bigint PRIMARY KEY, applied_at text)',
  );
  const rows = await backend.q('SELECT version FROM schema_migrations').all();
  const applied = new Set(rows.map((r) => Number(r.version)));

  const files = fs.existsSync(dir)
    ? fs.readdirSync(dir).filter((f) => /^\d+.*\.sql$/i.test(f)).sort()
    : [];

  const ran = [];
  for (const f of files) {
    const version = parseInt(f, 10);
    if (!Number.isInteger(version) || applied.has(version)) continue;
    const body = fs.readFileSync(path.join(dir, f), 'utf8').trim();
    const at = new Date().toISOString(); // applied_at — 내부 생성값(주입 없음, 안전)
    const script = [
      'BEGIN;',
      body ? (body.endsWith(';') ? body : body + ';') : '',
      `INSERT INTO schema_migrations(version, applied_at) VALUES(${version}, '${at}');`,
      'COMMIT;',
    ].filter(Boolean).join('\n');
    try {
      await backend.execMulti(script);
    } catch (e) {
      try { await backend.execMulti('ROLLBACK;'); } catch { /* 이미 롤백됨 */ }
      throw new Error(`migration ${f} failed: ${e.message}`);
    }
    ran.push(version);
  }
  return { dialect: backend.dialect, applied: ran, total: files.length };
}

module.exports = { migrate, migrationsDir };
