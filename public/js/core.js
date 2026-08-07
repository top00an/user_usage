'use strict';
/*
 * 뷰가 공유하는 것 전부 — 상태 · 이스케이프 · api · 토스트 · 모달.
 *
 * 여기서는 **아무것도 import 하지 않는다.** 이 파일이 다른 모듈을 부르기 시작하면 뷰 → core →
 * 뷰 의 순환이 생기고, 그때 브라우저는 에러 대신 `undefined` 를 넘긴다(조용한 고장).
 * 반대 방향(뷰 → core)만 허용한다.
 */

export const app = document.getElementById('app');

// ME 는 라이브 바인딩으로 내보낸다 — import 한 쪽은 항상 최신 값을 본다(대입은 setME 로만).
export let ME = null;          // { username, role }
export function setME(u) { ME = u; }

/* HTML 이스케이프. 뷰는 문자열 템플릿으로 마크업을 만들므로 **서버에서 온 값은 전부 이걸 통과한다.** */
export const esc = (s) => String(s == null ? '' : s).replace(
  /[&<>"']/g,
  (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]),
);

/* 서버는 UTC ISO(...Z)로 저장한다 → 화면은 브라우저 로컬시간으로 보여준다. */
export function fmtTime(iso) {
  if (!iso) return '-';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return String(iso);
  const p = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} `
    + `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

// 타이머는 하나만 — 토스트마다 걸면 앞선 타이머가 뒤늦은 토스트를 조기에 지운다.
let toastTimer = null;
export function toast(msg, kind) {
  const t = document.getElementById('toast');
  if (!t) return;
  t.textContent = msg;
  t.className = 'glass-strong show ' + (kind || '');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { t.className = 'glass-strong'; }, 3400);
}

/*
 * 401 처리(=토큰 게이트 복귀)는 셸(app.js)이 주입한다. core 가 셸을 import 하면 순환이 되고,
 * "토큰 지우기"도 결국 같은 동작이라 이 훅 하나로 모은다.
 */
let unauthorizedHandler = () => {};
export function setUnauthorizedHandler(fn) { unauthorizedHandler = fn; }
export function onUnauthorized() { ME = null; unauthorizedHandler(); }

/*
 * 실패 응답 → Error. 호출부가 문자열을 정규식으로 뜯지 않아도 되게 **구조를 남긴다**:
 *   e.message — 서버 error 문구(없으면 'HTTP <code>')
 *   e.status  — 403(권한 없음)과 5xx(연결 실패)를 구분해야 하는 호출부가 있다
 *   e.body    — 서버가 함께 보낸 응답 바디 전체
 * 에러 문구는 사람이 읽는 글이라 언제든 다듬어진다. 분기를 문구에 걸면 그때 화면이 조용히
 * 틀린 쪽으로 넘어간다 — 분기는 문자열이 아니라 구조로 한다.
 */
function fail(body, status, dfltMsg) {
  const j = body && typeof body === 'object' ? body : {};
  const e = new Error(j.error || dfltMsg || ('HTTP ' + status));
  e.status = status;
  e.body = j;
  return e;
}

/*
 * 유일한 서버 호출구.
 *
 * 자격증명은 쿠키(usage_tok)로 실린다 — 브라우저가 알아서 붙이므로 뷰가 토큰을 알 필요가 없다.
 * 401 은 던지기 **전에** onUnauthorized() 를 부른다: 토큰이 틀리거나 서버가 재기동돼 토큰이
 * 바뀌었을 때, 화면이 빈 카드로 남지 않고 게이트로 돌아가야 하기 때문이다.
 */
export async function api(path, opts) {
  const o = Object.assign({}, opts);
  o.headers = Object.assign({ 'Content-Type': 'application/json' }, o.headers || {});
  const r = await fetch(path, o);
  let j = {};
  try { j = await r.json(); } catch { /* 본문이 JSON 이 아니면 빈 객체로 둔다 */ }
  if (r.status === 401) { onUnauthorized(); throw fail(j, r.status, 'unauthorized'); }
  if (!r.ok) throw fail(j, r.status);
  return j;
}

