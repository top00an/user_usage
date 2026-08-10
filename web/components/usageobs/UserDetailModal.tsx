'use client';

import { useCallback } from 'react';
import { getSeries } from '@/lib/api';
import { useResource } from '@/hooks/useResource';
import type { SeriesLine, SeriesResponse } from '@/lib/types';
import { dayShift, n, shortTokens } from '@/lib/format';
import Modal from '@/components/Modal';
import { ErrorState, Loading, TableWrap } from '@/components/ui';
import { usePlatformFilter } from '@/lib/platformFilter';
import PlatformScope from '@/components/platform/PlatformScope';

/*
 * ── 사용자별 상세 ─────────────────────────────────────────────────────────
 *
 * "이 사람이 무엇을 얼마나 쓰는가" 를 **모델별 × 시간축**으로 편다. 리더보드는 기간 전체 합계라
 * "요즘 늘었나 줄었나", "어느 모델로 옮겨갔나" 를 답하지 못한다.
 *
 * 두 축을 함께 보여주는 이유: 일별 7일은 최근 추세를, 주간은 그보다 긴 흐름을 본다. 한쪽만
 * 두면 "이번 주가 유난한가" 를 판단할 기준이 없다.
 *
 * 데이터는 기존 /api/usage/series 를 그대로 쓴다(group_by=model · user 필터). 새 엔드포인트를
 * 만들지 않는다 — 같은 질문에 두 개의 집계 경로가 생기면 값이 갈릴 자리가 된다.
 */

const DETAIL_DAYS = 7;
const DETAIL_WEEKS = 8;

/*
 * 시계열 → 모델(행) × 시각(열) 표. 값이 0 인 칸은 비워 둔다 — 0 을 채우면 눈이 그리로 간다.
 *
 * 이 표는 **토큰 소모량**을 보여준다(금액이 아니다). 팀이 구독제라 금액은 실제 결제액이 아니고,
 * 알고 싶은 것은 "누가 얼마나 태우는가" 이기 때문이다.
 */
function Pivot({ series, label }: { series: SeriesLine[] | undefined; label: string }) {
  const lines = series ?? [];
  const cols = [...new Set(lines.flatMap((s) => (s.points ?? []).map((p) => p.t)))]
    .sort((a, b) => a.localeCompare(b));
  if (!cols.length) return <p className="help">{label} 구간에 데이터가 없습니다.</p>;

  const at = (s: SeriesLine, t: string) => (s.points ?? []).find((p) => p.t === t)?.v ?? 0;
  const totalOf = (t: string) => lines.reduce((sum, s) => sum + at(s, t), 0);
  const grand = lines.reduce((a, s) => a + (Number(s.total) || 0), 0);

  return (
    <TableWrap>
      <table>
        <caption className="help" style={{ captionSide: 'top', textAlign: 'left', paddingBottom: 4 }}>
          {label} · 모델별 토큰 소모량
        </caption>
        <thead>
          <tr>
            <th scope="col">모델</th>
            {cols.map((t) => <th scope="col" className="num" key={t}>{t.slice(5)}</th>)}
            <th scope="col" className="num">합계</th>
          </tr>
        </thead>
        <tbody>
          {lines.map((s) => (
            <tr key={s.label}>
              <td className="mono">{s.label || '-'}</td>
              {cols.map((t) => {
                const v = at(s, t);
                // title 에 원본값 — 축약은 자릿수를 접으므로 정확한 값이 필요한 사람이 볼 곳이 있어야 한다.
                return (
                  <td className="mono num" key={t} title={v ? n(v) : undefined}>
                    {v ? shortTokens(v) : <span className="help">·</span>}
                  </td>
                );
              })}
              <td className="mono num"><b title={n(s.total)}>{shortTokens(s.total)}</b></td>
            </tr>
          ))}
        </tbody>
        <tfoot>
          <tr>
            <td><b>합계</b></td>
            {cols.map((t) => (
              <td className="mono num" key={t}><b title={n(totalOf(t))}>{shortTokens(totalOf(t))}</b></td>
            ))}
            <td className="mono num"><b title={n(grand)}>{shortTokens(grand)}</b></td>
          </tr>
        </tfoot>
      </table>
    </TableWrap>
  );
}

export default function UserDetailModal({ username, onClose }: { username: string; onClose: () => void }) {
  // series 는 platform 축을 받는다 — 관측 탭에서 플랫폼을 골랐다면 이 상세도 같은 모집단이어야 한다.
  const platform = usePlatformFilter();

  const load = useCallback(async ({ signal }: { signal: AbortSignal }) => {
    const p = platform || undefined;
    // 두 요청은 서로 독립이다 — 순차로 기다릴 이유가 없다.
    const [daily, weekly] = await Promise.all([
      getSeries({ metric: 'tokens', interval: 'day', groupBy: 'model', user: username, from: dayShift(DETAIL_DAYS - 1), platform: p }, { signal }),
      getSeries({ metric: 'tokens', interval: 'week', groupBy: 'model', user: username, from: dayShift(DETAIL_WEEKS * 7), platform: p }, { signal }),
    ]);
    return { daily, weekly } as { daily: SeriesResponse; weekly: SeriesResponse };
  }, [username, platform]);

  const { state, reload } = useResource(load, [username, platform]);

  return (
    <Modal title={`${username} — 사용 상세`} onClose={onClose} maxWidth={880}>
      {state.status === 'loading' && <Loading />}
      {state.status === 'error' && <ErrorState what="사용자 상세를 불러오지 못했습니다." error={state.error} onRetry={reload} />}
      {state.status === 'ready' && (
        <>
          <p className="help">
            <PlatformScope applies />{' '}
            모델별 <b>토큰 소모량</b>입니다(입력·출력·캐시읽기·캐시생성 합계).
            숫자에 마우스를 올리면 정확한 값이 나옵니다.
            시각 기준 <span className="mono">{state.data.daily.timezone || state.data.weekly.timezone || '-'}</span>.
          </p>
          <h4 className="mt">최근 {DETAIL_DAYS}일 <span className="help">일별</span></h4>
          <Pivot series={state.data.daily.series} label={`최근 ${DETAIL_DAYS}일`} />
          <h4 className="mt">최근 {DETAIL_WEEKS}주 <span className="help">주간 · 월요일 시작</span></h4>
          <Pivot series={state.data.weekly.series} label={`최근 ${DETAIL_WEEKS}주`} />
        </>
      )}
    </Modal>
  );
}
