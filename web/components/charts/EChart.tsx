'use client';

/*
 * ECharts 얇은 React 래퍼. 정적 export(SSR) 라 init 은 반드시 클라이언트에서만 —
 * useEffect 안에서 echarts.init 하고, ResizeObserver 로 리사이즈, 언마운트 시 dispose 한다.
 * option 은 prop 으로 받아 setOption(option, true) 로 통째 교체(계열이 바뀌어도 잔상 없음).
 *
 * canvas 렌더러 — 선/영역이 안티에일리어싱된 실제 차트로 나온다(손으로 그린 SVG 아님).
 *
 * ── `height` 는 고정 높이가 아니라 **최소 높이**다 ────────────────────────
 *
 * 이 래퍼는 `height` 를 픽셀로 못 박고 있었다. 그러면 ResizeObserver 가 보는 박스가 세로로는
 * 절대 변하지 않는다 — 자유 캔버스(CanvasGrid)에서 패널을 아래로 늘려도 **카드만 커지고
 * 차트는 그대로**, 그 아래에 빈 공간이 남았다. 가로만 멀쩡했던 이유도 같다: 폭은 100% 라
 * 부모를 따라갔고 높이만 상수였다.
 *
 * 그래서 높이를 `min-height` 로 주고, 남는 세로 공간은 `.fillv`(globals.css: flex:1 1 auto)로
 * 먹는다. 두 성질이 **같이** 있어야 한다:
 *   · flex 부모 안(패널 본문 `.gpanel-body`·스탯 타일 `.gstat`)에서는 칸을 따라 커지고 줄어든다.
 *   · flex 부모가 아닌 자리에서 `flex` 는 무시되고 `min-height` 만 남는다 — 높이 auto 인 빈
 *     div 는 0 이 되므로 결과는 **종전과 정확히 같은 높이**다. 호출처 11곳을 고칠 필요가 없다.
 *
 * 호출처가 넘기는 숫자는 **아무렇게나 정한 값이 아니다**(브라우저에서 재서 잡은 값이다).
 * 그것이 이제 "이보다 작아지지는 않는다"는 바닥이 된다 — 좁은 칸에서 차트가 사라지지 않게.
 * 바닥보다 칸이 작으면 본문이 스크롤한다(잘리는 쪽이 아니라 스크롤하는 쪽이 규율이다).
 */
import { useEffect, useRef } from 'react';
import * as echarts from 'echarts';

/*
 * ── `label` 은 이 차트의 **이름**이다 ────────────────────────────────────
 *
 * 이 래퍼는 `aria-label="차트"` 를 못 박고 있었다. 대시보드에는 이 컴포넌트가 11개 있고,
 * canvas 라 안에는 읽을 텍스트가 한 글자도 없다 — 스크린리더로 화면을 훑으면 **"차트, 이미지"가
 * 11번** 들린다. 목록에서 원하는 차트로 건너뛸 방법이 없다는 뜻이다.
 *
 * 호출부는 이미 제목 문자열을 손에 들고 있다(`<Panel title="일별 토큰 추이">`) — 그걸 내려받는다.
 * **기본값은 남긴다**: 제목이 없는 호출부(CustomPanelView 등)가 무변경으로 컴파일돼야 하고,
 * 이름이 없는 `role="img"` 는 이름이 나쁜 것보다 더 나쁘다.
 */
export default function EChart({
  option, height = 160, className, label,
}: { option: echarts.EChartsCoreOption; height?: number; className?: string; label?: string }) {
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

  return (
    <div
      ref={ref}
      /* .fillv 는 호출처가 준 className 과 **함께** 간다 — 덮어쓰면 이 컴포넌트만 조용히 안 큰다. */
      className={className ? `fillv ${className}` : 'fillv'}
      style={{ width: '100%', minHeight: height }}
      role="img"
      aria-label={label ?? '차트'}
    />
  );
}

/* 드래그 재배치 후 등 외부에서 강제 리사이즈가 필요할 때 쓰는 이벤트 훅 대용 —
 * 패널이 window resize 를 디스패치하면 모든 EChart 가 스스로 resize 한다(ResizeObserver 가 잡는다). */
