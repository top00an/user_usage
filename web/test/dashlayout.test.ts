import { describe, it, expect } from 'vitest';
import {
  GRID_COLS, ROW_H, GRID_GAP, MAX_PANELS,
  type DashLayout,
  clampBox, moveBox, resizeBox, normalizeLayout, resolveLayout, parseLayout,
  sameLayout, colStep, pxToCells, describeBox,
} from '@/lib/dashLayout';

/*
 * ── 자유 캔버스의 레이아웃 수학 ────────────────────────────────────────────
 *
 * 이 파일이 지키는 것은 **패널이 화면에서 사라지지 않는다**는 성질이다. 배치 계산은 눈에
 * 안 보이는 순수 함수인데, 여기서 한 칸 틀리면 화면에는 "데이터가 없다"로 보인다 —
 * 코드에 새로 넣은 패널이 저장된 레이아웃에 없어서 안 그려지는 것이 그 사고의 전형이다.
 * 그래서 경계·깨진 입력·id 불일치를 전부 여기서 못 박는다.
 */

/** 자리 비교를 사람이 읽을 수 있게 — id 와 x/y/w/h 만 본다. */
const at = (l: DashLayout, id: string) => {
  const b = l.find((p) => p.id === id);
  if (!b) throw new Error(`${id} 가 레이아웃에 없다`);
  return { x: b.x, y: b.y, w: b.w, h: b.h };
};

describe('좌표계 상수 (CONTRACT §1)', () => {
  it('12열 · 56px 행 · 12px 간격이다 — 서버·CSS 가 이 값을 그대로 쓴다', () => {
    expect(GRID_COLS).toBe(12);
    expect(ROW_H).toBe(56);
    expect(GRID_GAP).toBe(12);
  });
});

describe('clampBox — 캔버스 밖으로 나가지 않는다', () => {
  it('x + w > 12 로 오는 값이 캔버스 밖으로 안 나간다', () => {
    const b = clampBox({ id: 'a', x: 10, y: 0, w: 8, h: 2 });
    expect(b.x + b.w).toBeLessThanOrEqual(GRID_COLS);
    expect(b.x).toBe(10); // 자리는 지키고 폭을 줄인다(오른쪽 끝에서 리사이즈한 것과 같은 결과)
    expect(b.w).toBe(2);
  });

  it('음수·0·소수를 정수 칸으로 되돌린다', () => {
    expect(clampBox({ id: 'a', x: -4, y: -9, w: 0, h: 0 })).toEqual({ id: 'a', x: 0, y: 0, w: 1, h: 1 });
    expect(clampBox({ id: 'a', x: 2.6, y: 1.2, w: 3.4, h: 2.5 })).toEqual({ id: 'a', x: 3, y: 1, w: 3, h: 3 });
  });

  it('x 가 마지막 열을 넘으면 마지막 열로 붙인다', () => {
    expect(clampBox({ id: 'a', x: 99, y: 0, w: 4, h: 1 })).toEqual({ id: 'a', x: 11, y: 0, w: 1, h: 1 });
  });
});

describe('moveBox / resizeBox — 칸 단위로만 움직인다', () => {
  const base = { id: 'a', x: 4, y: 2, w: 3, h: 2 };

  it('이동은 폭을 바꾸지 않는다 — 오른쪽 벽에 닿으면 멈춘다', () => {
    expect(moveBox(base, 1, -1)).toEqual({ id: 'a', x: 5, y: 1, w: 3, h: 2 });
    expect(moveBox(base, 99, 0)).toEqual({ id: 'a', x: 9, y: 2, w: 3, h: 2 }); // 9 + 3 = 12
    expect(moveBox(base, 0, -99)).toEqual({ id: 'a', x: 4, y: 0, w: 3, h: 2 });
  });

  it('리사이즈는 자리를 바꾸지 않는다 — 오른쪽 벽에서 폭이 멈춘다', () => {
    expect(resizeBox(base, 1, 1)).toEqual({ id: 'a', x: 4, y: 2, w: 4, h: 3 });
    expect(resizeBox(base, 99, 0)).toEqual({ id: 'a', x: 4, y: 2, w: 8, h: 2 }); // 4 + 8 = 12
    expect(resizeBox(base, -99, -99)).toEqual({ id: 'a', x: 4, y: 2, w: 1, h: 1 });
  });
});

