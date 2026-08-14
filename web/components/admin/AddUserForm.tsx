'use client';

/*
 * 사용자 추가 폼 — **계정을 만드는 화면**이다.
 *
 * 예전에는 표 위에 `.row` 한 줄이었다: 아이디·비밀번호·역할이 라벨과 나란히 붙어 있고, 규칙은
 * 아무것도 적혀 있지 않았다. 그 폼의 실패 방식은 조용하다 —
 *   · 비밀번호가 8자 미만이면 **서버 왕복 뒤에야** 400 으로 거절된다(규칙을 미리 안 알려준다).
 *   · 확인 입력이 없으니 오타가 그대로 저장되고, 그 사실은 **그 사람이 로그인을 못 할 때** 드러난다.
 *     관리자가 만든 계정이라 본인은 초기 비밀번호를 확인할 방법도 없다.
 *   · 아이디 중복은 이미 화면이 아는 사실인데도 서버에 물어본 뒤 실패로 알려 준다.
 * 계정 생성은 되돌리기가 비싼 조작이다(지우면 세션·인제스트 키까지 함께 거둬진다). 그래서
 * **누르기 전에** 알 수 있는 것은 전부 누르기 전에 말한다.
 *
 * ── 규칙의 주인은 서버다 ────────────────────────────────────────────────
 *
 * 최소 길이 8은 store.MinPasswordLen 과 **같은 값**이고, 여기 검증은 방어가 아니라 안내다
 * (서버가 마지막 방어선이다 — go/internal/store/users.go 머리말). 서버가 막지 않는 것을 화면이
 * 막으면 그 자리는 "이유 없이 안 되는 칸"이 되므로, 화면만의 규칙은 **공백 금지 하나**로 끝낸다.
 */
import { useId, useMemo, useState } from 'react';
import { ApiError, createUser, isAborted, setUserTeam } from '@/lib/api';
import type { AdminUser, UserRole } from '@/lib/types';
import { useToast } from '@/components/Toast';

/** store.MinPasswordLen 과 같은 값. 서버가 룬 수로 세므로 여기서도 룬 수로 센다(한글 비밀번호). */
const MIN_PASSWORD_LEN = 8;

interface FieldErrors {
  name?: string;
  password?: string;
  confirm?: string;
}

/**
 * 칸별 안내 문구. **제출 가능 여부와 문구가 같은 함수에서 나온다** — 버튼을 비활성으로 만드는
 * 조건과 화면에 뜨는 이유가 갈리면 "왜 안 눌리는지 모르는 폼"이 된다.
 */
function validate(v: { name: string; password: string; confirm: string; taken: Set<string> }): FieldErrors {
  const e: FieldErrors = {};
  if (!v.name) e.name = '아이디를 입력하세요.';
  else if (/\s/.test(v.name)) e.name = '아이디에 공백을 넣을 수 없습니다.';
  else if (v.taken.has(v.name.toLowerCase())) e.name = '이미 있는 아이디입니다.';

  if (!v.password) e.password = '비밀번호를 입력하세요.';
  else if ([...v.password].length < MIN_PASSWORD_LEN) e.password = `비밀번호는 최소 ${MIN_PASSWORD_LEN}자여야 합니다.`;

  if (!v.confirm) e.confirm = '비밀번호를 한 번 더 입력하세요.';
  else if (v.confirm !== v.password) e.confirm = '비밀번호가 서로 다릅니다.';
  return e;
}

/**
 * 비밀번호 권고 등급. **서버 규칙이 아니다** — 서버는 길이만 본다. 그래서 이 등급은 제출을
 * 막지 않고 색과 문구로만 말한다(막으면 규칙이 두 벌이 된다).
 */
function strengthOf(pw: string): { level: 0 | 1 | 2 | 3; label: string } {
  const len = [...pw].length;
  if (len === 0) return { level: 0, label: '' };
  const kinds = [/[a-z]/, /[A-Z]/, /[0-9]/, /[^a-zA-Z0-9]/].filter((re) => re.test(pw)).length;
  if (len < MIN_PASSWORD_LEN) return { level: 1, label: '너무 짧습니다' };
  if (len >= 12 && kinds >= 3) return { level: 3, label: '강함' };
  if (len >= 10 || kinds >= 2) return { level: 2, label: '보통' };
  return { level: 1, label: '약함 — 길이나 문자 종류를 늘리세요' };
}

