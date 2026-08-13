'use client';

import { useCallback, useEffect, useId, useRef, useState } from 'react';
import { ApiError, isAborted, login, type AuthUser } from '@/lib/api';

/*
 * 로그인 화면 — **화면 전체를 대체한다.**
 * 부분 배너로 두면 뒤의 대시보드가 계속 401 을 때려 토스트만 쌓인다(TokenGate 의 결정을 잇는다).
 *
 * 자격증명은 세션 쿠키로 실린다: login() 이 성공하면 서버가 httpOnly 쿠키를 내려주고, 여기서는
 * 반환된 user 만 셸에 넘긴다(onSuccess). 이 컴포넌트는 토큰을 만지지 않는다.
 *
 * 분기는 **status 로** 한다 — 401 은 "자격증명 오류"(폼 위 안내), 그 밖은 "연결 실패".
 * 문구에 걸면 서버가 문구를 다듬는 날 화면이 조용히 틀린 쪽으로 넘어간다.
 */
export default function Login({
  note,
  onSuccess,
}: {
  /** 세션 만료 등 셸이 전한 안내(선택). */
  note?: string | null;
  onSuccess: (user: AuthUser) => void;
}) {
  const userId = useId();
  const pwId = useId();
  const errId = useId();
  const userRef = useRef<HTMLInputElement>(null);

  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);

  useEffect(() => {
    userRef.current?.focus();
  }, []);

  const submit = useCallback(
    async (e: React.FormEvent) => {
      e.preventDefault();
      if (pending) return; // 이중 제출 방지 — 느린 네트워크에서 엔터 연타가 두 번 로그인한다.

      const u = username.trim();
      if (!u || !password) {
        // 빈 제출을 조용히 무시하지 않는다 — 눌렀는데 아무 일도 안 나면 고장으로 읽힌다.
        setError('아이디와 비밀번호를 입력하세요.');
        return;
      }

      setPending(true);
      setError(null);
      try {
        const user = await login(u, password);
        // 성공하면 셸이 대시보드로 전환하며 이 컴포넌트를 언마운트한다 — pending 을 되돌릴 필요가 없다.
        onSuccess(user);
      } catch (err) {
        if (isAborted(err)) return;
        setError(
          err instanceof ApiError && err.status === 401
            ? '아이디 또는 비밀번호가 올바르지 않습니다'
            : '로그인하지 못했습니다. 잠시 후 다시 시도하세요.',
        );
        setPending(false);
      }
    },
    [username, password, pending, onSuccess],
  );

  return (
    <div className="login-wrap">
      <main className="card glass" style={{ maxWidth: 400, width: '100%' }} aria-labelledby="login-title">
        <div className="brand" style={{ padding: '0 0 4px' }}>
          <span className="brand-mark" aria-hidden="true" />
          <span className="brand-name" id="login-title">
            사용량 대시보드
          </span>
        </div>
        <p className="help mt-sm">계속하려면 로그인하세요.</p>

        {note && (
          <div className="card mt" style={{ borderColor: 'var(--err-bd)' }} role="status">
            <div className="txt-err" style={{ fontWeight: 600, fontSize: 13 }}>{note}</div>
          </div>
        )}

        <form className="mt" noValidate onSubmit={submit}>
          <label className="help" htmlFor={userId}>아이디</label>
          <input
            id={userId}
            ref={userRef}
            name="username"
            type="text"
            autoComplete="username"
            autoCapitalize="none"
            spellCheck={false}
            disabled={pending}
            style={{ width: '100%' }}
            value={username}
            aria-invalid={error ? true : undefined}
            aria-describedby={error ? errId : undefined}
            onChange={(e) => {
              setUsername(e.target.value);
              if (error) setError(null);
            }}
          />

          <label className="help mt" htmlFor={pwId} style={{ display: 'block' }}>비밀번호</label>
          <input
            id={pwId}
            name="password"
            type="password"
            autoComplete="current-password"
            spellCheck={false}
            disabled={pending}
            style={{ width: '100%' }}
            value={password}
            aria-invalid={error ? true : undefined}
            aria-describedby={error ? errId : undefined}
            onChange={(e) => {
              setPassword(e.target.value);
              if (error) setError(null);
            }}
          />

          {error && (
            <div id={errId} className="help txt-err mt-sm" role="alert">
              {error}
            </div>
          )}

          <div className="row mt">
            <button className="primary" type="submit" disabled={pending} aria-busy={pending || undefined} style={{ width: '100%', justifyContent: 'center' }}>
              {pending ? '로그인 중…' : '로그인'}
            </button>
          </div>
        </form>
      </main>
    </div>
  );
}
