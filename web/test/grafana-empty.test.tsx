import { describe, it, expect, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import GrafanaDash from '@/components/grafana/GrafanaDash';
import { donutOption } from '@/components/grafana/options';
import { golden, mockFetch, platformRoutes, type RouteSpec } from './helpers';

/*
 * ── 값이 없을 때 차트가 가짜 사실을 그리지 않는다 ─────────────────────────
 *
 * echarts 의 pie 는 **합계가 0 이면 균등 분할로 그린다.** 그래서 수락 0 · 거부 0 인 패널이
 * 화면에서는 "수락 50% · 거부 50%"처럼 보였다 — 관측이 아니라 렌더러의 기본값이다.
 * 면적 차트도 같은 종류의 사고를 낸다: 전부 0 인 계열은 0 평선을 그려 "추가 0줄"이라고 **단정**한다.
 * 미수집과 "실제로 0줄"은 다른 사실이므로, 화면은 단정하지 않고 미수집이라고 말해야 한다.
 *
 * setup.ts 는 EChart 를 null 로 모킹한다 — 그러면 "차트를 그렸는가"를 화면에서 볼 수 없다.
 * 이 파일은 바로 그 판정을 검사하므로 마커를 남기는 모킹으로 덮어쓴다.
 */
vi.mock('@/components/charts/EChart', () => ({
  default: () => <div data-testid="echart" />,
}));

/** PM 이 실제 수집기로 확인한 값 — 이 축은 백엔드가 수집한다. */
const DEV_REAL = {
  totals: { linesAdded: 26328, linesRemoved: 810, editsAccepted: 279, editsRejected: 1 },
  byDay: [
    { day: '2026-08-10', linesAdded: 26000, linesRemoved: 800, editsAccepted: 270, editsRejected: 1 },
    { day: '2026-08-09', linesAdded: 328, linesRemoved: 10, editsAccepted: 9, editsRejected: 0 },
  ],
};

/** 아직 보고가 없는 팀 · 구버전 수집기 — 서버는 응답하지만 이 축이 전부 0 이다. */
const DEV_EMPTY = {
  totals: { linesAdded: 0, linesRemoved: 0, editsAccepted: 0, editsRejected: 0 },
  byDay: [
    { day: '2026-08-10', linesAdded: 0, linesRemoved: 0, editsAccepted: 0, editsRejected: 0 },
    { day: '2026-08-09', linesAdded: 0, linesRemoved: 0, editsAccepted: 0, editsRejected: 0 },
  ],
};

function dashRoutes(extra: [string, RouteSpec][] = []): [string, RouteSpec][] {
  return [
    ...extra,
    ...platformRoutes(),
    ['/api/usage/summary', { body: golden('summary') }],
  ];
}

/*
 * 패널 하나를 좁혀 잡는다 — 제목 문구가 다듬어져도 깨지지 않게 id 로 잡는다.
 * 옛 DragGrid 든 지금의 CanvasGrid 든 패널 id 는 같은 `data-pid` 로 나온다(저장된 레이아웃의 키).
 */
function panel(pid: string): HTMLElement {
  const el = document.querySelector<HTMLElement>(`[data-pid="${pid}"]`);
  if (!el) throw new Error(`패널 ${pid} 이 렌더되지 않았다`);
  return el;
}

const chartsIn = (pid: string) => within(panel(pid)).queryAllByTestId('echart');

describe('도넛 옵션 — 합계 0 을 균등 분할로 그리지 않는다', () => {
  const segments = (option: ReturnType<typeof donutOption>) => {
    const series = (option as { series?: { data?: unknown[] }[] }).series ?? [];
    return series.flatMap((s) => s.data ?? []);
  };

  it('수락 0 · 거부 0 이면 세그먼트를 만들지 않는다', () => {
    const option = donutOption([{ name: '수락', value: 0 }, { name: '거부', value: 0 }]);
    expect(segments(option)).toEqual([]);
  });

  it('rows 가 비어 있어도 빈 링을 만들지 않는다', () => {
    expect(segments(donutOption([]))).toEqual([]);
  });

  it('값이 있으면 그대로 그린다 — 빈 상태 판정이 과하게 걸리지 않는다', () => {
    const rows = [{ name: '수락', value: 279 }, { name: '거부', value: 1 }];
    expect(segments(donutOption(rows))).toEqual(rows);
  });

  it('한쪽만 0 이면 그린다 — 관측된 0 은 숨기지 않는다', () => {
    const rows = [{ name: '수락', value: 279 }, { name: '거부', value: 0 }];
    expect(segments(donutOption(rows))).toEqual(rows);
  });
});

describe('개발 지표 패널 — 값이 없으면 미수집이라고 말한다', () => {
  it('편집 결정: 값이 전부 0 이면 도넛 대신 안내를 그린다', async () => {
    mockFetch(dashRoutes([['/api/usage/dev', { body: DEV_EMPTY }]]));
    render(<GrafanaDash />);
    await screen.findByText('활성 세션');

    expect(chartsIn('dev-edit')).toHaveLength(0);
    expect(within(panel('dev-edit')).getByText(/미수집/)).toBeInTheDocument();
  });

  it('편집 결정: dev 조회가 실패해도(null) 안내를 그린다', async () => {
    mockFetch(dashRoutes([['/api/usage/dev', { status: 500, body: { error: '터졌다' } }]]));
    render(<GrafanaDash />);
    await screen.findByText('활성 세션');

    expect(chartsIn('dev-edit')).toHaveLength(0);
    expect(within(panel('dev-edit')).getByText(/미수집/)).toBeInTheDocument();
  });

  it('편집 결정: 실데이터(수락 279 · 거부 1)에서는 도넛을 그린다', async () => {
    mockFetch(dashRoutes([['/api/usage/dev', { body: DEV_REAL }]]));
    render(<GrafanaDash />);
    await screen.findByText('활성 세션');

    expect(chartsIn('dev-edit')).toHaveLength(1);
    expect(within(panel('dev-edit')).queryByText(/미수집/)).not.toBeInTheDocument();
  });

  it('일별 LOC: 전부 0 이면 0 평선을 그려 "추가 0줄"이라고 단정하지 않는다', async () => {
    mockFetch(dashRoutes([['/api/usage/dev', { body: DEV_EMPTY }]]));
    render(<GrafanaDash />);
    await screen.findByText('활성 세션');

    expect(chartsIn('dev-loc')).toHaveLength(0);
    expect(within(panel('dev-loc')).getByText(/미수집/)).toBeInTheDocument();
  });

  it('일별 LOC: 값이 있으면 그린다', async () => {
    mockFetch(dashRoutes([['/api/usage/dev', { body: DEV_REAL }]]));
    render(<GrafanaDash />);
    await screen.findByText('활성 세션');

    expect(chartsIn('dev-loc')).toHaveLength(1);
    expect(within(panel('dev-loc')).queryByText(/미수집/)).not.toBeInTheDocument();
  });
});

/*
 * 편집 결정 하나만 고치면 나머지 도넛 3곳이 같은 사고를 그대로 낸다 —
 * 실측에서 서브에이전트 도넛이 빈 회색 링으로 그려졌다.
 */
describe('도구 · 서브에이전트 · 스킬 도넛 — 같은 규율을 탄다', () => {
  const PIDS = ['tool-usage', 'tool-agents', 'tool-skills'] as const;

  it('top 축이 비면 세 도넛 모두 안내를 그린다', async () => {
    const s = golden<Record<string, unknown>>('summary');
    mockFetch(dashRoutes([
      ['/api/usage/summary', { body: { ...s, top: { tool: [], agent: [], skill: [], bash: [], mcp: [] } } }],
      ['/api/usage/dev', { body: DEV_REAL }],
    ]));
    render(<GrafanaDash />);
    await screen.findByText('활성 세션');

    for (const pid of PIDS) {
      expect(chartsIn(pid)).toHaveLength(0);
      expect(within(panel(pid)).getByText(/미수집/)).toBeInTheDocument();
    }
  });

  it('top 축에 값이 있으면 세 도넛 모두 그린다', async () => {
    mockFetch(dashRoutes([['/api/usage/dev', { body: DEV_REAL }]]));
    render(<GrafanaDash />);
    await screen.findByText('활성 세션');

    for (const pid of PIDS) {
      expect(chartsIn(pid)).toHaveLength(1);
      expect(within(panel(pid)).queryByText(/미수집/)).not.toBeInTheDocument();
    }
  });
});
