'use client';

/*
 * 12열 자유 캔버스. 패널을 섹션 경계 없이 어디로든 끌어 옮기고 칸 단위로 크기를 바꾼다.
 * **겹쳐도 된다** — 겹쳤다고 아무도 밀려나지 않는다(lib/dashLayout.ts 의 normalizeLayout 머리말).
 *
 * ── 이 컴포넌트는 수학을 하지 않는다 ─────────────────────────────────────
 *
 * 경계 클램프·픽셀→칸 스냅은 전부 lib/dashLayout.ts 의 순수 함수가 진다. 여기 남는 것은 DOM 과
 * 포인터뿐이다. 그래서 "한 칸 어긋남" 류의 버그는 브라우저 없이 잡히고, 이 파일은 이벤트 배선만
 * 읽으면 된다.
 *
 * ── 자리의 주인은 부모다(controlled) ─────────────────────────────────────
 *
 * 배치는 `layout` prop + `items` 에서 **렌더 중 파생**한다. 여기에 사본 state 를 두면 저장된
 * 레이아웃이 늦게 도착했을 때 화면이 기본 배치를 한 프레임 그린 뒤 튀고, 사람은 그 튐을 데이터
 * 변화로 읽는다(옛 DragGrid 가 순서를 useState 로 들었다가 겪은 사고와 같다 — 그 파일은 이제
 * 없고, 교훈만 여기로 옮겨 왔다). 드래그 중의
 * 픽셀 오프셋만 임시 state 이고, 그것도 놓는 순간 사라진다.
 *
 * ── onLayoutChange 는 놓는 순간 1회 ──────────────────────────────────────
 *
 * 드래그 매 프레임마다 부르면 저장 훅이 프레임마다 PUT 을 쏜다. 그래서 이동 중에는 ghost 로만
 * 보여 주고, 커밋은 pointerup 한 번이다(값이 실제로 달라졌을 때만).
 *
 * ── 놓기 전 취소는 여기, 놓은 뒤 되돌리기는 저기 ──────────────────────────
 *
 * 끄는 도중의 Esc 는 이 파일이 받는다(그 판을 없던 일로 — 커밋이 아예 안 나간다). 이미 커밋된
 * 배치를 되돌리는 Ctrl+Z 는 **배치의 주인인 lib/layoutPrefs.ts** 가 진다. 여기에 히스토리를
 * 두면 위와 같은 이유로(사본 state) 툴바의 "기본 배치로 되돌리기" 같은 바깥 변경을 놓친다.
 */
import { useEffect, useRef, useState } from 'react';
import {
  GRID_COLS, ROW_H, GRID_GAP, STACK_MAX_W,
  type DashLayout, type PanelBox, type BoxSpec,
  resolveLayout, normalizeLayout, moveBox, resizeBox, pxToCells, sameLayout, describeBox,
} from '@/lib/dashLayout';

export interface CanvasItem {
  id: string;
  node: React.ReactNode;
  /** 저장된 레이아웃에 이 id 가 없을 때 쓸 기본 자리. */
  defaultBox: BoxSpec;
  /**
   * 접근성 안내(aria-label · aria-live)에 쓸 사람이 읽는 이름. 계약 §3 에 대한 **선택적** 추가라
   * 안 주면 id 를 읽는다 — 다만 "live-cost" 를 소리로 듣는 사람에게는 제목이 훨씬 낫다.
   */
  label?: string;
}

type Mode = 'move' | 'resize';

/** 드래그 한 판의 불변 정보 + 마지막 목표 자리. 렌더에 안 쓰이므로 ref 에 산다. */
interface Session {
  id: string;
  mode: Mode;
  pointerId: number;
  startX: number;
  startY: number;
  base: PanelBox;          // 잡은 순간의 자리(여기에 델타를 더한다 — 누적 오차가 없다)
  snapshot: DashLayout;    // 잡은 순간의 전체 배치
  containerW: number;      // 잡은 순간의 캔버스 폭(드래그 중에는 안 바뀐다)
  ghost: PanelBox;         // 지금 놓으면 갈 자리
  moved: boolean;          // 임계값을 넘어 실제 드래그가 됐는가
  target: HTMLElement | null;
}

/** 화면에 그려야 하는 드래그 상태만. */
interface Preview { id: string; mode: Mode; dxPx: number; dyPx: number; ghost: PanelBox; active: boolean }

