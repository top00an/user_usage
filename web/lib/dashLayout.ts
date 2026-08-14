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
 * 겹침 해소 + 위로 당기기(compact).
 *
 * 위(y 작은 쪽)부터 차례로 "가장 위의 빈 자리"에 놓는 한 번의 스캔이다. 겹침 해소와 compact 가
 * 따로 도는 두 단계가 아니라 같은 규칙의 결과라, 두 단계가 서로의 결과를 되돌리는 사고
 * (밀어냈더니 다시 끌어올려져 또 겹침)가 구조적으로 생기지 않는다.
 *
 * @param priorityId 드래그로 방금 놓인 패널. 이 패널이 **먼저** 자리를 잡고 나머지가 비켜 준다 —
 *   놓은 자리에 원래 있던 패널이 이기면 사람은 "안 놓아졌다"로 읽는다.
 */
export function normalizeLayout(layout: DashLayout, priorityId?: string): DashLayout {
  const uniq: PanelBox[] = [];
  const seen = new Set<string>();
  for (const b of layout) {
    if (typeof b?.id !== 'string' || seen.has(b.id)) continue;
    seen.add(b.id);
    uniq.push(clampBox(b));
  }

  const order = uniq
    .map((b, i) => ({ b, i }))
    .sort((p, q) => {
      const pr = (p.b.id === priorityId ? 0 : 1) - (q.b.id === priorityId ? 0 : 1);
      return pr || p.b.y - q.b.y || p.b.x - q.b.x || p.i - q.i;
    });

  const placed: PanelBox[] = [];
  for (const { b } of order) {
    let y = 0;
    for (;;) {
      const probe = { ...b, y };
      const hit = placed.filter((p) => overlaps(probe, p));
      if (hit.length === 0) break;
      // 한 행씩 세는 대신 부딪힌 것들의 **가장 이른 바닥**으로 건너뛴다. 바닥은 반드시 y 보다
      // 크므로(겹쳤다는 것이 그 뜻이다) 루프는 끝난다.
      y = Math.min(...hit.map((p) => p.y + p.h));
      if (y > MAX_ROW) { y = MAX_ROW; break; }
    }
    placed.push({ ...b, y });
  }

  // 입력 순서를 그대로 돌려준다 — 저장 값과 비교(sameLayout)나 테스트가 순서로 흔들리지 않는다.
  const byId = new Map(placed.map((p) => [p.id, p]));
  return uniq.map((b) => byId.get(b.id)).filter((b): b is PanelBox => b !== undefined);
}

/**
 * 저장된 레이아웃 + 지금 코드가 가진 패널 → 실제 배치.
 *
 * - `saved` 에만 있는 id 는 **버린다**(패널이 코드에서 사라진 경우).
 * - `items` 에만 있는 id 는 `defaultBox` 로 붙인다 — **여기가 이 기능의 가장 위험한 자리다.**
 *   새로 넣은 패널이 저장된 레이아웃에 없다고 안 그려지면, 사람은 그것을 "데이터가 없다"로
 *   읽고 수집기·API 를 뒤진다. 화면에서 사라지는 것보다는 자리가 어긋나는 편이 낫다.
 */
export function resolveLayout(
  saved: DashLayout | null,
  items: readonly LayoutItem[],
  priorityId?: string,
): DashLayout {
  const savedById = new Map<string, PanelBox>();
  for (const b of saved ?? []) {
    if (typeof b?.id === 'string' && b.id !== '' && !savedById.has(b.id)) savedById.set(b.id, b);
  }

  const merged: DashLayout = [];
  const seen = new Set<string>();
  for (const it of items) {
    if (!it?.id || seen.has(it.id)) continue;
    seen.add(it.id);
    const s = savedById.get(it.id);
    merged.push(clampBox(s ? { ...s, id: it.id } : { id: it.id, ...it.defaultBox }));
  }
  return normalizeLayout(merged, priorityId);
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

/** 자리가 같은가(순서는 무시). 같으면 저장하지 않는다 — 불필요한 PUT 을 막는 자리다. */
export function sameLayout(a: DashLayout | null, b: DashLayout | null): boolean {
  if (a === b) return true;
  if (!a || !b || a.length !== b.length) return false;
  const byId = new Map(b.map((p) => [p.id, p]));
  return a.every((p) => {
    const q = byId.get(p.id);
    return !!q && q.x === p.x && q.y === p.y && q.w === p.w && q.h === p.h;
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
