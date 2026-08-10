'use client';

/*
 * ── 선택된 플랫폼(화면 상태) ──────────────────────────────────────────────
 *
 * 탭 셸은 탭이 바뀔 때 트리를 통째로 새로 만든다(Dashboard 의 key). 그래서 선택값을 컴포넌트
 * 상태로 들면 탭을 옮길 때마다 '전체'로 되돌아간다 — 플랫폼을 골라 놓고 관측→추적으로 넘어간
 * 사람에게는 그게 버그로 보인다. 해시(탭)와 커스텀 패널이 이미 그렇듯 **React 밖의 스토어**로 둔다.
 *
 * sessionStorage 에 둔다: 새로고침에는 살아남되 탭을 닫으면 사라진다. 플랫폼 선택은 조회 조건이지
 * 사용자 설정이 아니라서, 며칠 뒤 다시 열었을 때 조용히 걸려 있으면 안 된다.
 *
 * ⚠ 읽는 자리에서 반드시 허용목록으로 좁힌다(isPlatformId). 저장소는 사람이 손댈 수 있고,
 *   허용목록 밖 값이 질의로 나가면 서버가 400 을 낸다 — 화면 전체가 실패로 접힌다.
 */

import { useSyncExternalStore } from 'react';
import { isPlatformId } from './platforms';

const KEY = 'ccdash-platform-filter-v1';
const listeners = new Set<() => void>();

/** '' = 전체(=파라미터 미전송, 현행 동작). */
let current = '';
let loaded = false;

function hydrate(): void {
  if (loaded) return;
  loaded = true;
  try {
    const v = sessionStorage.getItem(KEY) ?? '';
    current = isPlatformId(v) ? v : '';
  } catch {
    current = '';
  }
}

/** 지금 선택된 플랫폼. 전체면 빈 문자열이다. */
export function getPlatformFilter(): string {
  hydrate();
  return current;
}

/** 허용목록 밖 값은 '전체'로 접는다 — 400 을 부르는 질의를 애초에 만들지 않는다. */
export function setPlatformFilter(v: string): void {
  hydrate();
  const next = isPlatformId(v) ? v : '';
  if (next === current) return;
  current = next;
  try {
    if (next) sessionStorage.setItem(KEY, next);
    else sessionStorage.removeItem(KEY);
  } catch {
    /* 저장 실패는 무시한다 — 이번 세션 동안만 메모리로 산다 */
  }
  listeners.forEach((fn) => fn());
}

export function subscribePlatformFilter(cb: () => void): () => void {
  listeners.add(cb);
  return () => { listeners.delete(cb); };
}

/**
 * 선택값 구독. 정적 export 라 서버 스냅샷은 항상 '전체'다 — 하이드레이션 뒤 실제 값으로 맞춰진다
 * (해시 탭이 이미 같은 방식이다).
 */
export function usePlatformFilter(): string {
  return useSyncExternalStore(subscribePlatformFilter, getPlatformFilter, () => '');
}
