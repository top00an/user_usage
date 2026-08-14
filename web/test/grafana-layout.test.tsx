import { describe, it, expect, beforeAll, beforeEach, afterEach, vi } from 'vitest';
import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import GrafanaDash from '@/components/grafana/GrafanaDash';
import { Donut } from '@/components/charts';
import { addPanel, removePanel } from '@/lib/customPanels';
import { LAYOUT_PATH, SAVE_DEBOUNCE_MS } from '@/lib/layoutPrefs';
import { GRID_GAP, ROW_H, type DashLayout } from '@/lib/dashLayout';
import { golden, PLATFORMS_FIXTURE, type RouteSpec } from './helpers';

/*
 * ── 대시보드 배치가 사람을 배신하지 않는다 ────────────────────────────────
 *
 * 이 파일은 원래 DragGrid(섹션 안 순서 바꾸기 + localStorage)를 재고 있었다. 화면이 12열 자유
 * 캔버스 + 서버 저장으로 바뀌면서 **저장소와 조작 방식은 통째로 달라졌지만, 지켜야 할 것은
 * 그대로다.** 그래서 테스트를 지우지 않고 같은 의도에 다시 겨눴다:
 *
 *   ① 패널이 사라지지 않는다 · 서브트리가 리마운트되지 않는다(노드 동일성으로 잰다 —
 *      리마운트는 정의상 DOM 노드를 새로 만들고, 화면에서는 "가끔 깜빡인다"로만 보인다)
 *   ② 리렌더가 방금 바꾼 배치를 되돌리지 않는다      (옛: 드래그 순서 유지)
 *   ③ 저장이 막혀도 이번 세션의 조작은 먹는다        (옛: localStorage 가 던져도 순서가 남는다)
 *   ④ 저장된 배치가 **첫 프레임부터** 옳다           (옛: 기본 순서를 한 프레임 그리고 튀지 않는다)
 *
 * 여기에 서버 저장이 새로 들여온 것 셋을 더한다: 디바운스(드래그 한 번 = PUT 한 번) ·
 * 되돌리기(DELETE) · 읽기 전용이 기본(편집을 눌러야 핸들이 생긴다).
 *
 * setup.ts 가 EChart 를 null 로 모킹하므로 여기서는 껍데기(패널 DOM)만 본다.
 */

/** jsdom 에는 PointerEvent 가 없다 — MouseEvent 위에 pointerId/pointerType 만 얹은 최소 폴리필. */
beforeAll(() => {
  if (typeof window.PointerEvent === 'undefined') {
    class PointerEventPolyfill extends MouseEvent {
      readonly pointerId: number;
      readonly pointerType: string;
      constructor(type: string, init: PointerEventInit = {}) {
        super(type, init);
        this.pointerId = init.pointerId ?? 1;
        this.pointerType = init.pointerType ?? 'mouse';
      }
    }
    Object.defineProperty(window, 'PointerEvent', { value: PointerEventPolyfill, configurable: true });
  }
});

/** jsdom 은 레이아웃을 하지 않아 폭이 0 이다 — 그대로 두면 가로 드래그가 항상 0칸이 된다. */
const CANVAS_W = 1200;
const COL_STEP = (CANVAS_W + GRID_GAP) / 12;
const ROW_STEP = ROW_H + GRID_GAP;

/** PM 이 실제 수집기로 확인한 값 — 개발 지표 패널이 '미수집' 자리로 빠지지 않게 채워 둔다. */
const DEV_REAL = {
  totals: { linesAdded: 26328, linesRemoved: 810, editsAccepted: 279, editsRejected: 1 },
  byDay: [
    { day: '2026-08-10', linesAdded: 26000, linesRemoved: 800, editsAccepted: 270, editsRejected: 1 },
    { day: '2026-08-09', linesAdded: 328, linesRemoved: 10, editsAccepted: 9, editsRejected: 0 },
  ],
};

/*
 * 이 파일 전용 fetch 모킹.
 *
 * test/helpers.ts 의 mockFetch 는 경로만 보고 답한다. 여기서는 **같은 경로의 GET·PUT·DELETE 를
 * 갈라** 답해야 한다(저장은 실패하는데 조회는 성공하는 상황이 이 파일의 핵심 시나리오다).
 * helpers.ts 는 다른 오너 소유라 고치지 않고, 필요한 것만 여기서 만든다.
 */
