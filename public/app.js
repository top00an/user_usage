'use strict';
/*
 * 독립 기동 셸 — 두 탭(사용 추적·사용 관측)만 그린다.
 *
 * 필요한 것은 넷뿐이다:
 *   ① 토큰 게이트   서버는 Authorization 헤더 또는 쿠키 usage_tok 를 요구한다. 브라우저 fetch 는
 *                   헤더를 붙일 수 없으므로(뷰를 고치지 않는 것이 전제다) 쿠키에 담는다.
 *   ② 탭 전환       coreDashboard 의 탭 규율과 같다 — pane 을 교체하고 seq 로 낡은 응답을 버린다.
 *   ③ 401 복구      토큰이 틀리거나 만료(=서버 재기동으로 토큰 교체)되면 게이트로 되돌린다.
 *   ④ 해시 딥링크   #/usageobs 로 바로 열 수 있게. 상태가 URL 에 없으면 새로고침에 탭이 날아간다.
 *
 * 뷰가 기대하는 URL(/js/core.js·/js/router.js)은 서버의 정적 화이트리스트가 제공한다(server.js).
 */
import { esc, setUnauthorizedHandler, setME, toast } from '/js/core.js';
import { nextSeq } from '/js/router.js';

const app = document.getElementById('app');

/*
 * 토큰 보관 — 쿠키 하나다.
 *
 * localStorage 에 두고 매 요청 헤더로 붙이는 안은 뷰를 고쳐야 성립한다(뷰가 core.api 를 쓰고,
 * core.api 는 이 서비스의 토큰을 모른다). 쿠키면 브라우저가 알아서 싣는다 — 뷰가 그대로 돈다.
 *
 * SameSite=Strict: 이 쿠키는 조회 자격증명이라 교차 사이트 요청에 실릴 이유가 전혀 없다.
 * (서버도 쿠키로는 상태변경을 태우지 않는다 — 이중 방어.)
 * Secure 는 붙이지 않는다: 로컬 http 기동이 기본 시나리오라 붙이면 쿠키가 아예 저장되지 않는다.
 * 원격 노출은 이 서비스의 범위가 아니다(터널·루프백 전제 — README.md).
 */
const COOKIE = 'usage_tok';
const readToken = () => {
  const m = /(?:^|;\s*)usage_tok=([^;]*)/.exec(document.cookie || '');
  try { return m ? decodeURIComponent(m[1]) : ''; } catch { return m ? m[1] : ''; }
};
const writeToken = (t) => { document.cookie = `${COOKIE}=${encodeURIComponent(t)}; path=/; SameSite=Strict`; };
const clearToken = () => { document.cookie = `${COOKIE}=; path=/; Max-Age=0; SameSite=Strict`; };

/*
 * 뷰는 core 의 ME 를 직접 읽지 않지만(권한 분기는 서버 응답으로 한다), core.api 가 401 에서
 * ME 를 비우고 onUnauthorized 를 부른다. 채워 두면 그 경로가 일관되게 돈다.
 */
setME({ username: 'usage-admin', role: 'admin' });

const TABS = [
  { id: 'usage', label: '사용 추적', load: () => import('/views/usagetrack.js').then((m) => m.renderUsage) },
  { id: 'usageobs', label: '사용 관측', load: () => import('/views/usageobs.js').then((m) => m.renderUsageObs) },
];

