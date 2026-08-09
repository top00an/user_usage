/*
 * ── 유일한 서버 호출구 ────────────────────────────────────────────────────
 *
 * 이 파일 **밖에서는 fetch 를 부르지 않는다**(eslint 의 no-restricted-globals 가 강제한다).
 * 나중에 SSO·프록시로 갈아탈 때 고칠 자리가 여기 하나여야 하기 때문이다. 컴포넌트가 fetch 를
 * 직접 부르면 그 전환이 전면 수정이 된다.
 *
 * 자격증명은 쿠키(usage_tok)로 실린다 — 브라우저가 알아서 붙이므로 호출부가 토큰을 모른다.
 *
 * 실패는 **구조로** 남긴다(현행 public/js/core.js 의 fail 과 같은 계약):
 *   · status — 401(게이트 복귀) · 403(권한 안내) · 5xx(연결 실패)를 갈라야 하는 호출부가 있다
 *   · body   — 서버가 함께 보낸 응답 바디 전체
 * 에러 문구는 사람이 읽는 글이라 언제든 다듬어진다. 분기를 문구에 걸면 그때 화면이 조용히
 * 틀린 쪽으로 넘어간다 — 분기는 문자열이 아니라 status 로 한다(failureKind).
 */
import type {
  Coverage,
  Dispatch,
  Distribution,
  ErrorBody,
  Dev,
  Leaderboard,
  Quality,
  Seats,
  SeriesResponse,
  SessionDetail,
  SessionsResponse,
  Summary,
  Teams,
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
}

export async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  let res: Response;
  try {
    res = await fetch(`${API_BASE}${path}`, {
      method: 'GET',
      credentials: 'same-origin',
      headers: { Accept: 'application/json' },
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

  if (res.status === 401) {
    // 던지기 **전에** 부른다: 토큰이 틀리거나 서버 재기동으로 바뀌었을 때 화면이 빈 카드로
    // 남지 않고 게이트로 돌아가야 한다.
    unauthorizedHandler();
  }

  if (!res.ok) {
    const err = (body && typeof body === 'object' ? (body as ErrorBody) : {});
    throw new ApiError(err.error || `HTTP ${res.status}`, res.status, err);
  }

  return body as T;
}

/* ── 엔드포인트 10개 ──────────────────────────────────────────────────── */

export const getSummary = (o?: RequestOptions) => request<Summary>('/api/usage/summary', o);
export const getDispatch = (o?: RequestOptions) => request<Dispatch>('/api/usage/dispatch', o);
export const getDistribution = (o?: RequestOptions) => request<Distribution>('/api/usage/distribution', o);
export const getQuality = (o?: RequestOptions) => request<Quality>('/api/usage/quality', o);
export const getCoverage = (o?: RequestOptions) => request<Coverage>('/api/usage/coverage', o);
export const getLeaderboard = (o?: RequestOptions) => request<Leaderboard>('/api/usage/leaderboard', o);
export const getSeats = (days = 30, o?: RequestOptions) =>
  request<Seats>(`/api/usage/seats?days=${days}`, o);
export const getTeams = (days = 30, o?: RequestOptions) =>
  request<Teams>(`/api/usage/teams?days=${days}`, o);
export const getDev = (days = 30, o?: RequestOptions) =>
  request<Dev>(`/api/usage/dev?days=${days}`, o);
export const getIdentity = (o?: RequestOptions) => request<unknown>('/api/usage/identity', o);

export const getSessions = (
  params: { sort?: string; top?: number } = {},
  o?: RequestOptions,
) => {
  const q = new URLSearchParams();
  if (params.sort) q.set('sort', params.sort);
  if (params.top != null) q.set('top', String(params.top));
  const qs = q.toString();
  return request<SessionsResponse>(`/api/usage/sessions${qs ? `?${qs}` : ''}`, o);
};

export const getSessionDetail = (id: string, o?: RequestOptions) =>
  request<SessionDetail>(`/api/usage/sessions/${encodeURIComponent(id)}`, o);

export interface SeriesParams {
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
  return `/api/usage/series?${q.toString()}`;
}

export const getSeries = (p: SeriesParams, o?: RequestOptions) =>
  request<SeriesResponse>(seriesQuery(p), o);
