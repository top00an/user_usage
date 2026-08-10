import { describe, it, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import Dashboard from '@/components/Dashboard';
import {
  getPlatforms, getSessions, getDistribution, getQuality, getCoverage, getLeaderboard, seriesQuery,
} from '@/lib/api';
import { supportOf, platformMeta, isPlatformId, PLATFORM_FILTER_VALUES } from '@/lib/platforms';
import { setPlatformFilter, getPlatformFilter } from '@/lib/platformFilter';
import { authRoutes, mockFetch, obsRoutes, trackRoutes, PLATFORMS_FIXTURE } from './helpers';

/*
 * 멀티플랫폼 표시의 계약.
 *
 * 여기서 재는 것은 네 가지다:
 *   ① 지원표가 세 상태(실제 0 · 미수집 · 해당 없음)를 **가른다**
 *   ② 질의: platform 미지정이면 파라미터를 **안 붙인다**(현행 동작 = 골든 무회귀의 근거)
 *   ③ 화면: 없는 값을 0 으로 그리지 않는다 — codex 의 캐시생성은 숫자가 아니라 배지다
 *   ④ 라벨: 비용은 어디서나 'API 환산 비용'이고, 청구액이 아니라고 말한다
 */

const USER = { username: 'admin', role: 'admin', tenant: 'acme' };

function allRoutes(extra: Parameters<typeof mockFetch>[0] = []) {
  return [...extra, ...authRoutes(USER), ...trackRoutes(), ...obsRoutes()];
}

beforeEach(() => {
  location.hash = '';
  setPlatformFilter(''); // 스토어는 모듈 수준이다 — 테스트 사이에 새지 않게 되돌린다
});

/* ── ① 지원표 ───────────────────────────────────────────────────────── */

describe('lib/platforms — 0 과 "모른다"를 가르는 사실표', () => {
  it('claude 는 전 축을 수집한다', () => {
    for (const m of ['tool', 'bash', 'slash', 'skill', 'agent', 'mcp', 'keyword', 'cacheRead', 'cacheCreate', 'loc', 'edits'] as const) {
      expect(supportOf('claude', m).state).toBe('yes');
    }
  });

  it('codex 의 slash·skill·agent 는 미수집이다 (0 이 아니다)', () => {
    for (const m of ['slash', 'skill', 'agent'] as const) {
      expect(supportOf('codex', m).state).toBe('unmeasured');
      expect(supportOf('codex', m).why).not.toBe('');
    }
  });

  it('codex 의 캐시생성은 해당 없음이다 — OpenAI 는 캐시 쓰기에 과금하지 않는다', () => {
    const s = supportOf('codex', 'cacheCreate');
    expect(s.state).toBe('na');
    expect(s.why).toMatch(/캐시 쓰기/);
    // 캐시읽기는 반대로 수집된다 — 둘을 뭉치면 codex 의 캐시 사용이 통째로 사라진다.
    expect(supportOf('codex', 'cacheRead').state).toBe('yes');
  });

  it('gemini 는 준비 중, 모르는 플랫폼은 미상이다 — 단정하지 않는다', () => {
    expect(supportOf('gemini', 'tool').state).toBe('planned');
    expect(supportOf('grok', 'tool').state).toBe('unknown');
    expect(supportOf('other', 'tool').state).toBe('unknown');
  });

  /*
   * 반대 방향의 거짓말도 막는다. 롤업이 숫자로 답하는 축(세션·토큰·비용)은 서버가 실제 행에서
   * 집계한 **관측**이다. 그걸 '준비 중'·'미상'으로 덮으면 이번엔 수집된 값을 화면이 숨긴다 —
   * 지원표를 갱신하지 않은 날 새 플랫폼의 사용량이 통째로 사라지는 경로가 여기다.
   */
  it('값 축(세션·토큰·비용)은 어떤 플랫폼이든 숨기지 않는다', () => {
    for (const p of ['gemini', 'other', 'grok']) {
      for (const m of ['sessions', 'input', 'output', 'cacheRead', 'cost'] as const) {
        expect(supportOf(p, m).state).toBe('yes');
      }
    }
    // 단 codex 의 캐시생성은 예외다 — 열은 합산되지만 개념 자체가 없다.
    expect(supportOf('codex', 'cacheCreate').state).toBe('na');
  });

  it('질의로 보낼 수 있는 값은 서버 허용목록과 같다 (그 밖은 400 이므로 보내지 않는다)', () => {
    // store/platform.go 의 PlatformFilterValues() = Platforms + other. 순서까지 같게 둔다.
    expect([...PLATFORM_FILTER_VALUES]).toEqual(['claude', 'codex', 'gemini', 'antigravity', 'other']);
    expect(isPlatformId('claud')).toBe(false);
    expect(isPlatformId('CLAUDE')).toBe(false); // 서버는 정규화하지 않는다 — 대문자도 오타다
    expect(isPlatformId('codex')).toBe(true);
    expect(isPlatformId('antigravity')).toBe(true);
    expect(isPlatformId('antigrav')).toBe(false);
  });

  /*
   * antigravity — "Gemini CLI" 라 불리는 두 도구 중 하나다(다른 하나가 gemini).
   * 모델이 같아 model 로는 안 갈리지만 **기록하는 축이 다르다.** 그래서 지원표가 갈라져야 한다.
   */
  it('antigravity 의 행동 축(도구·bash·MCP·스킬·서브에이전트·슬래시·LOC·편집)은 미수집이다', () => {
    for (const m of ['tool', 'bash', 'mcp', 'keyword', 'slash', 'skill', 'agent', 'loc', 'edits'] as const) {
      const s = supportOf('antigravity', m);
      expect(s.state).toBe('unmeasured');
      expect(s.why).toMatch(/Antigravity/);
    }
  });

  it('antigravity 의 캐시생성은 해당 없음이다 — 암시적 캐싱에는 쓰기 과금 개념이 없다', () => {
    const s = supportOf('antigravity', 'cacheCreate');
    expect(s.state).toBe('na');
    expect(s.why).not.toBe('');
    // 캐시읽기는 반대로 수집된다 — 둘을 뭉치면 캐시 사용이 통째로 사라진다(codex 와 같은 규율).
    expect(supportOf('antigravity', 'cacheRead').state).toBe('yes');
  });

  it('antigravity 의 값 축(세션·토큰·비용·모델)은 수집된다 — 미수집으로 덮지 않는다', () => {
    for (const m of ['sessions', 'input', 'output', 'cacheRead', 'cost'] as const) {
      expect(supportOf('antigravity', m).state).toBe('yes');
    }
  });

  it('antigravity 는 gemini 와 다른 표시명·계열색을 갖는다 (한 도구로 보이면 안 된다)', () => {
    expect(platformMeta('antigravity').label).toBe('Antigravity');
    expect(platformMeta('antigravity').color).not.toBe(platformMeta('gemini').color);
    // 기존 플랫폼의 지원표는 이 작업으로 바뀌지 않는다.
    expect(supportOf('gemini', 'tool').state).toBe('planned');
    expect(supportOf('claude', 'loc').state).toBe('yes');
  });

  it('모르는 플랫폼도 화면에서 사라지지 않는다 (이름 그대로 그린다)', () => {
    expect(platformMeta('grok').label).toBe('grok');
    expect(platformMeta('codex').label).toBe('Codex');
  });
});

/* ── ② 질의 ─────────────────────────────────────────────────────────── */

describe('lib/api — platform 질의', () => {
  function spyFetch() {
    const spy = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({}), { status: 200, headers: { 'Content-Type': 'application/json' } }),
    );
    vi.stubGlobal('fetch', spy);
    return spy;
  }

  it('미지정이면 platform 파라미터를 붙이지 않는다 (=전체, 현행 동작)', async () => {
    const spy = spyFetch();
    await getDistribution();
    await getQuality();
    await getCoverage();
    await getLeaderboard();
    await getSessions({ sort: 'cost', top: 25 });
    const urls = spy.mock.calls.map(([u]) => String(u));
    expect(urls).toEqual([
      '/api/usage/distribution',
      '/api/usage/quality',
      '/api/usage/coverage',
      '/api/usage/leaderboard',
      '/api/usage/sessions?sort=cost&top=25',
    ]);
    // 빈 값을 실어 보내면 서버가 오타로 보고 400 을 낸다.
    expect(urls.some((u) => u.includes('platform='))).toBe(false);
  });

  it('빈 문자열도 파라미터를 만들지 않는다', async () => {
    const spy = spyFetch();
    await getDistribution({ platform: '' });
    expect(String(spy.mock.calls[0]?.[0])).toBe('/api/usage/distribution');
  });

  it('선택하면 각 조회에 platform= 을 싣는다', async () => {
    const spy = spyFetch();
    await getDistribution({ platform: 'codex' });
    await getSessions({ sort: 'cost', top: 25, platform: 'codex' });
    const urls = spy.mock.calls.map(([u]) => String(u));
    expect(urls[0]).toBe('/api/usage/distribution?platform=codex');
    expect(urls[1]).toBe('/api/usage/sessions?sort=cost&top=25&platform=codex');
  });

  it('series 질의도 platform 을 싣고, 없으면 안 붙는다', () => {
    expect(seriesQuery({ metric: 'tokens', interval: 'day', platform: 'codex' }))
      .toBe('/api/usage/series?metric=tokens&interval=day&platform=codex');
    expect(seriesQuery({ metric: 'tokens', interval: 'day' }))
      .toBe('/api/usage/series?metric=tokens&interval=day');
  });

  it('플랫폼 롤업은 기간 파라미터 없이 부른다 — 서버가 days 를 보지 않기 때문이다', async () => {
    const spy = spyFetch();
    await getPlatforms();
    expect(String(spy.mock.calls[0]?.[0])).toBe('/api/usage/platforms');
  });
});

