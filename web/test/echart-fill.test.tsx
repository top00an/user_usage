import { readFileSync } from 'node:fs';
import path from 'node:path';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { golden, mockFetch, platformRoutes, type RouteSpec } from './helpers';

/*
 * ── 패널을 세로로 늘리면 차트도 같이 커진다 ───────────────────────────────
 *
 * 자유 캔버스에서 패널은 칸 단위로 커진다. 그런데 `EChart` 가 `height` 를 **픽셀 고정**으로
 * 박아 두어 세로 리사이즈가 반쪽이었다 — 카드만 커지고 차트 아래에 빈 공간이 남았다
 * (실측 2026-08-14, 크로미움: `cost-rate` 칸 396→600px, 캔버스 180→180px, 차트 아래 여백 177→381px).
 *
 * ⚠ **이 파일은 그 버그를 잡지 못한다.** jsdom 은 레이아웃을 하지 않아 박스 높이가 전부 0 이고,
 *   실제로 그 결함은 vitest 311개를 **전부 초록으로 통과한 채** 존재했다. 높이가 진짜로 따라
 *   커지는지는 진짜 브라우저에서만 잴 수 있다(리사이즈 전/후 `canvas` 의 getBoundingClientRect).
 *
 * 그래서 여기서 지키는 것은 높이가 아니라 **고침이 서 있는 전제**다. 전제가 조용히 뒤집히면
 * 브라우저 실측 없이도 빨간불이 되어야 한다:
 *   ① EChart 는 높이를 `height`(고정)가 아니라 `min-height`(최소)로 준다.
 *   ② 남는 세로 공간을 먹겠다는 표시(`.fillv`)를 스스로 단다.
 *   ③ 값이 없는 자리(NoData)도 같은 규율을 탄다 — 안 그러면 데이터가 들어오는 순간 패널이 튄다.
 *   ④ 패널 본문이 세로 flex 다 — ①②는 부모가 flex 컨테이너일 때만 뜻이 있다.
 */

/* echarts 는 canvas 를 초기화한다 — jsdom 엔 canvas 가 없다. 여기서 재는 것은 래퍼 div 의
 * 스타일이지 그림이 아니므로 init 을 빈 껍데기로 바꾼다. */
vi.mock('echarts', () => ({
  init: () => ({ setOption() {}, resize() {}, dispose() {} }),
}));

/* setup.ts 가 EChart 를 null 로 모킹한다 — 이 파일은 그 컴포넌트 자체를 재므로 진짜를 집는다. */
const realEChart = async () => {
  const mod = await vi.importActual<typeof import('@/components/charts/EChart')>('@/components/charts/EChart');
  return mod.default;
};

describe('EChart — 높이는 고정이 아니라 최소다', () => {
  it('height prop 은 min-height 로 나가고, 고정 height 는 걸리지 않는다', async () => {
    const EChart = await realEChart();
    const { container } = render(<EChart option={{}} height={180} />);
    const box = container.querySelector('[role="img"]') as HTMLElement;
    expect(box).toBeTruthy();
    expect(box.style.minHeight).toBe('180px');
    // 고정 높이가 남아 있으면 부모가 아무리 커져도 이 박스는 그대로다 — 그것이 이 버그였다.
    expect(box.style.height).toBe('');
  });

  it('남는 세로 공간을 먹겠다는 표시(.fillv)를 스스로 단다', async () => {
    const EChart = await realEChart();
    const { container } = render(<EChart option={{}} height={90} />);
    const box = container.querySelector('[role="img"]') as HTMLElement;
    expect(box.classList.contains('fillv')).toBe(true);
  });

  it('호출처가 준 className 을 지우지 않는다', async () => {
    const EChart = await realEChart();
    const { container } = render(<EChart option={{}} height={90} className="mine" />);
    const box = container.querySelector('[role="img"]') as HTMLElement;
    expect(box.classList.contains('fillv')).toBe(true);
    expect(box.classList.contains('mine')).toBe(true);
  });
});

/*
 * ③ 빈 상태도 같이 늘어난다.
 *
 * NoData 는 "차트가 쓰던 높이를 그대로 지킨다"는 규율로 만들어졌다(값이 들고 날 때 그리드가
 * 튀지 않게). 차트가 늘어나게 된 지금, 빈 상태만 고정으로 남으면 **데이터가 들어오는 순간**
 * 패널 안이 튄다 — 규율의 목적이 그대로 뒤집힌다.
 */
