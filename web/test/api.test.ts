import { describe, it, expect, vi, beforeEach } from 'vitest';
import {
  ApiError,
  failureKind,
  request,
  setUnauthorizedHandler,
  getSummary,
  getSessionDetail,
  getSeats,
  getTeams,
  getSessions,
  seriesQuery,
  login,
  logout,
  getMe,
} from '@/lib/api';

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}


/**
 * "실패해야 하는 요청"의 오류를 꺼낸다. `.catch(e => e)` 로 받으면 타입이 unknown 으로 접혀
 * 뒤의 단언이 전부 캐스팅 노이즈가 된다 — 그리고 **성공해 버린 경우를 놓친다**(가장 위험한
 * 통과다: 오류 계약이 깨졌는데 테스트가 초록불).
 */
async function failureOf(p: Promise<unknown>): Promise<ApiError> {
  let thrown: unknown = null;
  let resolved = false;
  try { await p; resolved = true; } catch (e) { thrown = e; }
  if (resolved) throw new Error('실패해야 하는 요청이 성공했다');
  return thrown as ApiError;
}

describe('lib/api — 유일한 서버 호출구', () => {
  beforeEach(() => {
    setUnauthorizedHandler(() => {});
  });

  it('200 은 본문을 그대로 돌려준다', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(200, { totals: { sessions: 3 } })));
    await expect(getSummary()).resolves.toEqual({ totals: { sessions: 3 } });
  });

  it('자격증명은 세션 쿠키로 실린다 — credentials: include', async () => {
    const spy = vi.fn().mockResolvedValue(jsonResponse(200, {}));
    vi.stubGlobal('fetch', spy);
    await request('/api/usage/summary');
    expect(spy.mock.calls[0]?.[1]).toMatchObject({ credentials: 'include' });
  });

  it('상태코드와 서버 본문을 구조로 남긴다 (문구 파싱 불필요)', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(400, { error: 'metric 은 …' })));
    const err = await failureOf(request('/api/usage/series'));
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(400);
    expect(err.body).toEqual({ error: 'metric 은 …' });
    expect(err.message).toBe('metric 은 …');
  });

  it('JSON 이 아닌 본문에서도 죽지 않는다', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('<html>502</html>', { status: 502 })));
    const err = await failureOf(request('/api/usage/summary'));
    expect(err.status).toBe(502);
    expect(err.body).toEqual({});
  });

  it('401 은 던지기 전에 onUnauthorized 를 부른다', async () => {
    const onUnauth = vi.fn();
    setUnauthorizedHandler(onUnauth);
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(401, { error: 'unauthorized' })));
    await expect(request('/api/usage/summary')).rejects.toBeInstanceOf(ApiError);
    expect(onUnauth).toHaveBeenCalledTimes(1);
  });

  /*
   * 분기는 **status 로** 한다. 아래 케이스가 전부 같은 문구('오류')를 달고 있어도
   * 갈래가 갈려야 한다 — 문구에 걸면 문구를 다듬는 날 화면이 조용히 틀린 쪽으로 넘어간다.
   */
  it.each([
    [401, 'unauthorized'],
    [403, 'forbidden'],
    [404, 'notFound'],
    [400, 'badRequest'],
    [500, 'server'],
    [503, 'server'],
  ])('status %i → %s (문구가 아니라 코드로 판정)', async (status, kind) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(status, { error: '오류' })));
    const err = await failureOf(request('/x'));
    expect(failureKind(err)).toBe(kind);
  });

  it('네트워크 실패는 network 로 접힌다 (status 없음)', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')));
    const err = await failureOf(request('/x'));
    expect(err).toBeInstanceOf(ApiError);
    expect(failureKind(err)).toBe('network');
  });

  it('취소(AbortError)는 network 가 아니라 aborted 다 — 화면에 에러로 뜨면 안 된다', async () => {
    const ac = new AbortController();
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(
      Object.assign(new Error('aborted'), { name: 'AbortError' }),
    ));
    const err = await failureOf(request('/x', { signal: ac.signal }));
    expect(failureKind(err)).toBe('aborted');
  });

  it('세션 id 는 URL 인코딩된다', async () => {
    const spy = vi.fn().mockResolvedValue(jsonResponse(200, {}));
    vi.stubGlobal('fetch', spy);
    await getSessionDetail('a/b?c');
    expect(spy.mock.calls[0]?.[0]).toBe('/api/usage/sessions/a%2Fb%3Fc');
  });

  it('series 질의는 파라미터를 인코딩해 붙인다', () => {
    expect(seriesQuery({ metric: 'tokens', interval: 'day', groupBy: 'model', user: 'a b', from: '2026-08-01' }))
      .toBe('/api/usage/series?metric=tokens&interval=day&group_by=model&user=a+b&from=2026-08-01');
  });

  it('getSeats 는 days 를 붙여 seats 를 부른다', async () => {
    const spy = vi.fn().mockResolvedValue(jsonResponse(200, { seats: [], summary: {} }));
    vi.stubGlobal('fetch', spy);
    await getSeats(14);
    expect(spy.mock.calls[0]?.[0]).toBe('/api/usage/seats?days=14');
  });

  it('getTeams 는 days 를 붙여 teams 를 부른다(기본 30)', async () => {
    const spy = vi.fn().mockResolvedValue(jsonResponse(200, { teams: [] }));
    vi.stubGlobal('fetch', spy);
    await getTeams();
    expect(spy.mock.calls[0]?.[0]).toBe('/api/usage/teams?days=30');
  });

  /*
   * runtime 축 — **조용히 버려지는 것**을 막는 테스트다.
   *
   * 왜 이 축만 따로 못박나: 호출부는 스코프를 스프레드(`{...base}`)로 넘기는데, 그러면
   * TypeScript 의 초과 속성 검사가 돌지 않아 질의 조립부가 그 키를 안 읽어도 **컴파일이
   * 통과한다.** 실제로 이 레포에서 user 축이 그렇게 사라진 적이 있고(api.ts 의 getSessions
   * 주석), runtime 을 붙일 때도 SeriesParams 가 RuntimeParams 를 안 상속해 같은 일이 났다.
   * 타입이 못 잡으므로 질의 문자열을 직접 본다.
   */
  it('series 질의에 runtime 이 실린다(스프레드로 넘겨도 사라지지 않는다)', () => {
    const base = { metric: 'tokens' as const, groupBy: 'model' as const, runtime: 'local' };
    expect(seriesQuery({ ...base, interval: 'day' })).toContain('runtime=local');
  });

  it('runtime 미지정이면 키 자체를 만들지 않는다(=전체)', () => {
    // 빈 값(`runtime=`)을 보내면 서버가 400 을 낸다 — 그래서 키를 아예 붙이지 않는다.
    expect(seriesQuery({ metric: 'tokens', runtime: '' })).not.toContain('runtime');
    expect(seriesQuery({ metric: 'tokens' })).not.toContain('runtime');
  });

  it('getSessions 는 platform·runtime·user 세 축을 함께 싣는다', async () => {
    const spy = vi.fn().mockResolvedValue(jsonResponse(200, { sessions: [] }));
    vi.stubGlobal('fetch', spy);
    await getSessions({ platform: 'codex', runtime: 'local', user: 'kim' });
    const url = String(spy.mock.calls[0]?.[0]);
    expect(url).toContain('platform=codex');
    expect(url).toContain('runtime=local');
    expect(url).toContain('user=kim');
  });

  it('seats 질의에도 runtime 이 실린다(scopeSuffix 경로)', async () => {
    const spy = vi.fn().mockResolvedValue(jsonResponse(200, { seats: [], summary: {} }));
    vi.stubGlobal('fetch', spy);
    await getSeats(30, undefined, { runtime: 'local' });
    expect(String(spy.mock.calls[0]?.[0])).toContain('runtime=local');
  });
});

