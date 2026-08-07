'use strict';
/*
 * 사용량 → 비용 환산.
 *
 * 왜 필요한가(실측): 화면이 토큰 4축을 원자료 그대로 보여주는 동안, 같은 사용량을 두고 도구마다
 * 176배 차이 나는 숫자가 나왔다. 원인은 화면이 가장 크게 띄운 값이 **출력 토큰 하나**였다는 것이다.
 * 실제 비용 구성은 정반대다(한 사용자의 한 달, opus 급 모델):
 *
 *   캐시읽기 4,468 MTok → $2,234 (64.5%)   ← 작은 글씨로 밀려 있던 축
 *   캐시생성   136 MTok →   $848 (24.5%)
 *   출력        15 MTok →   $381 (11.0%)   ← 화면이 제일 크게 보여주던 축
 *   입력      0.06 MTok →     $0.31
 *
 * 토큰 수가 큰 축과 비용이 큰 축이 다르다. 축마다 단가 배수가 다르기 때문이고, 그래서 "총 토큰량"
 * 합산은 비용도 작업량도 대변하지 못한다. 이 모듈이 그 환산을 한 곳에서 책임진다.
 *
 * 원칙:
 *  - 호출 시점에 읽는다(require 캐시 금지 — USAGE_CONFIG 테스트 격리 보존).
 *  - 절대 throw 하지 않는다 — 블록이 없거나 깨져도 시드로 수렴한다.
 *  - 비밀 없음(순수 정책값).
 *
 * ── 저장하지 않고 **읽기 시점에 계산한다** ──────────────────────────────
 * 비용을 컬럼으로 굳히면 단가가 바뀌었을 때 과거 수치가 옛 단가에 묶인다. 단가는 실제로 바뀐다
 * (아래 Sonnet 5 도입가 참조). 원자료는 토큰만 남기고 환산은 매번 다시 한다.
 */
const fs = require('fs');
const path = require('path');

const APP = path.join(__dirname, '..');

/*
 * 시드 단가 — USD / 1M 토큰. 출처: Anthropic 공개 가격표(2026-06-24 기준).
 *
 * **이 표는 낡는다.** config.json 의 `usage.pricing` 이 이기므로, 단가가 바뀌면 코드가 아니라
 * 설정을 고친다. 화면은 `pricedAt` 을 함께 표시해 이 표가 언제 것인지 보이게 한다.
 *
 * ⚠ Claude Sonnet 5 는 **기간 한정 도입가**($2/$10, 2026-08-31 까지)가 있다. `intro` 로 적어
 *   날짜에 따라 자동으로 갈린다 — 어느 쪽을 시드에 박아도 한쪽 기간에는 틀리기 때문이다.
 *   정가를 박으면 도입 기간 중 1.5배 과대, 도입가를 박으면 9/1 부터 33% 과소가 된다.
 *   기간이 끝나면 `intro` 를 지우기만 하면 된다(지우지 않아도 만료일이 지나면 정가를 쓴다).
 */
const SEED = Object.freeze({
  'claude-opus-5': { input: 5, output: 25 },
  'claude-opus-4-8': { input: 5, output: 25 },
  'claude-opus-4-7': { input: 5, output: 25 },
  'claude-opus-4-6': { input: 5, output: 25 },
  'claude-opus-4-5': { input: 5, output: 25 },
  'claude-sonnet-5': { input: 3, output: 15, intro: { until: '2026-08-31', input: 2, output: 10 } },
  'claude-sonnet-4-6': { input: 3, output: 15 },
  'claude-sonnet-4-5': { input: 3, output: 15 },
  'claude-fable-5': { input: 10, output: 50 },
  'claude-mythos-5': { input: 10, output: 50 },
  'claude-haiku-4-5': { input: 1, output: 5 },
});

// 공급자 공식 가격표와 대조·검증한 날짜.
const SEED_PRICED_AT = '2026-08-04';

/*
 * 캐시 축의 입력가 대비 배수.
 *
 * 캐시 읽기는 입력가의 약 0.1배, 캐시 생성은 **5분 TTL 이면 1.25배·1시간 TTL 이면 2배**다.
 * 배수가 1.6배 차이 나므로 TTL 을 뭉뚱그리면 비용이 그만큼 틀린다.
 *
 * ── TTL 은 추정하지 않는다. 트랜스크립트에 적혀 있다 ─────────────────────
 * 처음엔 "수집기가 TTL 을 담지 않으니 5분으로 계산한다"고 두었는데, 실측해 보니 틀린 전제였다.
 * assistant 턴의 usage 블록에는 다음이 **100%(9,345/9,345) 들어 있다**:
 *     usage.cache_creation = { ephemeral_5m_input_tokens, ephemeral_1h_input_tokens }
 * 같은 표본에서 캐시 생성 46.2 MTok 의 **100% 가 1시간 TTL** 이었다. 5분(1.25배)으로 계산하던
 * 값은 실제의 1/1.60 이었다 — 캐시 생성이 비용의 약 24% 를 차지하므로 총비용이 눈에 띄게
 * 낮게 나오고 있었다.
 *
 * 그래서 규칙은 **분해값 우선, 없으면 폴백**이다:
 *   ① cacheCreate5m / cacheCreate1h 의 합이 0 보다 크면 그대로 각각의 배수로 계산한다.
 *   ② 둘 다 없으면(= 구버전 수집기가 보낸 행) 총량에 5분 배수를 적용하고 `ttlKnown:false` 로
 *      표시한다. 화면이 "이 값은 과소 추정일 수 있다"고 말할 수 있어야 하기 때문이다.
 *      과거 행에 TTL 을 소급해 지어내지 않는다 — 모르는 것은 모른다고 내보낸다.
 *
 * 분해값과 총량이 어긋난 적은 표본에서 **0회**였고(9,320건 일치), 분해값만 있고 총량이 0 인
 * 경우가 4건 있었다. 그래서 ①을 먼저 본다.
 */
