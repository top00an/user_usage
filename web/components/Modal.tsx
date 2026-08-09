'use client';

import { useCallback, useEffect, useRef } from 'react';

/*
 * 공용 모달 — Esc · 바깥 클릭 · Tab 트랩 · 포커스 복원을 한 곳에서 처리한다.
 * 각 화면이 수제로 만들면 저 넷 중 하나가 반드시 빠지고, 빠진 것은 키보드 사용자에게만 보인다.
 *
 * 이 앱의 모달은 전부 **읽기 전용**이다(조회 도구라 저장할 것이 없다) — 그래서 확인 버튼이 없고
 * 닫기 하나다.
 */

const FOCUSABLE = 'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])';

export interface ModalProps {
  title: string;
  onClose: () => void;
  maxWidth?: number;
  children: React.ReactNode;
}

export default function Modal({ title, onClose, maxWidth, children }: ModalProps) {
  const overlay = useRef<HTMLDivElement>(null);
  const dialog = useRef<HTMLDivElement>(null);
  const restoreTo = useRef<Element | null>(null);

  const focusables = useCallback(
    () => Array.from(dialog.current?.querySelectorAll<HTMLElement>(FOCUSABLE) ?? [])
      .filter((el) => !el.hasAttribute('disabled')),
    [],
  );

  useEffect(() => {
    restoreTo.current = document.activeElement;
    const first = focusables()[0] ?? dialog.current;
    first?.focus();

    const prevOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';

    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') { e.preventDefault(); onClose(); return; }
      if (e.key !== 'Tab') return;
      const f = focusables();
      if (!f.length) return;
      const first = f[0]!;
      const last = f[f.length - 1]!;
      if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
      else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
    }
    document.addEventListener('keydown', onKey);

    return () => {
      document.removeEventListener('keydown', onKey);
      document.body.style.overflow = prevOverflow;
      // 포커스 복원 — 없으면 모달을 닫은 키보드 사용자가 문서 맨 위로 튕긴다.
      const back = restoreTo.current;
      if (back instanceof HTMLElement) back.focus();
    };
  }, [focusables, onClose]);

  return (
    <div
      className="modal-overlay"
      ref={overlay}
      onMouseDown={(e) => { if (e.target === overlay.current) onClose(); }}
    >
      <div
        className="modal glass-strong"
        role="dialog"
        aria-modal="true"
        aria-label={title}
        tabIndex={-1}
        ref={dialog}
        style={maxWidth ? { maxWidth: `${maxWidth}px` } : undefined}
      >
        <h3>{title}</h3>
        <div className="modal-body">{children}</div>
        <div className="actions">
          <button type="button" className="ghost" onClick={onClose}>닫기</button>
        </div>
      </div>
    </div>
  );
}
