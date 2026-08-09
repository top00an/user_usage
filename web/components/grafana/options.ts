/*
 * ECharts 다크 옵션 빌더 — Grafana 톤. 모든 차트가 축·툴팁·범례를 갖도록 여기서 표준화한다.
 * "값이 안 보인다"는 불만을 없애는 것이 목적: 시계열엔 x(날짜)·y(값)축 + hover 툴팁 + 범례.
 */
import type { EChartsCoreOption } from 'echarts';

export const PALETTE = ['#5794f2', '#e0742f', '#73bf69', '#f2cc0c', '#b877d9', '#37872d', '#e0523e', '#8ab8ff'];
const INK = '#d8d9da';
const MUTED = '#8e9297';
const GRID = '#23282e';
const AXISLINE = '#2c3235';
const PANEL2 = '#1f2329';

export function short(n: number): string {
  const a = Math.abs(n);
  if (a >= 1e9) return (n / 1e9).toFixed(2) + 'B';
  if (a >= 1e6) return (n / 1e6).toFixed(1) + 'M';
  if (a >= 1e3) return (n / 1e3).toFixed(1) + 'K';
  return String(Math.round(n));
}
export const fmtInt = (n: number) => Math.round(n).toLocaleString('en-US');

const tooltipBase = {
  backgroundColor: PANEL2,
  borderColor: AXISLINE,
  borderWidth: 1,
  textStyle: { color: INK, fontSize: 12 },
};

/* 계열이 너무 많으면(예: 사용자별 다중 선) 상위 N 만 남기고 나머지는 "기타"로 합쳐 엉킴을
 * 막는다. 색은 팔레트 고정 순서, "기타"는 회색. 반환은 areaOption 이 그대로 그릴 수 있는 형태. */
export function capSeries(
  series: { name: string; data: number[] }[],
  topN = 6,
): { name: string; data: number[]; color: string }[] {
  const withTotal = series.map((s) => ({ ...s, total: s.data.reduce((a, b) => a + b, 0) }));
  withTotal.sort((a, b) => b.total - a.total);
  const head = withTotal.slice(0, topN);
  const rest = withTotal.slice(topN);
  const out = head.map((s, i) => ({ name: s.name, data: s.data, color: PALETTE[i % PALETTE.length]! }));
  if (rest.length) {
    const len = series[0]?.data.length ?? 0;
    const merged = Array.from({ length: len }, (_, i) => rest.reduce((a, s) => a + (s.data[i] ?? 0), 0));
    out.push({ name: `기타(+${rest.length})`, data: merged, color: '#6e7681' });
  }
  return out;
}

/* 시계열 영역 차트 — 다중 계열. fmt 는 y축·툴팁 값 포맷터. */
export function areaOption(
  xLabels: string[],
  series: { name: string; data: number[]; color: string }[],
  fmt: (n: number) => string = short,
): EChartsCoreOption {
  const many = series.length > 3; // 선이 많으면 채움을 줄여(투명) 겹침이 덜 지저분하게
  return {
    backgroundColor: 'transparent',
    // 오른쪽 여백을 넉넉히 — boundaryGap:false 라 마지막 날짜 라벨(예: 08-09)이 잘리지 않게.
    grid: { left: 6, right: 28, top: 26, bottom: 4, containLabel: true },
    tooltip: {
      ...tooltipBase,
      trigger: 'axis',
      axisPointer: { type: 'line', lineStyle: { color: MUTED } },
      valueFormatter: (v: unknown) => fmt(Number(v)),
    },
    legend: {
      type: 'scroll', top: 0, left: 0, icon: 'roundRect', itemWidth: 10, itemHeight: 10,
      textStyle: { color: MUTED, fontSize: 11 },
    },
    xAxis: {
      type: 'category', data: xLabels, boundaryGap: false,
      axisLine: { lineStyle: { color: AXISLINE } },
      // 첫·마지막 라벨을 반드시 보이게 하고, 겹치면 중간을 숨긴다.
      axisLabel: { color: MUTED, fontSize: 10, showMinLabel: true, showMaxLabel: true, hideOverlap: true },
      axisTick: { show: false },
    },
    yAxis: {
      type: 'value',
      splitLine: { lineStyle: { color: GRID } },
      axisLabel: { color: MUTED, fontSize: 10, formatter: (v: number) => fmt(v) },
    },
    series: series.map((s) => ({
      name: s.name, type: 'line', data: s.data,
      smooth: true, showSymbol: false,
      lineStyle: { width: 1.6, color: s.color },
      itemStyle: { color: s.color },
      // 선이 많으면(다중 사용자) 채움을 거의 없애 선만 보이게 — 색 면적이 겹쳐 뭉개지는 것 방지.
      areaStyle: many ? { opacity: 0.06, color: s.color } : {
        opacity: 0.9,
        color: {
          type: 'linear', x: 0, y: 0, x2: 0, y2: 1,
          colorStops: [
            { offset: 0, color: hexA(s.color, 0.35) },
            { offset: 1, color: hexA(s.color, 0.02) },
          ],
        },
      },
    })),
  };
}

