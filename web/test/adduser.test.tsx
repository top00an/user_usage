import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import AddUserForm from '@/components/admin/AddUserForm';
import { ToastProvider } from '@/components/Toast';
import type { AdminUser } from '@/lib/types';

/*
 * ── 사용자 추가 폼 ────────────────────────────────────────────────────────
 *
 * 계정 생성은 **되돌리기가 비싼 조작**이다(지우면 세션·인제스트 키까지 함께 거둬진다).
 * 그래서 이 파일의 단정은 "그려지는가"가 아니라 **"잘못 만드는 것을 무엇이 막는가"** 다:
 *
 *   ① 서버 왕복 없이 막을 수 있는 것은 누르기 전에 막는다(짧은 비밀번호 · 중복 아이디 · 오타).
 *   ② 비밀번호 확인이 있어야 오타가 그 사람의 로그인 실패로 나타나지 않는다 — 관리자가 만든
 *      계정이라 본인은 초기 비밀번호를 확인할 방법이 없다.
 *   ③ 계정은 만들어졌는데 팀 배정만 실패하는 경우를 **나눠 말한다**(다른 엔드포인트다).
 *      "실패"라고만 하면 관리자는 같은 아이디로 다시 만들려 하고 중복 오류를 만난다.
 */

const USERS: AdminUser[] = [
  { username: 'ops-admin', role: 'admin', createdAt: '2026-06-01T00:00:00Z', team: '플랫폼' },
  { username: 'alice', role: 'member', createdAt: '2026-07-02T00:00:00Z', team: null },
];

type Reply = { status: number; body?: unknown };

function res(status: number, body: unknown): Promise<Response> {
  return Promise.resolve(new Response(JSON.stringify(body ?? {}), {
    status, headers: { 'Content-Type': 'application/json' },
  }));
}

/** POST 응답을 경로별로 심는다(생성 성공 / 팀 배정 실패 같은 사고를 만든다). */
function mockApi(post: Record<string, Reply> = {}) {
  const fn = vi.fn((url: string, init?: RequestInit) => {
    const path = String(url).split('?')[0]!;
    const method = (init?.method ?? 'GET').toUpperCase();
    if (method === 'POST' && post[path]) return res(post[path]!.status, post[path]!.body);
    if (method === 'POST') return res(200, { ok: true, user: USERS[1] });
    return res(404, { error: '없는 경로' });
  });
  vi.stubGlobal('fetch', fn);
  return fn;
}

/** 그 경로로 나간 POST 들의 본문. */
function posts(fn: ReturnType<typeof mockApi>, path: string) {
  return fn.mock.calls
    .filter(([url, init]) => String(url).split('?')[0] === path && (init?.method ?? 'GET') === 'POST')
    .map(([, init]) => JSON.parse(String(init?.body ?? '{}')));
}

const onCreated = vi.fn();
const onCancel = vi.fn();

function mount() {
  render(
    <ToastProvider>
      <AddUserForm users={USERS} onCancel={onCancel} onCreated={onCreated} />
    </ToastProvider>,
  );
  return userEvent.setup();
}

/* 라벨 문구로 정확히 집는다 — 부분 일치로 잡으면 "비밀번호"가 "비밀번호 확인"까지 물어 온다. */
const nameBox = () => screen.getByLabelText('아이디 *');
const pwBox = () => screen.getByLabelText('비밀번호 *');
const confirmBox = () => screen.getByLabelText('비밀번호 확인 *');
const teamBox = () => screen.getByLabelText('팀 (선택)');
const submit = () => screen.getByRole('button', { name: '사용자 만들기' });

beforeEach(() => {
  onCreated.mockClear();
  onCancel.mockClear();
});

