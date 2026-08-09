'use client';

/*
 * 드래그로 순서를 바꾸는 그리드. 같은 섹션 안에서 패널을 끌어 재배치하고, 순서를 localStorage 에
 * 저장한다(id 기반이라 코드가 바뀌어도 안전). 각 아이템은 안정적인 id 를 가진다.
 */
import { useCallback, useEffect, useRef, useState } from 'react';

export interface GridItem { id: string; node: React.ReactNode; }

const KEY_PREFIX = 'ccdash-order:';

export default function DragGrid({
  gridId, className, items,
}: { gridId: string; className?: string; items: GridItem[] }) {
  const idList = items.map((i) => i.id).join(',');
  const [order, setOrder] = useState<string[]>(() => items.map((i) => i.id));
  const dragId = useRef<string | null>(null);
  const [overId, setOverId] = useState<string | null>(null);

  // 저장된 순서 적용(현재 존재하는 id 만, 새 id 는 뒤에 붙임).
  useEffect(() => {
    let saved: string[] = [];
    try { saved = JSON.parse(localStorage.getItem(KEY_PREFIX + gridId) || '[]'); } catch { saved = []; }
    const ids = items.map((i) => i.id);
    const merged = [...saved.filter((id) => ids.includes(id)), ...ids.filter((id) => !saved.includes(id))];
    setOrder(merged);
    // idList 가 바뀔 때만(패널 구성 변경) 재계산.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [gridId, idList]);

  const persist = useCallback((next: string[]) => {
    setOrder(next);
    try { localStorage.setItem(KEY_PREFIX + gridId, JSON.stringify(next)); } catch { /* 저장 실패는 무시 */ }
  }, [gridId]);

  const onDrop = useCallback((targetId: string) => {
    const from = dragId.current;
    dragId.current = null;
    setOverId(null);
    if (!from || from === targetId) return;
    const next = [...order];
    const fi = next.indexOf(from), ti = next.indexOf(targetId);
    if (fi < 0 || ti < 0) return;
    next.splice(fi, 1);
    next.splice(ti, 0, from);
    persist(next);
    // 재배치 후 차트가 새 폭에 맞도록 리사이즈를 유도.
    requestAnimationFrame(() => window.dispatchEvent(new Event('resize')));
  }, [order, persist]);

  const byId = new Map(items.map((i) => [i.id, i.node]));
  const ordered = order.filter((id) => byId.has(id));

  return (
    <div className={`grid ${className ?? ''}`} data-grid={gridId}>
      {ordered.map((id) => (
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
  Object.keys(localStorage).filter((k) => k.startsWith(KEY_PREFIX)).forEach((k) => localStorage.removeItem(k));
  location.reload();
}