describe('lib/api — 인증(세션)', () => {
  beforeEach(() => {
    setUnauthorizedHandler(() => {});
  });

  it('login 은 자격증명을 JSON 바디로 POST 하고 user 를 돌려준다', async () => {
    const spy = vi.fn().mockResolvedValue(
      jsonResponse(200, { ok: true, user: { username: 'admin', role: 'admin', tenant: 'acme' } }),
    );
    vi.stubGlobal('fetch', spy);

    await expect(login('admin', 'pw-123456')).resolves.toEqual({
      username: 'admin', role: 'admin', tenant: 'acme',
    });

    const [url, init] = spy.mock.calls[0]!;
    expect(url).toBe('/api/auth/login');
    expect(init).toMatchObject({ method: 'POST', credentials: 'include' });
    expect((init as RequestInit).headers).toMatchObject({ 'Content-Type': 'application/json' });
    expect(JSON.parse((init as RequestInit).body as string)).toEqual({ username: 'admin', password: 'pw-123456' });
  });

  it('login 401 은 ApiError(401) 로 던지고 전역 401 훅을 부르지 않는다', async () => {
    const onUnauth = vi.fn();
    setUnauthorizedHandler(onUnauth);
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(401, { ok: false, error: '자격증명 오류' })));

    const err = await failureOf(login('admin', 'nope'));
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(401);
    expect(onUnauth).not.toHaveBeenCalled(); // 폼 위에 띄운다 — 게이트 훅으로 새어 나가면 안 된다
  });

  it('getMe 200 은 사용자를 돌려준다', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(
      jsonResponse(200, { username: 'bob', role: 'member', tenant: 'acme' }),
    ));
    await expect(getMe()).resolves.toEqual({ username: 'bob', role: 'member', tenant: 'acme' });
  });

  it('getMe 401 은 던지되 전역 401 훅을 부르지 않는다 (=로그인 안 됨은 정상 답)', async () => {
    const onUnauth = vi.fn();
    setUnauthorizedHandler(onUnauth);
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(401, {})));

    const err = await failureOf(getMe());
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(401);
    expect(onUnauth).not.toHaveBeenCalled();
  });

  it('logout 은 POST 하고 204(본문 없음)에도 죽지 않는다', async () => {
    const spy = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal('fetch', spy);
    await expect(logout()).resolves.toBeDefined();
    const [url, init] = spy.mock.calls[0]!;
    expect(url).toBe('/api/auth/logout');
    expect(init).toMatchObject({ method: 'POST', credentials: 'include' });
  });
});