describe('normalizeLayout — 겹침 해소와 위로 당기기', () => {
  it('같은 자리에 겹친 둘 중 하나가 아래로 밀린다', () => {
    const out = normalizeLayout([
      { id: 'a', x: 0, y: 0, w: 6, h: 2 },
      { id: 'b', x: 0, y: 0, w: 6, h: 2 },
    ]);
    expect(at(out, 'a')).toMatchObject({ x: 0, y: 0 });
    expect(at(out, 'b')).toMatchObject({ x: 0, y: 2 });
  });

  it('옆으로 나란한 패널은 서로 밀지 않는다', () => {
    const out = normalizeLayout([
      { id: 'a', x: 0, y: 0, w: 6, h: 2 },
      { id: 'b', x: 6, y: 0, w: 6, h: 2 },
    ]);
    expect(at(out, 'b')).toMatchObject({ x: 6, y: 0 });
  });

  it('위가 비면 끌어올린다(compact) — 빈 행이 남지 않는다', () => {
    const out = normalizeLayout([{ id: 'a', x: 0, y: 40, w: 4, h: 2 }]);
    expect(at(out, 'a').y).toBe(0);
  });

  it('priorityId 는 자기 자리를 지키고 나머지가 비켜 준다', () => {
    // b 를 a 위(같은 자리)로 끌어다 놓은 순간 — 놓은 쪽이 이긴다.
    const out = normalizeLayout([
      { id: 'a', x: 0, y: 0, w: 6, h: 2 },
      { id: 'b', x: 0, y: 0, w: 6, h: 2 },
    ], 'b');
    expect(at(out, 'b')).toMatchObject({ x: 0, y: 0 });
    expect(at(out, 'a')).toMatchObject({ x: 0, y: 2 });
  });

  it('입력 순서를 그대로 돌려준다 — 저장·비교가 순서로 흔들리지 않는다', () => {
    const out = normalizeLayout([
      { id: 'a', x: 0, y: 5, w: 4, h: 1 },
      { id: 'b', x: 0, y: 0, w: 4, h: 1 },
    ]);
    expect(out.map((b) => b.id)).toEqual(['a', 'b']);
  });

  it('중복 id 는 첫 것만 남는다 — 같은 패널이 두 번 그려지지 않는다', () => {
    const out = normalizeLayout([
      { id: 'a', x: 0, y: 0, w: 4, h: 1 },
      { id: 'a', x: 8, y: 3, w: 4, h: 1 },
    ]);
    expect(out).toHaveLength(1);
    expect(at(out, 'a')).toMatchObject({ x: 0, y: 0 });
  });
});

describe('resolveLayout — 저장된 레이아웃 + 지금 코드의 패널', () => {
  const items = [
    { id: 'cost', defaultBox: { x: 0, y: 0, w: 6, h: 3 } },
    { id: 'tokens', defaultBox: { x: 6, y: 0, w: 6, h: 3 } },
  ];

  it('저장된 레이아웃에 없는 새 패널이 보인다 (defaultBox 로 붙는다)', () => {
    const out = resolveLayout([{ id: 'cost', x: 0, y: 0, w: 12, h: 4 }], items);
    expect(out.map((b) => b.id).sort()).toEqual(['cost', 'tokens']);
    expect(at(out, 'tokens').w).toBe(6); // defaultBox 의 크기 그대로
  });

  it('저장된 레이아웃에 있는 사라진 패널 id 는 버려진다', () => {
    const out = resolveLayout(
      [{ id: 'cost', x: 0, y: 0, w: 6, h: 3 }, { id: '옛날패널', x: 6, y: 0, w: 6, h: 3 }],
      items,
    );
    expect(out.map((b) => b.id)).not.toContain('옛날패널');
    expect(out).toHaveLength(2);
  });

  it('저장된 것이 없으면(null) 전부 defaultBox 자리다', () => {
    const out = resolveLayout(null, items);
    expect(at(out, 'cost')).toEqual({ x: 0, y: 0, w: 6, h: 3 });
    expect(at(out, 'tokens')).toEqual({ x: 6, y: 0, w: 6, h: 3 });
  });

  it('저장된 값이 캔버스를 넘어와도 안으로 들어온다', () => {
    const out = resolveLayout([{ id: 'cost', x: 9, y: 0, w: 9, h: 3 }], items);
    const b = at(out, 'cost');
    expect(b.x + b.w).toBeLessThanOrEqual(GRID_COLS);
  });

  it('items 가 비면 빈 레이아웃이다', () => {
    expect(resolveLayout([{ id: 'cost', x: 0, y: 0, w: 6, h: 3 }], [])).toEqual([]);
  });
});

