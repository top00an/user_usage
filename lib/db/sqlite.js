'use strict';
/*
 * SQLite 백엔드(deploymentMode=local) — node:sqlite 동기 API 를 동결 계약(비동기 q/tx)로 래핑.
 * 단일 파일·단일 커넥션·단일 테넌트(tenant 무시). 자리표시자는 '?' 그대로(치환 없음).
 *
 * 계약:
 *   q(sql) → { async get(...p), async all(...p), async run(...p) }
 *     run 반환: { changes, lastInsertRowid } (Number 로 정규화)
 *     get: 행 없으면 null
 *   tx(async (t) => …)  // t 는 q 와 같은 인터페이스(같은 커넥션). 커밋/롤백 자동. 중첩 금지.
 *   execMulti(sqlText)  // 멀티 스테이트먼트 원시 실행(마이그레이션 러너용)
 */
const { DatabaseSync } = require('node:sqlite');
const fs = require('fs');
const path = require('path');

function num(v) { return typeof v === 'bigint' ? Number(v) : v; }
function normRow(row) {
  if (row == null) return null;
  for (const k of Object.keys(row)) if (typeof row[k] === 'bigint') row[k] = Number(row[k]);
  return row;
}

// dbFile 하나에 묶인 독립 백엔드를 만든다(테스트 격리·명시 파일용).
function create(dbFile) {
  let handle = null;
  function conn() {
    if (!handle) {
      handle = new DatabaseSync(dbFile);
      handle.exec('PRAGMA journal_mode = WAL;');
      // 같은 파일을 여는 커넥션이 둘 이상일 수 있다(서버 프로세스 + 점검용 CLI, 테스트 하네스가
      // 띄운 자식 등). 동시에 쓰기 락을 잡으려 할 때 즉시 SQLITE_BUSY 로 실패하지 않고
      // 최대 5초 대기 후 재시도하도록 완화한다.
      handle.exec('PRAGMA busy_timeout = 5000;');
    }
    return handle;
  }
  function q(sql) {
    return {
      async get(...p) { return normRow(conn().prepare(sql).get(...p) ?? null); },
      async all(...p) { return conn().prepare(sql).all(...p).map(normRow); },
      async run(...p) {
        const info = conn().prepare(sql).run(...p);
        return { changes: num(info.changes), lastInsertRowid: num(info.lastInsertRowid) };
      },
    };
  }
  async function tx(fn) {
    const c = conn();
    c.exec('BEGIN');
    try {
      const r = await fn(q); // sqlite 는 단일 커넥션 → q 가 곧 트랜잭션 스코프
      c.exec('COMMIT');
      return r;
    } catch (e) {
      try { c.exec('ROLLBACK'); } catch { /* 이미 롤백/닫힘 */ }
      throw e;
    }
  }
  function execMulti(sqlText) { conn().exec(sqlText); }
  function close() { if (handle) { try { handle.close(); } catch { /* noop */ } handle = null; } }
  return { dialect: 'sqlite', q, tx, execMulti, close, conn };
}

// 기본(운영) 싱글턴 — USAGE_DATA_DIR 격리 존중, <repo>/data/usage.db.
function defaultDbFile() {
  const DATA = process.env.USAGE_DATA_DIR
    ? path.resolve(process.env.USAGE_DATA_DIR)
    : path.join(__dirname, '..', '..', 'data');
  fs.mkdirSync(DATA, { recursive: true });
  return path.join(DATA, 'usage.db');
}
let def = null;
function d() { if (!def) def = create(defaultDbFile()); return def; }

module.exports = {
  dialect: 'sqlite',
  create,
  q: (sql) => d().q(sql),
  tx: (fn) => d().tx(fn),
  execMulti: (sqlText) => d().execMulti(sqlText),
  close: () => { if (def) { def.close(); def = null; } },
  conn: () => d().conn(),
};
