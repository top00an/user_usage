'use strict';
/*
 * 사용량 텔레메트리 저장소 — 팀원 PC 가 무엇을 얼마나 썼는지의 관측 계층.
 *
 * 세 테이블(스키마 근거와 축 설명은 migrations/pg/0014_usage.sql 주석이 단일 출처):
 *   usage_sessions         세션 단위 토큰 사용량(입력·출력·캐시읽기·캐시생성)
 *   usage_counters         축(kind)별 카운터 — tool|bash|slash|skill|agent|mcp|keyword
 *   usage_recommendations  추천 호출 관측 — 카탈로그 공백 탐지용
 *
 * 품질 판정 신호와 **테이블을 가르는** 것이 이 계층의 전제다. 여기 들어오는 건 관측치이지
 * 판정이 아니다 — 섞으면 "Bash 85회" 같은 관측치가 판정 입력으로 새어 들어간다.
 *
 * 멱등성이 이 계층의 핵심 계약이다. 클라이언트 훅은 세션 절대값을 보내고(델타가 아니다),
 * 여기서는 (세션, 축, 키)로 **덮어쓴다**. 누적(+=)을 쓰면 훅이 두 번 돌 때 값이 두 배가 된다.
 * 훅은 실패해도 재시도하는 best-effort 경로라 중복 전송이 정상 동작에 포함된다.
 *
 * SaaS 데이터 계층(동결 계약) — 접근은 전부 async q()/dialect. sqlite 는 여기 DDL, pg 는 migrations.
 */
const { q, dialect } = require('./db');
const adapter = require('./db/adapter');

// 축 화이트리스트. 클라이언트가 보고하는 값이라 서버가 반드시 좁힌다 — 오타 하나가 집계 축을
// 늘려 화면에 정체불명 행이 생기는 것을 막는다(learning store 의 SIGNAL_SCOPES 와 같은 규율).
const COUNTER_KINDS = Object.freeze(['tool', 'bash', 'slash', 'skill', 'agent', 'mcp', 'keyword']);

// 한 세션이 보낼 수 있는 카운터 행 수 상한. 키워드 축이 사실상 무제한이라 상한이 없으면
// 세션 하나가 테이블을 채운다. 클라이언트도 자르지만 서버가 최종 방어선이다.
const MAX_COUNTERS_PER_SESSION = 400;
const KEY_MAX = 120;

const DDL = `
  CREATE TABLE IF NOT EXISTS usage_sessions (
    session_id TEXT PRIMARY KEY,
    machine TEXT,
    username TEXT,
    project TEXT,
    model TEXT,
    input INTEGER NOT NULL DEFAULT 0,
    output INTEGER NOT NULL DEFAULT 0,
    cache_read INTEGER NOT NULL DEFAULT 0,
    cache_create INTEGER NOT NULL DEFAULT 0,
    web_search INTEGER NOT NULL DEFAULT 0,
    web_fetch INTEGER NOT NULL DEFAULT 0,
    turns INTEGER NOT NULL DEFAULT 0,
    started_at TEXT,
    ended_at TEXT,
    reported_at TEXT
  );
  CREATE INDEX IF NOT EXISTS idx_usage_sessions_at ON usage_sessions(started_at);
  CREATE INDEX IF NOT EXISTS idx_usage_sessions_user ON usage_sessions(username);

  /*
   * 시간 × 모델 버킷 — migrations/pg/0017_usage_series.sql 과 같은 모양이어야 한다.
   * 세션당 1행으로는 "몇 시에 튀었나"도 "어느 모델이 얼마를 썼나"도 답할 수 없다.
   * PK 가 (세션, 시간, 모델)인 것이 멱등성의 근거다 — 절대값 UPSERT 로 덮어쓴다.
   */
  CREATE TABLE IF NOT EXISTS usage_series (
    session_id TEXT NOT NULL,
    hour TEXT NOT NULL,
    model TEXT NOT NULL,
    input INTEGER NOT NULL DEFAULT 0,
    output INTEGER NOT NULL DEFAULT 0,
    cache_read INTEGER NOT NULL DEFAULT 0,
    cache_create INTEGER NOT NULL DEFAULT 0,
    cc_5m INTEGER NOT NULL DEFAULT 0,
    cc_1h INTEGER NOT NULL DEFAULT 0,
    turns INTEGER NOT NULL DEFAULT 0,
    tool_errors INTEGER NOT NULL DEFAULT 0,
    stop_max_tokens INTEGER NOT NULL DEFAULT 0,
    stop_refusal INTEGER NOT NULL DEFAULT 0,
    latency_ms_sum INTEGER NOT NULL DEFAULT 0,
    latency_ms_max INTEGER NOT NULL DEFAULT 0,
    latency_turns INTEGER NOT NULL DEFAULT 0,
    username TEXT,
    machine TEXT,
    project TEXT,
    PRIMARY KEY (session_id, hour, model)
  );
  CREATE INDEX IF NOT EXISTS idx_usage_series_hour ON usage_series(hour);
  CREATE INDEX IF NOT EXISTS idx_usage_series_model ON usage_series(model, hour);
  CREATE INDEX IF NOT EXISTS idx_usage_series_user ON usage_series(username, hour);

  CREATE TABLE IF NOT EXISTS usage_counters (
    session_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    key TEXT NOT NULL,
    count INTEGER NOT NULL DEFAULT 0,
    day TEXT,
    username TEXT,
    machine TEXT,
    PRIMARY KEY (session_id, kind, key)
  );
  CREATE INDEX IF NOT EXISTS idx_usage_counters_kind ON usage_counters(kind, key);
  CREATE INDEX IF NOT EXISTS idx_usage_counters_day ON usage_counters(day, kind);

  CREATE TABLE IF NOT EXISTS usage_recommendations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    goal_tokens TEXT,
    agent TEXT,
    skills TEXT,
    score REAL,
    source TEXT,
    username TEXT,
    at TEXT
  );
  CREATE INDEX IF NOT EXISTS idx_usage_reco_at ON usage_recommendations(at);
  CREATE INDEX IF NOT EXISTS idx_usage_reco_score ON usage_recommendations(score);
`;

// 기존 DB에 컬럼을 멱등 추가한다(CREATE TABLE IF NOT EXISTS 는 기존 테이블에 새 컬럼을 안 넣는다).
// local(sqlite) 전용 — init 의 sqlite 가드 아래에서만 부른다(pg 는 migrations 소유).
// learning/store.js·projects.js 의 ensureColumn 과 같은 패턴이다.
function ensureColumn(table, column, decl) {
  try {
    const d = adapter.conn(); // sqlite 원시 핸들
    const cols = d.prepare(`PRAGMA table_info(${table})`).all().map((r) => r.name);
    if (!cols.includes(column)) d.exec(`ALTER TABLE ${table} ADD COLUMN ${column} ${decl}`);
  } catch { /* best-effort — 실패해도 부팅을 막지 않는다 */ }
}

async function init() {
  if (dialect !== 'sqlite') return; // pg: 스키마는 migrations 소유
  adapter.execMulti(DDL);
  // 세션 종료 시각 — 신버전 수집기만 보낸다. 구 행은 NULL 로 남아 "모른다"가 그대로 보인다.
  ensureColumn('usage_sessions', 'ended_at', 'TEXT');
  // pg 는 migrations/pg/0026_usage_no_ts_turns.sql 이 같은 컬럼을 소유한다(양 방언 동기).
  ensureColumn('usage_sessions', 'no_ts_turns', 'INTEGER');
}

function now() { return new Date().toISOString(); }
function clip(v, n) { const s = v == null ? '' : String(v); return s.length > n ? s.slice(0, n) : s; }
function int(v) { const n = Math.floor(Number(v)); return Number.isFinite(n) && n > 0 ? n : 0; }
function dayOf(iso) {
  const s = String(iso || '');
  return /^\d{4}-\d{2}-\d{2}/.test(s) ? s.slice(0, 10) : now().slice(0, 10);
}

