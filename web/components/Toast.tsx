'use client';

import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';

/*
 * 토스트 — 화면을 갈아엎지 않고 알려야 할 실패의 자리(예: 사용자별 상세 조회 실패).
 *
 * 타이머는 **하나만** 둔다. 토스트마다 걸면 앞선 타이머가 뒤늦은 토스트를 조기에 지운다.
 * aria-live="polite" 로 두어 스크린리더가 현재 읽는 것을 끊지 않는다.
 */

type ToastKind = 'info' | 'err';
interface ToastState { msg: string; kind: ToastKind; seq: number }

const ToastCtx = createContext<(msg: string, kind?: ToastKind) => void>(() => {});

export function useToast() {
  return useContext(ToastCtx);
}

export function ToastProvider({ children }: { children: React.ReactNode }) {
  const [toast, setToast] = useState<ToastState | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const show = useCallback((msg: string, kind: ToastKind = 'info') => {
    setToast((prev) => ({ msg, kind, seq: (prev?.seq ?? 0) + 1 }));
  }, []);

  useEffect(() => {
    if (!toast) return;
    if (timer.current) clearTimeout(timer.current);
    timer.current = setTimeout(() => setToast(null), 3400);
    return () => { if (timer.current) clearTimeout(timer.current); };
  }, [toast]);

  const value = useMemo(() => show, [show]);

  return (
    <ToastCtx.Provider value={value}>
      {children}
      <div
        className={`toast glass-strong${toast ? ` show ${toast.kind}` : ''}`}
        role="status"
        aria-live="polite"
        aria-atomic="true"
      >
        {toast?.msg ?? ''}
      </div>
    </ToastCtx.Provider>
  );
}
