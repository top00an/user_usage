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

/** 관측 탭이 부르는 다섯 엔드포인트를 골든으로 채운다. */
export function obsRoutes(extra: [string, RouteSpec][] = []): [string, RouteSpec][] {
  return [
    ...extra,
    ['/api/usage/distribution', { body: golden('distribution') }],
    ['/api/usage/sessions/', { body: golden('session-detail-series') }],
    ['/api/usage/sessions', { body: golden('sessions-cost') }],
    ['/api/usage/leaderboard', { body: golden('leaderboard') }],
    ['/api/usage/quality', { body: golden('quality') }],
    ['/api/usage/coverage', { body: golden('coverage') }],
    ['/api/usage/series', { body: golden('series-group-model') }],
  ];
}

/** 추적 탭이 부르는 두 엔드포인트. */
export function trackRoutes(extra: [string, RouteSpec][] = []): [string, RouteSpec][] {
  return [
    ...extra,
    ['/api/usage/summary', { body: golden('summary') }],
    ['/api/usage/dispatch', { body: golden('dispatch') }],
  ];
}