/*
 * 조사 제거 — 키워드 축과 추천 공백 토큰이 **같은 어휘**를 쓰게 하는 정규화.
 *
 * 클라이언트 수집기의 stripParticle 과 같은 규칙이다.
 * 규칙이 두 곳에 있는 이유는 tokenize 와 같다 — 훅은 팀원 PC 에서 도는 별도 프로세스라 서버 코드를
 * import 할 수 없다. 한쪽만 고치면 "키워드 상위"와 "추천 공백"이 다른 어휘로 갈려 나란히 못 놓는다.
 *
 * 추천 매칭 쪽 토큰화에는 이 규칙을 넣지 않는다 — 거기는 매칭 규칙이고 조사를 떼면 추천 품질이
 * 조용히 바뀐다.
 */
const PARTICLES = ['으로부터', '에게서', '으로', '에서', '부터', '까지', '에게', '한테', '보다', '처럼', '마다',
  '이라', '라는', '라고', '이며', '와의', '과의', '들이', '들을', '들은',
  '을', '를', '이', '가', '은', '는', '에', '의', '로', '와', '과', '도', '만'];
// 3글자 토큰에는 '이'·'가'를 떼지 않는다 — 명사의 끝소리와 구분이 안 된다('고양이'→'고양',
// '어린이'→'어린' 같은 오절단). 4글자 이상은 어간이 충분히 남아 안전하다.
const PARTICLES_SHORT = PARTICLES.filter((p) => p !== '이' && p !== '가');
function stripParticle(t) {
  const s = String(t || '');
  if (!/^[가-힣]{3,}$/.test(s)) return s;
  for (const p of (s.length >= 4 ? PARTICLES : PARTICLES_SHORT)) {
    if (s.length - p.length >= 2 && s.endsWith(p)) return s.slice(0, -p.length);
  }
  return s;
}

/*
 * 세션 사용량 UPSERT — 같은 session_id 재전송은 덮어쓴다(멱등).
 * 방언마다 UPSERT 문법이 달라 여기서 가른다. sqlite/pg 둘 다 ON CONFLICT 를 지원하지만
 * 대상 컬럼 목록이 다르다(pg 는 PK 가 (tenant_id, session_id)).
 */
async function sessionUpsert(s = {}) {
  const id = clip(s.sessionId, 120);
  if (!id) return false;
  const cols = {
    machine: clip(s.machine, 200) || null,
    username: clip(s.username, 200) || null,
    project: clip(s.project, 200) || null,
    model: clip(s.model, 120) || null,
    input: int(s.input),
    output: int(s.output),
    cache_read: int(s.cacheRead),
    cache_create: int(s.cacheCreate),
    web_search: int(s.webSearch),
    web_fetch: int(s.webFetch),
    turns: int(s.turns),
    started_at: clip(s.startedAt, 40) || null,
    // 구버전 수집기는 안 보낸다 — 그 경우 NULL 로 남아 "모른다"가 화면에 그대로 보인다.
    ended_at: clip(s.endedAt, 40) || null,
    // 시각이 없어 series 에 못 올린 턴 수(D3). NULL = 구버전 수집기라 모른다 · 0 = 전 턴에 시각이 있었다.
    // int() 로 접지 않는다 — 그러면 NULL 이 0 이 되어 "모른다"가 "없다"로 바뀐다.
    no_ts_turns: s.noTsTurns == null ? null : int(s.noTsTurns),
    reported_at: now(),
  };
  const names = Object.keys(cols);
  const conflict = dialect === 'pg' ? '(tenant_id, session_id)' : '(session_id)';
  const sql = `INSERT INTO usage_sessions(session_id,${names.join(',')})`
    + ` VALUES(${new Array(names.length + 1).fill('?').join(',')})`
    + ` ON CONFLICT ${conflict} DO UPDATE SET ${names.map((n) => `${n}=excluded.${n}`).join(',')}`;
  await q(sql).run(id, ...names.map((n) => cols[n]));
  return true;
}

/*
 * 시간 × 모델 버킷 일괄 UPSERT. rows: [{hour, model, input, output, ...}]
 *
 * 멱등성은 sessionUpsert 와 같은 근거다 — 수집기가 트랜스크립트를 다시 읽어 **절대값**을
 * 보내므로 PK(세션, 시간, 모델)로 덮어쓴다. 누적(+=)을 쓰면 훅이 두 번 도는 순간 두 배가 된다.
 * 트랜스크립트는 append-only 라 한 번 생긴 버킷이 사라지지 않으므로, 덮어쓰기만으로 최신 상태와
 * 일치한다(지워진 버킷을 정리할 필요가 없다).
 *
 * 한 행이 깨져도 나머지는 넣는다. 시계열이 전부-아니면-전무일 이유가 없다.
 */
const HOUR_RE = /^\d{4}-\d{2}-\d{2}T\d{2}$/;
const MAX_SERIES_PER_SESSION = 200;

async function seriesUpsert({ sessionId, username, machine, project, rows } = {}) {
  const sid = clip(sessionId, 120);
  if (!sid || !Array.isArray(rows)) return 0;

  const conflict = dialect === 'pg'
    ? '(tenant_id, session_id, hour, model)'
    : '(session_id, hour, model)';

  let n = 0;
  for (const r of rows.slice(0, MAX_SERIES_PER_SESSION)) {
    if (!r) continue;
    const hour = clip(r.hour, 13);
    if (!HOUR_RE.test(hour)) continue;      // 시간 라벨이 아니면 시계열에 올릴 자리가 없다
    const model = clip(r.model, 120) || '(미상)';
    const cols = {
      input: int(r.input),
      output: int(r.output),
      cache_read: int(r.cacheRead),
      cache_create: int(r.cacheCreate),
      cc_5m: int(r.cc5m),
      cc_1h: int(r.cc1h),
      turns: int(r.turns),
      tool_errors: int(r.toolErrors),
      stop_max_tokens: int(r.stopMaxTokens),
      stop_refusal: int(r.stopRefusal),
      latency_ms_sum: int(r.latencyMsSum),
      latency_ms_max: int(r.latencyMsMax),
      latency_turns: int(r.latencyTurns),
      username: clip(username, 200) || null,
      machine: clip(machine, 200) || null,
      project: clip(project, 200) || null,
    };
    const names = Object.keys(cols);
    const sql = `INSERT INTO usage_series(session_id,hour,model,${names.join(',')})`
      + ` VALUES(${new Array(names.length + 3).fill('?').join(',')})`
      + ` ON CONFLICT ${conflict} DO UPDATE SET ${names.map((c) => `${c}=excluded.${c}`).join(',')}`;
    try {
      await q(sql).run(sid, hour, model, ...names.map((c) => cols[c]));
      n += 1;
    } catch { /* 한 행 실패가 나머지를 막지 않는다 */ }
  }
  return n;
}

/*
 * 카운터 일괄 UPSERT. rows: [{kind, key, count}]
 * 축·키·개수를 여기서 최종적으로 좁힌다 — 클라이언트 검증을 신뢰하지 않는다.
 * 한 행이 깨져도 나머지는 넣는다(텔레메트리가 전부-아니면-전무일 이유가 없다).
 */
async function countersUpsert({ sessionId, username, machine, startedAt, rows } = {}) {
  const sid = clip(sessionId, 120);
  if (!sid || !Array.isArray(rows)) return 0;
  const day = dayOf(startedAt);
  const user = clip(username, 200) || null;
  const mach = clip(machine, 200) || null;
  const conflict = dialect === 'pg' ? '(tenant_id, session_id, kind, key)' : '(session_id, kind, key)';
  const sql = 'INSERT INTO usage_counters(session_id,kind,key,count,day,username,machine)'
    + ' VALUES(?,?,?,?,?,?,?)'
    + ` ON CONFLICT ${conflict} DO UPDATE SET count=excluded.count, day=excluded.day,`
    + ' username=excluded.username, machine=excluded.machine';
  let n = 0;
  for (const r of rows.slice(0, MAX_COUNTERS_PER_SESSION)) {
    if (!r || !COUNTER_KINDS.includes(r.kind)) continue;
    const key = clip(r.key, KEY_MAX).trim();
    const count = int(r.count);
    if (!key || !count) continue;
    try { await q(sql).run(sid, r.kind, key, count, day, user, mach); n += 1; } catch { /* 행 단위 best-effort */ }
  }
  return n;
}