/* ── 저장된 선택 ────────────────────────────────────────────────────── */

describe('lib/platformFilter — 저장된 선택은 허용목록으로 좁힌다', () => {
  it('허용목록 밖 값은 전체로 접힌다 (400 을 부르는 질의를 만들지 않는다)', () => {
    setPlatformFilter('claud');
    expect(getPlatformFilter()).toBe('');
    setPlatformFilter('codex');
    expect(getPlatformFilter()).toBe('codex');
  });
});

/* ── ③ 화면: 없는 값을 0 으로 그리지 않는다 ─────────────────────────── */

describe('플랫폼 요약 — 실제 0 · 미수집 · 해당 없음을 갈라 말한다', () => {
  beforeEach(() => {
    location.hash = '#/overview';
    mockFetch(allRoutes());
  });

  it('플랫폼별 카드를 응답 목록대로 그린다', async () => {
    render(<Dashboard />);
    const sect = (await screen.findByRole('heading', { name: '플랫폼별 사용량' })).closest('section')!;
    expect(within(sect).getAllByText('Claude').length).toBeGreaterThan(0);
    expect(within(sect).getAllByText('Codex').length).toBeGreaterThan(0);
  });

  it('codex 의 캐시생성은 0 이 아니라 "해당 없음" 배지다', async () => {
    render(<Dashboard />);
    await screen.findByRole('heading', { name: '플랫폼별 사용량' });

    // 응답의 codex.cacheCreate 는 0 이다. 그 0 이 화면에 숫자로 나오면 안 된다.
    expect(PLATFORMS_FIXTURE.platforms[1]!.cacheCreate).toBe(0);
    const badges = screen.getAllByText('해당 없음').filter((el) => el.classList.contains('badge'));
    expect(badges.length).toBeGreaterThan(0);
    for (const b of badges) {
      expect(b).toHaveAttribute('title', expect.stringContaining('캐시 쓰기'));
    }
    // 그리고 그 자리에 0 이 대신 그려지지 않았다.
    const cacheCreateRow = screen.getAllByText('캐시생성')[0]!.closest('.pf-kv-row, tr');
    expect(cacheCreateRow?.textContent).not.toMatch(/(^|\D)0(\D|$)/);
  });

  it('codex 의 슬래시·스킬·서브에이전트는 "미수집"로 표시된다', async () => {
    render(<Dashboard />);
    await screen.findByRole('heading', { name: '플랫폼별 사용량' });
    // 캡션에도 '미수집'이라는 낱말이 나오므로 배지만 골라 센다.
    const badges = screen.getAllByText('미수집').filter((el) => el.classList.contains('badge'));
    // slash · skill · agent 세 축
    expect(badges.length).toBeGreaterThanOrEqual(3);
    for (const b of badges) {
      // 배지만 있고 사유가 없으면 사람은 그걸 버그로 읽는다.
      expect(b).toHaveAttribute('title', expect.stringContaining('기록'));
    }
  });

  it('공통 코어 비교 표는 합계 토큰 열을 두지 않는다 (성질이 다른 축을 한 이름으로 더하지 않는다)', async () => {
    render(<Dashboard />);
    const sect = (await screen.findByRole('heading', { name: '플랫폼별 사용량' })).closest('section')!;
    expect(within(sect).getByRole('columnheader', { name: '캐시읽기' })).toBeInTheDocument();
    expect(within(sect).queryByRole('columnheader', { name: '토큰' })).not.toBeInTheDocument();
  });
});

