/*
 * 토큰 보관 — 쿠키 하나다(현행 public/app.js 의 결정을 그대로 잇는다).
 *
 * 왜 쿠키인가: 서버는 `Authorization` 헤더 또는 쿠키 `usage_tok` 를 받는다. 쿠키에 담으면
 * 브라우저가 알아서 싣는다 — 호출부가 토큰을 알 필요가 없고, 나중에 SSO 로 갈아탈 때
 * 고칠 자리가 lib/api.ts 와 이 파일 둘로 끝난다.
 *
 * Next 서버가 토큰을 들고 프록시하는 안은 채택하지 않는다 — 지금의 "CSRF 표면 없음" 설계를
 * 다시 짜야 하고(서버가 쿠키 자격증명으로는 상태변경을 태우지 않는다), 붙일 사내 시스템을
 * 아직 모른다. 게다가 이 앱은 정적 export 라 애초에 서버가 없다.
 *
 * SameSite=Strict: 이 쿠키는 조회 자격증명이라 교차 사이트 요청에 실릴 이유가 전혀 없다.
 *
 * ⚠ HttpOnly 는 붙일 수 없다. 게이트 화면이 document.cookie 로 직접 쓰기 때문이다
 *   (서버가 Set-Cookie 를 내지 않는다). 대신 산출물에 인라인 스크립트를 남기지 않아
 *   CSP 가 script-src 'self' 로 잠긴다 — 그것이 여기서의 실질 방어다.
 */

export const COOKIE_NAME = 'usage_tok';

/**
 * 쿠키 속성 — **프로토콜을 보고 정한다.**
 *
 * `Secure` 를 무조건 붙이면 로컬 http 기동(기본 시나리오)에서 쿠키가 아예 저장되지 않아
 * 로그인이 성립하지 않는다. 무조건 빼면 누군가 리버스 프록시 뒤 https 로 올렸을 때 조회
 * 토큰이 평문으로 흐른다. https 로 열린 페이지는 이미 저장이 되므로 붙이는 쪽에 손실이 없다 —
 * 그래서 조건부가 정답이다.
 */
export function cookieAttrs(protocol: string = readProtocol()): string {
  return `; path=/; SameSite=Strict${protocol === 'https:' ? '; Secure' : ''}`;
}

function readProtocol(): string {
  return typeof location === 'undefined' ? 'http:' : location.protocol;
}

function readCookieJar(): string {
  return typeof document === 'undefined' ? '' : document.cookie || '';
}

export function readToken(): string {
  const m = new RegExp(`(?:^|;\\s*)${COOKIE_NAME}=([^;]*)`).exec(readCookieJar());
  if (!m || m[1] === undefined) return '';
  try {
    return decodeURIComponent(m[1]);
  } catch {
    // 인코딩이 깨진 값이라도 "토큰이 있다"는 사실은 남는다 — 게이트로 튕기는 대신 서버가 판정한다.
    return m[1];
  }
}

export function writeToken(token: string): void {
  if (typeof document === 'undefined') return;
  document.cookie = `${COOKIE_NAME}=${encodeURIComponent(token)}${cookieAttrs()}`;
  emit();
}

export function clearToken(): void {
  if (typeof document === 'undefined') return;
  document.cookie = `${COOKIE_NAME}=; Max-Age=0${cookieAttrs()}`;
  emit();
}

export function hasToken(): boolean {
  return readToken() !== '';
}

/*
 * ── 토큰을 외부 스토어로 노출한다 ────────────────────────────────────────
 *
 * 쿠키는 React 밖의 상태다. "마운트 뒤에 읽어서 setState" 로 다루면 두 가지가 생긴다:
 * 렌더가 한 번 더 돌고(이펙트 → 상태 → 렌더), 그 사이 한 프레임 동안 화면이 틀린 쪽을
 * 보여준다. useSyncExternalStore 는 React 가 이 상황을 위해 준 API다.
 *
 * 서버 스냅샷이 'unknown' 인 이유: 정적 export 라 빌드 시각에 미리 그려지는데, 그때는 쿠키가
 * 없다. 'no' 로 두면 **로그인한 사람에게 게이트가 한 프레임 번쩍인다.** 모르는 것은 모른다고
 * 말하고 화면은 "로딩 중"으로 둔다.
 */
export type AuthSnapshot = 'unknown' | 'yes' | 'no';

const listeners = new Set<() => void>();

function emit(): void {
  for (const l of listeners) l();
}

export function subscribeToken(cb: () => void): () => void {
  listeners.add(cb);
  return () => { listeners.delete(cb); };
}

export function tokenSnapshot(): AuthSnapshot {
  return hasToken() ? 'yes' : 'no';
}

export function tokenServerSnapshot(): AuthSnapshot {
  return 'unknown';
}
