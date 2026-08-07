'use client';

import { useId, useRef, useState, useEffect } from 'react';

/*
 * 토큰 게이트 — **화면 전체를 대체한다.**
 * 부분 배너로 두면 뒤의 탭이 계속 401 을 때려 토스트만 쌓인다(현행 public/app.js 의 결정).
 */
export default function TokenGate({
  note, onSubmit,
}: {
  note?: string | null;
  onSubmit: (token: string) => void;
}) {
  const inputId = useId();
  const noteId = useId();
  const input = useRef<HTMLInputElement>(null);
  const [empty, setEmpty] = useState(false);

  useEffect(() => { input.current?.focus(); }, []);

  return (
    <div className="login-wrap">
      <div className="card glass" style={{ maxWidth: 460, width: '100%' }}>
        <h2>사용량 대시보드</h2>
        <p className="help mt-sm">
          이 화면은 <b>사람별 사용량과 비용</b>을 담고 있어 토큰이 필요합니다.
          서버를 띄운 셸의 <span className="mono">USAGE_ADMIN_TOKEN</span> 값을 그대로 넣으세요.
        </p>

        {note && (
          <div className="card mt" style={{ borderColor: 'var(--err-bd)' }} role="alert">
            <div style={{ color: 'var(--err)', fontWeight: 600, fontSize: 13 }}>{note}</div>
          </div>
        )}

        <form
          className="mt"
          noValidate
          onSubmit={(e) => {
            e.preventDefault();
            const t = (input.current?.value ?? '').trim();
            if (!t) {
              // 빈 제출은 조용히 무시하지 않는다 — 눌렀는데 아무 일도 안 나면 고장으로 읽힌다.
              setEmpty(true);
              input.current?.focus();
              return;
            }
            setEmpty(false);
            onSubmit(t);
          }}
        >
          <label className="help" htmlFor={inputId}>토큰</label>
          <input
            id={inputId}
            ref={input}
            type="password"
            autoComplete="current-password"
            spellCheck={false}
            style={{ width: '100%' }}
            aria-label="사용량 대시보드 토큰"
            aria-invalid={empty || undefined}
            aria-describedby={empty ? noteId : undefined}
            onChange={() => empty && setEmpty(false)}
          />
          {empty && <div id={noteId} className="help txt-err mt-sm" role="alert">토큰을 입력하세요.</div>}
          <div className="row mt"><button className="primary" type="submit">열기</button></div>
        </form>
      </div>
    </div>
  );
}
