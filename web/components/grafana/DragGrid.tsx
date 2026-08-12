'use client';

/*
 * 드래그로 순서를 바꾸는 그리드. 같은 섹션 안에서 패널을 끌어 재배치하고, 순서를 localStorage 에
 * 저장한다(id 기반이라 코드가 바뀌어도 안전). 각 아이템은 안정적인 id 를 가진다.
 *
 * ── 순서의 주인은 localStorage 하나다 ────────────────────────────────────
 *
 * 예전에는 순서를 useState 로 들고 이펙트에서 localStorage 를 읽어 setState 로 덮었다. 그러면
 * 같은 사실이 두 군데(state · localStorage)에 살고, 화면은 **기본 순서를 한 프레임 먼저 그린 뒤**
 * 저장된 순서로 튄다(실측: 대시보드가 뜨는 첫 프레임은 기본 순서였다). 사람은 그 튐을 데이터
 * 변화로 읽는다.
 *
 * 그래서 저장소를 바깥 스토어로 보고 useSyncExternalStore 로 읽는다 — components/Dashboard.tsx 가
 * 쿠키·해시를 읽는 방법과 같다. 순서는 렌더 중에 저장값에서 **파생**되므로 첫 프레임부터 옳고,
 * 이펙트가 없다(react-hooks/set-state-in-effect).
 */
import { useCallback, useRef, useState, useSyncExternalStore } from 'react';

export interface GridItem { id: string; node: React.ReactNode; }

const KEY_PREFIX = 'ccdash-order:';

/*
 * 같은 문서 안의 쓰기는 storage 이벤트를 만들지 않는다(브라우저 규약: 다른 탭에만 간다).
 * 그래서 우리 쓰기는 리스너로 직접 알린다 — lib/customPanels.ts 와 같은 형태.
 */
const listeners = new Set<() => void>();

function subscribeOrder(cb: () => void): () => void {
  listeners.add(cb);
  const onStorage = (e: StorageEvent) => { if (!e.key || e.key.startsWith(KEY_PREFIX)) cb(); };
  window.addEventListener('storage', onStorage);
  return () => { listeners.delete(cb); window.removeEventListener('storage', onStorage); };
}

/*
 * 저장이 막힌 브라우저(사생활 보호 모드 · 용량 초과)를 위한 세션 한정 폴백.
 *
 * 순서의 주인이 저장소 하나가 되면서 생기는 함정이 하나 있다 — 예전에는 순서가 컴포넌트
 * state 에도 있어서 `setItem` 이 던져도 그 세션 동안은 드래그가 동작했다. 그대로 두면 저장이
 * 막힌 사람에게는 **패널을 끌어도 아무 일이 없다.** 저장이 안 되는 것은 어쩔 수 없지만 화면이
 * 죽어 보이는 것은 다른 문제다(test/grafana-layout.test.tsx 가 이 자리를 잡는다).
 */
const memory = new Map<string, string>();

/*
 * 스냅샷은 **문자열 그대로** 돌려준다. 여기서 JSON.parse 를 하면 매 호출이 새 배열이 되고,
 * useSyncExternalStore 는 스냅샷이 매번 달라졌다고 보아 무한 렌더에 빠진다.
 */
function readOrder(gridId: string): string {
  const key = KEY_PREFIX + gridId;
  try {
    const v = localStorage.getItem(key);
    if (v !== null) return v;
  } catch { /* 저장소를 못 읽는다 — 아래 폴백 */ }
  return memory.get(key) ?? '[]';
}

/*
 * 폴백에는 **쓰기가 실제로 실패한 것만** 남긴다. 성공하면 지운다 — 안 그러면 저장소가 비었을
 * 때(다른 탭에서 사이트 데이터를 지웠거나 레이아웃을 초기화했을 때) 죽은 순서가 되살아난다.
 */
function writeOrder(gridId: string, next: string[]): void {
  const key = KEY_PREFIX + gridId;
  const raw = JSON.stringify(next);
  try {
    localStorage.setItem(key, raw);
    memory.delete(key);
  } catch {
    memory.set(key, raw); // 이 세션 동안만 여기서 산다(다음 방문에는 안 남는다).
  }
  listeners.forEach((fn) => fn());
}

function parseOrder(raw: string): string[] {
  try {
    const v: unknown = JSON.parse(raw);
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [];
  } catch { return []; }
}

export default function DragGrid({
  gridId, className, items,
}: { gridId: string; className?: string; items: GridItem[] }) {
  const dragId = useRef<string | null>(null);
  const [overId, setOverId] = useState<string | null>(null);

  /*
   * 정적 export 라 프리렌더에는 localStorage 가 없다 — 서버 스냅샷은 '[]'(=기본 순서)다.
   * 하이드레이션 직후 클라이언트 스냅샷으로 한 번 맞춰진다.
   */
  const getSnapshot = useCallback(() => readOrder(gridId), [gridId]);
  const savedRaw = useSyncExternalStore(subscribeOrder, getSnapshot, () => '[]');

  /*
   * 저장된 순서 적용(현재 존재하는 id 만, 새 id 는 뒤에 붙임). **렌더 중 파생**이라 첫 프레임부터
   * 옳다. 메모하지 않는다 — 한 그리드의 패널은 열 개 남짓이라 비용이 없고, 메모하면 items 를
   * 문자열로 눌러 담은 가짜 deps 가 필요해진다(id 에 쉼표가 들어오는 날 조용히 깨진다).
   */
  const ids = items.map((i) => i.id);
  const saved = parseOrder(savedRaw);
  const ordered = [...saved.filter((id) => ids.includes(id)), ...ids.filter((id) => !saved.includes(id))];

  const persist = useCallback((next: string[]) => { writeOrder(gridId, next); }, [gridId]);

  const onDrop = (targetId: string) => {
    const from = dragId.current;
    dragId.current = null;
    setOverId(null);
    if (!from || from === targetId) return;
    const next = [...ordered];
    const fi = next.indexOf(from), ti = next.indexOf(targetId);
    if (fi < 0 || ti < 0) return;
    next.splice(fi, 1);
    next.splice(ti, 0, from);
    persist(next);
    // 재배치 후 차트가 새 폭에 맞도록 리사이즈를 유도.
    requestAnimationFrame(() => window.dispatchEvent(new Event('resize')));
  };

  const byId = new Map(items.map((i) => [i.id, i.node]));

  return (
    <div className={`grid ${className ?? ''}`} data-grid={gridId}>
      {ordered.filter((id) => byId.has(id)).map((id) => (
        <div
          key={id}
          data-pid={id}
          className={`gcell${overId === id ? ' over' : ''}`}
          draggable
          onDragStart={(e) => { dragId.current = id; e.dataTransfer.effectAllowed = 'move'; }}
          onDragOver={(e) => { e.preventDefault(); if (dragId.current && dragId.current !== id) setOverId(id); }}
          onDragLeave={() => setOverId((cur) => (cur === id ? null : cur))}
          onDrop={(e) => { e.preventDefault(); onDrop(id); }}
          onDragEnd={() => { dragId.current = null; setOverId(null); }}
        >
          {byId.get(id)}
        </div>
      ))}
    </div>
  );
}

export function resetLayout(): void {
  memory.clear(); // 저장이 막힌 세션에서는 이쪽이 실제 순서를 들고 있다.
  Object.keys(localStorage).filter((k) => k.startsWith(KEY_PREFIX)).forEach((k) => localStorage.removeItem(k));
  location.reload();
}
