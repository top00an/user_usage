'use client';

import { useCallback, useEffect, useState, useSyncExternalStore } from 'react';
import { setUnauthorizedHandler } from '@/lib/api';
import { clearToken, subscribeToken, tokenServerSnapshot, tokenSnapshot, writeToken } from '@/lib/token';
import { ToastProvider } from '@/components/Toast';
import TokenGate from '@/components/TokenGate';
import UsageTrackTab from '@/components/usagetrack/UsageTrackTab';
import UsageObsTab from '@/components/usageobs/UsageObsTab';

/*
 * 셸 — 필요한 것은 넷뿐이다(현행 public/app.js 와 같은 계약):
 *   ① 토큰 게이트   서버는 Authorization 헤더 또는 쿠키 usage_tok 를 요구한다. 브라우저 fetch 로
 *                   헤더를 붙이는 대신 쿠키에 담는다 — 브라우저가 알아서 싣는다.
 *   ② 탭 전환       pane 을 갈아끼우고 낡은 응답을 버린다(여기서는 key + AbortController).
 *   ③ 401 복구      토큰이 틀리거나 만료(=서버 재기동으로 토큰 교체)되면 게이트로 되돌린다.
 *   ④ 해시 딥링크   #/usageobs 로 바로 열 수 있게. 상태가 URL 에 없으면 새로고침에 탭이 날아간다.
 */

const TABS = [
  { id: 'usage', label: '사용 추적' },
  { id: 'usageobs', label: '사용 관측' },
] as const;

type TabId = (typeof TABS)[number]['id'];

function tabFromHash(hash: string): TabId {
  const raw = String(hash || '').replace(/^#\/?/, '').split(/[?&#]/)[0]?.trim() ?? '';
  return TABS.some((t) => t.id === raw) ? (raw as TabId) : TABS[0].id;
}

/*
 * 해시도 React 밖의 상태다. 토큰과 같은 이유로 외부 스토어로 읽는다 —
 * 이렇게 두면 뒤로가기·직접 입력·프로그램적 변경이 **전부 같은 한 경로**로 들어온다.
 */
function subscribeHash(cb: () => void): () => void {
  window.addEventListener('hashchange', cb);
  return () => window.removeEventListener('hashchange', cb);
}

export default function Dashboard() {
  /*
   * 정적 export 라 이 컴포넌트는 빌드 시각에 한 번 미리 그려진다. 그때는 쿠키도 해시도 없다 —
   * 서버 스냅샷을 'unknown' 으로 두어 마운트 전에는 아무 판단도 하지 않는다.
   */
  const auth = useSyncExternalStore(subscribeToken, tokenSnapshot, tokenServerSnapshot);
  const tab = useSyncExternalStore(
    subscribeHash,
    () => tabFromHash(location.hash),
    () => TABS[0].id as TabId,
  );
  const [note, setNote] = useState<string | null>(null);

  /*
   * ③ 401 복구. 서버를 다시 띄우면 토큰이 바뀔 수 있고, 그때 화면은 빈 카드만 남는다 —
   * 무엇이 잘못됐는지 말해 주고 다시 넣을 자리를 준다.
   * (clearToken 이 스토어를 흔들어 화면이 게이트로 돌아간다 — 여기서 authed 를 따로 들지 않는다.)
   */
  useEffect(() => {
    setUnauthorizedHandler(() => {
      clearToken();
      setNote('토큰이 올바르지 않거나 만료되었습니다. 다시 입력하세요.');
    });
    return () => setUnauthorizedHandler(() => {});
  }, []);

  const openWith = useCallback((token: string) => {
    setNote(null);
    writeToken(token);
  }, []);

  const signOut = useCallback(() => {
    clearToken();
    setNote('토큰을 지웠습니다.');
  }, []);

  const selectTab = useCallback((id: TabId) => {
    // 해시가 단일 출처다 — 상태를 따로 들면 뒤로가기가 화면과 어긋난다.
    location.hash = `#/${id}`;
  }, []);

  if (auth === 'unknown') return <div className="login-wrap"><div className="muted">로딩 중…</div></div>;
  if (auth === 'no') return <TokenGate note={note} onSubmit={openWith} />;

  return (
    <ToastProvider>
      <a className="skip-link" href="#tabpanel">본문으로 건너뛰기</a>
      <div style={{ minHeight: '100dvh', display: 'flex', flexDirection: 'column' }}>
        <header className="topbar">
          <div className="logo" aria-hidden="true" />
          <h1>사용량 대시보드</h1>
          <span className="help">독립 기동 · 조회 전용</span>
          <span className="spacer" />
          <button className="ghost" id="signout" type="button" onClick={signOut}>토큰 지우기</button>
        </header>

        <div className="main" style={{ flex: 1 }}>
          <div className="view">
            <div className="tabs" role="tablist" aria-label="화면">
              {TABS.map((t) => {
                const on = t.id === tab;
                return (
                  <button
                    key={t.id}
                    id={`shelltab-${t.id}`}
                    type="button"
                    role="tab"
                    className="tab"
                    aria-selected={on}
                    aria-controls="tabpanel"
                    tabIndex={on ? 0 : -1}
                    onClick={() => selectTab(t.id)}
                    onKeyDown={(e) => {
                      if (e.key !== 'ArrowRight' && e.key !== 'ArrowLeft') return;
                      e.preventDefault();
                      const i = TABS.findIndex((x) => x.id === tab);
                      const next = TABS[(i + (e.key === 'ArrowRight' ? 1 : -1) + TABS.length) % TABS.length]!;
                      selectTab(next.id);
                      requestAnimationFrame(() => document.getElementById(`shelltab-${next.id}`)?.focus());
                    }}
                  >
                    {t.label}
                  </button>
                );
              })}
            </div>

            {/*
              ② key 로 탭마다 트리를 통째로 새로 만든다 — 현행의 "pane 을 갈아끼운다"와 같은 뜻이다.
              앞 탭의 useEffect 정리 함수가 그 자리에서 돌아 진행 중인 요청이 abort 되고,
              늦게 도착한 응답은 정리 플래그에 걸려 버려진다(hooks/useResource.ts).
            */}
            <div id="tabpanel" role="tabpanel" aria-labelledby={`shelltab-${tab}`} tabIndex={-1}>
              {tab === 'usage' ? <UsageTrackTab key="usage" /> : <UsageObsTab key="usageobs" />}
            </div>
          </div>
        </div>
      </div>
    </ToastProvider>
  );
}
