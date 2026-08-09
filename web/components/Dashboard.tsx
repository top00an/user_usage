'use client';

import { useCallback, useEffect, useState, useSyncExternalStore } from 'react';
import { setUnauthorizedHandler } from '@/lib/api';
import { clearToken, subscribeToken, tokenServerSnapshot, tokenSnapshot, writeToken } from '@/lib/token';
import { ToastProvider } from '@/components/Toast';
import TokenGate from '@/components/TokenGate';
import GrafanaDash from '@/components/grafana/GrafanaDash';
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
  {
    id: 'overview',
    label: '대시보드',
    desc: '실시간 메트릭 — 비용 · 토큰 · 캐시 · 도구 (드래그로 패널 재배치)',
    icon: 'M3 3h8v8H3V3Zm10 0h8v5h-8V3ZM3 13h8v8H3v-8Zm10 3h8v5h-8v-5Z', // 대시보드 그리드
  },
  {
    id: 'usage',
    label: '사용 추적',
    desc: '누가 · 무엇을 · 얼마나 — 총계 · 일별 추이 · 모델별',
    icon: 'M3 13h4v7H3v-7Zm7-9h4v16h-4V4Zm7 5h4v11h-4V9Z', // 막대 그래프
  },
  {
    id: 'usageobs',
    label: '사용 관측',
    desc: '왜 그 숫자인가 — 비용 · 좌석 · 팀 · 분포',
    icon: 'M12 3a9 9 0 1 0 9 9h-9V3Z M13 3a9 9 0 0 1 8 8h-8V3Z', // 도넛/분해
  },
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

  const active = TABS.find((t) => t.id === tab) ?? TABS[0];

  return (
    <ToastProvider>
      <a className="skip-link" href="#tabpanel">본문으로 건너뛰기</a>
      <div className="app-shell">
        {/* ── 좌측 내비게이션 레일 ── */}
        <aside className="sidebar">
          <div className="brand">
            <span className="brand-mark" aria-hidden="true" />
            <span className="brand-name">사용량<br />대시보드</span>
          </div>

          <nav className="side-nav" role="tablist" aria-orientation="vertical" aria-label="화면">
            {TABS.map((t) => {
              const on = t.id === tab;
              return (
                <button
                  key={t.id}
                  id={`shelltab-${t.id}`}
                  type="button"
                  role="tab"
                  className="side-item"
                  aria-selected={on}
                  aria-controls="tabpanel"
                  tabIndex={on ? 0 : -1}
                  onClick={() => selectTab(t.id)}
                  onKeyDown={(e) => {
                    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
                    e.preventDefault();
                    const i = TABS.findIndex((x) => x.id === tab);
                    const next = TABS[(i + (e.key === 'ArrowDown' ? 1 : -1) + TABS.length) % TABS.length]!;
                    selectTab(next.id);
                    requestAnimationFrame(() => document.getElementById(`shelltab-${next.id}`)?.focus());
                  }}
                >
                  <svg className="side-icon" viewBox="0 0 24 24" aria-hidden="true">
                    <path d={t.icon} />
                  </svg>
                  <span>{t.label}</span>
                </button>
              );
            })}
          </nav>

          <div className="side-foot">
            <span className="badge ok" title="이 서버는 데이터를 쓰지 않고 조회만 합니다">조회 전용</span>
            <button className="ghost" id="signout" type="button" onClick={signOut}>토큰 지우기</button>
          </div>
        </aside>

        {/* ── 본문 ── */}
        <main className="content">
          <header className="content-head">
            <div>
              <h1>{active.label}</h1>
              <p className="content-desc">{active.desc}</p>
            </div>
          </header>

          {/*
            ② key 로 탭마다 트리를 통째로 새로 만든다 — 앞 탭의 useEffect 정리 함수가 돌아
            진행 중인 요청이 abort 되고, 늦게 도착한 응답은 버려진다(hooks/useResource.ts).
          */}
          <div id="tabpanel" role="tabpanel" aria-labelledby={`shelltab-${tab}`} tabIndex={-1}>
            {tab === 'overview' && <GrafanaDash key="overview" />}
            {tab === 'usage' && <UsageTrackTab key="usage" />}
            {tab === 'usageobs' && <UsageObsTab key="usageobs" />}
          </div>
        </main>
      </div>
    </ToastProvider>
  );
}
