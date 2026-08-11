import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { act, fireEvent, render, screen } from '@testing-library/react';
import GrafanaDash from '@/components/grafana/GrafanaDash';
import { Donut } from '@/components/charts';
import { addPanel } from '@/lib/customPanels';
import { golden, mockFetch, platformRoutes, type RouteSpec } from './helpers';

/*
 * ── 레이아웃이 리렌더를 견딘다 ────────────────────────────────────────────
 *
 * 이 파일이 지키는 것은 **컴포넌트 정체(identity)** 다. 렌더 함수 안에서 컴포넌트를 만들면
 * 매 렌더마다 새 타입이 되고, React 는 같은 자리를 "다른 컴포넌트"로 보아 서브트리를 통째로
 * 언마운트·리마운트한다. 그러면 DOM 노드가 새로 생기고 그 안의 상태·포커스·스크롤이 날아간다
 * (여기서는 DragGrid 의 드래그 순서와 드래그중 하이라이트).
 *
 * 그 사고는 화면에서 "가끔 패널이 깜빡인다" 정도로만 보여서 눈으로는 못 잡는다. 그래서
 * **DOM 노드 동일성**으로 잰다 — 리마운트는 정의상 노드를 새로 만든다.
 *
 * setup.ts 가 EChart 를 null 로 모킹하므로 여기서는 껍데기(패널 DOM)만 본다 — 이 파일이 재는
 * 것이 정체이지 차트가 아니다.
 */

/** PM 이 실제 수집기로 확인한 값 — 개발 지표 패널이 '미수집' 자리로 빠지지 않게 채워 둔다. */
const DEV_REAL = {
  totals: { linesAdded: 26328, linesRemoved: 810, editsAccepted: 279, editsRejected: 1 },
  byDay: [
    { day: '2026-08-10', linesAdded: 26000, linesRemoved: 800, editsAccepted: 270, editsRejected: 1 },
    { day: '2026-08-09', linesAdded: 328, linesRemoved: 10, editsAccepted: 9, editsRejected: 0 },
  ],
};

function dashRoutes(extra: [string, RouteSpec][] = []): [string, RouteSpec][] {
  return [
    ...extra,
    ...platformRoutes(),
    ['/api/usage/summary', { body: golden('summary') }],
    ['/api/usage/dev', { body: DEV_REAL }],
  ];
}

/** 패널 하나(DragGrid 의 data-pid). 제목 문구가 다듬어져도 깨지지 않게 id 로 잡는다. */
function panel(pid: string): HTMLElement {
  const el = document.querySelector<HTMLElement>(`[data-pid="${pid}"]`);
  if (!el) throw new Error(`패널 ${pid} 이 렌더되지 않았다`);
  return el;
}

/** 한 그리드의 패널 순서 — 화면에 보이는 순서 그대로. */
function orderOf(gid: string): string[] {
  const grid = document.querySelector(`[data-grid="${gid}"]`);
  if (!grid) throw new Error(`그리드 ${gid} 이 렌더되지 않았다`);
  return Array.from(grid.querySelectorAll<HTMLElement>('[data-pid]')).map((e) => e.dataset.pid!);
}

const LIVE_DEFAULT = ['live-sessions', 'live-cost', 'live-tokens', 'live-output', 'live-hit', 'live-share'];

/** jsdom 에는 DragEvent 가 없다 — 핸들러가 실제로 읽는 것(effectAllowed)만 갖춘 최소 객체. */
const dt = () => ({ effectAllowed: '', dropEffect: '', setData() {}, getData: () => '' });

let headSlot: HTMLElement;

beforeEach(() => {
  localStorage.clear();
  // 셸(components/Dashboard.tsx)이 렌더하는 상단 액션 슬롯. GrafanaDash 는 여기에 포털로 얹는다.
  headSlot = document.createElement('div');
  headSlot.id = 'head-actions';
  document.body.appendChild(headSlot);
});
afterEach(() => {
  headSlot.remove();
  localStorage.clear();
});

async function mountDash() {
  render(<GrafanaDash />);
  await screen.findByText('활성 세션');
}

