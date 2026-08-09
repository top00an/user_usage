'use strict';
/*
 * 테마 선반영 — 스타일시트보다 **먼저** 동기 실행돼야 한다(현행 public/js/theme-boot.js 이식).
 *
 * 늦게 돌면 다크 사용자에게 흰 화면이 한 프레임 번쩍인다(FOUC).
 *
 * 인라인 <script> 로 두지 않는 이유: 인라인이 하나라도 있으면 CSP 에 script-src 'self' 를 걸 수
 * 없어 스크립트 주입 방어를 통째로 포기해야 한다(server.js 의 CSP). 파일로 빼면 'self' 하나로 끝난다.
 *
 * 저장된 값이 없으면 OS 설정을 따르고, 그것도 모르면 다크로 둔다.
 */
(function () {
  var t = null;
  try { t = localStorage.getItem('usage-theme'); } catch { t = null; }
  if (t !== 'light' && t !== 'dark') {
    try {
      t = window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
    } catch { t = 'dark'; }
  }
  document.documentElement.setAttribute('data-theme', t);
})();
