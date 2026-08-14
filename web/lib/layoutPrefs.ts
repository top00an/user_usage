'use client';

/*
 * 대시보드 자유 캔버스 배치의 **서버 저장**(유저별).
 *
 * ── 화면의 진실은 이 훅의 state, 저장은 그 뒤를 따라간다 ────────────────────
 *
 * 배치를 서버 응답이 오고 나서야 바꾸면, 저장이 느리거나 실패하는 순간 **패널을 끌었는데
 * 아무 일도 일어나지 않는** 화면이 된다. 사람은 그것을 "저장 실패"가 아니라 "고장"으로 읽는다.
 * 옛 DragGrid.tsx 의 세션 한정 폴백(저장이 막힌 브라우저에서도 그 세션 동안은 재배치가
 * 동작하던 자리)이 같은 판단이었다 — 저장이 안 되는 것과 화면이 죽어 보이는 것은 다른 문제다.
 * 그래서 save()/reset() 은 **먼저 로컬 배치를 바꾸고** 네트워크는 뒤에서 돌린다. 실패하면
 * 되돌리지 않고 status='error' 로 사실만 말한다(다음 방문에는 서버 값이 돌아온다는 뜻이다).
 *
 * ── 첫 프레임부터 옳아야 한다 ─────────────────────────────────────────────
 *
 * GET 이 끝나기 전에 캔버스를 그리면 기본 배치를 한 프레임 보여 준 뒤 저장된 배치로 튄다.
 * 그 튐은 사람에게 "패널이 혼자 움직였다"로 보인다(옛 DragGrid.tsx 머리말이 순서로 겪은
 * 사고와 같다). 그래서 `ready` 를 내보내고, 호출부는 그때까지 캔버스를 그리지 않는다.
 *
 * ── 비-200 은 전부 "저장된 것 없음" 이다 ──────────────────────────────────
 *
 * 계약 개정 5: readOnly(remote) 배포에는 이 엔드포인트가 아예 없어 404 다. 구버전 서버·
 * 로그아웃·프록시 오류도 마찬가지로 여기 걸린다. 어느 경우든 **기본 배치로 살아난다** —
 * 저장을 못 하는 것이 대시보드를 못 보는 이유가 되어서는 안 된다.
 * 다만 401 은 삼키지 않는다: lib/api.ts 의 request 가 전역 401 훅(셸의 로그인 복귀)을 이미
 * 부르고 있고, 여기서 skipUnauthorizedHook 을 켜면 그 복구 경로가 조용히 끊긴다.
 */
import { useCallback, useEffect, useRef, useState } from 'react';
import { request } from '@/lib/api';
import { parseLayout, sameLayout, type DashLayout } from '@/lib/dashLayout';

/*
 * 엔드포인트 호출을 lib/api.ts 가 아니라 여기 두는 이유는 하나다 — 이번 웨이브에서 api.ts 는
 * 내 소유 파일이 아니다(계약 §6). 규약은 그대로 따른다: fetch 를 직접 부르지 않고 request()
 * 를 지나며, 그래서 자격증명·401 훅·ApiError shape 이 다른 조회와 한 벌이다.
 * ▷ PM 이 api.ts 를 열면 아래 세 호출은 그 파일의 엔드포인트 절로 옮기는 편이 낫다.
 */
export const LAYOUT_PATH = '/api/me/dashboard-layout';

/**
 * 저장을 미루는 시간. 키보드 화살표는 **누를 때마다 커밋**이라(CanvasGrid), 디바운스가 없으면
 * 한 번 옮기는 동안 PUT 이 열 번 나간다. 계약이 요구하는 하한은 600ms 다.
 */
export const SAVE_DEBOUNCE_MS = 600;

export type SaveStatus = 'idle' | 'saving' | 'saved' | 'error';

interface LayoutResponse { layout?: unknown; updatedAt?: unknown }

export interface DashLayoutPrefs {
  /** null = 저장된 것 없음 → 호출부가 기본 배치(defaultBox)로 그린다. */
  layout: DashLayout | null;
  /** 배치 변경. 로컬은 즉시 바뀌고 PUT 은 디바운스로 나간다. */
  save: (l: DashLayout) => void;
  /** 저장을 지워 기본 배치로 되돌린다(DELETE). */
  reset: () => void;
  status: SaveStatus;
  /**
   * GET 이 끝났는가. **계약의 4개 필드에 대한 추가**다 — 이것이 없으면 호출부가 저장된 배치를
   * 받기 전에 캔버스를 그려 기본 배치를 한 프레임 보여 준다(위 머리말).
   */
  ready: boolean;
}

