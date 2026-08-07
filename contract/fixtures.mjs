/*
 * 계약 동결용 시드 — **결정적(deterministic)** 이어야 한다.
 *
 * 이 데이터의 목적은 "그럴듯한 사용량"이 아니라 **포팅이 틀리기 쉬운 자리를 전부 밟는 것**이다.
 * 아래 각 세션은 하나씩 특정 함정을 담당한다. 하나라도 지우면 그 함정이 검증에서 빠진다.
 *
 *   S1 alice   series 완전 — 모델별 정확값(fromSeries) 경로
 *   S2 bob     series 없음 — 최빈 모델 귀속(fromSession) 경로. 버려지면 총합이 준다
 *   S3 carol   series 안에 **모델이 섞임** — 세션 최빈 모델로 GROUP BY 하면 오귀속되는 바로 그 행
 *   S4 carol   noTsTurns > 0 — series 가 덮지 못한 잔여를 최빈 모델에 귀속(③ 경로)
 *   S5 dave    모르는 모델 — unpriced 목록에 떠야 한다(조용히 0원 처리하면 안 된다)
 *   S6 alice   cacheCreate 는 있는데 cc1h 가 없음 — ttlUnknownRows 카운트 대상
 *   S7 erin    턴 0 · 토큰 0 — 0 나눗셈 방어(cacheHitRate·usdPerSession·perTurn)
 *   S8 (미상)  username 없음 — COALESCE '(미상)' 폴백
 *
 * 날짜는 **절대값 고정**이다. "오늘 기준 N일 전"으로 만들면 골든이 매일 달라진다.
 * usageByDay 는 날짜 필터 없이 최근 N일을 집는 쿼리라(LIMIT 365) 고정 날짜로도 안정적이다.
 */

/* 모든 조회에 명시적으로 실어 "오늘"에 의존하지 않게 하는 창(窓). */
export const WINDOW = Object.freeze({ from: '2026-07-28', to: '2026-08-05' });

const M_SONNET = 'claude-sonnet-5';
const M_OPUS = 'claude-opus-5';
const M_HAIKU = 'claude-haiku-4-5-20251001';
const M_UNKNOWN = 'some-unreleased-model-x';

