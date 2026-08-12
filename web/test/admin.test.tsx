import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import AdminTab from '@/components/admin/AdminTab';
import Dashboard from '@/components/Dashboard';
import { ToastProvider } from '@/components/Toast';
import { authRoutes, mockFetch, obsRoutes, trackRoutes } from './helpers';

/*
 * ── 관리 탭 ───────────────────────────────────────────────────────────────
 *
 * 이 화면에만 **되돌릴 수 없는 버튼**이 있다. 그래서 여기 단정은 "그려지는가"가 아니라
 * "사람이 잘못 누르는 것을 무엇이 막는가"다. 스펙(DESIGN-SPEC §3·§4·§8·§11)의 문구를
 * 그대로 못 박는다 — 문구를 바꾸려면 그 문서를 먼저 고쳐야 한다.
 *
 * `/api/admin/*` 는 응답 shape 가 경로+메서드로 갈리므로(같은 경로에 GET/POST가 다른 답)
 * 공용 mockFetch(접두사만 본다) 대신 **메서드까지 보는 로컬 목**을 쓴다.
 */

interface UserRow { username: string; role: 'admin' | 'member'; createdAt: string; team: string | null }
interface KeyRow { id: string; masked: string; createdAt: string; revokedAt: string | null; username: string | null }

const ADMIN: UserRow = { username: 'ops-admin', role: 'admin', createdAt: '2026-06-01T00:00:00Z', team: '플랫폼' };
const ALICE: UserRow = { username: 'alice', role: 'member', createdAt: '2026-07-02T00:00:00Z', team: '코어' };
const BOB: UserRow = { username: 'bob', role: 'admin', createdAt: '2026-07-03T00:00:00Z', team: null };

const KEY_ALICE: KeyRow = { id: 'k-alice', masked: 'uu_ing_…24ee', createdAt: '2026-08-11T01:46:00Z', revokedAt: null, username: 'alice' };
const KEY_UNBOUND: KeyRow = { id: 'k-orphan', masked: 'uu_ing_…9459', createdAt: '2026-08-11T04:10:00Z', revokedAt: null, username: null };
const KEY_REVOKED: KeyRow = { id: 'k-old', masked: 'uu_ing_…7a31', createdAt: '2026-08-02T02:20:00Z', revokedAt: '2026-08-09T02:20:00Z', username: 'alice' };

const ISSUED = { key: 'uu_ing_PLAINTEXT_ONCE', id: 'k-new', createdAt: '2026-08-11T05:00:00Z', username: 'alice' };

type Reply = { status: number; body?: unknown };

interface Scene {
  users?: UserRow[];
  keys?: KeyRow[];
  /** 경로별(POST) 응답 덮어쓰기 — 409 거부·sessionsRevoked:false 같은 사고를 심는다. */
  post?: Record<string, Reply>;
}

function res(status: number, body: unknown): Promise<Response> {
  // 204 는 본문을 가질 수 없다 — 실제 서버와 같은 계약이라야 request() 의 빈 본문 경로가 검증된다.
  return Promise.resolve(new Response(status === 204 ? null : JSON.stringify(body ?? {}), {
    status,
    headers: { 'Content-Type': 'application/json' },
  }));
}

function mockApi(scene: Scene = {}) {
  const users = scene.users ?? [ADMIN, ALICE];
  const keys = scene.keys ?? [KEY_ALICE, KEY_UNBOUND, KEY_REVOKED];
  const fn = vi.fn((url: string, init?: RequestInit) => {
    const path = String(url).split('?')[0]!;
    const method = (init?.method ?? 'GET').toUpperCase();
    if (method === 'POST' && scene.post?.[path]) {
      const r = scene.post[path]!;
      return res(r.status, r.body);
    }
    if (method === 'POST') {
      if (path === '/api/admin/users/delete') return res(200, { ok: true, username: 'alice', sessionsRevoked: true });
      if (path.startsWith('/api/admin/users')) return res(200, { ok: true, user: ALICE, sessionsRevoked: true });
      if (path === '/api/admin/keys' || path === '/api/me/keys') return res(200, ISSUED);
      if (path.endsWith('/revoke')) return res(204, null);
    }
    if (path === '/api/admin/users') return res(200, { users });
    if (path === '/api/admin/keys' || path === '/api/me/keys') return res(200, { keys });
    if (path === '/api/auth/me') return res(200, { username: 'ops-admin', role: 'admin', tenant: 'acme' });
    return res(404, { error: '없는 경로' });
  });
  vi.stubGlobal('fetch', fn);
  return fn;
}

