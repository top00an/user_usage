'use client';

/*
 * ── 테마 상태 ──────────────────────────────────────────────────────────────
 *
 * 값의 소유자는 **DOM 의 `<html data-theme>`** 이고, 그것을 처음 세우는 것은
 * `public/theme-boot.js` 다(스타일시트보다 먼저 동기 실행 — FOUC 방지).
 * 이 모듈은 그 값을 읽고 바꾸는 얇은 층이지 두 번째 진실이 아니다 — React state 에 사본을
 * 두면 부팅 스크립트가 세운 값과 갈리고, 그때 화면과 저장값이 서로 다른 말을 한다.
 *
 * 우선순위는 부팅 스크립트와 **같아야 한다**:
 *   localStorage 'usage-theme'  →  없으면 OS prefers-color-scheme  →  그것도 모르면 dark
 * 여기서 하는 일은 그 첫 칸(저장값)을 쓰는 것뿐이다. OS 추종은 "아직 고른 적 없음"의 기본값으로
 * 남는다 — 한 번 고르면 그 뒤로는 OS 를 따라 바뀌지 않는다(그게 고른다는 뜻이다).
 */

import { useSyncExternalStore } from 'react';

export type Theme = 'dark' | 'light';

/** 부팅 스크립트와 공유하는 저장 키. 두 곳에 적혀 있으므로 바꿀 때 함께 바꾼다. */
export const THEME_KEY = 'usage-theme';

const listeners = new Set<() => void>();

/** 현재 테마. DOM 이 단일 출처다 — 부팅 스크립트가 이미 세워 두었다. */
export function getTheme(): Theme {
  return document.documentElement.getAttribute('data-theme') === 'light' ? 'light' : 'dark';
}

export function setTheme(t: Theme): void {
  document.documentElement.setAttribute('data-theme', t);
  // 저장 실패(사생활 보호 모드·용량 초과)는 삼킨다. 이번 세션의 테마는 이미 바뀌었고,
  // 여기서 던지면 버튼 한 번에 화면이 통째로 죽는다.
  try { localStorage.setItem(THEME_KEY, t); } catch { /* 저장 못 해도 화면은 바뀐다 */ }
  for (const fn of listeners) fn();
}

export function toggleTheme(): void {
  setTheme(getTheme() === 'dark' ? 'light' : 'dark');
}

function subscribe(fn: () => void): () => void {
  listeners.add(fn);
  // 다른 탭에서 바꾼 값도 따라간다 — 같은 브라우저에 두 탭을 띄워 놓고 한쪽만 바뀌면
  // 사용자는 그것을 버그로 읽는다.
  const onStorage = (e: StorageEvent) => {
    if (e.key !== THEME_KEY) return;
    const t: Theme = e.newValue === 'light' ? 'light' : 'dark';
    document.documentElement.setAttribute('data-theme', t);
    fn();
  };
  window.addEventListener('storage', onStorage);
  return () => {
    listeners.delete(fn);
    window.removeEventListener('storage', onStorage);
  };
}

/*
 * 정적 export 라 프리렌더 HTML 에는 data-theme 이 없다. 서버 스냅샷을 CSS 의 기본값과 같은
 * 'dark' 로 두면, 하이드레이션 직후 useSyncExternalStore 가 클라이언트 스냅샷을 다시 읽어
 * 실제 값으로 맞춘다(불일치 경고 없이). 여기서 DOM 을 읽으면 서버·클라이언트가 갈린다.
 */
export function useTheme(): Theme {
  return useSyncExternalStore(subscribe, getTheme, () => 'dark' as const);
}
