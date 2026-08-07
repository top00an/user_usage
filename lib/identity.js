'use strict';
/*
 * 머신 → 계정 매핑 — **신원의 권위를 서버로 옮기는** 계층.
 *
 * 배경(스키마 주석 migrations/pg/0015 가 단일 출처): 팀원 PC 가 보고하는 사용자명은 기본적으로
 * OS 계정명이라 팀에서 쓰는 계정명과 어긋날 수 있다. 어긋난 자리를 고치려고 수집기를 재배포하고 그 PC 가
 * 재설치하는 방식은 반복 비용이 크고, 실제로 같은 누락을 세 번 반복했다.
 *
 * 이 모듈이 있으면 관리자가 화면에서 한 줄 고치는 것으로 끝난다. 클라이언트는 건드리지 않는다.
 *
 * 적용 시점은 **쓰기(인테이크)** 다. 읽기 시점 조인을 쓰면 집계 쿼리를 전부 고쳐야 하고, 새 화면이
 * 생길 때마다 조인을 빠뜨릴 자리가 늘어난다. 인테이크는 한 곳이라 빠뜨릴 자리가 없다.
 * 대신 매핑을 새로 걸 때 **과거 행을 함께 재스탬프**해 소급 적용한다(restamp).
 */
const { q, dialect } = require('./db');
const adapter = require('./db/adapter');

const MACHINE_MAX = 200;
const USER_MAX = 200;
const NOTE_MAX = 500;

const DDL = `
  CREATE TABLE IF NOT EXISTS machine_identity (
    machine TEXT PRIMARY KEY,
    username TEXT NOT NULL,
    note TEXT,
    updated_by TEXT,
    updated_at TEXT
  );
  CREATE INDEX IF NOT EXISTS idx_machine_identity_user ON machine_identity(username);
`;

async function init() {
  if (dialect !== 'sqlite') return; // pg: 스키마는 migrations 소유
  adapter.execMulti(DDL);
}

function now() { return new Date().toISOString(); }
function clip(v, n) { const s = v == null ? '' : String(v).trim(); return s.length > n ? s.slice(0, n) : s; }

/*
 * 이 머신의 귀속 계정 — 없으면 null(호출부가 클라이언트 값을 그대로 쓴다).
 * 인테이크는 절대 throw 하지 않아야 하므로 조회 실패도 null 로 접는다.
 */
async function resolve(machine) {
  const m = clip(machine, MACHINE_MAX);
  if (!m) return null;
  try {
    const r = await q('SELECT username FROM machine_identity WHERE machine=?').get(m);
    return (r && r.username) ? String(r.username) : null;
  } catch { return null; }
}

/* 전체 매핑 — 관리 화면용. */
async function list() {
  return (await q('SELECT machine, username, note, updated_by, updated_at FROM machine_identity ORDER BY machine').all())
    .map((r) => ({
      machine: r.machine, username: r.username, note: r.note || '',
      updatedBy: r.updated_by || '', updatedAt: r.updated_at || '',
    }));
}

/*
 * 이미 쌓인 행을 새 귀속으로 옮긴다.
 *
 * 이게 없으면 매핑을 걸어도 화면의 과거 데이터는 옛 이름으로 남아, 같은 사람이 두 줄로 보인다.
 * 머신으로 범위를 좁히므로 다른 사람의 행은 건드리지 않는다.
 */
async function restamp(machine, username) {
  const m = clip(machine, MACHINE_MAX);
  const u = clip(username, USER_MAX);
  if (!m || !u) return { sessions: 0, counters: 0 };
  let sessions = 0;
  let counters = 0;
  try {
    const r1 = await q('UPDATE usage_sessions SET username=? WHERE machine=? AND (username IS NULL OR username<>?)').run(u, m, u);
    sessions = Number(r1 && r1.changes) || 0;
  } catch { /* best-effort — 매핑 저장 자체는 성공시킨다 */ }
  try {
    const r2 = await q('UPDATE usage_counters SET username=? WHERE machine=? AND (username IS NULL OR username<>?)').run(u, m, u);
    counters = Number(r2 && r2.changes) || 0;
  } catch { /* 동상 */ }
  return { sessions, counters };
}

/* 매핑 설정(신규·갱신) + 소급 적용. */
async function set({ machine, username, note, actor } = {}) {
  const m = clip(machine, MACHINE_MAX);
  const u = clip(username, USER_MAX);
  if (!m) throw new Error('machine 이 필요합니다');
  if (!u) throw new Error('username 이 필요합니다');
  const conflict = dialect === 'pg' ? '(tenant_id, machine)' : '(machine)';
  await q(
    'INSERT INTO machine_identity(machine,username,note,updated_by,updated_at) VALUES(?,?,?,?,?)'
    + ` ON CONFLICT ${conflict} DO UPDATE SET username=excluded.username, note=excluded.note,`
    + ' updated_by=excluded.updated_by, updated_at=excluded.updated_at',
  ).run(m, u, clip(note, NOTE_MAX) || null, clip(actor, USER_MAX) || null, now());
  const moved = await restamp(m, u);
  return { machine: m, username: u, moved };
}

/*
 * 매핑 해제 — 과거 행은 **되돌리지 않는다.**
 * 되돌릴 원래 값이 남아 있지 않고(재스탬프가 덮었다), 해제의 의도는 보통 "앞으로는 클라이언트
 * 값을 쓰겠다"이지 "과거를 옛 이름으로 되돌리겠다"가 아니다. 되돌리려면 다른 매핑을 걸면 된다.
 */
async function remove(machine) {
  const m = clip(machine, MACHINE_MAX);
  if (!m) return false;
  const r = await q('DELETE FROM machine_identity WHERE machine=?').run(m);
  return (Number(r && r.changes) || 0) > 0;
}

/*
 * 아직 매핑이 없는 머신 — 관리 화면이 "고쳐야 할 후보"를 먼저 보여줄 수 있게.
 * 사용량에 등장한 머신 중 매핑 표에 없는 것들.
 */
async function unmapped() {
  return (await q(
    'SELECT s.machine, MIN(s.username) username, COUNT(*) sessions FROM usage_sessions s'
    + ' LEFT JOIN machine_identity m ON m.machine = s.machine'
    + ' WHERE s.machine IS NOT NULL AND m.machine IS NULL'
    + ' GROUP BY s.machine ORDER BY sessions DESC',
  ).all()).map((r) => ({ machine: r.machine, username: r.username || '(미상)', sessions: Number(r.sessions) || 0 }));
}

module.exports = { init, resolve, list, set, remove, restamp, unmapped };