describe('섹션 컴포넌트는 렌더마다 새로 만들어지지 않는다 (리마운트 없음)', () => {
  it('대시보드가 리렌더돼도 패널 DOM 노드가 그대로 유지된다', async () => {
    mockFetch(dashRoutes());
    await mountDash();

    const before = LIVE_DEFAULT.map(panel);
    const beforeGrid = document.querySelector('[data-grid="live"]');

    // 커스텀 패널 추가 → 구독(useSyncExternalStore)이 GrafanaDash 를 리렌더한다.
    // 섹션들의 props 는 그대로이므로, 정체가 안정적이면 DOM 은 손대지 않아야 한다.
    await act(async () => { addPanel({ title: '내 그래프 1', metric: 'tokens', type: 'line', groupBy: 'none', days: 7 }); });

    expect(document.querySelector('[data-grid="live"]')).toBe(beforeGrid);
    LIVE_DEFAULT.forEach((pid, i) => {
      expect(panel(pid)).toBe(before[i]);
    });
  });

  it('리렌더가 드래그로 바꾼 순서를 되돌리지 않는다 (DragGrid 상태 유지)', async () => {
    mockFetch(dashRoutes());
    await mountDash();
    expect(orderOf('live')).toEqual(LIVE_DEFAULT);

    // live-tokens 를 맨 앞(live-sessions 자리)으로 끌어다 놓는다.
    fireEvent.dragStart(panel('live-tokens'), { dataTransfer: dt() });
    fireEvent.dragOver(panel('live-sessions'), { dataTransfer: dt() });
    fireEvent.drop(panel('live-sessions'), { dataTransfer: dt() });

    const reordered = ['live-tokens', 'live-sessions', 'live-cost', 'live-output', 'live-hit', 'live-share'];
    expect(orderOf('live')).toEqual(reordered);

    await act(async () => { addPanel({ title: '내 그래프 1', metric: 'cost', type: 'bar', groupBy: 'none', days: 7 }); });

    expect(orderOf('live')).toEqual(reordered);
  });
});

describe('DragGrid — 순서를 저장하고 되살린다', () => {
  it('저장된 순서가 있으면 그 순서로 그린다', async () => {
    const saved = ['live-hit', 'live-share', 'live-sessions'];
    localStorage.setItem('ccdash-order:live', JSON.stringify(saved));
    mockFetch(dashRoutes());
    await mountDash();

    // 저장된 것 먼저, 저장에 없던 새 id 는 뒤에 원래 순서대로 붙는다.
    expect(orderOf('live')).toEqual([...saved, 'live-cost', 'live-tokens', 'live-output']);
  });

  it('저장에 없는 낡은 id 는 버린다', async () => {
    localStorage.setItem('ccdash-order:live', JSON.stringify(['live-hit', 'gone-panel', 'live-sessions']));
    mockFetch(dashRoutes());
    await mountDash();

    expect(orderOf('live')).not.toContain('gone-panel');
    expect(orderOf('live')).toHaveLength(LIVE_DEFAULT.length);
  });

  it('드래그로 바꾼 순서를 localStorage 에 남긴다 (새로고침에도 유지)', async () => {
    mockFetch(dashRoutes());
    await mountDash();

    fireEvent.dragStart(panel('live-share'), { dataTransfer: dt() });
    fireEvent.dragOver(panel('live-cost'), { dataTransfer: dt() });
    fireEvent.drop(panel('live-cost'), { dataTransfer: dt() });

    const expected = ['live-sessions', 'live-share', 'live-cost', 'live-tokens', 'live-output', 'live-hit'];
    expect(orderOf('live')).toEqual(expected);
    expect(JSON.parse(localStorage.getItem('ccdash-order:live')!)).toEqual(expected);
  });

  /*
   * 사생활 보호 모드·용량 초과면 localStorage.setItem 이 던진다. 그때 저장이 안 되는 것은
   * 어쩔 수 없지만 **드래그가 통째로 죽어서는 안 된다** — 사람은 패널을 끌었는데 아무 일도
   * 일어나지 않는 화면을 "고장"으로 읽는다. 그 세션 동안은 순서가 유지돼야 한다.
   */
  it('저장이 막힌 브라우저에서도 그 세션 동안은 재배치가 동작한다', async () => {
    mockFetch(dashRoutes());
    await mountDash();

    /*
     * 실패한 쓰기는 모듈 수준 폴백에 남아 페이지 세션 내내 산다(그게 이 기능이다).
     * 그래서 다른 테스트가 쓰지 않는 그리드('tools')로 잰다 — 테스트끼리 순서를 물려주지 않게.
     */
    const setItem = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('QuotaExceededError');
    });
    try {
      fireEvent.dragStart(panel('tool-skills'), { dataTransfer: dt() });
      fireEvent.dragOver(panel('tool-usage'), { dataTransfer: dt() });
      fireEvent.drop(panel('tool-usage'), { dataTransfer: dt() });

      expect(orderOf('tools')).toEqual(['tool-skills', 'tool-usage', 'tool-agents']);
    } finally {
      setItem.mockRestore();
    }
  });

  it('자기 자신 위에 떨어뜨리면 순서가 그대로다', async () => {
    mockFetch(dashRoutes());
    await mountDash();

    fireEvent.dragStart(panel('live-cost'), { dataTransfer: dt() });
    fireEvent.drop(panel('live-cost'), { dataTransfer: dt() });

    expect(orderOf('live')).toEqual(LIVE_DEFAULT);
  });

  it('드래그 중인 대상 위에 있으면 그 칸에 하이라이트가 붙는다', async () => {
    mockFetch(dashRoutes());
    await mountDash();

    fireEvent.dragStart(panel('live-tokens'), { dataTransfer: dt() });
    fireEvent.dragOver(panel('live-cost'), { dataTransfer: dt() });
    expect(panel('live-cost').className).toContain('over');

    fireEvent.dragLeave(panel('live-cost'), { dataTransfer: dt() });
    expect(panel('live-cost').className).not.toContain('over');
  });
});

