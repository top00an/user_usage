import { describe, it, expect, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import Onboarding, { installCommand } from '@/components/Onboarding';
import { ToastProvider } from '@/components/Toast';
import Dashboard from '@/components/Dashboard';
import { authRoutes, mockFetch, obsRoutes, trackRoutes } from './helpers';

/*
 * 연동(온보딩) — **셀프서비스**다(동결 ②). 관리자 전용이 아니다.
 * 내 키 발급/목록/해지 + 원라인 설치 명령. 남의 키는 서버가 목록에 담지 않는다.
 *
 * `/api/me/keys` 는 발급(POST)과 목록(GET)이 같은 경로라 응답 shape 가 다르므로, 공용
 * mockFetch(경로 접두사만 본다) 대신 **메서드까지 보는 로컬 목**을 쓴다.
 */

interface KeyItem { id: string; masked: string; createdAt: string; revokedAt: string | null; username: string | null }

function jsonRes(status: number, body: unknown): Promise<Response> {
  return Promise.resolve(new Response(status === 204 ? null : JSON.stringify(body ?? {}), {
    status,
    headers: { 'Content-Type': 'application/json' },
  }));
}

function mockMe({ list, issued }: { list: KeyItem[]; issued: { key: string; id: string; createdAt: string; username: string } }) {
  const fn = vi.fn((url: string, init?: RequestInit) => {
    const path = String(url).split('?')[0]!;
    const method = (init?.method ?? 'GET').toUpperCase();
    if (path === '/api/me/keys/revoke') return jsonRes(204, null);
    if (path === '/api/me/keys') {
      return method === 'POST' ? jsonRes(200, issued) : jsonRes(200, { keys: list });
    }
    return jsonRes(404, { error: '없는 경로' });
  });
  vi.stubGlobal('fetch', fn);
  return fn;
}

const ISSUED = { key: 'uu_ing_SECRET123', id: 'knew', createdAt: '2026-08-08T10:00:00Z', username: 'alice' };

function renderOnboarding(self = 'alice') {
  return render(<ToastProvider><Onboarding self={self} /></ToastProvider>);
}

describe('원라인 설치 명령 — 동결된 형태', () => {
  it('origin·key 를 계약 형태 그대로 끼워 넣는다', () => {
    expect(installCommand('https://obs.example.com', 'uu_ing_ABC'))
      .toBe('curl -fsSL https://obs.example.com/install.sh | sh -s -- --key uu_ing_ABC --server https://obs.example.com');
  });
});

describe('연동 화면 — 셀프서비스 발급 · 목록 · 해지', () => {
  it('내 키를 /api/me/keys 로 발급한다 — 남의 이름을 넣을 자리가 없다', async () => {
    const fn = mockMe({ list: [], issued: ISSUED });
    const user = userEvent.setup();
    renderOnboarding();

    await screen.findByRole('heading', { name: '내 키' });
    await user.click(screen.getByRole('button', { name: '내 키 발급' }));

    await waitFor(() => {
      const call = fn.mock.calls.find(([u], i) => String(u) === '/api/me/keys' && (fn.mock.calls[i]![1] as RequestInit)?.method === 'POST');
      expect(call).toBeTruthy();
      // 본문에 username 을 싣지 않는다 — 소유자는 서버가 요청자로 고정한다.
      expect((call![1] as RequestInit).body).toBeUndefined();
    });
    // 관리자 경로를 밟지 않았다.
    expect(fn.mock.calls.some(([u]) => String(u).startsWith('/api/admin/'))).toBe(false);
  });

  it('발급하면 평문 key 와 원라인 설치 명령을 모달로 보여주고 "지금만 표시" 를 제목에 단다', async () => {
    mockMe({ list: [], issued: ISSUED });
    const user = userEvent.setup();
    renderOnboarding();

    await screen.findByRole('heading', { name: '내 키' });
    await user.click(screen.getByRole('button', { name: '내 키 발급' }));

    /*
     * 카드가 아니라 모달이다 — 카드는 스크롤로 지나칠 수 있고 "확인했어요 · 닫기"는 언제든
     * 다시 열 수 있는 알림처럼 읽힌다. 다시 볼 수 있게 생기면 사람은 나중에 찾는다.
     */
    const dialog = within(await screen.findByRole('dialog', { name: '새 인제스트 키 — 지금만 표시됩니다' }));
    expect(dialog.getByText('uu_ing_SECRET123')).toBeInTheDocument();
    const origin = window.location.origin;
    expect(dialog.getByText(installCommand(origin, 'uu_ing_SECRET123'))).toBeInTheDocument();
    expect(dialog.getByText(/이 창을 닫으면 평문 키를 다시 볼 수 없습니다/)).toBeInTheDocument();
  });

  it('내 키 목록을 masked·상태로 그린다(활성/해지됨)', async () => {
    mockMe({
      list: [
        { id: 'k1', masked: 'uu_ing_…a1b2', createdAt: '2026-08-01T00:00:00Z', revokedAt: null, username: 'alice' },
        { id: 'k2', masked: 'uu_ing_…c3d4', createdAt: '2026-07-01T00:00:00Z', revokedAt: '2026-07-15T00:00:00Z', username: 'alice' },
      ],
      issued: ISSUED,
    });
    renderOnboarding();

    expect(await screen.findByText('uu_ing_…a1b2')).toBeInTheDocument();
    expect(screen.getByText('uu_ing_…c3d4')).toBeInTheDocument();
    expect(screen.getByText('활성')).toBeInTheDocument();
    expect(screen.getByText('해지됨')).toBeInTheDocument();
    // 해지된 키에는 해지 버튼이 없다.
    expect(screen.queryByRole('button', { name: 'uu_ing_…c3d4 해지' })).not.toBeInTheDocument();
    // 남의 키가 여기 없다는 사실을 화면이 말한다.
    expect(screen.getByText(/다른 사람의 키는 서버가 이 목록에 담지 않습니다/)).toBeInTheDocument();
  });

  it('해지는 두 단계 확인을 거쳐 /api/me/keys/revoke 로 id 를 실어 POST 한다', async () => {
    const fn = mockMe({
      list: [{ id: 'k1', masked: 'uu_ing_…a1b2', createdAt: '2026-08-01T00:00:00Z', revokedAt: null, username: 'alice' }],
      issued: ISSUED,
    });
    const user = userEvent.setup();
    renderOnboarding();

    await user.click(await screen.findByRole('button', { name: 'uu_ing_…a1b2 해지' }));
    // 무엇을 잃는지 말한 뒤에만 보낸다.
    expect(screen.getByText(/새 키를 발급해 그 머신에 다시 설치해야 합니다/)).toBeInTheDocument();
    await user.click(await screen.findByRole('button', { name: 'uu_ing_…a1b2 해지 확정' }));

    await waitFor(() => {
      const call = fn.mock.calls.find(([u]) => String(u) === '/api/me/keys/revoke');
      expect(call).toBeTruthy();
      const init = call![1] as RequestInit;
      expect(init.method).toBe('POST');
      expect(JSON.parse(init.body as string)).toEqual({ id: 'k1' });
    });
  });

  it('이 키가 누구 것으로 잡히는지 말한다 (이 화면에서 가장 중요한 두 줄)', async () => {
    mockMe({ list: [], issued: ISSUED });
    renderOnboarding('alice');
    expect(await screen.findByText(/이 키로 들어온 사용량은/)).toHaveTextContent('alice');
    expect(screen.getByText(/다른 사람의 키는 여기 보이지 않습니다/)).toBeInTheDocument();
  });

  it('member 에게는 관리 탭 안내를 깔지 않는다 (그 탭이 그에게 없다)', async () => {
    mockMe({ list: [], issued: ISSUED });
    renderOnboarding('bob');
    await screen.findByRole('heading', { name: '내 키' });
    expect(screen.queryByText('전체 키 현황은 관리 탭에 있습니다.')).not.toBeInTheDocument();
  });
});

/*
 * ── 키 스코프 표기 ───────────────────────────────────────────────────────
 *
 * 운영자는 이 키를 개발자 PC 에 심는다. 화면이 스코프를 말하지 않으면 사람은 **가장 넓은 쪽으로
 * 가정한다** — "이걸로 대시보드도 보이겠지". 그 가정 위에서 키가 아껴지거나 방치된다.
 *
 * 아래 문구는 전부 서버 코드에서 확인된 사실이다(server.go 인테이크 게이트 · agent.go 콜렉터 ·
 * org.go 의 revoked_at 조건과 sha256 저장 · usage.go 의 귀속 우선순위). 문구가 사라지면 이
 * 테스트가 먼저 붉어진다.
 */
describe('연동 화면 — 인제스트 키 스코프 표기', () => {
  it('키 목록 카드가 이 키로 되는 일과 안 되는 일을 명시한다', async () => {
    mockMe({ list: [], issued: ISSUED });
    renderOnboarding();

    const scope = within(await screen.findByRole('list', { name: '인제스트 키 스코프' }));
    // 열리는 것 둘.
    expect(scope.getByText('POST /api/usage')).toBeInTheDocument();
    expect(scope.getByText('GET /api/agent/collector')).toBeInTheDocument();
    // 안 열리는 것 — 열람은 403.
    expect(scope.getByText('대시보드 열람 불가')).toBeInTheDocument();
    expect(scope.getByText(/403/)).toBeInTheDocument();
    // 해지는 즉시 401.
    expect(scope.getByText(/바로 401/)).toBeInTheDocument();
    // 평문은 1회 · 서버는 해시만.
    expect(scope.getByText(/sha256 해시만/)).toBeInTheDocument();
    // 복제되는 자격이라는 사실 + 대응.
    expect(scope.getByText('팀원 PC 마다 복제되는 자격')).toBeInTheDocument();
    expect(scope.getByText(/해지하고 재발급/)).toBeInTheDocument();
  });

  it('귀속이 사실로 올라와 있다 — 이 키는 나에게 묶이고 PC 이름을 이긴다', async () => {
    mockMe({ list: [], issued: ISSUED });
    renderOnboarding();
    const scope = within(await screen.findByRole('list', { name: '인제스트 키 스코프' }));
    expect(scope.getByText('이 키는 나에게 묶입니다')).toBeInTheDocument();
    expect(scope.getByText(/PC 가 보내는 이름보다 우선합니다/)).toBeInTheDocument();
  });

  it('상태는 색이 아니라 글자로 말한다 (허용 · 차단 · 주의)', async () => {
    mockMe({ list: [], issued: ISSUED });
    renderOnboarding();

    const scope = within(await screen.findByRole('list', { name: '인제스트 키 스코프' }));
    expect(scope.getAllByText('허용')).toHaveLength(2);
    expect(scope.getByText('차단')).toBeInTheDocument();
    expect(scope.getAllByText('주의')).toHaveLength(1);
    expect(scope.getByText('귀속')).toBeInTheDocument();
  });

  it('발급 모달에도 같은 스코프가 붙는다 (전달 직전이 가장 필요한 자리)', async () => {
    mockMe({ list: [], issued: ISSUED });
    const user = userEvent.setup();
    renderOnboarding();

    await screen.findByRole('heading', { name: '내 키' });
    await user.click(screen.getByRole('button', { name: '내 키 발급' }));

    // 두 목록은 이름이 달라야 한다 — 같은 이름이면 스크린리더에서 어느 쪽인지 구분되지 않는다.
    const issuedScope = within(await screen.findByRole('list', { name: '발급된 키 스코프' }));
    expect(issuedScope.getByText('POST /api/usage')).toBeInTheDocument();
    expect(issuedScope.getByText('대시보드 열람 불가')).toBeInTheDocument();
    expect(screen.getByRole('list', { name: '인제스트 키 스코프' })).toBeInTheDocument();
  });
});

describe('셸 — 연동 탭은 모든 로그인 사용자의 것이다', () => {
  it('member 에게도 연동 탭이 보인다 (동결 ② — 셀프서비스)', async () => {
    mockFetch([
      ...authRoutes({ username: 'bob', role: 'member', tenant: 'acme' }),
      ...trackRoutes(),
      ...obsRoutes(),
    ]);
    render(<Dashboard />);
    await screen.findByRole('tab', { name: '대시보드' });
    expect(screen.getByRole('tab', { name: '연동' })).toBeInTheDocument();
  });

  it('member 가 딥링크로 연동 탭을 열면 자기 키 화면이 그려진다', async () => {
    location.hash = '#/onboarding';
    mockFetch([
      ...authRoutes({ username: 'bob', role: 'member', tenant: 'acme' }),
      ['/api/me/keys', { body: { keys: [] } }],
      ...trackRoutes(),
      ...obsRoutes(),
    ]);
    render(<Dashboard />);
    expect(await screen.findByRole('heading', { name: '내 인제스트 키' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '내 키 발급' })).toBeInTheDocument();
    location.hash = '';
  });

  it('admin 에게도 이 탭에는 자기 키만 나온다 — 전체는 관리 탭 한 곳뿐이다', async () => {
    location.hash = '#/onboarding';
    mockFetch([
      ...authRoutes(),
      ['/api/me/keys', { body: { keys: [] } }],
      ...trackRoutes(),
      ...obsRoutes(),
    ]);
    render(<Dashboard />);
    await screen.findByRole('heading', { name: '내 인제스트 키' });
    expect(screen.getByText('전체 키 현황은 관리 탭에 있습니다.')).toBeInTheDocument();
    // 전체 키 표를 복제하지 않는다.
    expect(screen.queryByRole('heading', { name: '인제스트 키 현황' })).not.toBeInTheDocument();
    location.hash = '';
  });
});
