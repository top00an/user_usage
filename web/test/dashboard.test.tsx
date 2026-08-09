import { describe, it, expect, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import Dashboard from '@/components/Dashboard';
import { readToken, writeToken, clearToken } from '@/lib/token';
import { golden, mockFetch, obsRoutes, trackRoutes } from './helpers';

const TOKEN = 'test-admin-token-0123456789';

function allRoutes(extra: Parameters<typeof mockFetch>[0] = []) {
  return [...extra, ...trackRoutes(), ...obsRoutes()];
}

beforeEach(() => {
  clearToken();
  location.hash = '';
});

describe('셸 — 게이트 · 탭 · 401 복구', () => {
  it('토큰이 없으면 게이트를 그린다', async () => {
    mockFetch(allRoutes());
    render(<Dashboard />);
    expect(await screen.findByLabelText('사용량 대시보드 토큰')).toBeInTheDocument();
  });

  it('토큰을 넣으면 쿠키에 담고 사용 추적을 그린다', async () => {
    mockFetch(allRoutes());
    const user = userEvent.setup();
    render(<Dashboard />);

    await user.type(await screen.findByLabelText('사용량 대시보드 토큰'), TOKEN);
    await user.click(screen.getByRole('button', { name: '열기' }));

    expect(readToken()).toBe(TOKEN);
    expect(await screen.findByRole('heading', { name: '사용자별' })).toBeInTheDocument();
  });

  it('빈 토큰 제출은 조용히 무시되지 않는다', async () => {
    mockFetch(allRoutes());
    const user = userEvent.setup();
    render(<Dashboard />);
    await user.click(await screen.findByRole('button', { name: '열기' }));
    expect(await screen.findByText('토큰을 입력하세요.')).toBeInTheDocument();
  });

  /*
   * 401 복구. 서버를 다시 띄우면 토큰이 바뀔 수 있고, 그때 화면은 빈 카드만 남는다 —
   * 무엇이 잘못됐는지 말해 주고 다시 넣을 자리를 준다.
   */
  it('401 이면 쿠키를 지우고 게이트로 되돌아온다', async () => {
    writeToken('stale-token');
    mockFetch([['/api/usage', { status: 401, body: { error: 'unauthorized' } }]]);
    render(<Dashboard />);

    expect(await screen.findByText('토큰이 올바르지 않거나 만료되었습니다. 다시 입력하세요.')).toBeInTheDocument();
    expect(readToken()).toBe('');
  });

  it('403 은 게이트로 튕기지 않고 권한 안내를 보여준다 (문구가 아니라 status 로 분기)', async () => {
    writeToken(TOKEN);
    // 문구는 403 과 무관한 아무 글자다 — 그래도 권한 안내가 나와야 한다.
    mockFetch([['/api/usage', { status: 403, body: { error: '설명이 언제든 바뀔 수 있는 문장' } }]]);
    render(<Dashboard />);

    expect(await screen.findByRole('heading', { name: '권한이 필요합니다' })).toBeInTheDocument();
    expect(readToken()).toBe(TOKEN);   // 게이트로 튕기지 않았다
  });

  it('5xx 는 연결 실패로 안내하고 다시 시도를 준다', async () => {
    writeToken(TOKEN);
    mockFetch([['/api/usage', { status: 503, body: { error: '설명' } }]]);
    render(<Dashboard />);
    expect(await screen.findByRole('heading', { name: '서버가 응답하지 못했습니다' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '다시 시도' })).toBeInTheDocument();
  });

  it('토큰 지우기는 쿠키를 지우고 게이트로 돌아간다', async () => {
    writeToken(TOKEN);
    mockFetch(allRoutes());
    const user = userEvent.setup();
    render(<Dashboard />);

    await screen.findByRole('heading', { name: '사용자별' });
    await user.click(screen.getByRole('button', { name: '토큰 지우기' }));

    expect(await screen.findByText('토큰을 지웠습니다.')).toBeInTheDocument();
    expect(readToken()).toBe('');
  });

  it('해시 딥링크로 관측 탭을 바로 연다', async () => {
    writeToken(TOKEN);
    location.hash = '#/usageobs';
    mockFetch(allRoutes());
    render(<Dashboard />);
    expect(await screen.findByRole('heading', { name: 'API 환산액' })).toBeInTheDocument();
  });

  /*
   * ── 탭을 빠르게 오갈 때 앞 탭 응답이 현재 탭을 덮지 않는다 ─────────────
   * 없으면 화면이 틀린 값을 보여주는데 아무 에러도 안 난다. 조용한 오표시가 가장 비싸다.
   */
  it('탭을 빠르게 오가면 앞 탭의 늦은 응답을 버린다', async () => {
    writeToken(TOKEN);
    const { fn } = mockFetch([
      // 추적 탭의 summary 만 아주 늦게 온다
      ['/api/usage/summary', { body: golden('summary'), delay: 500 }],
      ...trackRoutes(),
      ...obsRoutes(),
    ]);
    const user = userEvent.setup();
    render(<Dashboard />);

    // 추적 탭에서 시작 → 응답 오기 전에 관측으로 이동
    await screen.findByRole('tab', { name: '사용 추적' });
    await user.click(screen.getByRole('tab', { name: '사용 관측' }));
    await screen.findByRole('heading', { name: 'API 환산액' });

    // 늦은 추적 응답이 도착하고도 남을 시간
    await new Promise((r) => setTimeout(r, 700));

    expect(screen.getByRole('heading', { name: 'API 환산액' })).toBeInTheDocument();
    expect(screen.queryByText('보고된 세션')).not.toBeInTheDocument();
    expect(screen.getByRole('tab', { name: '사용 관측' })).toHaveAttribute('aria-selected', 'true');

    // 그리고 요청 자체가 끊겼는지 — 화면만 안 덮은 것이 아니라 네트워크도 정리돼야 한다.
    const summaryCall = fn.mock.calls.find(([u]) => String(u).includes('/summary'));
    expect(summaryCall).toBeTruthy();
  });

  it('탭은 키보드 상하 화살표로도 옮겨진다(세로 내비)', async () => {
    writeToken(TOKEN);
    mockFetch(allRoutes());
    const user = userEvent.setup();
    render(<Dashboard />);

    // 좌측 세로 내비 레일이라 방향키는 위/아래다(WAI-ARIA vertical tablist).
    const first = await screen.findByRole('tab', { name: '사용 추적' });
    first.focus();
    await user.keyboard('{ArrowDown}');
    await waitFor(() => expect(screen.getByRole('tab', { name: '사용 관측' })).toHaveAttribute('aria-selected', 'true'));
    await user.keyboard('{ArrowUp}');
    await waitFor(() => expect(screen.getByRole('tab', { name: '사용 추적' })).toHaveAttribute('aria-selected', 'true'));
  });
});

describe('사용 추적 — ④ 근사값을 정확한 값으로 위장하지 않는다', () => {
  beforeEach(() => {
    writeToken(TOKEN);
    mockFetch(allRoutes());
  });

  it('모델별 표에 근거 열이 있고 정확/근사를 갈라 말한다', async () => {
    render(<Dashboard />);
    const table = (await screen.findByRole('heading', { name: '모델별' })).closest('section')!;
    expect(within(table).getByRole('columnheader', { name: '근거' })).toBeInTheDocument();
    expect(within(table).getByText('series — 모델별 정확')).toBeInTheDocument();
    expect(within(table).getAllByText(/세션 최빈 기준/).length).toBeGreaterThan(0);
  });

  it('사용자별 series 커버리지를 낸다', async () => {
    render(<Dashboard />);
    expect(await screen.findByText('사용자별 series 커버리지')).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: /series 있음/ })).toBeInTheDocument();
    // 커버리지가 낮은 이유는 서버가 아는 데까지만 말한다
    expect(screen.getByText(/시각 없는 턴/)).toBeInTheDocument();
    expect(screen.getAllByText('알 수 없음(구버전 수집기)').length).toBeGreaterThan(0);
  });

  it('키워드 보존 기한이 없으면 "무기한"이라고 말한다', async () => {
    // golden 의 retention.keywordDays 는 null 이다 — 침묵하지 않고 그 사실을 말해야 한다.
    render(<Dashboard />);
    expect(await screen.findByText(/무기한 보관됩니다/)).toBeInTheDocument();
  });

  it('보존 기한이 있으면 일수를 말한다', async () => {
    const s = golden<Record<string, unknown>>('summary');
    mockFetch([
      ['/api/usage/summary', { body: { ...s, retention: { keywordDays: 90 } } }],
      ...trackRoutes(),
      ...obsRoutes(),
    ]);
    render(<Dashboard />);
    expect(await screen.findByText(/90일/)).toBeInTheDocument();
  });
});