const tabFromHash = () => {
  const raw = String(location.hash || '').replace(/^#\/?/, '').split(/[?&#]/)[0].trim();
  return TABS.some((t) => t.id === raw) ? raw : TABS[0].id;
};

let tab = tabFromHash();

/* ── 토큰 게이트 ─────────────────────────────────────────────────────
 * 화면 전체를 대체한다. 부분 배너로 두면 뒤의 탭이 계속 401 을 때려 토스트만 쌓인다.
 */
function renderGate(note) {
  app.innerHTML = `
    <div class="login-wrap"><div class="card glass" style="max-width:460px;width:100%">
      <h2>사용량 대시보드</h2>
      <p class="help mt-sm">이 화면은 <b>사람별 사용량과 비용</b>을 담고 있어 토큰이 필요합니다.
        서버를 띄운 셸의 <span class="mono">USAGE_ADMIN_TOKEN</span> 값을 그대로 넣으세요.</p>
      ${note ? `<div class="card mt" style="border-color:var(--err-bd)" role="alert">
        <div style="color:var(--err);font-weight:600;font-size:13px">${esc(note)}</div></div>` : ''}
      <form id="gate" class="mt">
        <label class="help" for="tok">토큰</label>
        <input id="tok" type="password" autocomplete="current-password" spellcheck="false"
          style="width:100%" aria-label="사용량 대시보드 토큰">
        <div class="row mt"><button class="primary" type="submit">열기</button></div>
      </form>
    </div></div>`;

  const form = app.querySelector('#gate');
  const input = app.querySelector('#tok');
  input.focus();
  form.onsubmit = (e) => {
    e.preventDefault();
    const t = String(input.value || '').trim();
    if (!t) { input.focus(); return; }
    writeToken(t);
    boot();
  };
}

/* ── 셸 ──────────────────────────────────────────────────────────────── */
function renderShell() {
  app.innerHTML = `
    <div style="min-height:100vh;display:flex;flex-direction:column">
      <div class="topbar">
        <div class="logo" aria-hidden="true"></div>
        <h1>사용량 대시보드</h1>
        <span class="help">독립 기동 · 조회 전용</span>
        <span class="spacer"></span>
        <button class="ghost" id="signout" type="button">토큰 지우기</button>
      </div>
      <div class="main" style="flex:1">
        <div class="view">
          <div class="tabs" id="tabs" role="tablist">
            ${TABS.map((t) => `<button type="button" role="tab" data-tab="${t.id}"
              class="tab${t.id === tab ? ' active' : ''}" aria-selected="${t.id === tab}">${t.label}</button>`).join('')}
          </div>
          <div id="body"></div>
        </div>
      </div>
    </div>`;

  app.querySelector('#signout').onclick = () => { clearToken(); renderGate('토큰을 지웠습니다.'); };
  const tabsEl = app.querySelector('#tabs');
  tabsEl.querySelectorAll('[data-tab]').forEach((b) => {
    b.onclick = () => {
      if (b.dataset.tab === tab) return;
      tab = b.dataset.tab;
      location.hash = `#/${tab}`;   // hashchange 가 paint 를 부른다
    };
  });
  return app.querySelector('#body');
}

/*
 * 탭 그리기 — 대시보드(코어 views/dashboard.js)와 같은 pane 교체 규율.
 * pane 을 새로 만들어 통째로 갈아끼우고 seq 를 올린다. 그러면 앞 탭의 늦은 응답이 도착해도
 * 그 뷰의 isStale(seq) 이 참이 되어 조용히 빠져나간다(덮어쓰기 방지).
 */
async function paint(body) {
  const tabsEl = app.querySelector('#tabs');
  if (tabsEl) {
    tabsEl.querySelectorAll('[data-tab]').forEach((b) => {
      const on = b.dataset.tab === tab;
      b.className = 'tab' + (on ? ' active' : '');
      b.setAttribute('aria-selected', String(on));
    });
  }
  const seq = nextSeq();
  const pane = document.createElement('div');
  pane.innerHTML = '<div class="muted">불러오는 중…</div>';
  body.replaceChildren(pane);

  const def = TABS.find((t) => t.id === tab);
  if (!def) return;
  try {
    const render = await def.load();
    if (pane.isConnected) await render(pane, seq);
  } catch (e) {
    // 401 은 게이트가 이미 화면을 바꿨다(pane 이 떨어져 나간다) — 그때는 아무것도 그리지 않는다.
    if (!pane.isConnected) return;
    pane.innerHTML = `<div class="card glass"><div class="muted">이 탭을 불러오지 못했습니다.</div>
      <div class="help mt-sm">${esc(String((e && e.message) || e))}</div></div>`;
  }
}

/* ── 부팅 ────────────────────────────────────────────────────────────── */
let body = null;

function boot() {
  if (!readToken()) { renderGate(); return; }
  body = renderShell();
  paint(body);
}

/*
 * 401 복구. 서버를 다시 띄우면 토큰이 바뀔 수 있고, 그때 화면은 빈 카드만 남는다 —
 * 무엇이 잘못됐는지 말해 주고 다시 넣을 자리를 준다.
 */
setUnauthorizedHandler(() => {
  clearToken();
  renderGate('토큰이 올바르지 않거나 만료되었습니다. 다시 입력하세요.');
});

window.addEventListener('hashchange', () => {
  const next = tabFromHash();
  if (next === tab && body) return;
  tab = next;
  if (body && body.isConnected) paint(body);
});

// 뷰가 toast 를 쓴다(usageobs 의 상세 조회 실패). #toast 가 없으면 거기서 죽으므로 한 번 확인한다.
if (!document.getElementById('toast')) {
  console.error('셸에 #toast 가 없다 — usageobs 의 오류 안내가 예외로 바뀐다(index.html 계약).');
} else {
  void toast; // 참조 유지(트리셰이킹 없는 환경이라 실질 무해 — 계약을 코드로 남긴다)
}

boot();