function renderAdmin(self = 'ops-admin') {
  return render(<ToastProvider><AdminTab self={self} /></ToastProvider>);
}

/** 행을 눌러 그 사용자의 시트를 연다(표 자체는 읽기 전용 — 모든 변경은 시트 안에서 일어난다). */
async function openSheet(user: ReturnType<typeof userEvent.setup>, username: string) {
  await user.click(await screen.findByRole('button', { name: `${username} 관리` }));
  return within(await screen.findByRole('dialog'));
}

beforeEach(() => {
  location.hash = '';
});

/* ── ① 탭 등록 · 권한 경계 ─────────────────────────────────────────────── */

describe('셸 — 관리 탭은 사이드바 맨 뒤 · 관리자 전용', () => {
  it('관리자에게 관리 탭이 보이고, 사이드바에서 맨 뒤다', async () => {
    mockFetch([...authRoutes(), ...trackRoutes(), ...obsRoutes()]);
    render(<Dashboard />);
    expect(await screen.findByRole('tab', { name: '관리' })).toBeInTheDocument();
    // 파괴적 화면은 오조작 거리를 벌린다 — 자주 쓰는 탭 사이에 끼우지 않는다(§1.1).
    const tabs = screen.getAllByRole('tab');
    expect(tabs[tabs.length - 1]).toHaveAccessibleName('관리');
  });

  it('member 에게는 관리 탭이 보이지 않는다', async () => {
    mockFetch([...authRoutes({ username: 'bob', role: 'member', tenant: 'acme' }), ...trackRoutes(), ...obsRoutes()]);
    render(<Dashboard />);
    await screen.findByRole('tab', { name: '대시보드' });
    expect(screen.queryByRole('tab', { name: '관리' })).not.toBeInTheDocument();
  });

  it('member 가 #/admin 딥링크로 들어와도 그 탭이 열리지 않는다 (숨김이 아니라 렌더 차단)', async () => {
    location.hash = '#/admin';
    mockFetch([...authRoutes({ username: 'bob', role: 'member', tenant: 'acme' }), ...trackRoutes(), ...obsRoutes()]);
    render(<Dashboard />);
    // 첫 탭으로 접힌다(이중 방어) — 관리 탭 본문은 아예 마운트되지 않는다.
    expect(await screen.findByRole('tab', { name: '대시보드' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.queryByRole('heading', { name: '사용자' })).not.toBeInTheDocument();
  });

  it('member 에게 연동 탭은 온전히 자기 것이다 — 더는 관리자 전용이 아니다', async () => {
    mockFetch([...authRoutes({ username: 'bob', role: 'member', tenant: 'acme' }), ...trackRoutes(), ...obsRoutes()]);
    render(<Dashboard />);
    expect(await screen.findByRole('tab', { name: '연동' })).toBeInTheDocument();
  });
});

/* ── ② 표는 읽기 전용 · 변경은 시트 안에서만 ───────────────────────────── */

describe('관리 탭 — 사용자 표', () => {
  it('사용자·역할·팀과 그 사람의 활성 키 수를 낸다', async () => {
    mockApi();
    renderAdmin();

    const table = (await screen.findByRole('heading', { name: '사용자' })).closest('section')!;
    const row = within(table).getByRole('button', { name: 'alice 관리' });
    expect(within(row).getByText('구성원')).toBeInTheDocument();
    expect(within(row).getByText('코어')).toBeInTheDocument();
    // 활성 키 1개 — 해지된 k-old 는 세지 않는다.
    expect(within(row).getByText('1')).toBeInTheDocument();
    expect(within(table).getByText('전체 2명 · 관리자 1명')).toBeInTheDocument();
  });

  it('표 안에 버튼을 중첩하지 않는다 — 행 자체가 버튼이다', async () => {
    mockApi();
    renderAdmin();
    const row = await screen.findByRole('button', { name: 'alice 관리' });
    expect(row.tagName).toBe('TR');
    expect(row.querySelector('button')).toBeNull();
  });

  it('행은 키보드(Enter)로도 열리고, Esc 로 닫으면 포커스가 그 행으로 돌아온다', async () => {
    mockApi();
    const user = userEvent.setup();
    renderAdmin();

    const row = await screen.findByRole('button', { name: 'alice 관리' });
    row.focus();
    await user.keyboard('{Enter}');
    await screen.findByRole('dialog');

    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(document.activeElement).toBe(row);
  });

  it('관리자가 혼자면 행동을 초대한다 (진짜 빈 상태는 1명뿐일 때다)', async () => {
    mockApi({ users: [ADMIN], keys: [] });
    renderAdmin();
    expect(await screen.findByText('아직 당신뿐입니다. 팀원을 추가하면 사용량이 사람 이름으로 갈립니다.')).toBeInTheDocument();
  });

  it('목록 조회가 403 이면 관리자 전용임을 말한다 (문구가 아니라 status 로 분기)', async () => {
    vi.stubGlobal('fetch', vi.fn(() => res(403, { error: '언제든 다듬어지는 문장' })));
    renderAdmin();
    expect(await screen.findByRole('heading', { name: '권한이 필요합니다' })).toBeInTheDocument();
  });
});

/* ── ③ 권한 경계를 화면이 말하는 법 — 보이되 비활성 + 보이는 이유 ────────── */

describe('관리 탭 — 사전 거부는 숨기지 않고 비활성 + 보이는 이유', () => {
  it('마지막 관리자는 강등·삭제가 비활성이고 이유가 보이는 글자다 (툴팁이 아니다)', async () => {
    mockApi({ users: [ADMIN, ALICE] }); // 관리자 1명
    const user = userEvent.setup();
    renderAdmin('alice'); // 본인 계정 규칙과 섞이지 않게 다른 사람으로 로그인해 둔다
    const sheet = await openSheet(user, 'ops-admin');

    const roleBtn = sheet.getByRole('button', { name: '역할 변경' });
    expect(roleBtn).toBeDisabled();
    const reason = sheet.getByText('⚠ 마지막 관리자입니다 — 강등할 수 없습니다. 다른 사용자를 먼저 관리자로 올린 뒤 다시 시도하세요.');
    expect(reason).toBeInTheDocument();
    // 이유는 aria-describedby 로 컨트롤에 붙는다 — title 툴팁은 키보드·터치·스크린리더에 닿지 않는다.
    expect(roleBtn).toHaveAttribute('aria-describedby', reason.id);
    expect(roleBtn).not.toHaveAttribute('title');

    expect(sheet.getByRole('button', { name: '사용자 삭제' })).toBeDisabled();
    expect(sheet.getByText('⚠ 마지막 관리자입니다 — 삭제할 수 없습니다. 지우면 아무도 사용자와 키를 관리할 수 없습니다.')).toBeInTheDocument();
  });

  it('본인 계정은 스스로 강등·삭제할 수 없다', async () => {
    mockApi({ users: [ADMIN, BOB, ALICE] }); // 관리자 2명이라 "마지막 관리자"로는 막히지 않는다
    const user = userEvent.setup();
    renderAdmin('ops-admin');
    const sheet = await openSheet(user, 'ops-admin');

    expect(sheet.getByRole('button', { name: '역할 변경' })).toBeDisabled();
    expect(sheet.getByText('⚠ 본인 계정입니다 — 스스로 강등할 수 없습니다. 다른 관리자에게 요청하세요.')).toBeInTheDocument();
    expect(sheet.getByRole('button', { name: '사용자 삭제' })).toBeDisabled();
    expect(sheet.getByText('⚠ 본인 계정입니다 — 스스로 삭제할 수 없습니다. 다른 관리자에게 요청하세요.')).toBeInTheDocument();
  });

  it('member 를 관리자로 올리는 것은 막지 않는다 (승격은 사고가 아니다)', async () => {
    mockApi({ users: [ADMIN, ALICE] });
    const user = userEvent.setup();
    renderAdmin('ops-admin');
    const sheet = await openSheet(user, 'alice');
    expect(sheet.getByRole('button', { name: '역할 변경' })).toBeEnabled();
  });
});

/* ── ④ 위험 동작 — 확인의 무게를 되돌림 가능성에 맞춘다 ───────────────── */

describe('관리 탭 — 역할 변경(B급: 인라인 2단계)', () => {
  it('무엇이 바뀌는지 말한 뒤에만 보낸다', async () => {
    const fn = mockApi({ users: [ADMIN, ALICE] });
    const user = userEvent.setup();
    renderAdmin('ops-admin');
    const sheet = await openSheet(user, 'alice');

    await user.selectOptions(sheet.getByLabelText('역할'), 'admin');
    await user.click(sheet.getByRole('button', { name: '역할 변경' }));

    // 결과 문장 — 무엇을 얻고 무엇을 잃는지.
    expect(sheet.getByText('구성원 → 관리자 로 바꿉니다.')).toBeInTheDocument();
    expect(sheet.getByText(/사용자 생성·역할 변경·삭제와 전체 키 현황을 할 수 있게 됩니다/)).toBeInTheDocument();
    /*
     * ⚠ 승격은 세션을 끊지 않는다(서버가 강등에서만 끊는다 — 과잉 로그아웃 방지).
     *   DESIGN-SPEC §4-B 의 예시 문구는 승격에도 "즉시 끊깁니다"라고 적었지만 그것은 서버 동작과
     *   다르다. 화면은 **서버가 실제로 하는 일**을 말한다.
     */
    expect(sheet.getByText(/승격은 과잉 로그아웃을 만들지 않습니다/)).toBeInTheDocument();
    // 확정 전에는 아무것도 보내지 않았다.
    expect(fn.mock.calls.some(([u]) => String(u) === '/api/admin/users/role')).toBe(false);

    await user.click(sheet.getByRole('button', { name: '역할 변경 확정' }));
    await waitFor(() => {
      const call = fn.mock.calls.find(([u]) => String(u) === '/api/admin/users/role');
      expect(call).toBeTruthy();
      expect(JSON.parse((call![1] as RequestInit).body as string)).toEqual({ username: 'alice', role: 'admin' });
    });
  });

  it('강등에서는 세션이 끊긴다는 사실을 말한다', async () => {
    mockApi({ users: [ADMIN, BOB, ALICE] });
    const user = userEvent.setup();
    renderAdmin('ops-admin');
    const sheet = await openSheet(user, 'bob');

    await user.selectOptions(sheet.getByLabelText('역할'), 'member');
    await user.click(sheet.getByRole('button', { name: '역할 변경' }));
    expect(sheet.getByText('관리자 → 구성원 로 바꿉니다.')).toBeInTheDocument();
    expect(sheet.getByText(/bob 의 로그인 세션은 즉시 끊깁니다/)).toBeInTheDocument();
  });

  it('이름 재입력을 요구하지 않는다 — 재입력은 사용자 삭제 하나뿐이다(확인 피로)', async () => {
    mockApi({ users: [ADMIN, ALICE] });
    const user = userEvent.setup();
    renderAdmin('ops-admin');
    const sheet = await openSheet(user, 'alice');

    await user.selectOptions(sheet.getByLabelText('역할'), 'admin');
    await user.click(sheet.getByRole('button', { name: '역할 변경' }));
    expect(sheet.queryByLabelText('확인하려면 사용자 이름을 그대로 입력하세요')).not.toBeInTheDocument();
  });
});

describe('관리 탭 — 사용자 삭제(C급: 재입력)', () => {
  it('이름이 정확히 일치할 때까지 삭제 버튼이 눌리지 않는다', async () => {
    mockApi({ users: [ADMIN, ALICE] });
    const user = userEvent.setup();
    renderAdmin('ops-admin');
    const sheet = await openSheet(user, 'alice');

    await user.click(sheet.getByRole('button', { name: '사용자 삭제' }));

    expect(sheet.getByText('정말 alice 를 삭제합니까?')).toBeInTheDocument();
    expect(sheet.getByText(/되돌릴 수 없습니다/)).toBeInTheDocument();
    expect(sheet.getByText(/이미 수집된 사용량은 alice 이름으로 그대로 남습니다/)).toBeInTheDocument();
    // 10-1 확정 전 보수적 문구 — 키는 남는다(따로 해지해야 한다).
    expect(sheet.getByText(/발급된 인제스트 키 1개는 남습니다/)).toBeInTheDocument();

    const confirm = sheet.getByRole('button', { name: 'alice 삭제' });
    const input = sheet.getByLabelText('확인하려면 사용자 이름을 그대로 입력하세요');
    expect(confirm).toBeDisabled();
    // 확인 블록이 열리면 입력칸으로 포커스가 간다 — 그러지 않으면 마우스로 다시 찾아야 한다.
    expect(document.activeElement).toBe(input);

    await user.type(input, 'Alice');            // 대소문자를 구분한다
    expect(confirm).toBeDisabled();
    await user.clear(input);
    await user.type(input, '  alice  ');        // 붙여넣기 공백만 봐준다
    expect(confirm).toBeEnabled();
  });

  it('파괴 버튼이 초기 포커스를 가져가지 않는다 — 취소가 DOM 에서 먼저다', async () => {
    mockApi({ users: [ADMIN, ALICE] });
    const user = userEvent.setup();
    renderAdmin('ops-admin');
    const sheet = await openSheet(user, 'alice');
    await user.click(sheet.getByRole('button', { name: '사용자 삭제' }));

    const cancel = sheet.getByRole('button', { name: '취소' });
    const confirm = sheet.getByRole('button', { name: 'alice 삭제' });
    expect(cancel.compareDocumentPosition(confirm) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it('일치하면 삭제를 보내고 시트를 닫는다', async () => {
    const fn = mockApi({ users: [ADMIN, ALICE] });
    const user = userEvent.setup();
    renderAdmin('ops-admin');
    const sheet = await openSheet(user, 'alice');

    await user.click(sheet.getByRole('button', { name: '사용자 삭제' }));
    await user.type(sheet.getByLabelText('확인하려면 사용자 이름을 그대로 입력하세요'), 'alice');
    await user.click(sheet.getByRole('button', { name: 'alice 삭제' }));

    await waitFor(() => {
      const call = fn.mock.calls.find(([u]) => String(u) === '/api/admin/users/delete');
      expect(call).toBeTruthy();
      expect(JSON.parse((call![1] as RequestInit).body as string)).toEqual({ username: 'alice' });
    });
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(await screen.findByText('alice 를 삭제했습니다.')).toBeInTheDocument();
  });
});

/* ── 서버 거부 · 사고 신호 ─────────────────────────────────────────────── */

describe('관리 탭 — 서버 판정을 화면이 그대로 읽는다', () => {
  it('409 거부는 시트를 닫지 않고 그 자리에서 뜬다 (토스트로 던지지 않는다)', async () => {
    mockApi({
      users: [ADMIN, BOB, ALICE],
      post: {
        '/api/admin/users/role': {
          status: 409,
          body: { error: '마지막 관리자는 강등·삭제할 수 없습니다 — 먼저 다른 관리자를 만드세요' },
        },
      },
    });
    const user = userEvent.setup();
    renderAdmin('ops-admin');
    // 화면은 관리자 2명이라 사전 판정으로는 허용한다 — 그 사이 다른 탭에서 누가 강등했다.
    const sheet = await openSheet(user, 'bob');
    await user.selectOptions(sheet.getByLabelText('역할'), 'member');
    await user.click(sheet.getByRole('button', { name: '역할 변경' }));
    await user.click(sheet.getByRole('button', { name: '역할 변경 확정' }));

    const alert = await screen.findByRole('alert');
    // 서버 문구를 그대로 보여준다(화면이 문구로 분기하지 않는다).
    expect(alert).toHaveTextContent('마지막 관리자는 강등·삭제할 수 없습니다 — 먼저 다른 관리자를 만드세요');
    expect(alert).toHaveTextContent('화면이 낡았을 수 있습니다');
    // 시트는 열려 있다 — 무엇을 눌렀는지 기억으로 복원하게 만들지 않는다.
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    expect(within(screen.getByRole('dialog')).getByRole('button', { name: '목록 새로고침' })).toBeInTheDocument();
  });

  it('sessionsRevoked:false 인 강등은 사고로 취급한다', async () => {
    mockApi({
      users: [ADMIN, BOB, ALICE],
      post: { '/api/admin/users/role': { status: 200, body: { ok: true, user: { ...BOB, role: 'member' }, sessionsRevoked: false } } },
    });
    const user = userEvent.setup();
    renderAdmin('ops-admin');
    const sheet = await openSheet(user, 'bob');
    await user.selectOptions(sheet.getByLabelText('역할'), 'member');
    await user.click(sheet.getByRole('button', { name: '역할 변경' }));
    await user.click(sheet.getByRole('button', { name: '역할 변경 확정' }));

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('강등했는데 그 사람의 세션이 끊기지 않았습니다');
    expect(alert).toHaveTextContent(/세션이 만료될 때까지 관리자 권한으로 남아 있습니다/);
  });

  it('세션이 끊긴 강등은 성공 토스트가 그 사실까지 말한다', async () => {
    mockApi({
      users: [ADMIN, BOB, ALICE],
      post: { '/api/admin/users/role': { status: 200, body: { ok: true, user: { ...BOB, role: 'member' }, sessionsRevoked: true } } },
    });
    const user = userEvent.setup();
    renderAdmin('ops-admin');
    const sheet = await openSheet(user, 'bob');
    await user.selectOptions(sheet.getByLabelText('역할'), 'member');
    await user.click(sheet.getByRole('button', { name: '역할 변경' }));
    await user.click(sheet.getByRole('button', { name: '역할 변경 확정' }));
    expect(await screen.findByText('bob 를 구성원으로 바꿨습니다. 그 사람의 세션은 끊겼습니다.')).toBeInTheDocument();
  });

  it('낙관적 갱신을 하지 않는다 — 성공 후 서버에서 다시 읽는다', async () => {
    const fn = mockApi({ users: [ADMIN, ALICE] });
    const user = userEvent.setup();
    renderAdmin('ops-admin');
    const sheet = await openSheet(user, 'alice');
    const before = fn.mock.calls.filter(([u]) => String(u) === '/api/admin/users').length;

    await user.clear(sheet.getByLabelText('팀'));
    await user.type(sheet.getByLabelText('팀'), '코어2');
    await user.click(sheet.getByRole('button', { name: '팀 저장' }));

    await waitFor(() => {
      expect(fn.mock.calls.filter(([u]) => String(u) === '/api/admin/users').length).toBeGreaterThan(before);
    });
    expect(await screen.findByText('alice 를 코어2 팀으로 배정했습니다.')).toBeInTheDocument();
  });
});

/* ── ⑤ 결속 없는 키 — 근사를 정확값으로 위장하지 않는다 ────────────────── */

describe('관리 탭 — 인제스트 키 현황', () => {
  it('사용자에 묶이지 않은 키에 ⚠ 를 달고 무슨 뜻인지 말한다', async () => {
    mockApi();
    renderAdmin();

    const card = (await screen.findByRole('heading', { name: '인제스트 키 현황' })).closest('section')!;
    expect(within(card).getByText('활성 2 · 해지 1 · ⚠ 미결속 1')).toBeInTheDocument();

    // Flag 는 ⚠ 를 글자로 함께 낸다 — 색만으로 말하지 않는다.
    const orphan = within(card).getByText('uu_ing_…9459').closest('tr')!;
    expect(within(orphan).getByText('⚠ PC 이름')).toBeInTheDocument();
    expect(within(orphan).getByTitle(/이 키는 사용자에 묶여 있지 않습니다/)).toBeInTheDocument();

    // 결속된 키는 사람 이름이 그대로 나온다(⚠ 없음).
    const bound = within(card).getByText('uu_ing_…24ee').closest('tr')!;
    expect(within(bound).getByText('alice')).toBeInTheDocument();

    expect(within(card).getByText(/보고한 PC 가 주장하는 이름으로 잡힙니다/)).toBeInTheDocument();
    // 과거 사용량이 바뀐다고 쓰지 않는다 — 귀속은 보고 시점의 키로 정해진다.
    expect(within(card).getByText(/이미 수집된 사용량의 이름은 바뀌지 않습니다/)).toBeInTheDocument();
  });

  it('아직 집계하지 않는 열을 빈칸으로 두지 않는다 — 못 잰다고 밝힌다', async () => {
    mockApi();
    renderAdmin();
    const card = (await screen.findByRole('heading', { name: '인제스트 키 현황' })).closest('section')!;
    expect(within(card).getByText(/^키별 마지막 보고는 아직 집계하지 않습니다/)).toBeInTheDocument();
  });

  it('키가 없으면 어디서 생기는지 말한다', async () => {
    mockApi({ keys: [] });
    renderAdmin();
    expect(await screen.findByText('아직 발급된 인제스트 키가 없습니다 — 팀원이 연동 탭에서 자기 키를 발급하면 여기 나타납니다.')).toBeInTheDocument();
  });
});

/* ── ⑥ 대리발급 — 소유자는 기본값 있는 필수 입력이다 ───────────────────── */

describe('관리 탭 — 대리발급의 소유자 선택', () => {
  it('기본값은 로그인한 관리자 본인이고, 그대로 발급하면 그 사람에게 묶인다', async () => {
    const fn = mockApi();
    const user = userEvent.setup();
    renderAdmin('ops-admin');

    await user.click(await screen.findByRole('button', { name: '키 발급' }));
    const owner = screen.getByLabelText('이 키의 소유자');
    expect(owner).toHaveValue('ops-admin');

    await user.click(screen.getByRole('button', { name: '발급' }));
    await waitFor(() => {
      const call = fn.mock.calls.find(([u], i) => String(u) === '/api/admin/keys' && (fn.mock.calls[i]![1] as RequestInit)?.method === 'POST');
      expect(call).toBeTruthy();
      expect(JSON.parse((call![1] as RequestInit).body as string)).toEqual({ username: 'ops-admin' });
    });
  });

  it('결속 없는 발급은 명시적으로 골라야 하고, 무슨 뜻인지 화면이 말한다', async () => {
    const fn = mockApi();
    const user = userEvent.setup();
    renderAdmin('ops-admin');

    await user.click(await screen.findByRole('button', { name: '키 발급' }));
    await user.selectOptions(screen.getByLabelText('이 키의 소유자'), '');
    expect(screen.getByText(/이 키의 사용량은 보고한 PC 가 주장하는 이름으로 잡힙니다/)).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: '발급' }));
    await waitFor(() => {
      const call = fn.mock.calls.find(([u], i) => String(u) === '/api/admin/keys' && (fn.mock.calls[i]![1] as RequestInit)?.method === 'POST');
      expect(JSON.parse((call![1] as RequestInit).body as string)).toEqual({ username: '' });
    });
  });

  it('소유자 선택지는 사용자 목록에서 온다 — 없는 계정을 고를 자리를 만들지 않는다', async () => {
    mockApi({ users: [ADMIN, ALICE, BOB] });
    const user = userEvent.setup();
    renderAdmin('ops-admin');
    await user.click(await screen.findByRole('button', { name: '키 발급' }));
    const owner = screen.getByLabelText('이 키의 소유자') as HTMLSelectElement;
    expect([...owner.options].map((o) => o.value)).toEqual(['ops-admin', 'alice', 'bob', '']);
  });
});

/* ── ⑦ 평문 키 1회 노출 — 카드가 아니라 모달 ───────────────────────────── */

describe('관리 탭 — 발급된 평문 키', () => {
  it('모달로 뜨고, 다시 볼 수 없다는 사실을 말한다', async () => {
    mockApi();
    const user = userEvent.setup();
    renderAdmin('ops-admin');

    await user.click(await screen.findByRole('button', { name: '키 발급' }));
    await user.click(screen.getByRole('button', { name: '발급' }));

    const dialog = within(await screen.findByRole('dialog', { name: '새 인제스트 키 — 지금만 표시됩니다' }));
    expect(dialog.getByText('uu_ing_PLAINTEXT_ONCE')).toBeInTheDocument();
    expect(dialog.getByText(/이 창을 닫으면 평문 키를 다시 볼 수 없습니다/)).toBeInTheDocument();
    expect(dialog.getByText(/sha256 해시만 저장합니다/)).toBeInTheDocument();
  });

  it('복사하지 않고 닫으면 복구 경로를 한 문장으로 준다 (닫기를 막지 않는다)', async () => {
    mockApi();
    const user = userEvent.setup();
    renderAdmin('ops-admin');

    await user.click(await screen.findByRole('button', { name: '키 발급' }));
    await user.click(screen.getByRole('button', { name: '발급' }));
    const dialog = await screen.findByRole('dialog', { name: '새 인제스트 키 — 지금만 표시됩니다' });
    await user.click(within(dialog).getByRole('button', { name: '닫기' }));

    expect(await screen.findByText('키를 복사하지 않고 닫았습니다 — 다시 볼 수 없습니다. 필요하면 해지하고 새로 발급하세요.')).toBeInTheDocument();
  });

  it('평문은 메모리에만 있다 — localStorage · 쿠키 · 토스트 문구 어디에도 없다', async () => {
    mockApi();
    const user = userEvent.setup();
    renderAdmin('ops-admin');

    await user.click(await screen.findByRole('button', { name: '키 발급' }));
    await user.click(screen.getByRole('button', { name: '발급' }));
    const dialog = await screen.findByRole('dialog', { name: '새 인제스트 키 — 지금만 표시됩니다' });
    await user.click(within(dialog).getByRole('button', { name: '키 복사' }));

    expect(await screen.findByText('키를 복사했습니다')).toBeInTheDocument();
    expect(JSON.stringify(window.localStorage)).not.toContain(ISSUED.key);
    expect(JSON.stringify(window.sessionStorage)).not.toContain(ISSUED.key);
    expect(document.cookie).not.toContain(ISSUED.key);
    expect(document.querySelector('.toast')!.textContent).not.toContain(ISSUED.key);
    expect(location.href).not.toContain(ISSUED.key);
  });
});
