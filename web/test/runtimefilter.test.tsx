/*
 * ── 실행 위치(runtime) 필터 ───────────────────────────────────────────────
 *
 * 로컬 LLM 관측의 프런트 쪽 계약이다(docs/PLAN-local-llm.md §2.2). 검증 축:
 *
 *   ① 컨트롤이 뜨고 선택지는 **정적**이다(전체·클라우드·로컬). platform 과 달리 응답에서
 *      만들지 않는다 — 서버 허용목록이 이분법으로 고정돼 있다.
 *   ② 고르면 **모든 스코프 조회에** runtime= 이 실린다. 한쪽만 실리면 같은 화면의 두 카드가
 *      서로 다른 모집단을 그리면서 그 사실을 말하지 않는다 — 실제로 그 사고가 났다
 *      (getSummary 가 UserParams 만 받아 runtime 을 조용히 버렸다. 2026-08-21 브라우저 실측:
 *      seats·dev 는 좁혀졌는데 활성 세션 타일만 1,589 그대로였다).
 *   ③ 전체가 기본이고, 그때는 runtime 을 **아예 보내지 않는다**(골든 무회귀의 프런트 근거).
 *      빈 값(`runtime=`)을 보내면 서버가 400 을 낸다.
 *   ④ 필터가 걸리면 배지로 밝힌다. 전체면 아무 말도 하지 않는다(PlatformScope 와 같은 규율).
 *   ⑤ 필터 셋은 **한 줄**에 선다(.pf-bars) — 축이 늘 때마다 상단이 한 줄씩 먹지 않게.
 */