interface LayoutMock {
  /** GET 이 돌려줄 저장값. null = 저장된 적 없음 → 기본 배치 */
  saved?: DashLayout | null;
  getStatus?: number;
  /** GET 을 늦춘다(ms) — 데이터가 먼저 도착하는 상황을 만든다. */
  getDelay?: number;
  putStatus?: number;
  /** 네트워크 자체가 끊긴 경우(응답 없음). */
  putThrows?: boolean;
  deleteStatus?: number;
}

interface Call { url: string; method: string; body: unknown }

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

function mockDash(layout: LayoutMock = {}, extra: [string, RouteSpec][] = []) {
  const routes: [string, RouteSpec][] = [
    ...extra,
    ['/api/usage/platforms', { body: PLATFORMS_FIXTURE }],
    ['/api/usage/summary', { body: golden('summary') }],
    ['/api/usage/dev', { body: DEV_REAL }],
  ];
  const calls: Call[] = [];

  const fn = vi.fn(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET';
    calls.push({ url, method, body: init?.body ? JSON.parse(String(init.body)) : undefined });

    if (url === LAYOUT_PATH) {
      if (method === 'PUT') {
        if (layout.putThrows) throw new TypeError('Failed to fetch');
        return json(layout.putStatus ?? 200, { ok: true, updatedAt: '2026-08-14T00:00:00Z' });
      }
      if (method === 'DELETE') return json(layout.deleteStatus ?? 200, { ok: true });
      if (layout.getDelay) await sleep(layout.getDelay);
      return json(layout.getStatus ?? 200, { layout: layout.saved ?? null, updatedAt: '' });
    }

    const hit = routes.find(([p]) => url.startsWith(p));
    const spec: RouteSpec = hit?.[1] ?? { status: 404, body: { error: '없는 경로' } };
    return json(spec.status ?? 200, spec.body ?? {});
  });
  vi.stubGlobal('fetch', fn);

  const of = (method: string) => calls.filter((c) => c.url === LAYOUT_PATH && c.method === method);
  return { calls, puts: () => of('PUT'), deletes: () => of('DELETE') };
}

/** 패널 한 장. 제목 문구가 다듬어져도 깨지지 않게 id(=저장된 레이아웃의 키)로 잡는다. */
function panel(pid: string): HTMLElement {
  const el = document.querySelector<HTMLElement>(`.dc-cell[data-pid="${pid}"]`);
  if (!el) throw new Error(`패널 ${pid} 이 렌더되지 않았다`);
  return el;
}

/** 화면에서 그 패널이 차지한 자리(CSS Grid 는 1부터 센다). */
const at = (pid: string) => `${panel(pid).style.gridColumn} / ${panel(pid).style.gridRow}`;

/** 마우스 한 판: 잡고 → 끌고 → 놓는다(CanvasGrid 의 4px 임계값을 넘기려 두 번 움직인다). */
function drag(el: HTMLElement, dx: number, dy: number) {
  fireEventPointer(el, 'pointerDown', 200, 200);
  fireEventPointer(window, 'pointerMove', 200 + dx / 2, 200 + dy / 2);
  fireEventPointer(window, 'pointerMove', 200 + dx, 200 + dy);
  fireEventPointer(window, 'pointerUp', 200 + dx, 200 + dy);
}

function fireEventPointer(target: HTMLElement | Window, type: string, clientX: number, clientY: number) {
  const ev = new window.PointerEvent(type.toLowerCase(), {
    clientX, clientY, button: 0, pointerId: 1, bubbles: true, cancelable: true,
  } as PointerEventInit);
  act(() => { target.dispatchEvent(ev); });
}

const LIVE = ['live-sessions', 'live-cost', 'live-tokens', 'live-output', 'live-hit', 'live-share'];

let headSlot: HTMLElement;

beforeEach(() => {
  localStorage.clear();
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({
    x: 0, y: 0, top: 0, left: 0, right: CANVAS_W, bottom: 900,
    width: CANVAS_W, height: 900, toJSON: () => ({}),
  } as DOMRect);
  // 셸(components/Dashboard.tsx)이 렌더하는 상단 액션 슬롯. GrafanaDash 는 여기에 포털로 얹는다.
  headSlot = document.createElement('div');
  headSlot.id = 'head-actions';
  document.body.appendChild(headSlot);
});
afterEach(() => {
  headSlot.remove();
  localStorage.clear();
  vi.restoreAllMocks();
});

async function mountDash() {
  render(<GrafanaDash />);
  await screen.findByText('활성 세션');
}

