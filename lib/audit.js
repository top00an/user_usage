'use strict';
/*
 * 감사 로그 — 누가·언제·무엇을 바꿨나.
 *
 * 이 서비스에서 사람이 **데이터를 바꾸는** 경로는 하나뿐이다: 귀속 교정
 * (PUT/DELETE /api/usage/identity). 그 한 동작이 과거 행 수천 개의 username 을 재스탬프하므로,
 * "왜 어제 보던 이름이 오늘 다르지" 를 나중에 답할 수 있어야 한다. 그게 이 테이블의 전부다.
 *
 * ── 설계 결정 두 가지 ────────────────────────────────────────────────
 * ① **자기 테이블을 쓴다.** 다른 시스템의 감사 스키마에 얹지 않는다 — 그 스키마가 바뀌면
 *    사용량과 무관한 이유로 여기가 깨진다.
 * ② **절대 던지지 않는다.** 감사 기록 실패가 본 동작(귀속 교정)을 되돌리게 하면, 사람은 로그를
 *    남기지 않으려고 기능을 피하게 된다. 기록에 실패하면 stderr 로 흘려 최소한 흔적은 남긴다.
 *
 * pg 백엔드에서는 DDL 을 돌리지 않는다(스키마는 migrations/ 소유). remote 모드는 애초에
 * 읽기 전용이라 이 경로가 열리지 않으므로, 그 조합에서 감사 기록은 stderr 폴백으로만 남는다.
 */
const { q, dialect } = require('./db');
const adapter = require('./db/adapter');

const DDL = `
  CREATE TABLE IF NOT EXISTS usage_audit (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    at TEXT NOT NULL,
    actor TEXT,
    action TEXT NOT NULL,
    target TEXT,
    detail TEXT
  );
  CREATE INDEX IF NOT EXISTS idx_usage_audit_at ON usage_audit(at);
`;

async function init() {
  if (dialect !== 'sqlite') return;   // pg: 스키마는 migrations 소유
  adapter.execMulti(DDL);
}

function clip(v, n) { const s = v == null ? '' : String(v); return s.length > n ? s.slice(0, n) : s; }

/*
 * 1건 기록. `extra` 는 구조화 필드(예: { username, moved })이고 JSON 문자열로 접어 넣는다 —
 * 컬럼으로 펴면 새 동작이 생길 때마다 마이그레이션이 필요해진다.
 * 반환은 기록한 레코드다(호출부가 응답에 실을 수 있게).
 */
async function log(actor, action, target, extra) {
  const rec = {
    at: new Date().toISOString(),
    actor: clip(actor, 200) || 'system',
    action: clip(action, 120),
    target: clip(target, 400) || null,
    detail: extra == null ? null : clip(JSON.stringify(extra), 4000),
  };
  try {
    await q('INSERT INTO usage_audit(at,actor,action,target,detail) VALUES(?,?,?,?,?)')
      .run(rec.at, rec.actor, rec.action, rec.target, rec.detail);
  } catch (e) {
    /*
     * 기록 실패가 본 동작을 되돌리지 않는다 — 다만 조용히 사라지게 두지도 않는다.
     *
     * ⚠ `detail` 은 찍지 않는다. 거기에 귀속 대상 계정명이 들어가는데, stderr 는 이 서비스의
     *   보존 정책이 닿지 않는 곳이다(컨테이너 로그 수집기가 그대로 퍼간다). 사고 추적에
     *   필요한 넷(언제·누가·무엇을·어디에)은 아래로 충분하고, 그 이상은 DB 가 살아났을 때
     *   다시 남기면 된다.
     */
    console.error('audit: 기록 실패 —', String((e && e.message) || e),
      `at=${rec.at} actor=${rec.actor} action=${rec.action} target=${rec.target || '-'}`);
  }
  return rec;
}

/* 최근 기록. 조회 실패는 빈 배열 — 감사 화면이 없다고 서비스가 죽을 이유는 없다. */
async function recent(n = 200) {
  const lim = Math.max(1, Math.min(1000, Math.floor(Number(n)) || 200));
  try {
    return (await q('SELECT at, actor, action, target, detail FROM usage_audit ORDER BY id DESC LIMIT ?').all(lim))
      .map((r) => ({
        at: r.at,
        actor: r.actor,
        action: r.action,
        target: r.target,
        detail: r.detail ? JSON.parse(r.detail) : null,
      }));
  } catch { return []; }
}

module.exports = { init, log, recent };
