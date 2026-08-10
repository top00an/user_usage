'use client';

/*
 * ── "지금 보는 숫자가 어느 모집단인가" ────────────────────────────────────
 *
 * 플랫폼을 골라 놓으면 사람은 화면의 **모든** 숫자가 그 플랫폼 것이라고 읽는다. 그런데 서버는
 * 일부 엔드포인트에서만 platform 축을 거른다(lib/api.ts 의 표). 안 거르는 조회는 전체 합계를
 * 돌려주고, 그걸 아무 말 없이 그리면 화면이 조용히 틀린 값을 보여주게 된다 — 에러 없이.
 *
 * 그래서 필터가 걸려 있는 동안에는 **모든 패널이 자기 모집단을 밝힌다.** 걸러진 패널은
 * "Codex 기준", 못 거르는 패널은 "전체 플랫폼 기준". 필터가 없으면(=전체) 아무 말도 하지 않는다 —
 * 그때는 현행과 완전히 같은 화면이고, 덧붙일 사실이 없다.
 */

import { usePlatformFilter } from '@/lib/platformFilter';
import { platformMeta } from '@/lib/platforms';

export default function PlatformScope({ applies, what }: { applies: boolean; what?: string }) {
  const cur = usePlatformFilter();
  if (!cur) return null; // 전체 = 현행 동작. 말할 것이 없다.

  const label = platformMeta(cur).label;
  const suffix = what ? ` · ${what}` : '';

  return applies ? (
    <span className="badge ok" title={`서버 조회에 platform=${cur} 을 실어 보냅니다.`}>
      {label} 기준{suffix}
    </span>
  ) : (
    <span
      className="badge mute"
      title="이 조회는 서버가 플랫폼 축으로 거르지 못합니다 — 선택과 무관하게 전체 플랫폼 합계입니다."
    >
      전체 플랫폼 기준{suffix}
    </span>
  );
}
