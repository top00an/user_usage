import '@testing-library/jest-dom/vitest';
import { afterEach } from 'vitest';
import { cleanup } from '@testing-library/react';

afterEach(() => {
  cleanup();
  // 쿠키는 문서 전역이라 테스트 사이로 샌다 — 게이트 테스트가 앞 테스트의 토큰을 물려받으면
  // "게이트가 안 뜬다"가 되어 원인이 엉뚱한 데로 간다.
  document.cookie = 'usage_tok=; path=/; Max-Age=0; SameSite=Strict';
});
