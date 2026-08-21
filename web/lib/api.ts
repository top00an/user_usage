/*
 * ── 유일한 서버 호출구 ────────────────────────────────────────────────────
 *
 * 이 파일 **밖에서는 fetch 를 부르지 않는다**(eslint 의 no-restricted-globals 가 강제한다).
 * 나중에 SSO·프록시로 갈아탈 때 고칠 자리가 여기 하나여야 하기 때문이다. 컴포넌트가 fetch 를
 * 직접 부르면 그 전환이 전면 수정이 된다.
 *
 * 자격증명은 **세션 쿠키**로 실린다. 로그인이 성공하면 서버가 httpOnly 쿠키를 Set-Cookie 로
 * 내려주고, 이후 모든 요청은 `credentials:'include'` 로 그 쿠키를 실어 보낸다 — JS 는 토큰을
 * 만지지 않는다(httpOnly 라 읽지도 못한다). 로그인/로그아웃/현재 사용자 확인은 auth 엔드포인트
 * 세 개로 모은다(login·logout·getMe).
 *
 * 실패는 **구조로** 남긴다(현행 public/js/core.js 의 fail 과 같은 계약):
 *   · status — 401(게이트 복귀) · 403(권한 안내) · 5xx(연결 실패)를 갈라야 하는 호출부가 있다
 *   · body   — 서버가 함께 보낸 응답 바디 전체
 * 에러 문구는 사람이 읽는 글이라 언제든 다듬어진다. 분기를 문구에 걸면 그때 화면이 조용히
 * 틀린 쪽으로 넘어간다 — 분기는 문자열이 아니라 status 로 한다(failureKind).
 */
import type {
  AdminUsers,
  Coverage,
  Dispatch,
  Distribution,
  ErrorBody,
  Dev,
  Leaderboard,
  PlatformsResponse,
  Quality,
  Seats,
  SeriesResponse,
  SessionDetail,
  SessionsResponse,
  Summary,
  Teams,
  UserDeletion,
  UserMutation,
  UserRole,
} from './types';

/**
 * 같은 오리진이 기본이다 — Go 바이너리가 정적 산출물과 API 를 함께 서빙한다.
 * 개발 중 다른 오리진에 붙일 때만 NEXT_PUBLIC_API_BASE 로 갈아끼운다(빌드 시 인라인).
 */
export const API_BASE = process.env.NEXT_PUBLIC_API_BASE ?? '';

export type FailureKind =
  | 'aborted'
  | 'unauthorized'
  | 'forbidden'
  | 'notFound'
  | 'badRequest'
  | 'server'
  | 'network';

export class ApiError extends Error {
  /** HTTP 상태. 네트워크 실패·취소처럼 응답이 없으면 0 이다. */
  readonly status: number;
  readonly body: ErrorBody;
  readonly aborted: boolean;

  constructor(message: string, status: number, body: ErrorBody = {}, aborted = false) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.body = body;
    this.aborted = aborted;
  }
}

/**
 * 실패를 화면 갈래로 접는다. **문구를 보지 않는다.**
 * 여기가 이 앱에서 status 를 해석하는 유일한 자리다 — 갈래가 늘면 여기만 고친다.
 */
export function failureKind(e: unknown): FailureKind {
  if (!(e instanceof ApiError)) return 'network';
  if (e.aborted) return 'aborted';
  if (e.status === 401) return 'unauthorized';
  if (e.status === 403) return 'forbidden';
  if (e.status === 404) return 'notFound';
  if (e.status >= 500) return 'server';
  if (e.status >= 400) return 'badRequest';
  return 'network';
}

/** 취소는 실패가 아니다 — 화면에 에러로 띄우면 탭을 옮길 때마다 오류 카드가 뜬다. */
export function isAborted(e: unknown): boolean {
  return e instanceof ApiError && e.aborted;
}

/*
 * 401 처리(=토큰 게이트 복귀)는 셸이 주입한다. 이 파일이 컴포넌트를 import 하면 순환이 되고,
 * "토큰 지우기"도 결국 같은 동작이라 훅 하나로 모은다.
 */
