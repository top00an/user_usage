'use client';

/*
 * 커스텀 패널 하나를 실제 데이터로 렌더한다. 정의(CustomPanel)에 따라 getSeries 로 조회해
 * line/bar/donut 으로 그린다. 사용자별(groupBy=user) 다중 선은 capSeries 로 상위 N + 기타.
 */
import { useCallback } from 'react';
import { getSeries } from '@/lib/api';
import { useResource } from '@/hooks/useResource';
import type { SeriesResponse } from '@/lib/types';
import type { CustomPanel } from '@/lib/customPanels';
import EChart from '@/components/charts/EChart';
import { areaOption, barOption, donutOption, capSeries, short, fmtInt } from './options';

function daysAgo(n: number): string {
  const d = new Date();
  d.setDate(d.getDate() - (n - 1));
  return d.toISOString().slice(0, 10);
}

export default function CustomPanelView({ panel }: { panel: CustomPanel }) {
  const load = useCallback(({ signal }: { signal: AbortSignal }) =>
    getSeries({
      metric: panel.metric,
      interval: 'day',
      groupBy: panel.groupBy === 'none' ? undefined : panel.groupBy,
      from: daysAgo(panel.days),
    }, { signal }),
  [panel.metric, panel.groupBy, panel.days]);

  const { state } = useResource<SeriesResponse>(load, [panel.metric, panel.groupBy, panel.days]);

  if (state.status === 'loading') return <div className="hint" style={{ padding: 20 }}>불러오는 중…</div>;
  if (state.status === 'error') return <div className="hint" style={{ padding: 20 }}>데이터를 불러오지 못했습니다.</div>;

  const lines = state.data.series ?? [];
  const isCost = panel.metric === 'cost';
  const fmt = isCost ? (v: number) => '$' + v.toFixed(2) : (panel.metric === 'tokens' ? short : fmtInt);

  if (!lines.length) return <div className="hint" style={{ padding: 20 }}>표시할 데이터가 없습니다.</div>;

  if (panel.type === 'line') {
    // x축: 모든 계열의 날짜 합집합(정렬). 각 계열을 t→v 로 정렬 매핑.
    const xs = Array.from(new Set(lines.flatMap((l) => l.points.map((p) => p.t)))).sort();
    const raw = lines.map((l) => {
      const m = new Map(l.points.map((p) => [p.t, p.v]));
      return { name: l.label, data: xs.map((t) => m.get(t) ?? 0) };
    });
    const capped = capSeries(raw, 6);
    return <EChart option={areaOption(xs.map((t) => t.slice(5)), capped, fmt)} height={200} />;
  }

  // bar/donut: 계열 합계로 구성비/크기.
  const rows = lines.map((l) => ({ name: l.label, value: l.total })).filter((r) => r.value > 0);
  if (panel.type === 'donut') return <EChart option={donutOption(rows)} height={210} />;
  return <EChart option={barOption(rows, fmt)} height={Math.max(140, rows.length * 30)} />;
}
