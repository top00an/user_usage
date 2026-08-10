'use client';

import { useCallback, useEffect, useState, useSyncExternalStore } from 'react';
import { getMe, isAborted, logout, setUnauthorizedHandler, type AuthUser } from '@/lib/api';
import { ToastProvider } from '@/components/Toast';
import Login from '@/components/Login';
import GrafanaDash from '@/components/grafana/GrafanaDash';
import UsageTrackTab from '@/components/usagetrack/UsageTrackTab';
import UsageObsTab from '@/components/usageobs/UsageObsTab';
import Onboarding from '@/components/Onboarding';
import Architecture from '@/components/Architecture';

/*
 * 셸 — 필요한 것은 넷뿐이다:
 *   ① 로그인 게이트  진입 시 getMe() 로 세션을 확인한다. 401 이면 로그인 화면, 성공이면 대시보드.
 *                   자격증명은 서버가 내린 httpOnly 세션 쿠키로 실린다 — 셸은 토큰을 만지지 않는다.
 *   ② 탭 전환       pane 을 갈아끼우고 낡은 응답을 버린다(여기서는 key + AbortController).
 *   ③ 401 복구      세션이 만료되거나 서버 재기동으로 끊기면 로그인 화면으로 되돌린다.
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
  {
    id: 'onboarding',
    label: '연동',
    desc: '개발자 머신 연동 — 인제스트 키 발급 · 원라인 설치 명령 · 키 관리',
    icon: 'M10 13a5 5 0 0 0 7 0l3-3a5 5 0 0 0-7-7l-1.5 1.5M14 11a5 5 0 0 0-7 0l-3 3a5 5 0 0 0 7 7l1.5-1.5', // 링크/체인
  },
  {
    id: 'architecture',
    label: '아키텍처',
    desc: '작동 방식 — 인제스트 키로 데이터가 수집 · 연동 · 수신되는 구성도와 흐름 설명',
    icon: 'M4 5h6v6H4V5Zm10 8h6v6h-6v-6ZM7 11v2m0 0h10m0 0v2M7 13H7', // 노드-연결 도식
  },
] as const;

/** 관리자 전용 탭 — member 는 목록에서 숨기고, 딥링크로도 열리지 않게 렌더에서도 막는다. */
const ADMIN_ONLY_TABS = new Set<TabId>(['onboarding', 'architecture']);

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

/*
 * 세션 상태. 정적 export 라 마운트 전에는 서버를 물어볼 수 없다 — 'loading' 으로 두고
 * 마운트 뒤 getMe() 한 번으로 판정한다. 'no' 로 시작하면 로그인한 사람에게 로그인 화면이
 * 한 프레임 번쩍인다.
 */
type Auth =
  | { status: 'loading' }
  | { status: 'out' }
  | { status: 'in'; user: AuthUser };

export default function Dashboard() {
  const [auth, setAuth] = useState<Auth>({ status: 'loading' });
  const [note, setNote] = useState<string | null>(null);

  const tab = useSyncExternalStore(
    subscribeHash,
    () => tabFromHash(location.hash),
    () => TABS[0].id as TabId,
  );

  /* ① 진입 시 세션 확인 — 401 이면 로그인, 성공이면 대시보드. */
  useEffect(() => {
    const ac = new AbortController();
    getMe({ signal: ac.signal })
      .then((user) => setAuth({ status: 'in', user }))
      .catch((e) => {
        if (isAborted(e)) return; // 언마운트로 인한 취소는 상태를 건드리지 않는다.
        setAuth({ status: 'out' });
      });
    return () => ac.abort();
  }, []);

  /*
   * ③ 401 복구. 세션이 만료되거나 서버 재기동으로 끊기면 데이터 요청이 401 을 물고 오고,
   * 그때 화면은 빈 카드만 남는다 — 로그인 화면으로 되돌리고 무슨 일이 있었는지 말해 준다.
   */
  useEffect(() => {
    setUnauthorizedHandler(() => {
      setAuth({ status: 'out' });
      setNote('세션이 만료되었습니다. 다시 로그인하세요.');
    });
    return () => setUnauthorizedHandler(() => {});
  }, []);

  const onLoggedIn = useCallback((user: AuthUser) => {
    setNote(null);
    setAuth({ status: 'in', user });
  }, []);

  const signOut = useCallback(async () => {
    // 서버 세션 파기를 시도한다. 실패해도 화면은 로그인으로 돌린다 — 이미 나가려는 사용자다.
    try {
      await logout();
    } catch {
      /* 무시 */
    }
    setNote(null);
    setAuth({ status: 'out' });
  }, []);

  const selectTab = useCallback((id: TabId) => {
    // 해시가 단일 출처다 — 상태를 따로 들면 뒤로가기가 화면과 어긋난다.
    location.hash = `#/${id}`;
  }, []);

  if (auth.status === 'loading') return <div className="login-wrap"><div className="muted">로딩 중…</div></div>;
  if (auth.status === 'out') return <Login note={note} onSuccess={onLoggedIn} />;

  const { user } = auth;
  const isAdmin = user.role === 'admin';
  const roleLabel = isAdmin ? '관리자' : '구성원';
  // 관리자 전용 탭은 member 목록에서 뺀다. 딥링크로 #/onboarding 에 온 member 는 여기 없어
  // active 가 첫 탭으로 접히고, 아래 패널도 렌더되지 않는다(이중 방어).
  const visibleTabs = TABS.filter((t) => isAdmin || !ADMIN_ONLY_TABS.has(t.id));
  const active = visibleTabs.find((t) => t.id === tab) ?? visibleTabs[0]!;

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
            {visibleTabs.map((t) => {
              const on = t.id === active.id;
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
                    const i = visibleTabs.findIndex((x) => x.id === active.id);
                    const next = visibleTabs[(i + (e.key === 'ArrowDown' ? 1 : -1) + visibleTabs.length) % visibleTabs.length]!;
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
            <span
              className={`badge ${user.role === 'admin' ? 'ok' : ''}`}
              title={`${user.username} · ${user.tenant}`}
            >
              {roleLabel}
            </span>
            <button className="ghost" id="signout" type="button" onClick={signOut}>로그아웃</button>
          </div>
        </aside>

        {/* ── 본문 ── */}
        <main className="content">
          <header className="content-head">
            <div>
              <h1>{active.label}</h1>
              <p className="content-desc">{active.desc}</p>
            </div>
            {/* 탭별 상단 액션 슬롯 — GrafanaDash 가 포털로 버튼을 얹는다(제목과 같은 줄 우측). */}
            <div id="head-actions" className="head-actions" />
          </header>

          {/*
            ② key 로 탭마다 트리를 통째로 새로 만든다 — 앞 탭의 useEffect 정리 함수가 돌아
            진행 중인 요청이 abort 되고, 늦게 도착한 응답은 버려진다(hooks/useResource.ts).
          */}
          <div id="tabpanel" role="tabpanel" aria-labelledby={`shelltab-${active.id}`} tabIndex={-1}>
            {active.id === 'overview' && <GrafanaDash key="overview" />}
            {active.id === 'usage' && <UsageTrackTab key="usage" />}
            {active.id === 'usageobs' && <UsageObsTab key="usageobs" />}
            {active.id === 'onboarding' && <Onboarding key="onboarding" />}
            {active.id === 'architecture' && <Architecture key="architecture" />}
          </div>
        </main>
      </div>
    </ToastProvider>
  );
}