type UnauthorizedHandler = () => void;
let unauthorizedHandler: UnauthorizedHandler = () => {};
export function setUnauthorizedHandler(fn: UnauthorizedHandler): void {
  unauthorizedHandler = fn;
}

export interface RequestOptions {
  signal?: AbortSignal;
  /** 기본 GET. auth 엔드포인트는 POST 로 부른다. */
  method?: string;
  /** 있으면 JSON 으로 직렬화해 보낸다(Content-Type 자동). login 바디 등. */
  body?: unknown;
  /**
   * 인증 흐름(login·getMe)은 전역 401 훅을 타지 않는다 — 호출부가 인라인으로 처리한다.
   * getMe 의 401 은 "로그인 안 됨"이라는 정상 답이고, login 의 401 은 "자격증명 오류"라
   * 폼 위에 그대로 띄워야지 게이트 훅으로 새어 나가면 안 된다.
   */
  skipUnauthorizedHook?: boolean;
}

export async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const hasBody = opts.body !== undefined;
  let res: Response;
  try {
    res = await fetch(`${API_BASE}${path}`, {
      method: opts.method ?? 'GET',
      // 세션 쿠키는 httpOnly 라 JS 가 붙일 수 없다 — 브라우저가 싣게 include 로 둔다.
      credentials: 'include',
      headers: hasBody
        ? { Accept: 'application/json', 'Content-Type': 'application/json' }
        : { Accept: 'application/json' },
      body: hasBody ? JSON.stringify(opts.body) : undefined,
      signal: opts.signal,
    });
  } catch (e) {
    const aborted = e instanceof Error && (e.name === 'AbortError' || opts.signal?.aborted === true);
    throw new ApiError(
      aborted ? '요청이 취소되었습니다' : '서버에 연결하지 못했습니다',
      0,
      {},
      aborted,
    );
  }

  // 본문이 JSON 이 아니면 빈 객체로 둔다 — 프록시가 끼어 HTML 오류 페이지를 내는 경우가 있다.
  let body: unknown = {};
  try {
    body = await res.json();
  } catch {
    body = {};
  }

  if (res.status === 401 && !opts.skipUnauthorizedHook) {
    // 던지기 **전에** 부른다: 세션이 만료되거나 서버 재기동으로 끊겼을 때 화면이 빈 카드로
    // 남지 않고 로그인으로 돌아가야 한다. (auth 엔드포인트는 skipUnauthorizedHook 로 제외.)
    unauthorizedHandler();
  }

  if (!res.ok) {
    const err = (body && typeof body === 'object' ? (body as ErrorBody) : {});
    throw new ApiError(err.error || `HTTP ${res.status}`, res.status, err);
  }

  return body as T;
}

/* ── 인증(사람 로그인) ────────────────────────────────────────────────────
 *
 * 동결 계약:
 *   POST /api/auth/login  {username,password} → 200 {ok:true,user:{username,role,tenant}} · 401 {ok:false,error}
 *   POST /api/auth/logout → 204
 *   GET  /api/auth/me     → 200 {username,role,tenant} · 401
 * 세션 쿠키는 서버가 Set-Cookie 로 내린다(httpOnly). 여기서는 쿠키를 만지지 않는다.
 */
export interface AuthUser {
  username: string;
  role: string;
  tenant: string;
}

interface LoginResponse {
  ok: boolean;
  user: AuthUser;
}

/** 401 은 ApiError(status 401) 로 던진다 — 호출부가 "자격증명 오류"를 인라인으로 띄운다. */
export const login = (username: string, password: string, o?: RequestOptions) =>
  request<LoginResponse>('/api/auth/login', {
    ...o,
    method: 'POST',
    body: { username, password },
    skipUnauthorizedHook: true,
  }).then((r) => r.user);

export const logout = (o?: RequestOptions) =>
  request<void>('/api/auth/logout', { ...o, method: 'POST' });

/** 401 이면 ApiError 를 던진다("로그인 안 됨"). 호출부(셸)가 로그인 화면으로 분기한다. */
export const getMe = (o?: RequestOptions) =>
  request<AuthUser>('/api/auth/me', { ...o, skipUnauthorizedHook: true });

