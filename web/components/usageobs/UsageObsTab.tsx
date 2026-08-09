'use client';

import { useCallback, useState } from 'react';
import {
  getCoverage, getDistribution, getLeaderboard, getQuality, getSessions,
} from '@/lib/api';
import { softly, useResource } from '@/hooks/useResource';
import type {
  Coverage, Distribution, DistributionKey, Leaderboard, Quality, SessionsResponse,
} from '@/lib/types';
import { n, pctOf, relTime, fmtTime, seconds, shortTokens, usd } from '@/lib/format';
import { Card, ErrorState, Flag, Loading, TableWrap } from '@/components/ui';
import CostCard from './CostCard';
import SessionsCard from './SessionsCard';
import UserDetailModal from './UserDetailModal';

/*
 * 사용 관측 — 비용·분포·드릴다운.
 *
 * '사용 추적'이 합계를 보여준다면 이 화면은 **"왜 그 숫자인가"** 를 보여준다.
 * '언제 튀었나'(시간축 추이)는 '사용 추적'이 담당한다 — 여기서는 중복을 걷어내고
 * "왜"(축별 분해·분포·상위 세션)에 집중한다.
 */

interface ObsData {
  dist: Distribution;
  sessions: SessionsResponse;
  leaderboard: Leaderboard;
  quality: Quality | null;
  coverage: Coverage;
}

/* 분포 — 합계가 감춘 이상치를 끌어올린다. */
const DIST_DEFS: { k: DistributionKey; label: string; fmt: (v: unknown) => string; why: string }[] = [
  {
    k: 'cacheReadPerTurn', label: '턴당 캐시읽기', fmt: (v) => shortTokens(v),
    why: '한 요청이 실어 보낸 컨텍스트 크기. 캐시읽기가 큰 이유가 여기 있다.',
  },
  { k: 'sessionCostUsd', label: '세션당 비용', fmt: (v) => usd(v), why: '비싼 세션이 어디쯤인지.' },
  { k: 'turnsPerSession', label: '세션당 턴 수', fmt: (v) => n(v), why: '' },
  { k: 'cacheHitRate', label: '캐시 적중률', fmt: (v) => pctOf(v), why: '낮아지면 접두사가 깨지고 있다는 뜻.' },
];