/*
 * 이만큼 움직이기 전에는 드래그로 치지 않는다. 편집 모드에서도 패널 안의 버튼은 눌려야 하는데,
 * 손이 1~2px 흔들렸다고 클릭이 드래그로 먹히면 "버튼이 안 눌린다"가 된다.
 */
const DRAG_SLOP = 4;

const ARROW: Record<string, { x: number; y: number }> = {
  ArrowLeft: { x: -1, y: 0 },
  ArrowRight: { x: 1, y: 0 },
  ArrowUp: { x: 0, y: -1 },
  ArrowDown: { x: 0, y: 1 },
};

/** 패널 안의 조작 요소에서 시작한 포인터는 드래그가 아니라 그 요소의 것이다. */
function isInteractive(t: EventTarget | null): boolean {
  if (!(t instanceof Element)) return false;
  return t.closest('button, a, input, select, textarea, [role="button"], [role="tab"]') !== null;
}

/*
 * 크기가 바뀐 뒤 안의 차트에 새 폭을 알린다.
 *
 * EChart 는 자기 컨테이너를 ResizeObserver 로 보고 있어서 대개는 스스로 따라오지만, 캔버스가
 * 커밋되는 순간과 캔버스 내부의 다른 렌더(패널이 교체되는 경우)가 겹치면 옛 폭으로 그려진
 * 채 남는다. 옛 DragGrid 가 재배치 뒤에 window resize 를 쏘던 것과 **같은 이유·같은 방법**
 * 이다 — 관측되는 증상은 "패널만 넓어지고 그래프는 잘려 있다"이고, 눈으로만 잡히는 종류다.
 * 커밋 다음 프레임에 보내야 새 크기가 반영된 뒤 재측정된다.
 */
function bumpCharts(): void {
  requestAnimationFrame(() => window.dispatchEvent(new Event('resize')));
}

/** 자리·크기가 같은가. 같으면 커밋할 것이 없다(순서만 바꿔 PUT 을 쏘지 않기 위한 판정). */
function samePlace(a: PanelBox, b: PanelBox): boolean {
  return a.x === b.x && a.y === b.y && a.w === b.w && a.h === b.h;
}

function cellStyle(b: PanelBox): React.CSSProperties {
  // CSS Grid 의 열·행은 1부터 센다.
  return { gridColumn: `${b.x + 1} / span ${b.w}`, gridRow: `${b.y + 1} / span ${b.h}` };
}