/* ── 플랫폼 필터 ──────────────────────────────────────────────────────────
 *
 * 동결 계약: 조회 엔드포인트가 선택적 `platform=` 을 받는다(claude|codex|gemini|other).
 *
 *   · **미지정 = 전체**다. 파라미터를 아예 붙이지 않는다 — 빈 값(`platform=`)을 보내면 서버는
 *     그것을 오타로 보고 400 을 낸다. 미전송이 곧 현행 동작이고, 골든 44개가 사는 근거다.
 *   · 허용목록 밖 값은 **400** 이다(서버가 other 로 접지 않는다 — 요청한 것과 다른 모집단이
 *     조용히 돌아오는 편이 더 나쁘기 때문이다). 그래서 호출부는 lib/platforms.ts 의
 *     isPlatformId 로 좁힌 값만 넘긴다.
 *
 * ⚠ **이 표는 한때 틀렸다** — 아래가 2026-08-13 실측으로 고친 내용이다.
 *
 *   서버는 `/api/usage/*` 조회 **전부**에서 platform 을 읽고 거른다:
 *     series · distribution · sessions · quality · coverage · leaderboard · platforms
 *     summary · dispatch · seats · teams · dev
 *
 *   예전 주석은 뒤의 5개(summary·dispatch·seats·teams·dev)를 "핸들러가 platform 을 읽기 전에
 *   끝난다 = 안 받는다"고 적어 두었다. 사실이 아니다. 확인 방법과 근거:
 *     · 오타를 보내면 **400** 이다(`?platform=claud`) — 읽지 않으면 나올 수 없는 응답이다.
 *     · `?platform=codex` 로 부르면 본문이 실제로 줄어든다(summary 5616→367 바이트).
 *     · 코드: usage.go 의 summary 갈래와 analytics.go 의 dispatch·seats·teams·dev 갈래가
 *       모두 platformParam 을 읽어 store.Filter{Platform: …} 을 만든다.
 *     · 테스트: httpapi/platform_scope_test.go 의 TestSummaryPlatformScope 가 이미 이것을 못 박고 있다.
 *
 *   ⚠ 2026-08-13: seats·teams·dev 의 배선을 함께 넣었다(scopeSuffix). 남은 미배선은
 *     사용 추적의 summary·dispatch 뿐이고, 그 화면은 "전체 플랫폼 기준"이라고 정직하게
 *     밝힌다(PlatformScope 의 applies={false}). **못 하는 것이 아니라 아직 배선하지 않은 것**
 *     이라는 점만 헷갈리지 말 것.
 *
 *   ⚠ 서버가 platform 으로 거를 수 **없는** 축은 딱 하나다: 추천 공백·전환 요약
 *     (usage_recommendations). 그 표에는 session_id 가 없어 어느 도구에서 나온 호출인지
 *     되짚을 근거가 존재하지 않는다(usage.go 의 같은 주석). 사용자 축은 username 컬럼이
 *     있어서 닿는다.
 */
export interface PlatformParams {
  /** 미지정·빈 문자열 = 전체. 허용목록 밖 값은 보내지 않는다. */
  platform?: string;
}

/** 플랫폼 값을 질의에 실는다. 빈 값이면 **키 자체를 만들지 않는다**(=전체). */
function setPlatform(q: URLSearchParams, platform?: string): void {
  if (platform) q.set('platform', platform);
}

/* ── runtime 필터 ─────────────────────────────────────────────────────────
 *
 * 이 세션이 **어디서 돌았나**다(cloud|local). platform 과 직교한다 — platform 은 "어느
 * 도구"이고, 같은 도구가 클라우드 모델도 로컬 모델도 문다. 그래서 `codex-local` 같은 합성
 * platform 을 만들지 않고 축을 하나 더 둔다.
 *
 * platform 과 같은 계약이다:
 *   · **미지정 = 전체.** 파라미터를 아예 붙이지 않는다 — 빈 값(`runtime=`)을 보내면 서버가
 *     400 을 낸다(오타를 조용히 접지 않는 규율).
 *   · 허용목록은 `lib/runtimes.ts`(= go/internal/store/runtime.go 의 Runtimes)가 소유한다.
 *
 * 서버는 `/api/usage/*` 조회 전부에서 이 축을 읽는다(httpapi 의 runtimeParam →
 * store.Filter{Runtime}). 자식 표(usage_series·usage_counters)는 세션으로 되짚어 거른다.
 */
