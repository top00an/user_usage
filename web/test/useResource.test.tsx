import { describe, it, expect, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { useResource } from '@/hooks/useResource';
import { ApiError } from '@/lib/api';

/*
 * 낡은 응답 폐기는 이 앱에서 **가장 비싼 사고를 막는 장치**다 —
 * 화면이 틀린 값을 보여주는데 아무 에러도 나지 않는 종류의 사고.
 * 그래서 세 겹(abort · cancelled 플래그 · 요청 키 대조)을 각각 따로 잡는다.
 */

function Probe({ loader, deps }: { loader: (o: { signal: AbortSignal }) => Promise<string>; deps: string[] }) {
  const { state } = useResource(loader, deps);
  return <div data-testid="out">{state.status === 'ready' ? state.data : state.status}</div>;
}

describe('useResource — 낡은 응답 폐기', () => {
  it('언마운트되면 abort 를 건다', async () => {
    let signal: AbortSignal | null = null;
    const loader = ({ signal: s }: { signal: AbortSignal }) => {
      signal = s;
      return new Promise<string>(() => {});   // 영원히 안 온다
    };
    const { unmount } = render(<Probe loader={loader} deps={[]} />);
    await waitFor(() => expect(signal).not.toBeNull());
    expect(signal!.aborted).toBe(false);
    unmount();
    expect(signal!.aborted).toBe(true);
  });

  /*
   * abort 만으로는 부족하다. 이미 응답 본문까지 받아 마이크로태스크에 올라간 resolve 는
   * abort 로 막히지 않는다 — 그 늦은 resolve 가 setState 를 부르면 화면이 덮인다.
   * 여기서는 signal 을 **무시하는** 로더로 그 상황을 정확히 재현한다.
   */
  it('signal 을 무시하고 늦게 resolve 해도 화면을 덮지 않는다', async () => {
    let resolveOld: ((v: string) => void) | null = null;
    const loader = () => new Promise<string>((r) => { resolveOld = r; });

    const { unmount } = render(<Probe loader={loader} deps={['old']} />);
    await waitFor(() => expect(resolveOld).not.toBeNull());
    unmount();

    // 언마운트 뒤 도착 — setState 가 불리면 React 가 경고하고, 살아 있는 화면이면 값이 바뀐다.
    const warn = vi.spyOn(console, 'error').mockImplementation(() => {});
    resolveOld!('낡은 값');
    await new Promise((r) => setTimeout(r, 0));
    expect(warn).not.toHaveBeenCalled();
    warn.mockRestore();
  });

  it('deps 가 바뀌면 앞 요청을 끊고 새 결과만 남긴다', async () => {
    const signals: AbortSignal[] = [];
    const resolvers: ((v: string) => void)[] = [];
    const loader = ({ signal }: { signal: AbortSignal }) => {
      signals.push(signal);
      return new Promise<string>((r) => resolvers.push(r));
    };

    const { rerender } = render(<Probe loader={loader} deps={['a']} />);
    await waitFor(() => expect(resolvers).toHaveLength(1));

    rerender(<Probe loader={loader} deps={['b']} />);
    await waitFor(() => expect(resolvers).toHaveLength(2));
    expect(signals[0]!.aborted).toBe(true);

    // 앞 요청이 뒤늦게 도착 → 무시돼야 한다
    resolvers[0]!('앞 결과');
    await new Promise((r) => setTimeout(r, 0));
    expect(screen.getByTestId('out').textContent).toBe('loading');

    resolvers[1]!('뒤 결과');
    await waitFor(() => expect(screen.getByTestId('out').textContent).toBe('뒤 결과'));
  });

  it('취소(AbortError)는 error 상태로 넘기지 않는다', async () => {
    const loader = () => Promise.reject(new ApiError('취소', 0, {}, true));
    render(<Probe loader={loader} deps={[]} />);
    await new Promise((r) => setTimeout(r, 10));
    expect(screen.getByTestId('out').textContent).toBe('loading');
  });

  it('진짜 실패는 error 상태가 된다', async () => {
    const loader = () => Promise.reject(new ApiError('서버 오류', 500, {}));
    render(<Probe loader={loader} deps={[]} />);
    await waitFor(() => expect(screen.getByTestId('out').textContent).toBe('error'));
  });
});