/** 편집 모드를 연다 — 평소에는 읽기 전용이라 드래그·핸들이 없다. */
async function openEditing() {
  await userEvent.click(screen.getByRole('button', { name: '배치 편집' }));
}

describe('패널은 사라지지도, 리마운트되지도 않는다', () => {
  it('대시보드가 리렌더돼도 패널 DOM 노드가 그대로 유지된다', async () => {
    mockDash();
    await mountDash();

    const before = LIVE.map(panel);
    const beforeCanvas = document.querySelector('.dashcanvas');

    // 커스텀 패널 추가 → 구독(useSyncExternalStore)이 GrafanaDash 를 리렌더한다.
    // 정체(identity)가 안정적이면 DOM 은 손대지 않아야 한다 — 리마운트는 노드를 새로 만든다.
    await act(async () => { addPanel({ title: '내 그래프 1', metric: 'tokens', type: 'line', groupBy: 'none', days: 7 }); });

    expect(document.querySelector('.dashcanvas')).toBe(beforeCanvas);
    LIVE.forEach((pid, i) => { expect(panel(pid)).toBe(before[i]); });
  });

  it('모든 패널이 하나의 캔버스 안에 있다 (섹션 경계가 없다)', async () => {
    mockDash();
    await mountDash();

    expect(document.querySelectorAll('.dashcanvas')).toHaveLength(1);
    for (const pid of [...LIVE, 'cost-models', 'dev-loc', 'cache-usage', 'tool-skills', 'rate-mcp', 'top-tools']) {
      expect(panel(pid).closest('.dashcanvas')).toBe(document.querySelector('.dashcanvas'));
    }
  });
});

describe('저장된 배치는 첫 프레임부터 적용된다', () => {
  /*
   * 옛 DragGrid 머리말이 적어 둔 사고를 캔버스에서 다시 막는다: 저장된 배치가 늦게 오는데
   * 화면을 먼저 그리면 **기본 배치를 한 프레임 보여 준 뒤 튄다.** 사람은 그 튐을 데이터
   * 변화로 읽는다. 그래서 배치를 다 읽기 전에는 캔버스를 아예 그리지 않는다.
   */
  it('데이터가 먼저 도착해도 기본 배치를 한 프레임 그리지 않는다', async () => {
    const { calls } = mockDash({
      saved: [{ id: 'live-share', x: 0, y: 0, w: 12, h: 2 }],
      getDelay: 60,
    });
    render(<GrafanaDash />);

    // 대시보드 데이터(summary 등)는 이미 도착했다.
    await waitFor(() => expect(calls.some((c) => c.url.startsWith('/api/usage/summary'))).toBe(true));
    await act(async () => { await sleep(20); });

    // 그런데도 패널은 아직 하나도 없다 — 기본 배치가 화면에 뜬 적이 없다는 뜻이다.
    expect(document.querySelector('.dc-cell[data-pid]')).toBeNull();

    await screen.findByText('활성 세션');
    // 처음 보이는 순간부터 저장된 자리다(기본 배치는 11열 2칸이었다).
    expect(panel('live-share').style.gridColumn).toBe('1 / span 12');
  });

  it('저장된 적 없으면 기본 배치로 그린다 (옛 섹션 열 수를 그대로 옮긴 자리)', async () => {
    mockDash({ saved: null });
    await mountDash();

    expect(panel('live-sessions').style.gridColumn).toBe('1 / span 2');
    expect(panel('live-share').style.gridColumn).toBe('11 / span 2');
    expect(panel('cache-usage').style.gridColumn).toBe('1 / span 12');
  });

  it('서버에 그 API 가 없어도(404) 화면이 죽지 않는다 — 기본 배치로 산다', async () => {
    // readOnly(remote) 배포·구버전 서버가 이 경로다(계약 개정 5).
    mockDash({ getStatus: 404 });
    await mountDash();

    expect(panel('live-sessions').style.gridColumn).toBe('1 / span 2');
    expect(document.querySelectorAll('.dc-cell[data-pid]').length).toBeGreaterThan(10);
  });

  it('응답이 이상해도(layout 이 배열이 아님) 기본 배치로 산다', async () => {
    mockDash({ saved: { nope: true } as unknown as DashLayout });
    await mountDash();

    expect(panel('live-share').style.gridColumn).toBe('11 / span 2');
  });
});

