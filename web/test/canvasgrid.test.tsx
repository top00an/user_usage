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

  /*
   * 제자리에 놓는 것은 이제 "앞으로 가져오기"다(겹침 허용의 짝). 자리는 한 칸도 안 바뀌고
   * 순서만 맨 뒤로 간다 — 그래서 **이미 맨 앞이면** 저장할 것이 없다.
   */
  it('제자리에 놓으면 자리는 그대로고 순서만 맨 앞으로 온다', () => {
    const onLayoutChange = mount();
    drag(cell('cost'), 6, 0); // 임계값은 넘지만 반 칸도 안 되는 거리
    const next = onLayoutChange.mock.calls.at(-1)![0] as DashLayout;
    expect(next.map((b) => b.id)).toEqual(['tokens', 'cost']);
    expect(next.find((b) => b.id === 'cost')).toMatchObject({ x: 0, y: 0, w: 6, h: 3 });
  });

  it('이미 맨 앞인 패널을 제자리에 놓으면 저장하지 않는다 — 값도 순서도 그대로다', () => {
    const onLayoutChange = mount();
    drag(cell('tokens'), 6, 0);   // tokens 는 배열 마지막(=맨 앞)이다
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

  /*
   * 크기를 **줄일 때도** 얼마나 줄어드는지 보여야 한다. ghost 가 패널 아래에 깔려 있던 판에서는
   * 키울 때만 보였다(삐져나온 부분만 보이므로) — 그게 "줄일 때는 안 나온다"의 정체였다.
   */
  it('줄이는 중에도 미리보기가 보이고, 목표 크기를 숫자로 말한다', () => {
    mount();
    const handle = cell('cost').querySelector<HTMLElement>('.dc-handle')!;
    drag(handle, -COL_STEP * 2, -ROW_STEP * 1, { drop: false });

    const ghost = document.querySelector<HTMLElement>('.dc-cell.ghost');
    expect(ghost).not.toBeNull();
    // 6칸 3행에서 2칸·1행 줄였다 → 4칸 2행. 그 값이 화면에 글자로 있어야 한다.
    expect(ghost!.textContent).toBe('4칸 × 2행');
    expect(ghost!.style.gridColumn).toBe('1 / span 4');
    expect(ghost!.style.gridRow).toBe('1 / span 2');
  });

  it('옮기는 중에는 목표 자리를 열·행으로 말한다', () => {
    mount();
    drag(cell('cost'), COL_STEP * 2, ROW_STEP * 3, { drop: false });
    expect(document.querySelector('.dc-cell.ghost')?.textContent).toBe('3열 4행');
  });

  it('남의 자리에 겹쳐 놓아도 그 패널은 안 움직인다 (겹침 허용)', () => {
    const onLayoutChange = mount();
    drag(cell('cost'), COL_STEP * 6, 0); // cost 를 tokens 자리로 통째로 겹친다
    expect(committed(onLayoutChange, 'cost')).toMatchObject({ x: 6, y: 0 });
    // 예전에는 tokens 가 아래로 밀렸다. 그 밀림이 "혼자 배치된다"의 마지막 잔재였다.
    expect(committed(onLayoutChange, 'tokens')).toMatchObject({ x: 6, y: 0, w: 6, h: 3 });
  });

  it('방금 만진 패널이 위로 온다 — 겹쳐도 다시 잡을 수 있다', () => {
    mount();
    drag(cell('cost'), COL_STEP * 6, 0);
    expect(cell('cost').style.zIndex).toBe('2');
    expect(cell('tokens').style.zIndex).toBe('');
  });

  /*
   * 앞뒤(그리는 순서)는 z-index 가 아니라 **배열 순서**로 저장된다. 그래서 커밋된 배치의 맨 뒤에
   * 방금 옮긴 패널이 있어야 하고, 화면도 그 순서로 그려져야 한다(뒤가 위).
   */
  it('커밋된 배치의 맨 뒤가 방금 옮긴 패널이다 — 앞뒤가 저장된다', () => {
    const onLayoutChange = mount();
    drag(cell('cost'), COL_STEP * 6, 0);
    const next = onLayoutChange.mock.calls.at(-1)![0] as DashLayout;
    expect(next.map((b) => b.id)).toEqual(['tokens', 'cost']);
  });

  it('화면은 배치 배열 순서로 그린다 — 뒤에 있는 것이 위에 보인다', () => {
    mount({ layout: [
      { id: 'tokens', x: 0, y: 0, w: 6, h: 3 },
      { id: 'cost', x: 0, y: 0, w: 6, h: 3 },   // 겹쳐 있고, 뒤에 있으므로 위
    ] });
    const order = Array.from(document.querySelectorAll('.dc-cell[data-pid]')).map((el) => el.getAttribute('data-pid'));
    expect(order).toEqual(['tokens', 'cost']);
  });

  it('벽에 막혀 자리가 안 바뀌면 순서도 안 바뀐다 (저장도 없다)', () => {
    const onLayoutChange = mount();
    fireEvent.keyDown(cell('cost'), { key: 'ArrowLeft' });   // 이미 0열
    expect(onLayoutChange).not.toHaveBeenCalled();
  });

  it('탭으로 겨눈 패널도 위로 온다 — 가려진 패널을 보면서 옮길 수 있게', () => {
    mount();
    fireEvent.focus(cell('tokens'));
    expect(cell('tokens').style.zIndex).toBe('2');
  });

  /*
   * 클릭(끌지 않은 포인터)과 Enter/Space 는 **같은 값**을 낸다 — 앞으로 가져오기.
   * 완전히 덮인 패널은 클릭이 위 카드에 맞으므로, 키보드 경로가 그때의 유일한 길이다.
   */
  it('클릭하면 앞으로 온다 — 끌지 않아도 순서가 맨 뒤(맨 위)로 간다', () => {
    const onLayoutChange = mount({ layout: [
      { id: 'cost', x: 0, y: 0, w: 6, h: 3 },
      { id: 'tokens', x: 0, y: 0, w: 6, h: 3 },   // 겹쳐 있고 tokens 가 위
    ] });
    fireEvent.pointerDown(cell('cost'), { clientX: 100, clientY: 100, button: 0, pointerType: 'mouse', pointerId: 1 });
    fireEvent.pointerUp(window, { clientX: 100, clientY: 100, pointerId: 1 });

    const next = onLayoutChange.mock.calls.at(-1)![0] as DashLayout;
    expect(next.map((b) => b.id)).toEqual(['tokens', 'cost']);
    expect(next.find((b) => b.id === 'cost')).toMatchObject({ x: 0, y: 0, w: 6, h: 3 });   // 자리는 그대로
  });

  it('패널 안의 버튼을 눌러도 순서는 바뀌지 않는다 — 그 클릭은 버튼의 것이다', () => {
    const onLayoutChange = mount({ items: [
      { id: 'cost', node: <button type="button">삭제</button>, defaultBox: { x: 0, y: 0, w: 6, h: 3 } },
      ITEMS[1]!,
    ] });
    fireEvent.pointerDown(screen.getByRole('button', { name: '삭제' }), { clientX: 100, clientY: 100, button: 0, pointerType: 'mouse', pointerId: 1 });
    fireEvent.pointerUp(window, { clientX: 100, clientY: 100, pointerId: 1 });
    expect(onLayoutChange).not.toHaveBeenCalled();
  });

  it('Enter 로도 앞으로 온다 (완전히 덮인 패널의 유일한 길)', () => {
    const onLayoutChange = mount();
    fireEvent.keyDown(cell('cost'), { key: 'Enter' });
    const next = onLayoutChange.mock.calls.at(-1)![0] as DashLayout;
    expect(next.map((b) => b.id)).toEqual(['tokens', 'cost']);
    expect(screen.getByRole('status')).toHaveTextContent('비용 · 맨 앞으로 가져왔습니다');
  });

  it('Space 도 같은 값이고, 이미 맨 앞이면 저장하지 않는다', () => {
    const onLayoutChange = mount();
    fireEvent.keyDown(cell('tokens'), { key: ' ' });   // tokens 는 이미 마지막
    expect(onLayoutChange).not.toHaveBeenCalled();
    expect(screen.getByRole('status')).toHaveTextContent('토큰 · 맨 앞으로 가져왔습니다');
  });

  it('읽기 전용에서는 클릭도 Enter 도 순서를 바꾸지 않는다', () => {
    const onLayoutChange = mount({ editable: false });
    fireEvent.pointerDown(cell('cost'), { clientX: 100, clientY: 100, button: 0, pointerType: 'mouse', pointerId: 1 });
    fireEvent.pointerUp(window, { clientX: 100, clientY: 100, pointerId: 1 });
    fireEvent.keyDown(cell('cost'), { key: 'Enter' });
    expect(onLayoutChange).not.toHaveBeenCalled();
  });

  /*
   * "혼자 자동으로 배치된다"의 정체가 여기였다 — 아래 빈 곳에 놓으면 위로 끌려 올라가고,
   * 겹치지도 않은 옆 패널까지 따라 움직였다. 그 둘을 이 두 테스트가 못 박는다.
   */
  it('아래 빈 곳에 놓으면 그 자리에 남는다 — 위로 끌려 올라가지 않는다', () => {
    const onLayoutChange = mount();
    drag(cell('cost'), 0, ROW_STEP * 5);
    expect(committed(onLayoutChange, 'cost')).toMatchObject({ x: 0, y: 5, w: 6, h: 3 });
  });

  it('겹치지 않으면 다른 패널은 움직이지 않는다', () => {
    const onLayoutChange = mount();
    drag(cell('cost'), 0, ROW_STEP * 5);
    // tokens 는 손대지 않았다 — 옆 패널이 따라 움직이면 사람은 그것을 "혼자 재배치됐다"로 읽는다.
    expect(committed(onLayoutChange, 'tokens')).toMatchObject({ x: 6, y: 0, w: 6, h: 3 });
  });

  it('놓기 전 Esc 는 그 판을 없던 일로 한다 — 저장이 나가지 않는다', () => {
    const onLayoutChange = mount();
    drag(cell('cost'), COL_STEP * 3, ROW_STEP * 2, { drop: false });
    expect(document.querySelector('.dc-cell.ghost')).not.toBeNull();
    fireEvent.keyDown(window, { key: 'Escape' });
    expect(onLayoutChange).not.toHaveBeenCalled();
    expect(document.querySelector('.dc-cell.ghost')).toBeNull();
    expect(cell('cost').className).not.toContain('dragging');
    // 이어서 손을 떼도 취소된 판이 되살아나지 않는다.
    fireEvent.pointerUp(window, { pointerId: 1 });
    expect(onLayoutChange).not.toHaveBeenCalled();
    expect(screen.getByRole('status')).toHaveTextContent('이동을 취소했습니다');
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
    // 위가 비어 있어도 저장된 y=2(사람 단위 3행)에 그대로 있다 — 이름도 그 사실을 말해야 한다.
    expect(cell('tokens')).toHaveAttribute('aria-label', '토큰 · 5열 3행 · 3칸 폭 · 2행');
  });

  it('label 이 없으면 id 를 읽는다 — 그래도 침묵하지는 않는다', () => {
    mount({ items: [{ id: 'cost', node: <div>비용 패널</div>, defaultBox: { x: 0, y: 0, w: 6, h: 3 } }] });
    expect(cell('cost')).toHaveAttribute('aria-label', 'cost · 1열 1행 · 6칸 폭 · 3행');
  });
});
