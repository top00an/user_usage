'use client';

import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react';
import {
  ApiError,
  deleteUser,
  isAborted,
  setUserPassword,
  setUserRole,
  setUserTeam,
} from '@/lib/api';
import type { AdminUser, UserRole } from '@/lib/types';
import Modal from '@/components/Modal';
import { useToast } from '@/components/Toast';
import { fmtTime } from '@/lib/format';
import { roleLabel, type SheetTarget } from '@/components/admin/derive';

/*
 * ── 사용자 시트 — 모든 변경이 일어나는 유일한 자리 ─────────────────────────
 *
 * 표는 읽기 전용이다. 변경을 표 안에 두면 (a) `role="button"` 인 행 안에 `<button>` 이 들어가
 * 접근성이 깨지고 (b) 390px 에서 그 버튼이 가로 스크롤 맨 오른쪽에 있어 손이 닿지 않는다.
 *
 * `Modal` 을 **그대로** 쓴다 — Esc·바깥클릭·Tab 트랩·포커스 복원이 이미 있고, 손으로 다시
 * 만들면 넷 중 하나가 빠진다(그리고 빠진 것은 키보드 사용자에게만 보인다).
 *
 * ── 이 파일이 지는 네 가지 ──────────────────────────────────────────────
 *
 * ① **확인의 무게를 되돌림 가능성에 맞춘다.** 팀 배정은 확인 없음(A), 역할·비밀번호는 인라인
 *    2단계(B), 사용자 삭제만 이름 재입력(C). 전부에 재입력을 붙이면 사람은 읽지 않고 친다.
 *
 * ② **모달을 겹치지 않는다.** 이 시트가 이미 모달이다 — 확인은 그 동작이 있던 자리를 그대로
 *    대체한다. 위에 확인 모달을 띄우면 포커스 트랩이 둘이 되고 Esc 가 어느 것을 닫는지 모른다.
 *
 * ③ **사전 판정을 믿지 않는다.** 마지막 관리자·본인 계정은 미리 비활성으로 막지만(이유를 보이는
 *    글자로 붙인다), 그 사이 다른 탭에서 상태가 바뀌었을 수 있다. 서버의 409 는 **시트를 닫지
 *    않고 그 자리에서** 뜬다 — 토스트로 던지고 닫으면 무엇을 눌렀는지 기억으로 복원해야 한다.
 *
 * ④ **`sessionsRevoked` 를 읽는다.** 강등·삭제·비밀번호 재설정에서 이 값이 `false` 면 **사고**다
 *    (그 사람은 세션 만료까지 옛 권한으로 남는다). 응답은 200 이고 다른 증상이 없으므로 화면이
 *    말하지 않으면 아무도 모른다.
 *
 * 낙관적 갱신을 하지 않는다 — 서버가 거부하는 동작이 있는 화면에서는 화면이 잠깐 거짓을 말한다.
 */

/** 사전 거부 문구(DESIGN-SPEC §3). 문구를 바꾸려면 그 문서를 먼저 고친다. */
const DENY = {
  lastAdminRole: '⚠ 마지막 관리자입니다 — 강등할 수 없습니다. 다른 사용자를 먼저 관리자로 올린 뒤 다시 시도하세요.',
  lastAdminDelete: '⚠ 마지막 관리자입니다 — 삭제할 수 없습니다. 지우면 아무도 사용자와 키를 관리할 수 없습니다.',
  selfRole: '⚠ 본인 계정입니다 — 스스로 강등할 수 없습니다. 다른 관리자에게 요청하세요.',
  selfDelete: '⚠ 본인 계정입니다 — 스스로 삭제할 수 없습니다. 다른 관리자에게 요청하세요.',
} as const;

const MIN_PASSWORD = 8;

/** 무엇을 확인하는 중인가. null 이면 각 섹션이 평상시 모습이다. */
type Stage = 'role' | 'password' | 'delete' | null;
type Busy = 'role' | 'team' | 'password' | 'delete' | null;

interface Alert {
  head: string;
  body: string;
  /** 화면이 낡았을 가능성 — [목록 새로고침] 을 붙인다(서버가 거부한 경우). */
  stale: boolean;
}

