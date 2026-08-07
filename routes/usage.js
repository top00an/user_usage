'use strict';
/*
 * 사용량 텔레메트리 라우트 — /api/usage 접두사를 통째로 소유한다(안 걸리면 404를 직접 낸다).
 *
 * 두 계층으로 갈린다(routes/README.md 의 PUBLIC vs AUTHED 계약):
 *   PUBLIC  POST /api/usage          팀원 PC 훅이 보고. 세션 쿠키가 없으므로 자체 게이트를 진다.
 *   AUTHED  GET  /api/usage/summary  관제 화면이 읽는다.
 *
 * 왜 보고가 PUBLIC 인가: 보고자는 브라우저가 아니라 SessionStart 훅이다. /api/report 와 같은
 * 계층이고, 같은 게이트(LAN · 세션 · adminToken · 동기화 토큰)를 쓴다.
 *
 * 왜 조회에 admin 을 요구하나: 이 데이터는 **사람별 사용량**이다. 누가 얼마나 썼는지가 보이므로
 * 열람 권한을 editor 까지 넓히지 않는다. 팀 총량만 보고 싶다면 대시보드 카드로 따로 빼는 편이
 * 낫지 정책을 여기서 느슨하게 할 이유가 없다.
 */
const store = require('../lib/store');
const intake = require('../lib/intake');
const audit = require('../lib/audit');

/* PUBLIC — 팀원 PC 보고 인테이크. */
async function usageIntake(req, res, ctx) {
  const { p, sendJson, jsonBody } = ctx;
  if (req.method !== 'POST' || p !== '/api/usage') return false;

  /*
   * 등록 판정 — **브라우저 세션을 근거로 인정하지 않는다.**
   *
   * 이건 쓰기 경로이고, 인증 게이트보다 앞에서 돌 수 있다. 세션을 등록 근거로 인정하면 로그인한
   * 사람의 브라우저를 꾀어 임의 사용량을 밀어 넣을 수 있는 자리가 된다.
   * 이 엔드포인트의 보고자는 언제나 수집기(LAN·동기화 토큰·인테이크/관리자 토큰)이지
   * 브라우저가 아니다.
   *
   * isIntakeReq 는 보고 전용 토큰(USAGE_INTAKE_TOKEN)으로 온 요청이다 — 이 경로 **하나만**
   * 열려 있고 조회는 호스트가 막는다(server.js 상단 ②'). 호스트가 그 함수를 안 주는 임베드
   * 환경도 있으므로 존재 여부를 먼저 본다.
   */
  const enrolled = ctx.isLanReq(req) || ctx.isAdminReq(req)
    || (typeof ctx.isIntakeReq === 'function' && ctx.isIntakeReq(req))
    || (ctx.syncTenant && ctx.syncTenant === require('../lib/tenant').currentTenant());
  if (!enrolled) { sendJson(res, 403, { error: 'not enrolled' }); return true; }

  let body = null;
  try { body = await jsonBody(req); } catch { body = null; }
  const sessions = intake.normPayload(body);

  /*
   * 서버 권위 귀속 — 머신 매핑이 걸려 있으면 클라이언트가 보낸 사용자명을 **덮어쓴다**.
   *
   * 클라이언트가 보고하는 이름은 기본이 OS 계정명이라 팀 계정명과 어긋날 수 있고, 어긋난 것을
   * 수집기 재배포+재설치로 고치는 방식은 반복 비용이 크다.
   * 인테이크가 유일한 진입점이라, 여기서 덮으면 클라이언트 경로가 몇 개든 결과가 하나로 모인다.
   */
  const identity = require('../lib/identity');
  let stored = 0;
  let counters = 0;
  let buckets = 0;
  for (const { session, rows, series } of sessions) {
    const mapped = await identity.resolve(session.machine);
    if (mapped) session.username = mapped;
    // 한 세션이 실패해도 나머지는 넣는다 — 텔레메트리가 전부-아니면-전무일 이유가 없다.
    try {
      if (await store.sessionUpsert(session)) stored += 1;
      counters += await store.countersUpsert({
        sessionId: session.sessionId, username: session.username,
        machine: session.machine, startedAt: session.startedAt, rows,
      });
      /*
       * 시간 버킷은 신버전 수집기만 보낸다. 없으면 아무 일도 일어나지 않는다 —
       * 구버전 수집기를 쓰는 팀원의 보고가 거절되면 그동안 그 사람 사용량이 통째로 사라진다.
       * 귀속(username)은 위에서 서버가 정한 값을 쓴다. 세션 행과 갈라지면 안 된다.
       */
      if (series && series.length) {
        buckets += await store.seriesUpsert({
          sessionId: session.sessionId, username: session.username,
          machine: session.machine, project: session.project, rows: series,
        });
      }
    } catch { /* best-effort */ }
  }

  sendJson(res, 200, { ok: true, sessions: stored, counters, buckets });
  return true;
}