/* ── ③ antigravity 가 화면에서 1급으로 그려지는가 ───────────────────── */

/*
 * antigravity 롤업 응답. 값 축은 실제로 오고(세션·토큰·비용), 행동 축은 응답에 아예 없다.
 * 화면은 이 둘을 갈라 그려야 한다 — 값은 숫자로, 행동은 '미수집' 배지로.
 */
const AG_ROW = {
  platform: 'antigravity', sessions: 7, input: 54321, output: 9876,
  cacheRead: 123456, cacheCreate: 0, costUsd: 1.2345,
  firstSeen: '2026-08-06T00:00:00.000Z', lastSeen: '2026-08-10T01:00:00.000Z',
};

function withAntigravity(): [string, Parameters<typeof mockFetch>[0][number][1]][] {
  return [['/api/usage/platforms', { body: { platforms: [...PLATFORMS_FIXTURE.platforms, AG_ROW] } }]];
}

/** 지원표에서 (지표 행 × 플랫폼 열) 한 칸의 글자를 읽는다. */
function supportCellText(metricLabel: string, platformLabel: string): string {
  // 지원표만 행 머리(rowheader)를 갖는다 — 공통 코어 표는 tbody 가 전부 td 다.
  const row = screen.getByRole('rowheader', { name: metricLabel }).closest('tr')!;
  const headers = within(row.closest('table')!).getAllByRole('columnheader').map((h) => h.textContent);
  const col = headers.indexOf(platformLabel);
  expect(col).toBeGreaterThan(0); // 0번은 '지표' 열이다
  return within(row).getAllByRole('cell')[col - 1]!.textContent ?? '';
}

