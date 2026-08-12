'use client';

import { useCallback, useId, useState } from 'react';
import { isAborted, issueKeyFor, revokeKey, type IssuedKey, type KeyListItem } from '@/lib/api';
import type { AdminUser } from '@/lib/types';
import { Card, Flag, TableWrap } from '@/components/ui';
import { useToast } from '@/components/Toast';
import { fmtTime, n } from '@/lib/format';
import { keyTally } from '@/components/admin/derive';
import RevokeCell from '@/components/onboarding/RevokeCell';

/*
 * 전체 인제스트 키 현황(관리자) — 이 표는 **한 곳에만 있다.** 연동 탭에는 자기 키만 나온다.
 * 같은 표를 두 곳에 두면 둘 중 하나만 고쳐지는 날이 온다.
 *
 * ── 이 카드가 지는 하나 ────────────────────────────────────────────────
 * **사용자에 묶이지 않은 키에 ⚠ 를 단다.** 그 키로 들어온 사용량은 보고한 PC 가 주장하는
 * 이름(machine_identity 매핑 또는 payload.user)으로 잡힌다 — 사람 계정과 일치한다는 보장이 없다.
 * ⚠ 를 떼는 순간 근사가 정확값이 된다. 그래서 `귀속` 을 **두 번째 열**에 둔다: 390px 에서
 * ⚠ 가 가로 스크롤 밖으로 밀리면 미결속 키가 정상 키로 보인다.
 *
 * ── 대리발급의 소유자는 기본값 있는 필수 입력이다 ────────────────────────
 * `POST /api/admin/keys` 는 username 없이 부르면 종전대로 **결속 없는 org 공용 키**를 낸다
 * (api-admin 보고서 ④-6). 관리자가 아무 생각 없이 발급하면 그 키의 사용량은 PC 이름으로
 * 잡히므로, 화면은 소유자를 **고르게** 만들고 결속 없는 선택이 무슨 뜻인지 말한다.
 */
