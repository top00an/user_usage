'use client';

/*
 * 키 해지 — B급(재작업으로 복구) 확인. **연동 탭과 관리 탭이 같은 것을 쓴다.**
 * 같은 동작을 두 곳에 따로 만들면 한쪽 문구만 강화되는 날이 오고, 그때 두 화면은 같은 버튼에
 * 다른 무게를 말한다.
 *
 * 왜 삭제와 같은 무게(이름 재입력)를 주지 않는가. 해지로 잃는 것은 그 머신의 재설치 수고뿐이고
 * 재발급으로 완전히 복구된다. 여기에 재입력을 붙이면 정작 **사용자 삭제** 확인을 안 읽는다
 * (확인 피로 — DESIGN-SPEC §4).
 *
 * 확인은 **그 동작이 있던 자리를 그대로 대체한다.** 모달을 겹치지 않는다(관리 탭에서는 이 표가
 * 이미 모달 밖이고, 시트 위에 확인 모달을 띄우면 포커스 트랩이 둘이 된다).
 */
export default function RevokeCell({
  masked, revoked, confirming, busy, onAsk, onCancel, onConfirm,
}: {
  masked: string;
  revoked: boolean;
  confirming: boolean;
  busy: boolean;
  onAsk: () => void;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  if (revoked) return <span className="help">—</span>;

  if (!confirming) {
    return (
      <button type="button" className="ghost sm" aria-label={`${masked} 해지`} onClick={onAsk}>
        해지
      </button>
    );
  }

  return (
    <span className="row" style={{ justifyContent: 'flex-end' }}>
      <span className="help">
        이 키로 보고하던 머신이 즉시 차단됩니다. 되돌릴 수 없습니다 — 새 키를 발급해 그 머신에
        다시 설치해야 합니다.
      </span>
      {/*
        DESIGN-SPEC §4-B 의 배치 그대로 [해지 확정][취소] 다. 여기서는 확인 블록이 열릴 때
        포커스를 옮기지 않으므로(표 안이고, 누른 버튼이 이 자리에 있었다) 파괴 버튼이 초기
        포커스를 가져가는 문제가 없다 — 포커스를 옮기는 §4-C(사용자 삭제)에서만 취소를 앞에 둔다.
      */}
      <button
        type="button"
        className="danger sm"
        style={{ minHeight: 44 }}
        disabled={busy}
        aria-busy={busy}
        aria-label={`${masked} 해지 확정`}
        onClick={onConfirm}
      >
        {busy ? '해지 중…' : '해지 확정'}
      </button>
      <button type="button" className="ghost sm" disabled={busy} onClick={onCancel}>취소</button>
    </span>
  );
}
