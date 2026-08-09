'use client';

import { useCallback, useState } from 'react';
import { getSessionDetail } from '@/lib/api';
import { useResource } from '@/hooks/useResource';
import type { SessionDetail, SessionsResponse } from '@/lib/types';
import { n, seconds, shortTokens, usd } from '@/lib/format';
import { ErrorState, Flag, Loading, TableWrap } from '@/components/ui';

/* 상위 세션 — 메트릭에서 개별 세션으로 내려가는 입구. */

function Detail({ id }: { id: string }) {
  const load = useCallback(({ signal }: { signal: AbortSignal }) => getSessionDetail(id, { signal }), [id]);
  const { state, reload } = useResource<SessionDetail>(load, [id]);

  if (state.status === 'loading') return <Loading />;
  if (state.status === 'error') return <ErrorState what="세션 상세를 불러오지 못했습니다." error={state.error} onRetry={reload} />;

  const d = state.data;
  const c = d.cost;
  const bk = Array.isArray(d.series) ? d.series : [];
  const counters = Object.entries(d.counters ?? {});
  const dur = d.session.startedAt && d.session.endedAt
    ? `${Math.round((Date.parse(d.session.endedAt) - Date.parse(d.session.startedAt)) / 60000)}분`
    : null;

  return (
    <section className="card glass">
      <div className="between">
        <h4 className="mono">{d.session.sessionId}</h4>
        <span className="mono">{c.priced ? usd(c.usd) : '단가 미등록'}</span>
      </div>
      <p className="help">
        캐시읽기 {usd(c.byAxis.cacheRead)} · 캐시생성 {usd(c.byAxis.cacheCreate)}
        {' · '}출력 {usd(c.byAxis.output)} · 입력 {usd(c.byAxis.input)}
        {dur && <> · 지속 {dur}</>}
        {!c.priced && <> · <Flag title="이 세션의 모델이 단가표에 없습니다 — 환산액 0 은 '공짜'가 아니라 '모른다'입니다.">단가 미등록</Flag></>}
        {!c.exact && <> <Flag title="시간 버킷(series)이 없어 세션 최빈 모델 1개로 계산했습니다. 모델이 섞인 세션이면 틀린 단가가 적용됩니다.">근사</Flag></>}
      </p>

      {/*
        시간 버킷이 있으면 그것이 이 세션의 진짜 모양이다 — 몇 시에, 어느 모델로, 얼마나 느렸고
        몇 번 실패했나. 세션 한 줄로는 답할 수 없던 질문이 여기서 끝난다.
      */}
      {bk.length > 0 && (
        <TableWrap>
          <table className="mt-sm">
            <caption className="help" style={{ captionSide: 'top', textAlign: 'left', paddingBottom: 4 }}>
              시간 버킷 — 시각 × 모델
            </caption>
            <thead>
              <tr>
                <th>시각</th><th>모델</th><th className="num">턴</th>
                <th className="num">캐시읽기</th><th className="num">평균지연</th><th className="num">오류</th>
              </tr>
            </thead>
            <tbody>
              {bk.map((b, i) => (
                <tr key={`${b.hour}-${b.model}-${i}`}>
                  <td className="mono">{String(b.hour).slice(8, 10)}일 {String(b.hour).slice(11, 13)}시</td>
                  <td className="mono">{b.model}</td>
                  <td className="mono num">{n(b.turns)}</td>
                  <td className="mono num" title={n(b.cacheRead)}>{shortTokens(b.cacheRead)}</td>
                  <td className="mono num">{b.latencyTurns ? seconds(b.latencyMsSum / b.latencyTurns) : '-'}</td>
                  <td className="mono num">{b.toolErrors ? n(b.toolErrors) : '-'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}

      {counters.length ? counters.map(([kind, items]) => (
        <div className="mt-sm" key={kind}>
          <b>{kind}</b>
          <div className="help">{(items ?? []).slice(0, 12).map((i) => `${i.key} ${n(i.count)}`).join(' · ')}</div>
        </div>
      )) : <div className="muted mt-sm">이 세션의 도구 사용 기록이 없습니다.</div>}

      {d.note && <p className="help mt-sm">{d.note}</p>}
    </section>
  );
}

export default function SessionsCard({ s }: { s: SessionsResponse }) {
  const [open, setOpen] = useState<string | null>(null);

  return (
    <section className="card glass mt">
      <h3>상위 세션 <span className="help">비용순 · 행을 누르면 상세</span></h3>
      <TableWrap>
        <table className="mt-sm">
          <thead>
            <tr>
              <th>시작</th><th>사용자</th><th>모델</th><th>프로젝트</th>
              <th className="num">턴</th><th className="num">캐시읽기</th><th className="num">비용</th>
            </tr>
          </thead>
          <tbody>
            {s.sessions.map((row) => {
              const on = open === row.sessionId;
              return (
                <tr
                  key={row.sessionId}
                  className="rowlink"
                  /* 행이 곧 버튼이다 — 마우스만 되는 드릴다운은 키보드 사용자에게 통째로 사라진다. */
                  tabIndex={0}
                  role="button"
                  aria-expanded={on}
                  onClick={() => setOpen(on ? null : row.sessionId)}
                  onKeyDown={(e) => {
                    if (e.key !== 'Enter' && e.key !== ' ') return;
                    e.preventDefault();
                    setOpen(on ? null : row.sessionId);
                  }}
                >
                  <td className="mono">{String(row.startedAt || '').slice(0, 16).replace('T', ' ')}</td>
                  <td>{row.username || '(미상)'}</td>
                  <td className="mono">{row.model || '(미상)'}</td>
                  <td>{row.project || ''}</td>
                  <td className="mono num">{n(row.turns)}</td>
                  <td className="mono num" title={n(row.cacheRead)}>{shortTokens(row.cacheRead)}</td>
                  <td className="mono num">{row.priced ? usd(row.usd) : <span className="help">단가 미등록</span>}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </TableWrap>
      {/* key 로 세션이 바뀔 때마다 트리를 새로 만든다 — 앞 세션의 늦은 응답이 새 상세를 덮지 못한다. */}
      <div className="mt-sm">{open && <Detail key={open} id={open} />}</div>
    </section>
  );
}