export interface RuntimeParams {
  /** 미지정·빈 문자열 = 전체. 허용목록 밖 값은 보내지 않는다. */
  runtime?: string;
}

/** runtime 값을 질의에 실는다. 빈 값이면 **키 자체를 만들지 않는다**(=전체). */
function setRuntime(q: URLSearchParams, runtime?: string): void {
  if (runtime) q.set('runtime', runtime);
}



/* ── 사용자 필터 ──────────────────────────────────────────────────────────
 *
 * '사용 추적' 화면이 한 사람으로 좁힐 때 싣는 축이다. 파라미터 이름은 서버가 이미 쓰는
 * `user` 다(go/internal/httpapi 의 sessions·platforms 갈래와 한 벌).
 *
 * platform 과 다른 점 둘:
 *   · **허용목록이 없다.** 사용자명은 자유 문자열이라 서버가 400 을 낼 근거가 없고, 없는
 *     이름은 200 + 빈 집계로 돌아온다. 그래서 화면은 선택지를 **응답의 byUser 에서만** 만들고
 *     (하드코딩한 목록을 두지 않는다), 목록에 없는 선택은 조용히 전체로 되돌린다.
 *   · 추천 공백 축까지 닿는다. usage_recommendations 에 username 컬럼이 있어서다 —
 *     플랫폼은 그 표에 session_id 가 없어 못 거르는 자리다.
 *
 * 미지정이면 **키 자체를 만들지 않는다** = 전체(현행과 같은 응답, 골든 44개 무회귀).
 */
export interface UserParams {
  /** 미지정·빈 문자열 = 전체. */
  user?: string;
}

/**
 * ScopeParams — 이 서버의 조회 스코프는 **세 축**이다: 플랫폼 · 사람 · runtime.
 *
 * 축을 한 타입으로 묶는 이유: 화면이 한 축만 싣고 다른 축을 잊는 사고가 이 레포에서
 * 두 번 났다(둘 다 "서버가 그 축을 못 받는다"는 낡은 주석 때문이었다). 타입이 전부 함께
 * 들고 있으면 새 조회를 배선할 때 축이 같이 보인다.
 *
 * ⚠ 필드가 전부 optional 이라 **타입이 누락을 잡아 주지는 못한다.** 값을 만드는 자리를
 *   한 곳(useScope)으로 모아 두는 것이 실제 방어다.
 */
export interface ScopeParams extends PlatformParams, UserParams, RuntimeParams {}

/** 스코프 두 축을 질의에 싣는다. 빈 값은 **키를 만들지 않는다**(=전체). */
function withScope(path: string, p: ScopeParams = {}): string {
  const q = new URLSearchParams();
  setPlatform(q, p.platform);
  setRuntime(q, p.runtime);
  if (p.user) q.set('user', p.user);
  const qs = q.toString();
  return qs ? `${path}?${qs}` : path;
}

/* ── 엔드포인트 ───────────────────────────────────────────────────────── */

/*
 * ⚠ **스코프 세 축을 전부 받는다.** 예전에는 `UserParams` 만 받아 platform·runtime 을 **조용히
 *   버렸다** — 호출부가 `{platform, runtime, user}` 를 넘겨도 컴파일이 통과하고(스프레드는
 *   TypeScript 의 초과 속성 검사를 우회한다) 질의에서만 사라졌다.
 *
 *   그 결과가 실제로 났다(2026-08-21 브라우저 실측): 대시보드에서 '실행 위치=로컬'을 골랐을 때
 *   `seats`·`dev` 는 좁혀졌는데 `summary` 는 안 좁혀져 **활성 세션 타일이 1,589 그대로**였다.
 *   같은 화면의 두 카드가 서로 다른 모집단을 그리면서 그 사실을 말하지 않은 것 — 이 축에서
 *   가장 나쁜 실패다.
 *
 *   서버는 처음부터 두 축을 읽고 있었다(httpapi 의 platformParam·runtimeParam →
 *   store.Filter). 못 하는 쪽은 서버가 아니라 화면이었다. 예전 주석이 "platform 축은 받지
 *   않는다"고 적어 둔 것이 그 오해의 출처다.
 *
 * 두 조회에 **같은 값**을 실어야 한다: 한쪽만 걸면 같은 화면의 두 카드가 서로 다른
 * 모집단을 그리면서 그 사실을 말하지 않는다.
 */