describe('antigravity — 응답에 뜨면 코드 변경 없이 1급으로 붙는다', () => {
  beforeEach(() => {
    location.hash = '#/overview';
    mockFetch(allRoutes(withAntigravity()));
  });

  it('카드가 그려지고 값 축(세션·토큰·비용)은 숫자로 나온다', async () => {
    render(<Dashboard />);
    const sect = (await screen.findByRole('heading', { name: '플랫폼별 사용량' })).closest('section')!;
    const card = within(sect).getAllByText('Antigravity')[0]!.closest('.pf-card') as HTMLElement;
    expect(card.textContent).toMatch(/7\s*세션/);
    // 비용·토큰이 배지로 대체되지 않았다 — 서버가 집계한 관측이다.
    expect(within(card).queryByText('미수집')).not.toBeInTheDocument();
    expect(card.textContent).toMatch(/\$/);
  });

  it('캐시생성 0 은 숫자가 아니라 "해당 없음" 배지다', async () => {
    render(<Dashboard />);
    const sect = (await screen.findByRole('heading', { name: '플랫폼별 사용량' })).closest('section')!;
    const card = within(sect).getAllByText('Antigravity')[0]!.closest('.pf-card') as HTMLElement;
    const row = within(card).getByText('캐시생성').closest('.pf-kv-row') as HTMLElement;
    expect(within(row).getByText('해당 없음')).toBeInTheDocument();
    expect(row.textContent).not.toMatch(/(^|\D)0(\D|$)/);
  });

  it('지원표에서 도구·LOC·MCP 는 미수집, 세션·캐시읽기는 수집됨으로 갈린다', async () => {
    render(<Dashboard />);
    await screen.findByRole('heading', { name: '플랫폼별 사용량' });
    for (const m of ['내장 도구', 'MCP 도구', 'LOC(추가·삭제)', '스킬', '서브에이전트', '슬래시 명령']) {
      expect(supportCellText(m, 'Antigravity')).toBe('미수집');
    }
    expect(supportCellText('캐시생성', 'Antigravity')).toBe('해당 없음');
    expect(supportCellText('세션', 'Antigravity')).toBe('수집됨');
    expect(supportCellText('캐시읽기', 'Antigravity')).toBe('수집됨');
    // 옆 열(기존 플랫폼)은 이 작업으로 바뀌지 않았다.
    expect(supportCellText('내장 도구', 'Claude')).toBe('수집됨');
    expect(supportCellText('슬래시 명령', 'Codex')).toBe('미수집');
  });

  it('플랫폼 필터에 코드 변경 없이 선택지로 붙고, 고르면 질의로 나간다', async () => {
    location.hash = '#/usageobs';
    const { fn } = mockFetch(allRoutes(withAntigravity()));
    const user = userEvent.setup();
    render(<Dashboard />);
    await screen.findByRole('heading', { name: 'API 환산 비용' });

    const select = await screen.findByLabelText('플랫폼');
    const values = within(select).getAllByRole('option').map((o) => (o as HTMLOptionElement).value);
    expect(values).toEqual(['', 'claude', 'codex', 'antigravity']);

    await user.selectOptions(select, 'antigravity');
    await waitFor(() => {
      const urls = fn.mock.calls.map(([u]) => String(u));
      expect(urls.some((u) => u === '/api/usage/distribution?platform=antigravity')).toBe(true);
      expect(urls.some((u) => u.startsWith('/api/usage/sessions?') && u.includes('platform=antigravity'))).toBe(true);
    });
  });
});