const CACHE_READ_MULT = 0.1;
const CACHE_CREATE_MULT = 1.25;      // 5분 TTL
const CACHE_CREATE_1H_MULT = 2;      // 1시간 TTL
const MULT_MIN = 0;
const MULT_MAX = 10;

function readBlock() {
  const p = process.env.USAGE_CONFIG ? path.resolve(process.env.USAGE_CONFIG) : path.join(APP, 'config.json');
  try {
    const c = JSON.parse(fs.readFileSync(p, 'utf8')).usage;
    return c && typeof c === 'object' && !Array.isArray(c) ? c : {};
  } catch {
    return {};
  }
}

// 0 이상 유한수만 인정. 그 외는 기본값으로 수렴한다(설정 오타가 비용을 0 으로 만들지 않게).
function rate(v, dflt) {
  if (v === undefined || v === null || v === '') return dflt;
  const n = Number(v);
  if (!Number.isFinite(n) || n < 0) return dflt;
  return n;
}

/*
 * 배수는 **2단으로** 방어한다 — 두 오류가 성격이 다르기 때문이다.
 *   무효값(음수·비수치·빈값) → 기본값. 배수로 성립하지 않는 입력이라 살릴 의도가 없다.
 *     여기서 0 으로 클램프하면 "캐시읽기가 공짜"가 되어 비용이 조용히 사라진다.
 *   범위 초과(예: 9999)  → 상한으로 클램프. 의도("아주 높게")는 살리되 폭발 반경만 자른다.
 */
function clampMult(v, dflt) {
  const n = rate(v, dflt);
  return Math.max(MULT_MIN, Math.min(MULT_MAX, n));
}

/*
 * 모델 이름 정규화 — 표 조회 전에 꼬리를 떼어낸다.
 *   'claude-opus-5[1m]'        → 'claude-opus-5'   (컨텍스트 변형 접미사)
 *   'claude-opus-4-5-20251101' → 'claude-opus-4-5' (날짜 스냅샷)
 * 벤더 접두사가 붙은 것(anthropic.claude-opus-5)도 같은 모델이므로 떼어낸다.
 * 이 정규화가 실패해도 문제되지 않는다 — 표에 없으면 priced:false 로 정직하게 나간다.
 */
function normalizeModel(model) {
  let s = String(model == null ? '' : model).trim().toLowerCase();
  if (!s) return '';
  s = s.replace(/\[[^\]]*\]$/, '');           // [1m] 류 접미사
  s = s.replace(/-\d{8}$/, '');               // -20251101 류 날짜 스냅샷
  s = s.replace(/^anthropic\./, '');          // Bedrock 접두사
  return s.trim();
}

/*
 * 시드 + config 오버라이드. 깨진 항목은 무시하고 시드를 유지한다(부분 오버라이드 허용).
 *
 * @param {string} [today] 'YYYY-MM-DD'. 기간 한정 도입가를 어느 날짜 기준으로 볼지.
 *   기본은 오늘(UTC)이다. **테스트가 날짜를 고정할 수 있어야** 하므로 인자로 받는다 —
 *   안 그러면 2026-09-01 에 테스트가 저절로 빨개진다.
 */
function pricing(today) {
  const day = /^\d{4}-\d{2}-\d{2}$/.test(String(today || ''))
    ? String(today)
    : new Date().toISOString().slice(0, 10);
  const table = {};
  for (const [model, v] of Object.entries(SEED)) {
    // 도입가는 만료일**까지 포함**이다(공식 표기 "2026년 8월 31일까지").
    const useIntro = v.intro && day <= v.intro.until;
    table[model] = useIntro
      ? { input: v.intro.input, output: v.intro.output }
      : { input: v.input, output: v.output };
  }
  const over = readBlock().pricing;
  if (over && typeof over === 'object' && !Array.isArray(over)) {
    for (const [model, v] of Object.entries(over)) {
      if (!v || typeof v !== 'object') continue;
      const input = rate(v.input, null);
      const output = rate(v.output, null);
      if (input === null || output === null) continue; // 한쪽만 온 항목은 통째로 무시
      table[normalizeModel(model)] = { input, output };
    }
  }
  return table;
}