function DistCard({ d }: { d: Distribution['distributions'] }) {
  return (
    <Card title="분포" className="mt">
      <TableWrap>
        <table className="mt-sm">
          <thead>
            <tr>
              <th>지표</th><th className="num">p50</th><th className="num">p95</th>
              <th className="num">p99</th><th className="num">최대</th>
            </tr>
          </thead>
          <tbody>
            {DIST_DEFS.map(({ k, label, fmt, why }) => {
              const s = d[k];
              if (!s?.n) {
                return <tr key={k}><td>{label}</td><td colSpan={4} className="muted">표본 없음</td></tr>;
              }
              return (
                <tr key={k}>
                  <td>{label}{why && <><br /><span className="help">{why}</span></>}</td>
                  <td className="mono num">{fmt(s.p.p50)}</td>
                  <td className="mono num">{fmt(s.p.p95)}</td>
                  <td className="mono num">{fmt(s.p.p99)}</td>
                  <td className="mono num">{fmt(s.max)}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </TableWrap>
    </Card>
  );
}

/* 품질축 — usage_series 에 이미 쌓이는 오류·거부·지연을 꺼낸다. 신수집기만 보내므로 커버리지를 함께 밝힌다. */
function QualityCard({ q }: { q: Quality | null }) {
  if (!q) return null;
  if (!q.turns) {
    return (
      <Card title="품질" className="mt">
        <p className="help">
          아직 시간 버킷(신수집기)이 없어 품질 지표를 낼 수 없습니다 —
          팀원 PC 가 수집기를 갱신하면 도구 오류율·거부율·지연이 여기 쌓입니다.
        </p>
      </Card>
    );
  }
  const rows: [string, string, string][] = [
    ['도구 오류율', pctOf(q.toolErrorRate), 'tool_result 가 오류로 돌아온 비율'],
    ['응답 거부율', pctOf(q.refusalRate), '모델이 거부(refusal)로 종료한 비율'],
    ['최대토큰 중단율', pctOf(q.stopMaxRate), '길이 상한에 걸려 잘린 비율'],
    ['평균 지연', seconds(q.latencyAvgMs), '턴당 응답 대기(평균)'],
    ['최대 지연', seconds(q.latencyMaxMs), '가장 느렸던 턴'],
  ];
  return (
    <Card
      title="품질"
      className="mt"
      aside={(
        <span className="help">
          신수집기 세션 {n(q.coverage?.sessionsWithSeries)}/{n(q.coverage?.sessionsTotal)} 커버 · 측정 턴 {n(q.latencyTurns)}
        </span>
      )}
    >
      <TableWrap>
        <table className="mt-sm">
          <thead><tr><th>지표</th><th className="num">값</th></tr></thead>
          <tbody>
            {rows.map(([k, v, why]) => (
              <tr key={k}>
                <td>{k}<br /><span className="help">{why}</span></td>
                <td className="mono num">{v}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </TableWrap>
    </Card>
  );
}

/* 사람별 리더보드 — byUser 에 비용·효율(캐시적중·세션당비용)을 얹어 팀 관점으로 세운다. */
function LeaderboardCard({ d, onDetail }: { d: Leaderboard; onDetail: (u: string) => void }) {
  const users = d?.users ?? [];
  if (!users.length) return null;
  return (
    <Card title="사용자별" className="mt" aside={<span className="help">API 환산액순 · 이 기간 전체</span>}>
      <TableWrap>
        <table className="mt-sm">
          <thead>
            <tr>
              <th>사용자</th><th className="num">세션</th><th className="num">턴</th>
              <th className="num">토큰</th><th className="num">캐시적중</th>
              <th className="num">세션당</th><th className="num">환산액</th>
              <th className="num"><span className="help">상세</span></th>
            </tr>
          </thead>
          <tbody>
            {users.slice(0, 15).map((u) => {
              const name = u.username || '(미상)';
              return (
                <tr key={name}>
                  <td>{name}</td>
                  <td className="mono num">{n(u.sessions)}</td>
                  <td className="mono num">{n(u.turns)}</td>
                  <td className="mono num" title={n(u.tokens)}>{shortTokens(u.tokens)}</td>
                  <td className="mono num">{pctOf(u.cacheHitRate, 0)}</td>
                  <td className="mono num">{u.priced ? usd(u.usdPerSession) : '-'}</td>
                  <td className="mono num">{u.priced ? usd(u.usd) : <span className="help">단가 미등록</span>}</td>
                  <td className="num">
                    {/* 같은 글자('상세')가 여러 줄에 반복되므로 라벨로 어느 행인지 말한다. */}
                    <button
                      type="button"
                      className="ghost sm"
                      aria-label={`${name} 사용 상세 열기`}
                      onClick={() => onDetail(name)}
                    >
                      상세
                    </button>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </TableWrap>
      {users.length > 15 && <p className="help">상위 15명 표시(전체 {n(users.length)}명).</p>}
    </Card>
  );
}

/* 수집 상태 — 발신처별 마지막 보고. "왜 데이터가 없나"에 화면이 직접 답한다. */
const STALE_MS = 2 * 24 * 3600 * 1000;

function ReporterCoverageCard({ d }: { d: Coverage }) {
  const reps = d?.reporters ?? [];
  if (!reps.length) return null;
  const now = d?.now ?? '';
  return (
    <Card title="수집 상태" className="mt" aside={<span className="help">발신처별 마지막 보고 — &ldquo;왜 데이터가 없나&rdquo;에 답합니다</span>}>
      <TableWrap>
        <table className="mt-sm">
          <thead>
            <tr>
              <th>발신처</th><th>사용자</th><th className="num">세션</th>
              <th className="num">마지막 보고</th><th className="num">수집기</th>
            </tr>
          </thead>
          <tbody>
            {reps.map((r) => {
              const staleMs = r.lastReportedAt ? Date.parse(now) - Date.parse(r.lastReportedAt) : Infinity;
              const stale = !(staleMs < STALE_MS);
              return (
                <tr key={r.machine}>
                  <td className="mono">{r.machine}</td>
                  <td>{r.username || '(미상)'}</td>
                  <td className="mono num">{n(r.sessions)}</td>
                  <td className="mono num" title={fmtTime(r.lastReportedAt)}>
                    {stale
                      ? <Flag title="2일 이상 보고가 없습니다 — 그 PC 의 수집기가 꺼졌거나 네트워크가 막힌 신호입니다.">{relTime(now, r.lastReportedAt)}</Flag>
                      : relTime(now, r.lastReportedAt)}
                  </td>
                  <td className="num">
                    {r.sendsSeries ? <span className="badge ok">신</span> : <span className="help">구</span>}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </TableWrap>
      <p className="help mt-sm">
        2일 이상 보고 없으면 <b>주황</b> — 그 PC 의 수집기가 꺼졌거나 네트워크가 막힌 신호입니다.
        &lsquo;신&rsquo;=시간 버킷까지 보내는 신수집기(품질 지표 기여), &lsquo;구&rsquo;=세션 합계만.
      </p>
    </Card>
  );
}

export default function UsageObsTab() {
  const [detailUser, setDetailUser] = useState<string | null>(null);

  /*
   * 다섯 조회를 한 로더에 묶는다 — 같은 signal 을 공유하므로 탭을 떠나면 전부 끊긴다.
   * 곁가지 넷은 fail-soft 다: 하나가 실패해도 비용 카드와 상위 세션은 그대로 보여야 한다.
   * 반대로 sessions 는 이 화면의 뼈대라 실패하면 화면 전체가 실패로 간다.
   */
  const load = useCallback(async ({ signal }: { signal: AbortSignal }): Promise<ObsData> => {
    const [dist, sessions, leaderboard, quality, coverage] = await Promise.all([
      getDistribution({ signal }),
      getSessions({ sort: 'cost', top: 25 }, { signal }),
      softly(getLeaderboard({ signal }), { users: [], pricedAt: '', sample: { limit: 0, rows: 0, truncated: false }, unpriced: [] } as Leaderboard),
      softly(getQuality({ signal }), null as Quality | null),
      softly(getCoverage({ signal }), { now: '', reporters: [] } as Coverage),
    ]);
    return { dist, sessions, leaderboard, quality, coverage };
  }, []);

  const { state, reload } = useResource(load, []);

  if (state.status === 'loading') return <Loading />;
  if (state.status === 'error') return <ErrorState what="사용 관측을 불러오지 못했습니다." error={state.error} onRetry={reload} />;

  const { dist, sessions, leaderboard, quality, coverage } = state.data;

  if (!sessions.sessions?.length) {
    return (
      <Card title="아직 보고가 없습니다">
        <p className="help">팀원 PC 가 수집기를 갱신한 뒤 세션을 한 번 열면 집계가 올라옵니다.</p>
      </Card>
    );
  }

  return (
    <>
      <p className="lead">
        토큰이 아니라 <b>비용 비중</b>으로 봅니다. 축마다 단가 배수가 달라
        토큰 수가 큰 축과 비용이 큰 축이 다릅니다.
      </p>
      <CostCard s={sessions} />
      <LeaderboardCard d={leaderboard} onDetail={setDetailUser} />
      <QualityCard q={quality} />
      <DistCard d={dist.distributions ?? {}} />
      <SessionsCard s={sessions} />
      <ReporterCoverageCard d={coverage} />

      {detailUser && (
        <UserDetailModal key={detailUser} username={detailUser} onClose={() => setDetailUser(null)} />
      )}
    </>
  );
}