export default function AddUserForm({
  users, onCancel, onCreated,
}: {
  /** 이미 있는 사용자 — 중복 아이디를 **서버에 묻기 전에** 잡는 근거다. */
  users: AdminUser[];
  onCancel: () => void;
  onCreated: () => void;
}) {
  const toast = useToast();
  const nameId = useId();
  const pwId = useId();
  const confirmId = useId();
  const teamId = useId();
  const pwHintId = useId();
  const nameHintId = useId();

  const [name, setName] = useState('');
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [role, setRole] = useState<UserRole>('member');
  const [team, setTeam] = useState('');
  const [reveal, setReveal] = useState(false);
  const [busy, setBusy] = useState(false);
  /** 제출을 한 번 눌렀는가 — 그전에는 빨간 글씨를 뿌리지 않는다(타이핑 중 훈계 금지). */
  const [tried, setTried] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const taken = useMemo(
    () => new Set(users.map((u) => u.username.trim().toLowerCase())),
    [users],
  );

  const trimmed = name.trim();
  const errors = useMemo(
    () => validate({ name: trimmed, password, confirm, taken }),
    [confirm, password, taken, trimmed],
  );

  const show = (k: keyof FieldErrors) => (tried ? errors[k] : undefined);
  const strength = strengthOf(password);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (busy) return;   // 이중 제출 방지 — 느린 네트워크에서 엔터 연타가 두 번 만든다.
    setTried(true);
    setError(null);
    if (errors.name || errors.password || errors.confirm) return;

    setBusy(true);
    const u = trimmed;
    const t = team.trim();
    try {
      await createUser({ username: u, password, role });
      /*
       * 팀은 **다른 엔드포인트**다(POST /api/admin/users/team). 그래서 계정은 만들어졌는데 팀
       * 배정만 실패할 수 있다. 그때 "실패"라고만 말하면 관리자는 같은 아이디로 다시 만들려 하고
       * 중복 오류를 만난다 — 무엇이 됐고 무엇이 안 됐는지 나눠 말한다(팀은 시트에서 고칠 수 있다).
       */
      if (t) {
        try {
          await setUserTeam(u, t);
        } catch (teamErr) {
          if (!isAborted(teamErr)) {
            toast(`사용자 ${u} 는 만들었지만 팀 배정은 실패했습니다 — 행을 눌러 시트에서 배정하세요.`);
            onCreated();
            return;
          }
        }
      }
      toast(`사용자 ${u} 를 만들었습니다.${t ? ` 팀: ${t}` : ''}`);
      onCreated();
    } catch (err) {
      if (isAborted(err)) return;
      // 서버 사유를 그대로 보여준다(중복·길이·role 판정의 최종 권한은 서버다).
      const why = err instanceof ApiError && err.body.error ? err.body.error : '';
      setError(why ? `사용자를 만들지 못했습니다 — ${why}` : '사용자를 만들지 못했습니다.');
    } finally {
      setBusy(false);
    }
  };

  return (
    <form className="uform mb" noValidate onSubmit={submit} aria-labelledby={`${nameId}-legend`}>
      <p className="uform-legend" id={`${nameId}-legend`}>새 계정 만들기</p>

      <div className="uform-grid">
        <div className="uform-field">
          <label htmlFor={nameId}>아이디 <span className="req" aria-hidden="true">*</span></label>
          <input
            id={nameId}
            type="text"
            value={name}
            autoFocus
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            disabled={busy}
            aria-invalid={show('name') ? true : undefined}
            aria-describedby={show('name') ? `${nameId}-err` : nameHintId}
            onChange={(e) => { setName(e.target.value); setError(null); }}
          />
          {show('name')
            ? <p className="uform-err" id={`${nameId}-err`} role="alert">{show('name')}</p>
            : <p className="uform-hint" id={nameHintId}>로그인에 쓰는 이름입니다. 만든 뒤에는 바꿀 수 없습니다.</p>}
        </div>

        <div className="uform-field">
          <label htmlFor={teamId}>팀 <span className="opt">(선택)</span></label>
          <input
            id={teamId}
            type="text"
            value={team}
            autoComplete="off"
            disabled={busy}
            onChange={(e) => setTeam(e.target.value)}
          />
          <p className="uform-hint">비워 두면 미배정입니다. 나중에 시트에서 배정할 수 있습니다.</p>
        </div>

        <div className="uform-field">
          <label htmlFor={pwId}>비밀번호 <span className="req" aria-hidden="true">*</span></label>
          <span className="uform-pw">
            <input
              id={pwId}
              type={reveal ? 'text' : 'password'}
              value={password}
              autoComplete="new-password"
              disabled={busy}
              aria-invalid={show('password') ? true : undefined}
              aria-describedby={show('password') ? `${pwId}-err` : pwHintId}
              onChange={(e) => { setPassword(e.target.value); setError(null); }}
            />
            {/*
              * 보기 토글 — 관리자가 **남에게 전달할** 초기 비밀번호를 입력하는 자리다. 눈으로
              * 확인할 수 없으면 오타가 그대로 굳고, 그 사실은 그 사람이 로그인을 못 할 때 드러난다.
              */}
            <button
              type="button"
              className="ghost sm"
              aria-pressed={reveal}
              onClick={() => setReveal((v) => !v)}
            >{reveal ? '숨기기' : '보기'}</button>
          </span>
          {show('password')
            ? <p className="uform-err" id={`${pwId}-err`} role="alert">{show('password')}</p>
            : (
              <p className="uform-hint" id={pwHintId}>
                최소 {MIN_PASSWORD_LEN}자.
                {strength.label && (
                  <>
                    {' '}
                    <span className={`uform-strength s${strength.level}`}>{strength.label}</span>
                  </>
                )}
              </p>
            )}
        </div>

        <div className="uform-field">
          <label htmlFor={confirmId}>비밀번호 확인 <span className="req" aria-hidden="true">*</span></label>
          <input
            id={confirmId}
            type={reveal ? 'text' : 'password'}
            value={confirm}
            autoComplete="new-password"
            disabled={busy}
            aria-invalid={show('confirm') ? true : undefined}
            aria-describedby={show('confirm') ? `${confirmId}-err` : undefined}
            onChange={(e) => { setConfirm(e.target.value); setError(null); }}
          />
          {show('confirm')
            ? <p className="uform-err" id={`${confirmId}-err`} role="alert">{show('confirm')}</p>
            /* 일치는 **입력하는 동안** 알려 준다 — 틀린 것을 제출 버튼까지 끌고 가지 않게. */
            : <p className="uform-hint">{confirm && confirm === password ? '일치합니다.' : ' '}</p>}
        </div>
      </div>

      {/*
        * 역할은 select 가 아니라 라디오다. 권한을 고르는 자리에서 select 는 접힌 채로 기본값을
        * 숨기고, 관리자 권한이 무엇을 뜻하는지 적을 자리도 없다.
        */}
      <fieldset className="uform-roles">
        <legend>역할</legend>
        <label className={role === 'member' ? 'on' : undefined}>
          <input type="radio" name="role" value="member" checked={role === 'member'}
            disabled={busy} onChange={() => setRole('member')} />
          <span className="t">구성원</span>
          <span className="d">자기 사용량과 팀 현황을 봅니다.</span>
        </label>
        <label className={role === 'admin' ? 'on' : undefined}>
          <input type="radio" name="role" value="admin" checked={role === 'admin'}
            disabled={busy} onChange={() => setRole('admin')} />
          <span className="t">관리자</span>
          <span className="d">사용자·인제스트 키·단가를 관리합니다.</span>
        </label>
      </fieldset>

      {error && <p className="uform-err mb" role="alert">{error}</p>}

      <div className="uform-actions">
        <button type="button" className="ghost" disabled={busy} onClick={onCancel}>취소</button>
        <button type="submit" className="primary" disabled={busy} aria-busy={busy}>
          {busy ? '만드는 중…' : '사용자 만들기'}
        </button>
      </div>
    </form>
  );
}
