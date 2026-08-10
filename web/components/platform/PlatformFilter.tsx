'use client';

/*
 * ── 플랫폼 선택 ───────────────────────────────────────────────────────────
 *
 * 선택지는 **응답이 정한다.** /api/usage/platforms 가 돌려준 목록에서 세션이 있는 것만 세운다 —
 * 하드코딩한 목록으로 만들면 (ㄱ) 데이터가 없는 플랫폼을 고를 수 있어 빈 화면이 나오고,
 * (ㄴ) 서버가 새 플랫폼을 수집하기 시작해도 화면에서는 영원히 안 보인다.
 *
 * 선택지가 하나뿐이면 컨트롤 자체를 그리지 않는다 — '전체'와 그 하나가 같은 화면이라
 * 고를 것이 없는 셀렉트만 남는다.
 *
 * 저장된 선택이 더 이상 목록에 없으면(그 플랫폼 데이터가 사라졌다) 조용히 전체로 되돌린다.
 * 안 그러면 목록에 없는 값이 계속 질의로 나가고, 화면은 이유 없이 빈 채로 남는다.
 */

import { useEffect } from 'react';
import type { PlatformRow } from '@/lib/types';
import { isPlatformId, platformMeta } from '@/lib/platforms';
import { setPlatformFilter, usePlatformFilter } from '@/lib/platformFilter';
import PlatformScope from './PlatformScope';

/** 응답에서 고를 수 있는 것만 남긴다: 허용목록 안 + 세션이 실제로 있는 것. */
export function selectableRows(rows: PlatformRow[] | null | undefined): PlatformRow[] {
  return (rows ?? []).filter((r) => isPlatformId(r.platform) && (Number(r.sessions) || 0) > 0);
}

export default function PlatformFilter({
  rows, applies, what,
}: {
  rows: PlatformRow[] | null;
  /** 이 화면의 조회가 platform= 을 실제로 싣는가. 아니면 '전체 플랫폼 기준'이라고 말한다. */
  applies: boolean;
  what?: string;
}) {
  const cur = usePlatformFilter();
  const options = selectableRows(rows);
  const known = options.some((o) => o.platform === cur);

  // 목록에 없는 선택은 되돌린다. 렌더 중 스토어를 건드리면 다른 구독자가 렌더 도중 갱신되므로
  // 이펙트에서 한다.
  useEffect(() => {
    if (cur && !known) setPlatformFilter('');
  }, [cur, known]);

  if (options.length < 2) return null;

  return (
    <div className="pf-bar">
      <label htmlFor="platform-filter">플랫폼</label>
      <select
        id="platform-filter"
        className="builder-input"
        value={known ? cur : ''}
        onChange={(e) => setPlatformFilter(e.target.value)}
      >
        <option value="">전체</option>
        {options.map((o) => (
          <option key={o.platform} value={o.platform}>{platformMeta(o.platform).label}</option>
        ))}
      </select>
      <PlatformScope applies={applies} what={what} />
    </div>
  );
}