import { describe, expect, it, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import Dashboard from '@/components/Dashboard';
import { setPlatformFilter } from '@/lib/platformFilter';
import { setRuntimeFilter } from '@/lib/runtimeFilter';
import { authRoutes, mockFetch, obsRoutes, trackRoutes, type RouteSpec } from './helpers';

/*
 * 관측 탭이 부르는 것 전부 + 관리자 전용 두 개(seats·teams).
 *
 * seats·teams 는 골든에 스냅샷이 없다(관리자 교차 뷰라 계약 표본에서 빠져 있다). 화면이
 * softly 로 감싸 null 이면 카드가 스스로 숨으므로, 여기서는 **빈 응답**을 준다 — 그래야
 * 그 두 조회도 실제로 나가고 runtime 이 실리는지 볼 수 있다.
 */
function dashRoutes(extra: [string, RouteSpec][] = []): [string, RouteSpec][] {
  return [
    ...extra,
    ['/api/usage/seats', { body: { seats: [], summary: {} } }],
    ['/api/usage/teams', { body: { teams: [] } }],
    ...trackRoutes(),
    ...obsRoutes(),
    ...authRoutes({ username: 'admin', role: 'admin', tenant: 'acme' }),
  ];
}

async function openObsTab() {
  render(<Dashboard />);
  // 사용 관측 탭은 스코프 세 축을 전부 싣는 화면이다.
  await screen.findByLabelText('실행 위치');
}

beforeEach(() => {
  location.hash = '#/usageobs';
  setPlatformFilter('');
  setRuntimeFilter('');
});

describe('실행 위치(runtime) 필터', () => {
  it('① 선택지는 정적이다 — 전체·클라우드·로컬', async () => {
    mockFetch(dashRoutes());
    await openObsTab();

    const select = await screen.findByLabelText('실행 위치');
    const values = within(select).getAllByRole('option').map((o) => (o as HTMLOptionElement).value);
    expect(values).toEqual(['', 'cloud', 'local']);
  });

  it('③ 전체가 기본이고 그때는 runtime 을 보내지 않는다', async () => {
    const { fn } = mockFetch(dashRoutes());
    await openObsTab();

    const urls = fn.mock.calls.map(([u]) => String(u));
    expect(urls.some((u) => u.startsWith('/api/usage/distribution'))).toBe(true);
    // 빈 값도 보내지 않는다 — `runtime=` 은 서버가 400 으로 거부한다.
    expect(urls.some((u) => u.includes('runtime='))).toBe(false);
  });

  it('② 고르면 스코프 조회 전부에 runtime= 이 실린다', async () => {
    const { fn } = mockFetch(dashRoutes());
    const user = userEvent.setup();
    await openObsTab();

    await user.selectOptions(await screen.findByLabelText('실행 위치'), 'local');

    /*
     * 이 화면의 카드들은 **서로 다른 표**에서 온다(세션·시간 버킷·카운터). 하나라도 안 걸면
     * 같은 화면 안에서 두 카드가 다른 모집단을 그린다. 그래서 전부를 확인한다.
     */
    await waitFor(() => {
      const urls = fn.mock.calls.map(([u]) => String(u));
      for (const ep of ['distribution', 'sessions', 'leaderboard', 'quality', 'coverage']) {
        const hit = urls.filter((u) => u.startsWith(`/api/usage/${ep}`));
        expect(hit.some((u) => u.includes('runtime=local')), `${ep} 에 runtime 이 안 실렸다`).toBe(true);
      }
    });
  });

  it('② 선택지의 원천(platforms)에는 runtime 을 싣지 않는다', async () => {
    const { fn } = mockFetch(dashRoutes());
    const user = userEvent.setup();
    await openObsTab();

    await user.selectOptions(await screen.findByLabelText('실행 위치'), 'local');
    await waitFor(() => {
      expect(fn.mock.calls.map(([u]) => String(u)).some((u) => u.includes('runtime=local'))).toBe(true);
    });

    // platforms 는 "이 서버에 어떤 플랫폼 데이터가 있나"라 스코프와 무관하다.
    const pf = fn.mock.calls.map(([u]) => String(u)).filter((u) => u.startsWith('/api/usage/platforms'));
    expect(pf.length).toBeGreaterThan(0);
    expect(pf.every((u) => !u.includes('runtime='))).toBe(true);
  });

  it('④ 걸리면 배지로 밝히고, 전체면 아무 말도 하지 않는다', async () => {
    mockFetch(dashRoutes());
    const user = userEvent.setup();
    await openObsTab();

    expect(screen.queryByText(/로컬 기준/)).toBeNull();
    await user.selectOptions(await screen.findByLabelText('실행 위치'), 'local');
    expect(await screen.findByText(/로컬 기준/)).toBeTruthy();

    await user.selectOptions(await screen.findByLabelText('실행 위치'), '');
    await waitFor(() => expect(screen.queryByText(/로컬 기준/)).toBeNull());
  });

  it('⑤ 필터 셋이 한 줄(.pf-bars)에 선다', async () => {
    mockFetch(dashRoutes());
    const { container } = render(<Dashboard />);
    /*
     * 셋을 다 기다린다. PlatformFilter·UserFilter 는 **선택지가 2개 미만이면 자기를 숨기고**,
     * 그 선택지는 비동기 응답(platforms·leaderboard)이 정한다. '실행 위치'만 기다리면 아직
     * 목록이 안 온 순간을 재게 되고, 그러면 "한 줄인가"가 아니라 "로딩 중인가"를 재는 테스트가 된다.
     */
    await screen.findByLabelText('실행 위치');
    await screen.findByLabelText('플랫폼');
    await screen.findByLabelText('사용자');

    const bars = container.querySelector('.pf-bars');
    expect(bars, '.pf-bars 컨테이너가 없다 — 필터가 줄마다 하나씩 쌓인다').toBeTruthy();
    const labels = Array.from(bars!.querySelectorAll('.pf-bar > label')).map((l) => l.textContent?.trim());
    expect(labels).toContain('플랫폼');
    expect(labels).toContain('실행 위치');
    expect(labels).toContain('사용자');
  });
});