/*
 * 서버측 카운터 원자적 +1.
 *
 * 클라이언트 보고(countersUpsert)는 세션 절대값을 **덮어쓰지만**, 서버가 직접 세는 축(mcp)은
 * 이벤트마다 하나씩 늘어난다. 읽고-고쳐-쓰면 동시 요청에서 카운트가 샌다(installcode 의
 * `WHERE used < max_uses` 와 같은 이유) — 한 문장 UPSERT 로 DB 가 더하게 한다.
 *
 * 세션 자리에 날짜를 넣어 일별 누적 행 하나로 유지한다(PK 가 (세션, 축, 키)라 세션 없이는 못 넣는다).
 */
async function counterBump({ kind, key, day, username = null, machine = null } = {}) {
  if (!COUNTER_KINDS.includes(kind)) return false;
  const k = clip(key, KEY_MAX).trim();
  if (!k) return false;
  const d = dayOf(day);
  const sid = `server-${d}`;
  const conflict = dialect === 'pg' ? '(tenant_id, session_id, kind, key)' : '(session_id, kind, key)';
  await q(
    'INSERT INTO usage_counters(session_id,kind,key,count,day,username,machine) VALUES(?,?,?,1,?,?,?)'
    + ` ON CONFLICT ${conflict} DO UPDATE SET count = usage_counters.count + 1`,
  ).run(sid, kind, k, d, clip(username, 200) || null, clip(machine, 200) || null);
  return true;
}

/* 추천 관측 1건. 목표 **원문은 받지 않는다** — 호출부가 토큰으로 바꿔 넘긴다. */
async function recommendationAdd({ goalTokens, agent, skills, score, source, username } = {}) {
  await q('INSERT INTO usage_recommendations(goal_tokens,agent,skills,score,source,username,at)'
    + ' VALUES(?,?,?,?,?,?,?)').run(
    clip(Array.isArray(goalTokens) ? goalTokens.join(' ') : goalTokens, 600) || null,
    clip(agent, 200) || null,
    clip(Array.isArray(skills) ? skills.join(',') : skills, 400) || null,
    Number.isFinite(Number(score)) ? Number(score) : 0,
    clip(source, 40) || null,
    clip(username, 200) || null,
    now(),
  );
}

// ── 집계(읽기) ──

/* 일별 토큰 사용량 — 비용 추세. 캐시 읽기를 반드시 따로 돌려준다(합치면 비용이 왜곡된다). */
async function usageByDay(days = 30) {
  const lim = Math.max(1, Math.min(365, Math.floor(Number(days)) || 30));
  return (await q(
    'SELECT substr(started_at,1,10) d, SUM(input) i, SUM(output) o, SUM(cache_read) cr,'
    + ' SUM(cache_create) cc, COUNT(*) n FROM usage_sessions'
    + ' WHERE started_at IS NOT NULL GROUP BY d ORDER BY d DESC LIMIT ?',
  ).all(lim)).map((r) => ({
    day: r.d, input: Number(r.i) || 0, output: Number(r.o) || 0,
    cacheRead: Number(r.cr) || 0, cacheCreate: Number(r.cc) || 0, sessions: Number(r.n) || 0,
  }));
}

/* 사람별 사용량 — 좌석·비용 배분의 근거. */
async function usageByUser() {
  return (await q(
    'SELECT COALESCE(NULLIF(username,\'\'),\'(미상)\') u, SUM(input) i, SUM(output) o,'
    + ' SUM(cache_read) cr, SUM(cache_create) cc, SUM(turns) t, COUNT(*) n'
    + ' FROM usage_sessions GROUP BY u ORDER BY o DESC',
  ).all()).map((r) => ({
    username: r.u, input: Number(r.i) || 0, output: Number(r.o) || 0,
    cacheRead: Number(r.cr) || 0, cacheCreate: Number(r.cc) || 0,
    turns: Number(r.t) || 0, sessions: Number(r.n) || 0,
  }));
}

/*
 * 수집 커버리지 — 발신처(머신)별 마지막 보고 시각. "왜 데이터가 없나"를 화면이 답하게 한다.
 * 신수집기(시간 버킷=usage_series)를 보내는 머신 집합도 함께 표시해 품질축 커버리지를 설명한다.
 * machine→username 은 identity 매핑으로 사실상 1:1 이라 대표값 하나(MAX)로 충분하다.
 */
async function reporterCoverage() {
  const rows = await q(
    "SELECT COALESCE(NULLIF(machine,''),'(미상)') m, MAX(COALESCE(username,'')) u,"
    + ' COUNT(*) n, MAX(reported_at) last_rep, MAX(started_at) last_start'
    + ' FROM usage_sessions GROUP BY m ORDER BY last_rep DESC',
  ).all();
  const withSeries = new Set((await q(
    "SELECT DISTINCT COALESCE(NULLIF(machine,''),'(미상)') m FROM usage_series",
  ).all()).map((r) => r.m));
  return rows.map((r) => ({
    machine: r.m,
    username: r.u || '',
    sessions: Number(r.n) || 0,
    lastReportedAt: r.last_rep || '',
    lastStartedAt: r.last_start || '',
    sendsSeries: withSeries.has(r.m),
  }));
}

/*
 * ── 모델별 집계 ───────────────────────────────────────────────────────
 *
 * ⚠ `usage_sessions.model` 은 모델 축이 **아니다** — 그 세션에서 가장 많이 쓴 모델 1개다
 * (0014:25 주석, 수집기 team-usage.js 의 turn-argmax). 그것으로 GROUP BY 하면 모델이 섞인
 * 세션의 토큰이 통째로 한 칸에 들어간다. 실측(한 사용자 1,133세션): opus-4-8 행이
 * output +313,633 을 얻고 fable-5·sonnet-5·opus-5 가 정확히 그만큼 잃었다. **총합은 맞는데
 * 행이 틀린 것**이라 사람에게는 "서버가 더 최신인데 값이 적다"(=유실)로 보였다. 유실이 아니다.
 *
 * 그래서 축을 셋으로 갈라 더한다:
 *   ① series 가 있는 세션 → `usage_series`(PK 에 model 이 있다)의 모델별 **정확한** 값.
 *      과거분까지 소급 교정된다 — 새 수집이 필요 없다(pruneSeries 는 호출부가 없어 온전하다).
 *   ② series 가 **없는** 세션 → 종전대로 세션 최빈 모델에 귀속한다. **버리지 않는다.**
 *   ③ ①의 세션 중 series 가 덮지 못한 잔여(시각이 파싱되지 않아 버킷이 안 생긴 턴) → 역시
 *      최빈 모델에 귀속한다.
 *
 * ②를 버리지 않는 이유: series 커버리지가 사람마다 다르다(실측: 91.3% · 100% · **2.2%**).
 * series 로 갈아타기만 하면 커버리지 2.2% 인 사람의 모델별이 97.8%
 * 사라진다 — 오귀속을 고치면서 더 큰 거짓말을 만드는 셈이다. 그리고 짧은 세션은 대개 단일
 * 모델이라(중앙값 2턴) 그 귀속이 틀렸다는 보장도 없다.
 *
 * ③이 없으면 **총합이 줄어든다.** ①+②+③ = `usage_sessions` 총합이어야 한다 — 같은 화면의
 * totals·사용자별 카드가 그 값이고, 모델별만 작으면 그게 다시 "유실"로 읽힌다.
 *
 * 그리고 ②③의 몫을 `fromSession` 으로 **밝힌다**(①은 `fromSeries`). 밝히지 않으면 근사를
 * 정확한 값으로 위장하는 것이고, 그게 이번 결함의 본질이었다.
 *
 * 응답은 **더하기만** 한다 — 기존 필드(model·input·output·cacheRead·cacheCreate·sessions)의
 * 이름과 타입은 그대로다. 새 필드를 모르는 화면은 종전대로 그린다.
 */
function zeroShare() {
  return { input: 0, output: 0, cacheRead: 0, cacheCreate: 0, sessions: 0 };
}
function addShare(dst, r) {
  dst.input += Number(r.i) || 0;
  dst.output += Number(r.o) || 0;
  dst.cacheRead += Number(r.cr) || 0;
  dst.cacheCreate += Number(r.cc) || 0;
}