export default function KeysCard({
  keys, users, self, onIssued, onChanged,
}: {
  keys: KeyListItem[];
  /** 소유자 선택지 — 없는 계정을 고를 자리를 만들지 않는다(서버는 404 를 낸다). */
  users: AdminUser[];
  self: string;
  onIssued: (issued: IssuedKey) => void;
  onChanged: () => void;
}) {
  const toast = useToast();
  const ownerId = useId();
  const [issuing, setIssuing] = useState(false);
  const [revoking, setRevoking] = useState(false);
  const [confirmId, setConfirmId] = useState<string | null>(null);
  const [open, setOpen] = useState(false);
  /** '' = 결속 없는 org 공용 키를 **명시적으로** 고른 것. 기본값은 로그인한 본인이다. */
  const [owner, setOwner] = useState(self);

  const tally = keyTally(keys);

  const onIssue = useCallback(async () => {
    if (issuing) return;
    setIssuing(true);
    try {
      const res = await issueKeyFor(owner);
      onIssued(res);
      setOpen(false);
      setOwner(self);
      onChanged();
    } catch (e) {
      if (!isAborted(e)) toast('키를 발급하지 못했습니다. 잠시 뒤 다시 시도하세요.', 'err');
    } finally {
      setIssuing(false);
    }
  }, [issuing, onChanged, onIssued, owner, self, toast]);

  const onRevoke = useCallback(async (id: string) => {
    if (revoking) return;
    setRevoking(true);
    try {
      await revokeKey(id);
      setConfirmId(null);
      toast('키를 해지했습니다.');
      onChanged();
    } catch (e) {
      if (!isAborted(e)) toast('키를 해지하지 못했습니다.', 'err');
    } finally {
      setRevoking(false);
    }
  }, [onChanged, revoking, toast]);

  const tallyText = tally.unbound > 0
    ? `활성 ${n(tally.active)} · 해지 ${n(tally.revoked)} · ⚠ 미결속 ${n(tally.unbound)}`
    : `활성 ${n(tally.active)} · 해지 ${n(tally.revoked)}`;

  return (
    <Card
      title="인제스트 키 현황"
      className="mt"
      aside={
        <span className="row">
          <span className="help">{tallyText}</span>
          {/*
            주 동작은 '사용자 추가' 쪽이다. 여기까지 primary 로 두면 파란 버튼이 둘이 되어
            이 화면의 시그니처(위험 구역 하나)가 흐려진다 — 조용한 버튼으로 둔다.
          */}
          <button type="button" className="ghost sm" onClick={() => setOpen((v) => !v)}>
            {open ? '발급 취소' : '키 발급'}
          </button>
        </span>
      }
    >
      {open && (
        <div className="mb">
          <div className="row">
            <label className="help" htmlFor={ownerId}>이 키의 소유자</label>
            <select id={ownerId} value={owner} disabled={issuing} onChange={(e) => setOwner(e.target.value)}>
              {users.map((u) => (
                <option key={u.username} value={u.username}>{u.username}</option>
              ))}
              <option value="">사용자에 묶지 않음 (org 공용 키)</option>
            </select>
            <button type="button" className="primary" disabled={issuing} aria-busy={issuing} onClick={onIssue}>
              {issuing ? '발급 중…' : '발급'}
            </button>
          </div>
          <p className={`help mt-sm ${owner ? '' : 'txt-warn'}`}>
            {owner
              ? `이 키로 들어온 사용량은 ${owner} 님의 것으로 잡힙니다 — 그 PC 가 보내는 이름보다 우선합니다.`
              : '⚠ 이 키의 사용량은 보고한 PC 가 주장하는 이름으로 잡힙니다 — 사람 계정과 일치한다는 보장이 없습니다. 사람에게 묶으려면 위에서 소유자를 고르세요.'}
          </p>
        </div>
      )}

      {keys.length === 0 ? (
        <p className="help">아직 발급된 인제스트 키가 없습니다 — 팀원이 연동 탭에서 자기 키를 발급하면 여기 나타납니다.</p>
      ) : (
        <>
          <TableWrap>
            <table>
              <thead>
                <tr>
                  <th>키</th><th>귀속</th>
                  <th className="num">생성일</th><th className="num">마지막 보고</th>
                  <th>상태</th><th className="num">관리</th>
                </tr>
              </thead>
              <tbody>
                {keys.map((k) => {
                  const revoked = k.revokedAt !== null;
                  return (
                    <tr key={k.id}>
                      <td className="mono wrap-any">{k.masked}</td>
                      <td>
                        {k.username ?? (
                          <Flag title="이 키는 사용자에 묶여 있지 않습니다. 이 키로 들어온 사용량은 보고한 PC 가 주장하는 이름(machine_identity 매핑 또는 payload.user)으로 잡힙니다.">
                            PC 이름
                          </Flag>
                        )}
                      </td>
                      <td className="mono num" title={fmtTime(k.createdAt)}>{String(k.createdAt).slice(0, 10)}</td>
                      {/* 서버가 키별 마지막 보고를 아직 집계하지 않는다 — 아래 안내가 그 사실을 밝힌다. */}
                      <td className="num help">—</td>
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

          {tally.unbound > 0 && (
            <p className="help mt-sm">
              ⚠ 표시된 키는 사용자에 묶여 있지 않습니다 — 그 키로 들어온 사용량은 보고한 PC 가
              주장하는 이름으로 잡힙니다. 사람 계정과 일치한다는 보장이 없습니다. 그 사람 이름의
              키를 새로 발급해 교체하면 이후 사용량이 확정됩니다 — 이미 수집된 사용량의 이름은
              바뀌지 않습니다.
            </p>
          )}
          {/*
            못 재는 것을 빈칸으로 두면 "보고가 없다"로 읽힌다. 서버가 키 단위 최근 보고 시각도
            발급자도 응답에 담지 않으므로 그 사실을 밝힌다 — **한 줄로.** 회색 작은 글씨를 세 줄
            쌓으면 정작 위의 ⚠ 문단이 소음에 묻힌다(390px 실물에서 그렇게 보였다).
          */}
          <p className="help mt-sm">
            키별 마지막 보고는 아직 집계하지 않습니다 · 발급한 사람은 서버 감사 로그에만 남습니다
          </p>
        </>
      )}
    </Card>
  );
}
