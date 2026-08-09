import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { readToken, writeToken, clearToken, COOKIE_NAME, cookieAttrs } from '@/lib/token';

describe('토큰 쿠키', () => {
  beforeEach(() => { clearToken(); });
  afterEach(() => { clearToken(); });

  it('쓴 값을 그대로 읽는다', () => {
    writeToken('dev-local-token-0123456789abcdef');
    expect(readToken()).toBe('dev-local-token-0123456789abcdef');
  });

  it('세미콜론·공백이 든 값도 인코딩되어 왕복한다', () => {
    writeToken('a b;c=d');
    expect(readToken()).toBe('a b;c=d');
  });

  it('지우면 빈 문자열이 된다', () => {
    writeToken('tok');
    clearToken();
    expect(readToken()).toBe('');
  });

  /*
   * Secure 는 **프로토콜을 보고 정한다.**
   * 무조건 붙이면 로컬 http 기동(기본 시나리오)에서 쿠키가 저장되지 않아 로그인이 성립하지 않고,
   * 무조건 빼면 https 뒤에서 조회 토큰이 평문으로 흐른다.
   */
  it('http 에서는 Secure 를 붙이지 않는다', () => {
    expect(cookieAttrs('http:')).toBe('; path=/; SameSite=Strict');
  });

  it('https 에서는 Secure 를 붙인다', () => {
    expect(cookieAttrs('https:')).toBe('; path=/; SameSite=Strict; Secure');
  });

  it('쿠키 이름은 서버가 읽는 그 이름이다', () => {
    expect(COOKIE_NAME).toBe('usage_tok');
  });
});