describe('편집 모드 — 평소에는 읽기 전용이다', () => {
  it('편집이 꺼져 있으면 드래그·리사이즈 핸들이 없다', async () => {
    mockDash();
    await mountDash();

    expect(document.querySelector('.dc-handle')).toBeNull();
    expect(document.querySelector('.dashcanvas.editing')).toBeNull();
    expect(panel('live-cost').getAttribute('tabindex')).toBeNull();
  });

  it('편집을 눌러야 핸들이 생기고, 끄면 다시 사라진다', async () => {
    mockDash();
    await mountDash();

    await openEditing();
    expect(document.querySelector('.dashcanvas.editing')).not.toBeNull();
    expect(document.querySelectorAll('.dc-handle').length).toBeGreaterThan(10);
    expect(panel('live-cost').getAttribute('tabindex')).toBe('0');

    await userEvent.click(screen.getByRole('button', { name: '편집 완료' }));
    expect(document.querySelector('.dc-handle')).toBeNull();
  });

  it('편집이 꺼져 있으면 끌어도 배치가 바뀌지 않는다 (PUT 도 없다)', async () => {
    const { puts } = mockDash();
    await mountDash();

    const before = at('live-sessions');
    drag(panel('live-sessions'), COL_STEP * 4, ROW_STEP * 2);
    await act(async () => { await sleep(SAVE_DEBOUNCE_MS + 120); });

    expect(at('live-sessions')).toBe(before);
    expect(puts()).toHaveLength(0);
  });
});

describe('저장 — 드래그 한 번에 PUT 한 번', () => {
  it('드래그를 놓으면 화면이 즉시 따라오고, PUT 은 디바운스 뒤에 한 번만 나간다', async () => {
    const { puts } = mockDash();
    await mountDash();
    await openEditing();

    const before = at('live-sessions');
    drag(panel('live-sessions'), COL_STEP * 4, 0);

    // 화면은 서버를 기다리지 않는다.
    expect(at('live-sessions')).not.toBe(before);
    // 디바운스 안에서는 아직 나가지 않았다.
    expect(puts()).toHaveLength(0);

    await act(async () => { await sleep(SAVE_DEBOUNCE_MS + 120); });
    expect(puts()).toHaveLength(1);

    const body = puts()[0]!.body as { layout: DashLayout };
    expect(body.layout.find((b) => b.id === 'live-sessions')).toMatchObject({ x: 4, w: 2, h: 2 });
    // 저장되는 값은 항상 정수 칸이다(서버 검증이 소수를 400 으로 거절한다).
    expect(body.layout.every((b) => [b.x, b.y, b.w, b.h].every(Number.isInteger))).toBe(true);
    await screen.findByText('배치 저장됨');
  });

  it('연달아 두 번 옮겨도 PUT 은 마지막 것 한 번이다', async () => {
    const { puts } = mockDash();
    await mountDash();
    await openEditing();

    drag(panel('live-sessions'), COL_STEP * 2, 0);
    drag(panel('live-sessions'), COL_STEP * 2, 0);

    await act(async () => { await sleep(SAVE_DEBOUNCE_MS + 120); });
    expect(puts()).toHaveLength(1);
    expect((puts()[0]!.body as { layout: DashLayout }).layout.find((b) => b.id === 'live-sessions'))
      .toMatchObject({ x: 4 });
  });
});

describe('저장이 실패해도 화면은 살아 있다', () => {
  /*
   * 옛 DragGrid 는 저장이 막힌 브라우저(사생활 보호·용량 초과)를 위해 세션 한정 폴백을 뒀다.
   * 저장소가 서버로 옮겨졌어도 판단은 같다 — **저장이 안 되는 것과 화면이 죽어 보이는 것은
   * 다른 문제다.** 사람은 패널을 끌었는데 제자리로 돌아오는 화면을 "고장"으로 읽는다.
   */
  it('서버가 500 이어도 이번 세션의 배치는 유지되고, 사실을 말한다', async () => {
    mockDash({ putStatus: 500 });
    await mountDash();
    await openEditing();

    drag(panel('live-sessions'), COL_STEP * 4, 0);
    const moved = at('live-sessions');

    await act(async () => { await sleep(SAVE_DEBOUNCE_MS + 120); });

    expect(at('live-sessions')).toBe(moved);            // 되돌아가지 않는다
    expect(await screen.findByText(/저장 실패/)).toBeInTheDocument();
    expect(screen.queryByText('배치 저장됨')).not.toBeInTheDocument();
  });

  it('네트워크가 끊겨도(fetch reject) 마찬가지다 — 이어서 더 옮길 수도 있다', async () => {
    mockDash({ putThrows: true });
    await mountDash();
    await openEditing();

    drag(panel('live-sessions'), COL_STEP * 4, 0);
    await act(async () => { await sleep(SAVE_DEBOUNCE_MS + 120); });
    expect(await screen.findByText(/저장 실패/)).toBeInTheDocument();

    // 실패한 뒤에도 조작은 계속 먹는다.
    const before = at('live-tokens');
    drag(panel('live-tokens'), 0, ROW_STEP * 3);
    expect(at('live-tokens')).not.toBe(before);
  });
});

