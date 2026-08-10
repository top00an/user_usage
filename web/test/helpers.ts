import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { vi } from 'vitest';

/*
 * 픽스처는 **contract/golden/ 을 그대로 읽는다.** 손으로 옮겨 적으면 그 사본이 계약과 갈라지고,
 * 그때 테스트는 현실이 아니라 사본에 대해 초록불이 된다. golden 은 읽기 전용이다(다른 오너 소유).
 */
const HERE = path.dirname(fileURLToPath(import.meta.url));
const GOLDEN = path.resolve(HERE, '..', '..', 'contract', 'golden');

export function golden<T = unknown>(name: string): T {
  return JSON.parse(readFileSync(path.join(GOLDEN, `${name}.json`), 'utf8')).body as T;
}

export interface RouteSpec {
  /** 응답 본문. 함수면 호출 시점에 만든다. */
  body?: unknown;
  status?: number;
  /** 응답을 늦춘다(ms). 레이스 재현용. */
  delay?: number;
}

/**
 * 경로 → 응답. 앞에서부터 **첫 번째로 접두사가 맞는 것**을 쓴다(구체적인 경로를 먼저 적을 것).
 * signal 이 끊기면 AbortError 로 거절한다 — 실제 fetch 와 같은 계약이라야 정리 경로가 검증된다.
 */
export function mockFetch(routes: [string, RouteSpec][]) {
  const calls: string[] = [];
  const fn = vi.fn((url: string, init?: RequestInit) => {
    calls.push(url);
    const hit = routes.find(([p]) => url.startsWith(p));
    const spec: RouteSpec = hit?.[1] ?? { status: 404, body: { error: '없는 경로' } };
    const status = spec.status ?? 200;
    const res = () => new Response(JSON.stringify(spec.body ?? {}), {
      status,
      headers: { 'Content-Type': 'application/json' },
    });

    if (!spec.delay) return Promise.resolve(res());

    return new Promise<Response>((resolve, reject) => {
      const signal = init?.signal;
      const timer = setTimeout(() => resolve(res()), spec.delay);
      if (signal) {
        if (signal.aborted) { clearTimeout(timer); reject(abortError()); return; }
        signal.addEventListener('abort', () => {
          clearTimeout(timer);
          reject(abortError());
        });
      }
    });
  });
  vi.stubGlobal('fetch', fn);
  return { fn, calls };
}

function abortError() {
  return Object.assign(new Error('The operation was aborted.'), { name: 'AbortError' });
}

/*
 * 플랫폼 롤업 픽스처.
 *
 * ⚠ 이것만 golden 이 아니다 — contract/golden/ 에 platforms 스냅샷이 아직 없다(그 디렉터리는
 *   다른 오너 소유라 여기서 만들지 않는다). shape 와 값은 백엔드가 확정해 넘긴 계약 그대로다:
 *   claude 135세션 / codex 19세션.
 *
 * codex 의 cacheCreate 를 0 으로 둔 것이 이 픽스처의 핵심이다 — 화면은 이 0 을 숫자로 그리면
 * 안 되고 '해당 없음'으로 그려야 한다(OpenAI 는 캐시 쓰기에 과금하지 않는다).
 */
export const PLATFORMS_FIXTURE = {
  platforms: [
    {
      platform: 'claude', sessions: 135, input: 989034, output: 20176298,
      cacheRead: 3802581865, cacheCreate: 114233960, costUsd: 3195.30679875,
      firstSeen: '2026-07-06T08:01:12.180Z', lastSeen: '2026-08-10T02:14:16.443Z',
    },
    {
      platform: 'codex', sessions: 19, input: 123456, output: 234567,
      cacheRead: 3456789, cacheCreate: 0, costUsd: 12.3456,
      firstSeen: '2026-08-01T00:00:00.000Z', lastSeen: '2026-08-09T12:00:00.000Z',
    },
  ],
};

export function platformRoutes(): [string, RouteSpec][] {
  return [['/api/usage/platforms', { body: PLATFORMS_FIXTURE }]];
}

/** 관측 탭이 부르는 여섯 엔드포인트를 골든으로 채운다. */
export function obsRoutes(extra: [string, RouteSpec][] = []): [string, RouteSpec][] {
  return [
    ...extra,
    ...platformRoutes(),
    ['/api/usage/distribution', { body: golden('distribution') }],
    ['/api/usage/sessions/', { body: golden('session-detail-series') }],
    ['/api/usage/sessions', { body: golden('sessions-cost') }],
    ['/api/usage/leaderboard', { body: golden('leaderboard') }],
    ['/api/usage/quality', { body: golden('quality') }],
    ['/api/usage/coverage', { body: golden('coverage') }],
    ['/api/usage/series', { body: golden('series-group-model') }],
  ];
}

/** 추적 탭이 부르는 세 엔드포인트. */
export function trackRoutes(extra: [string, RouteSpec][] = []): [string, RouteSpec][] {
  return [
    ...extra,
    ...platformRoutes(),
    ['/api/usage/summary', { body: golden('summary') }],
    ['/api/usage/dispatch', { body: golden('dispatch') }],
  ];
}

/**
 * 셸 진입 시 getMe() 가 부르는 세션 확인 엔드포인트. 기본은 로그인된 관리자.
 * 로그아웃/미로그인 시나리오는 status 401 로 넘긴다.
 */
export function authRoutes(
  user: { username: string; role: string; tenant: string } = {
    username: 'admin', role: 'admin', tenant: 'acme',
  },
): [string, RouteSpec][] {
  return [['/api/auth/me', { body: user }]];
}
