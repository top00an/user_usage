import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import Login from '@/components/Login';
import { mockFetch } from './helpers';

const USER = { username: 'admin', role: 'admin', tenant: 'acme' };

beforeEach(() => {
  location.hash = '';
});

describe('Login — ID/PW 세션 로그인', () => {
  it('아이디/비밀번호 입력 후 성공하면 onSuccess 에 user 를 넘긴다', async () => {
    mockFetch([['/api/auth/login', { body: { ok: true, user: USER } }]]);
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    render(<Login onSuccess={onSuccess} />);

    await user.type(screen.getByLabelText('아이디'), 'admin');
    await user.type(screen.getByLabelText('비밀번호'), 'pw-123456');
    await user.click(screen.getByRole('button', { name: '로그인' }));

    await waitFor(() => expect(onSuccess).toHaveBeenCalledWith(USER));
  });

  it('엔터로도 제출된다', async () => {
    mockFetch([['/api/auth/login', { body: { ok: true, user: USER } }]]);
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    render(<Login onSuccess={onSuccess} />);

    await user.type(screen.getByLabelText('아이디'), 'admin');
    await user.type(screen.getByLabelText('비밀번호'), 'pw-123456{Enter}');

    await waitFor(() => expect(onSuccess).toHaveBeenCalledWith(USER));
  });

  it('자격증명이 틀리면(401) 안내를 띄우고 onSuccess 를 부르지 않는다', async () => {
    mockFetch([['/api/auth/login', { status: 401, body: { ok: false, error: '아무 문장' } }]]);
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    render(<Login onSuccess={onSuccess} />);

    await user.type(screen.getByLabelText('아이디'), 'admin');
    await user.type(screen.getByLabelText('비밀번호'), 'wrong');
    await user.click(screen.getByRole('button', { name: '로그인' }));

    expect(await screen.findByText('아이디 또는 비밀번호가 올바르지 않습니다')).toBeInTheDocument();
    expect(onSuccess).not.toHaveBeenCalled();
    // 접근성: 오류는 입력과 연결되고 aria-invalid 가 선다.
    expect(screen.getByLabelText('아이디')).toHaveAttribute('aria-invalid', 'true');
  });

  it('네트워크·5xx 는 401 과 다른 문구로 안내한다', async () => {
    mockFetch([['/api/auth/login', { status: 500, body: { error: '터졌다' } }]]);
    const user = userEvent.setup();
    render(<Login onSuccess={vi.fn()} />);

    await user.type(screen.getByLabelText('아이디'), 'admin');
    await user.type(screen.getByLabelText('비밀번호'), 'pw-123456');
    await user.click(screen.getByRole('button', { name: '로그인' }));

    expect(await screen.findByText('로그인하지 못했습니다. 잠시 후 다시 시도하세요.')).toBeInTheDocument();
  });

  it('빈 제출은 조용히 무시되지 않는다', async () => {
    mockFetch([['/api/auth/login', { body: { ok: true, user: USER } }]]);
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    render(<Login onSuccess={onSuccess} />);

    await user.click(screen.getByRole('button', { name: '로그인' }));
    expect(await screen.findByText('아이디와 비밀번호를 입력하세요.')).toBeInTheDocument();
    expect(onSuccess).not.toHaveBeenCalled();
  });

  it('제출 중에는 버튼과 입력을 비활성화한다 (이중 제출 방지)', async () => {
    const { fn } = mockFetch([['/api/auth/login', { body: { ok: true, user: USER }, delay: 300 }]]);
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    render(<Login onSuccess={onSuccess} />);

    await user.type(screen.getByLabelText('아이디'), 'admin');
    await user.type(screen.getByLabelText('비밀번호'), 'pw-123456');
    await user.click(screen.getByRole('button', { name: '로그인' }));

    // 응답 오기 전: 버튼이 '로그인 중…' 으로 비활성.
    const busy = await screen.findByRole('button', { name: '로그인 중…' });
    expect(busy).toBeDisabled();
    expect(screen.getByLabelText('아이디')).toBeDisabled();

    // 이 사이 다시 눌러도 두 번째 요청은 나가지 않는다.
    await user.click(busy);

    await waitFor(() => expect(onSuccess).toHaveBeenCalledWith(USER));
    const loginCalls = fn.mock.calls.filter(([u]) => String(u).includes('/api/auth/login'));
    expect(loginCalls.length).toBe(1);
  });
});
