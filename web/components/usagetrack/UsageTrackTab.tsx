'use client';

import { useCallback } from 'react';
import { getDispatch, getSummary } from '@/lib/api';
import { softly, useResource } from '@/hooks/useResource';
import type { DayRow, Dispatch, RecommendationSummary, Summary, UserRow } from '@/lib/types';
import { n, shortTokens } from '@/lib/format';
import { Card, Empty, ErrorState, Loading, TableWrap, TokenCount } from '@/components/ui';
import ModelTable from './ModelTable';
import AxisExplorer from './AxisExplorer';
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

/*
 * 카탈로그 공백 카드 — 이 화면에서 가장 값어치 있는 자리.
 * 추천이 실패한 목표들이 공유하는 토큰을 보여준다. 여기 오래 남는 단어가 곧 만들어야 할 에이전트다.
 */
function GapCard({ reco }: { reco?: RecommendationSummary }) {
  const gaps = reco?.gaps ?? [];
  const total = reco?.total ?? 0;
  const miss = reco?.miss ?? 0;
  const rate = total ? Math.round((miss / total) * 100) : 0;
  return (
    <Card
      title="카탈로그 공백"
      className="mb"
      accent={gaps.length > 0}
      aside={<span className="count">추천 {n(total)}건 중 매칭 실패 {n(miss)}건 ({rate}%)</span>}
    >
      {gaps.length ? (
        <>
          <p className="help mb">
            <b>매칭이 약했던</b> 목표들이 공유한 단어입니다(점수 1 이하 — 위 &lsquo;실패&rsquo;는 점수 0 만 센 것이라
            숫자가 다를 수 있습니다). <b>여기 반복해서 오르는 단어가 새 에이전트를 만들 자리</b>입니다.
          </p>
          <div className="row" style={{ gap: 6, flexWrap: 'wrap' }}>
            {gaps.map((g) => <span className="badge" key={g.token}>{g.token} <b>{n(g.count)}</b></span>)}
          </div>
        </>
      ) : (
        <>
          <Empty>{total ? '매칭이 약했던 목표가 아직 반복되지 않았습니다.' : '추천 호출 기록이 아직 없습니다.'}</Empty>
          <p className="help mt-sm">추천 API 를 호출하면 그 관측이 여기에 쌓입니다.</p>
        </>
      )}
    </Card>
  );
}

/* 일별 추이 — 막대 하나가 하루. 캐시읽기는 자릿수가 달라 같은 축에 두지 않고 툴팁에만 남긴다. */
function DayTrend({ rows }: { rows: DayRow[] }) {
  if (!rows.length) return null;
  const asc = rows.slice().reverse();
  const max = Math.max(...asc.map((r) => (r.output || 0) + (r.input || 0)), 1);
  return (
    <Card title="일별 추이" className="mb" aside={<span className="help">막대는 입력+출력 · 캐시읽기는 툴팁</span>}>
      <div className="udays">
        {asc.map((r) => {
          const h = Math.max(2, Math.round((((r.output || 0) + (r.input || 0)) / max) * 100));
          return (
            <div
              className="uday"
              key={r.day}
              title={`${r.day} · 출력 ${n(r.output)} · 입력 ${n(r.input)} · 캐시읽기 ${n(r.cacheRead)}`}
            >
              <div className="uday-bar" style={{ height: `${h}%` }} />
              <div className="uday-l">{String(r.day || '').slice(5)}</div>
            </div>
          );
        })}
      </div>
    </Card>
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

      {/* 세 입력 축의 관계를 한 줄로. 이게 없으면 '입력(비캐시)' 라는 이름만 남고 왜 작은지는 모른다. */}
      <p className="help mt-sm">
        입력(비캐시) + 캐시읽기 + 캐시생성 = 그 세션이 실제로 넣은 입력 전부입니다.
        같은 맥락을 다시 보낼 때는 캐시읽기로 잡히므로, 캐싱이 잘 도는 사람일수록 입력(비캐시)만 작아집니다.
      </p>

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
        <GapCard reco={d.recommendation} />
        <DayTrend rows={d.byDay ?? []} />

        <Card
          title="사용자별"
          className="mb"
          aside={<span className="help">이름이 실제 담당자와 다르면 <b>귀속 교정</b>(/api/usage/identity)으로 묶습니다</span>}
        >
          <UserTable rows={d.byUser ?? []} />
        </Card>

        <Card
          title="모델별"
          className="mb"
          aside={<span className="help">series 가 있는 세션은 모델별 정확값 · 없는 세션은 세션 최빈 모델 기준</span>}
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