// 세션당 series 합계. ①의 잔여(③)와 커버리지를 같은 정의로 계산하려고 한 곳에 둔다.
const SERIES_PER_SESSION = 'SELECT session_id sid, SUM(input) i, SUM(output) o,'
  + ' SUM(cache_read) cr, SUM(cache_create) cc FROM usage_series GROUP BY 1';

async function usageByModel() {
  const [exact, fallback, residual] = await Promise.all([
    /*
     * ① series 의 모델별 정확값. 세션 행이 없는 고아 버킷은 제외한다 — 그것까지 더하면
     * 총합이 usage_sessions 를 넘어서 이 화면 안에서 두 카드가 어긋난다.
     */
    q(
      "SELECT COALESCE(NULLIF(x.model,''),'(미상)') m, SUM(x.input) i, SUM(x.output) o,"
      + ' SUM(x.cache_read) cr, SUM(x.cache_create) cc, COUNT(DISTINCT x.session_id) n'
      + ' FROM usage_series x'
      + ' WHERE EXISTS (SELECT 1 FROM usage_sessions s WHERE s.session_id = x.session_id)'
      + ' GROUP BY 1',
    ).all(),
    // ② series 가 없는 세션 — 종전 질의 그대로. 이 몫이 곧 "세션 최빈 모델 기준" 이다.
    q(
      "SELECT COALESCE(NULLIF(s.model,''),'(미상)') m, SUM(s.input) i, SUM(s.output) o,"
      + ' SUM(s.cache_read) cr, SUM(s.cache_create) cc, COUNT(*) n'
      + ' FROM usage_sessions s'
      + ' WHERE NOT EXISTS (SELECT 1 FROM usage_series x WHERE x.session_id = s.session_id)'
      + ' GROUP BY 1',
    ).all(),
    /*
     * ③ 잔여 = 세션 행 − series 합. 축마다 CASE 로 0 에서 끊는다(음수 금지) —
     * GREATEST/MAX(a,b) 는 방언이 갈려 쓰지 않는다. 음수가 나오는 세션(series 가 세션 행보다
     * 큰 경우)은 정상이 아니므로 usageModelAxis().overSessions 로 따로 센다. 조용히 덮지 않는다.
     */
    q(
      "SELECT COALESCE(NULLIF(s.model,''),'(미상)') m,"
      + ' SUM(CASE WHEN s.input > x.i THEN s.input - x.i ELSE 0 END) i,'
      + ' SUM(CASE WHEN s.output > x.o THEN s.output - x.o ELSE 0 END) o,'
      + ' SUM(CASE WHEN s.cache_read > x.cr THEN s.cache_read - x.cr ELSE 0 END) cr,'
      + ' SUM(CASE WHEN s.cache_create > x.cc THEN s.cache_create - x.cc ELSE 0 END) cc'
      + ' FROM usage_sessions s'
      + ` JOIN (${SERIES_PER_SESSION}) x ON x.sid = s.session_id`
      + ' GROUP BY 1',
    ).all(),
  ]);

  const out = new Map();
  const at = (m) => {
    const key = m || '(미상)';
    if (!out.has(key)) out.set(key, { model: key, fromSeries: zeroShare(), fromSession: zeroShare() });
    return out.get(key);
  };
  for (const r of exact) {
    const row = at(r.m);
    addShare(row.fromSeries, r);
    row.fromSeries.sessions += Number(r.n) || 0;
  }
  for (const r of fallback) {
    const row = at(r.m);
    addShare(row.fromSession, r);
    row.fromSession.sessions += Number(r.n) || 0;
  }
  for (const r of residual) {
    // 세션 수는 더하지 않는다 — 이 세션들은 ①에서 이미 세었다(같은 세션을 두 번 세면 안 된다).
    addShare(at(r.m).fromSession, r);
  }

  return [...out.values()].map((r) => ({
    model: r.model,
    input: r.fromSeries.input + r.fromSession.input,
    output: r.fromSeries.output + r.fromSession.output,
    cacheRead: r.fromSeries.cacheRead + r.fromSession.cacheRead,
    cacheCreate: r.fromSeries.cacheCreate + r.fromSession.cacheCreate,
    // 이 모델이 등장한 세션 수. 모델이 섞인 세션은 여러 행에 세어지므로 **열의 합이 총
    // 세션 수를 넘을 수 있다** — 화면이 그 사실을 말한다(종전에는 세션당 1행이라 합이 맞았다).
    sessions: r.fromSeries.sessions + r.fromSession.sessions,
    fromSeries: r.fromSeries,
    fromSession: r.fromSession,
  })).sort((a, b) => (b.output - a.output) || String(a.model).localeCompare(String(b.model)));
}

/*
 * 모델 축의 커버리지 — "이 표의 어디까지가 정확한 값인가"를 화면이 말하게 하는 근거.
 *
 * 사람별로 내보내는 이유: 커버리지가 사람마다 갈리고(낮은 사람은 2.2%), 그 사람의 모델별 값이
 * 사실상 전부 세션 최빈 모델 기준이라는 것은 **그 사람 행을 볼 때** 알아야 하는 사실이다.
 * 지금은 DB 를 열어야만 알 수 있다.
 *
 * **원인은 말하지 않는다.** 커버리지가 낮다는 사실과 그것이 뜻하는 것(값의 근거가 무엇인가)만
 * 낸다 — 낮은 이유는 그 PC 쪽 사실이고 서버는 모른다.
 *
 * username 은 `usage_sessions` 쪽을 쓴다. `usage_series.username` 은 identity 재스탬프가
 * 건드리지 않아 매핑 후 갈릴 수 있다 — 세션 행을 기준으로 잡아야 그 결함에 오염되지 않는다.
 */
async function usageModelAxis() {
  const rows = await q(
    "SELECT COALESCE(NULLIF(s.username,''),'(미상)') u, COUNT(*) n,"
    + ' SUM(CASE WHEN x.sid IS NULL THEN 0 ELSE 1 END) w,'
    + ' SUM(CASE WHEN x.sid IS NOT NULL AND (s.input < x.i OR s.output < x.o'
    + ' OR s.cache_read < x.cr OR s.cache_create < x.cc) THEN 1 ELSE 0 END) ov,'
    /*
     * series 가 **왜** 없는지의 근거(D3). 시각이 없어 버킷에 못 올린 턴 수를 수집기가 보낸다.
     * 커버리지가 낮은 것만 보면 사람은 "보고가 안 온다" 로 읽는다 — 보고는 오고 있고 시각이
     * 없을 뿐이다. 그 구분이 없으면 엉뚱한 데(토큰·서버 주소)를 판다.
     * NULL(구버전 수집기)은 세지 않는다 — 모르는 것을 0 으로 접으면 "시각 문제 없음" 이 된다.
     */
    + ' SUM(COALESCE(s.no_ts_turns,0)) nts,'
    + ' SUM(CASE WHEN s.no_ts_turns IS NULL THEN 1 ELSE 0 END) ntsUnknown'
    + ' FROM usage_sessions s'
    + ` LEFT JOIN (${SERIES_PER_SESSION}) x ON x.sid = s.session_id`
    + ' GROUP BY 1 ORDER BY 2 DESC',
  ).all();
  const users = rows.map((r) => ({
    username: r.u,
    sessions: Number(r.n) || 0,
    withSeries: Number(r.w) || 0,
    // 시각이 없어 series 에 못 올린 턴 수 · 그 값을 안 보낸 세션 수(구버전 수집기).
    noTsTurns: Number(r.nts) || 0,
    noTsUnknown: Number(r.ntsunknown ?? r.ntsUnknown) || 0,
  }));
  return {
    sessions: users.reduce((a, r) => a + r.sessions, 0),
    withSeries: users.reduce((a, r) => a + r.withSeries, 0),
    users,
    // series 합이 세션 행보다 큰 세션 수. 정상이면 0 이다(수집기는 같은 실행에서 두 값을
    // 같은 원본으로 만든다). >0 이면 그만큼이 ③에서 0 으로 끊겼다는 뜻 — 조용히 두지 않는다.
    overSessions: rows.reduce((a, r) => a + (Number(r.ov) || 0), 0),
  };
}

