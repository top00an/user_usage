'use client';

import { useCallback, useState } from 'react';
import type { IssuedKey } from '@/lib/api';
import Modal from '@/components/Modal';
import { useToast } from '@/components/Toast';
import { KeyScope, copyText, installCommand } from '@/components/onboarding/keyscope';

/*
 * 발급 직후: 평문 키 + 원라인 설치 명령. **카드가 아니라 모달이다.**
 *
 * 왜 승격했나. 현행은 다른 카드와 같은 `.card glass` 였다 — 스크롤로 지나칠 수 있고,
 * "확인했어요 · 닫기"는 언제든 다시 열 수 있는 알림처럼 읽힌다. **다시 볼 수 있게 생기면
 * 사람은 나중에 찾는다.** 모달은 본질적으로 일시적이고, 그것이 일회성 비밀의 의미와 맞는다
 * (스크롤로 지나칠 수 없고 포커스 트랩·복원이 이미 있다).
 *
 * 평문은 **메모리(state)에만** 있다. localStorage·쿠키·URL·로그·토스트 문구 어디에도 넣지
 * 않는다 — 토스트는 `키를 복사했습니다` 까지고 값을 싣지 않는다(test/admin.test.tsx 가 단정한다).
 */
export default function IssuedKeyModal({
  issued, mine = false, onClose,
}: {
  issued: IssuedKey;
  /** 내가 나에게 발급한 키인가(연동 탭). 스코프 표기가 '나에게 묶입니다'로 바뀐다. */
  mine?: boolean;
  onClose: () => void;
}) {
  const toast = useToast();
  const [copied, setCopied] = useState<'key' | 'cmd' | null>(null);
  const origin = typeof window !== 'undefined' ? window.location.origin : '';
  const cmd = installCommand(origin, issued.key);
  /** 서버는 결속 없는 키에 빈 문자열을 준다 — KeyScope 의 null(결속 없음)로 옮긴다. */
  const owner = issued.username || null;

  /*
   * 성공 문구를 **완성된 문장으로 받는다.** `${label}을` 로 붙이면 '키을 복사했습니다' 가 된다
   * (한국어 조사는 앞말의 종성에 따라 갈린다) — 현행 코드가 그렇게 새고 있던 자리다.
   */
  const copy = useCallback(async (text: string, what: 'key' | 'cmd', done: string) => {
    const ok = await copyText(text);
    if (ok) setCopied(what);
    toast(ok ? done : '복사하지 못했습니다 — 직접 선택해 복사하세요', ok ? 'info' : 'err');
  }, [toast]);

  /*
   * 닫기를 **막지 않는다**(모달에 사람을 가두지 않는다). 대신 복구 경로를 한 문장으로 준다 —
   * 배포되지 않은 키를 잃은 손실은 해지하고 재발급하면 끝이다.
   */
  const close = useCallback(() => {
    if (!copied) {
      toast('키를 복사하지 않고 닫았습니다 — 다시 볼 수 없습니다. 필요하면 해지하고 새로 발급하세요.', 'err');
    }
    onClose();
  }, [copied, onClose, toast]);

  return (
    <Modal title="새 인제스트 키 — 지금만 표시됩니다" onClose={close} maxWidth={640}>
      <p className="txt-warn">
        ⚠ 이 창을 닫으면 평문 키를 다시 볼 수 없습니다. 서버는 sha256 해시만 저장합니다.
      </p>
      <p className="help mt-sm">
        {owner
          ? <>이 키로 들어온 사용량은 <b>{owner}</b> 님의 것으로 잡힙니다.</>
          : <>이 키는 사용자에 묶여 있지 않습니다 — 이 키로 들어온 사용량은 보고한 PC 가 주장하는 이름으로 잡힙니다.</>}
      </p>

      {/*
        복사 버튼을 상자 **아래 줄**에 둔다. 한 줄(flexWrap:'nowrap')에 두면 390px 에서 버튼
        ~90px 가 358px 중 4분의 1을 가져가 명령 표시 폭이 그만큼 준다.
      */}
      <p className="help mt">평문 키</p>
      <code className="install-cmd mono">{issued.key}</code>
      <div className="row mt-sm">
        <button type="button" className="ghost sm" onClick={() => copy(issued.key, 'key', '키를 복사했습니다')}>키 복사</button>
        {copied === 'key' && <span className="help ok">✓ 복사했습니다</span>}
      </div>

      <p className="help mt">원라인 설치 명령</p>
      <pre className="install-cmd mono">{cmd}</pre>
      <div className="row mt-sm">
        <button type="button" className="primary" onClick={() => copy(cmd, 'cmd', '설치 명령을 복사했습니다')}>명령 복사</button>
        {copied === 'cmd' && <span className="help ok">✓ 복사했습니다</span>}
      </div>
      <p className="help mt-sm">
        개발자 머신에서 이 한 줄을 실행하면 수집기와 훅이 자동 설치되어 바로 연동됩니다.
      </p>

      {/* 이 키가 무엇을 여는 자격인지 — 전달하기 직전이 가장 필요한 자리다. */}
      <div className="mt"><KeyScope label="발급된 키 스코프" owner={mine && owner ? undefined : owner} /></div>
    </Modal>
  );
}