const DEV_EMPTY = {
  totals: { linesAdded: 0, linesRemoved: 0, editsAccepted: 0, editsRejected: 0 },
  byDay: [{ day: '2026-08-10', linesAdded: 0, linesRemoved: 0, editsAccepted: 0, editsRejected: 0 }],
};

function dashRoutes(): [string, RouteSpec][] {
  return [
    ['/api/usage/dev', { body: DEV_EMPTY }],
    ...platformRoutes(),
    ['/api/usage/summary', { body: golden('summary') }],
  ];
}

describe('NoData — 빈 상태도 차트와 같이 늘어난다', () => {
  beforeEach(() => { mockFetch(dashRoutes()); });

  /*
   * `미수집` 은 캔버스 밖(플랫폼 롤업의 SupportBadge)에서도 쓰는 어휘다 — 전체 문서에서
   * 찾으면 그쪽까지 걸려 이 단정이 엉뚱한 것을 재게 된다. 패널 id 로 좁힌다.
   */
  it.each([['dev-loc'], ['dev-edit']])('%s 의 미수집 자리는 고정 높이가 아니라 최소 높이 + .fillv 다', async (pid) => {
    const { default: GrafanaDash } = await import('@/components/grafana/GrafanaDash');
    render(<GrafanaDash />);
    await screen.findAllByText('미수집');
    const cell = document.querySelector<HTMLElement>(`[data-pid="${pid}"]`);
    expect(cell, `패널 ${pid} 이 렌더되지 않았다`).toBeTruthy();
    const mark = within(cell!).getByText('미수집');
    const box = mark.closest('.fillv') as HTMLElement | null;
    expect(box, '미수집 자리가 .fillv 를 달고 있지 않다').toBeTruthy();
    // 고정 높이가 남아 있으면 값이 들어오는 순간 차트 높이와 어긋나 패널 안이 튄다.
    expect(box!.style.minHeight).not.toBe('');
    expect(box!.style.height).toBe('');
  });
});

/*
 * ④ 본문이 세로 flex 라는 CSS 계약.
 *
 * jsdom 은 스타일시트를 적용하지 않으므로 화면에서 확인할 수 없다 — 텍스트로 단정한다.
 * 약한 테스트지만 **없는 것보다 낫다**: 이 한 줄이 사라지면 `.fillv` 는 아무 일도 하지 않는
 * 장식이 되고, 증상은 브라우저를 열어야만 보이는 자리로 돌아간다.
 */
describe('globals.css — 패널 본문은 세로 flex 다', () => {
  /* .tsx 는 react 플러그인을 타면서 import.meta.url 이 파일 URL 이 아니게 된다(no-direct-fetch
   * 테스트가 쓰는 fileURLToPath 가 여기서는 던진다). vitest 의 root 는 vitest.config.ts 가 있는
   * web/ 이므로 cwd 를 기준으로 잡는다. */
  const css = readFileSync(path.resolve(process.cwd(), 'app', 'globals.css'), 'utf8');

  it('.gpanel-card > .gpanel-body 가 flex column 이고 스크롤 규율을 유지한다', () => {
    const rule = css.match(/\.gpanel-card\s*>\s*\.gpanel-body\s*\{([^}]*)\}/)?.[1];
    expect(rule, '.gpanel-card > .gpanel-body 규칙을 찾지 못했다').toBeTruthy();
    expect(rule).toMatch(/display:\s*flex/);
    expect(rule).toMatch(/flex-direction:\s*column/);
    // 넘치는 표는 잘리는 대신 스크롤한다 — 차트를 늘리려다 이 규율을 깨면 표가 말없이 잘린다.
    expect(rule).toMatch(/overflow:\s*auto/);
    expect(rule).toMatch(/min-height:\s*0/);
  });

  it('.fillv 는 남는 세로 공간을 먹는다', () => {
    const rule = css.match(/\.fillv\s*\{([^}]*)\}/)?.[1];
    expect(rule, '.fillv 규칙을 찾지 못했다').toBeTruthy();
    expect(rule).toMatch(/flex:\s*1\s+1\s+auto/);
  });
});
