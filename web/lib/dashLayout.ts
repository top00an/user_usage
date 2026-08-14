/*
 * 대시보드 자유 캔버스(12열)의 레이아웃 수학.
 *
 * ── 여기는 DOM 을 모른다 ──────────────────────────────────────────────────
 *
 * 배치 계산은 전부 이 파일의 순수 함수가 지고, CanvasGrid.tsx 는 포인터·DOM 만 다룬다.
 * 이렇게 갈라 두는 이유는 취향이 아니다 — 겹침 해소·경계 클램프·스냅은 눈으로 확인하기
 * 가장 비싼 종류의 버그(한 칸 어긋남, 패널이 안 보임)를 내는데, 순수 함수로 떼어 두면
 * 그 버그를 브라우저 없이 테스트가 잡는다(test/dashlayout.test.ts).
 *
 * ── 단위는 칸이다 ────────────────────────────────────────────────────────
 *
 * x·y·w·h 는 전부 **칸**이고 정수다. 픽셀은 드래그 중에만 존재하고, 커밋되는 값은 항상
 * pxToCells 를 지나 정수 칸이 된다. 서버(`/api/me/dashboard-layout`)도 이 값을 그대로
 * 저장하며 같은 상한을 검증한다 — 여기 상수를 바꾸면 서버 검증도 같이 바뀌어야 한다.
 */

export const GRID_COLS = 12;   // 열 수. 이 값이 x·w 의 상한을 정한다.
export const ROW_H = 56;       // 한 행 단위 높이(px)
export const GRID_GAP = 12;    // 셀 간격(px)

/** 서버가 거절하는 상한과 같은 값 — 클라이언트가 먼저 잘라야 400 을 만들지 않는다. */
export const MAX_PANELS = 200;
export const MAX_ROW = 10000;
export const MAX_H = 100;

/**
 * 이 폭 이하에서는 globals.css 가 캔버스를 한 열로 쌓는다(@media (max-width: 720px)).
 * 쌓인 상태에서 12칸 드래그를 허용하면 손가락이 간 곳과 패널이 가는 곳이 어긋나므로
 * CanvasGrid 는 이 값으로 포인터 드래그를 끈다. **CSS 와 같은 숫자여야 한다.**
 */
export const STACK_MAX_W = 720;

/** 한 행의 피치(행 높이 + 간격). 세로 스냅의 단위다. */
export const ROW_STEP = ROW_H + GRID_GAP;

/** 패널 한 장의 자리. 단위는 전부 **칸**이지 픽셀이 아니다. */
export interface PanelBox {
  id: string;   // 패널 안정 id
  x: number;    // 0..11
  y: number;    // 0.. (행 인덱스)
  w: number;    // 1..12, 단 x + w <= 12
  h: number;    // 1..100
}
export type DashLayout = PanelBox[];

/** id 없는 자리(기본 배치 선언용). */
export interface BoxSpec { x: number; y: number; w: number; h: number; }

/** resolveLayout 이 필요로 하는 최소 정보 — CanvasItem 이 이 모양을 만족한다. */
export interface LayoutItem { id: string; defaultBox: BoxSpec; }

function clampInt(v: number, min: number, max: number): number {
  if (!Number.isFinite(v)) return min;
  if (max < min) return min;
  return Math.min(max, Math.max(min, Math.round(v)));
}

/**
 * 캔버스 안으로 되돌린다. **x 를 진실로 보고 w 를 줄인다** — 오른쪽 끝에서 리사이즈한 것과
 * 같은 결과다. 반대로(x 를 왼쪽으로 당기기) 하면 저장된 값이 조금 넘쳤을 때 패널이 통째로
 * 옆으로 순간이동해 사용자가 "내 배치가 망가졌다"로 읽는다.
 */
export function clampBox(box: PanelBox): PanelBox {
  const x = clampInt(box.x, 0, GRID_COLS - 1);
  const w = clampInt(box.w, 1, GRID_COLS - x);
  const y = clampInt(box.y, 0, MAX_ROW);
  const h = clampInt(box.h, 1, MAX_H);
  return { id: box.id, x, y, w, h };
}

/** 이동 — 크기는 그대로 두고 자리만 옮긴다(벽에 닿으면 멈춘다). */
export function moveBox(box: PanelBox, dx: number, dy: number): PanelBox {
  const w = clampInt(box.w, 1, GRID_COLS);
  const h = clampInt(box.h, 1, MAX_H);
  return {
    id: box.id,
    x: clampInt(box.x + dx, 0, GRID_COLS - w),
    y: clampInt(box.y + dy, 0, MAX_ROW),
    w,
    h,
  };
}