export const getSummary = (p: ScopeParams = {}, o?: RequestOptions) =>
  request<Summary>(withScope('/api/usage/summary', p), o);
export const getDispatch = (p: ScopeParams = {}, o?: RequestOptions) =>
  request<Dispatch>(withScope('/api/usage/dispatch', p), o);
/*
 * seats·teams·dev — days + 스코프 두 축.
 *
 * ⚠ 예전 주석은 이 셋이 "platform 을 안 받는다"고 적었고 그래서 화면이 안 실었다. 사실이
 *   아니다(2026-08-13 실측: ?platform=codex 로 본문이 647→261 바이트). user 축도 같은 날
 *   서버에 배선했다. 두 축을 모두 싣는다.
 */
const scopeSuffix = (p: ScopeParams): string => {
  const q = new URLSearchParams();
  setPlatform(q, p.platform);
  setRuntime(q, p.runtime);
  if (p.user) q.set('user', p.user);
  const qs = q.toString();
  return qs ? `&${qs}` : '';
};

export const getSeats = (days = 30, o?: RequestOptions, p: ScopeParams = {}) =>
  request<Seats>(`/api/usage/seats?days=${days}${scopeSuffix(p)}`, o);
export const getTeams = (days = 30, o?: RequestOptions, p: ScopeParams = {}) =>
  request<Teams>(`/api/usage/teams?days=${days}${scopeSuffix(p)}`, o);
export const getDev = (days = 30, o?: RequestOptions, p: ScopeParams = {}) =>
  request<Dev>(`/api/usage/dev?days=${days}${scopeSuffix(p)}`, o);
export const getIdentity = (o?: RequestOptions) => request<unknown>('/api/usage/identity', o);

/* platform 축을 받는 조회. */
export const getDistribution = (p: ScopeParams = {}, o?: RequestOptions) =>
  request<Distribution>(withScope('/api/usage/distribution', p), o);
export const getQuality = (p: ScopeParams = {}, o?: RequestOptions) =>
  request<Quality>(withScope('/api/usage/quality', p), o);
export const getCoverage = (p: ScopeParams = {}, o?: RequestOptions) =>
  request<Coverage>(withScope('/api/usage/coverage', p), o);
export const getLeaderboard = (p: ScopeParams = {}, o?: RequestOptions) =>
  request<Leaderboard>(withScope('/api/usage/leaderboard', p), o);

/**
 * 플랫폼별 롤업 — "이 서버에 어떤 플랫폼의 데이터가 얼마나 있나".
 *
 * 기간 파라미터를 붙이지 않는다: 서버는 이 경로에서 from/to 만 읽고 days 는 보지 않으므로,
 * days 를 실어 보내면 화면이 "최근 N일"이라고 말하면서 실제로는 전체 기간을 받게 된다.
 * 그래서 **전체 기간 누적**으로 부르고, 화면도 그렇게 말한다(응답의 firstSeen·lastSeen 이
 * 실제 구간을 밝힌다).
 */
export const getPlatforms = (o?: RequestOptions) =>
  request<PlatformsResponse>('/api/usage/platforms', o);