/* 축별 상위 키 — 명령·키워드·자산 사용 순위. */
async function topKeys(kind, limit = 20) {
  if (!COUNTER_KINDS.includes(kind)) return [];
  const lim = Math.max(1, Math.min(200, Math.floor(Number(limit)) || 20));
  return (await q(
    'SELECT key, SUM(count) c, COUNT(DISTINCT session_id) s, COUNT(DISTINCT username) u'
    + ' FROM usage_counters WHERE kind=? GROUP BY key ORDER BY c DESC LIMIT ?',
  ).all(kind, lim)).map((r) => ({
    key: r.key, count: Number(r.c) || 0, sessions: Number(r.s) || 0, users: Number(r.u) || 0,
  }));
}

/*
 * ── 게이트로 보이는 선두 실행파일 ─────────────────────────────────────────
 *
 * 클라이언트 수집기가 "검증 게이트"로 인정하는 명령들의 **선두 실행파일**만 모은 목록이다.
 * 이 축(usage_counters kind='bash')에는 설계상 **인자가 없다** — 그래서 `npm` 한 건이
 * `npm test`(게이트)인지 `npm install`(아님)인지 여기서는 **가릴 수 없다.** 그러니 결론이 아니라
 * **후보**라고만 말한다(화면 문구도 "게이트로 보이는 명령"이다). 인자를 저장하는 쪽으로 바꾸는
 * 선택지는 두지 않는다 — 이 축의 프라이버시 계약(집계만·인자 없음)이 그 값보다 크다.
 *
 * 왜 서버에 이 목록이 필요한가(2026-08-05): "동기화는 되는데 학습 보고가 없는 PC" 앞에서
 * 사람이 물을 다음 질문은 언제나 **"배선이 고장 났나, 아니면 게이트를 안 돌리나"** 다. 그 둘은
 * 대응이 완전히 다르다(전자는 우리가 고칠 결함, 후자는 그 PC 의 작업 방식). 훅이 보고하지 않아도
 * 사용량 텔레메트리는 같은 수집기의 다른 축으로 이미 도착해 있으므로, 그 축을 대조하면 답할 수 있다.
 *
 * 이 목록이 수집기 쪽 판정 규칙과 어긋나면 화면이 틀린 말을 한다 — 한쪽만 고치지 않는다.
 *
 * ── 확실 / 불확실을 가른다(2026-08-05 오전, 화면이 과장한 자리) ──────────
 * 첫 판에서 둘을 한 통에 넣었더니 라이브에서 이렇게 떴다:
 *   `게이트로 보이는 명령 168건 · 보고 0`  (내역: python.exe 122 · python 36 · node 5)
 * 빨간 배지는 "우리가 고칠 결함"이라는 뜻인데, 저 셋은 **인자 없이는 게이트인지 알 수 없는**
 * 인터프리터다(`python train.py` 도 python 이다). 근거 없는 단정이 경고를 켠 것이다 — 이 카드가
 * 처음부터 경계한 그 실수(화면이 원인을 추측하면 사람이 엉뚱한 데를 판다)를 우리가 저질렀다.
 *
 *   CERTAIN   그 이름만으로 게이트다(pytest·jest·eslint·ruff …) → 빨간 판정의 유일한 근거
 *   AMBIGUOUS 하위명령·플래그가 있어야 게이트다(python·npm·go·make …) → **세되, 판정하지 않는다**
 */
// 이름만으로 게이트인 러너 — 이것만이 "게이트는 도는데 보고가 0" 판정의 근거다.
const GATE_LEAD_CERTAIN = Object.freeze([
  'pytest', 'py.test', 'tox', 'nose', 'nose2',
  'jest', 'vitest', 'mocha', 'ava', 'cypress', 'playwright',
  'eslint', 'biome', 'tsc',
  'ruff', 'mypy', 'flake8', 'pylint', 'pyright',
  'rspec', 'phpunit', 'Invoke-Pester',
]);
/*
 * 하위명령·플래그가 있어야 게이트인 것들. `npm`(test vs install) · `python`(-m pytest vs script.py) ·
 * `go`(test vs run) · `black`/`prettier`(--check 여야 게이트) · `coverage`(run -m pytest) 처럼
 * **인자가 판정을 가르는데 이 축엔 인자가 없다.** 그래서 세기만 하고 판정에는 쓰지 않는다.
 */
const GATE_LEAD_AMBIGUOUS = Object.freeze([
  'npm', 'pnpm', 'yarn', 'bun', 'npx', 'bunx', 'node',
  'python', 'python3', 'py', 'uv', 'poetry', 'pipenv', 'rye',
  'go', 'cargo', 'rails', 'gradle', 'mvn', 'dotnet', 'make',
  'black', 'prettier', 'coverage',
]);
const GATE_LEAD_KEYS = Object.freeze([...GATE_LEAD_CERTAIN, ...GATE_LEAD_AMBIGUOUS]);
const CERTAIN_SET = new Set(GATE_LEAD_CERTAIN.map((k) => k.toLowerCase()));
const GATE_LEAD_SET = new Set(GATE_LEAD_KEYS.map((k) => k.toLowerCase()));
// 윈도우 확장자는 접어서 본다(`npm.cmd`·`python.exe` 는 같은 실행파일이다 — 수집기와 같은 규율).
function normLeadKey(key) {
  return String(key || '').toLowerCase().replace(/\.(?:exe|cmd|bat|ps1|com)$/, '');
}
function isGateLeadKey(key) { return GATE_LEAD_SET.has(normLeadKey(key)); }
function isCertainGateLeadKey(key) { return CERTAIN_SET.has(normLeadKey(key)); }

/*
 * 활동 대조의 기본 창 — **14일**.
 *
 * 왜 창이 필요한가(2026-08-05 라이브에서 배운 것): 전 기간 누계로 세면 화면이 **고쳐진 뒤에도
 * 옛 데이터로 빨간불을 켠다.** 하네스 훅 수정이 그 PC 에 10:07 에 도착했는데, 표는 그 전 몇 주치
 * 명령까지 합쳐 "게이트 168건인데 보고 0" 이라고 말했다 — 판정의 근거가 판정 대상 기간 밖에 있었다.
 * "지금도 그런가"를 묻는 카드에는 최근 창이 맞다. 14일인 이유: 주말·휴가 한 번을 흡수하면서도
 * 몇 주 전 습관이 오늘의 판정을 오염시키지 않는 가장 짧은 길이.
 */
const ACTIVITY_WINDOW_DAYS = 14;

/*
 * 머신별 활동 — "그 PC 에서 Claude 가 돌기는 하는가, 게이트를 부르기는 하는가"(최근 창 기준).
 *
 * "보고가 안 온다" 진단의 대조축이다(index.js 의 machineActivity 가 이 값을 감싼다). 반환:
 *   { '<machine>': { sessions, lastSessionAt, bash, gateTotal, gateKeys, certainTotal, certainKeys, sinceDay, windowDays } }
 *     gateTotal/gateKeys      후보 전체(확실 + 불확실) — 사람이 내역을 보고 판단하는 축
 *     certainTotal/certainKeys 이름만으로 게이트인 것 — **빨간 판정의 유일한 근거**
 *
 * 세 상태를 가른다 — 이 구분이 이 함수의 존재 이유다:
 *   · 머신 키가 아예 없다           → 그 PC 에서 **사용량 보고조차 오지 않는다**(수집기 설치 자체를 의심한다)
 *   · sessions>0 · certainTotal=0  → 확실한 러너 호출이 창 안에 없다(작업 방식일 수 있다 — 단정 금지)
 *   · sessions>0 · certainTotal>0  → 러너는 도는데 **학습 보고가 없다** = 우리가 고칠 결함
 *
 * 방언: 표준 집계(COUNT/MAX/SUM)와 byUser 가 이미 쓰는 `GROUP BY 1` 관용구만 쓴다. 날짜 경계는
 * sessionRows 와 같은 **접두 10자 비교**(substr(started_at,1,10))로 맞춘다 — usage_counters 는
 * `day` 컬럼이 이미 그 형식이라 그대로 비교한다. 필터는 SQL IN 목록을 만들지 않고 **JS 에서 접는다**.
 */
