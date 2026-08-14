'use client';

import { useCallback, useEffect, useRef } from 'react';

/*
 * 공용 모달 — Esc · 바깥 클릭 · Tab 트랩 · 포커스 복원을 한 곳에서 처리한다.
 * 각 화면이 수제로 만들면 저 넷 중 하나가 반드시 빠지고, 빠진 것은 키보드 사용자에게만 보인다.
 *
 * 모달은 **기본이 읽기 전용**이다(조회 도구라 대개 저장할 것이 없다) — 그래서 아무것도 주지 않으면
 * 종료 경로는 `닫기` 하나다. 자기 액션이 있는 모달(예: 차트 빌더의 `취소`·`대시보드에 추가`)만
 * `footer` 로 그 버튼들을 넘긴다. **넘긴 쪽이 기본 `닫기` 를 대체한다** — 나란히 두면 화면에
 * 종료 경로가 둘(`취소`·`닫기`)이 되어 어느 쪽이 취소인지 사용자가 판단해야 한다.
 *
 * ⚠ 그러므로 `footer` 를 주는 쪽은 **자기 footer 안에 닫는 길을 반드시 포함**해야 한다.
 *   (Esc·바깥 클릭은 남아 있지만 그 둘은 보이지 않는 경로다.)
 */

const FOCUSABLE = 'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])';

export interface ModalProps {
  title: string;
  onClose: () => void;
  maxWidth?: number;
  /**
   * 자기 액션이 있는 모달만 준다. 주면 기본 `닫기` **대신** 렌더된다(종료 경로 이중화 방지).
   * 안 주면 기존 동작 그대로 — 그래서 읽기 전용 모달들은 손댈 필요가 없다.
   */
  footer?: React.ReactNode;
  children: React.ReactNode;
}

export default function Modal({ title, onClose, maxWidth, footer, children }: ModalProps) {
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
          {footer ?? <button type="button" className="ghost" onClick={onClose}>닫기</button>}
        </div>
      </div>
    </div>
  );
}