/** 리사이즈 — 좌상단은 고정이고 폭·높이만 바뀐다(오른쪽 벽에서 폭이 멈춘다). */
export function resizeBox(box: PanelBox, dw: number, dh: number): PanelBox {
  const x = clampInt(box.x, 0, GRID_COLS - 1);
  return {
    id: box.id,
    x,
    y: clampInt(box.y, 0, MAX_ROW),
    w: clampInt(box.w + dw, 1, GRID_COLS - x),
    h: clampInt(box.h + dh, 1, MAX_H),
  };
}

function overlaps(a: PanelBox, b: PanelBox): boolean {
  return a.x < b.x + b.w && a.x + a.w > b.x && a.y < b.y + b.h && a.y + a.h > b.y;
}

/**
 * 서로 겹친 패널의 id — 겹침을 허용한 대가로 필요해진 함수다.
 *
 * 겹치면 위 카드가 아래를 **완전히 가린다**(카드는 불투명하다). 화면에는 아무 흔적도 남지 않아,
 * 실수로 겹친 사람은 그것을 "패널이 사라졌다 / 데이터가 없다"로 읽는다. 그래서 화면이 몇 장이
 * 가려져 있는지 말할 수 있어야 한다(툴바 안내 + "겹침·빈 줄 정리" 버튼으로 복구).
 */
export function overlappingIds(layout: DashLayout): string[] {
  const out: string[] = [];
  for (const a of layout) {
    if (layout.some((b) => b.id !== a.id && overlaps(a, b))) out.push(a.id);
  }
  return out;
}

/**
 * 이 칸을 놓을 수 있는 **가장 가까운 아래쪽 빈 자리**의 y. 안 겹치면 그 자리 그대로다.
 *
 * 새로 붙는 패널에만 쓴다(resolveLayout). 이미 자리를 가진 패널은 절대 이 함수를 지나지 않는다 —
 * 그러면 "내가 만지지 않은 패널이 움직인다"가 되살아난다.
 */
function firstFreeY(box: PanelBox, placed: readonly PanelBox[]): number {
  let y = box.y;
  for (;;) {
    const hit = placed.filter((p) => overlaps({ ...box, y }, p));
    if (hit.length === 0) return y;
    const next = Math.min(...hit.map((p) => p.y + p.h));
    if (next > MAX_ROW) return MAX_ROW;
    y = next;
  }
}

/**
 * 캔버스에 올릴 수 있는 값으로 다듬는다 — **자리는 손대지 않는다.**
 *
 * 하는 일은 둘뿐이다: 같은 id 는 첫 것만 남기고, 각 칸을 캔버스 안으로 클램프한다.
 * 겹침은 **허용한다.** 겹쳤다고 누군가를 밀어내지 않는다.
 *
 * 왜 아무것도 안 미는가. 이 함수는 두 번 자리를 옮기는 규칙을 가졌었고 둘 다 사고였다:
 *   ① 전부 맨 위로 끌어올리기(compact) — 아래 빈 곳에 놓으면 위로 날아갔다.
 *   ② 겹친 것만 아래로 밀기 — 한 장을 놓았을 뿐인데 이웃이 내려가고, 크기를 줄여도 그 이웃은
 *      돌아오지 않아 여러 번 만지는 동안 배치가 계속 흘러내렸다.
 * 둘의 공통점은 **내가 만지지 않은 패널이 움직인다**는 것이고, 사람은 그것을 하나같이
 * "혼자 배치된다"로 읽는다. 그래서 지금 규칙은 하나다 — *놓은 그 자리에 그대로 둔다.*
 *
 * 겹쳐서 가린 것을 정리하고 싶을 때 쓰는 도구는 compactLayout 이다(툴바 버튼).
 */
export function normalizeLayout(layout: DashLayout): DashLayout {
  const out: PanelBox[] = [];
  const seen = new Set<string>();
  for (const b of layout) {
    if (typeof b?.id !== 'string' || seen.has(b.id)) continue;
    seen.add(b.id);
    out.push(clampBox(b));
  }
  return out;
}

