'use client';

import { useCallback, useState } from 'react';
import {
  getSummary,
  isAborted,
  issueKey,
  listKeys,
  revokeKey,
  type IssuedKey,
  type KeyList,
} from '@/lib/api';
import type { Summary } from '@/lib/types';
import { softly, useResource } from '@/hooks/useResource';
import { fmtTime, n } from '@/lib/format';
import { Card, ErrorState, Loading, TableWrap } from '@/components/ui';
import { useToast } from '@/components/Toast';

/*
 * 연동(온보딩) — 관리자 전용.
 *
 * 개발자 머신에 수집기·훅을 붙이려면 인제스트 키가 필요하다. 이 화면은 셋만 한다:
 *   ① 키 발급        → 서버가 평문 key 를 **딱 한 번** 돌려준다(응답 1회뿐).
 *   ② 원라인 명령    → 그 평문 key 로 설치 명령을 만들어 크게 보여주고 복사 버튼을 준다.
 *                     여기서 복사하지 않으면 다시 볼 수 없으므로, 강조하고 경고를 붙인다.
 *   ③ 목록·해지      → 이후로는 masked 만 다룬다(평문은 서버도 안 준다).
 *
 * 평문 key 는 **메모리(state)에만** 둔다 — localStorage·쿠키 어디에도 쓰지 않는다.
 * 탭을 떠나 컴포넌트가 언마운트되면(Dashboard 가 key 로 트리를 버린다) 그대로 사라진다.
 */

interface OnboardData {
  keys: KeyList;
  /** 요약이 실패해도(구서버·권한) 이 화면은 살아야 한다 — fail-soft 로 null. */
  summary: Summary | null;
}

/**
 * 동결된 원라인 설치 명령. **이 형태 그대로** 만든다 — 서버가 install.sh 를 이 인자 계약으로 받는다.
 * 순수 함수로 떼어 테스트가 문자열을 직접 잡을 수 있게 한다(화면 렌더에 의존하지 않는다).
 */
export function installCommand(origin: string, key: string): string {
  return `curl -fsSL ${origin}/install.sh | sh -s -- --key ${key} --server ${origin}`;
}

/*
 * ── 인제스트 키가 무엇을 열고 무엇을 못 여는가 ────────────────────────────
 *
 * 운영자는 이 키를 팀원 PC 에 심는다. 그 순간 "이걸로 대시보드도 보이나?", "해지하면 언제
 * 끊기나?" 를 화면이 대신 답해 주지 않으면, 운영자는 가장 안전한 쪽이 아니라 **가장 넓은
 * 쪽으로 가정한다** — 열람 권한이 있다고 믿고 키를 아끼거나, 반대로 유출을 방치한다.
 *
 * 아래 사실은 전부 서버 코드에서 확인된 것만 적는다(추정·계획 금지):
 *   · go/internal/httpapi/server.go — 인테이크 스코프는 `POST /api/usage` 하나만 통과시키고
 *     그 밖의 경로는 403 이다. 즉 이 키로는 대시보드를 열 수 없다.
 *   · go/internal/httpapi/agent.go — `GET /api/agent/collector` 는 인제스트 키로 통과한다.
 *   · go/internal/org/org.go — 키 해석은 요청마다 DB 조회이고 `revoked_at IS NULL` 조건이라
 *     해지가 즉시 반영된다(캐시 없음). 해지된 키는 보고·다운로드 모두 401.
 *   · 저장은 sha256 해시뿐이다(HashKey) — 그래서 평문은 발급 응답 그 1회가 전부다.
 *
 * 접근성: 상태를 **색이 아니라 글자**로 말한다(허용/차단/주의 배지 텍스트). 색만으로 구분하면
 * 색각 이상 사용자에게 '허용'과 '차단'이 같은 회색 칩이 된다 — SupportBadge 와 같은 규율.
 */
const SCOPE_FACTS: { kind: string; cls: string; term: string; body: React.ReactNode }[] = [
  {
    kind: '허용', cls: 'ok', term: '사용량 보고',
    body: <>이 키는 <code className="mono">POST /api/usage</code> <b>하나만</b> 엽니다.</>,
  },
  {
    kind: '허용', cls: 'ok', term: '수집기 다운로드',
    body: <><code className="mono">GET /api/agent/collector</code> 로 수집기 바이너리를 받습니다.</>,
  },
  {
    kind: '차단', cls: 'mute', term: '대시보드 열람 불가',
    body: <>이 키로 조회를 시도하면 <b>403</b> 입니다. 열람은 로그인 계정으로 합니다.</>,
  },
  {
    kind: '해지', cls: '', term: '즉시 반영',
    body: <>해지하면 그 키의 보고·다운로드가 <b>바로 401</b> 이 됩니다.</>,
  },
  {
    kind: '보관', cls: '', term: '평문은 1회만',
    body: <>서버는 <b>sha256 해시만</b> 저장합니다 — 평문은 발급 시 한 번만 표시됩니다.</>,
  },
  {
    kind: '주의', cls: 'warn', term: '팀원 PC 마다 복제되는 자격',
    body: <>유출되면 그 배포에 임의 사용량이 들어올 수 있습니다. 의심되면 해지하고 재발급하세요.</>,
  },
];

