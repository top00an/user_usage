import { describe, it, expect, beforeAll, beforeEach, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import CanvasGrid, { type CanvasItem } from '@/components/grafana/CanvasGrid';
import { GRID_GAP, ROW_H, type DashLayout } from '@/lib/dashLayout';

/*
 * ── 자유 캔버스의 배선 ────────────────────────────────────────────────────
 *
 * 수학은 dashlayout.test.ts 가 잡는다. 여기서 재는 것은 **화면과 이벤트**다:
 *   ① 저장된 레이아웃과 코드의 패널 목록이 어긋나도 패널이 사라지지 않는가
 *   ② 드래그 중에 onLayoutChange 가 새지 않고, 놓는 순간 정확히 1회 오는가
 *      (프레임마다 부르면 다음 웨이브의 저장 훅이 프레임마다 PUT 을 쏜다)
 *   ③ 마우스 없이도 옮기고 크기를 바꿀 수 있는가
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

/*
 * 캔버스 폭 1200px → 한 칸 피치 (1200 + 12) / 12 = 101px. 아래 드래그 거리는 전부 이 값을 쓴다.
 * jsdom 은 레이아웃을 하지 않아 폭이 0 이고, 그대로 두면 가로 이동이 0칸으로만 나온다.
 */
const CANVAS_W = 1200;
const COL_STEP = (CANVAS_W + GRID_GAP) / 12;
const ROW_STEP = ROW_H + GRID_GAP;

beforeEach(() => {
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({
    x: 0, y: 0, top: 0, left: 0, right: CANVAS_W, bottom: 600,
    width: CANVAS_W, height: 600, toJSON: () => ({}),
  } as DOMRect);
});

const ITEMS: CanvasItem[] = [
  { id: 'cost', node: <div>비용 패널</div>, defaultBox: { x: 0, y: 0, w: 6, h: 3 }, label: '비용' },
  { id: 'tokens', node: <div>토큰 패널</div>, defaultBox: { x: 6, y: 0, w: 6, h: 3 }, label: '토큰' },
];

function mount(opts: { layout?: DashLayout | null; editable?: boolean; items?: CanvasItem[] } = {}) {
  const onLayoutChange = vi.fn();
  render(
    <CanvasGrid
      items={opts.items ?? ITEMS}
      layout={opts.layout ?? null}
      editable={opts.editable ?? true}
      onLayoutChange={onLayoutChange}
    />,
  );
  return onLayoutChange;
}

function cell(pid: string): HTMLElement {
  const el = document.querySelector<HTMLElement>(`.dc-cell[data-pid="${pid}"]`);
  if (!el) throw new Error(`패널 ${pid} 이 렌더되지 않았다`);
  return el;
}

/** 마지막 호출에서 이 패널이 받은 자리. */
function committed(fn: ReturnType<typeof vi.fn>, id: string) {
  const next = fn.mock.calls.at(-1)?.[0] as DashLayout;
  const b = next.find((p) => p.id === id);
  if (!b) throw new Error(`${id} 가 커밋된 레이아웃에 없다`);
  return b;
}

/** 마우스 한 판: 잡고 → 끌고 → 놓는다. */
function drag(el: HTMLElement, dx: number, dy: number, opts: { drop?: boolean } = {}) {
  fireEvent.pointerDown(el, { clientX: 200, clientY: 200, button: 0, pointerType: 'mouse', pointerId: 1 });
  fireEvent.pointerMove(window, { clientX: 200 + dx / 2, clientY: 200 + dy / 2, pointerId: 1 });
  fireEvent.pointerMove(window, { clientX: 200 + dx, clientY: 200 + dy, pointerId: 1 });
  if (opts.drop !== false) fireEvent.pointerUp(window, { clientX: 200 + dx, clientY: 200 + dy, pointerId: 1 });
}

describe('패널은 화면에서 사라지지 않는다', () => {
  it('저장된 레이아웃에 없는 새 패널이 보인다 (defaultBox 로 붙는다)', () => {
    mount({ layout: [{ id: 'cost', x: 0, y: 0, w: 12, h: 4 }] });
    expect(screen.getByText('비용 패널')).toBeInTheDocument();
    // 저장된 적 없는 패널 — 여기서 안 보이면 사람은 그것을 "데이터가 없다"로 읽는다.
    expect(screen.getByText('토큰 패널')).toBeInTheDocument();
  });

  it('저장된 레이아웃에 있는 사라진 패널 id 는 버려진다', () => {
    mount({ layout: [
      { id: 'cost', x: 0, y: 0, w: 6, h: 3 },
      { id: '옛날패널', x: 6, y: 0, w: 6, h: 3 },
    ] });
    expect(document.querySelectorAll('.dc-cell[data-pid]')).toHaveLength(2);
    expect(document.querySelector('[data-pid="옛날패널"]')).toBeNull();
  });

  it('x + w > 12 로 오는 값이 캔버스 밖으로 안 나간다', () => {
    mount({ layout: [{ id: 'cost', x: 10, y: 0, w: 8, h: 2 }] });
    // CSS Grid 열은 1부터 — 11열에서 2칸이면 12열에서 끝난다.
    expect(cell('cost').style.gridColumn).toBe('11 / span 2');
  });

  it('저장된 것이 없으면 defaultBox 자리에 그린다', () => {
    mount({ layout: null });
    expect(cell('cost').style.gridColumn).toBe('1 / span 6');
    expect(cell('tokens').style.gridColumn).toBe('7 / span 6');
    expect(cell('cost').style.gridRow).toBe('1 / span 3');
  });
});

describe('드래그 — 놓는 순간 1회, 정수 칸으로', () => {
  it('끄는 동안에는 onLayoutChange 를 부르지 않는다 (대신 ghost 로 보여 준다)', () => {
    const onLayoutChange = mount();
    drag(cell('cost'), COL_STEP * 2, 0, { drop: false });
    expect(onLayoutChange).not.toHaveBeenCalled();
    expect(document.querySelector('.dc-cell.ghost')).not.toBeNull();
    expect(cell('cost').className).toContain('dragging');
  });

  it('놓으면 정확히 1회, 정수 칸으로 온다', () => {
    const onLayoutChange = mount();
    drag(cell('cost'), COL_STEP * 2, ROW_STEP * 1);
    expect(onLayoutChange).toHaveBeenCalledTimes(1);
    const next = onLayoutChange.mock.calls[0]![0] as DashLayout;
    expect(next.every((b) => [b.x, b.y, b.w, b.h].every(Number.isInteger))).toBe(true);
    expect(committed(onLayoutChange, 'cost')).toMatchObject({ x: 2, w: 6, h: 3 });
    // 놓고 나면 미리보기는 사라진다.
    expect(document.querySelector('.dc-cell.ghost')).toBeNull();
  });

  it('제자리에 놓으면 저장하지 않는다 — 값이 같은데 PUT 을 쏘지 않게', () => {
    const onLayoutChange = mount();
    drag(cell('cost'), 6, 0); // 임계값은 넘지만 반 칸도 안 되는 거리
    expect(onLayoutChange).not.toHaveBeenCalled();
  });

  it('핸들을 끌면 자리는 그대로 폭·높이만 바뀐다', () => {
    const onLayoutChange = mount();
    const handle = cell('cost').querySelector<HTMLElement>('.dc-handle');
    expect(handle).not.toBeNull();
    drag(handle!, COL_STEP * 2, ROW_STEP * 1);
    expect(onLayoutChange).toHaveBeenCalledTimes(1);
    expect(committed(onLayoutChange, 'cost')).toMatchObject({ x: 0, y: 0, w: 8, h: 4 });
  });

  it('놓은 자리에 있던 패널이 비켜 준다 — 놓은 쪽이 이긴다', () => {
    const onLayoutChange = mount();
    drag(cell('cost'), COL_STEP * 6, 0); // cost 를 tokens 자리로
    expect(committed(onLayoutChange, 'cost')).toMatchObject({ x: 6, y: 0 });
    expect(committed(onLayoutChange, 'tokens').y).toBeGreaterThan(0);
  });
});

describe('읽기 전용이 기본 상태다 (editable=false)', () => {
  it('핸들도 드래그도 없고 편집 표시도 없다', () => {
    const onLayoutChange = mount({ editable: false });
    expect(document.querySelector('.dashcanvas')?.className).not.toContain('editing');
    expect(document.querySelector('.dc-handle')).toBeNull();
    expect(cell('cost').tabIndex).toBe(-1);   // 포커스 대상이 아니다
    drag(cell('cost'), COL_STEP * 2, 0);
    expect(onLayoutChange).not.toHaveBeenCalled();
  });

  it('키보드로도 움직이지 않는다', () => {
    const onLayoutChange = mount({ editable: false });
    fireEvent.keyDown(cell('cost'), { key: 'ArrowRight' });
    expect(onLayoutChange).not.toHaveBeenCalled();
  });
});

describe('마우스 없이도 된다 (키보드 · aria-live)', () => {
  it('화살표로 한 칸 옮긴다', () => {
    const onLayoutChange = mount();
    cell('cost').focus();
    expect(document.activeElement).toBe(cell('cost'));
    fireEvent.keyDown(cell('cost'), { key: 'ArrowRight' });
    expect(onLayoutChange).toHaveBeenCalledTimes(1);
    expect(committed(onLayoutChange, 'cost')).toMatchObject({ x: 1, w: 6 });
  });

  it('Shift + 화살표로 한 칸 리사이즈한다', () => {
    const onLayoutChange = mount();
    fireEvent.keyDown(cell('cost'), { key: 'ArrowDown', shiftKey: true });
    expect(committed(onLayoutChange, 'cost')).toMatchObject({ x: 0, y: 0, w: 6, h: 4 });
  });

  it('벽에 막히면 저장하지 않는다', () => {
    const onLayoutChange = mount();
    fireEvent.keyDown(cell('cost'), { key: 'ArrowLeft' }); // 이미 0열
    expect(onLayoutChange).not.toHaveBeenCalled();
  });

  it('바뀐 자리를 aria-live 로 읽어 준다', () => {
    mount();
    fireEvent.keyDown(cell('cost'), { key: 'ArrowRight' });
    expect(screen.getByRole('status')).toHaveTextContent('비용 · 2열 1행 · 6칸 폭 · 3행');
  });

  it('패널마다 지금 자리가 이름으로 붙는다 (포커스하면 들린다)', () => {
    mount({
      items: [ITEMS[1]!],
      layout: [{ id: 'tokens', x: 4, y: 2, w: 3, h: 2 }],
    });
    // y=2 는 위가 비어 있으므로 compact 로 첫 행까지 끌어올려진다 — 이름도 그 결과를 말해야 한다.
    expect(cell('tokens')).toHaveAttribute('aria-label', '토큰 · 5열 1행 · 3칸 폭 · 2행');
  });

  it('label 이 없으면 id 를 읽는다 — 그래도 침묵하지는 않는다', () => {
    mount({ items: [{ id: 'cost', node: <div>비용 패널</div>, defaultBox: { x: 0, y: 0, w: 6, h: 3 } }] });
    expect(cell('cost')).toHaveAttribute('aria-label', 'cost · 1열 1행 · 6칸 폭 · 3행');
  });
});
