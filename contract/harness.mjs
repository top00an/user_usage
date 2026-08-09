/*
 * 계약 하네스 — 시드 · 캡처 · 정규화.
 *
 * 이 파일이 **포팅의 합격 기준을 정의한다.** Go 포팅본은 여기 열거된 요청에 대해 현행 Node
 * 서버와 같은 상태코드·같은 JSON 을 내야 한다(정규화 대상 필드 제외).
 *
 * ⚠ 목록에서 요청을 지우면 그 경로는 검증되지 않는다. 오류 케이스가 특히 그렇다 —
 *   포팅에서 가장 흔히 빠지는 것이 400/403/404 의 **정확한 코드와 문구**이고,
 *   화면은 그 구조에 분기를 걸고 있다(public/js/core.js 의 fail()).
 */
import { WINDOW } from './fixtures.mjs';

const W = `from=${WINDOW.from}&to=${WINDOW.to}`;

/*
 * 캡처 대상. name 은 파일명이 되므로 안정적이어야 한다(바뀌면 골든이 통째로 갈린다).
 * auth: 'admin'(기본) | 'intake' | 'none' | 'cookie'
 */
export const REQUESTS = Object.freeze([
  /* ── 무인증 ─────────────────────────────────────────────────────────── */
  { name: 'healthz', method: 'GET', path: '/healthz', auth: 'none' },

  /* ── 총계 ───────────────────────────────────────────────────────────── */
  // days 는 "최근 N일"이라 오늘에 의존한다. 365(상한)로 고정해 시드 전량을 항상 덮는다.
  { name: 'summary', method: 'GET', path: '/api/usage/summary?days=365&top=20' },
  { name: 'summary-topN', method: 'GET', path: '/api/usage/summary?days=365&top=3' },

  /* ── 시계열: 3개 간격 × 4개 지표 ────────────────────────────────────── */
  { name: 'series-day-cost', method: 'GET', path: `/api/usage/series?${W}&metric=cost&interval=day` },
  { name: 'series-day-tokens', method: 'GET', path: `/api/usage/series?${W}&metric=tokens&interval=day` },
  { name: 'series-day-sessions', method: 'GET', path: `/api/usage/series?${W}&metric=sessions&interval=day` },
  { name: 'series-day-turns', method: 'GET', path: `/api/usage/series?${W}&metric=turns&interval=day` },
  // hour 는 usage_series(신수집기)에서만 온다 — day 와 **다른 테이블**이다. 섞이면 안 된다.
  { name: 'series-hour-cost', method: 'GET', path: `/api/usage/series?${W}&metric=cost&interval=hour` },
  { name: 'series-week-cost', method: 'GET', path: `/api/usage/series?${W}&metric=cost&interval=week` },

  /* ── 시계열 그룹핑(최대 3축) ────────────────────────────────────────── */
  { name: 'series-group-user', method: 'GET', path: `/api/usage/series?${W}&interval=day&group_by=user` },
  { name: 'series-group-model', method: 'GET', path: `/api/usage/series?${W}&interval=hour&group_by=model` },
  { name: 'series-group-multi', method: 'GET', path: `/api/usage/series?${W}&interval=day&group_by=user,model,project` },

  /* ── 분포 ───────────────────────────────────────────────────────────── */
  { name: 'distribution', method: 'GET', path: `/api/usage/distribution?${W}` },

  /* ── 세션 목록: 정렬축 5개 전부 ─────────────────────────────────────── */
  { name: 'sessions-cost', method: 'GET', path: `/api/usage/sessions?${W}&sort=cost&top=25` },
  { name: 'sessions-turns', method: 'GET', path: `/api/usage/sessions?${W}&sort=turns&top=25` },
  { name: 'sessions-output', method: 'GET', path: `/api/usage/sessions?${W}&sort=output&top=25` },
  { name: 'sessions-cacheread', method: 'GET', path: `/api/usage/sessions?${W}&sort=cacheRead&top=25` },
  { name: 'sessions-startedat', method: 'GET', path: `/api/usage/sessions?${W}&sort=startedAt&top=25` },
  { name: 'sessions-filtered', method: 'GET', path: `/api/usage/sessions?${W}&sort=cost&top=25&user=carol` },
  { name: 'sessions-top1', method: 'GET', path: `/api/usage/sessions?${W}&sort=cost&top=1` },

  /* ── 세션 상세(드릴다운) — 함정별로 하나씩 ──────────────────────────── */
  { name: 'session-detail-series', method: 'GET', path: '/api/usage/sessions/S1-alice-series-full' },
  { name: 'session-detail-noseries', method: 'GET', path: '/api/usage/sessions/S2-bob-no-series' },
  { name: 'session-detail-mixed', method: 'GET', path: '/api/usage/sessions/S3-carol-mixed-models' },
  { name: 'session-detail-residual', method: 'GET', path: '/api/usage/sessions/S4-carol-residual-turns' },
  { name: 'session-detail-unpriced', method: 'GET', path: '/api/usage/sessions/S5-dave-unpriced-model' },
  { name: 'session-detail-zero', method: 'GET', path: '/api/usage/sessions/S7-erin-zero-session' },

  /* ── 나머지 관측 축 ─────────────────────────────────────────────────── */
  { name: 'quality', method: 'GET', path: `/api/usage/quality?${W}` },
  { name: 'coverage', method: 'GET', path: '/api/usage/coverage' },
  { name: 'leaderboard', method: 'GET', path: `/api/usage/leaderboard?${W}` },
  { name: 'dispatch', method: 'GET', path: '/api/usage/dispatch' },
  { name: 'identity', method: 'GET', path: '/api/usage/identity' },

  /* ── 오류 계약 ───────────────────────────────────────────────────────
   * 여기가 포팅에서 가장 잘 깨진다. 화면은 status 로 분기하므로(core.js 의 fail),
   * 코드가 다르면 조용히 틀린 쪽으로 넘어간다.
   */
  { name: 'err-401-no-token', method: 'GET', path: '/api/usage/summary', auth: 'none' },
  { name: 'err-401-bad-token', method: 'GET', path: '/api/usage/summary', auth: 'bogus' },
  { name: 'err-403-intake-scope', method: 'GET', path: '/api/usage/summary', auth: 'intake' },
  { name: 'err-403-cookie-mutation', method: 'DELETE', path: '/api/usage/identity?machine=host-a', auth: 'cookie' },
  { name: 'err-400-bad-from', method: 'GET', path: '/api/usage/sessions?from=2026-8-1' },
  { name: 'err-400-bad-metric', method: 'GET', path: '/api/usage/series?metric=vibes' },
  { name: 'err-400-bad-interval', method: 'GET', path: '/api/usage/series?interval=minute' },
  { name: 'err-400-bad-sort', method: 'GET', path: '/api/usage/sessions?sort=nope' },
  { name: 'err-400-bad-groupby', method: 'GET', path: '/api/usage/series?group_by=zodiac' },
  { name: 'err-404-unknown-session', method: 'GET', path: '/api/usage/sessions/does-not-exist' },
  { name: 'err-404-unknown-path', method: 'GET', path: '/api/usage/nope' },
  // 쿠키는 조회만 태운다 — 그러나 GET 은 통과해야 한다(게이트가 과하게 잠기지 않았는지).
  { name: 'cookie-read-ok', method: 'GET', path: '/api/usage/summary?days=365&top=3', auth: 'cookie' },
]);

