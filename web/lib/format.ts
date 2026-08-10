/*
 * 숫자·시각 표기 — 이 화면의 오독 사고는 전부 **표기**에서 났다. 그래서 여기 규칙이 모여 있다.
 *
 * ⚠ 토큰 수의 축약 단위는 **한 벌뿐이다.**
 *   현행은 두 뷰가 서로 다른 축약을 썼다 — 사용 추적은 만/억, 사용 관측은 K/M/B. 같은 양(토큰)을
 *   두 탭이 다른 자로 재면, 탭을 오간 사람은 두 숫자를 비교할 수 없고 비교하려다 틀린다.
 *   이 레포가 이미 그 값을 치른 적이 있다(같은 사용량에 176배 차이 나는 숫자).
 *   → 한국어 운영 도구이므로 만/억 한 벌로 모은다. 정확한 값은 언제나 title 에 남는다.
 */

const KO = 'ko-KR';

/**
 * 천 단위 구분자.
 *
 * ⚠ 소수를 반올림해 버리지 않는다. 이 함수는 개수(정수)뿐 아니라 **분위수**에도 쓰인다 —
 *   `세션당 턴 수 p50 = 7.5` 를 `8` 로 접으면 없는 정밀도를 만들어 낸다(보간된 값이라
 *   7.5 라는 것 자체가 정보다). 정수 값은 어차피 소수점이 붙지 않는다.
 */
export function n(v: unknown): string {
  return (Number(v) || 0).toLocaleString(KO);
}

/**
 * 토큰 수 축약. 자릿수가 커서 원본만 보면 비교가 안 된다 —
 * 만/억 단위로 접어 보여주되 **정확한 값은 title 로 남긴다**(TokenCount 컴포넌트가 붙인다).
 */
export function shortTokens(v: unknown): string {
  const x = Number(v) || 0;
  const sign = x < 0 ? '-' : '';
  const a = Math.abs(x);
  if (a >= 1e8) return `${sign}${(a / 1e8).toFixed(1)}억`;
  if (a >= 1e4) return `${sign}${(a / 1e4).toFixed(1)}만`;
  return `${sign}${a.toLocaleString(KO, { maximumFractionDigits: 0 })}`;
}

/**
 * API 환산 비용(lib/costLabels.ts). 1달러 미만은 소수 셋째 자리까지 — 세션 단가가 대개 그 구간이고,
 * 반올림해 $0 으로 만들면 "0원짜리 세션"이라는 없는 사실이 생긴다.
 */
export function usd(v: unknown): string {
  const x = Number(v) || 0;
  if (x === 0) return '$0';
  return x < 1 ? `$${x.toFixed(3)}` : `$${x.toLocaleString('en-US', { maximumFractionDigits: 2 })}`;
}

export function ratio(part: unknown, whole: unknown): number {
  const w = Number(whole) || 0;
  return w > 0 ? ((Number(part) || 0) / w) * 100 : 0;
}

/** 0~1 비율 → 백분율 문구. */
export function pctOf(v: unknown, digits = 1): string {
  return `${((Number(v) || 0) * 100).toFixed(digits)}%`;
}

export function seconds(ms: unknown): string {
  return `${((Number(ms) || 0) / 1000).toFixed(1)}s`;
}

/** 서버는 UTC ISO(...Z)로 저장한다 → 화면은 브라우저 로컬시간으로 보여준다. */
export function fmtTime(iso: string | null | undefined): string {
  if (!iso) return '-';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return String(iso);
  const p = (x: number) => String(x).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} `
    + `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

/** 상대 시각 — 응답의 now 를 기준으로 한다(클라이언트 시계 오차를 타지 않게). */
export function relTime(nowIso: string, iso: string | null | undefined): string {
  if (!iso) return '—';
  const ms = Date.parse(nowIso) - Date.parse(iso);
  if (!Number.isFinite(ms)) return '—';
  const m = Math.max(0, Math.floor(ms / 60000));
  if (m < 60) return `${m}분 전`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}시간 전`;
  return `${Math.floor(h / 24)}일 전`;
}

/**
 * n일 전의 **로컬** 날짜. UTC 로 계산하면 00:00~09:00 KST 사이에 화면을 열었을 때 하루가
 * 밀린 범위를 요청하게 된다(서버가 로컬 라벨로 거르므로 그만큼 빈 칸이 생긴다).
 */
export function dayShift(days: number, from: Date = new Date()): string {
  const d = new Date(from.getTime());
  d.setDate(d.getDate() - days);
  const p = (x: number) => String(x).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}
