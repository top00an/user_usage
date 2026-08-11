'use client';

import { useCallback, useId, useState } from 'react';
import { ApiError, createUser, isAborted } from '@/lib/api';
import type { AdminUser, UserRole } from '@/lib/types';
import { Card, TableWrap } from '@/components/ui';
import { useToast } from '@/components/Toast';
import { fmtTime, n } from '@/lib/format';
import { adminCount as countAdmins, roleLabel } from '@/components/admin/derive';

/*
 * 사용자 표 — **읽기 전용이다.** 행을 누르면 그 사용자의 시트가 열리고, 변경은 거기서만 한다.
 *
 * 왜 행 전체가 시트를 여는가. 표 안에 버튼을 중첩하면 `role="button"` 인 행 안에 `<button>` 이
 * 들어가 접근성이 깨진다. 그리고 390px 에서 관리 버튼은 가로 스크롤 맨 오른쪽에 있어 손이 닿지
 * 않는다. **시트를 여는 행위 자체는 비파괴적**이고 파괴는 시트 안에서 두 단계 더 있으므로,
 * 행 클릭이 위험을 늘리지 않는다(usageobs/SessionsCard.tsx 가 이미 쓰는 패턴이다).
 *
 * 열 순서가 좁은 화면의 설계다: 스크롤 없이 보이는 왼쪽 두 열(사용자·역할)만으로 판단의 8할이
 * 끝난다. 표는 `.table-wrap` 안에서 자기가 가로 스크롤한다 — 본문은 절대 옆으로 밀리지 않는다.
 */
export default function UsersCard({
  users, activeKeys, onOpen, onCreated,
}: {
  users: AdminUser[];
  /** 사용자별 활성 키 수(derive.ts). 없으면 0. */
  activeKeys: Map<string, number>;
  onOpen: (username: string) => void;
  onCreated: () => void;
}) {
  const toast = useToast();
  const nameId = useId();
  const pwId = useId();
  const roleId = useId();

  const [adding, setAdding] = useState(false);
  const [name, setName] = useState('');
  const [password, setPassword] = useState('');
  const [role, setRole] = useState<UserRole>('member');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const admins = countAdmins(users);

  const reset = useCallback(() => {
    setAdding(false);
    setName('');
    setPassword('');
    setRole('member');
    setError(null);
  }, []);

  const submit = useCallback(async (e: React.FormEvent) => {
    e.preventDefault();
    if (busy) return; // 이중 제출 방지 — 느린 네트워크에서 엔터 연타가 두 번 만든다.
    const u = name.trim();
    if (!u || !password) {
      // 빈 제출을 조용히 무시하지 않는다 — 눌렀는데 아무 일도 안 나면 고장으로 읽힌다.
      setError('아이디와 비밀번호를 입력하세요.');
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await createUser({ username: u, password, role });
      toast(`사용자 ${u} 를 만들었습니다.`);
      reset();
      onCreated();
    } catch (err) {
      if (isAborted(err)) return;
      // 서버 사유를 그대로 보여준다(중복·비밀번호 길이·role 검증은 서버가 판정한다).
      const why = err instanceof ApiError && err.body.error ? err.body.error : '';
      setError(why ? `사용자를 만들지 못했습니다 — ${why}` : '사용자를 만들지 못했습니다.');
    } finally {
      setBusy(false);
    }
  }, [busy, name, onCreated, password, reset, role, toast]);

  return (
    <Card
      title="사용자"
      className="mt"
      aside={
        <span className="row">
          <span className="help">전체 {n(users.length)}명 · 관리자 {n(admins)}명</span>
          <button type="button" className="primary sm" onClick={() => (adding ? reset() : setAdding(true))}>
            {adding ? '추가 취소' : '사용자 추가'}
          </button>
        </span>
      }
    >
      {adding && (
        <form className="row mb" noValidate onSubmit={submit}>
          <label className="help" htmlFor={nameId}>아이디</label>
          <input id={nameId} type="text" value={name} autoComplete="off" autoCapitalize="none" spellCheck={false}
            disabled={busy} onChange={(e) => { setName(e.target.value); setError(null); }} />
          <label className="help" htmlFor={pwId}>비밀번호</label>
          <input id={pwId} type="password" value={password} autoComplete="new-password"
            disabled={busy} onChange={(e) => { setPassword(e.target.value); setError(null); }} />
          <label className="help" htmlFor={roleId}>역할</label>
          <select id={roleId} value={role} disabled={busy} onChange={(e) => setRole(e.target.value as UserRole)}>
            <option value="member">구성원</option>
            <option value="admin">관리자</option>
          </select>
          <button type="submit" className="primary" disabled={busy} aria-busy={busy}>
            {busy ? '만드는 중…' : '만들기'}
          </button>
        </form>
      )}
      {error && <p className="help txt-err mb" role="alert">{error}</p>}

      <TableWrap>
        <table>
          <thead>
            <tr>
              <th>사용자</th><th>역할</th><th>팀</th>
              <th className="num">계정 생성</th><th className="num">활성 키</th><th aria-hidden="true" />
            </tr>
          </thead>
          <tbody>
            {users.map((u) => {
              const keys = activeKeys.get(u.username) ?? 0;
              const open = () => onOpen(u.username);
              return (
                <tr
                  key={u.username}
                  className="rowlink"
                  /* 행이 곧 버튼이다 — 마우스만 되는 드릴다운은 키보드 사용자에게 통째로 사라진다. */
                  tabIndex={0}
                  role="button"
                  aria-label={`${u.username} 관리`}
                  onClick={open}
                  onKeyDown={(e) => {
                    if (e.key !== 'Enter' && e.key !== ' ') return;
                    e.preventDefault();
                    open();
                  }}
                >
                  <td>{u.username}</td>
                  <td>
                    {/* 색만으로 말하지 않는다 — 역할은 글자다. */}
                    <span className={`badge ${u.role === 'admin' ? 'ok' : ''}`}>{roleLabel(u.role)}</span>
                  </td>
                  <td>{u.team ? u.team : <span className="help">미배정</span>}</td>
                  <td className="mono num" title={fmtTime(u.createdAt)}>{String(u.createdAt).slice(0, 10)}</td>
                  {/*
                    0 에 ⚠ 를 달지 않는다. 키가 없는 것은 이상 상태가 아니라 아직 연동하지 않은
                    것이고, 이상 없는 곳에 경고를 달면 진짜 경고(결속 없는 키)가 소음에 묻힌다.
                  */}
                  <td className="num">{n(keys)}</td>
                  {/* 비대화형 표시자. 행 안에 다른 대화형 요소를 넣지 않는다. */}
                  <td className="help" aria-hidden="true">›</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </TableWrap>

      <p className="help mt-sm">행을 누르면 그 사용자의 관리 시트가 열립니다.</p>
      {users.length === 1 && (
        <p className="help mt-sm">아직 당신뿐입니다. 팀원을 추가하면 사용량이 사람 이름으로 갈립니다.</p>
      )}
      {/*
        마지막 로그인 시각은 서버 스키마에 없다(auth_users 에 그 열이 없다). 조용히 빈칸으로 두면
        "로그인한 적이 없다"로 읽히므로, 열을 만들지 않고 못 잰다고 밝힌다.
      */}
      <p className="help mt-sm">마지막 로그인 시각은 서버가 기록하지 않습니다 — 계정 생성일만 낼 수 있습니다.</p>
    </Card>
  );
}
