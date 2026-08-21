'use client';

/*
 * ── 조회 스코프의 단일 조립 지점 ──────────────────────────────────────────
 *
 * 이 파일이 존재하는 이유는 하나다: **축을 하나 잊는 사고를 구조로 막는다.**
 *
 * `ScopeParams` 의 필드는 전부 optional 이라 타입이 누락을 잡아 주지 못한다. 그래서 화면이
 * `{ platform }` 만 만들어 넘겨도 컴파일이 통과하고, 그 화면만 조용히 다른 모집단을 그린다 —
 * 이 레포에서 실제로 두 번 난 사고이고(둘 다 "서버가 그 축을 못 받는다"는 낡은 주석 때문),
 * `lib/api.ts` 의 ScopeParams 주석이 그 이력을 갖고 있다.
 *
 * 축이 셋(platform · runtime · user)이 되면서 조합이 늘었으므로, 화면이 축을 **직접 세지
 * 않게** 한다. 새 축이 생기면 이 훅 하나만 고치면 모든 화면이 함께 싣는다.
 *
 * user 축은 여기서 읽지 않는다 — 화면마다 출처가 다르다(사용 추적은 응답의 byUser 에서
 * 만들고, 모달은 부모가 준다). 그래서 호출부가 넘기고, 나머지 둘은 스토어가 소유한다.
 *
 * ── ⚠ 의존성 배열에는 이 객체를 넣지 마라 ─────────────────────────────────
 *
 * `useResource` 는 의존성을 `deps.map(String)` 으로 이어 키를 만든다(hooks/useResource.ts).
 * 객체를 넣으면 `[object Object]` 로 굳어 **키가 절대 바뀌지 않고, 필터를 골라도 재조회가
 * 일어나지 않는다** — 화면은 낡은 값을 그대로 보여주면서 아무 에러도 내지 않는다.
 * 그 훅의 타입이 원시값만 받도록 좁혀 둔 이유가 이것이다.
 *
 * 그래서 호출부는 **먼저 분해해서 원시값을 나열한다**:
 *
 *	const scope = useScope(user || undefined);
 *	const { platform, runtime } = scope;
 *	const load = useCallback(async () => { …platform·runtime 을 쓴다… }, [platform, runtime, user]);
 *	const { state } = useResource(load, [platform, runtime, user]);
 *
 * 콜백 안에서 `scope` 를 직접 참조하지 않는 것이 중요하다 — 참조하면
 * react-hooks/exhaustive-deps 가 `scope` 를 의존성으로 요구하고, 그걸 넣으면 위 사고가 난다.
 */

import { useMemo } from 'react';
import { usePlatformFilter } from './platformFilter';
import { useRuntimeFilter } from './runtimeFilter';
import type { ScopeParams } from './api';

/**
 * 스토어가 소유한 축(platform · runtime)을 한 번에 읽는다. user 는 호출부가 얹는다.
 *
 * 빈 값은 그대로 빈 값으로 둔다 — api.ts 가 빈 값이면 파라미터 키를 만들지 않는다(=전체).
 */
export function useScope(user?: string): ScopeParams {
  const platform = usePlatformFilter();
  const runtime = useRuntimeFilter();
  // 값이 같으면 같은 객체를 돌려준다 — 이 객체를 props 로 내려도 불필요한 리렌더가 안 난다.
  return useMemo(() => ({ platform, runtime, user }), [platform, runtime, user]);
}