export const getSessions = (
  params: { sort?: string; top?: number } & ScopeParams = {},
  o?: RequestOptions,
) => {
  const q = new URLSearchParams();
  if (params.sort) q.set('sort', params.sort);
  if (params.top != null) q.set('top', String(params.top));
  setPlatform(q, params.platform);
  // runtime 축도 같은 이유로 싣는다 — 아래 주석의 사고가 이 축에서도 한 번 났다.
  setRuntime(q, params.runtime);
  // user 축도 싣는다. 예전에는 PlatformParams 만 받아 호출부가 user 를 넘겨도 **조용히
  // 버려졌다** — 타입이 excess property 를 스프레드로 통과시켜 컴파일도 통과했다.
  if (params.user) q.set('user', params.user);
  const qs = q.toString();
  return request<SessionsResponse>(`/api/usage/sessions${qs ? `?${qs}` : ''}`, o);
};

export const getSessionDetail = (id: string, o?: RequestOptions) =>
  request<SessionDetail>(`/api/usage/sessions/${encodeURIComponent(id)}`, o);

/*
 * ⚠ **RuntimeParams 를 반드시 함께 상속한다.** 예전에는 PlatformParams 만 상속했는데, 호출부가
 *   스프레드로 `{...base}` 를 넘기면 TypeScript 의 초과 속성 검사가 돌지 않아 `runtime` 이
 *   **조용히 버려졌다** — 타입 체크는 통과하고 질의에서만 사라진다. 축을 늘릴 때 이 상속과
 *   아래 seriesQuery 를 함께 고쳐야 한다.
 */
export interface SeriesParams extends PlatformParams, RuntimeParams {
  metric?: 'cost' | 'tokens' | 'sessions' | 'turns';
  interval?: 'hour' | 'day' | 'week';
  groupBy?: string;
  user?: string;
  from?: string;
  to?: string;
}

/** 질의 문자열을 만드는 자리를 따로 둔다 — 테스트가 인코딩을 직접 잡을 수 있게. */
export function seriesQuery(p: SeriesParams): string {
  const q = new URLSearchParams();
  if (p.metric) q.set('metric', p.metric);
  if (p.interval) q.set('interval', p.interval);
  if (p.groupBy) q.set('group_by', p.groupBy);
  if (p.user) q.set('user', p.user);
  if (p.from) q.set('from', p.from);
  if (p.to) q.set('to', p.to);
  setPlatform(q, p.platform);
  setRuntime(q, p.runtime);
  return `/api/usage/series?${q.toString()}`;
}

export const getSeries = (p: SeriesParams, o?: RequestOptions) =>
  request<SeriesResponse>(seriesQuery(p), o);

/* ── 인제스트 키 ──────────────────────────────────────────────────────────
 *
 * 표면이 **둘**이다. 접두사부터 갈라져 있어 한쪽을 다른 쪽으로 착각할 수 없다:
 *
 *   셀프서비스(로그인한 누구나 · member 포함)  /api/me/keys*
 *     POST /api/me/keys             → 200 IssuedKey      ※ 소유자는 언제나 요청자 본인
 *     GET  /api/me/keys             → 200 {keys:[…]}     ※ **자기 키만**. 남의 것은 서버가 안 준다
 *     POST /api/me/keys/revoke {id} → 204                ※ 남의 키·없는 키 모두 404(문구 동일)
 *
 *   관리자(전체 현황)                          /api/admin/keys*
 *     POST /api/admin/keys {username?} → 200 IssuedKey   ※ username 을 비우면 결속 없는 org 공용 키
 *     GET  /api/admin/keys             → 200 {keys:[…]}  ※ tenant 전체
 *     POST /api/admin/keys/revoke {id} → 204
 *
 * 평문 key 는 **여기서도 어디서도 저장하지 않는다** — 발급 응답을 그대로 호출부(화면)에 넘기고,
 * 화면은 메모리에만 들고 표시한다. 목록은 masked 만 다룬다.
 */
export interface IssuedKey {
  /** 평문 인제스트 키. 발급 응답에서만 존재하며 다시 조회되지 않는다. */
  key: string;
  id: string;
  createdAt: string;
  /** 묶인 사람. 빈 문자열이면 결속 없는 org 공용 키다. */
  username: string;
}

