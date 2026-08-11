'use client';

import { useCallback, useState } from 'react';
import {
  isAborted,
  issueMyKey,
  listMyKeys,
  revokeMyKey,
  type IssuedKey,
  type KeyList,
} from '@/lib/api';
import { useResource } from '@/hooks/useResource';
import { fmtTime, n } from '@/lib/format';
import { Card, ErrorState, Loading, TableWrap } from '@/components/ui';
import { useToast } from '@/components/Toast';
import IssuedKeyModal from '@/components/onboarding/IssuedKeyModal';
import RevokeCell from '@/components/onboarding/RevokeCell';
import { KeyScope } from '@/components/onboarding/keyscope';

/*
 * 연동(온보딩) — **모든 로그인 사용자의 화면이다**(동결 ②). 관리자 전용이 아니다.
 *
 * 개발자 머신에 수집기·훅을 붙이려면 인제스트 키가 필요하다. 이 화면은 셋만 한다:
 *   ① 내 키 발급    → 서버가 평문 key 를 **딱 한 번** 돌려준다(응답 1회뿐). 그 키는 **나에게
 *                    묶인다** — 그 키로 들어온 사용량은 PC 가 주장하는 이름을 이기고 내 것이 된다.
 *   ② 원라인 명령    → 그 평문 key 로 설치 명령을 만들어 모달에서 보여주고 복사 버튼을 준다.
 *                    여기서 복사하지 않으면 다시 볼 수 없으므로 모달로 승격했다(§6).
 *   ③ 목록·해지      → 이후로는 masked 만 다룬다(평문은 서버도 안 준다). **자기 키만** 보인다 —
 *                    남의 키는 UI 가 숨기는 게 아니라 서버가 아예 주지 않는다(`/api/me/keys`).
 *
 * 전체 키 현황은 **관리 탭 한 곳뿐이다.** 같은 표를 두 곳에 두면 둘 중 하나만 고쳐지는 날이 온다.
 *
 * 평문 key 는 **메모리(state)에만** 둔다 — localStorage·쿠키 어디에도 쓰지 않는다.
 * 탭을 떠나 컴포넌트가 언마운트되면(Dashboard 가 key 로 트리를 버린다) 그대로 사라진다.
 */

/* 기존 임포트 경로를 유지한다 — 이 두 개는 다른 화면·테스트가 이름으로 잡고 있다. */
export { installCommand, KeyScope } from '@/components/onboarding/keyscope';

