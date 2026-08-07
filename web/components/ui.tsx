'use client';

import { failureKind, type FailureKind } from '@/lib/api';
import { n, shortTokens } from '@/lib/format';

/*
 * 화면 조각들 — 두 탭이 공유한다. 스타일은 전부 app/globals.css 의 클래스이고
 * 여기서 <style> 을 쓰지 않는다(토큰이 두 벌이 되면 한 벌만 고쳐지는 날이 온다).
 */

export function Card({
  title, aside, children, className = '', accent = false,
}: {
  title?: string;
  aside?: React.ReactNode;
  children: React.ReactNode;
  className?: string;
  accent?: boolean;
}) {
  return (
    <section
      className={`card glass ${className}`}
      style={accent ? { borderColor: 'var(--accent-from)' } : undefined}
    >
      {(title || aside) && (
        <div className="between mb">
          {title ? <h3>{title}</h3> : <span />}
          {aside}
        </div>
      )}
      {children}
    </section>
  );
}

/**
 * 토큰 수 한 칸. 축약해 보여주고 **정확한 값은 title 로 남긴다** —
 * 축약만 남기면 "1.2만"이 실제로 얼마인지 확인할 자리가 화면에서 사라진다.
 */
export function TokenCount({ v }: { v: unknown }) {
  return <span title={n(v)}>{shortTokens(v)}</span>;
}

export function TableWrap({ children }: { children: React.ReactNode }) {
  return <div className="table-wrap">{children}</div>;
}

/** 근사·미상 표시. 색만으로 말하지 않는다 — 색각 이상 사용자에게 사라지면 근사가 정확값이 된다. */
export function Flag({ children, title }: { children: React.ReactNode; title?: string }) {
  return <span className="flag" title={title}>⚠ {children}</span>;
}

export function Empty({ children }: { children: React.ReactNode }) {
  return <div className="muted">{children}</div>;
}

export function Loading({ label = '불러오는 중…' }: { label?: string }) {
  return <div className="muted" role="status" aria-live="polite">{label}</div>;
}

/*
 * ── 실패 화면: **status 로 분기한다. 문구로 하지 않는다.** ─────────────────
 *
 * 에러 문구는 사람이 읽는 글이라 언제든 다듬어진다. 분기를 문구에 걸면 그때 화면이 예외 없이
 * 조용히 틀린 쪽으로 넘어간다. 현행이 `/403|forbidden|권한/i.test(msg)` 로 하던 자리다 —
 * 서버가 403 문구를 한 글자만 바꿔도 그 화면은 "권한 안내" 대신 "불러오지 못했습니다"가 된다.
 */
const COPY: Record<FailureKind, { head: string; body: string }> = {
  unauthorized: {
    head: '토큰이 필요합니다',
    body: '토큰이 올바르지 않거나 만료되었습니다. 다시 입력하세요.',
  },
  forbidden: {
    head: '권한이 필요합니다',
    body: '이 화면은 사용자별 사용량과 비용을 담고 있어 관리자만 열람합니다.',
  },
  notFound: {
    head: '없는 자원입니다',
    body: '요청한 항목이 서버에 없습니다. 화면이 낡았을 수 있으니 새로고침해 보세요.',
  },
  badRequest: {
    head: '요청이 올바르지 않습니다',
    body: '조회 조건을 서버가 받아들이지 않았습니다.',
  },
  server: {
    head: '서버가 응답하지 못했습니다',
    body: '잠시 뒤 다시 시도하세요. 계속되면 서버 로그를 확인해야 합니다.',
  },
  network: {
    head: '연결 실패',
    body: '서버에 연결하지 못했습니다. 주소와 네트워크를 확인하세요.',
  },
  aborted: {
    head: '요청이 취소되었습니다',
    body: '화면이 바뀌어 이전 조회를 버렸습니다.',
  },
};

export function ErrorState({ what, error, onRetry }: { what: string; error: unknown; onRetry?: () => void }) {
  const kind = failureKind(error);
  const copy = COPY[kind];
  // 서버가 보낸 원문은 **보조 정보로만** 둔다 — 분기는 위에서 이미 끝났다.
  const detail = error instanceof Error ? error.message : '';
  return (
    <section className="card glass" role="alert">
      <h3>{copy.head}</h3>
      <p className="help mt-sm">{copy.body}</p>
      <p className="help mt-sm">
        {what}
        {detail ? ` — 서버 응답: ${detail}` : ''}
      </p>
      {onRetry && kind !== 'forbidden' && kind !== 'unauthorized' && (
        <div className="row mt"><button type="button" className="ghost" onClick={onRetry}>다시 시도</button></div>
      )}
    </section>
  );
}

/*
 * 탭 목록 — 키보드로 좌우 이동이 되어야 한다(WAI-ARIA tabs 패턴).
 * 마우스만 되는 탭은 접근성 위반이면서, 표가 긴 이 화면에서는 마우스 사용자에게도 불편하다.
 */
export interface TabDef<Id extends string> { id: Id; label: string }

export function Tabs<Id extends string>({
  tabs, active, onSelect, label, idPrefix,
}: {
  tabs: readonly TabDef<Id>[];
  active: Id;
  onSelect: (id: Id) => void;
  label: string;
  idPrefix: string;
}) {
  const move = (dir: 1 | -1) => {
    const i = tabs.findIndex((t) => t.id === active);
    const next = tabs[(i + dir + tabs.length) % tabs.length];
    if (next) {
      onSelect(next.id);
      // 포커스를 따라 옮긴다 — 선택만 바뀌고 포커스가 남으면 다음 화살표가 엉뚱하게 돈다.
      requestAnimationFrame(() => document.getElementById(`${idPrefix}-${next.id}`)?.focus());
    }
  };
  return (
    <div className="tabs" role="tablist" aria-label={label}>
      {tabs.map((t) => {
        const on = t.id === active;
        return (
          <button
            key={t.id}
            id={`${idPrefix}-${t.id}`}
            type="button"
            role="tab"
            className="tab"
            aria-selected={on}
            aria-controls={`${idPrefix}-panel`}
            tabIndex={on ? 0 : -1}
            onClick={() => onSelect(t.id)}
            onKeyDown={(e) => {
              if (e.key === 'ArrowRight') { e.preventDefault(); move(1); }
              if (e.key === 'ArrowLeft') { e.preventDefault(); move(-1); }
            }}
          >
            {t.label}
          </button>
        );
      })}
    </div>
  );
}
