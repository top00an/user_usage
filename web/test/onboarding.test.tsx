import { describe, it, expect, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import Onboarding, { installCommand } from '@/components/Onboarding';
import { ToastProvider } from '@/components/Toast';
import Dashboard from '@/components/Dashboard';
import { authRoutes, mockFetch, obsRoutes, trackRoutes } from './helpers';

/*
 * 연동(온보딩) — 관리자용 인제스트 키 발급/목록/해지 + 원라인 설치 명령.
 *
 * /api/admin/* 는 아직 다른 오너가 병렬 구현 중이라 **fetch 를 목한다.** 발급(POST)과
 * 목록(GET)이 같은 경로라 응답 shape 가 다르므로, 공용 mockFetch(경로 접두사만 본다) 대신
 * **메서드까지 보는 로컬 목**을 쓴다.
 */

interface KeyItem { id: string; masked: string; createdAt: string; revokedAt: string | null }

function jsonRes(status: number, body: unknown): Promise<Response> {
  return Promise.resolve(new Response(JSON.stringify(body ?? {}), {
    status,
    headers: { 'Content-Type': 'application/json' },
  }));
}

function mockAdmin({ list, issued }: { list: KeyItem[]; issued: { key: string; id: string; createdAt: string } }) {
  const fn = vi.fn((url: string, init?: RequestInit) => {
    const method = (init?.method ?? 'GET').toUpperCase();
    if (url.startsWith('/api/admin/keys/revoke')) return jsonRes(204, {});
    if (url.startsWith('/api/admin/keys')) {
      return method === 'POST' ? jsonRes(200, issued) : jsonRes(200, { keys: list });
    }
    if (url.startsWith('/api/usage/summary')) return jsonRes(200, { totals: { machines: 3, users: 2 } });
    return jsonRes(404, { error: '없는 경로' });
  });
  vi.stubGlobal('fetch', fn);
  return fn;
}

const ISSUED = { key: 'uu_ing_SECRET123', id: 'knew', createdAt: '2026-08-08T10:00:00Z' };

function renderOnboarding() {
  return render(<ToastProvider><Onboarding /></ToastProvider>);
}

describe('원라인 설치 명령 — 동결된 형태', () => {
  it('origin·key 를 계약 형태 그대로 끼워 넣는다', () => {
    expect(installCommand('https://obs.example.com', 'uu_ing_ABC'))
      .toBe('curl -fsSL https://obs.example.com/install.sh | sh -s -- --key uu_ing_ABC --server https://obs.example.com');
  });
});

describe('연동 화면 — 발급 · 목록 · 해지', () => {
  it('발급하면 평문 key 와 원라인 설치 명령을 크게 보여주고 "지금만 표시" 경고를 단다', async () => {
    mockAdmin({ list: [], issued: ISSUED });
    const user = userEvent.setup();
    renderOnboarding();

    // 목록 로드가 끝나 발급 카드가 떠 있다(빈 목록이라 안내 카드).
    await screen.findByRole('heading', { name: '발급된 키' });
    await user.click(screen.getByRole('button', { name: '인제스트 키 발급' }));

    // 평문 key.
    expect(await screen.findByText('uu_ing_SECRET123')).toBeInTheDocument();
    // 원라인 명령 — origin(=jsdom location.origin)과 key 가 그대로 박힌 동결 문자열.
    const origin = window.location.origin;
    expect(screen.getByText(installCommand(origin, 'uu_ing_SECRET123'))).toBeInTheDocument();
    // 1회 표시 경고.
    expect(screen.getByText(/지금만 표시/)).toBeInTheDocument();
  });

  it('발급된 키 목록을 masked·상태로 그린다(활성/해지됨)', async () => {
    mockAdmin({
      list: [
        { id: 'k1', masked: 'uu_ing_…a1b2', createdAt: '2026-08-01T00:00:00Z', revokedAt: null },
        { id: 'k2', masked: 'uu_ing_…c3d4', createdAt: '2026-07-01T00:00:00Z', revokedAt: '2026-07-15T00:00:00Z' },
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
  });

  it('해지는 두 단계 확인을 거쳐 id 를 실어 POST 한다', async () => {
    const fn = mockAdmin({
      list: [{ id: 'k1', masked: 'uu_ing_…a1b2', createdAt: '2026-08-01T00:00:00Z', revokedAt: null }],
      issued: ISSUED,
    });
    const user = userEvent.setup();
    renderOnboarding();

    await user.click(await screen.findByRole('button', { name: 'uu_ing_…a1b2 해지' }));
    await user.click(await screen.findByRole('button', { name: 'uu_ing_…a1b2 해지 확정' }));

    await waitFor(() => {
      const call = fn.mock.calls.find(([u]) => String(u).startsWith('/api/admin/keys/revoke'));
      expect(call).toBeTruthy();
      const init = call![1] as RequestInit;
      expect(init.method).toBe('POST');
      expect(JSON.parse(init.body as string)).toEqual({ id: 'k1' });
    });
  });
});

describe('셸 — 연동 탭은 관리자에게만 보인다', () => {
  it('member 에게는 연동 탭이 노출되지 않는다', async () => {
    mockFetch([
      ...authRoutes({ username: 'bob', role: 'member', tenant: 'acme' }),
      ...trackRoutes(),
      ...obsRoutes(),
    ]);
    render(<Dashboard />);
    await screen.findByRole('tab', { name: '대시보드' });
    expect(screen.queryByRole('tab', { name: '연동' })).not.toBeInTheDocument();
  });

  it('admin 에게는 연동 탭이 보인다', async () => {
    mockFetch([
      ...authRoutes(),
      ...trackRoutes(),
      ...obsRoutes(),
      ['/api/admin/keys', { body: { keys: [] } }],
    ]);
    render(<Dashboard />);
    expect(await screen.findByRole('tab', { name: '연동' })).toBeInTheDocument();
  });
});