export const SEED = Object.freeze([
  /* ── 보고 1: alice / host-a ─────────────────────────────────────────── */
  {
    user: 'alice',
    machine: 'host-a',
    sessions: [
      {
        // S1 — series 완전. 시간 2개 × 모델 1개. 모델별 정확값의 근거.
        sessionId: 'S1-alice-series-full',
        model: M_SONNET,
        project: 'orca-core',
        input: 1200, output: 3400, cacheRead: 88000, cacheCreate: 5200,
        turns: 12, startedAt: '2026-07-30T01:00:00.000Z', noTsTurns: 0,
        counters: {
          tool: { Read: 40, Edit: 12, Bash: 22 },
          bash: { git: 9, npm: 4, pytest: 3 },
          slash: { '/clear': 2, '/loop': 1 },
          skill: { 'superpowers:test-driven-development': 3 },
          agent: { 'backend-engineer': 2, 'general-purpose': 1 },
          mcp: { 'team-cms:cms_get_kb': 5 },
          keyword: { 리팩터: 4, 테스트: 7 },
        },
        series: [
          { hour: '2026-07-30T01', model: M_SONNET, input: 700, output: 2000, cacheRead: 50000, cacheCreate: 5200, cc1h: 5200, turns: 7, toolErrors: 1, latencyMsSum: 42000, latencyTurns: 7, latencyMsMax: 9100 },
          { hour: '2026-07-30T02', model: M_SONNET, input: 500, output: 1400, cacheRead: 38000, cacheCreate: 0, cc1h: 0, turns: 5, toolErrors: 0, latencyMsSum: 21000, latencyTurns: 5, latencyMsMax: 6200 },
        ],
      },
      {
        // S6 — cacheCreate 는 있는데 cc1h 가 없다(구수집기). TTL 미상 → ttlUnknownRows 대상.
        sessionId: 'S6-alice-ttl-unknown',
        model: M_HAIKU,
        project: 'orca-core',
        input: 300, output: 900, cacheRead: 4000, cacheCreate: 2400,
        turns: 4, startedAt: '2026-08-01T05:30:00.000Z',
        counters: { tool: { Read: 6 }, keyword: { 배포: 2 } },
        // series 없음 → 세션 행으로 비용 계산 + TTL 5분 가정
      },
    ],
  },

  /* ── 보고 2: bob / host-b — series 를 아예 안 보내는 구버전 수집기 ──── */
  {
    user: 'bob',
    machine: 'host-b',
    sessions: [
      {
        // S2 — series 없음. 모델별은 최빈 모델 귀속(fromSession). 이 몫이 사라지면 총합이 준다.
        sessionId: 'S2-bob-no-series',
        model: M_OPUS,
        project: 'tscorp-web',
        input: 2400, output: 8100, cacheRead: 150000, cacheCreate: 9000,
        turns: 21, startedAt: '2026-07-31T08:00:00.000Z',
        counters: {
          tool: { Read: 55, Write: 8 },
          bash: { docker: 6, git: 14 },
          agent: { 'general-purpose': 9 },   // 역할 없는 범용 — dispatch 의 generic 축
          keyword: { 마이그레이션: 3 },
        },
      },
    ],
  },

  /* ── 보고 3: carol / host-c — 모델 혼합과 잔여 턴 ────────────────────── */
  {
    user: 'carol',
    machine: 'host-c',
    sessions: [
      {
        // S3 — 세션 최빈 모델은 opus 인데 series 에는 opus·sonnet·haiku 가 섞여 있다.
        // 세션 model 로 GROUP BY 하면 sonnet/haiku 몫이 통째로 opus 행에 들어간다(오귀속).
        sessionId: 'S3-carol-mixed-models',
        model: M_OPUS,
        project: 'tscorp-web',
        input: 3000, output: 6000, cacheRead: 220000, cacheCreate: 12000,
        turns: 30, startedAt: '2026-08-02T03:00:00.000Z', noTsTurns: 0,
        counters: {
          tool: { Read: 70, Edit: 30, Grep: 15 },
          skill: { 'artifacts-builder': 2, 'apple-design': 1 },
          agent: { 'frontend-engineer': 4, 'design-engineer': 2, Explore: 3 },
          mcp: { 'sequential-thinking:sequentialthinking': 11 },
        },
        series: [
          { hour: '2026-08-02T03', model: M_OPUS, input: 1500, output: 3000, cacheRead: 120000, cacheCreate: 12000, cc1h: 12000, turns: 15, toolErrors: 2, stopMaxTokens: 1, latencyMsSum: 95000, latencyTurns: 15, latencyMsMax: 14000 },
          { hour: '2026-08-02T04', model: M_SONNET, input: 1000, output: 2200, cacheRead: 70000, cacheCreate: 0, cc1h: 0, turns: 10, toolErrors: 0, latencyMsSum: 40000, latencyTurns: 10, latencyMsMax: 7000 },
          { hour: '2026-08-02T05', model: M_HAIKU, input: 500, output: 800, cacheRead: 30000, cacheCreate: 0, cc1h: 0, turns: 5, stopRefusal: 1, latencyMsSum: 9000, latencyTurns: 5, latencyMsMax: 2600 },
        ],
      },
      {
        // S4 — series 가 세션 총량을 다 덮지 못한다(noTsTurns). 잔여는 최빈 모델로 간다(③).
        // ①+②+③ = usage_sessions 총합 이 깨지면 모델별만 작아져 "유실"로 보인다.
        sessionId: 'S4-carol-residual-turns',
        model: M_SONNET,
        project: 'orca-core',
        input: 1000, output: 2000, cacheRead: 40000, cacheCreate: 1000,
        turns: 10, startedAt: '2026-08-03T09:00:00.000Z',
        noTsTurns: 4,   // 시각이 없어 버킷에 못 올린 턴
        counters: { tool: { Read: 12 }, bash: { make: 2 } },
        series: [
          // 6턴어치만 버킷에 있다. 나머지 4턴 몫이 ③ 경로로 흘러야 한다.
          { hour: '2026-08-03T09', model: M_SONNET, input: 600, output: 1200, cacheRead: 24000, cacheCreate: 1000, cc1h: 0, turns: 6, latencyMsSum: 18000, latencyTurns: 6, latencyMsMax: 4000 },
        ],
      },
    ],
  },

  /* ── 보고 4: dave / host-d — 단가표에 없는 모델 ──────────────────────── */
  {
    user: 'dave',
    machine: 'host-d',
    sessions: [
      {
        // S5 — 모르는 모델. 비용은 계산하지 않고(priced:false) unpriced 에 이름이 떠야 한다.
        // 조용히 $0 으로 처리하면 합계가 틀렸다는 사실이 화면에서 사라진다.
        sessionId: 'S5-dave-unpriced-model',
        model: M_UNKNOWN,
        project: 'lab',
        input: 500, output: 1500, cacheRead: 20000, cacheCreate: 800,
        turns: 5, startedAt: '2026-08-04T11:00:00.000Z',
        counters: { tool: { Read: 3 }, keyword: { 실험: 1 } },
      },
    ],
  },

  /* ── 보고 5: erin / host-e — 0 나눗셈 방어 ───────────────────────────── */
  {
    user: 'erin',
    machine: 'host-e',
    sessions: [
      {
        // S7 — 턴 0, 토큰 0. perTurn·cacheHitRate·usdPerSession 이 전부 0 분모다.
        sessionId: 'S7-erin-zero-session',
        model: M_SONNET,
        project: 'lab',
        input: 0, output: 0, cacheRead: 0, cacheCreate: 0,
        turns: 0, startedAt: '2026-08-05T00:00:00.000Z',
        counters: {},
      },
    ],
  },

  /* ── 보고 6: username 없음 — '(미상)' 폴백 ──────────────────────────── */
  {
    machine: 'host-f',
    sessions: [
      {
        // S8 — user 도 username 도 없다. COALESCE 폴백이 도는 자리.
        sessionId: 'S8-anonymous-session',
        model: M_HAIKU,
        project: 'unknown',
        input: 100, output: 200, cacheRead: 1000, cacheCreate: 0,
        turns: 2, startedAt: '2026-07-28T12:00:00.000Z',
        counters: { tool: { Read: 1 } },
      },
    ],
  },
]);

/*
 * 멱등 검증용 — SEED 를 한 번 더 보낸다. 값이 두 배가 되면 upsert 가 아니라 누적이라는 뜻이다.
 * 수집기는 실패 시 재시도하는 best-effort 경로라 중복 전송이 **정상 동작에 포함된다.**
 */
export const REPLAY = SEED;

/*
 * 귀속 교정. host-f 의 익명 세션을 frank 에게 붙인다 — 과거 행 소급 재스탬프가 도는 자리다.
 * 캡처 순서상 이 뮤테이션 **뒤에** 다시 조회하는 스냅샷이 따로 있다.
 */
export const IDENTITY = Object.freeze({ machine: 'host-f', username: 'frank', note: '계약 동결 시드' });