describe('사용 관측 — ④ 나머지 항목', () => {
  beforeEach(() => {
    writeToken(TOKEN);
    location.hash = '#/usageobs';
    mockFetch(allRoutes());
  });

  it('단가 미등록 모델의 이름을 남긴다 (조용한 $0 금지)', async () => {
    render(<Dashboard />);
    await screen.findByRole('heading', { name: 'API 환산액' });
    expect(screen.getByText(/단가 미등록 모델/)).toBeInTheDocument();
    // 상위 세션 표에도 같은 이름이 나온다 — 여기서 보는 것은 "환산액 카드가 이름을 남기는가"다.
    expect(screen.getAllByText('some-unreleased-model-x').length).toBeGreaterThan(0);
  });

  it('TTL 미상 행 수와 과소 추정 사실을 말한다', async () => {
    render(<Dashboard />);
    await screen.findByRole('heading', { name: 'API 환산액' });
    expect(screen.getByText(/TTL 미상 5행/)).toBeInTheDocument();
    expect(screen.getByText(/최대 1.6배/)).toBeInTheDocument();
  });

  it('수집 커버리지(발신처별 마지막 보고)를 낸다', async () => {
    render(<Dashboard />);
    expect(await screen.findByRole('heading', { name: '수집 상태' })).toBeInTheDocument();
    expect(screen.getByText('host-a')).toBeInTheDocument();
  });

  it('곁가지 조회가 실패해도 비용 카드는 남는다 (fail-soft)', async () => {
    mockFetch([
      ['/api/usage/leaderboard', { status: 500, body: { error: '터졌다' } }],
      ['/api/usage/quality', { status: 500, body: { error: '터졌다' } }],
      ['/api/usage/coverage', { status: 500, body: { error: '터졌다' } }],
      ...obsRoutes(),
      ...trackRoutes(),
    ]);
    render(<Dashboard />);
    expect(await screen.findByRole('heading', { name: 'API 환산액' })).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: '수집 상태' })).not.toBeInTheDocument();
  });

  it('세션 상세는 행을 키보드로도 열 수 있다', async () => {
    const user = userEvent.setup();
    render(<Dashboard />);
    await screen.findByRole('heading', { name: /상위 세션/ });
    const rows = screen.getAllByRole('button', { expanded: false });
    rows[0]!.focus();
    await user.keyboard('{Enter}');
    expect(await screen.findByText(/keyword 축은 세션 단위로 제공하지 않는다/)).toBeInTheDocument();
  });

  it('사용자 상세 모달이 열리고 Escape 로 닫힌다', async () => {
    const user = userEvent.setup();
    render(<Dashboard />);
    await screen.findByRole('heading', { name: '사용자별' });
    await user.click(screen.getByRole('button', { name: 'carol 사용 상세 열기' }));

    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getAllByText(/최근 7일/).length).toBeGreaterThan(0);
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
  });
});

describe('XSS — 서버 값을 마크업으로 해석하지 않는다', () => {
  it('사용자 이름에 태그가 들어와도 텍스트로 남는다', async () => {
    writeToken(TOKEN);
    const s = golden<{ byUser: unknown[] }>('summary');
    const evil = '<img src=x onerror="document.title=\'xss\'">';
    mockFetch([
      ['/api/usage/summary', {
        body: {
          ...s,
          byUser: [{ username: evil, input: 1, output: 1, cacheRead: 1, cacheCreate: 1, turns: 1, sessions: 1 }],
        },
      }],
      ...trackRoutes(),
      ...obsRoutes(),
    ]);
    render(<Dashboard />);
    expect(await screen.findByText(evil)).toBeInTheDocument();
    expect(document.querySelector('img')).toBeNull();
    expect(document.title).not.toBe('xss');
  });
});