/* AUTHED — 관제 화면. */
module.exports = async function usageRoutes(req, res, ctx) {
  const { u, p, sendJson, requireRole, jsonBody } = ctx;
  if (!p.startsWith('/api/usage')) return false;

  if (req.method === 'GET' && p === '/api/usage/summary') {
    // 관측(사용량·비용 대시보드)은 운영자+어드민에게 연다(observe). 사람별 비용이 담기지만
    // 팀 운영자가 봐야 하는 관측 지표라 admin 아래로 한 칸 내린다 — 감사로그·관리작업은 그대로 admin.
    if (!requireRole('observe')) return true;
    const days = Number(u.searchParams.get('days')) || 30;
    const topN = Number(u.searchParams.get('top')) || 20;
    const [
      totals, byDay, byUser, byModel, modelAxis,
      tool, bash, slash, skill, agent, mcp, keyword,
      gaps, reco,
    ] = await Promise.all([
      store.totals(), store.usageByDay(days), store.usageByUser(), store.usageByModel(),
      /*
       * 모델 축의 근거 커버리지. byModel 행의 fromSeries/fromSession 이 "이 값이 어디서
       * 왔나"를 말하고, 이쪽이 "누구의 값이 근사인가"를 말한다 — 둘이 붙어야 화면이
       * 오귀속을 정확한 값으로 위장하지 않는다.
       */
      store.usageModelAxis(),
      store.topKeys('tool', topN), store.topKeys('bash', topN), store.topKeys('slash', topN),
      store.topKeys('skill', topN), store.topKeys('agent', topN), store.topKeys('mcp', topN),
      store.topKeys('keyword', topN),
      store.recommendationGaps({ limit: topN }), store.recommendationSummary(),
    ]);
    // 보존 정책을 응답에 실어 화면이 말하게 한다 — 데이터가 언젠가 사라진다는 사실은
    // 보는 사람이 알아야 하고(추세가 끊기는 이유), 팀에게도 공개돼야 하는 약속이다.
    const retention = require('../lib/retention');
    const keywordDays = retention.configuredDays();
    sendJson(res, 200, {
      totals, byDay, byUser, byModel, modelAxis,
      top: { tool, bash, slash, skill, agent, mcp, keyword },
      recommendation: Object.assign({}, reco, { gaps }),
      retention: { keywordDays },   // null = 무기한 보관(USAGE_KEYWORD_RETENTION_DAYS=off)
    });
    return true;
  }

  /*
   * 머신 → 계정 매핑 관리(admin).
   * 사람별 사용량을 다루는 화면과 같은 권한 경계를 쓴다 — 귀속을 바꾸는 일이라 더 낮출 이유가 없다.
   */
  if (p === '/api/usage/identity') {
    const identity = require('../lib/identity');
    if (!requireRole('admin')) return true;

    if (req.method === 'GET') {
      const [items, pending] = await Promise.all([identity.list(), identity.unmapped()]);
      sendJson(res, 200, { items, unmapped: pending });
      return true;
    }
    if (req.method === 'PUT' || req.method === 'POST') {
      const b = await jsonBody(req).catch(() => ({}));
      try {
        const r = await identity.set({
          machine: b.machine, username: b.username, note: b.note, actor: ctx.me && ctx.me.u,
        });
        await audit.log(ctx.me && ctx.me.u, 'usage.identity.set', r.machine,
          { username: r.username, moved: r.moved });
        sendJson(res, 200, Object.assign({ ok: true }, r));
      } catch (e) { sendJson(res, 400, { error: String((e && e.message) || e) }); }
      return true;
    }
    if (req.method === 'DELETE') {
      const machine = u.searchParams.get('machine') || '';
      const ok = await identity.remove(machine);
      if (ok) await audit.log(ctx.me && ctx.me.u, 'usage.identity.remove', machine, {});
      sendJson(res, 200, { ok });
      return true;
    }
  }

  // 접두사를 소유하므로 여기까지 오면 404를 직접 낸다(다른 모듈로 흘려보내지 않는다).
  sendJson(res, 404, { error: 'not found' });
  return true;
};

module.exports.intake = usageIntake;

/*
 * 서버측 계측 헬퍼 — 팀원 PC 를 전혀 건드리지 않고 오늘부터 쌓이는 축.
 *
 * 호스트가 도구 호출 경로 한 지점에서 부른다.
 * 클라이언트 수집기의 'mcp' 축과 교차 검증되는 값이기도 하다(둘이 크게 어긋나면 수집기가 죽은 것).
 */
async function noteMcpCall(name) {
  try { await store.counterBump({ kind: 'mcp', key: name }); }
  catch { /* 계측 실패가 도구 호출을 흔들지 않는다 */ }
}

/*
 * 추천 관측 기록. 목표 **원문을 받지 않는다** — 호출부가 tokenize 결과를 넘긴다.
 * 추천 로직을 순수 함수로 남기기 위해 로깅은 호출부가 진다.
 */
async function noteRecommendation({ goalTokens, agent, skills, score, source, username }) {
  try {
    await store.recommendationAdd({ goalTokens, agent, skills, score, source, username });
  } catch { /* best-effort */ }
}

module.exports.noteMcpCall = noteMcpCall;
module.exports.noteRecommendation = noteRecommendation;
