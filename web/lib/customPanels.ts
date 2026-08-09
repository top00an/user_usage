'use client';

/*
 * 사용자가 만든 커스텀 그래프 패널 정의. 대시보드의 '내 그래프' 섹션에 렌더된다.
 * localStorage 에 저장하고, 탭 간(추적 탭에서 추가 → 대시보드에서 표시) 반영을 위해
 * 같은 탭 내 구독 + storage 이벤트(다른 탭)로 알린다.
 */
export type Metric = 'tokens' | 'cost' | 'sessions' | 'turns';
export type ChartType = 'line' | 'bar' | 'donut';
export type GroupBy = 'none' | 'model' | 'user';

export interface CustomPanel {
  id: string;
  title: string;
  metric: Metric;
  type: ChartType;
  groupBy: GroupBy;
  days: number;
}

const KEY = 'ccdash-custom-panels-v1';
const PREFILL_KEY = 'ccdash-builder-prefill';
const listeners = new Set<() => void>();

function read(): CustomPanel[] {
  try {
    const v = JSON.parse(localStorage.getItem(KEY) || '[]');
    return Array.isArray(v) ? v : [];
  } catch {
    return [];
  }
}
function write(panels: CustomPanel[]): void {
  try { localStorage.setItem(KEY, JSON.stringify(panels)); } catch { /* 저장 실패 무시 */ }
  listeners.forEach((fn) => fn());
}

export function listPanels(): CustomPanel[] { return read(); }

export function addPanel(p: Omit<CustomPanel, 'id'>): void {
  const id = `cp-${read().length}-${p.metric}-${p.groupBy}-${p.type}`;
  write([...read(), { ...p, id }]);
}
export function removePanel(id: string): void {
  write(read().filter((p) => p.id !== id));
}

/** 컴포넌트가 패널 목록 변화를 구독한다(useSyncExternalStore 용). */
export function subscribePanels(cb: () => void): () => void {
  listeners.add(cb);
  const onStorage = (e: StorageEvent) => { if (e.key === KEY) cb(); };
  window.addEventListener('storage', onStorage);
  return () => { listeners.delete(cb); window.removeEventListener('storage', onStorage); };
}
export function panelsSnapshot(): string {
  // 참조 안정성을 위해 문자열 스냅샷 — 값이 바뀔 때만 리렌더.
  try { return localStorage.getItem(KEY) || '[]'; } catch { return '[]'; }
}

/* 추적/관측 탭의 '그래프로 추가' → 대시보드 빌더를 프리필로 연다. */
export function setBuilderPrefill(p: Partial<Omit<CustomPanel, 'id'>>): void {
  try { localStorage.setItem(PREFILL_KEY, JSON.stringify(p)); } catch { /* 무시 */ }
}
export function takeBuilderPrefill(): Partial<Omit<CustomPanel, 'id'>> | null {
  try {
    const raw = localStorage.getItem(PREFILL_KEY);
    if (!raw) return null;
    localStorage.removeItem(PREFILL_KEY);
    return JSON.parse(raw);
  } catch { return null; }
}
