'use strict';
/*
 * 분포 계산 — 합계가 감추는 것을 드러낸다.
 *
 * 왜 필요한가: 사용량 화면이 합계만 보여주면 "평균적인 세션"과 "이상한 세션"이 같은 숫자에
 * 섞인다. 2026-08-03 실측에서 4,314턴짜리 세션 하나가 23억 토큰을 썼는데, 그건 전체 합계의
 * 13% 였다 — 합계만 보면 그 세션이 존재한다는 사실 자체가 보이지 않는다. p95/p99 는 그 한 건을
 * 화면으로 끌어올리는 장치다.
 *
 * ── 왜 SQL 이 아니라 JS 인가 ─────────────────────────────────────────────
 * PostgreSQL 의 percentile_cont 를 쓰면 SQLite 경로가 갈라진다(이 프로젝트는 두 방언을 함께
 * 문다 — lib/db 참조). 방언 분기를 하나 더 만드는 비용보다, 세션 행을 받아 JS 에서 정렬하는
 * 비용이 싸다. 행 수는 호출부가 상한을 걸어 넘긴다(usage/store.js sessionRows 의 limit).
 *
 * 순수 함수만 둔다 — DB·설정·시간에 의존하지 않는다.
 */

/* 기본 분위. 화면이 쓰는 셋을 여기 한 곳에 둔다. */
const DEFAULT_PS = Object.freeze([0.5, 0.95, 0.99]);

/*
 * 선형 보간 분위수(PostgreSQL percentile_cont 와 같은 방식).
 *
 * 최근접 순위(nearest-rank)를 쓰지 않는 이유: 표본이 적을 때 p95 와 p99 가 같은 값으로 붙어
 * 화면에서 두 열이 똑같아 보인다. 보간을 쓰면 적은 표본에서도 두 분위가 갈린다.
 *
 * @param {number[]} sorted  **오름차순 정렬된** 유한수 배열
 * @param {number} p         0~1
 * @returns {number|null}    빈 배열이면 null
 */
function quantileSorted(sorted, p) {
  const n = sorted.length;
  if (!n) return null;
  if (n === 1) return sorted[0];
  const q = Math.max(0, Math.min(1, Number(p) || 0));
  const idx = (n - 1) * q;
  const lo = Math.floor(idx);
  const hi = Math.ceil(idx);
  if (lo === hi) return sorted[lo];
  return sorted[lo] + (sorted[hi] - sorted[lo]) * (idx - lo);
}

/*
 * 값 배열 → 분포 요약.
 *
 * 값이 아닌 것은 **버리고**, 0 은 버리지 않는다 — 0 턴 세션이나 0 원 세션은 실재하는 관측이고,
 * 그걸 빼면 분포가 낙관적으로 왜곡된다. 버린 개수는 `dropped` 로 돌려 호출부가 표본이 얼마나
 * 깎였는지 알 수 있게 한다.
 *
 * ⚠ **null 을 0 으로 세지 않는다.** `Number(null)` 은 0 이라, 값 그대로 Number() 에 넣으면
 *   "값 없음"이 "0 이라는 관측"으로 둔갑해 분포를 아래로 당긴다(단위 테스트가 실제로 잡은 자리).
 *   같은 이유로 boolean·배열·객체도 거른다 — Number(false)·Number([]) 가 전부 0 이다.
 *   숫자 문자열('1')은 살린다: DB 드라이버가 bigint 를 문자열로 주는 경로가 있다.
 *
 * @returns {{n:number, dropped:number, min:number|null, max:number|null,
 *            avg:number|null, p:Object<string, number|null>}}
 */
function summarize(values = [], ps = DEFAULT_PS) {
  const clean = [];
  let dropped = 0;
  for (const v of (Array.isArray(values) ? values : [])) {
    // 숫자이거나 비어 있지 않은 문자열만 후보로 삼는다(위 ⚠ 참조).
    const isCandidate = typeof v === 'number' || (typeof v === 'string' && v.trim() !== '');
    if (!isCandidate) { dropped += 1; continue; }
    const n = Number(v);
    if (Number.isFinite(n)) clean.push(n);
    else dropped += 1;
  }
  if (!clean.length) {
    return { n: 0, dropped, min: null, max: null, avg: null, p: {} };
  }
  clean.sort((a, b) => a - b);
  const sum = clean.reduce((a, b) => a + b, 0);
  const p = {};
  for (const q of (Array.isArray(ps) && ps.length ? ps : DEFAULT_PS)) {
    // 키는 'p50'·'p95'·'p99' 처럼 읽히게 — 화면이 그대로 쓴다.
    p[`p${Math.round(Number(q) * 100)}`] = quantileSorted(clean, q);
  }
  return {
    n: clean.length,
    dropped,
    min: clean[0],
    max: clean[clean.length - 1],
    avg: sum / clean.length,
    p,
  };
}

module.exports = { summarize, quantileSorted, DEFAULT_PS };