/*
 * 모달 위에 모달이 뜰 수 있다. 키 핸들러는 document 에 붙으므로 **맨 위 오버레이만** 반응해야
 * Escape 한 번에 둘 다 닫히지 않는다.
 */
export function isTopOverlay(ov) {
  const all = document.querySelectorAll('.modal-overlay');
  return all.length > 0 && all[all.length - 1] === ov;
}

/*
 * 공용 모달. 각 뷰가 수제로 만들던 오버레이(Esc·포커스 트랩·포커스 복원 제각각)를 하나로 모은다.
 * body 는 HTML 문자열 — **넣는 쪽에서 esc() 할 것.**
 *
 * 반환 { ov, q, close, ok, cancel }. 호출부는 ok.onclick 에서 값을 모으고 검증한 뒤 close() 를
 * 부른다(검증 실패 시 닫지 않는다). Esc·바깥 클릭·Tab 트랩·포커스 복원은 이 헬퍼가 처리한다.
 *   okLabel: null → 확인 버튼 없는 보기 전용 모달(cancelLabel 기본이 '닫기')
 *   maxWidth(px) → 기본 460px 대신 넓은 팝업
 *   onClose      → 어떤 경로로 닫혀도 **1회만** 호출되는 정리 훅
 */
export function openModal({
  title = '', body = '', okLabel = '저장', cancelLabel, danger = false,
  ariaLabel, maxWidth, onClose,
} = {}) {
  const prev = document.activeElement;
  const showOk = okLabel !== null;
  const cancelText = cancelLabel ?? (showOk ? '취소' : '닫기');

  const ov = document.createElement('div');
  ov.className = 'modal-overlay';
  ov.innerHTML = `
    <div class="modal glass-strong" role="dialog" aria-modal="true"
      aria-label="${esc(ariaLabel ?? title)}"${maxWidth ? ` style="max-width:${Number(maxWidth)}px"` : ''}>
      ${title ? `<h3>${esc(title)}</h3>` : ''}
      <div class="modal-body">${body}</div>
      <div class="actions">
        <button class="ghost" data-mc type="button">${esc(cancelText)}</button>
        ${showOk ? `<button class="${danger ? 'danger' : 'primary'}" data-mo type="button">${esc(okLabel)}</button>` : ''}
      </div>
    </div>`;
  document.body.appendChild(ov);
  requestAnimationFrame(() => ov.classList.add('show'));

  const q = (s) => ov.querySelector(s);
  const focusables = () => Array.from(
    ov.querySelectorAll('button, input, select, textarea, [tabindex]:not([tabindex="-1"])'),
  ).filter((el) => !el.disabled && el.offsetParent !== null);

  let closed = false;
  const close = () => {
    // Esc·닫기 버튼·바깥 클릭이 겹쳐도 정리(리스너 해제·onClose)는 1회만.
    if (closed) return;
    closed = true;
    ov.classList.remove('show');
    document.removeEventListener('keydown', onKey);
    setTimeout(() => ov.remove(), 150);
    if (prev && prev.focus) prev.focus();
    if (typeof onClose === 'function') { try { onClose(); } catch { /* 정리 실패가 닫기를 막지 않는다 */ } }
  };
  function onKey(e) {
    if (!isTopOverlay(ov)) return;
    if (e.key === 'Escape') { e.preventDefault(); close(); return; }
    if (e.key !== 'Tab') return;
    const f = focusables();
    if (!f.length) return;
    const first = f[0];
    const last = f[f.length - 1];
    if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
    else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
  }
  document.addEventListener('keydown', onKey);
  ov.addEventListener('mousedown', (e) => { if (e.target === ov) close(); });
  ov._close = close;   // 뷰 전환 정리용 — keydown 리스너까지 해제되는 경로

  const ok = showOk ? q('[data-mo]') : null;
  const cancel = q('[data-mc]');
  cancel.onclick = close;
  setTimeout(() => { const f = focusables(); if (f.length) f[0].focus(); }, 30);

  return { ov, q, close, ok, cancel };
}