/**
 * 겹침을 풀고 빈 줄을 걷어내 위로 당긴다(compact).
 *
 * **사람이 버튼을 눌렀을 때만 돈다.** 예전에는 normalizeLayout 이 이 일을 배치가 바뀔 때마다
 * 자동으로 했고, 그것이 "패널이 혼자 배치된다"의 정체였다. 같은 계산이라도 내가 시켜서 도는
 * 것과 저절로 도는 것은 다른 기능이다 — 전자는 도구고 후자는 사고다.
 *
 * 자유 겹침을 허용한 뒤로 이 함수의 쓸모가 하나 늘었다: 겹쳐서 **가려진 패널을 다시 드러내는**
 * 유일한 한 번의 조작이다(겹친 것은 아래 빈 자리로 내려간다). 그래서 툴바 이름도 "겹침·빈 줄 정리"다.
 *
 * 결과는 다른 변경과 똑같이 되돌릴 수 있어야 한다(호출부가 히스토리에 쌓는다). 한 번의 클릭이
 * 배치 전체를 바꾸는데 되돌릴 수 없으면, 그건 눌러 보기 무서운 버튼이다.
 */
export function compactLayout(layout: DashLayout): DashLayout {
  const order = layout
    .map((b, i) => ({ b: clampBox(b), i }))
    .sort((p, q) => p.b.y - q.b.y || p.b.x - q.b.x || p.i - q.i);

  const placed: PanelBox[] = [];
  for (const { b } of order) {
    let y = 0;   // 여기가 normalizeLayout 과 갈리는 유일한 지점이다 — 무조건 맨 위부터 찾는다.
    for (;;) {
      const probe = { ...b, y };
      const hit = placed.filter((p) => overlaps(probe, p));
      if (hit.length === 0) break;
      y = Math.min(...hit.map((p) => p.y + p.h));
      if (y > MAX_ROW) { y = MAX_ROW; break; }
    }
    placed.push({ ...b, y });
  }

  const byId = new Map(placed.map((p) => [p.id, p]));
  return layout.map((b) => byId.get(b.id)).filter((b): b is PanelBox => b !== undefined);
}

/**
 * 저장된 레이아웃 + 지금 코드가 가진 패널 → 실제 배치.
 *
 * - `saved` 에만 있는 id 는 **버린다**(패널이 코드에서 사라진 경우).
 * - `items` 에만 있는 id 는 `defaultBox` 자리에 붙이되, 그 자리가 이미 찬 경우 **바로 아래 빈
 *   자리**로 내려 붙인다. 겹침을 허용한 뒤로 이게 필요해졌다: 새 그래프가 남의 카드 밑에 정확히
 *   숨으면 사람은 "추가가 안 됐다"로 읽고 빌더를 다시 누른다(그리고 유령 패널이 쌓인다).
 *   **이미 자리를 가진 패널은 이 규칙을 지나지 않는다** — 새로 붙는 것만 비켜 앉는다.
 *
 * ── 배열 순서 = 그리는 순서(겹칠 때 앞뒤) ────────────────────────────────
 *
 * 반환 순서는 `saved` 의 순서를 따르고, 저장에 없던 패널은 뒤에 붙는다. 뒤에 있는 것이 위에
 * 그려진다(CanvasGrid 가 이 순서로 렌더한다). 그래서 "방금 올린 패널이 위" 라는 사실이 **서버에
 * 저장되는 배열 순서 그 자체**로 남는다 — z 필드를 새로 만들지 않았고, 서버 검증도 그대로다.
 */
export function resolveLayout(
  saved: DashLayout | null,
  items: readonly LayoutItem[],
): DashLayout {
  const wanted = new Map<string, LayoutItem>();
  for (const it of items) {
    if (it?.id && !wanted.has(it.id)) wanted.set(it.id, it);
  }

  const out: DashLayout = [];
  const done = new Set<string>();

  // ① 저장된 순서대로 — 이 순서가 겹칠 때의 앞뒤다.
  for (const b of saved ?? []) {
    if (typeof b?.id !== 'string' || !wanted.has(b.id) || done.has(b.id)) continue;
    done.add(b.id);
    out.push(clampBox({ ...b, id: b.id }));
  }

  // ② 저장에 없던 패널(코드에 새로 들어온 것) — 빈 자리를 찾아 뒤에 붙인다.
  for (const it of items) {
    if (!it?.id || done.has(it.id)) continue;
    done.add(it.id);
    const box = clampBox({ id: it.id, ...it.defaultBox });
    out.push({ ...box, y: clampInt(firstFreeY(box, out), 0, MAX_ROW) });
  }
  return out;
}