function cutoffDayBefore(days, nowMs) {
  const n = Number.isFinite(Number(days)) ? Math.max(1, Math.floor(Number(days))) : ACTIVITY_WINDOW_DAYS;
  return new Date((Number(nowMs) || Date.now()) - n * 86400e3).toISOString().slice(0, 10);
}

async function machineActivity({ windowDays = ACTIVITY_WINDOW_DAYS, nowMs } = {}) {
  const days = Number.isFinite(Number(windowDays)) ? Math.max(1, Math.floor(Number(windowDays))) : ACTIVITY_WINDOW_DAYS;
  const sinceDay = cutoffDayBefore(days, nowMs);

  const sess = await q(
    "SELECT COALESCE(NULLIF(machine,''),'') m, COUNT(*) c, MAX(started_at) last_at"
    + ' FROM usage_sessions WHERE substr(started_at,1,10) >= ? GROUP BY 1',
  ).all(sinceDay);
  /*
   * day 가 비어 있는 행(구버전 수집기)은 **창 밖으로 친다.** 시각을 모르는 것을 최근으로 세면
   * 옛 데이터가 오늘의 판정을 다시 오염시킨다 — 그게 이 창을 만든 이유다.
   */
  const bash = await q(
    "SELECT COALESCE(NULLIF(machine,''),'') m, key, SUM(count) c"
    + " FROM usage_counters WHERE kind='bash' AND day >= ? GROUP BY 1, key",
  ).all(sinceDay);

  const out = {};
  const at = (m) => {
    if (!out[m]) {
      out[m] = {
        sessions: 0, lastSessionAt: null, bash: 0,
        gateTotal: 0, gateKeys: [], certainTotal: 0, certainKeys: [],
        windowDays: days, sinceDay,
      };
    }
    return out[m];
  };
  for (const r of sess) {
    if (!r.m) continue;                    // 머신을 모르는 보고는 대조에 쓸 수 없다(빈 키로 뭉개지 않는다)
    const e = at(r.m);
    e.sessions = Number(r.c) || 0;
    e.lastSessionAt = r.last_at || null;
  }
  for (const r of bash) {
    if (!r.m) continue;
    const e = at(r.m);
    const n = Number(r.c) || 0;
    e.bash += n;
    if (isGateLeadKey(r.key)) {
      e.gateTotal += n;
      e.gateKeys.push({ key: r.key, count: n, certain: isCertainGateLeadKey(r.key) });
      if (isCertainGateLeadKey(r.key)) { e.certainTotal += n; e.certainKeys.push({ key: r.key, count: n }); }
    }
  }
  // 정렬은 결정론이다(화면이 흔들리면 안 된다) — 횟수 내림차순, 동률은 코드포인트 오름차순.
  const byCount = (a, b) => (b.count - a.count) || (a.key < b.key ? -1 : a.key > b.key ? 1 : 0);
  for (const e of Object.values(out)) {
    e.gateKeys.sort(byCount); e.gateKeys = e.gateKeys.slice(0, 8);
    e.certainKeys.sort(byCount); e.certainKeys = e.certainKeys.slice(0, 8);
  }
  return out;
}

/*
 * 사용자별 축 집계 — "누가 어떤 서브에이전트·스킬을 쓰는가".
 *
 * 왜 필요한가(실측): 같은 지침이 모든 세션에 실리는데 실제 사용은 사람마다 갈렸다.
 *   사용자 A  backend-engineer 25 · qa-engineer 16 · frontend-engineer 14 · security-reviewer 10
 *   사용자 B  general-purpose 25 · Explore 10 · **역할 에이전트 0**
 * 지침만 있고 측정이 없어 아무도 몰랐다. topKeys 는 전체 합이라 이 차이를 보여주지 못한다.
 *
 * 강제하지 않는다 — 보이면 사람이 판단한다. 훅으로 넛지하면 "실질 작업" 을 정확히 가릴 수 없어
 * 오판정 잔소리가 되고, 그건 무시당한다.
 */
async function byUser(kind, limit = 8) {
  if (!COUNTER_KINDS.includes(kind)) return [];
  const lim = Math.max(1, Math.min(50, Math.floor(Number(limit)) || 8));
  const rows = await q(
    "SELECT COALESCE(NULLIF(username,''),'(미상)') u, key, SUM(count) c"
    + ' FROM usage_counters WHERE kind=? GROUP BY 1, key ORDER BY 1, c DESC',
  ).all(kind);
  const byU = new Map();
  for (const r of rows) {
    if (!byU.has(r.u)) byU.set(r.u, []);
    byU.get(r.u).push({ key: r.key, count: Number(r.c) || 0 });
  }
  return [...byU.entries()].map(([username, items]) => ({
    username,
    total: items.reduce((a, x) => a + x.count, 0),
    items: items.slice(0, lim),
  })).sort((a, b) => b.total - a.total);
}

/*
 * 추천 공백 — 점수가 낮았던 목표의 토큰 빈도.
 *
 * 이 목록이 학습 플랫폼 고도화의 실질 입력이다. 매칭이 약한 목표들이 같은 토큰을 공유하면
 * 그 토큰이 곧 카탈로그가 덮지 못하는 주제다. 임계값을 인자로 빼는 이유: 점수 분포는 카탈로그
 * 크기에 따라 달라져서 고정 상수로 두면 카탈로그가 커질 때 조용히 아무것도 안 잡는다.
 */
async function recommendationGaps({ maxScore = 1, limit = 20 } = {}) {
  const rows = await q(
    'SELECT goal_tokens FROM usage_recommendations WHERE score <= ? AND goal_tokens IS NOT NULL'
    + ' ORDER BY id DESC LIMIT 2000',
  ).all(Number(maxScore));
  const freq = new Map();
  for (const r of rows) {
    for (const raw of String(r.goal_tokens || '').split(/\s+/)) {
      const t = stripParticle(raw);
      if (t.length < 2) continue;
      freq.set(t, (freq.get(t) || 0) + 1);
    }
  }
  return [...freq.entries()]
    .filter(([, c]) => c >= 2)               // 1회성 목표는 공백의 증거가 못 된다(distill 과 같은 규율)
    .sort((a, b) => b[1] - a[1])
    .slice(0, Math.max(1, Math.min(100, Math.floor(Number(limit)) || 20)))
    .map(([token, count]) => ({ token, count }));
}

/* 추천 전환 요약 — 몇 건 중 몇 건이 매칭 실패였나. */
async function recommendationSummary() {
  const r = (await q(
    'SELECT COUNT(*) n, SUM(CASE WHEN score <= 0 THEN 1 ELSE 0 END) miss FROM usage_recommendations',
  ).all())[0] || {};
  return { total: Number(r.n) || 0, miss: Number(r.miss) || 0 };
}

/* ── 보존(retention) ──
 *
 * 키워드 축만 기한을 둔다. 이유가 둘이다:
 *   ① 어휘가 무제한이다. 다른 축(tool·bash·slash·skill·agent·mcp)은 사실상 고정된 이름 집합이라
 *      행 수가 수렴하지만, keyword 는 사람이 쓰는 말이라 계속 새 행이 생긴다.
 *      실측(2026-08-03): 한 사람 50세션에 3,590 키 / 130,670 카운트. 팀 전체면 수만 행이다.
 *   ② 이 축만 사람이 입력한 말에서 나온다. 오래 들고 있을 이유가 가장 약하고, 오래 들고 있어서
 *      생기는 위험은 가장 크다. 추세 파악에 필요한 창은 분기면 충분하다.
 *
 * 다른 축과 usage_sessions 는 지우지 않는다 — 행 수가 작고, 비용 추세는 길게 봐야 의미가 있다.
 *
 * usage_series 는 예외다(pruneSeries). 세션당 1행이 아니라 **시간 × 모델**당 1행이라 증가
 * 속도가 다르다 — 실측으로 4일짜리 세션 하나가 버킷 24개를 만들었다. 다만 기한은 훨씬 길게
 * 둔다. 이 축은 개인 발화가 아니라 숫자뿐이고(지울 사생활 근거가 약하다), 비용 추세를 연 단위로
 * 보는 것이 이 테이블을 만든 이유의 절반이기 때문이다.
 */