export function useDashLayout(): DashLayoutPrefs {
  const [layout, setLayout] = useState<DashLayout | null>(null);
  const [ready, setReady] = useState(false);
  const [status, setStatus] = useState<SaveStatus>('idle');

  /** 서버가 지금 들고 있다고 믿는 값. 같은 값이면 PUT 하지 않는다. */
  const serverRef = useRef<DashLayout | null>(null);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const pendingRef = useRef<DashLayout | null>(null);
  /**
   * 요청을 **보낸 순서대로** 보낸다. PUT 과 DELETE 가 겹쳐 서버에 뒤집혀 도착하면
   * "되돌리기를 눌렀는데 다음 방문에 옛 배치가 살아 있다"가 된다 — 화면에는 아무 증상도
   * 남지 않아 사용자는 되돌리기가 고장 났다고만 안다.
   */
  const chainRef = useRef<Promise<void>>(Promise.resolve());
  /**
   * 가장 최근 조작의 번호. 늦게 도착한 옛 응답이 새 조작의 status 를 덮어쓰지 않게 한다
   * (드래그 직후 또 드래그하면 앞 PUT 의 '저장됨'이 뒤 조작의 '저장 중…'을 지운다).
   */
  const opRef = useRef(0);
  const aliveRef = useRef(true);

  useEffect(() => {
    aliveRef.current = true;
    return () => { aliveRef.current = false; };
  }, []);

  /* 마운트 1회 — 저장된 배치를 읽는다. */
  useEffect(() => {
    const controller = new AbortController();
    let cancelled = false;

    request<LayoutResponse>(LAYOUT_PATH, { signal: controller.signal })
      .then((res) => {
        if (cancelled) return;
        // 서버를 믿지 않는다: 배열이 아니면 parseLayout 이 null(=저장된 적 없음)로 접고,
        // 깨진 항목만 버린 나머지는 살린다.
        const saved = parseLayout(res?.layout);
        serverRef.current = saved;
        setLayout(saved);
        setReady(true);
      })
      .catch(() => {
        if (cancelled) return;
        serverRef.current = null;
        setLayout(null);
        setReady(true);   // 실패도 "다 읽었다"이다 — 여기서 멈추면 화면이 영영 로딩이다.
      });

    return () => { cancelled = true; controller.abort(); };
  }, []);

  /** 요청 하나를 순서대로 태우고, 그 결과로 status 를 옮긴다. */
  const enqueue = useCallback((gen: number, run: () => Promise<void>) => {
    chainRef.current = chainRef.current.then(async () => {
      try {
        await run();
        if (aliveRef.current && opRef.current === gen) setStatus('saved');
      } catch {
        // 실패해도 로컬 배치는 그대로다. 사실만 말한다.
        if (aliveRef.current && opRef.current === gen) setStatus('error');
      }
    });
  }, []);

  const put = useCallback((next: DashLayout) => request<unknown>(LAYOUT_PATH, {
    method: 'PUT',
    body: { layout: next },
  }).then(() => { serverRef.current = next; }), []);

  const save = useCallback((next: DashLayout) => {
    setLayout(next);                       // 로컬이 먼저다(위 머리말).
    if (sameLayout(next, serverRef.current)) {
      // 서버에 이미 같은 값이 있다 — 보낼 것이 없다. 상태를 '저장 중'으로 두면 영영 안 끝난다.
      if (timerRef.current) { clearTimeout(timerRef.current); timerRef.current = null; }
      pendingRef.current = null;
      opRef.current += 1;
      setStatus('saved');
      return;
    }
    pendingRef.current = next;
    const gen = ++opRef.current;
    setStatus('saving');                   // 사람이 기다리는 동안 화면이 아무 말도 안 하지 않게.
    if (timerRef.current) clearTimeout(timerRef.current);
    timerRef.current = setTimeout(() => {
      timerRef.current = null;
      const payload = pendingRef.current;
      pendingRef.current = null;
      if (!payload) return;
      enqueue(gen, () => put(payload));
    }, SAVE_DEBOUNCE_MS);
  }, [enqueue, put]);

  const reset = useCallback(() => {
    // 대기 중인 PUT 을 먼저 버린다 — 안 그러면 DELETE 뒤에 옛 배치가 다시 저장된다.
    if (timerRef.current) { clearTimeout(timerRef.current); timerRef.current = null; }
    pendingRef.current = null;
    setLayout(null);                       // null 이지 [] 가 아니다: "저장된 것 없음"과 "저장했는데 비었다"는 다르다.
    const gen = ++opRef.current;
    setStatus('saving');
    enqueue(gen, () => request<unknown>(LAYOUT_PATH, { method: 'DELETE' })
      .then(() => { serverRef.current = null; }));
  }, [enqueue]);

  /*
   * 언마운트 시 대기 중인 저장을 흘려보낸다(탭을 옮기면 이 컴포넌트는 사라진다).
   * 안 보내면 "마지막으로 옮긴 한 번만 저장이 안 되는" 사고가 되고, 그건 사람이 재현하지
   * 못한다. 이 경로는 화면이 없으므로 상태는 건드리지 않는다 — 결과를 볼 사람이 없다.
   */
  useEffect(() => () => {
    if (timerRef.current) { clearTimeout(timerRef.current); timerRef.current = null; }
    const payload = pendingRef.current;
    pendingRef.current = null;
    if (payload) { chainRef.current = chainRef.current.then(() => put(payload)).catch(() => {}); }
  }, [put]);

  return { layout, save, reset, status, ready };
}
