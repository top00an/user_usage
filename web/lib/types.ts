/*
 * 응답 타입 — `contract/golden/` 44개 스냅샷에서 그대로 뽑았다. **추측한 필드가 없다.**
 *
 * 이 파일은 계약의 사본이지 계약 자체가 아니다. 화면에 필요한데 여기 없는 값이 생기면
 * 타입을 늘리는 것이 아니라 PM 에게 보고한다(응답 shape 는 동결됐다).
 *
 * 낙관적으로 넓게 잡은 자리가 두 곳 있고, 둘 다 이유가 있다:
 *   · `fromSeries`/`fromSession` 은 **없을 수 있다**(구 서버·구 스텁). 없으면 근거 열을 그리지
 *     않는다 — 화면이 새 필드를 요구하지 않는다.
 *   · `noTsUnknown` 같은 신규 필드도 optional 이다. 0 과 "모른다"를 뭉치면 구버전 수집기가
 *     "문제 없음"으로 단정된다.
 */

/** 네 축은 언제나 같이 다닌다 — 하나만 떼어 쓰면 "입력"이 무엇인지 다시 흐려진다. */
export interface TokenAxes {
  input: number;
  output: number;
  cacheRead: number;
  cacheCreate: number;
}

/* ── GET /api/usage/summary ─────────────────────────────────────────── */

export interface Totals extends TokenAxes {
  sessions: number;
  users: number;
  machines: number;
}

export interface DayRow extends TokenAxes {
  day: string;
  sessions: number;
}

export interface UserRow extends TokenAxes {
  username: string;
  turns: number;
  sessions: number;
}

/** series 로 정확히 집힌 몫 / 세션 최빈 모델에 통째로 귀속된 몫. */
export interface ModelBasis extends TokenAxes {
  sessions: number;
}

export interface ModelRow extends TokenAxes {
  model: string;
  sessions: number;
  fromSeries?: ModelBasis;
  fromSession?: ModelBasis;
}

export interface ModelAxisUser {
  username: string;
  sessions: number;
  withSeries: number;
  noTsTurns?: number;
  noTsUnknown?: number;
}

export interface ModelAxis {
  sessions: number;
  withSeries: number;
  overSessions?: number;
  users: ModelAxisUser[];
}

export interface KeyRow {
  key: string;
  count: number;
  users: number;
  sessions: number;
}

export type CounterKind = 'bash' | 'tool' | 'mcp' | 'skill' | 'agent' | 'slash' | 'keyword';

export interface RecommendationSummary {
  total: number;
  miss: number;
  gaps: { token: string; count: number }[];
}

export interface Summary {
  totals: Totals;
  byDay: DayRow[];
  byUser: UserRow[];
  byModel: ModelRow[];
  modelAxis?: ModelAxis;
  top: Partial<Record<CounterKind, KeyRow[]>>;
  recommendation?: RecommendationSummary;
  /** keywordDays 는 null 일 수 있다 — 그러면 "무기한 보관"이고, 화면이 그렇게 말해야 한다. */
  retention?: { keywordDays: number | null };
}

/* ── GET /api/usage/dispatch ────────────────────────────────────────── */

export interface DispatchItem {
  key: string;
  count: number;
}

export interface DispatchAgentUser {
  username: string;
  role: number;
  generic: number;
  total: number;
  items: DispatchItem[];
}

export interface DispatchSkillUser {
  username: string;
  total: number;
  items: DispatchItem[];
}

export interface Dispatch {
  agents: DispatchAgentUser[];
  skills: DispatchSkillUser[];
  generic: string[];
}

/* ── GET /api/usage/distribution ────────────────────────────────────── */

export interface Sample {
  limit: number;
  rows: number;
  truncated: boolean;
}

export interface Quantiles {
  n: number;
  min: number;
  max: number;
  avg: number;
  dropped: number;
  p: { p50: number; p95: number; p99: number };
}

export type DistributionKey =
  | 'cacheReadPerTurn'
  | 'sessionCostUsd'
  | 'turnsPerSession'
  | 'cacheHitRate';

export interface Distribution {
  distributions: Partial<Record<DistributionKey, Quantiles>>;
  pricedAt: string;
  sample: Sample;
  unpriced: string[];
}