const KEYWORD_RETENTION_DEFAULT = 90;
const KEYWORD_RETENTION_MIN = 7;      // 너무 짧으면 축 자체가 쓸모없어진다
const KEYWORD_RETENTION_MAX = 3650;

function retentionDays(v) {
  const raw = v == null || String(v).trim() === '' ? KEYWORD_RETENTION_DEFAULT : Number(v);
  if (!Number.isFinite(raw)) return KEYWORD_RETENTION_DEFAULT;
  return Math.max(KEYWORD_RETENTION_MIN, Math.min(KEYWORD_RETENTION_MAX, Math.floor(raw)));
}

/* 경계 날짜(YYYY-MM-DD) — 이 날짜 **이전**(미포함) 키워드 행을 지운다. now 는 테스트 주입용. */
function cutoffDay(days, now = new Date()) {
  const d = new Date(now.getTime() - retentionDays(days) * 86400000);
  return d.toISOString().slice(0, 10);
}

/*
 * 기한 지난 키워드 카운터 삭제. 반환은 지운 행 수(방언 무관하게 세어서 돌려준다).
 *
 * day 가 NULL 인 행도 지운다 — day 는 적재 시점에 항상 채워지므로(countersUpsert), NULL 은
 * 스키마 이전에 들어온 잔재이고 나이를 알 수 없다. 나이를 모르는 개인 발화 데이터를 영구 보관하는
 * 쪽보다 지우는 쪽이 이 축의 취지에 맞다.
 */
async function pruneKeywords({ days, now } = {}) {
  const cutoff = cutoffDay(days, now);
  const before = (await q("SELECT COUNT(*) c FROM usage_counters WHERE kind='keyword'").all())[0];
  await q("DELETE FROM usage_counters WHERE kind='keyword' AND (day IS NULL OR day < ?)").run(cutoff);
  const after = (await q("SELECT COUNT(*) c FROM usage_counters WHERE kind='keyword'").all())[0];
  const removed = Math.max(0, (Number(before && before.c) || 0) - (Number(after && after.c) || 0));
  return { removed, cutoff, days: retentionDays(days), kept: Number(after && after.c) || 0 };
}

/*
 * 기한 지난 시간 버킷 삭제. 기본 365일 — 키워드(90일)보다 훨씬 길다(위 주석의 이유).
 *
 * hour 가 NULL 인 행은 애초에 들어올 수 없다(PK 구성원이다). 그래서 키워드 prune 과 달리
 * NULL 처리 분기를 두지 않는다 — 없는 경우를 방어하는 코드는 그 자체가 거짓말이 된다.
 */
const SERIES_RETENTION_DEFAULT = 365;

async function pruneSeries({ days, now } = {}) {
  const d = days == null || String(days).trim() === '' ? SERIES_RETENTION_DEFAULT : days;
  const cutoff = cutoffDay(d, now);
  const before = (await q('SELECT COUNT(*) c FROM usage_series').all())[0];
  await q('DELETE FROM usage_series WHERE substr(hour,1,10) < ?').run(cutoff);
  const after = (await q('SELECT COUNT(*) c FROM usage_series').all())[0];
  const removed = Math.max(0, (Number(before && before.c) || 0) - (Number(after && after.c) || 0));
  return { removed, cutoff, days: retentionDays(d), kept: Number(after && after.c) || 0 };
}

/* 전체 규모 — 화면 상단 요약과 "데이터가 아직 없음" 판정에 쓴다. */
async function totals() {
  const r = (await q(
    'SELECT COUNT(*) n, SUM(input) i, SUM(output) o, SUM(cache_read) cr, SUM(cache_create) cc,'
    + ' COUNT(DISTINCT username) u, COUNT(DISTINCT machine) m FROM usage_sessions',
  ).all())[0] || {};
  return {
    sessions: Number(r.n) || 0, input: Number(r.i) || 0, output: Number(r.o) || 0,
    cacheRead: Number(r.cr) || 0, cacheCreate: Number(r.cc) || 0,
    users: Number(r.u) || 0, machines: Number(r.m) || 0,
  };
}

/*
 * 세션 원행 — 분포·비용·드릴다운이 공유하는 단일 조회구.
 *
 * 왜 집계된 뷰가 아니라 원행인가: 분포(p50/p95/p99)와 비용은 **세션 단위 값**이 있어야 계산된다.
 * SUM 으로 접힌 뒤에는 복원할 수 없다. 그래서 여기서는 접지 않고 넘기고, 접는 일은 호출부가
 * 한다(lib/usage/stats.js 는 분포, lib/usage/cost.js 는 비용).
 *
 * ⚠ **limit 이 이 함수의 안전장치다.** 세션 수가 계속 늘어나므로 상한 없이 부르면 언젠가 전량을
 *   메모리로 끌어온다. 기본 5,000 · 최대 50,000 으로 가두고, 넘칠 때는 **최신 것부터** 자른다
 *   (오래된 표본을 버리는 쪽이 분포 왜곡이 적다). 호출부는 반환 길이가 limit 과 같으면 표본이
 *   잘렸다고 보고 화면에 그 사실을 표시해야 한다.
 *
 * 필터는 값만 바인딩한다 — 컬럼명·정렬축을 문자열로 이어 붙이지 않는다(주입 표면 0).
 */
const SESSION_ROWS_DEFAULT = 5000;
const SESSION_ROWS_MAX = 50000;

async function sessionRows({ from, to, username, model, limit } = {}) {
  const lim = Math.max(1, Math.min(SESSION_ROWS_MAX, Math.floor(Number(limit)) || SESSION_ROWS_DEFAULT));
  const where = [];
  const args = [];
  /*
   * 날짜 경계는 **접두 10자 비교**로 한다(usageByDay 와 같은 관용구).
   * started_at 이 ISO 라 `started_at <= '2026-08-03'` 은 그날 00:00 이후를 전부 놓친다 —
   * 하루치를 조용히 잘라먹는 자리라 상한/하한을 같은 방식으로 맞춘다.
   * 형식이 아닌 값은 무시한다(경계가 깨진 채로 조회되느니 없는 편이 낫다).
   */
  const isDay = (v) => /^\d{4}-\d{2}-\d{2}$/.test(String(v || ''));
  if (isDay(from)) { where.push('substr(started_at,1,10) >= ?'); args.push(String(from)); }
  if (isDay(to)) { where.push('substr(started_at,1,10) <= ?'); args.push(String(to)); }
  if (username) { where.push('username = ?'); args.push(String(username)); }
  if (model) { where.push('model = ?'); args.push(String(model)); }

  const sql = 'SELECT session_id, machine, username, project, model, input, output, cache_read,'
    + ' cache_create, web_search, web_fetch, turns, started_at, ended_at, reported_at FROM usage_sessions'
    + (where.length ? ` WHERE ${where.join(' AND ')}` : '')
    + ' ORDER BY started_at DESC LIMIT ?';

  return (await q(sql).all(...args, lim)).map((r) => ({
    sessionId: r.session_id,
    machine: r.machine || '',
    username: r.username || '',
    project: r.project || '',
    model: r.model || '',
    input: Number(r.input) || 0,
    output: Number(r.output) || 0,
    cacheRead: Number(r.cache_read) || 0,
    cacheCreate: Number(r.cache_create) || 0,
    webSearch: Number(r.web_search) || 0,
    webFetch: Number(r.web_fetch) || 0,
    turns: Number(r.turns) || 0,
    startedAt: r.started_at || null,
    endedAt: r.ended_at || null,
    reportedAt: r.reported_at || null,
  }));
}

/* 세션 하나. 드릴다운이 전량을 끌어와 훑지 않게 하는 자리다. */
async function sessionById(sessionId) {
  if (!sessionId) return null;
  const rows = await q(
    'SELECT session_id, machine, username, project, model, input, output, cache_read,'
    + ' cache_create, web_search, web_fetch, turns, started_at, ended_at, reported_at'
    + ' FROM usage_sessions WHERE session_id = ?',
  ).all(String(sessionId));
  const r = rows[0];
  if (!r) return null;
  return {
    sessionId: r.session_id,
    machine: r.machine || '',
    username: r.username || '',
    project: r.project || '',
    model: r.model || '',
    input: Number(r.input) || 0,
    output: Number(r.output) || 0,
    cacheRead: Number(r.cache_read) || 0,
    cacheCreate: Number(r.cache_create) || 0,
    webSearch: Number(r.web_search) || 0,
    webFetch: Number(r.web_fetch) || 0,
    turns: Number(r.turns) || 0,
    startedAt: r.started_at || null,
    endedAt: r.ended_at || null,
    reportedAt: r.reported_at || null,
  };
}