describe('누르기 전에 막는다 (서버 왕복 없이)', () => {
  it('빈 폼으로 제출하면 칸마다 이유를 말하고 아무것도 보내지 않는다', async () => {
    const fn = mockApi();
    const user = mount();
    await user.click(submit());

    expect(screen.getByText('아이디를 입력하세요.')).toBeInTheDocument();
    expect(screen.getByText('비밀번호를 입력하세요.')).toBeInTheDocument();
    expect(screen.getByText('비밀번호를 한 번 더 입력하세요.')).toBeInTheDocument();
    expect(posts(fn, '/api/admin/users')).toHaveLength(0);
  });

  it('8자 미만 비밀번호는 서버에 묻지 않는다 — 규칙을 미리 말한다', async () => {
    const fn = mockApi();
    const user = mount();
    await user.type(nameBox(), 'newbie');
    await user.type(confirmBox(), 'short1');
    await user.type(pwBox(), 'short1');
    await user.click(submit());

    expect(screen.getByText('비밀번호는 최소 8자여야 합니다.')).toBeInTheDocument();
    expect(posts(fn, '/api/admin/users')).toHaveLength(0);
  });

  it('비밀번호가 서로 다르면 막는다 — 오타가 그 사람의 로그인 실패로 나타나지 않게', async () => {
    const fn = mockApi();
    const user = mount();
    await user.type(nameBox(), 'newbie');
    await user.type(pwBox(), 'correct-horse');
    await user.type(confirmBox(), 'correct-hores');
    await user.click(submit());

    expect(screen.getByText('비밀번호가 서로 다릅니다.')).toBeInTheDocument();
    expect(posts(fn, '/api/admin/users')).toHaveLength(0);
  });

  it('이미 있는 아이디는 화면이 아는 사실이다 — 서버에 물어보지 않는다', async () => {
    const fn = mockApi();
    const user = mount();
    await user.type(nameBox(), 'Alice');   // 대소문자가 달라도 같은 사람이다
    await user.type(pwBox(), 'correct-horse');
    await user.type(confirmBox(), 'correct-horse');
    await user.click(submit());

    expect(screen.getByText('이미 있는 아이디입니다.')).toBeInTheDocument();
    expect(posts(fn, '/api/admin/users')).toHaveLength(0);
  });

  it('아이디에 공백은 넣을 수 없다', async () => {
    mockApi();
    const user = mount();
    await user.type(nameBox(), 'new bie');
    await user.type(pwBox(), 'correct-horse');
    await user.type(confirmBox(), 'correct-horse');
    await user.click(submit());
    expect(screen.getByText('아이디에 공백을 넣을 수 없습니다.')).toBeInTheDocument();
  });

  it('타이핑하는 동안에는 훈계하지 않는다 (제출 전에는 빨간 글씨가 없다)', async () => {
    mockApi();
    const user = mount();
    await user.type(pwBox(), 'ab');
    expect(screen.queryByText('비밀번호는 최소 8자여야 합니다.')).not.toBeInTheDocument();
    // 대신 권고 등급으로만 말한다.
    expect(screen.getByText('너무 짧습니다')).toBeInTheDocument();
  });

  it('확인이 일치하면 그 자리에서 알려 준다', async () => {
    mockApi();
    const user = mount();
    await user.type(pwBox(), 'correct-horse');
    await user.type(confirmBox(), 'correct-horse');
    expect(screen.getByText('일치합니다.')).toBeInTheDocument();
  });
});