/* 반원 게이지 (0~1). */
export function gaugeOption(value: number, color: string): EChartsCoreOption {
  const pct = Math.round(Math.max(0, Math.min(1, value)) * 100);
  return {
    backgroundColor: 'transparent',
    series: [{
      type: 'gauge', startAngle: 200, endAngle: -20, min: 0, max: 100,
      radius: '100%', center: ['50%', '62%'],
      progress: { show: true, width: 10, itemStyle: { color } },
      axisLine: { lineStyle: { width: 10, color: [[1, 'rgba(255,255,255,.18)']] } },
      pointer: { show: false }, axisTick: { show: false }, splitLine: { show: false }, axisLabel: { show: false },
      anchor: { show: false },
      detail: { valueAnimation: true, offsetCenter: [0, 0], fontSize: 22, fontWeight: 800, color: '#fff', formatter: '{value}%' },
      data: [{ value: pct }],
    }],
  };
}

/* 도넛(pie) — 카테고리 구성비. */
export function donutOption(
  rows: { name: string; value: number }[],
): EChartsCoreOption {
  return {
    backgroundColor: 'transparent',
    color: PALETTE,
    tooltip: { ...tooltipBase, trigger: 'item', formatter: (p: { name: string; value: number; percent: number }) => `${p.name}<br/><b>${fmtInt(p.value)}</b> (${p.percent}%)` },
    legend: {
      type: 'scroll', orient: 'vertical', right: 0, top: 'center',
      textStyle: { color: MUTED, fontSize: 11 }, itemWidth: 10, itemHeight: 10,
    },
    series: [{
      type: 'pie', radius: ['48%', '72%'], center: ['32%', '50%'],
      avoidLabelOverlap: true, label: { show: false }, labelLine: { show: false },
      itemStyle: { borderColor: '#181b1f', borderWidth: 2 },
      data: rows,
    }],
  };
}

/* 가로 막대 — Top N. */
export function barOption(
  rows: { name: string; value: number }[],
  fmt: (n: number) => string = fmtInt,
): EChartsCoreOption {
  const sorted = [...rows].sort((a, b) => a.value - b.value); // 아래→위 증가
  return {
    backgroundColor: 'transparent',
    grid: { left: 6, right: 48, top: 8, bottom: 4, containLabel: true },
    tooltip: { ...tooltipBase, trigger: 'axis', axisPointer: { type: 'shadow' }, valueFormatter: (v: unknown) => fmt(Number(v)) },
    xAxis: { type: 'value', splitLine: { lineStyle: { color: GRID } }, axisLabel: { color: MUTED, fontSize: 10, formatter: (v: number) => fmt(v) } },
    yAxis: { type: 'category', data: sorted.map((r) => r.name), axisLine: { lineStyle: { color: AXISLINE } }, axisLabel: { color: MUTED, fontSize: 11 }, axisTick: { show: false } },
    series: [{
      type: 'bar', data: sorted.map((r) => r.value),
      itemStyle: { color: '#5794f2', borderRadius: [0, 3, 3, 0] },
      label: { show: true, position: 'right', color: INK, fontSize: 11, formatter: (p: { value: number }) => fmt(p.value) },
      barMaxWidth: 18,
    }],
  };
}

/* hex + alpha → rgba */
function hexA(hex: string, a: number): string {
  const h = hex.replace('#', '');
  const n = parseInt(h.length === 3 ? h.split('').map((c) => c + c).join('') : h, 16);
  const r = (n >> 16) & 255, g = (n >> 8) & 255, b = n & 255;
  return `rgba(${r},${g},${b},${a})`;
}