/* ── ② + ③ 필터가 실제로 서버까지 간다 ─────────────────────────────── */

describe('플랫폼 필터 — 선택지는 응답이 정하고, 선택은 질의로 나간다', () => {
  it('전체가 기본이고 그때는 platform 을 보내지 않는다', async () => {
    location.hash = '#/usageobs';
    const { fn } = mockFetch(allRoutes());
    render(<Dashboard />);
    await screen.findByRole('heading', { name: 'API 환산 비용' });

    const urls = fn.mock.calls.map(([u]) => String(u));
    expect(urls.some((u) => u.includes('/api/usage/sessions'))).toBe(true);
    expect(urls.some((u) => u.includes('platform='))).toBe(false);
  });

  it('플랫폼을 고르면 관측 탭의 조회들이 platform= 을 싣는다', async () => {
    location.hash = '#/usageobs';
    const { fn } = mockFetch(allRoutes());
    const user = userEvent.setup();
    render(<Dashboard />);
    await screen.findByRole('heading', { name: 'API 환산 비용' });

    await user.selectOptions(screen.getByLabelText('플랫폼'), 'codex');

    await waitFor(() => {
      const urls = fn.mock.calls.map(([u]) => String(u));
      expect(urls.some((u) => u.startsWith('/api/usage/sessions?') && u.includes('platform=codex'))).toBe(true);
      expect(urls.some((u) => u === '/api/usage/distribution?platform=codex')).toBe(true);
      expect(urls.some((u) => u === '/api/usage/leaderboard?platform=codex')).toBe(true);
    });

    // 반대로 platform 축을 못 받는 조회에는 절대 붙지 않는다 — 붙여도 서버가 무시하므로
    // 화면이 전체 합계를 그 플랫폼의 값인 척 그리게 된다.
    const urls = fn.mock.calls.map(([u]) => String(u));
    expect(urls.some((u) => u.includes('/api/usage/seats') && u.includes('platform='))).toBe(false);
    expect(urls.some((u) => u.includes('/api/usage/summary') && u.includes('platform='))).toBe(false);
  });

  it('데이터가 없는 플랫폼은 선택지에 없다 (하드코딩 목록이 아니다)', async () => {
    location.hash = '#/usageobs';
    mockFetch(allRoutes());
    render(<Dashboard />);
    const select = await screen.findByLabelText('플랫폼');
    const values = within(select).getAllByRole('option').map((o) => (o as HTMLOptionElement).value);
    expect(values).toEqual(['', 'claude', 'codex']);
    expect(values).not.toContain('gemini'); // 응답에 없다
  });

  it('플랫폼이 하나뿐이면 선택 컨트롤을 그리지 않는다', async () => {
    location.hash = '#/usageobs';
    mockFetch([
      ...authRoutes(USER),
      ['/api/usage/platforms', { body: { platforms: [PLATFORMS_FIXTURE.platforms[0]] } }],
      ...obsRoutes(),
      ...trackRoutes(),
    ]);
    render(<Dashboard />);
    await screen.findByRole('heading', { name: 'API 환산 비용' });
    expect(screen.queryByLabelText('플랫폼')).not.toBeInTheDocument();
  });

  it('플랫폼 축을 못 거르는 카드는 "전체 플랫폼 기준"이라고 말한다', async () => {
    location.hash = '#/usage'; // 사용 추적: summary·dispatch 는 platform 축을 받지 않는다
    mockFetch(allRoutes());
    const user = userEvent.setup();
    render(<Dashboard />);
    await screen.findByRole('heading', { name: '사용자별' });

    await user.selectOptions(await screen.findByLabelText('플랫폼'), 'codex');
    expect(await screen.findAllByText(/전체 플랫폼 기준/)).not.toHaveLength(0);
  });
});