export interface KeyListItem {
  id: string;
  /** 서버가 마스킹한 표시용 문자열(예: uu_ing_…a1b2). 평문은 절대 담기지 않는다. */
  masked: string;
  createdAt: string;
  /** null = 활성. 문자열이면 그 시각에 해지됨. */
  revokedAt: string | null;
  /**
   * 묶인 사람. **null 이면 결속 없는(레거시·org 공용) 키**다 — 그 키로 들어온 사용량은
   * 보고한 PC 가 주장하는 이름으로 잡힌다. 화면은 이 null 을 반드시 ⚠ 로 드러낸다.
   */
  username: string | null;
}

export interface KeyList {
  keys: KeyListItem[];
}

/* 셀프서비스 — 자기 키. */

/** 내 인제스트 키를 발급한다. 응답의 평문 key 는 이 한 번뿐이다(호출부가 즉시 표시). */
export const issueMyKey = (o?: RequestOptions) =>
  request<IssuedKey>('/api/me/keys', { ...o, method: 'POST' });

export const listMyKeys = (o?: RequestOptions) =>
  request<KeyList>('/api/me/keys', o);

/** 204(본문 없음)로 답한다 — request 가 빈 본문을 견딘다. */
export const revokeMyKey = (id: string, o?: RequestOptions) =>
  request<void>('/api/me/keys/revoke', { ...o, method: 'POST', body: { id } });

/* 관리자 — 전체 키 현황. */

/**
 * 관리자 대리발급.
 *
 * `username` 은 **필수 인자**다(빈 문자열 = 결속 없는 org 공용 키를 명시적으로 고른 것).
 * 선택 인자로 두면 호출부가 아무 생각 없이 생략하고, 그 키의 사용량은 PC 이름으로 잡힌다 —
 * 타입이 그 선택을 하게 만든다(api-admin 보고서 ④-6 이 화면에 넘긴 판단).
 */
export const issueKeyFor = (username: string, o?: RequestOptions) =>
  request<IssuedKey>('/api/admin/keys', { ...o, method: 'POST', body: { username } });

export const listKeys = (o?: RequestOptions) =>
  request<KeyList>('/api/admin/keys', o);

export const revokeKey = (id: string, o?: RequestOptions) =>
  request<void>('/api/admin/keys/revoke', { ...o, method: 'POST', body: { id } });

/* ── 관리자: 사용자 관리 ──────────────────────────────────────────────────
 *
 * 동결 계약(api-admin 보고서 ⑤-A). 상태변경 자격은 로그인 세션 쿠키다 — 브라우저가 자동으로
 * 싣는다(레거시 usage_tok 쿠키로는 403).
 *
 * **409 는 버그가 아니라 정상적인 거부**다(마지막 관리자 · 자기 강등 · 자기 삭제). 서버는
 * 아무것도 바꾸지 않았으므로 화면 상태를 되돌리지 말고 서버 문구를 그대로 보여준다.
 * 분기는 문구가 아니라 status 로 한다(failureKind) — 서버가 문구를 다듬는 날 조용히 틀리지
 * 않게. 그래서 여기서는 문구를 해석하지 않고 ApiError 를 그대로 던진다.
 */
export const listAdminUsers = (o?: RequestOptions) =>
  request<AdminUsers>('/api/admin/users', o);

/** role 을 생략하면 서버가 최소 권한(member)으로 만든다 — 여기서도 넘기지 않는다. */
export const createUser = (
  body: { username: string; password: string; role?: UserRole },
  o?: RequestOptions,
) => request<UserMutation>('/api/admin/users', { ...o, method: 'POST', body });

export const setUserRole = (username: string, role: UserRole, o?: RequestOptions) =>
  request<UserMutation>('/api/admin/users/role', { ...o, method: 'POST', body: { username, role } });

export const setUserPassword = (username: string, password: string, o?: RequestOptions) =>
  request<UserMutation>('/api/admin/users/password', { ...o, method: 'POST', body: { username, password } });

/** 빈 문자열이면 미배정으로 되돌린다. */
export const setUserTeam = (username: string, team: string, o?: RequestOptions) =>
  request<UserMutation>('/api/admin/users/team', { ...o, method: 'POST', body: { username, team } });

export const deleteUser = (username: string, o?: RequestOptions) =>
  request<UserDeletion>('/api/admin/users/delete', { ...o, method: 'POST', body: { username } });
