'use client';

/*
 * ECharts 얇은 React 래퍼. 정적 export(SSR) 라 init 은 반드시 클라이언트에서만 —
 * useEffect 안에서 echarts.init 하고, ResizeObserver 로 리사이즈, 언마운트 시 dispose 한다.
 * option 은 prop 으로 받아 setOption(option, true) 로 통째 교체(계열이 바뀌어도 잔상 없음).
 *
 * canvas 렌더러 — 선/영역이 안티에일리어싱된 실제 차트로 나온다(손으로 그린 SVG 아님).
 */
import { useEffect, useRef } from 'react';
import * as echarts from 'echarts';

export default function EChart({
  option, height = 160, className,
}: { option: echarts.EChartsCoreOption; height?: number; className?: string }) {
  const ref = useRef<HTMLDivElement | null>(null);
  const chartRef = useRef<echarts.ECharts | null>(null);

  useEffect(() => {
    if (!ref.current) return;
    const chart = echarts.init(ref.current, undefined, { renderer: 'canvas' });
    chartRef.current = chart;
    const ro = new ResizeObserver(() => chart.resize());
    ro.observe(ref.current);
    return () => {
      ro.disconnect();
      chart.dispose();
      chartRef.current = null;
    };
  }, []);

  useEffect(() => {
    chartRef.current?.setOption(option, true);
  }, [option]);

  return <div ref={ref} className={className} style={{ width: '100%', height }} role="img" aria-label="차트" />;
}

/* 드래그 재배치 후 등 외부에서 강제 리사이즈가 필요할 때 쓰는 이벤트 훅 대용 —
 * 패널이 window resize 를 디스패치하면 모든 EChart 가 스스로 resize 한다(ResizeObserver 가 잡는다). */
