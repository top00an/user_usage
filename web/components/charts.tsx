'use client';

/*
 * ── 인라인 SVG 차트 프리미티브 ────────────────────────────────────────────
 *
 * 외부 차트 라이브러리를 쓰지 않는다(CSP 가 CDN 을 막고, 번들도 얇게 유지). 전부 순수 SVG 다.
 * 색은 검증된 카테고리 팔레트(dataviz)를 CSS 변수 --series-1..8 로 **고정 순서** 사용한다 —
 * 순서가 색각(CVD) 안전성의 근거라 절대 섞거나 순환하지 않는다.
 *
 * 규율: 정체(identity)는 색만이 아니라 **직접 라벨**로도 준다(색각 이상에서도 읽힌다).
 * 매 마크에 <title> 을 달아 최소 호버(브라우저 네이티브 툴팁)를 준다.
 */
import { useId } from 'react';

const SERIES = Array.from({ length: 8 }, (_, i) => `var(--series-${i + 1})`);

/* ── 섹션 · 패널 래퍼 ── */
export function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="panel-section">
      <h2 className="panel-section-title">{title}</h2>
      <div className="panel-grid">{children}</div>
    </section>
  );
}

export function Panel({
  title, span, children,
}: { title: string; span?: 1 | 2 | 3; children: React.ReactNode }) {
  return (
    <div className={`panel${span ? ` span-${span}` : ''}`}>
      <div className="panel-title">{title}</div>
      {children}
    </div>
  );
}

/* ── 도넛 — 카테고리 구성비 ──────────────────────────────────────────────── */
export function Donut({
  data, unit = '',
}: { data: { label: string; value: number }[]; unit?: string }) {
  const clean = data.filter((d) => d.value > 0);
  const total = clean.reduce((a, d) => a + d.value, 0);
  if (!total) return <p className="help panel-empty">표시할 데이터가 없습니다.</p>;

  // 상위 7 + 나머지는 "기타"(9번째부터 색을 만들지 않는다 — 팔레트 규율).
  const sorted = [...clean].sort((a, b) => b.value - a.value);
  const head = sorted.slice(0, 7);
  const restSum = sorted.slice(7).reduce((a, d) => a + d.value, 0);
  const slices = restSum > 0 ? [...head, { label: '기타', value: restSum }] : head;

  const r = 42, c = 2 * Math.PI * r;
  let acc = 0;
  return (
    <div className="donut-wrap">
      <svg viewBox="0 0 120 120" className="donut" role="img" aria-label="구성비 도넛">
        <g transform="translate(60 60) rotate(-90)">
          {slices.map((s, i) => {
            const frac = s.value / total;
            const dash = c * frac;
            const seg = (
              <circle
                key={s.label}
                r={r} cx={0} cy={0} fill="none"
                stroke={i < 8 ? SERIES[i] : 'var(--fg-faint)'}
                strokeWidth={16}
                strokeDasharray={`${Math.max(dash - 1.5, 0)} ${c - Math.max(dash - 1.5, 0)}`}
                strokeDashoffset={-acc}
              >
                <title>{s.label}: {s.value.toLocaleString()}{unit} ({(frac * 100).toFixed(1)}%)</title>
              </circle>
            );
            acc += dash;
            return seg;
          })}
        </g>
        <text x="60" y="58" className="donut-center-v">{total.toLocaleString()}</text>
        <text x="60" y="72" className="donut-center-k">{unit || '합계'}</text>
      </svg>
      <ul className="legend">
        {slices.map((s, i) => (
          <li key={s.label}>
            <span className="legend-dot" style={{ background: i < 8 ? SERIES[i] : 'var(--fg-faint)' }} />
            <span className="legend-label">{s.label}</span>
            <span className="legend-val">{((s.value / total) * 100).toFixed(0)}%</span>
          </li>
        ))}
      </ul>
    </div>
  );
}

/* ── 영역 추이 — 단일 계열 시계열 ─────────────────────────────────────────── */
export function AreaTrend({
  points, unit = '', height = 120,
}: { points: { t: string; v: number }[]; unit?: string; height?: number }) {
  const gid = useId();
  if (points.length < 2) return <p className="help panel-empty">추이를 그릴 만큼의 기간이 아직 없습니다.</p>;

  const W = 320, H = height, pad = 6;
  const max = Math.max(...points.map((p) => p.v), 1);
  const stepX = (W - pad * 2) / (points.length - 1);
  const x = (i: number) => pad + i * stepX;
  const y = (v: number) => H - pad - (v / max) * (H - pad * 2);

  const line = points.map((p, i) => `${i === 0 ? 'M' : 'L'}${x(i).toFixed(1)} ${y(p.v).toFixed(1)}`).join(' ');
  const area = `${line} L${x(points.length - 1).toFixed(1)} ${H - pad} L${x(0).toFixed(1)} ${H - pad} Z`;
  const last = points[points.length - 1]!;

  return (
    <svg viewBox={`0 0 ${W} ${H}`} className="area" preserveAspectRatio="none" role="img"
      aria-label={`시계열 추이, 마지막 ${last.v.toLocaleString()}${unit}`}>
      <defs>
        <linearGradient id={`ag-${gid}`} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor="var(--accent)" stopOpacity="0.35" />
          <stop offset="100%" stopColor="var(--accent)" stopOpacity="0" />
        </linearGradient>
      </defs>
      <path d={area} fill={`url(#ag-${gid})`} />
      <path d={line} className="area-line" />
      <circle cx={x(points.length - 1)} cy={y(last.v)} r={3} className="area-dot">
        <title>{last.t}: {last.v.toLocaleString()}{unit}</title>
      </circle>
    </svg>
  );
}

/* ── 바 게이지 — 크기 비교(단일 색) ──────────────────────────────────────── */
export function BarGauge({
  data, fmt = (v) => v.toLocaleString(),
}: { data: { label: string; value: number }[]; fmt?: (v: number) => string }) {
  const rows = data.filter((d) => d.value > 0).sort((a, b) => b.value - a.value).slice(0, 8);
  if (!rows.length) return <p className="help panel-empty">표시할 데이터가 없습니다.</p>;
  const max = Math.max(...rows.map((d) => d.value), 1);
  return (
    <div className="bargauge">
      {rows.map((d) => (
        <div className="bargauge-row" key={d.label} title={`${d.label}: ${fmt(d.value)}`}>
          <span className="bargauge-label" title={d.label}>{d.label}</span>
          <span className="bargauge-track">
            <span className="bargauge-fill" style={{ width: `${(d.value / max) * 100}%` }} />
          </span>
          <span className="bargauge-val">{fmt(d.value)}</span>
        </div>
      ))}
    </div>
  );
}