/* ── GET /api/usage/sessions ────────────────────────────────────────── */

export interface SessionRow extends TokenAxes {
  sessionId: string;
  username: string | null;
  machine: string;
  model: string | null;
  project: string | null;
  turns: number;
  startedAt: string | null;
  endedAt: string | null;
  reportedAt: string;
  webFetch: number;
  webSearch: number;
  priced: boolean;
  usd: number;
}

export interface SessionsResponse {
  sessions: SessionRow[];
  sort: string;
  usd: number;
  byAxis: TokenAxes;
  pricedAt: string;
  sample: Sample;
  /** 단가표에 없는 모델 이름. 조용히 $0 으로 처리하지 않는다는 증거다. */
  unpriced: string[];
  /** 캐시 TTL 을 모르는 행 수. 그만큼 캐시생성 단가가 낮게(=최대 1.6배 과소) 잡힌다. */
  ttlUnknownRows: number;
}

/* ── GET /api/usage/sessions/{id} ───────────────────────────────────── */

export interface SeriesBucket extends TokenAxes {
  sessionId: string;
  hour: string;
  model: string;
  machine: string;
  username: string | null;
  project: string | null;
  turns: number;
  cacheCreate5m: number;
  cacheCreate1h: number;
  latencyMsSum: number;
  latencyMsMax: number;
  latencyTurns: number;
  toolErrors: number;
  stopMaxTokens: number;
  stopRefusal: number;
}

export interface SessionCost {
  usd: number;
  byAxis: TokenAxes;
  priced: boolean;
  /** false = 세션 최빈 모델 1개로 계산한 **근사**다. 화면이 그 사실을 말해야 한다. */
  exact: boolean;
  pricedAt: string;
}

export interface SessionDetail {
  session: SessionRow;
  series: SeriesBucket[];
  counters: Partial<Record<CounterKind, DispatchItem[]>>;
  cost: SessionCost;
  note: string;
}

/* ── GET /api/usage/quality ─────────────────────────────────────────── */

export interface Quality {
  turns: number;
  toolErrors: number;
  toolErrorRate: number;
  stopRefusal: number;
  refusalRate: number;
  stopMaxTokens: number;
  stopMaxRate: number;
  latencyAvgMs: number;
  latencyMaxMs: number;
  latencyTurns: number;
  coverage: { sessionsTotal: number; sessionsWithSeries: number };
  sample: Sample;
}

/* ── GET /api/usage/coverage ────────────────────────────────────────── */

export interface Reporter {
  machine: string;
  username: string | null;
  sessions: number;
  lastReportedAt: string | null;
  lastStartedAt: string | null;
  /** true = 시간 버킷까지 보내는 신수집기(품질 지표에 기여). */
  sendsSeries: boolean;
}

export interface Coverage {
  now: string;
  reporters: Reporter[];
}

/* ── GET /api/usage/leaderboard ─────────────────────────────────────── */

export interface LeaderboardUser {
  username: string | null;
  sessions: number;
  turns: number;
  tokens: number;
  cacheHitRate: number;
  usd: number;
  usdPerSession: number;
  priced: boolean;
}

export interface Leaderboard {
  users: LeaderboardUser[];
  pricedAt: string;
  sample: Sample;
  unpriced: string[];
}

/* ── GET /api/usage/series ──────────────────────────────────────────── */

export interface SeriesPoint {
  t: string;
  v: number;
}

export interface SeriesLine {
  key?: Record<string, string>;
  label: string;
  points: SeriesPoint[];
  total: number;
}

export interface SeriesResponse {
  metric: 'cost' | 'tokens' | 'sessions' | 'turns';
  interval: 'hour' | 'day' | 'week';
  groupBy: string[];
  series: SeriesLine[];
  timezone: string;
  pricedAt: string;
  sample: Sample;
  unpriced: string[];
  attribution: string;
  modelAttribution: string;
  coverage?: { sessionsTotal: number; sessionsWithSeries: number };
}

/* ── 서버 오류 본문 ─────────────────────────────────────────────────── */

export interface ErrorBody {
  error?: string;
}