describe('상단 액션 버튼 — 셸의 슬롯에 포털로 얹는다', () => {
  it('#head-actions 안에 두 버튼이 들어간다', async () => {
    mockFetch(dashRoutes());
    await mountDash();

    expect(headSlot.textContent).toContain('그래프 추가');
    expect(headSlot.textContent).toContain('레이아웃 초기화');
  });
});

/*
 * ── 도넛의 조각 오프셋 ────────────────────────────────────────────────────
 *
 * 각 조각은 앞 조각들의 길이 합만큼 회전해 있어야 한다(strokeDashoffset = -누적합).
 * 누산이 어긋나면 조각이 서로 겹치거나 링에 구멍이 생긴다 — 값은 맞는데 그림이 거짓말을 한다.
 */
describe('Donut — 조각이 누적 오프셋대로 놓인다', () => {
  const C = 2 * Math.PI * 42;
  const offsets = () =>
    Array.from(document.querySelectorAll('circle')).map((c) => Number(c.getAttribute('stroke-dashoffset')));

  it('첫 조각은 0, 그 다음은 앞 조각들의 길이 합만큼 밀린다', () => {
    render(<Donut data={[{ label: 'a', value: 3 }, { label: 'b', value: 1 }]} />);
    const got = offsets();
    expect(got).toHaveLength(2);
    expect(got[0]).toBeCloseTo(-0, 10);
    expect(got[1]).toBeCloseTo(-C * 0.75, 10);
  });

  it('조각 셋도 순서대로 누적된다', () => {
    render(<Donut data={[{ label: 'a', value: 2 }, { label: 'b', value: 1 }, { label: 'c', value: 1 }]} />);
    const got = offsets();
    expect(got).toHaveLength(3);
    expect(got[0]).toBeCloseTo(-0, 10);
    expect(got[1]).toBeCloseTo(-C * 0.5, 10);
    expect(got[2]).toBeCloseTo(-C * 0.75, 10);
  });

  it('상위 7 + 기타 — 8조각이 되고 마지막이 나머지 합이다', () => {
    const data = Array.from({ length: 10 }, (_, i) => ({ label: `s${i}`, value: 10 - i }));
    render(<Donut data={data} unit="회" />);
    expect(offsets()).toHaveLength(8);
    // 기타 = 3 + 2 + 1 = 6, 전체 = 55
    expect(screen.getByText(/기타: 6회/)).toBeInTheDocument();
  });

  it('값이 전부 0 이면 그리지 않는다', () => {
    render(<Donut data={[{ label: 'a', value: 0 }]} />);
    expect(offsets()).toHaveLength(0);
    expect(screen.getByText('표시할 데이터가 없습니다.')).toBeInTheDocument();
  });
});