/* ── ③ 축 패널 ─────────────────────────────────────────────────────── */

describe('축 패널 — 미지원 축을 0 으로 그리지 않는다', () => {
  beforeEach(() => {
    location.hash = '#/usage';
    mockFetch(allRoutes());
  });

  it('축마다 어느 플랫폼이 그것을 기록하는지 말한다', async () => {
    const user = userEvent.setup();
    render(<Dashboard />);
    await screen.findByRole('heading', { name: '사용 현황' });

    // 기본 축(개발 명령)은 두 플랫폼 모두 수집한다.
    expect(screen.getByText('이 축을 기록하는 플랫폼:')).toBeInTheDocument();
    expect(screen.getByText('Codex · 수집됨')).toBeInTheDocument();

    // 서브에이전트 축으로 옮기면 Codex 는 미수집이다.
    await user.click(screen.getByRole('tab', { name: '서브에이전트' }));
    expect(await screen.findByText('Codex · 미수집')).toBeInTheDocument();
    expect(screen.getByText('Claude · 수집됨')).toBeInTheDocument();
  });

  it('미지원 축을 선택한 플랫폼으로 걸러 놓으면 "0 이 아니라 없다"고 말한다', async () => {
    const user = userEvent.setup();
    render(<Dashboard />);
    await screen.findByRole('heading', { name: '사용 현황' });

    await user.selectOptions(await screen.findByLabelText('플랫폼'), 'codex');
    await user.click(screen.getByRole('tab', { name: '스킬' }));

    expect(await screen.findByText(/0 이 아니라 애초에 없습니다/)).toBeInTheDocument();
  });
});

/* ── ④ 비용 라벨 ───────────────────────────────────────────────────── */

describe('비용 라벨 — 환산 추정치를 청구액이라 부르지 않는다', () => {
  it('관측 탭의 비용 카드가 "API 환산 비용"이고 청구액이 아니라고 말한다', async () => {
    location.hash = '#/usageobs';
    mockFetch(allRoutes());
    render(<Dashboard />);
    expect(await screen.findByRole('heading', { name: 'API 환산 비용' })).toBeInTheDocument();
    expect(screen.getByText(/실제 청구액이 아닙니다/)).toBeInTheDocument();
  });

  it('대시보드 타일도 같은 라벨을 쓴다 (Total Cost 가 아니다)', async () => {
    location.hash = '#/overview';
    mockFetch(allRoutes());
    render(<Dashboard />);
    await screen.findByRole('heading', { name: '플랫폼별 사용량' });
    expect(screen.queryByText('Total Cost')).not.toBeInTheDocument();
    expect(screen.getAllByText('API 환산 비용').length).toBeGreaterThan(0);
    expect(screen.getAllByText(/구독 요금제 사용 시 실제 청구액과 다릅니다/).length).toBeGreaterThan(0);
  });
});