describe('되돌리기 — 서버 저장을 지운다', () => {
  it('"기본 배치로 되돌리기" 가 DELETE 를 부르고 화면이 기본 배치로 돌아온다', async () => {
    const { deletes } = mockDash({ saved: [{ id: 'live-share', x: 0, y: 0, w: 12, h: 2 }] });
    await mountDash();
    expect(panel('live-share').style.gridColumn).toBe('1 / span 12');

    await openEditing();
    await userEvent.click(screen.getByRole('button', { name: '기본 배치로 되돌리기' }));

    expect(panel('live-share').style.gridColumn).toBe('11 / span 2');
    await waitFor(() => expect(deletes()).toHaveLength(1));
  });

  it('되돌리기는 대기 중인 저장을 취소한다 — DELETE 뒤에 옛 배치가 다시 저장되지 않게', async () => {
    const { puts, deletes } = mockDash();
    await mountDash();
    await openEditing();

    drag(panel('live-sessions'), COL_STEP * 4, 0);          // 디바운스 대기 중
    await userEvent.click(screen.getByRole('button', { name: '기본 배치로 되돌리기' }));

    await act(async () => { await sleep(SAVE_DEBOUNCE_MS + 120); });
    expect(deletes()).toHaveLength(1);
    expect(puts()).toHaveLength(0);
  });
});

describe("'내 그래프' 는 같은 캔버스에 들어간다", () => {
  it('패널을 추가·삭제해도 나머지 배치가 그대로다', async () => {
    mockDash({ saved: [{ id: 'live-share', x: 0, y: 0, w: 12, h: 2 }] });
    await mountDash();

    const before = [...LIVE, 'cache-usage', 'top-tools'].map(at);

    await act(async () => { addPanel({ title: '내 그래프 1', metric: 'tokens', type: 'line', groupBy: 'none', days: 7 }); });
    const added = document.querySelector('.dc-cell[data-pid^="cp-"]');
    expect(added).not.toBeNull();
    expect([...LIVE, 'cache-usage', 'top-tools'].map(at)).toEqual(before);

    const id = (added as HTMLElement).dataset.pid!;
    await act(async () => { removePanel(id); });
    expect(document.querySelector(`.dc-cell[data-pid="${id}"]`)).toBeNull();
    expect([...LIVE, 'cache-usage', 'top-tools'].map(at)).toEqual(before);
  });

  it('추가된 패널은 맨 아래에 붙는다 — 남의 자리를 밀어내지 않는다', async () => {
    mockDash();
    await mountDash();

    const bottom = Math.max(...[...document.querySelectorAll<HTMLElement>('.dc-cell[data-pid]')]
      .map((el) => Number(el.style.gridRow.split('/')[0])));

    await act(async () => { addPanel({ title: '내 그래프 1', metric: 'cost', type: 'bar', groupBy: 'none', days: 7 }); });
    const added = document.querySelector<HTMLElement>('.dc-cell[data-pid^="cp-"]')!;
    expect(Number(added.style.gridRow.split('/')[0])).toBeGreaterThanOrEqual(bottom);
  });
});

describe('상단 액션 · 툴바', () => {
  it('셸의 #head-actions 에는 그래프 추가만 얹는다', async () => {
    mockDash();
    await mountDash();

    expect(headSlot.textContent).toContain('그래프 추가');
    // 레이아웃 초기화는 localStorage 청소 + reload 였다. 이제 서버 DELETE 라 편집 툴바로 옮겼다.
    expect(headSlot.textContent).not.toContain('초기화');
  });

  it('되돌리기는 편집 중에만 보인다 (읽는 화면에 되돌릴 것이 없다)', async () => {
    mockDash();
    await mountDash();

    expect(screen.queryByRole('button', { name: '기본 배치로 되돌리기' })).not.toBeInTheDocument();
    await openEditing();
    expect(screen.getByRole('button', { name: '기본 배치로 되돌리기' })).toBeInTheDocument();
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