/* 이 단가표가 언제 것인지. 화면이 그대로 표시해 낡은 값이 보이게 한다. */
function pricedAt() {
  const v = readBlock().pricedAt;
  return typeof v === 'string' && v.trim() ? v.trim() : SEED_PRICED_AT;
}

function multipliers() {
  const b = readBlock();
  return {
    cacheRead: clampMult(b.cacheReadMult, CACHE_READ_MULT),
    cacheCreate: clampMult(b.cacheCreateMult, CACHE_CREATE_MULT),
    cacheCreate1h: clampMult(b.cacheCreate1hMult, CACHE_CREATE_1H_MULT),
  };
}

const MTOK = 1e6;
function tok(v) { const n = Number(v); return Number.isFinite(n) && n > 0 ? n : 0; }

/*
 * 한 행(세션·시간버킷·집계 무엇이든)의 비용.
 *
 * 반환의 `priced` 가 이 함수의 핵심이다. 단가표에 없는 모델을 0 원으로 계산하면 비용이 화면에서
 * **사라진다** — 사내 게이트웨이로 도는 서드파티 모델이 실제로 그 자리에 있다. 0 으로 숨기지 않고
 * priced:false 로 올려 보내 화면이 "단가 미등록"으로 말하게 한다.
 *
 * `ttlKnown` 은 캐시 생성 비용의 신뢰도다. false 면 TTL 분해값이 없어 5분으로 가정했다는
 * 뜻이고, 그 행의 캐시 생성 비용은 최대 1.6배까지 과소 추정일 수 있다.
 *
 * @returns {{usd:number, byAxis:{input:number,output:number,cacheRead:number,cacheCreate:number},
 *            priced:boolean, ttlKnown:boolean, model:string}}
 */
function costOf(row = {}, table = null, mult = null) {
  const t = table || pricing();
  const m = mult || multipliers();
  const model = normalizeModel(row.model);
  const p = t[model];

  // TTL 분해값이 있으면 그것이 진실이다. 없을 때만 총량 + 5분 가정으로 내려간다.
  const cc5 = tok(row.cacheCreate5m);
  const cc1h = tok(row.cacheCreate1h);
  const ttlKnown = cc5 + cc1h > 0;

  const zero = { input: 0, output: 0, cacheRead: 0, cacheCreate: 0 };
  if (!p) return { usd: 0, byAxis: zero, priced: false, ttlKnown, model };

  const cacheCreateUsd = ttlKnown
    ? ((cc5 * m.cacheCreate) + (cc1h * m.cacheCreate1h)) * p.input / MTOK
    : (tok(row.cacheCreate) * p.input * m.cacheCreate) / MTOK;

  const byAxis = {
    input: (tok(row.input) * p.input) / MTOK,
    output: (tok(row.output) * p.output) / MTOK,
    cacheRead: (tok(row.cacheRead) * p.input * m.cacheRead) / MTOK,
    cacheCreate: cacheCreateUsd,
  };
  const usd = byAxis.input + byAxis.output + byAxis.cacheRead + byAxis.cacheCreate;
  return { usd, byAxis, priced: true, ttlKnown, model };
}

/*
 * 여러 행의 합계. 단가표·배수를 한 번만 읽고 돌린다(행마다 config.json 을 다시 읽지 않게).
 *
 * `unpriced` 는 단가 미등록 모델의 목록이다. 비어 있지 않으면 화면이 "이 모델들은 비용에서
 * 빠져 있다"고 말해야 한다 — 합계만 보여주면 누락이 보이지 않는다.
 */
function summarize(rows = []) {
  const table = pricing();
  const mult = multipliers();
  const byAxis = { input: 0, output: 0, cacheRead: 0, cacheCreate: 0 };
  const unpriced = new Set();
  let usd = 0;
  /*
   * TTL 을 모르는 행이 섞여 있으면 캐시 생성 비용이 과소 추정된다. 몇 건인지 세어 올려 보내
   * 화면이 "이 합계 중 N건은 TTL 미상"이라고 말할 수 있게 한다. 구버전 수집기를 쓰는 팀원이
   * 남아 있는 한 이 값은 0 이 아니다.
   */
  let ttlUnknownRows = 0;

  for (const r of (Array.isArray(rows) ? rows : [])) {
    const c = costOf(r, table, mult);
    if (!c.priced) { if (c.model) unpriced.add(c.model); continue; }
    if (!c.ttlKnown && tok(r.cacheCreate) > 0) ttlUnknownRows += 1;
    usd += c.usd;
    byAxis.input += c.byAxis.input;
    byAxis.output += c.byAxis.output;
    byAxis.cacheRead += c.byAxis.cacheRead;
    byAxis.cacheCreate += c.byAxis.cacheCreate;
  }

  return { usd, byAxis, unpriced: [...unpriced].sort(), ttlUnknownRows, pricedAt: pricedAt() };
}

module.exports = {
  costOf, summarize, pricing, pricedAt, multipliers, normalizeModel,
  SEED, SEED_PRICED_AT, CACHE_READ_MULT, CACHE_CREATE_MULT, CACHE_CREATE_1H_MULT,
};
