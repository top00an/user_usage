import '@testing-library/jest-dom/vitest';
import { afterEach, vi } from 'vitest';
import { cleanup } from '@testing-library/react';

// EChart 는 echarts(canvas)를 초기화한다 — jsdom 엔 canvas·ResizeObserver 가 없어 터진다.
// 차트 렌더 자체는 이 단위 테스트의 대상이 아니므로(브라우저에서 확인) 플레이스홀더로 모킹한다.
vi.mock('@/components/charts/EChart', () => ({ default: () => null }));

// 혹시 다른 코드가 쓸 때를 대비한 ResizeObserver 폴리필.
if (typeof globalThis.ResizeObserver === 'undefined') {
  globalThis.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
}

afterEach(() => {
  cleanup();
  // 쿠키는 문서 전역이라 테스트 사이로 샌다 — 게이트 테스트가 앞 테스트의 토큰을 물려받으면
  // "게이트가 안 뜬다"가 되어 원인이 엉뚱한 데로 간다.
  document.cookie = 'usage_tok=; path=/; Max-Age=0; SameSite=Strict';
});
