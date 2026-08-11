import type { KeyListItem } from '@/lib/api';
import type { AdminUser } from '@/lib/types';

/*
 * ── 화면이 스스로 계산하는 값들 ────────────────────────────────────────────
 *
 * `GET /api/admin/users` 는 `adminCount` 도 `activeKeys` 도 주지 않는다(api-admin 이 확정한
 * shape 은 username·role·createdAt·team 넷뿐이다). 그래서 화면이 센다 — 그 계산을 컴포넌트
 * 안에 흩어 두면 같은 수를 두 곳에서 다르게 세는 날이 오므로 여기 한 곳에 모은다.
 *
 * ⚠ **이 계산은 "목록이 전부"라는 가정 위에 있다.** 지금 두 목록은 페이지네이션이 없다(서버가
 *   tenant 의 전 사용자·전 키를 한 번에 준다). 그 응답에 `nextPage` 류가 생기는 날, 여기서 센
 *   수는 조용히 작아지고 **화면은 "마지막 관리자가 아니다"라고 잘못 말한다** — 그러면 사전
 *   비활성이 풀린다. 서버가 ②③을 거부(409)하므로 사고까지는 가지 않지만, 사전 판정이
 *   무의미해지는 그 시점에 이 파일이 먼저 고쳐져야 한다.
 */

/** 사용자별 **활성**(미해지) 키 수. 결속 없는 키(username=null)는 누구에게도 세지 않는다. */
export function activeKeysByUser(keys: readonly KeyListItem[]): Map<string, number> {
  const out = new Map<string, number>();
  for (const k of keys) {
    if (k.revokedAt !== null || !k.username) continue;
    out.set(k.username, (out.get(k.username) ?? 0) + 1);
  }
  return out;
}

export function adminCount(users: readonly AdminUser[]): number {
  return users.filter((u) => u.role === 'admin').length;
}

export interface KeyTally {
  active: number;
  revoked: number;
  /** 사용자에 묶이지 않은 **활성** 키 — 이 수가 0 이 아니면 그만큼의 사용량이 PC 이름으로 잡힌다. */
  unbound: number;
}

export function keyTally(keys: readonly KeyListItem[]): KeyTally {
  let active = 0;
  let revoked = 0;
  let unbound = 0;
  for (const k of keys) {
    if (k.revokedAt !== null) { revoked += 1; continue; }
    active += 1;
    if (!k.username) unbound += 1;
  }
  return { active, revoked, unbound };
}

export const roleLabel = (role: string): string => (role === 'admin' ? '관리자' : '구성원');

/**
 * 사용자 시트를 여는 순간 확정되는 값 묶음.
 *
 * 시트가 목록 재조회에 흔들리지 않게 **열 때 한 번** 계산해 넘긴다(AdminTab 의 주석 참고).
 * 여기 사는 이유: 셋 다 위에서 센 파생값이고, 시트는 그것을 소비만 한다.
 */
export interface SheetTarget {
  user: AdminUser;
  /** 사전 비활성 판정("마지막 관리자")의 근거. */
  adminCount: number;
  /** 삭제 확인 문구가 읽는 수 — "키 0개"라고 잘못 말하면 사람은 지워도 되는 줄 안다. */
  activeKeys: number;
}