export default function CanvasGrid({
  items, layout, editable, onLayoutChange,
}: {
  items: CanvasItem[];
  /** null = 저장된 것이 없다 → 전부 defaultBox 로 배치 */
  layout: DashLayout | null;
  /** 편집 모드에서만 드래그·리사이즈 핸들이 붙는다 */
  editable: boolean;
  /** 칸으로 스냅된 결과만 올라온다. 드래그 중에는 부르지 않는다(커밋 시 1회). */
  onLayoutChange: (next: DashLayout) => void;
}): React.JSX.Element {
  const rootRef = useRef<HTMLDivElement | null>(null);
  const sessionRef = useRef<Session | null>(null);
  const [preview, setPreview] = useState<Preview | null>(null);
  const [announce, setAnnounce] = useState('');
  /*
   * 지금 겨누고 있는 패널. 겹침을 허용하면 "누가 위에 보이나"가 곧 조작 가능성이므로, 잡거나
   * 포커스한 패널은 **커밋 전에도** 즉시 위로 올라와야 한다(안 그러면 남의 카드 밑에서 끌게 된다).
   * 커밋된 뒤의 앞뒤는 이 state 가 아니라 배치 **배열 순서**가 진다(아래 commit 주석).
   */
  const [raised, setRaised] = useState<string | null>(null);

  /*
   * 배치는 렌더 중 파생 — 첫 프레임부터 옳다.
   * **이 배열의 순서가 그리는 순서다**(뒤에 있는 것이 위에 보인다). resolveLayout 이 저장된 배열
   * 순서를 그대로 물려주므로, 겹칠 때의 앞뒤가 서버에 저장된 채로 다음 방문까지 남는다.
   */
  const boxes = resolveLayout(layout, items);
  const byId = new Map(boxes.map((b) => [b.id, b]));
  const nodeById = new Map(items.map((it) => [it.id, it]));

  /*
   * window 리스너는 드래그가 시작될 때만 붙는다. 그 콜백이 최신 onLayoutChange 를 보게 ref 로
   * 넘긴다 — prop 을 deps 에 넣으면 부모가 매 렌더 새 함수를 주는 흔한 경우에 드래그 도중
   * 리스너가 떼였다 붙어 한 판이 끊긴다.
   */
  const commitRef = useRef(onLayoutChange);
  useEffect(() => { commitRef.current = onLayoutChange; });

  const dragging = preview !== null;
  useEffect(() => {
    if (!dragging) return;

    const onMove = (ev: PointerEvent) => {
      const s = sessionRef.current;
      if (!s) return;
      const dxPx = ev.clientX - s.startX;
      const dyPx = ev.clientY - s.startY;
      if (!s.moved && Math.hypot(dxPx, dyPx) < DRAG_SLOP) return;
      s.moved = true;
      const { dx, dy } = pxToCells(dxPx, dyPx, s.containerW);
      s.ghost = s.mode === 'move' ? moveBox(s.base, dx, dy) : resizeBox(s.base, dx, dy);
      setPreview({ id: s.id, mode: s.mode, dxPx, dyPx, ghost: s.ghost, active: true });
    };

    const finish = (commit: boolean) => {
      const s = sessionRef.current;
      sessionRef.current = null;
      setPreview(null);
      if (!s) return;
      const el = s.target;
      if (el && typeof el.releasePointerCapture === 'function'
        && typeof el.hasPointerCapture === 'function' && el.hasPointerCapture(s.pointerId)) {
        el.releasePointerCapture(s.pointerId);
      }
      if (!commit) return;
      /*
       * 겹쳐도 자리는 그대로 둔다 — 다른 패널은 이 커밋으로 한 칸도 움직이지 않는다.
       * 다만 **순서는 맨 뒤로** 보낸다: 배열 뒤 = 위에 그려짐. 방금 만진 패널이 남의 카드 밑으로
       * 들어가 버리면 다시 잡을 수도, 읽을 수도 없다. 이 순서가 그대로 저장되므로 다음 방문에도
       * 앞뒤가 유지된다(z 필드 없이).
       *
       * 끌지 않고 눌렀다 놓기만 했으면(클릭) **앞으로 가져오기**다 — 자리는 그대로, 순서만 맨 뒤로.
       * 겹침을 허용한 뒤 사람이 가려진 패널을 꺼내려 할 때 가장 먼저 하는 동작이 그것이다.
       * 이미 맨 위면 sameLayout 이 걸러 PUT 이 나가지 않는다.
       *
       * ⚠ **완전히** 덮인 패널은 클릭으로 못 꺼낸다(클릭이 위 카드에 맞는다). 그 경우의 길은
       *   탭 포커스 + Enter/Space(onKeyDown)와 툴바의 "겹침·빈 줄 정리"다.
       */
      const target = s.moved ? s.ghost : s.base;
      const next = normalizeLayout([...s.snapshot.filter((b) => b.id !== s.id), target]);
      if (sameLayout(next, s.snapshot)) return;   // 값도 순서도 그대로 — 저장할 일이 없다.
      commitRef.current(next);
      if (!samePlace(target, s.base)) bumpCharts();   // 크기가 바뀐 경우에만 차트에 알린다.
    };

    const onUp = () => finish(true);
    const onCancel = () => finish(false);
    /*
     * 끄는 도중의 Esc 는 **그 판을 없던 일로 한다**(놓기 전 취소). 손을 떼야만 끝낼 수 있으면
     * "잘못 잡았다"를 깨달은 사람이 할 수 있는 일은 원래 자리를 눈대중으로 되짚는 것뿐이고,
     * 그건 대개 또 어긋난다. commit=false 라 onLayoutChange 가 아예 나가지 않는다.
     */
    const onKey = (ev: KeyboardEvent) => {
      if (ev.key !== 'Escape') return;
      ev.preventDefault();
      ev.stopPropagation();   // 이 Esc 는 드래그의 것이다 — 뒤에 있는 모달·패널까지 닫지 않는다.
      finish(false);
      setAnnounce('이동을 취소했습니다 — 패널이 원래 자리로 돌아갔습니다');
    };
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
    window.addEventListener('pointercancel', onCancel);
    window.addEventListener('keydown', onKey, true);
    return () => {
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
      window.removeEventListener('pointercancel', onCancel);
      window.removeEventListener('keydown', onKey, true);
    };
  }, [dragging]);

  const startDrag = (e: React.PointerEvent<HTMLElement>, id: string, mode: Mode) => {
    if (!editable || sessionRef.current) return;
    if (e.pointerType === 'mouse' && e.button !== 0) return;
    if (mode === 'move' && isInteractive(e.target)) return;
    /*
     * 좁은 화면에서는 CSS 가 캔버스를 한 열로 쌓는다. 그때도 12칸 좌표로 끌면 손가락이 간 곳과
     * 패널이 가는 곳이 어긋나므로, 아예 드래그를 시작하지 않는다(키보드 경로는 살아 있다).
     */
    if (window.innerWidth <= STACK_MAX_W) return;
    const base = byId.get(id);
    const root = rootRef.current;
    if (!base || !root) return;

    const el = e.currentTarget;
    setRaised(id);
    sessionRef.current = {
      id, mode, pointerId: e.pointerId,
      startX: e.clientX, startY: e.clientY,
      base, snapshot: boxes,
      containerW: root.getBoundingClientRect().width,
      ghost: base, moved: false, target: el,
    };
    setPreview({ id, mode, dxPx: 0, dyPx: 0, ghost: base, active: false });
    // 포인터가 패널 밖으로 나가도 이벤트를 계속 받는다(jsdom 에는 이 API 가 없다).
    if (typeof el.setPointerCapture === 'function') el.setPointerCapture(e.pointerId);
  };

  const labelOf = (it: CanvasItem) => it.label ?? it.id;

  /*
   * 키보드 경로. 드래그 전용 UI 는 키보드 사용자에게 **기능이 통째로 없는 것**과 같다.
   * ←/→/↑/↓ 한 칸 이동, Shift 를 더하면 한 칸 리사이즈, Enter·Space 는 앞으로 가져오기.
   * 매 키가 곧 커밋이다(드래그와 달리 중간 상태가 없다). 자리가 벽에 막혀 안 바뀌면 저장하지
   * 않고 읽어 주기만 한다.
   */
  const onKeyDown = (e: React.KeyboardEvent<HTMLElement>, it: CanvasItem) => {
    if (!editable) return;
    const cur = byId.get(it.id);
    if (!cur) return;

    /*
     * Enter · Space = **앞으로 가져오기**. 마우스의 클릭과 같은 값을 내는 경로다.
     *
     * 이 경로는 편의가 아니라 **유일한 길**인 경우가 있다: 완전히 덮인 패널은 클릭이 위 카드에
     * 맞으므로 포인터로는 영원히 꺼낼 수 없다. 탭으로 겨누면(onFocus 가 임시로 위로 올린다)
     * 눈으로 확인한 뒤 Enter 로 확정한다.
     */
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();   // Space 로 페이지가 스크롤되지 않게
      setRaised(it.id);
      const front = normalizeLayout([...boxes.filter((b) => b.id !== it.id), cur]);
      setAnnounce(`${labelOf(it)} · 맨 앞으로 가져왔습니다`);
      if (sameLayout(front, boxes)) return;   // 이미 맨 앞 — 저장할 일이 없다.
      onLayoutChange(front);
      return;
    }

    const dir = ARROW[e.key];
    if (!dir) return;
    e.preventDefault();   // 화살표로 페이지가 스크롤되지 않게
    setRaised(it.id);     // 키보드로 옮기는 패널도 위로 — 마우스와 같은 규칙이다.
    const moved = e.shiftKey ? resizeBox(cur, dir.x, dir.y) : moveBox(cur, dir.x, dir.y);
    /*
     * 드래그와 같은 규칙 — 만진 패널이 배열 맨 뒤(= 화면 맨 위)로 간다. 단 **벽에 막혀 자리가
     * 그대로면** 순서도 그대로다(그렇지 않으면 안 움직인 화살표가 저장을 부른다).
     */
    const next = samePlace(moved, cur)
      ? boxes
      : normalizeLayout([...boxes.filter((b) => b.id !== it.id), moved]);
    const applied = next.find((b) => b.id === it.id) ?? moved;
    setAnnounce(describeBox(labelOf(it), applied));
    if (sameLayout(next, boxes)) return;
    onLayoutChange(next);
    bumpCharts();
  };

  return (
    <div
      ref={rootRef}
      className={`dashcanvas${editable ? ' editing' : ''}`}
      /* 칸 크기의 단일 출처는 lib/dashLayout.ts 다 — CSS 는 이 변수를 읽는다(두 벌이 되지 않게). */
      style={{
        '--dc-cols': GRID_COLS,
        '--dc-row-h': `${ROW_H}px`,
        '--dc-gap': `${GRID_GAP}px`,
      } as React.CSSProperties}
    >
      {/*
        * **items 순서가 아니라 boxes 순서로 그린다.** 겹칠 때 뒤에 그려진 것이 위에 보이므로,
        * 이 순서가 곧 앞뒤이고 그 순서는 서버에 저장된 배열 순서다(resolveLayout 머리말).
        * key 가 id 라서 순서가 바뀌어도 React 는 노드를 새로 만들지 않고 옮긴다 — 차트가 다시
        * 마운트되지 않는다(그건 "가끔 깜빡인다"로 보인다).
        */}
      {boxes.map((box) => {
        const it = nodeById.get(box.id);
        if (!it) return null;   // 있을 수 없다(boxes 는 items 에서 나왔다) — 방어적으로만.
        const isDragging = preview?.id === it.id && preview.active;
        const style = cellStyle(box);
        // 겹쳤을 때 방금 만진 패널이 위에 보인다(끄는 중은 CSS 의 .dragging 이 더 위로 올린다).
        if (raised === it.id) style.zIndex = 2;
        if (isDragging && preview && preview.mode === 'move') {
          // 이동은 손을 따라오게 보여 준다(칸 스냅된 목표는 ghost 가 말한다).
          // 리사이즈는 늘려 보여 주면 안의 내용이 찌그러지므로 ghost 만 움직인다.
          style.transform = `translate(${preview.dxPx}px, ${preview.dyPx}px)`;
        }
        return (
          <div
            key={it.id}
            data-pid={it.id}
            className={`dc-cell${isDragging ? ' dragging' : ''}`}
            style={style}
            role="group"
            aria-label={describeBox(labelOf(it), box)}
            tabIndex={editable ? 0 : undefined}
            onPointerDown={editable ? (e) => startDrag(e, it.id, 'move') : undefined}
            onKeyDown={editable ? (e) => onKeyDown(e, it) : undefined}
            /* 탭으로 겨눈 패널도 즉시 위로 — 가려진 패널을 눈으로 확인하며 옮길 수 있어야 한다. */
            onFocus={editable ? () => setRaised(it.id) : undefined}
          >
            {it.node}
            {editable && (
              /*
               * 핸들은 포인터 전용이다. 키보드에는 Shift+화살표라는 같은 값을 내는 경로가 이미
               * 있으므로 탭 순서에 빈 정거장을 하나 더 만들지 않는다(그래서 aria-hidden).
               */
              <span
                className="dc-handle"
                aria-hidden="true"
                onPointerDown={(e) => { e.stopPropagation(); startDrag(e, it.id, 'resize'); }}
              />
            )}
          </div>
        );
      })}

      {/*
        * 놓일 자리. **줄일 때도 보여야 한다** — ghost 가 패널보다 아래에 깔려 있으면 키울 때는
        * 삐져나온 부분이 보이지만 줄일 때는 패널에 완전히 가려, 얼마나 줄어드는지 알 방법이
        * 없었다(그 상태로는 "커질 때만 보인다"가 정확한 증상이다). 그래서 ghost 가 제일 위다.
        *
        * 숫자도 함께 말한다. 점선 사각형만으로는 "몇 칸인지"를 눈으로 세야 하는데, 리사이즈는
        * 대개 "6칸으로 맞추고 싶다"는 조작이다.
        */}
      {preview?.active && (
        <div className={`dc-cell ghost ${preview.mode}`} style={cellStyle(preview.ghost)} aria-hidden="true">
          <span className="dc-ghost-size">
            {preview.mode === 'resize'
              ? `${preview.ghost.w}칸 × ${preview.ghost.h}행`
              : `${preview.ghost.x + 1}열 ${preview.ghost.y + 1}행`}
          </span>
        </div>
      )}

      {/* 키보드로 옮긴 자리를 소리로 읽어 준다 — 화면을 못 보면 이것 말고 확인할 방법이 없다. */}
      <div className="sr-only" role="status" aria-live="polite">{announce}</div>
    </div>
  );
}