/*
 * ── 정규화 ────────────────────────────────────────────────────────────
 * 시각처럼 **호출 시점마다 달라지는 값**은 비교 대상이 아니다. 남겨 두면 diff 가 매번 빨개져
 * 아무도 안 보게 되고, 그때 진짜 회귀가 그 소음에 묻힌다.
 *
 * ⚠ 값을 지우는 게 아니라 **타입·존재 여부는 검사하고 값만 접는다.** 필드가 통째로 사라지는
 *   회귀(포팅에서 흔하다)는 여전히 잡혀야 하기 때문이다.
 */
const VOLATILE = new Set([
  'now',            // coverage 응답의 현재 시각
  'reportedAt',     // 인테이크 시점 = 캡처 시점
  'lastReportedAt', // 위와 같음(머신별 MAX)
  'updatedAt',      // machine_identity.updated_at — 귀속 교정을 건 시점
  'at',             // 감사 로그 시각
  'ts',
]);

function fold(v) {
  if (v === null) return '<null>';
  if (Array.isArray(v)) return `<array:${v.length}>`;
  return `<${typeof v}>`;
}

export function normalize(v) {
  if (Array.isArray(v)) return v.map(normalize);
  if (v && typeof v === 'object') {
    const out = {};
    // 키 순서를 고정한다 — Go 의 map 은 순서가 없고 JS 객체는 삽입 순서라, 정렬하지 않으면
    // 내용이 같아도 diff 가 난다. 배열 순서는 **접지 않는다**(정렬은 계약의 일부다).
    for (const k of Object.keys(v).sort()) {
      out[k] = VOLATILE.has(k) ? fold(v[k]) : normalize(v[k]);
    }
    return out;
  }
  return v;
}

