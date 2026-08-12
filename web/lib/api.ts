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
 * ⚠ **모든 엔드포인트가 이 축을 받는 것은 아니다.** 서버 라우팅(go/internal/httpapi/analytics.go)
 *   기준으로 갈린다:
 *     받는다  — series · distribution · sessions · quality · coverage · leaderboard · platforms
 *     안 받는다 — summary · dispatch · seats · teams · dev  (핸들러가 platform 을 읽기 전에 끝난다)
 *
 *   안 받는 쪽에 platform 을 붙여도 서버는 **조용히 무시하고 전체를 돌려준다.** 그래서 여기서는
 *   아예 받을 수 있는 함수에만 인자를 둔다 — 타입이 "이건 못 거른다"를 말하게 해서, 화면이
 *   전체 합계를 그 플랫폼의 값인 척 그리는 사고를 컴파일 시점에 막는다.
 *   (화면은 그 사실을 사용자에게도 말한다 — components/platform/PlatformScope.tsx)
 */
export interface PlatformParams {
  /** 미지정·빈 문자열 = 전체. 허용목록 밖 값은 보내지 않는다. */
  platform?: string;
}

/** 플랫폼 값을 질의에 실는다. 빈 값이면 **키 자체를 만들지 않는다**(=전체). */
function setPlatform(q: URLSearchParams, platform?: string): void {
  if (platform) q.set('platform', platform);
}

function withPlatform(path: string, platform?: string): string {
  const q = new URLSearchParams();
  setPlatform(q, platform);
  const qs = q.toString();
  return qs ? `${path}?${qs}` : path;
}

/* ── 엔드포인트 ───────────────────────────────────────────────────────── */

/* platform 축을 받지 않는 조회 — 인자를 두지 않는다(위 주석 참고). */
export const getSummary = (o?: RequestOptions) => request<Summary>('/api/usage/summary', o);
export const getDispatch = (o?: RequestOptions) => request<Dispatch>('/api/usage/dispatch', o);
export const getSeats = (days = 30, o?: RequestOptions) =>
  request<Seats>(`/api/usage/seats?days=${days}`, o);
export const getTeams = (days = 30, o?: RequestOptions) =>
  request<Teams>(`/api/usage/teams?days=${days}`, o);
export const getDev = (days = 30, o?: RequestOptions) =>
  request<Dev>(`/api/usage/dev?days=${days}`, o);
export const getIdentity = (o?: RequestOptions) => request<unknown>('/api/usage/identity', o);

/* platform 축을 받는 조회. */
export const getDistribution = (p: PlatformParams = {}, o?: RequestOptions) =>
  request<Distribution>(withPlatform('/api/usage/distribution', p.platform), o);
export const getQuality = (p: PlatformParams = {}, o?: RequestOptions) =>
  request<Quality>(withPlatform('/api/usage/quality', p.platform), o);
export const getCoverage = (p: PlatformParams = {}, o?: RequestOptions) =>
  request<Coverage>(withPlatform('/api/usage/coverage', p.platform), o);
export const getLeaderboard = (p: PlatformParams = {}, o?: RequestOptions) =>
  request<Leaderboard>(withPlatform('/api/usage/leaderboard', p.platform), o);

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
  params: { sort?: string; top?: number } & PlatformParams = {},
  o?: RequestOptions,
) => {
  const q = new URLSearchParams();
  if (params.sort) q.set('sort', params.sort);
  if (params.top != null) q.set('top', String(params.top));
  setPlatform(q, params.platform);
  const qs = q.toString();
  return request<SessionsResponse>(`/api/usage/sessions${qs ? `?${qs}` : ''}`, o);
};

export const getSessionDetail = (id: string, o?: RequestOptions) =>
  request<SessionDetail>(`/api/usage/sessions/${encodeURIComponent(id)}`, o);

export interface SeriesParams extends PlatformParams {
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