describe('만들기', () => {
  it('아이디·비밀번호·역할을 그대로 보내고, 성공하면 호출부에 알린다', async () => {
    const fn = mockApi();
    const user = mount();
    await user.type(nameBox(), '  newbie  ');   // 앞뒤 공백은 다듬어 보낸다
    await user.type(pwBox(), 'correct-horse');
    await user.type(confirmBox(), 'correct-horse');
    await user.click(screen.getByRole('radio', { name: /관리자/ }));
    await user.click(submit());

    await waitFor(() => expect(posts(fn, '/api/admin/users')).toHaveLength(1));
    expect(posts(fn, '/api/admin/users')[0]).toEqual({
      username: 'newbie', password: 'correct-horse', role: 'admin',
    });
    expect(onCreated).toHaveBeenCalled();
  });

  it('기본 역할은 최소 권한(구성원)이다 — 승격은 명시적으로만', async () => {
    const fn = mockApi();
    const user = mount();
    await user.type(nameBox(), 'newbie');
    await user.type(pwBox(), 'correct-horse');
    await user.type(confirmBox(), 'correct-horse');
    await user.click(submit());

    await waitFor(() => expect(posts(fn, '/api/admin/users')).toHaveLength(1));
    expect(posts(fn, '/api/admin/users')[0].role).toBe('member');
  });

  it('팀을 적으면 배정까지 한다 (다른 엔드포인트다)', async () => {
    const fn = mockApi();
    const user = mount();
    await user.type(nameBox(), 'newbie');
    await user.type(teamBox(), '코어');
    await user.type(pwBox(), 'correct-horse');
    await user.type(confirmBox(), 'correct-horse');
    await user.click(submit());

    await waitFor(() => expect(posts(fn, '/api/admin/users/team')).toHaveLength(1));
    expect(posts(fn, '/api/admin/users/team')[0]).toEqual({ username: 'newbie', team: '코어' });
  });

  it('팀은 비워 둘 수 있다 — 그때는 배정 호출도 없다', async () => {
    const fn = mockApi();
    const user = mount();
    await user.type(nameBox(), 'newbie');
    await user.type(pwBox(), 'correct-horse');
    await user.type(confirmBox(), 'correct-horse');
    await user.click(submit());

    await waitFor(() => expect(onCreated).toHaveBeenCalled());
    expect(posts(fn, '/api/admin/users/team')).toHaveLength(0);
  });

  it('계정은 됐고 팀만 실패했으면 그렇게 말한다 — "실패"로 뭉치지 않는다', async () => {
    mockApi({ '/api/admin/users/team': { status: 500, body: { error: '팀 저장 실패' } } });
    const user = mount();
    await user.type(nameBox(), 'newbie');
    await user.type(teamBox(), '코어');
    await user.type(pwBox(), 'correct-horse');
    await user.type(confirmBox(), 'correct-horse');
    await user.click(submit());

    expect(await screen.findByText(/만들었지만 팀 배정은 실패/)).toBeInTheDocument();
    // 계정은 실제로 생겼으므로 목록은 새로 읽어야 한다.
    expect(onCreated).toHaveBeenCalled();
  });

  it('서버가 거절하면 그 사유를 그대로 보여준다 (판정의 최종 권한은 서버다)', async () => {
    mockApi({ '/api/admin/users': { status: 400, body: { error: '이미 있는 사용자입니다' } } });
    const user = mount();
    await user.type(nameBox(), 'newbie');
    await user.type(pwBox(), 'correct-horse');
    await user.type(confirmBox(), 'correct-horse');
    await user.click(submit());

    expect(await screen.findByRole('alert')).toHaveTextContent('이미 있는 사용자입니다');
    expect(onCreated).not.toHaveBeenCalled();
  });

  it('취소는 아무것도 보내지 않고 닫는다', async () => {
    const fn = mockApi();
    const user = mount();
    await user.type(nameBox(), 'newbie');
    await user.click(screen.getByRole('button', { name: '취소' }));
    expect(onCancel).toHaveBeenCalled();
    expect(posts(fn, '/api/admin/users')).toHaveLength(0);
  });
});

describe('초기 비밀번호는 남에게 전달하는 값이다', () => {
  it('보기를 누르면 평문으로 확인할 수 있다 (오타가 굳지 않게)', async () => {
    mockApi();
    const user = mount();
    await user.type(pwBox(), 'correct-horse');
    expect(pwBox()).toHaveAttribute('type', 'password');

    await user.click(screen.getByRole('button', { name: '보기' }));
    expect(pwBox()).toHaveAttribute('type', 'text');
    // 확인 칸도 같이 열린다 — 한쪽만 보이면 비교가 안 된다.
    expect(confirmBox()).toHaveAttribute('type', 'text');

    await user.click(screen.getByRole('button', { name: '숨기기' }));
    expect(pwBox()).toHaveAttribute('type', 'password');
  });
});
