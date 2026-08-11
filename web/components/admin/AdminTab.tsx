'use client';

import { useCallback, useState } from 'react';
import { listAdminUsers, listKeys, type IssuedKey, type KeyList } from '@/lib/api';
import type { AdminUsers } from '@/lib/types';
import { useResource } from '@/hooks/useResource';
import { ErrorState, Loading } from '@/components/ui';
import IssuedKeyModal from '@/components/onboarding/IssuedKeyModal';
import { activeKeysByUser, adminCount, type SheetTarget } from '@/components/admin/derive';
import KeysCard from '@/components/admin/KeysCard';
import UserSheet from '@/components/admin/UserSheet';
import UsersCard from '@/components/admin/UsersCard';

/*
 * ── 관리 탭 ───────────────────────────────────────────────────────────────
 *
 * 카드 두 장, 그게 전부다. 상단에 큰 숫자 타일 행을 두지 않는다 — "사용자 6명 / 키 3개"를
 * 1.9rem 로 그리는 것은 밀도가 아니라 장식이고, 이 화면의 시그니처는 위험 구역 하나뿐이다.
 *
 * 두 조회는 **둘 다 필요하다**(fail-soft 하지 않는다): 키 목록이 없으면 사용자별 활성 키 수를
 * 셀 수 없고, 그 수는 삭제 확인 문구가 읽는 값이다 — "키 0개"라고 잘못 말하면 사람은 지워도
 * 되는 줄 안다. 하나가 실패하면 화면 전체가 그 사실을 말한다.
 *
 * ⚠ **시트는 두 카드와 같은 가지에 두지 않는다.** `useResource` 의 reload 는 상태를 loading 으로
 *   되돌리므로(낡은 결과가 렌더에 오르지 못하게 하는 그 훅의 규율이다), 시트를 데이터 가지 안에
 *   두면 저장 한 번에 시트가 언마운트되고 **방금 뜬 서버 거부·사고 경고와 입력해 둔 값이 함께
 *   사라진다.** 그래서 시트는 열 때 그 시점의 값을 받아 들고(아래 SheetTarget), 이후 변경은
 *   **서버 응답의 user** 로 갱신한다 — 추측이 아니라 서버가 말한 값이다. 목록은 그와 별개로 매
 *   변경 뒤 다시 읽는다(낙관적 갱신 없음).
 */

interface AdminData {
  users: AdminUsers;
  keys: KeyList;
}

export default function AdminTab({ self }: { self: string }) {
  const [sheet, setSheet] = useState<SheetTarget | null>(null);
  const [issued, setIssued] = useState<IssuedKey | null>(null);

  const load = useCallback(async ({ signal }: { signal: AbortSignal }): Promise<AdminData> => {
    const [users, keys] = await Promise.all([listAdminUsers({ signal }), listKeys({ signal })]);
    return { users, keys };
  }, []);

  const { state, reload } = useResource(load, []);

  /* 성공한 변경 뒤에는 **서버에서 다시 읽는다.** 낙관적 갱신은 서버가 거부할 때 거짓을 말한다. */
  const onChanged = useCallback((opts?: { closed?: boolean }) => {
    if (opts?.closed) setSheet(null);
    reload();
  }, [reload]);

  const users = state.status === 'ready' ? state.data.users.users ?? [] : [];
  const keys = state.status === 'ready' ? state.data.keys.keys ?? [] : [];
  const activeKeys = activeKeysByUser(keys);

  return (
    <>
      {state.status === 'error' ? (
        <ErrorState
          what="사용자 목록을 불러오지 못했습니다. 관리 화면은 관리자만 열람합니다."
          error={state.error}
          onRetry={reload}
        />
      ) : state.status === 'loading' ? (
        <Loading label="사용자 목록을 불러오는 중…" />
      ) : (
        <>
          <p className="lead">
            사용자를 삭제하거나 강등하면 그 사람의 로그인 세션이 즉시 끊깁니다.
            이미 수집된 사용량은 그대로 남습니다.
          </p>

          <UsersCard
            users={users}
            activeKeys={activeKeys}
            /* 클릭 시점의 목록으로 확정한다 — 시트가 목록 재조회에 흔들리지 않게. */
            onOpen={(username) => {
              const user = users.find((u) => u.username === username);
              if (!user) return;
              setSheet({ user, adminCount: adminCount(users), activeKeys: activeKeys.get(username) ?? 0 });
            }}
            onCreated={reload}
          />

          <KeysCard
            keys={keys}
            users={users}
            self={self}
            onIssued={setIssued}
            onChanged={reload}
          />
        </>
      )}

      {sheet && (
        <UserSheet
          key={sheet.user.username}
          target={sheet}
          self={self}
          /*
           * 그냥 닫을 때는 재조회하지 않는다. 변경은 이미 각자 성공 직후 목록을 다시 읽었고,
           * 여기서 또 읽으면 그 순간 표가 언마운트되어 **Modal 이 포커스를 되돌릴 행이 사라진다**
           * (키보드 사용자는 문서 맨 위로 튕긴다).
           */
          onClose={() => setSheet(null)}
          onChanged={onChanged}
        />
      )}

      {/*
        평문 키는 모달로만 보여준다(카드는 "다시 볼 수 있는 것"처럼 생겨서 사람이 나중에 찾는다).
        시트와 겹치지 않는다 — 발급은 키 카드에서만 시작되고 그때 시트는 닫혀 있다.
      */}
      {issued && <IssuedKeyModal issued={issued} onClose={() => setIssued(null)} />}
    </>
  );
}
