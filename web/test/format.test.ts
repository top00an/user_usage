import { describe, it, expect } from 'vitest';
import { n, shortTokens, usd, ratio, pctOf, relTime, dayShift, fmtTime } from '@/lib/format';

describe('표기', () => {
  it('천 단위로 끊되 분위수의 소수는 살린다', () => {
    expect(n(1234567)).toBe('1,234,567');
    expect(n(null)).toBe('0');
    expect(n('nope')).toBe('0');
    // 세션당 턴 수 p50 = 7.5. 8 로 접으면 없는 정밀도를 만든다.
    expect(n(7.5)).toBe('7.5');
  });

  it('토큰 축약은 만/억 한 벌이다 (두 탭이 같은 자를 쓴다)', () => {
    expect(shortTokens(9999)).toBe('9,999');
    expect(shortTokens(12345)).toBe('1.2만');
    expect(shortTokens(523000)).toBe('52.3만');
    expect(shortTokens(1.61e10)).toBe('161.0억');
  });

  it('1달러 미만은 소수 셋째 자리까지 — 반올림해 $0 을 만들지 않는다', () => {
    expect(usd(0)).toBe('$0');
    expect(usd(0.0012)).toBe('$0.001');
    expect(usd(0.35)).toBe('$0.350');
    expect(usd(1234.567)).toBe('$1,234.57');
  });

  it('0 으로 나누지 않는다', () => {
    expect(ratio(5, 0)).toBe(0);
    expect(ratio(5, 10)).toBe(50);
    expect(pctOf(0.0625)).toBe('6.3%');
  });

  it('상대 시각은 응답의 now 를 기준으로 잰다', () => {
    const now = '2026-08-05T12:00:00.000Z';
    expect(relTime(now, '2026-08-05T11:30:00.000Z')).toBe('30분 전');
    expect(relTime(now, '2026-08-05T09:00:00.000Z')).toBe('3시간 전');
    expect(relTime(now, '2026-08-01T12:00:00.000Z')).toBe('4일 전');
    expect(relTime(now, null)).toBe('—');
  });

  it('dayShift 는 로컬 날짜로 센다', () => {
    expect(dayShift(6, new Date(2026, 7, 7))).toBe('2026-08-01');
    expect(dayShift(0, new Date(2026, 0, 1))).toBe('2026-01-01');
  });

  it('잘못된 시각 문자열에서 죽지 않는다', () => {
    expect(fmtTime(null)).toBe('-');
    expect(fmtTime('not-a-date')).toBe('not-a-date');
  });
});