function parseBox(v: unknown): PanelBox | null {
  if (typeof v !== 'object' || v === null) return null;
  const r = v as Record<string, unknown>;
  const id = typeof r.id === 'string' ? r.id.trim() : '';
  if (id === '') return null;
  const cell = (n: unknown): number | null => (typeof n === 'number' && Number.isFinite(n) ? n : null);
  const x = cell(r.x), y = cell(r.y), w = cell(r.w), h = cell(r.h);
  if (x === null || y === null || w === null || h === null) return null;
  // 소수는 **버리지 않고** 가장 가까운 칸으로 붙인다 — 반올림하면 자리가 한 칸 어긋날 뿐이지만
  // 버리면 패널이 사라진다. 둘 중 덜 나쁜 쪽을 고른다.
  return clampBox({ id, x, y, w, h });
}

/**
 * 서버·localStorage 가 준 값을 **믿지 않는** 파서.
 *
 * 항목 하나가 깨졌다고 레이아웃 전체를 버리면 사용자는 배치를 통째로 잃는다. 그래서 깨진
 * 항목만 버리고 나머지는 살린다. 반대로 배열이 아예 아니면(응답 shape 가 다르거나 null)
 * "저장된 적 없음"(null)으로 보아 기본 배치로 되살아난다.
 */
export function parseLayout(raw: unknown): DashLayout | null {
  if (!Array.isArray(raw)) return null;
  const out: DashLayout = [];
  const seen = new Set<string>();
  for (const v of raw) {
    if (out.length >= MAX_PANELS) break;   // 서버 상한과 같다 — 넘치는 응답이 렌더를 죽이지 않게.
    const b = parseBox(v);
    if (!b || seen.has(b.id)) continue;
    seen.add(b.id);
    out.push(b);
  }
  return out;
}

/**
 * 배치가 같은가. 같으면 저장하지 않는다 — 불필요한 PUT 을 막는 자리다.
 *
 * **순서도 본다.** 예전에는 순서를 무시했다(같은 자리면 같다). 겹침을 허용한 뒤로 배열 순서가
 * 곧 앞뒤(그리는 순서)라는 뜻을 갖게 되었으므로, 순서만 바뀐 변경을 "같다"고 접으면 사용자가
 * 위로 올린 패널이 저장되지 않고 다음 방문에 다시 뒤로 간다.
 */
export function sameLayout(a: DashLayout | null, b: DashLayout | null): boolean {
  if (a === b) return true;
  if (!a || !b || a.length !== b.length) return false;
  return a.every((p, i) => {
    const q = b[i];
    return !!q && q.id === p.id && q.x === p.x && q.y === p.y && q.w === p.w && q.h === p.h;
  });
}

/**
 * 한 칸의 가로 피치(칸 폭 + 간격). 12칸 + 11간격 = 컨테이너 폭 이므로 (폭 + 간격) / 12 다.
 * 폭을 못 재면 0 — 호출자는 이때 가로 이동을 0칸으로 본다(0 으로 나눠 Infinity 칸을 만들지 않는다).
 */
export function colStep(containerW: number): number {
  if (!Number.isFinite(containerW) || containerW <= 0) return 0;
  return (containerW + GRID_GAP) / GRID_COLS;
}

/** 드래그한 픽셀 → 옮겨진 칸 수(반올림 = 절반을 넘으면 다음 칸으로 스냅). */
export function pxToCells(dxPx: number, dyPx: number, containerW: number): { dx: number; dy: number } {
  const step = colStep(containerW);
  const dx = step > 0 && Number.isFinite(dxPx) ? Math.round(dxPx / step) : 0;
  const dy = Number.isFinite(dyPx) ? Math.round(dyPx / ROW_STEP) : 0;
  return { dx, dy };
}

/**
 * aria-live 로 읽어 줄 현재 자리. 열·행은 **사람 단위(1부터)** 로 센다 — 0열이라고 읽어 주면
 * 화면을 못 보는 사람이 그것을 "맨 왼쪽 다음 칸"으로 오해한다.
 */
export function describeBox(label: string, box: PanelBox): string {
  return `${label} · ${box.x + 1}열 ${box.y + 1}행 · ${box.w}칸 폭 · ${box.h}행`;
}
