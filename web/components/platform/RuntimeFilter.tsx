'use client';

/*
 * ── runtime 선택 (클라우드 / 로컬) ────────────────────────────────────────
 *
 * `PlatformFilter` 의 형제지만 두 가지가 다르다.
 *
 * ① **선택지의 출처.** platform 은 선택지를 응답(`/api/usage/platforms`)에서 만든다 — 도구는
 *    계속 늘어나므로 하드코딩하면 새 도구가 화면에서 영원히 안 보인다. runtime 은 반대로
 *    서버 허용목록이 이분법으로 고정돼 있어(store.Runtimes) 늘어날 이유가 없다. 정적 목록이다.
 *
 * ② **선택지가 없을 때 숨지 않는다.** platform 필터는 고를 것이 하나뿐이면 자기를 숨긴다.
 *    여기서는 그렇게 할 수 없다 — 조회 응답이 아직 runtime 을 싣지 않아서 "로컬 세션이
 *    있는가"를 화면이 알 방법이 없다. 응답에 필드를 늘리는 것은 골든 스냅샷을 다시 떠야 하는
 *    계약 변경이고, 그건 이 축을 화면에 붙이는 것과 **별개의 결정**이다.
 *
 *    그래서 항상 그리고, 로컬을 골랐는데 결과가 비면 **배지가 그 이유를 말한다.** "로컬 사용이
 *    0건"은 고장이 아니라 답이고, 배지가 없으면 사람이 그것을 빈 화면으로 오해한다.
 */

import { RUNTIME_IDS, runtimeHint, runtimeLabel } from '@/lib/runtimes';
import { setRuntimeFilter, useRuntimeFilter } from '@/lib/runtimeFilter';

export default function RuntimeFilter() {
  const cur = useRuntimeFilter();

  return (
    <div className="pf-bar">
      <label htmlFor="runtime-filter">실행 위치</label>
      {/* 클래스를 주지 않는다 — 전역 `input, select, textarea` 규칙이 받는다(PlatformFilter 와 동일). */}
      <select
        id="runtime-filter"
        value={cur}
        onChange={(e) => setRuntimeFilter(e.target.value)}
      >
        <option value="">전체</option>
        {RUNTIME_IDS.map((id) => (
          <option key={id} value={id} title={runtimeHint(id)}>{runtimeLabel(id)}</option>
        ))}
      </select>
      {cur ? (
        <span className="badge ok" title={runtimeHint(cur)}>
          {`runtime=${cur} 기준 · 해당 세션이 없으면 빈 값이 정상입니다`}
        </span>
      ) : (
        <span className="muted small">전체 실행 위치 기준</span>
      )}
    </div>
  );
}
