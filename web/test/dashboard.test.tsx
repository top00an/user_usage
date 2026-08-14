import { describe, it, expect, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import Dashboard from '@/components/Dashboard';
import { authRoutes, golden, mockFetch, obsRoutes, trackRoutes } from './helpers';

const USER = { username: 'admin', role: 'admin', tenant: 'acme' };

function allRoutes(extra: Parameters<typeof mockFetch>[0] = []) {
  return [...extra, ...authRoutes(USER), ...trackRoutes(), ...obsRoutes()];
}

/** 진입 시 getMe() 가 401 인(=미로그인) 상태의 라우트 묶음. */
function loggedOutRoutes(extra: Parameters<typeof mockFetch>[0] = []) {
  return [...extra, ['/api/auth/me', { status: 401, body: {} }] as [string, { status: number; body: unknown }], ...trackRoutes(), ...obsRoutes()];
}

beforeEach(() => {
  location.hash = '';
  // 테마는 DOM(`<html data-theme>`)과 localStorage 에 산다 — 테스트 사이에 새지 않게 되돌린다.
  document.documentElement.removeAttribute('data-theme');
  localStorage.clear();
});

/*
 * ── 테마 토글 ──────────────────────────────────────────────────────────────
 * 값의 단일 출처는 `<html data-theme>` 이고 public/theme-boot.js 가 먼저 세운다. 여기서 잡는
 * 것은 "버튼이 그 값을 실제로 바꾸고 저장하는가"다 — React state 에 사본을 두면 부팅 스크립트가
 * 세운 값과 갈리고, 그때 화면과 저장값이 서로 다른 말을 한다.
 */
describe('셸 — 테마 토글', () => {
  it('버튼 글자는 "지금 상태"가 아니라 "누르면 되는 것"이다', async () => {
    document.documentElement.setAttribute('data-theme', 'dark');
    mockFetch(allRoutes());
    render(<Dashboard />);
    await screen.findByRole('tab', { name: '대시보드' });
    // 다크에 있으면 버튼은 '라이트 모드'라고 말한다(누르면 갈 곳).
    expect(await screen.findByRole('button', { name: /라이트 모드/ })).toBeInTheDocument();
  });

  it('누르면 data-theme 이 바뀌고 localStorage 에 남는다', async () => {
    document.documentElement.setAttribute('data-theme', 'dark');
    mockFetch(allRoutes());
    const user = userEvent.setup();
    render(<Dashboard />);
    await screen.findByRole('tab', { name: '대시보드' });

    await user.click(await screen.findByRole('button', { name: /라이트 모드/ }));
    expect(document.documentElement.getAttribute('data-theme')).toBe('light');
    expect(localStorage.getItem('usage-theme')).toBe('light');

    // 되돌리면 다시 dark 로. 저장값도 따라간다.
    await user.click(await screen.findByRole('button', { name: /다크 모드/ }));
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark');
    expect(localStorage.getItem('usage-theme')).toBe('dark');
  });

  it('지금 상태는 aria-pressed 로만 말한다 (보조기기용)', async () => {
    document.documentElement.setAttribute('data-theme', 'light');
    mockFetch(allRoutes());
    render(<Dashboard />);
    await screen.findByRole('tab', { name: '대시보드' });
    // 라이트 상태 → 다크가 아니므로 pressed=false, 글자는 '다크 모드'.
    const btn = await screen.findByRole('button', { name: /다크 모드/ });
    expect(btn).toHaveAttribute('aria-pressed', 'false');
  });
});

describe('셸 — 로그인 게이트 · 탭 · 401 복구', () => {
  it('세션이 없으면(getMe 401) 로그인 화면을 그린다', async () => {
    mockFetch(loggedOutRoutes());
    render(<Dashboard />);
    expect(await screen.findByLabelText('아이디')).toBeInTheDocument();
    expect(screen.getByLabelText('비밀번호')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '로그인' })).toBeInTheDocument();
  });

  it('로그인에 성공하면 대시보드를 연다 (기본 탭)', async () => {
    mockFetch(loggedOutRoutes([
      ['/api/auth/login', { body: { ok: true, user: USER } }],
    ]));
    const user = userEvent.setup();
    render(<Dashboard />);

    await user.type(await screen.findByLabelText('아이디'), 'admin');
    await user.type(screen.getByLabelText('비밀번호'), 'pw-123456');
    await user.click(screen.getByRole('button', { name: '로그인' }));

    // 기본 랜딩은 '대시보드'(overview). 탭이 선택돼 있어야 한다.
    expect(await screen.findByRole('tab', { name: '대시보드' })).toHaveAttribute('aria-selected', 'true');
    // 사용 추적으로 이동하면 그 화면이 그려진다.
    await user.click(screen.getByRole('tab', { name: '사용 추적' }));
    expect(await screen.findByRole('heading', { name: '사용자별' })).toBeInTheDocument();
  });

  it('빈 제출은 조용히 무시되지 않는다', async () => {
    mockFetch(loggedOutRoutes());
    const user = userEvent.setup();
    render(<Dashboard />);
    await user.click(await screen.findByRole('button', { name: '로그인' }));
    expect(await screen.findByText('아이디와 비밀번호를 입력하세요.')).toBeInTheDocument();
  });

  it('자격증명이 틀리면(401) 폼 위에 안내하고 로그인 화면에 머문다', async () => {
    mockFetch(loggedOutRoutes([
      ['/api/auth/login', { status: 401, body: { ok: false, error: '아무 문장' } }],
    ]));
    const user = userEvent.setup();
    render(<Dashboard />);

    await user.type(await screen.findByLabelText('아이디'), 'admin');
    await user.type(screen.getByLabelText('비밀번호'), 'wrong-pw');
    await user.click(screen.getByRole('button', { name: '로그인' }));

    expect(await screen.findByText('아이디 또는 비밀번호가 올바르지 않습니다')).toBeInTheDocument();
    // 게이트로 남는다 — 대시보드로 넘어가지 않았다.
    expect(screen.getByLabelText('아이디')).toBeInTheDocument();
  });

  /*
   * ③ 401 복구. 세션이 만료되거나 서버 재기동으로 끊기면 데이터 요청이 401 을 물고 오고,
   * 그때 화면은 빈 카드만 남는다 — 로그인 화면으로 되돌리고 무엇이 잘못됐는지 말해 준다.
   */
  it('데이터 요청이 401 이면 로그인 화면으로 되돌아온다', async () => {
    mockFetch([
      ...authRoutes(USER),
      ['/api/usage', { status: 401, body: { error: 'unauthorized' } }],
    ]);
    render(<Dashboard />);

    expect(await screen.findByText('세션이 만료되었습니다. 다시 로그인하세요.')).toBeInTheDocument();
    expect(screen.getByLabelText('아이디')).toBeInTheDocument();
  });

  it('403 은 로그인으로 튕기지 않고 권한 안내를 보여준다 (문구가 아니라 status 로 분기)', async () => {
    location.hash = '#/usage'; // 에러 UI 를 surface 하는 탭에서 검사(대시보드는 fail-soft)
    mockFetch([
      ...authRoutes(USER),
      ['/api/usage', { status: 403, body: { error: '설명이 언제든 바뀔 수 있는 문장' } }],
    ]);
    render(<Dashboard />);

    expect(await screen.findByRole('heading', { name: '권한이 필요합니다' })).toBeInTheDocument();
    expect(screen.queryByLabelText('아이디')).not.toBeInTheDocument(); // 게이트로 튕기지 않았다
  });

  it('5xx 는 연결 실패로 안내하고 다시 시도를 준다', async () => {
    location.hash = '#/usage';
    mockFetch([
      ...authRoutes(USER),
      ['/api/usage', { status: 503, body: { error: '설명' } }],
    ]);
    render(<Dashboard />);
    expect(await screen.findByRole('heading', { name: '서버가 응답하지 못했습니다' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '다시 시도' })).toBeInTheDocument();
  });

  it('로그아웃하면 세션을 파기하고 로그인 화면으로 돌아간다', async () => {
    const { fn } = mockFetch(allRoutes([
      ['/api/auth/logout', { status: 204 }],
    ]));
    const user = userEvent.setup();
    render(<Dashboard />);

    await screen.findByRole('tab', { name: '대시보드' });
    await user.click(screen.getByRole('button', { name: '로그아웃' }));

    expect(await screen.findByLabelText('아이디')).toBeInTheDocument();
    // 서버 세션 파기를 실제로 호출했다.
    expect(fn.mock.calls.some(([u]) => String(u).includes('/api/auth/logout'))).toBe(true);
  });

  it('해시 딥링크로 관측 탭을 바로 연다', async () => {
    location.hash = '#/usageobs';
    mockFetch(allRoutes());
    render(<Dashboard />);
    expect(await screen.findByRole('heading', { name: 'API 환산 비용' })).toBeInTheDocument();
  });

  /*
   * ── 탭을 빠르게 오갈 때 앞 탭 응답이 현재 탭을 덮지 않는다 ─────────────
   * 없으면 화면이 틀린 값을 보여주는데 아무 에러도 안 난다. 조용한 오표시가 가장 비싸다.
   */
  it('탭을 빠르게 오가면 앞 탭의 늦은 응답을 버린다', async () => {
    const { fn } = mockFetch([
      ...authRoutes(USER),
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
    await screen.findByRole('heading', { name: 'API 환산 비용' });

    // 늦은 추적 응답이 도착하고도 남을 시간
    await new Promise((r) => setTimeout(r, 700));

    expect(screen.getByRole('heading', { name: 'API 환산 비용' })).toBeInTheDocument();
    expect(screen.queryByText('보고된 세션')).not.toBeInTheDocument();
    expect(screen.getByRole('tab', { name: '사용 관측' })).toHaveAttribute('aria-selected', 'true');

    // 그리고 요청 자체가 끊겼는지 — 화면만 안 덮은 것이 아니라 네트워크도 정리돼야 한다.
    const summaryCall = fn.mock.calls.find(([u]) => String(u).includes('/summary'));
    expect(summaryCall).toBeTruthy();
  });

  it('탭은 키보드 상하 화살표로도 옮겨진다(세로 내비)', async () => {
    mockFetch(allRoutes());
    const user = userEvent.setup();
    render(<Dashboard />);

    // 좌측 세로 내비 레일이라 방향키는 위/아래다(WAI-ARIA vertical tablist).
    // 화살표는 **선택된 탭** 기준으로 이동하므로 먼저 사용 추적을 선택한다.
    await user.click(await screen.findByRole('tab', { name: '사용 추적' }));
    await user.keyboard('{ArrowDown}');
    await waitFor(() => expect(screen.getByRole('tab', { name: '사용 관측' })).toHaveAttribute('aria-selected', 'true'));
    await user.keyboard('{ArrowUp}');
    await waitFor(() => expect(screen.getByRole('tab', { name: '사용 추적' })).toHaveAttribute('aria-selected', 'true'));
  });
});

/*
 * 대시보드 타일 라벨 — 비용 타일만 한글이고 나머지가 영어였다. 한 행 안에서 두 언어가 섞이면
 * 사람은 그 차이를 **의미의 차이**로 읽는다("한글 타일만 우리가 계산한 값인가?").
 * 그래서 라벨이 한국어인지가 아니라 **영어가 남아 있지 않은지**까지 본다 — 절반만 옮기는 것이
 * 이 화면에서 가장 흔한 회귀다.
 */
/*
 * ⚠ **섹션은 이제 없다.** 대시보드가 12열 자유 캔버스로 바뀌면서 7개 섹션 머리글(`실시간 현황`
 *   ·`비용 · 토큰`…)이 사라졌다 — 패널이 섹션 밖으로 나갈 수 있으니 남은 머리글은 아래 있는
 *   것을 설명하지 못하는 거짓말이 된다. 그 어휘는 캔버스 위 **한 줄 설명**으로 흡수됐다.
 *
 *   그래서 이 블록이 재는 것은 "섹션 머리글이 있다"가 아니라 **어휘가 한국어로 남아 있다** 이다.
 *   `/실시간 현황/` 이 지금도 초록인 것은 그 한 줄에 같은 말이 살아 있기 때문이지 머리글이
 *   남아서가 아니다 — 그 사실을 안 적어 두면 다음 사람이 이 단언을 보고 "섹션이 있구나"라는
 *   틀린 전제를 얻는다(이 레포가 반복해서 값을 치른 사고 유형이다).
 */
describe('대시보드 — 타일 · 어휘 한국어 통일', () => {
  it('실시간 현황 타일이 한국어 라벨을 쓰고 영어 라벨이 남지 않는다', async () => {
    mockFetch(allRoutes());
    render(<Dashboard />);

    expect(await screen.findByText('활성 세션')).toBeInTheDocument();
    expect(screen.getByText('전체 토큰')).toBeInTheDocument();
    expect(screen.getByText('출력 토큰')).toBeInTheDocument();
    expect(screen.getByText('캐시 적중률')).toBeInTheDocument();
    expect(screen.getByText('캐시읽기 비중')).toBeInTheDocument();
    // 섹션 머리글이 아니라 캔버스 위 한 줄 설명에서 나온다(위 머리말 참고).
    expect(screen.getByText(/실시간 현황/)).toBeInTheDocument();

    for (const en of [
      'Active Sessions', 'Total Tokens', 'Output Tokens',
      'Cache Hit Ratio', 'Cache Read Share',
    ]) {
      expect(screen.queryByText(en)).not.toBeInTheDocument();
    }
    for (const en of [/Live Status/, /Cost & Tokens/, /Tool & Agent Analytics/]) {
      expect(screen.queryByText(en)).not.toBeInTheDocument();
    }
  });
});

describe('사용 추적 — ④ 근사값을 정확한 값으로 위장하지 않는다', () => {
  beforeEach(() => {
    location.hash = '#/usage'; // 이 블록은 '사용 추적' 탭 내용을 검사한다(기본 탭은 대시보드)
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
      ...authRoutes(USER),
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
    location.hash = '#/usageobs';
    mockFetch(allRoutes());
  });

  it('단가 미등록 모델의 이름을 남긴다 (조용한 $0 금지)', async () => {
    render(<Dashboard />);
    await screen.findByRole('heading', { name: 'API 환산 비용' });
    expect(screen.getByText(/단가 미등록 모델/)).toBeInTheDocument();
    // 상위 세션 표에도 같은 이름이 나온다 — 여기서 보는 것은 "환산액 카드가 이름을 남기는가"다.
    expect(screen.getAllByText('some-unreleased-model-x').length).toBeGreaterThan(0);
  });

  it('TTL 미상 행 수와 과소 추정 사실을 말한다', async () => {
    render(<Dashboard />);
    await screen.findByRole('heading', { name: 'API 환산 비용' });
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
      ...authRoutes(USER),
      ['/api/usage/leaderboard', { status: 500, body: { error: '터졌다' } }],
      ['/api/usage/quality', { status: 500, body: { error: '터졌다' } }],
      ['/api/usage/coverage', { status: 500, body: { error: '터졌다' } }],
      ...obsRoutes(),
      ...trackRoutes(),
    ]);
    render(<Dashboard />);
    expect(await screen.findByRole('heading', { name: 'API 환산 비용' })).toBeInTheDocument();
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
    location.hash = '#/usage'; // byUser(사용 추적) 렌더에서 XSS 안전성 검사
    const s = golden<{ byUser: unknown[] }>('summary');
    const evil = '<img src=x onerror="document.title=\'xss\'">';
    mockFetch([
      ...authRoutes(USER),
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
