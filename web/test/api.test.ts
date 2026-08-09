import { describe, it, expect, vi, beforeEach } from 'vitest';
import {
  ApiError,
  failureKind,
  request,
  setUnauthorizedHandler,
  getSummary,
  getSessionDetail,
  seriesQuery,
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

  it('자격증명은 쿠키로 실린다 — credentials: same-origin', async () => {
    const spy = vi.fn().mockResolvedValue(jsonResponse(200, {}));
    vi.stubGlobal('fetch', spy);
    await request('/api/usage/summary');
    expect(spy.mock.calls[0]?.[1]).toMatchObject({ credentials: 'same-origin' });
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
});