export default function UserSheet({
  target, self, onClose, onChanged,
}: {
  /** 시트를 연 시점의 값(derive.ts 의 SheetTarget). 이후 변경은 서버 응답으로 갱신한다. */
  target: SheetTarget;
  /** 로그인한 관리자 본인의 이름. 서버가 준 신원이다(getMe) — 화면이 세지 않는다. */
  self: string;
  onClose: () => void;
  /** 성공한 변경 뒤 목록을 다시 읽는다. 삭제면 closed=true 로 시트를 닫는다. */
  onChanged: (opts?: { closed?: boolean }) => void;
}) {
  const { adminCount, activeKeys } = target;
  /*
   * 이 사용자의 현재 값. **서버 응답의 user 로만** 갱신한다 — 화면이 추측한 값을 넣으면
   * 낙관적 갱신이 되고, 서버가 거부했을 때 시트가 잠깐 거짓을 말한다.
   */
  const [user, setUser] = useState<AdminUser>(target.user);
  const toast = useToast();
  const roleId = useId();
  const teamId = useId();
  const pwId = useId();
  const echoId = useId();
  const reasonRoleId = useId();
  const reasonDeleteId = useId();
  const echoRef = useRef<HTMLInputElement>(null);

  const [role, setRole] = useState<UserRole>(user.role);
  const [team, setTeam] = useState(user.team ?? '');
  const [password, setPassword] = useState('');
  const [showPw, setShowPw] = useState(false);
  const [stage, setStage] = useState<Stage>(null);
  const [busy, setBusy] = useState<Busy>(null);
  const [echo, setEcho] = useState('');
  const [alert, setAlert] = useState<Alert | null>(null);
  const [sameRole, setSameRole] = useState(false);

  const isSelf = user.username === self;
  const isLastAdmin = user.role === 'admin' && adminCount <= 1;
  /*
   * 강등·삭제만 막힌다. 승격은 사고가 아니므로 막지 않는다 — member 대상은 둘 다 열려 있다.
   * 본인 사유를 먼저 본다(더 구체적이다): 관리자가 둘인데 자기를 지우는 사고는 ②로 안 걸린다.
   */
  const roleReason = user.role !== 'admin' ? null : isSelf ? DENY.selfRole : isLastAdmin ? DENY.lastAdminRole : null;
  const deleteReason = isSelf ? DENY.selfDelete : isLastAdmin ? DENY.lastAdminDelete : null;

  /* 확인 블록이 열리면 입력칸으로 포커스를 옮긴다 — 안 옮기면 마우스로 다시 찾아야 한다. */
  useEffect(() => {
    if (stage === 'delete') echoRef.current?.focus();
  }, [stage]);

  /*
   * 서버 판정을 그대로 읽는다. **문구로 분기하지 않는다** — 4xx/5xx 면 그 문구를 보여주고,
   * 409(정상적인 거부)는 화면 상태를 되돌리지 않는다(서버는 아무것도 바꾸지 않았다).
   */
  const failed = useCallback((e: unknown, what: string) => {
    if (isAborted(e)) return;
    const msg = e instanceof ApiError && e.body.error ? e.body.error : `${what}하지 못했습니다.`;
    setAlert({
      head: msg,
      body: e instanceof ApiError && e.status === 409
        ? '서버가 이 변경을 거부했습니다 — 아무것도 바뀌지 않았습니다. 화면이 낡았을 수 있습니다.'
        : '화면이 낡았을 수 있습니다.',
      stale: true,
    });
  }, []);

  /** 끊겼어야 하는데 안 끊긴 세션 — 200 이지만 사고다(불변식 ④). */
  const checkSessions = useCallback((revoked: boolean, head: string) => {
    if (revoked) return false;
    setAlert({
      head,
      body: '요청은 성공했지만 그 사람의 세션이 살아 있습니다 — 세션이 만료될 때까지 관리자 권한으로 남아 있습니다. 서버 로그를 확인하세요.',
      stale: false,
    });
    return true;
  }, []);

  const onRole = useCallback(async () => {
    setBusy('role');
    setAlert(null);
    try {
      const res = await setUserRole(user.username, role);
      const demoted = user.role === 'admin' && role === 'member';
      setUser(res.user);
      setStage(null);
      onChanged();
      // 승격은 세션을 끊지 않는다(과잉 로그아웃 방지) — 강등만 끊겼어야 한다.
      if (demoted && checkSessions(res.sessionsRevoked, '강등했는데 그 사람의 세션이 끊기지 않았습니다')) return;
      toast(res.sessionsRevoked
        ? `${user.username} 를 ${roleLabel(role)}으로 바꿨습니다. 그 사람의 세션은 끊겼습니다.`
        : `${user.username} 를 ${roleLabel(role)}으로 바꿨습니다.`);
    } catch (e) {
      failed(e, '역할을 바꾸지 못했습니다');
    } finally {
      setBusy(null);
    }
  }, [checkSessions, failed, onChanged, role, toast, user.role, user.username]);

  const onTeam = useCallback(async () => {
    setBusy('team');
    setAlert(null);
    const next = team.trim();
    try {
      const res = await setUserTeam(user.username, next);
      setUser(res.user);
      onChanged();
      toast(next
        ? `${user.username} 를 ${next} 팀으로 배정했습니다.`
        : `${user.username} 의 팀 배정을 지웠습니다 — 미배정입니다.`);
    } catch (e) {
      if (!isAborted(e)) toast('팀을 저장하지 못했습니다.', 'err');
    } finally {
      setBusy(null);
    }
  }, [onChanged, team, toast, user.username]);

  const onPassword = useCallback(async () => {
    setBusy('password');
    setAlert(null);
    try {
      const res = await setUserPassword(user.username, password);
      setStage(null);
      setPassword('');
      setShowPw(false);
      onChanged();
      if (checkSessions(res.sessionsRevoked, '비밀번호를 바꿨는데 그 사람의 세션이 끊기지 않았습니다')) return;
      toast('비밀번호를 재설정했습니다. 그 사람의 세션은 끊겼습니다.');
    } catch (e) {
      failed(e, '비밀번호를 재설정');
    } finally {
      setBusy(null);
    }
  }, [checkSessions, failed, onChanged, password, toast, user.username]);

  const onDelete = useCallback(async () => {
    setBusy('delete');
    setAlert(null);
    try {
      const res = await deleteUser(user.username);
      if (checkSessions(res.sessionsRevoked, '삭제했는데 그 사람의 세션이 끊기지 않았습니다')) {
        onChanged();
        return;
      }
      toast(`${user.username} 를 삭제했습니다.`);
      onChanged({ closed: true });
    } catch (e) {
      failed(e, '사용자를 삭제');
    } finally {
      setBusy(null);
    }
  }, [checkSessions, failed, onChanged, toast, user.username]);

  /* 대소문자를 구분한다. 붙여넣기 공백만 봐준다(§4-C). */
  const echoMatches = echo.trim() === user.username;
  const pwTooShort = password.trim().length < MIN_PASSWORD;

  const keySentence = useMemo(() => (activeKeys > 0
    ? `발급된 인제스트 키 ${activeKeys}개는 남습니다 — 그 키는 계속 보고할 수 있으니 따로 해지하세요.`
    : '발급된 활성 인제스트 키가 없습니다.'), [activeKeys]);

  return (
    <Modal title={`${user.username} — 사용자 관리`} onClose={onClose} maxWidth={560}>
      {/*
        서버 거부·사고는 **시트 안에서** 뜬다. role="alert" 라 스크린리더가 즉시 읽는다.
      */}
      {alert && (
        /*
         * ⚠ 이 인라인 style 은 취향이 아니라 **클래스가 없어서** 남아 있다. "오류 톤 카드"(빨간
         * 테두리 + 아주 옅은 빨강 바탕)는 이 파일에서만 두 번 쓰는데(여기와 아래 위험 구역),
         * globals.css 에 그 이름이 없다. 토큰은 제대로 쓰고 있으므로(--err-bd / --err) 다크·라이트
         * 는 따라오지만, 두 곳이 **각자** 같은 값을 들고 있어 한쪽만 바뀌는 날이 온다.
         * → 새 클래스(예: `.card.err`)를 만드는 것은 계약 변경이라 이번 범위 밖이다. 사실만 남긴다.
         */
        <div
          className="card mt-sm"
          role="alert"
          style={{ borderColor: 'var(--err-bd)', background: 'color-mix(in srgb, var(--err) 7%, transparent)' }}
        >
          <div className="txt-err" style={{ fontWeight: 600 }}>⚠ {alert.head}</div>
          <p className="help mt-sm">{alert.body}</p>
          {alert.stale && (
            <div className="row mt-sm">
              <button type="button" className="ghost sm" onClick={() => { setAlert(null); onChanged(); }}>
                목록 새로고침
              </button>
            </div>
          )}
        </div>
      )}

      <div className="row mt-sm">
        <span className={`badge ${user.role === 'admin' ? 'ok' : ''}`}>{roleLabel(user.role)}</span>
        <span className="help">{user.team ? user.team : '미배정'}</span>
      </div>

      <dl className="pf-kv">
        <div className="pf-kv-row">
          <dt>계정 생성</dt>
          <dd title={fmtTime(user.createdAt)}>{fmtTime(user.createdAt)}</dd>
        </div>
        <div className="pf-kv-row">
          <dt>활성 인제스트 키</dt>
          <dd>{activeKeys}개</dd>
        </div>
      </dl>
      {/*
        귀속은 **문장이라 kv 행에 넣지 않는다.** `.pf-kv-row` 는 `1fr auto` 그리드라 긴 값이
        오른쪽에서 왼쪽 라벨을 밀고, 390px 에서 라벨이 '귀/속' 으로 쪼개진다(실물에서 확인).
        전폭 왼쪽 정렬 한 줄이 두 화면 너비에서 다 읽힌다.
      */}
      <p className="help mt-sm">
        <b>귀속</b> —{' '}
        {activeKeys > 0
          ? `키에 묶여 있습니다. 이 사람의 키로 들어온 사용량은 ${user.username} 로 잡힙니다.`
          : '묶인 활성 키가 없습니다 — 이 이름의 사용량은 보고한 PC 가 주장하는 이름에서 옵니다.'}
      </p>

      {/* ── 역할 (B급: 인라인 2단계) ── */}
      <h4 className="mt">역할</h4>
      {stage === 'role' ? (
        <>
          <p className="mt-sm">{roleLabel(user.role)} → {roleLabel(role)} 로 바꿉니다.</p>
          <p className="help mt-sm">
            {role === 'admin'
              ? `${user.username} 는 사용자 생성·역할 변경·삭제와 전체 키 현황을 할 수 있게 됩니다.`
              : `${user.username} 는 사용자·역할·팀·전체 키를 더는 관리할 수 없습니다.`}
            {' '}
            {role === 'member'
              ? `${user.username} 의 로그인 세션은 즉시 끊깁니다.`
              : `${user.username} 의 로그인 세션은 그대로 둡니다 — 승격은 과잉 로그아웃을 만들지 않습니다.`}
          </p>
          <div className="row mt-sm">
            {/* 취소가 DOM 에서 먼저다. */}
            <button type="button" className="ghost" disabled={busy === 'role'} onClick={() => setStage(null)}>취소</button>
            <button type="button" className="primary" disabled={busy === 'role'} aria-busy={busy === 'role'} onClick={onRole}>
              {busy === 'role' ? '바꾸는 중…' : '역할 변경 확정'}
            </button>
          </div>
        </>
      ) : (
        <>
          <div className="row mt-sm">
            {/* 라벨에서 `.help` 를 뗀다 — 그 클래스는 **보조 설명**(--fg-faint / .78rem)이라,
                라벨과 그 아래 설명문이 글자 크기·굵기·색까지 하나도 다르지 않게 된다. 라벨의
                모양은 globals.css 의 `.row > label` 이 준다(아래 세 라벨도 같다). */}
            <label htmlFor={roleId}>역할</label>
            <select
              id={roleId}
              value={role}
              disabled={!!roleReason}
              aria-describedby={roleReason ? reasonRoleId : undefined}
              onChange={(e) => { setRole(e.target.value as UserRole); setSameRole(false); }}
            >
              <option value="member">구성원</option>
              <option value="admin">관리자</option>
            </select>
            <button
              type="button"
              disabled={!!roleReason || busy !== null}
              aria-describedby={roleReason ? reasonRoleId : undefined}
              onClick={() => (role === user.role ? setSameRole(true) : setStage('role'))}
            >
              역할 변경
            </button>
          </div>
          {/*
            ── 왜 하나는 status 이고 하나는 alert 인가 ────────────────────────────
            사전 거부 사유는 시트를 **열 때부터** 그 자리에 있는 상시 안내다. 게다가 select·버튼이
            이미 aria-describedby 로 이 문단을 가리키므로(위 :aria-describedby), 포커스만 가면
            읽힌다 — 낭독을 끊을 이유가 없다. role="status" 로 존재만 알린다.
            '같은 역할' 은 반대다. **버튼을 누른 뒤에 새로 나타나는 판단 근거**라 AddUserForm 의
            기준선(누른 뒤 나타나는 근거는 전부 role="alert")을 그대로 탄다. 이게 없으면 화면을
            못 보는 사람에게는 [역할 변경] 을 눌렀는데 **아무 일도 일어나지 않는다.**
          */}
          {roleReason && <p className="help txt-warn mt-sm" id={reasonRoleId} role="status">{roleReason}</p>}
          {sameRole && <p className="help txt-warn mt-sm" role="alert">지금과 같은 역할입니다 — 바꿀 역할을 먼저 고르세요.</p>}
          <p className="help mt-sm">
            관리자는 사용자·역할·팀·전체 키를 관리할 수 있습니다. 역할을 바꾸면 그 사람의 로그인
            세션이 즉시 끊깁니다.
          </p>
        </>
      )}

      {/* ── 팀 (A급: 완전 복구 → 확인 없음) ── */}
      <h4 className="mt">팀</h4>
      <div className="row mt-sm">
        <label htmlFor={teamId}>팀</label>
        <input
          id={teamId}
          type="text"
          value={team}
          autoComplete="off"
          disabled={busy === 'team'}
          onChange={(e) => setTeam(e.target.value)}
        />
        <button type="button" disabled={busy !== null} aria-busy={busy === 'team'} onClick={onTeam}>
          {busy === 'team' ? '저장 중…' : '팀 저장'}
        </button>
      </div>
      <p className="help mt-sm">비우면 &apos;미배정&apos; 입니다. 팀은 비용 롤업에만 쓰입니다.</p>

      {/* ── 비밀번호 (B급: 인라인 2단계) ── */}
      <h4 className="mt">비밀번호</h4>
      {stage === 'password' ? (
        <>
          <p className="mt-sm">{user.username} 의 비밀번호를 새로 설정합니다.</p>
          <p className="help mt-sm">
            {user.username} 의 로그인 세션은 즉시 끊깁니다. 이 화면을 벗어나면 입력한 비밀번호를
            다시 볼 수 없습니다 — 본인에게 직접 전달하세요.
          </p>
          <div className="row mt-sm">
            <button type="button" className="ghost" disabled={busy === 'password'} onClick={() => setStage(null)}>취소</button>
            <button type="button" className="primary" disabled={busy === 'password'} aria-busy={busy === 'password'} onClick={onPassword}>
              {busy === 'password' ? '재설정 중…' : '비밀번호 재설정 확정'}
            </button>
          </div>
        </>
      ) : (
        <>
          <div className="row mt-sm">
            <label htmlFor={pwId}>새 비밀번호</label>
            <input
              id={pwId}
              type={showPw ? 'text' : 'password'}
              value={password}
              autoComplete="new-password"
              spellCheck={false}
              disabled={busy !== null}
              onChange={(e) => setPassword(e.target.value)}
            />
            <button type="button" className="ghost sm" aria-pressed={showPw} onClick={() => setShowPw((v) => !v)}>
              보기
            </button>
            <button type="button" disabled={pwTooShort || busy !== null} onClick={() => setStage('password')}>
              비밀번호 재설정
            </button>
          </div>
          <p className="help mt-sm">
            {MIN_PASSWORD}자 이상. 재설정하면 그 사람의 로그인 세션이 즉시 끊깁니다. 이 화면을
            벗어나면 다시 볼 수 없습니다 — 본인에게 직접 전달하세요.
          </p>
        </>
      )}

      {/* ── 위험 구역 (C급: 복구 불가 → 이름 재입력) ── */}
      <div style={{ borderTop: '1px solid var(--border-soft)', marginTop: 20, paddingTop: 16 }}>
        {/* 위 알림 카드와 **같은 인라인**이다 — 같은 이유(오류 톤 카드에 클래스가 없다)로 남는다.
            두 벌이라는 사실이 문제고, 그것을 지우려면 클래스를 새로 만들어야 한다(범위 밖). */}
        <div
          className="card"
          style={{ borderColor: 'var(--err-bd)', background: 'color-mix(in srgb, var(--err) 7%, transparent)' }}
        >
          {stage === 'delete' ? (
            <>
              <h4>정말 {user.username} 를 삭제합니까?</h4>
              <ul className="help mt-sm" style={{ margin: 0, paddingLeft: 18 }}>
                <li>로그인 계정이 사라집니다. 되돌릴 수 없습니다.</li>
                <li>진행 중인 세션이 즉시 끊깁니다.</li>
                <li>이미 수집된 사용량은 {user.username} 이름으로 그대로 남습니다.</li>
                <li>{keySentence}</li>
              </ul>
              {/* 여기는 `.row` 밖이라 위 라벨들과 달리 기본 본문 크기가 된다 — 그래야 맞다.
                  되돌릴 수 없는 동작의 **지시문**이고, 바로 아래 `.help` 한 줄(정확히 일치해야
                  한다)이 그 보조 설명이다. 둘이 같은 글자면 무엇이 지시인지 알 수 없다. */}
              <label className="mt" htmlFor={echoId} style={{ display: 'block' }}>
                확인하려면 사용자 이름을 그대로 입력하세요
              </label>
              <input
                id={echoId}
                ref={echoRef}
                type="text"
                value={echo}
                autoComplete="off"
                autoCapitalize="none"
                spellCheck={false}
                aria-describedby={`${echoId}-help`}
                disabled={busy === 'delete'}
                onChange={(e) => setEcho(e.target.value)}
              />
              <p className="help mt-sm" id={`${echoId}-help`}>
                {user.username} 와 정확히 일치해야 삭제할 수 있습니다.
              </p>
              <div className="row mt" style={{ justifyContent: 'flex-end' }}>
                {/* 취소가 DOM 에서 먼저다 — 파괴 버튼이 초기 포커스를 가져가지 않는다. */}
                <button type="button" className="ghost" disabled={busy === 'delete'} onClick={() => { setStage(null); setEcho(''); }}>
                  취소
                </button>
                {/*
                 * ⚠ `minHeight: 44` 는 **우리 자체 기준**이고, 그 기준이 CSS 가 아니라 이 파일의
                 * 인라인 두 곳에만 산다. WCAG 2.2 (2.5.8 Target Size, Minimum, AA)가 요구하는 값은
                 * **24×24 CSS px** 이고, 44 는 그보다 훨씬 크게 잡은 값이다 — 되돌릴 수 없는
                 * 버튼이라 실수로 스치는 손가락이 눌러서는 안 되고, 반대로 누르려는 손가락은
                 * 한 번에 닿아야 한다(iOS HIG 44pt · Material 48dp 와 같은 근거).
                 * 규칙이 아니라 예외로 남아 있다는 것이 문제다: 다음에 생기는 파괴 버튼은 아무도
                 * 이 숫자를 모른다. 클래스·토큰으로 올리는 것은 계약 변경이라 이번 범위 밖이다.
                 */}
                <button
                  type="button"
                  className="danger"
                  style={{ minHeight: 44 }}
                  disabled={!echoMatches || busy === 'delete'}
                  aria-busy={busy === 'delete'}
                  onClick={onDelete}
                >
                  {busy === 'delete' ? '삭제 중…' : `${user.username} 삭제`}
                </button>
              </div>
            </>
          ) : (
            <>
              {/* 상자 이름은 '위험 구역'이다 — 무엇을 하는 자리인지 먼저 말한다(DESIGN-SPEC §2). */}
              <h4>위험 구역</h4>
              <p className="help mt-sm">
                <b>사용자 삭제</b> — 로그인 계정이 사라지고 진행 중인 세션이 즉시 끊깁니다.
                되돌릴 수 없습니다. 이미 수집된 사용량은 {user.username} 이름으로 그대로 남습니다.
                {' '}{keySentence}
              </p>
              <div className="row mt" style={{ justifyContent: 'flex-end' }}>
                {/* 위 삭제 확정 버튼과 같은 자체 기준 44px 이다(WCAG 2.2 요구치는 24×24) —
                    같은 값이 두 곳에 손으로 적혀 있다는 사실을 여기에도 남긴다. */}
                <button
                  type="button"
                  className="danger"
                  style={{ minHeight: 44 }}
                  disabled={!!deleteReason || busy !== null}
                  aria-describedby={deleteReason ? reasonDeleteId : undefined}
                  onClick={() => setStage('delete')}
                >
                  사용자 삭제
                </button>
              </div>
              {/* 위 roleReason 과 같은 성질(상시 안내 + aria-describedby)이라 같은 role 을 준다 —
                  둘 중 하나만 표시가 있으면 그 차이를 다음 사람이 규칙으로 읽는다. */}
              {deleteReason && <p className="help txt-warn mt-sm" id={reasonDeleteId} role="status">{deleteReason}</p>}
            </>
          )}
        </div>
      </div>
    </Modal>
  );
}