describe('parseLayout — 서버가 준 값을 믿지 않는다', () => {
  it('깨진 항목 하나가 나머지 레이아웃을 죽이지 않는다', () => {
    const out = parseLayout([
      { id: 'a', x: 0, y: 0, w: 6, h: 2 },
      { id: 'b', x: '3', y: 0, w: 6, h: 2 },   // 문자열 좌표 — 모양이 틀렸다
      null,
      { x: 0, y: 0, w: 6, h: 2 },              // id 가 없다
      { id: '   ', x: 0, y: 0, w: 6, h: 2 },   // 공백 id
      { id: 'c', x: 0, y: 4, w: 6, h: 2 },
    ]);
    expect(out?.map((b) => b.id)).toEqual(['a', 'c']);
  });

  it('배열이 아니면 "저장된 것 없음"(null)으로 본다 — 기본 배치로 살아난다', () => {
    expect(parseLayout(null)).toBeNull();
    expect(parseLayout(undefined)).toBeNull();
    expect(parseLayout({ layout: [] })).toBeNull();
    expect(parseLayout('[]')).toBeNull();
  });

  it('빈 배열은 null 이 아니다 — "저장했고 비어 있다"는 다른 사실이다', () => {
    expect(parseLayout([])).toEqual([]);
  });

  it('중복 id 는 첫 것만 살린다', () => {
    const out = parseLayout([
      { id: 'a', x: 0, y: 0, w: 6, h: 2 },
      { id: 'a', x: 6, y: 0, w: 6, h: 2 },
    ]);
    expect(out).toHaveLength(1);
  });

  it('NaN·Infinity 는 버린다', () => {
    expect(parseLayout([{ id: 'a', x: NaN, y: 0, w: 6, h: 2 }])).toEqual([]);
    expect(parseLayout([{ id: 'a', x: 0, y: Infinity, w: 6, h: 2 }])).toEqual([]);
  });

  it('소수는 버리지 않고 가장 가까운 칸으로 붙인다 — 패널을 잃는 것보다 낫다', () => {
    expect(parseLayout([{ id: 'a', x: 2.4, y: 0.6, w: 5.5, h: 2 }])?.[0]).toEqual({ id: 'a', x: 2, y: 1, w: 6, h: 2 });
  });

  it('패널 수가 서버 상한을 넘으면 잘라 낸다', () => {
    const many = Array.from({ length: MAX_PANELS + 30 }, (_, i) => ({ id: `p${i}`, x: 0, y: i, w: 1, h: 1 }));
    expect(parseLayout(many)).toHaveLength(MAX_PANELS);
  });
});

describe('sameLayout — 값이 같으면 저장하지 않는다', () => {
  it('순서가 달라도 자리가 같으면 같다고 본다', () => {
    const a: DashLayout = [{ id: 'a', x: 0, y: 0, w: 6, h: 2 }, { id: 'b', x: 6, y: 0, w: 6, h: 2 }];
    const b: DashLayout = [{ id: 'b', x: 6, y: 0, w: 6, h: 2 }, { id: 'a', x: 0, y: 0, w: 6, h: 2 }];
    expect(sameLayout(a, b)).toBe(true);
    expect(sameLayout(a, [{ id: 'a', x: 1, y: 0, w: 6, h: 2 }, { id: 'b', x: 6, y: 0, w: 6, h: 2 }])).toBe(false);
    expect(sameLayout(a, a.slice(0, 1))).toBe(false);
  });
});

describe('픽셀 → 칸 스냅', () => {
  it('한 칸 피치는 (컨테이너 폭 + 간격) / 12 다', () => {
    // 12칸 + 11간격 = 폭 이므로 피치 = 칸폭 + 간격.
    expect(colStep(12 * 100 + 11 * GRID_GAP)).toBe(100 + GRID_GAP);
  });

  it('절반을 넘게 끌면 다음 칸으로 스냅한다', () => {
    const w = 12 * 100 + 11 * GRID_GAP; // 한 칸 피치 112px
    expect(pxToCells(60, 0, w).dx).toBe(1);
    expect(pxToCells(50, 0, w).dx).toBe(0);
    expect(pxToCells(-260, 0, w).dx).toBe(-2);
    expect(pxToCells(0, ROW_H + GRID_GAP + 4, w).dy).toBe(1);
  });

  it('폭을 못 재면(0) 가로로는 움직이지 않는다 — 엉뚱한 칸으로 튀지 않는다', () => {
    expect(pxToCells(500, 0, 0)).toEqual({ dx: 0, dy: 0 });
    expect(pxToCells(NaN, NaN, 1200)).toEqual({ dx: 0, dy: 0 });
  });
});

describe('describeBox — 마우스 없이 자리를 듣는다', () => {
  it('열·행·폭·높이를 사람 단위(1부터)로 읽어 준다', () => {
    expect(describeBox('비용 패널', { id: 'cost', x: 2, y: 1, w: 4, h: 2 }))
      .toBe('비용 패널 · 3열 2행 · 4칸 폭 · 2행');
  });
});
