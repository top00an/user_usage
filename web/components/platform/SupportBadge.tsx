'use client';

/*
 * ── 없는 값을 0 으로 그리지 않는 자리 ─────────────────────────────────────
 *
 * 이 파일의 존재 이유는 하나다. 응답은 언제나 숫자 하나로 오고, 그 숫자가 0 일 때 화면은
 * 세 가지 다른 사실 중 무엇인지 모른 채로 `0` 을 그린다:
 *
 *   실제 0    수집됐고 값이 0 이다        → 숫자를 그린다(관측이다)
 *   미수집    그 플랫폼이 기록하지 않는다  → 회색 배지 + 사유(관측이 아니다)
 *   해당 없음 개념 자체가 없다            → 회색 배지 + 사유(값이 존재하지 않는다)
 *
 * 판정은 lib/platforms.ts 의 지원표가 한다. **그러니 값을 렌더하는 자리는 반드시 MetricValue 를
 * 거친다** — 컴포넌트마다 if 를 손으로 쓰면 한 곳이 빠지고, 빠진 그 칸이 "안 썼다"고 말한다.
 *
 * 접근성: 배지는 색이 아니라 **글자**로 상태를 말한다(회색 배지 + '미수집' 텍스트).
 * 색만으로 구분하면 색각 이상 사용자에게 그 구분이 통째로 사라진다 — ui.tsx 의 Flag 와 같은 규율.
 */

import { supportOf, SUPPORT_LABEL, type MetricId, type Support } from '@/lib/platforms';

export function SupportBadge({ support }: { support: Support }) {
  return (
    <span className="badge mute" title={support.why}>
      {SUPPORT_LABEL[support.state]}
    </span>
  );
}

/**
 * 값 한 칸. 지원되는 지표만 숫자를 그리고, 나머지는 배지로 대체한다.
 *
 * `children` 이 이미 포맷된 값이다(축약·통화 규칙은 lib/format.ts 의 몫이라 여기서 모른다).
 * `zero` 는 "그 값이 0 인가" — 0 을 그릴 때는 그것이 **관측된 0** 이라고 title 로 못박는다.
 */
export function MetricValue({
  platform, metric, zero = false, children,
}: {
  platform: string;
  metric: MetricId;
  zero?: boolean;
  children: React.ReactNode;
}) {
  const support = supportOf(platform, metric);
  if (support.state !== 'yes') return <SupportBadge support={support} />;
  if (zero) return <span title="수집된 값입니다 — 실제 0 입니다(미수집이 아닙니다)">{children}</span>;
  return <>{children}</>;
}

/** 축 × 플랫폼 한 칸(지원표·축 패널용). 지원되면 '수집됨'을 글자로 남긴다. */
export function SupportChip({ platform, metric, label }: { platform: string; metric: MetricId; label?: string }) {
  const support = supportOf(platform, metric);
  const cls = support.state === 'yes' ? 'badge ok' : 'badge mute';
  return (
    <span className={cls} title={support.why}>
      {label ? `${label} · ` : ''}{SUPPORT_LABEL[support.state]}
    </span>
  );
}
