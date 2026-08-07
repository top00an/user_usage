'use strict';
/*
 * PostgreSQL 백엔드(deploymentMode=saas) — pg(node-postgres) + pg.Pool.
 * 동결 계약(sqlite 와 동일한 q/tx 인터페이스)을 async 로 구현한다.
 *
 *  - 자리표시자: SQL 본문의 '?' 를 로드 시 '$n' 으로 치환한다(./sql.js 의 toPg).
 *  - tenant/RLS: 매 쿼리/tx 마다 풀에서 커넥션 1개를 checkout 해
 *      BEGIN → SELECT set_config('app.tenant_id', <currentTenant>, true) → 쿼리 → COMMIT
 *    로 묶는다. set_config(..., true)=LOCAL 이라 트랜잭션 종료 시 자동 리셋된다 → 풀 재사용 누수 차단.
 *  - bigint(int8) → Number 정규화: pg 는 int8 을 문자열로 반환하므로 타입 파서를 등록해
 *    id·COUNT 등 정수를 앱 기대(Number)에 맞춘다.
 *  - RETURNING: run 의 lastInsertRowid = rows[0]?.id (INSERT … RETURNING id 필요).
 */
const { toPg } = require('./sql');
const { currentTenant } = require('../tenant');

let typesConfigured = false;
function configureTypes(pg) {
  if (typesConfigured) return;
  // OID 20 = int8(bigint). 앱은 id/집계를 Number 로 다룬다(SQLite 시절과 동일).
  pg.types.setTypeParser(20, (v) => (v == null ? null : Number(v)));
  typesConfigured = true;
}

function normRow(row) { return row == null ? null : row; }

// connectionString 하나에 묶인 독립 백엔드(자체 풀).
function create(connectionString, opts = {}) {
  const pg = require('pg');
  configureTypes(pg);
  const pool = new pg.Pool({
    connectionString,
    max: Number(opts.max || process.env.USAGE_PG_POOL_MAX) || 10,
  });

  // tenant/RLS 를 세팅한 커넥션 1개에서 fn(client) 를 트랜잭션으로 실행.
  async function withTenantClient(fn) {
    const client = await pool.connect();
    try {
      await client.query('BEGIN');
      await client.query("SELECT set_config('app.tenant_id', $1, true)", [currentTenant()]);
      const r = await fn(client);
      await client.query('COMMIT');
      return r;
    } catch (e) {
      try { await client.query('ROLLBACK'); } catch { /* 커넥션 이미 종료 */ }
      throw e;
    } finally {
      client.release();
    }
  }

  // 기존 client(트랜잭션)에 바인딩된 q 인터페이스 — tx 내부에서 t(sql) 로 쓴다.
  function boundQ(client) {
    return (sql) => {
      const text = toPg(sql);
      return {
        async get(...p) { const r = await client.query(text, p); return normRow(r.rows[0] ?? null); },
        async all(...p) { const r = await client.query(text, p); return r.rows.map(normRow); },
        async run(...p) {
          const r = await client.query(text, p);
          return { changes: r.rowCount, lastInsertRowid: r.rows[0]?.id };
        },
      };
    };
  }

  function q(sql) {
    const text = toPg(sql);
    return {
      async get(...p) {
        return withTenantClient(async (c) => { const r = await c.query(text, p); return normRow(r.rows[0] ?? null); });
      },
      async all(...p) {
        return withTenantClient(async (c) => { const r = await c.query(text, p); return r.rows.map(normRow); });
      },
      async run(...p) {
        return withTenantClient(async (c) => {
          const r = await c.query(text, p);
          return { changes: r.rowCount, lastInsertRowid: r.rows[0]?.id };
        });
      },
    };
  }

  async function tx(fn) {
    return withTenantClient((client) => fn(boundQ(client)));
  }

  // 관리/마이그레이션 경로 — tenant 미주입, 멀티 스테이트먼트 원시 실행.
  async function execMulti(sqlText) {
    const client = await pool.connect();
    try { await client.query(sqlText); }
    finally { client.release(); }
  }

  async function close() { await pool.end(); }

  return { dialect: 'pg', q, tx, execMulti, close, pool };
}

// 기본(운영) 싱글턴 — USAGE_PG_URL 을 지연 사용.
let def = null;
function d() {
  if (!def) def = create(process.env.USAGE_PG_URL);
  return def;
}

module.exports = {
  dialect: 'pg',
  create,
  q: (sql) => d().q(sql),
  tx: (fn) => d().tx(fn),
  execMulti: (sqlText) => d().execMulti(sqlText),
  close: async () => { if (def) { await def.close(); def = null; } },
  get pool() { return d().pool; },
};
