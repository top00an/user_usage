'use client';

import { useCallback } from 'react';
import { getDispatch, getSummary } from '@/lib/api';
import { softly, useResource } from '@/hooks/useResource';
import type { Dispatch, Summary, UserRow } from '@/lib/types';
import { n, shortTokens } from '@/lib/format';
import { Card, Empty, ErrorState, Loading, TableWrap, TokenCount } from '@/components/ui';
import ModelTable from './ModelTable';
import AxisExplorer from './AxisExplorer';
import { setBuilderPrefill, type GroupBy, type Metric } from '@/lib/customPanels';

/* '그래프로 추가' — 대시보드 빌더에 프리필을 넘기고 대시보드 탭으로 이동한다. */
function PinButton({ metric, groupBy, title }: { metric: Metric; groupBy: GroupBy; title: string }) {
  return (
    <button
      type="button"
      className="pin-btn"
      onClick={() => { setBuilderPrefill({ metric, groupBy, type: groupBy === 'none' ? 'line' : 'bar', days: 30, title }); location.hash = '#/overview'; }}
    >
      ＋ 그래프로 추가
    </button>
  );
}
import { AX_CACHE_CREATE, AX_CACHE_READ, IN_HINT, IN_LABEL, TURNS_HINT } from './labels';

/* ================================================================
   사용 추적 — 동기화된 PC 들이 무엇을 얼마나 썼는가.

   세 가지를 한 화면에서 본다:
     ① 규모   토큰 사용량(입력·출력·캐시읽기·캐시생성)을 사람·모델·날짜로
     ② 행동   실제로 부른 도구·명령·스킬·에이전트·MCP
     ③ 공백   추천이 매칭에 실패한 목표의 토큰 — **새 에이전트를 만들 자리**

   ③ 이 이 화면의 존재 이유다. ①②만 보면 비용 대시보드에 그친다.

   캐시 읽기를 항상 따로 세운다 — 입력에 합치면 비용이 수십 배로 과대 표시된다(실측 71887 vs 2).
   ================================================================ */

interface TrackData {
  summary: Summary;
  dispatch: Dispatch | null;
}

/*
 * 보존 정책 문구 — 키워드 축만 기한이 있다.
 * 화면이 이 사실을 말하지 않으면 두 가지가 생긴다: 추세가 끊긴 이유를 아무도 모르고,
 * 팀은 자기 발화 데이터가 얼마나 남는지 모른다. 둘 다 화면이 답할 일이다.
 */
function RetentionNote({ retention }: { retention?: Summary['retention'] }) {
  const days = retention?.keywordDays;
  return days
    ? <span className="help">키워드는 <b>{n(days)}일</b> 보관 후 자동 삭제됩니다(다른 축과 사용량은 계속 보관).</span>
    : <span className="help">키워드 보존 기한이 설정돼 있지 않습니다 — 무기한 보관됩니다.</span>;
}

function TotalsTiles({ t }: { t: Summary['totals'] }) {
  return (
    <div className="grid cols-2">
      <div className="tile glass">
        <div className="k">보고된 세션</div>
        <div className="v">{n(t.sessions)}</div>
        <div className="s">{n(t.users)}명 · {n(t.machines)}대</div>
      </div>
      <div className="tile glass">
        <div className="k">출력 토큰</div>
        <div className="v" title={n(t.output)}>{shortTokens(t.output)}</div>
        <div className="s" title={IN_HINT}>
          {IN_LABEL} {shortTokens(t.input)} · 캐시읽기 {shortTokens(t.cacheRead)} · 캐시생성 {shortTokens(t.cacheCreate)}
        </div>
      </div>
    </div>
  );
}