/* ── 요청 실행 ────────────────────────────────────────────────────────── */
export async function request(base, req, tokens) {
  const headers = {};
  const { admin, intake } = tokens;
  if (!req.auth || req.auth === 'admin') headers.Authorization = `Bearer ${admin}`;
  else if (req.auth === 'intake') headers.Authorization = `Bearer ${intake}`;
  else if (req.auth === 'bogus') headers.Authorization = 'Bearer 0000000000000000000000';
  else if (req.auth === 'cookie') headers.Cookie = `usage_tok=${encodeURIComponent(admin)}`;

  const res = await fetch(base + req.path, { method: req.method, headers });
  const text = await res.text();
  let body;
  try { body = JSON.parse(text); } catch { body = { '<non-json>': text.slice(0, 400) }; }
  return {
    status: res.status,
    // 화면과 수집기가 의존하는 헤더만 남긴다(전부 담으면 Date 등으로 매번 달라진다).
    contentType: res.headers.get('content-type') || '',
    wwwAuthenticate: res.headers.get('www-authenticate') || '',
    body: normalize(body),
  };
}

/* ── 시드 투입 ────────────────────────────────────────────────────────── */
export async function seed(base, token, { SEED, REPLAY, IDENTITY }) {
  const post = async (payload) => {
    const r = await fetch(`${base}/api/usage`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(`인테이크 실패 ${r.status}: ${JSON.stringify(j)}`);
    return j;
  };

  const first = [];
  for (const p of SEED) first.push(await post(p));
  // 멱등 확인 — 같은 페이로드를 한 번 더. 값이 두 배가 되면 upsert 가 아니라 누적이다.
  for (const p of REPLAY) await post(p);

  const idRes = await fetch(`${base}/api/usage/identity`, {
    method: 'PUT',
    headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
    body: JSON.stringify(IDENTITY),
  });
  const idJson = await idRes.json().catch(() => ({}));
  if (!idRes.ok) throw new Error(`귀속 교정 실패 ${idRes.status}: ${JSON.stringify(idJson)}`);

  return { intake: first, identity: normalize(idJson) };
}

/* ── 전체 캡처 ────────────────────────────────────────────────────────── */
export async function captureAll(base, tokens) {
  const out = {};
  for (const req of REQUESTS) {
    out[req.name] = { request: `${req.method} ${req.path}`, auth: req.auth || 'admin', ...await request(base, req, tokens) };
  }
  return out;
}