/* 발급된 내 키 목록 — masked·생성일·상태. 활성 키만 해지할 수 있고, 해지는 두 단계로 확인한다. */
function KeyTable({
  keys, self, justIssued, onRevoke, revoking, confirmId, setConfirmId,
}: {
  keys: KeyList['keys'];
  self: string;
  /** 이 컴포넌트가 마운트되어 있는 동안만 남는 표시 — "내가 방금 만든 게 저거 맞나"에 답한다. */
  justIssued: string | null;
  onRevoke: (id: string) => void;
  revoking: boolean;
  confirmId: string | null;
  setConfirmId: (id: string | null) => void;
}) {
  if (!keys.length) {
    return (
      <Card title="내 키" className="mt">
        <p className="help">아직 내 키가 없습니다. 위에서 첫 키를 발급하세요.</p>
        <div className="mt"><KeyScope label="인제스트 키 스코프" /></div>
      </Card>
    );
  }
  const active = keys.filter((k) => k.revokedAt === null).length;
  return (
    <Card
      title="내 키"
      className="mt"
      aside={<span className="help">활성 {n(active)} · 해지 {n(keys.length - active)}</span>}
    >
      {/* 목록만 보고 "이 키로 대시보드도 보이나?" 를 추측하지 않도록, 스코프를 표 위에 그대로 둔다. */}
      <KeyScope label="인제스트 키 스코프" />
      <TableWrap>
        <table className="mt-sm">
          <thead>
            <tr>
              <th>키</th><th className="num">생성일</th><th>상태</th><th className="num">관리</th>
            </tr>
          </thead>
          <tbody>
            {keys.map((k) => {
              const revoked = k.revokedAt !== null;
              return (
                <tr key={k.id}>
                  <td className="mono wrap-any">
                    {k.masked}
                    {justIssued === k.id && <> <span className="badge ok">방금 발급</span></>}
                    {/*
                      내 목록에 결속 없는(레거시) 키가 섞여 나올 수 있다 — `/api/me/keys` 는
                      username 으로 고르므로 원칙적으로 없지만, 서버가 주면 화면은 사실을 말한다.
                    */}
                    {!k.username && <> <span className="badge warn" title="이 키는 사용자에 묶여 있지 않습니다 — 이 키로 들어온 사용량은 보고한 PC 가 주장하는 이름으로 잡힙니다.">⚠ 미결속</span></>}
                  </td>
                  <td className="mono num" title={fmtTime(k.createdAt)}>{fmtTime(k.createdAt)}</td>
                  <td>
                    {revoked
                      ? <span className="badge" title={fmtTime(k.revokedAt)}>해지됨</span>
                      : <span className="badge ok">활성</span>}
                  </td>
                  <td className="num">
                    <RevokeCell
                      masked={k.masked}
                      revoked={revoked}
                      confirming={confirmId === k.id}
                      busy={revoking}
                      onAsk={() => setConfirmId(k.id)}
                      onCancel={() => setConfirmId(null)}
                      onConfirm={() => onRevoke(k.id)}
                    />
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </TableWrap>
      <p className="help mt-sm">
        {self} 님의 키만 보입니다. 다른 사람의 키는 서버가 이 목록에 담지 않습니다.
      </p>
    </Card>
  );
}

export default function Onboarding({ self, isAdmin = false }: { self: string; isAdmin?: boolean }) {
  const toast = useToast();
  const [issued, setIssued] = useState<IssuedKey | null>(null);
  const [justIssued, setJustIssued] = useState<string | null>(null);
  const [issuing, setIssuing] = useState(false);
  const [revoking, setRevoking] = useState(false);
  const [confirmId, setConfirmId] = useState<string | null>(null);

  const load = useCallback(({ signal }: { signal: AbortSignal }) => listMyKeys({ signal }), []);
  const { state, reload } = useResource(load, []);

  /* 발급 — 이중 클릭을 막는다(issuing 게이트). 성공하면 평문을 메모리에 담고 목록을 새로 읽는다. */
  const onIssue = useCallback(async () => {
    if (issuing) return;
    setIssuing(true);
    try {
      const res = await issueMyKey();
      setIssued(res);
      setJustIssued(res.id);
      reload();
    } catch (e) {
      if (!isAborted(e)) toast('키 발급에 실패했습니다. 잠시 뒤 다시 시도하세요.', 'err');
    } finally {
      setIssuing(false);
    }
  }, [issuing, reload, toast]);

  const onRevoke = useCallback(async (id: string) => {
    if (revoking) return;
    setRevoking(true);
    try {
      await revokeMyKey(id);
      setConfirmId(null);
      toast('키를 해지했습니다.');
      reload();
    } catch (e) {
      if (!isAborted(e)) toast('키를 해지하지 못했습니다.', 'err');
    } finally {
      setRevoking(false);
    }
  }, [revoking, reload, toast]);

  return (
    <>
      <p className="lead">
        개발자 머신에서 아래 한 줄을 실행하면 수집기와 훅이 자동 설치되어 바로 연동됩니다.
      </p>

      <Card>
        <div className="between">
          <div>
            <h3>내 인제스트 키</h3>
            {/* 이 화면에서 가장 중요한 두 줄 — 무엇에 묶이고, 누구에게 안 보이는가. */}
            <p className="help mt-sm">
              이 키로 들어온 사용량은 <b>{self}</b> 님의 것으로 잡힙니다.
              <br />
              다른 사람의 키는 여기 보이지 않습니다.
            </p>
          </div>
          <button type="button" className="primary" onClick={onIssue} disabled={issuing} aria-busy={issuing}>
            {issuing ? '발급 중…' : '내 키 발급'}
          </button>
        </div>
        {isAdmin && (
          <p className="help mt">전체 키 현황은 관리 탭에 있습니다.</p>
        )}
      </Card>

      {issued && <IssuedKeyModal issued={issued} mine onClose={() => setIssued(null)} />}

      {state.status === 'loading' && <div className="mt"><Loading label="내 키를 불러오는 중…" /></div>}
      {state.status === 'error' && (
        <div className="mt"><ErrorState what="내 키 목록을 불러오지 못했습니다." error={state.error} onRetry={reload} /></div>
      )}
      {state.status === 'ready' && (
        <KeyTable
          keys={state.data.keys ?? []}
          self={self}
          justIssued={justIssued}
          onRevoke={onRevoke}
          revoking={revoking}
          confirmId={confirmId}
          setConfirmId={setConfirmId}
        />
      )}
    </>
  );
}