function UserTable({ rows }: { rows: UserRow[] }) {
  if (!rows.length) return <Empty>아직 사용량 보고가 없습니다.</Empty>;
  return (
    <TableWrap>
      <table>
        <thead>
          <tr>
            <th>사용자</th>
            <th className="num" title={IN_HINT}>{IN_LABEL}</th>
            <th className="num" title={AX_CACHE_READ}>캐시읽기</th>
            <th className="num" title={AX_CACHE_CREATE}>캐시생성</th>
            <th className="num">출력</th>
            <th className="num" title={TURNS_HINT}>턴</th>
            <th className="num">세션</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.username}>
              <td className="mono">{r.username}</td>
              <td className="num"><TokenCount v={r.input} /></td>
              <td className="num"><TokenCount v={r.cacheRead} /></td>
              <td className="num"><TokenCount v={r.cacheCreate} /></td>
              <td className="num"><TokenCount v={r.output} /></td>
              <td className="num">{n(r.turns)}</td>
              <td className="num">{n(r.sessions)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </TableWrap>
  );
}

export default function UsageTrackTab() {
  /*
   * 두 조회를 한 로더에 묶는다 — 같은 signal 을 공유하므로 탭을 떠나면 **둘 다** 끊긴다.
   * '사람별 활용'(dispatch)은 fail-soft 다: 실패해도 위의 전체 집계는 그대로 보여야 한다.
   */
  const load = useCallback(async ({ signal }: { signal: AbortSignal }): Promise<TrackData> => {
    const [summary, dispatch] = await Promise.all([
      getSummary({ signal }),
      softly(getDispatch({ signal }), null as Dispatch | null),
    ]);
    return { summary, dispatch };
  }, []);

  const { state, reload } = useResource(load, []);

  if (state.status === 'loading') return <Loading />;
  if (state.status === 'error') return <ErrorState what="사용 추적을 불러오지 못했습니다." error={state.error} onRetry={reload} />;

  const d = state.data.summary;
  const t = d.totals ?? { sessions: 0, users: 0, machines: 0, input: 0, output: 0, cacheRead: 0, cacheCreate: 0 };
  const empty = !t.sessions;

  return (
    <>
      <p className="lead">
        동기화된 PC 들이 <b>무엇을 얼마나 썼는지</b>를 모읍니다.
        집계만 수집하며 <b>프롬프트 원문·파일 경로·명령 인자는 저장하지 않습니다.</b>{' '}
        <RetentionNote retention={d.retention} />
      </p>

      <TotalsTiles t={t} />

      {empty && (
        <Card title="아직 보고가 없습니다" className="mt">
          <p className="help">
            팀원 PC 가 <b>수집기를 갱신한 뒤 세션을 한 번 열면</b> 직전 세션들의 집계가 올라옵니다
            (수집기는 하루 1회 자동 갱신되므로 재설치는 필요 없습니다).
          </p>
          <p className="help mt-sm">
            각 PC 에서 끄려면 <span className="mono">TEAM_USAGE_DISABLE=1</span>,
            키워드 축만 빼려면 <span className="mono">TEAM_USAGE_NO_KEYWORDS=1</span> 입니다.
          </p>
        </Card>
      )}

      <div className="mt">

        <Card
          title="사용자별"
          className="mb"
          aside={<span className="aside-row"><span className="help">이름이 실제 담당자와 다르면 <b>귀속 교정</b>(/api/usage/identity)으로 묶습니다</span><PinButton metric="cost" groupBy="user" title="사용자별 비용" /></span>}
        >
          <UserTable rows={d.byUser ?? []} />
        </Card>

        <Card
          title="모델별"
          className="mb"
          aside={<span className="aside-row"><span className="help">series 가 있는 세션은 모델별 정확값 · 없는 세션은 세션 최빈 모델 기준</span><PinButton metric="tokens" groupBy="model" title="모델별 토큰" /></span>}
        >
          <ModelTable rows={d.byModel ?? []} axis={d.modelAxis} />
        </Card>

        <AxisExplorer
          top={d.top ?? {}}
          dispatch={state.data.dispatch}
          dispatchFailed={state.data.dispatch === null}
        />
      </div>
    </>
  );
}
