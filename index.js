'use strict';
/*
 * 라이브러리 공개 API — 이 프로젝트를 **내장해서** 쓸 때 통과하는 유일한 문.
 *
 * 두 가지 쓰임을 같은 코드로 지탱한다:
 *   ① 독립 서비스   `node server.js` 가 이 파일을 집어 라우트를 배선한다.
 *   ② 임베드        다른 Node 서버가 `require('user-usage')` 로 라우트 셋을 자기 체인에 끼운다.
 *
 * ②를 위해 규칙이 하나 있다: **호스트는 lib/* 를 직접 require 하지 않는다.** 내부를 뒤지기
 * 시작하면 컬럼 이름·상수까지 호스트 코드에 새고, 그러면 스키마를 바꿀 때마다 호스트가 조용히
 * 깨진다(런타임 require + try/catch 라 컴파일 에러도 안 난다). 필요한 것이 여기 없으면
 * 내부를 뒤지지 말고 **여기에 래퍼를 추가**한다.
 *
 * 부작용 금지: 이 파일을 require 하는 것만으로 DB 를 건드리거나 타이머를 걸지 않는다.
 * 스키마는 init(), 타이머는 startRetention() 이 명시적으로 시작한다(server.js 부팅 경로).
 */
const store = require('./lib/store');
const identity = require('./lib/identity');
const retention = require('./lib/retention');
const audit = require('./lib/audit');

/* ── 라우트 ─────────────────────────────────────────────────────────
 * server.js 가 배선할 세 핸들러. 시그니처는 `(req, res, ctx) => Promise<boolean>` 이고,
 * true 를 돌려주면 "내가 응답했다"는 뜻이다(체인이 거기서 멈춘다).
 *
 * **등록 순서가 계약이다** — analytics 가 admin 보다 앞이어야 한다. admin 이 /api/usage 접두사를
 * 통째로 소유하고 안 걸리면 404 를 직접 내므로, 뒤로 가면 관측 화면이 통째로 404 가 된다.
 */
const usageRoutes = require('./routes/usage');        // 함수 본체 = 조회·관리, .intake = 인테이크
const analyticsRoutes = require('./routes/usage-analytics');

const routes = Object.freeze({
  intake: usageRoutes.intake,   // POST /api/usage                        수집기 보고(자체 게이트)
  analytics: analyticsRoutes,   // GET  /api/usage/{series,distribution,sessions,dispatch}
  admin: usageRoutes,           //      /api/usage/*                      접두사 소유(안 걸리면 404)
});

/* ── 수명주기 ───────────────────────────────────────────────────────── */

/*
 * 테이블 보장(멱등, additive). 인자를 받지 않고 어댑터를 쓴다 — 어느 백엔드인지는 lib/db 가 안다.
 * sqlite 면 여기서 DDL 을 돌리고, pg 면 조기 반환한다(스키마는 migrations/ 소유).
 */
async function init() {
  await store.init();
  await identity.init();   // 머신 → 계정 매핑(귀속 교정표)
  await audit.init();      // 관리 동작 감사 로그
}

/*
 * 키워드 보존 정리기 기동. 보존이 꺼져 있으면(USAGE_KEYWORD_RETENTION_DAYS=off) **null** 을
 * 돌려준다 — 호출부는 null 이면 아무것도 등록하지 않는다(타이머 0개, 완전 no-op).
 * 반환 핸들은 { stop(), days } 이고 server.js 가 disposers 에 등록한다(SIGTERM 정리).
 */
function startRetention() {
  return retention.start();
}

/* ── 호스트가 부르는 계측·조회 ────────────────────────────────────────
 * 아래 셋은 전부 **best-effort** 다. 실패해도 던지지 않는다 — 계측 실패가 호스트의 본 기능을
 * 흔들면 안 된다. (호출부에도 try/catch 가 있겠지만, 방어를 호출부에만 두면 새 호출부가 그걸
 * 빠뜨렸을 때 조용히 본 기능이 죽는다.)
 */

/*
 * 서버측 도구 호출 계측. 호스트가 도구 호출 경로 한 지점에서 부른다.
 * 클라이언트 수집기의 'mcp' 축과 교차 검증되는 값이다(둘이 크게 어긋나면 수집기가 죽은 것).
 */
async function noteMcpCall(name) {
  return usageRoutes.noteMcpCall(name);
}

/*
 * 추천 관측 기록. 목표 **원문을 받지 않는다** — 호출부가 토큰화 결과를 넘긴다
 * (추천 로직을 순수 함수로 남기기 위해 로깅은 호출부가 진다).
 */
async function noteRecommendation(rec) {
  return usageRoutes.noteRecommendation(rec);
}

/*
 * 머신 → 계정 귀속. 호스트의 다른 보고 경로가 사용량 인테이크와 **같은 규칙**을 쓰게 하는
 * 창구다(두 화면이 같은 사람을 다른 이름으로 보이면 안 된다).
 * 매핑이 없으면 null — 호출부는 그때 클라이언트가 보낸 이름을 그대로 쓴다.
 */
async function resolveMachineIdentity(machine) {
  try { return await identity.resolve(machine); }
  catch { return null; }   // 매핑 조회 실패가 보고를 막지 않는다
}

/*
 * 머신별 활동 요약 — "그 PC 에서 세션이 돌기는 하는가"를 묻는 화면의 조회 창구.
 *
 * 왜 래퍼인가: 호출부가 store 를 직접 뒤지면 활동 행의 **필드 이름 전부**와 기본 창 길이 상수까지
 * 알게 된다. 그러면 스키마를 건드릴 때마다 그 화면이 인질이 된다. 호출부가 알아야 할 것은 셋뿐이다:
 * activity(맵 또는 null) · empty(빈 행) · windowDays.
 *
 * 반환 계약:
 *   { activity: { [machine]: row } | null, empty: row, windowDays: number }
 *   · activity === null 은 **"못 셌다"** 이지 "활동이 0" 이 아니다. 조회 실패를 0 으로 접으면
 *     "그 PC 는 아무것도 안 쓴다"로 읽힌다 — 원인이 정반대인 두 상황이 같은 화면이 된다.
 *   · empty 는 activity 는 읽혔는데 그 머신 행만 없을 때 쓰는 0 행이다.
 * 던지지 않는다.
 */
async function machineActivity({ windowDays } = {}) {
  const n = Number(windowDays);
  const days = Number.isFinite(n) && n > 0 ? Math.floor(n) : store.ACTIVITY_WINDOW_DAYS;
  const empty = {
    sessions: 0, lastSessionAt: null, bash: 0,
    gateTotal: 0, gateKeys: [], certainTotal: 0, certainKeys: [],
    windowDays: days, sinceDay: null,
  };
  let activity = null;
  try { activity = await store.machineActivity({ windowDays: days }); }
  catch { activity = null; }
  return { activity, empty, windowDays: days };
}

module.exports = {
  routes,
  init,
  startRetention,
  noteMcpCall,
  noteRecommendation,
  resolveMachineIdentity,
  machineActivity,
};