/*
 * 시간 버킷 조회 — 시간 시계열의 원자료.
 *
 * 경계 비교가 sessionRows 와 **다르다**. hour 는 'YYYY-MM-DDTHH' 라 앞 10자가 곧 날짜이므로
 * substr(hour,1,10) 으로 날짜 경계를 잡는다. sessionRows 의 관용구를 컬럼 이름만 바꿔 옮기면
 * 안 되는 자리다.
 *
 * 상한이 세션보다 큰 이유: 한 세션이 시간 버킷을 여럿 만든다(실측: 4일짜리 세션 하나가 24개).
 * 같은 기간을 보려면 행 수가 그만큼 더 필요하다.
 */
const SERIES_ROWS_DEFAULT = 20000;
const SERIES_ROWS_MAX = 200000;

async function seriesRows({ from, to, username, model, limit } = {}) {
  const lim = Math.max(1, Math.min(SERIES_ROWS_MAX, Math.floor(Number(limit)) || SERIES_ROWS_DEFAULT));
  const where = [];
  const args = [];
  const isDay = (v) => /^\d{4}-\d{2}-\d{2}$/.test(String(v || ''));
  if (isDay(from)) { where.push('substr(hour,1,10) >= ?'); args.push(String(from)); }
  if (isDay(to)) { where.push('substr(hour,1,10) <= ?'); args.push(String(to)); }
  if (username) { where.push('username = ?'); args.push(String(username)); }
  if (model) { where.push('model = ?'); args.push(String(model)); }

  const sql = 'SELECT session_id, hour, model, input, output, cache_read, cache_create,'
    + ' cc_5m, cc_1h, turns, tool_errors, stop_max_tokens, stop_refusal,'
    + ' latency_ms_sum, latency_ms_max, latency_turns, username, machine, project'
    + ' FROM usage_series'
    + (where.length ? ` WHERE ${where.join(' AND ')}` : '')
    + ' ORDER BY hour DESC LIMIT ?';

  return (await q(sql).all(...args, lim)).map(mapSeries);
}

function mapSeries(r) {
  return {
    sessionId: r.session_id,
    hour: r.hour,
    model: r.model || '',
    input: Number(r.input) || 0,
    output: Number(r.output) || 0,
    cacheRead: Number(r.cache_read) || 0,
    cacheCreate: Number(r.cache_create) || 0,
    // cost.js 가 기대하는 이름으로 올린다 — TTL 분해가 있으면 그쪽이 정확히 가격을 매긴다.
    cacheCreate5m: Number(r.cc_5m) || 0,
    cacheCreate1h: Number(r.cc_1h) || 0,
    turns: Number(r.turns) || 0,
    toolErrors: Number(r.tool_errors) || 0,
    stopMaxTokens: Number(r.stop_max_tokens) || 0,
    stopRefusal: Number(r.stop_refusal) || 0,
    latencyMsSum: Number(r.latency_ms_sum) || 0,
    latencyMsMax: Number(r.latency_ms_max) || 0,
    latencyTurns: Number(r.latency_turns) || 0,
    username: r.username || '',
    machine: r.machine || '',
    project: r.project || '',
  };
}

/*
 * 품질축 집계 — usage_series 전량 SUM(행 제한 없이). 도구 오류·거부·최대토큰중단·지연을
 * 기간 전체로 합산한다. 신수집기만 시간 버킷을 보내므로 sessionsWithSeries 로 커버리지를 함께
 * 내보내 화면이 "이 지표는 세션 N/M 만 덮는다"고 정직하게 말하게 한다.
 */
async function seriesQualityTotals({ from, to, username } = {}) {
  const where = []; const args = [];
  const isDay = (v) => /^\d{4}-\d{2}-\d{2}$/.test(String(v || ''));
  if (isDay(from)) { where.push('substr(hour,1,10) >= ?'); args.push(String(from)); }
  if (isDay(to)) { where.push('substr(hour,1,10) <= ?'); args.push(String(to)); }
  if (username) { where.push('username = ?'); args.push(String(username)); }
  const w = where.length ? ` WHERE ${where.join(' AND ')}` : '';
  const r = await q(
    'SELECT COUNT(DISTINCT session_id) sess, COALESCE(SUM(turns),0) turns,'
    + ' COALESCE(SUM(tool_errors),0) te, COALESCE(SUM(stop_max_tokens),0) smt,'
    + ' COALESCE(SUM(stop_refusal),0) ref, COALESCE(SUM(latency_ms_sum),0) lsum,'
    + ' COALESCE(MAX(latency_ms_max),0) lmax, COALESCE(SUM(latency_turns),0) lturns'
    + ` FROM usage_series${w}`,
  ).get(...args);
  return {
    sessionsWithSeries: Number(r && r.sess) || 0,
    turns: Number(r && r.turns) || 0,
    toolErrors: Number(r && r.te) || 0,
    stopMaxTokens: Number(r && r.smt) || 0,
    stopRefusal: Number(r && r.ref) || 0,
    latencyMsSum: Number(r && r.lsum) || 0,
    latencyMsMax: Number(r && r.lmax) || 0,
    latencyTurns: Number(r && r.lturns) || 0,
  };
}

/* 한 세션의 시간 버킷 — 드릴다운에서 "이 세션이 몇 시에 무엇을 썼나". */
async function seriesOf(sessionId) {
  if (!sessionId) return [];
  const rows = await q(
    'SELECT session_id, hour, model, input, output, cache_read, cache_create, cc_5m, cc_1h,'
    + ' turns, tool_errors, stop_max_tokens, stop_refusal, latency_ms_sum, latency_ms_max,'
    + ' latency_turns, username, machine, project'
    + ' FROM usage_series WHERE session_id = ? ORDER BY hour ASC, model ASC',
  ).all(String(sessionId));
  return rows.map(mapSeries);
}

/* 한 세션의 축별 카운터 — 드릴다운(세션 상세)의 본문. */
async function countersOf(sessionId, kinds = COUNTER_KINDS) {
  const allow = (Array.isArray(kinds) ? kinds : []).filter((k) => COUNTER_KINDS.includes(k));
  if (!sessionId || !allow.length) return {};
  const marks = allow.map(() => '?').join(',');
  const rows = await q(
    `SELECT kind, key, count FROM usage_counters WHERE session_id = ? AND kind IN (${marks})`
    + ' ORDER BY count DESC',
  ).all(String(sessionId), ...allow);
  const out = {};
  for (const r of rows) {
    const kind = r.kind;
    if (!out[kind]) out[kind] = [];
    out[kind].push({ key: r.key, count: Number(r.count) || 0 });
  }
  return out;
}

module.exports = {
  init, COUNTER_KINDS, MAX_COUNTERS_PER_SESSION,
  sessionUpsert, countersUpsert, counterBump, recommendationAdd,
  usageByDay, usageByUser, usageByModel, usageModelAxis,
  topKeys, byUser, reporterCoverage, seriesQualityTotals,
  GATE_LEAD_KEYS, GATE_LEAD_CERTAIN, GATE_LEAD_AMBIGUOUS,
  isGateLeadKey, isCertainGateLeadKey, machineActivity, ACTIVITY_WINDOW_DAYS,
  recommendationGaps, recommendationSummary, totals,
  sessionRows, sessionById, countersOf, SESSION_ROWS_DEFAULT, SESSION_ROWS_MAX,
  seriesUpsert, seriesRows, seriesOf, pruneSeries,
  SERIES_ROWS_DEFAULT, SERIES_ROWS_MAX, MAX_SERIES_PER_SESSION,
  pruneKeywords, retentionDays, cutoffDay,
  KEYWORD_RETENTION_DEFAULT, KEYWORD_RETENTION_MIN, KEYWORD_RETENTION_MAX,
};