/**
 * 키 스코프 명세. 발급 패널과 키 목록 **양쪽**에 같은 사실을 둔다 — 한쪽만 보고 판단하는 자리를
 * 남기지 않기 위해서다. 두 인스턴스가 한 화면에 동시에 뜨므로 aria-label 은 서로 달라야 한다
 * (같은 이름의 목록이 둘이면 스크린리더 사용자는 어느 쪽을 듣고 있는지 알 수 없다).
 */
export function KeyScope({ label }: { label: string }) {
  return (
    <div className="key-scope" role="list" aria-label={label}>
      {SCOPE_FACTS.map((f) => (
        <div className="key-scope-item" role="listitem" key={f.term}>
          <span className={`badge ${f.cls}`}>{f.kind}</span>
          <span className="key-scope-txt">
            <b>{f.term}</b> — {f.body}
          </span>
        </div>
      ))}
    </div>
  );
}

/** 클립보드 복사 — 최신 API 를 쓰되, 없거나 막힌 환경에서도 죽지 않게 textarea 로 되짚는다. */
async function copyText(text: string): Promise<boolean> {
  try {
    if (typeof navigator !== 'undefined' && navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    /* 아래 폴백으로 */
  }
  try {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.setAttribute('readonly', '');
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand('copy');
    document.body.removeChild(ta);
    return ok;
  } catch {
    return false;
  }
}

/* 발급 직후: 평문 key + 원라인 명령. 이 창을 닫으면 평문은 다시 볼 수 없다 — 그 사실을 크게 말한다. */
function IssuedPanel({ issued, onDismiss }: { issued: IssuedKey; onDismiss: () => void }) {
  const toast = useToast();
  const origin = typeof window !== 'undefined' ? window.location.origin : '';
  const cmd = installCommand(origin, issued.key);

  const copy = useCallback(async (text: string, what: string) => {
    const ok = await copyText(text);
    toast(ok ? `${what}을 복사했습니다` : '복사하지 못했습니다 — 직접 선택해 복사하세요', ok ? 'info' : 'err');
  }, [toast]);

  return (
    <section className="card glass key-hero mt" role="status" aria-live="polite" aria-label="발급된 인제스트 키">
      <div className="between mb">
        <h3>새 인제스트 키가 발급되었습니다</h3>
        <span className="badge ok">방금 발급</span>
      </div>

      <p className="txt-warn">
        ⚠ 이 키는 <b>지금만 표시</b>됩니다 — 이 창을 닫으면 평문 키를 다시 볼 수 없습니다.
        아래 설치 명령을 복사해 개발자에게 전달하세요.
      </p>

      <label className="help mt" htmlFor="issued-key">평문 키</label>
      <div className="row" style={{ flexWrap: 'nowrap' }}>
        <code id="issued-key" className="install-cmd mono" style={{ flex: 1 }}>{issued.key}</code>
        <button type="button" className="ghost sm" onClick={() => copy(issued.key, '키')}>키 복사</button>
      </div>

      <label className="help mt" htmlFor="install-cmd">원라인 설치 명령</label>
      <div className="row" style={{ flexWrap: 'nowrap', alignItems: 'stretch' }}>
        <pre id="install-cmd" className="install-cmd mono" style={{ flex: 1 }}>{cmd}</pre>
        <button type="button" className="primary" onClick={() => copy(cmd, '설치 명령')}>명령 복사</button>
      </div>
      <p className="help mt-sm">
        개발자 머신에서 이 한 줄을 실행하면 수집기와 훅이 자동 설치되어 바로 연동됩니다.
      </p>

      {/* 이 키가 무엇을 여는 자격인지 — 전달하기 직전이 가장 필요한 자리다. */}
      <div className="mt"><KeyScope label="발급된 키 스코프" /></div>

      <div className="row mt">
        <button type="button" className="ghost" onClick={onDismiss}>확인했어요 · 닫기</button>
      </div>
    </section>
  );
}

/* 발급된 키 목록 — masked·생성일·상태. 활성 키만 해지할 수 있고, 해지는 두 단계로 확인한다. */
function KeyTable({
  keys, onRevoke, revoking, confirmId, setConfirmId,
}: {
  keys: KeyList['keys'];
  onRevoke: (id: string) => void;
  revoking: boolean;
  confirmId: string | null;
  setConfirmId: (id: string | null) => void;
}) {
  if (!keys.length) {
    return (
      <Card title="발급된 키" className="mt">
        <p className="help">아직 발급된 키가 없습니다. 위에서 첫 키를 발급하세요.</p>
        <div className="mt"><KeyScope label="인제스트 키 스코프" /></div>
      </Card>
    );
  }
  return (
    <Card title="발급된 키" className="mt" aside={<span className="help">전체 {n(keys.length)}개</span>}>
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
              const confirming = confirmId === k.id;
              return (
                <tr key={k.id}>
                  <td className="mono wrap-any">{k.masked}</td>
                  <td className="mono num" title={fmtTime(k.createdAt)}>{fmtTime(k.createdAt)}</td>
                  <td>
                    {revoked
                      ? <span className="badge" title={fmtTime(k.revokedAt)}>해지됨</span>
                      : <span className="badge ok">활성</span>}
                  </td>
                  <td className="num">
                    {revoked ? (
                      <span className="help">—</span>
                    ) : confirming ? (
                      <span className="row" style={{ justifyContent: 'flex-end' }}>
                        <span className="help">이 키로 보고하던 머신은 즉시 차단됩니다.</span>
                        <button
                          type="button"
                          className="danger sm"
                          disabled={revoking}
                          aria-label={`${k.masked} 해지 확정`}
                          onClick={() => onRevoke(k.id)}
                        >
                          {revoking ? '해지 중…' : '해지 확정'}
                        </button>
                        <button type="button" className="ghost sm" disabled={revoking} onClick={() => setConfirmId(null)}>취소</button>
                      </span>
                    ) : (
                      <button
                        type="button"
                        className="ghost sm"
                        aria-label={`${k.masked} 해지`}
                        onClick={() => setConfirmId(k.id)}
                      >
                        해지
                      </button>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </TableWrap>
    </Card>
  );
}

export default function Onboarding() {
  const toast = useToast();
  const [issued, setIssued] = useState<IssuedKey | null>(null);
  const [issuing, setIssuing] = useState(false);
  const [revoking, setRevoking] = useState(false);
  const [confirmId, setConfirmId] = useState<string | null>(null);

  const load = useCallback(async ({ signal }: { signal: AbortSignal }): Promise<OnboardData> => {
    const [keys, summary] = await Promise.all([
      listKeys({ signal }),
      softly(getSummary({ signal }), null as Summary | null),
    ]);
    return { keys, summary };
  }, []);

  const { state, reload } = useResource(load, []);

  /* 발급 — 이중 클릭을 막는다(issuing 게이트). 성공하면 평문을 메모리에 담고 목록을 새로 읽는다. */
  const onIssue = useCallback(async () => {
    if (issuing) return;
    setIssuing(true);
    try {
      const res = await issueKey();
      setIssued(res);
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
      await revokeKey(id);
      setConfirmId(null);
      toast('키를 해지했습니다.');
      reload();
    } catch (e) {
      if (!isAborted(e)) toast('키 해지에 실패했습니다.', 'err');
    } finally {
      setRevoking(false);
    }
  }, [revoking, reload, toast]);

  const totals = state.status === 'ready' ? state.data.summary?.totals : undefined;

  return (
    <>
      <p className="lead">
        개발자 머신에서 아래 한 줄을 실행하면 수집기와 훅이 자동 설치되어 바로 연동됩니다.
      </p>

      <Card>
        <div className="between">
          <div>
            <h3>인제스트 키</h3>
            <p className="help mt-sm">
              머신마다 이 키로 사용량을 <b>보고</b>합니다 — 열람 자격이 아닙니다.
              아래 <b>발급된 키</b> 카드에 이 키로 되는 일과 안 되는 일을 적어 두었습니다.
            </p>
          </div>
          <button type="button" className="primary" onClick={onIssue} disabled={issuing} aria-busy={issuing}>
            {issuing ? '발급 중…' : '인제스트 키 발급'}
          </button>
        </div>
        {totals && (
          <p className="help mt">
            현재 연동: 머신 <b>{n(totals.machines)}</b>대 · 사용자 <b>{n(totals.users)}</b>명
          </p>
        )}
      </Card>

      {issued && <IssuedPanel issued={issued} onDismiss={() => setIssued(null)} />}

      {state.status === 'loading' && <div className="mt"><Loading label="발급된 키를 불러오는 중…" /></div>}
      {state.status === 'error' && (
        <div className="mt"><ErrorState what="발급된 키 목록을 불러오지 못했습니다." error={state.error} onRetry={reload} /></div>
      )}
      {state.status === 'ready' && (
        <KeyTable
          keys={state.data.keys.keys ?? []}
          onRevoke={onRevoke}
          revoking={revoking}
          confirmId={confirmId}
          setConfirmId={setConfirmId}
        />
      )}
    </>
  );
}
