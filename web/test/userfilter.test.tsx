/*
 * ── 사용 추적 · 사용자 필터 ───────────────────────────────────────────────
 *
 * 이 화면의 요점은 "유저마다 사용량을 따로 본다"다. 그래서 검증도 그 축으로 한다:
 *
 *   ① 선택지는 **필터 없는 응답**(leaderboard)이 정한다 — 하드코딩한 목록이 아니다.
 *   ② 고르면 summary·dispatch **양쪽에** user= 가 실린다. 한쪽만 실리면 같은 화면의 두 카드가
 *      서로 다른 모집단을 그리면서 그 사실을 말하지 않는다.
 *   ③ 고른 뒤에도 **명단이 줄지 않는다.** 걸린 응답의 byUser 로 목록을 만들면 한 사람을 고른
 *      순간 그 사람만 남아 다른 사람으로 갈아탈 수 없게 된다 — 가장 쉽게 생기는 회귀다.
 *   ④ 전체가 기본이고, 그때는 user 를 아예 보내지 않는다(골든 무회귀의 프런트 쪽 근거).
 *   ⑤ 선택지가 2개 미만이면 컨트롤을 그리지 않는다.
 */

import { describe, expect, it, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import Dashboard from '@/components/Dashboard';
import { setPlatformFilter } from '@/lib/platformFilter';
import { authRoutes, golden, mockFetch, obsRoutes, trackRoutes, type RouteSpec } from './helpers';

/** 사용 추적 탭이 쓰는 경로 + 명단 출처(leaderboard). */
function usageRoutes(extra: [string, RouteSpec][] = []): [string, RouteSpec][] {
  return [
    ...extra,
    ['/api/usage/leaderboard', { body: golden('leaderboard') }],
    ...trackRoutes(),
    ...authRoutes({ username: 'admin', role: 'admin', tenant: 'acme' }),
  ];
}

/** 사용 추적 탭이 떠서 첫 조회가 끝날 때까지 기다린다. */
async function openTrackTab() {
  render(<Dashboard />);
  await screen.findByRole('heading', { name: '사용자별' });
}

beforeEach(() => {
  location.hash = '#/usage';
  setPlatformFilter('');
});

describe('사용 추적 — 사용자 필터', () => {
  it('① 선택지는 응답이 정한다 (leaderboard 의 사람들 + 전체)', async () => {
    mockFetch(usageRoutes());
    await openTrackTab();

    const select = await screen.findByLabelText('사용자');
    const values = within(select).getAllByRole('option').map((o) => (o as HTMLOptionElement).value);
    // golden/leaderboard.json 의 순서 그대로 — 화면이 목록을 다시 정렬하지 않는다.
    expect(values).toEqual(['', 'carol', 'bob', 'alice', 'frank', 'dave', 'erin']);
  });

  it('④ 전체가 기본이고 그때는 user 를 보내지 않는다', async () => {
    const { fn } = mockFetch(usageRoutes());
    await openTrackTab();

    const urls = fn.mock.calls.map(([u]) => String(u));
    expect(urls.some((u) => u.startsWith('/api/usage/summary'))).toBe(true);
    expect(urls.some((u) => u.includes('user='))).toBe(false);
  });

  it('② 고르면 summary·dispatch 양쪽에 user= 가 실린다', async () => {
    const { fn } = mockFetch(usageRoutes());
    const user = userEvent.setup();
    await openTrackTab();

    await user.selectOptions(await screen.findByLabelText('사용자'), 'alice');

    await waitFor(() => {
      const urls = fn.mock.calls.map(([u]) => String(u));
      expect(urls).toContain('/api/usage/summary?user=alice');
      expect(urls).toContain('/api/usage/dispatch?user=alice');
    });
  });

  it('② 명단 출처(leaderboard)에는 user 를 싣지 않는다 — 자기 자신을 좁히면 안 된다', async () => {
    const { fn } = mockFetch(usageRoutes());
    const user = userEvent.setup();
    await openTrackTab();

    await user.selectOptions(await screen.findByLabelText('사용자'), 'alice');
    await waitFor(() => {
      expect(fn.mock.calls.map(([u]) => String(u))).toContain('/api/usage/summary?user=alice');
    });

    const leaderboardCalls = fn.mock.calls
      .map(([u]) => String(u))
      .filter((u) => u.startsWith('/api/usage/leaderboard'));
    expect(leaderboardCalls.length).toBeGreaterThan(0);
    for (const u of leaderboardCalls) {
      expect(u).not.toContain('user=');
    }
  });

  /*
   * ③ 이것이 이 파일의 핵심 회귀 테스트다.
   *
   * 한 사람을 고르면 서버 응답의 byUser 는 한 줄로 줄어든다. 그 값으로 셀렉트를 만들면
   * 옵션이 ['', 'alice'] 로 쪼그라들어 **다른 사람으로 갈아탈 방법이 사라진다.**
   * 응답을 필터에 맞게 좁혀 돌려주는 목(mock)으로 그 상황을 실제로 만든다.
   */
  it('③ 고른 뒤에도 명단이 줄지 않는다 (다른 사람으로 갈아탈 수 있다)', async () => {
    const full = golden<{ byUser: { username: string }[]; totals: unknown }>('summary');
    const narrowed = {
      ...full,
      byUser: full.byUser.filter((r) => r.username === 'alice'),
    };
    const { fn } = mockFetch([
      // 구체적인 경로가 먼저다 — mockFetch 는 첫 접두사 일치를 쓴다.
      ['/api/usage/summary?user=alice', { body: narrowed }],
      ...usageRoutes(),
    ]);
    const user = userEvent.setup();
    await openTrackTab();

    const select = await screen.findByLabelText('사용자');
    await user.selectOptions(select, 'alice');
    await waitFor(() => {
      expect(fn.mock.calls.map(([u]) => String(u))).toContain('/api/usage/summary?user=alice');
    });

    // 좁혀진 응답이 화면에 반영됐다(사용자별 표가 한 줄).
    await waitFor(() => {
      const values = within(select).getAllByRole('option').map((o) => (o as HTMLOptionElement).value);
      expect(values).toEqual(['', 'carol', 'bob', 'alice', 'frank', 'dave', 'erin']);
    });

    // 그리고 실제로 갈아탈 수 있다.
    await user.selectOptions(select, 'bob');
    await waitFor(() => {
      expect(fn.mock.calls.map(([u]) => String(u))).toContain('/api/usage/summary?user=bob');
    });
  });

  it('⑤ 명단이 1명뿐이면 컨트롤을 그리지 않는다', async () => {
    const one = { ...golden<{ users: unknown[] }>('leaderboard'), users: [{ username: 'solo', sessions: 1, turns: 1, tokens: 1, cacheHitRate: 0, usd: 0, usdPerSession: 0, priced: true }] };
    mockFetch([
      ['/api/usage/leaderboard', { body: one }],
      ...trackRoutes(),
      ...authRoutes({ username: 'admin', role: 'admin', tenant: 'acme' }),
    ]);
    await openTrackTab();

    expect(screen.queryByLabelText('사용자')).not.toBeInTheDocument();
  });

  it('명단 조회가 실패해도 화면은 전체 기준으로 산다 (fail-soft)', async () => {
    mockFetch([
      ['/api/usage/leaderboard', { status: 500, body: { error: 'boom' } }],
      ...trackRoutes(),
      ...authRoutes({ username: 'admin', role: 'admin', tenant: 'acme' }),
    ]);
    await openTrackTab();

    expect(screen.queryByLabelText('사용자')).not.toBeInTheDocument();
    // 전체 집계는 그대로 보인다 — golden/summary.json 의 8세션.
    expect((await screen.findByText('보고된 세션')).closest('.tile')!.textContent).toMatch(/8/);
  });
});

/*
 * ── 권한 경계 ─────────────────────────────────────────────────────────────
 *
 * 사용 추적의 주 조회(summary·dispatch)는 전사 교차 뷰라 member 에게 403 이고, summary 는
 * fail-soft 가 아니라서 403 이 곧 탭 전체의 오류 화면이 된다. 열 수 없는 탭을 보여 주면
 * "권한이 없다"가 아니라 "고장났다"로 읽힌다 — 그래서 member 에게는 감춘다.
 */
describe('사용 추적 — member 에게는 감춘다 (열 수 없는 탭이다)', () => {
  const member = () => [
    ...authRoutes({ username: 'bob', role: 'member', tenant: 'acme' }),
    ...trackRoutes(),
    ...obsRoutes(),
  ] as [string, RouteSpec][];

  it('member 에게 사용 추적 탭이 보이지 않는다', async () => {
    location.hash = '';
    mockFetch(member());
    render(<Dashboard />);
    await screen.findByRole('tab', { name: '대시보드' });
    expect(screen.queryByRole('tab', { name: '사용 추적' })).not.toBeInTheDocument();
  });

  it('member 가 #/usage 딥링크로 와도 그 탭 본문이 마운트되지 않는다 (숨김이 아니라 렌더 차단)', async () => {
    location.hash = '#/usage';
    mockFetch(member());
    render(<Dashboard />);
    expect(await screen.findByRole('tab', { name: '대시보드' })).toHaveAttribute('aria-selected', 'true');
    // 사용 추적의 카드가 아예 없다.
    expect(screen.queryByRole('heading', { name: '사용자별' })).not.toBeInTheDocument();
    expect(screen.queryByLabelText('사용자')).not.toBeInTheDocument();
  });

  it('admin 에게는 그대로 보인다 (감춤이 과하게 걸리지 않았다)', async () => {
    location.hash = '';
    mockFetch(usageRoutes());
    render(<Dashboard />);
    expect(await screen.findByRole('tab', { name: '사용 추적' })).toBeInTheDocument();
  });
});

/*
 * ── 사용 관측 탭 — 비용·좌석·팀·분포도 유저별로 본다 ─────────────────────
 *
 * 요구: "비용 토큰 모든 차트는 연동된 아이디 사용자별로 각자 봐야 한다. 전체 통합은 기본이고
 * 세부 내역도 필요하다."
 *
 * 사용 추적 탭만 유저별이면 절반이다 — 비용이 사는 화면은 사용 관측이다.
 */
describe('사용 관측 — 유저별 세부 내역', () => {
  const obsRoutesAll = (extra: [string, RouteSpec][] = []): [string, RouteSpec][] => [
    ...extra,
    ...obsRoutes(),
    ...trackRoutes(),
    ...authRoutes({ username: 'admin', role: 'admin', tenant: 'acme' }),
  ];

  async function openObsTab() {
    render(<Dashboard />);
    await screen.findByRole('heading', { name: 'API 환산 비용' });
  }

  it('전체가 기본이다 — 그때는 user 를 보내지 않는다', async () => {
    location.hash = '#/usageobs';
    const { fn } = mockFetch(obsRoutesAll());
    await openObsTab();

    const urls = fn.mock.calls.map(([u]) => String(u));
    expect(urls.some((u) => u.includes('/api/usage/distribution'))).toBe(true);
    expect(urls.some((u) => u.includes('user='))).toBe(false);
  });

  /*
   * 고르면 **일곱 조회 전부**에 실려야 한다. 하나만 빠지면 같은 화면에서 한 카드는 개인,
   * 다른 카드는 전사를 그리면서 그 사실을 말하지 않는다 — 이 축에서 가장 나쁜 실패다.
   */
  it('사용자를 고르면 비용·세션·리더보드·품질·커버리지·좌석·팀 전부에 user= 가 실린다', async () => {
    location.hash = '#/usageobs';
    const { fn } = mockFetch(obsRoutesAll());
    const user = userEvent.setup();
    await openObsTab();

    await user.selectOptions(await screen.findByLabelText('사용자'), 'alice');

    await waitFor(() => {
      const urls = fn.mock.calls.map(([u]) => String(u));
      for (const ep of ['distribution', 'sessions', 'leaderboard', 'quality', 'coverage', 'seats', 'teams']) {
        expect(
          urls.some((u) => u.includes(`/api/usage/${ep}`) && u.includes('user=alice')),
          `${ep} 에 user=alice 가 실리지 않았다`,
        ).toBe(true);
      }
    });
  });

  it('명단 출처(필터 없는 leaderboard)에는 user 를 싣지 않는다', async () => {
    location.hash = '#/usageobs';
    const { fn } = mockFetch(obsRoutesAll());
    const user = userEvent.setup();
    await openObsTab();

    await user.selectOptions(await screen.findByLabelText('사용자'), 'alice');
    await waitFor(() => {
      expect(fn.mock.calls.map(([u]) => String(u)).some((u) => u.includes('user=alice'))).toBe(true);
    });
    // 필터 없는 leaderboard 호출이 최소 하나 남아 있어야 명단이 줄지 않는다.
    const plain = fn.mock.calls.map(([u]) => String(u))
      .filter((u) => u.startsWith('/api/usage/leaderboard') && !u.includes('user='));
    expect(plain.length).toBeGreaterThan(0);
  });
});
