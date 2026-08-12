'use client';

/*
 * ── 플랫폼 요약 ───────────────────────────────────────────────────────────
 *
 * 운영자가 답하고 싶은 질문은 둘이다: "어느 도구를 얼마나 쓰나", "그게 얼마짜리인가".
 * 그래서 이 섹션은 두 덩어리로 되어 있다.
 *
 *   ① 카드     플랫폼마다 한 장. 세션·토큰 네 축·환산 비용·최근 수집.
 *   ② 공통 코어 비교 표
 *              **세 플랫폼이 같은 방식으로 기록하는 축만** 나란히 세운다. 한쪽만 기록하는
 *              축을 비교에 넣으면 "안 쓴 것"과 "못 재는 것"이 같은 길이의 막대가 되고,
 *              그 표를 근거로 "Codex 는 서브에이전트를 안 쓴다" 같은 없는 결론이 나온다.
 *              (합계 토큰 열을 두지 않는 이유는 usagetrack/labels.ts 와 같다 — 성질이 다른
 *              축을 한 이름으로 더하면 산술적으로 맞고 의미로는 거짓말이다.)
 *
 * ⚠ 전체 지원표(지표 × 플랫폼)는 **여기 없다.** 카드와 비교표의 배지는 인라인으로 사유를
 *   달고 있고(MetricValue → SupportBadge), 그 배지가 왜 붙었는지의 사실표는 **아키텍처 탭**이
 *   소유한다(components/Architecture.tsx 의 수집 범위 표). 판정 자체는 두 화면 모두
 *   lib/platforms.ts 의 supportOf() 하나를 부른다 — 표를 옮겨도 판정이 갈리지 않는 이유다.
 *
 * 기간: /api/usage/platforms 는 전체 기간 누적이다(lib/api.ts 의 getPlatforms 주석 참고).
 * 화면도 그렇게 말하고, 실제 구간은 firstSeen~lastSeen 으로 밝힌다.
 */

import type { PlatformRow } from '@/lib/types';
import { fmtTime, n, shortTokens, usd } from '@/lib/format';
import { COMMON_CORE, METRIC_LABEL, platformMeta, type MetricId } from '@/lib/platforms';
import { COST_DISCLAIMER, COST_LABEL, COST_LABEL_SHORT, COST_WHY } from '@/lib/costLabels';
import { usePlatformFilter } from '@/lib/platformFilter';
import { TableWrap } from '@/components/ui';
import { MetricValue } from './SupportBadge';

/** 카드·표가 쓰는 값 한 칸. 지원되지 않는 축은 숫자 대신 배지가 된다. */
function Cell({ row, metric }: { row: PlatformRow; metric: MetricId }) {
  const raw = value(row, metric);
  return (
    <MetricValue platform={row.platform} metric={metric} zero={raw === 0}>
      {format(metric, raw)}
    </MetricValue>
  );
}

function value(row: PlatformRow, metric: MetricId): number {
  switch (metric) {
    case 'sessions': return Number(row.sessions) || 0;
    case 'input': return Number(row.input) || 0;
    case 'output': return Number(row.output) || 0;
    case 'cacheRead': return Number(row.cacheRead) || 0;
    case 'cacheCreate': return Number(row.cacheCreate) || 0;
    case 'cost': return Number(row.costUsd) || 0;
    // 응답에 없는 축(도구·LOC 등)은 이 표의 값이 아니다 — 지원표만 보여준다.
    default: return 0;
  }
}

function format(metric: MetricId, v: number): string {
  if (metric === 'cost') return usd(v);
  if (metric === 'sessions') return n(v);
  return shortTokens(v);
}

/** 응답이 값으로 답하는 축. 나머지는 지원 여부만 말한다. */
const VALUE_METRICS: readonly MetricId[] = ['sessions', 'input', 'output', 'cacheRead', 'cacheCreate', 'cost'];

function PlatformCard({ row, selected }: { row: PlatformRow; selected: boolean }) {
  const meta = platformMeta(row.platform);
  return (
    <div
      className="tile glass pf-card"
      style={{ borderLeftColor: meta.color }}
      aria-current={selected ? 'true' : undefined}
    >
      <div className="between">
        <span className="pf-name">
          <span className="pf-dot" style={{ background: meta.color }} aria-hidden="true" />
          {meta.label}
        </span>
        {meta.note && <span className="badge mute">{meta.note}</span>}
      </div>

      <div className="v">
        {n(row.sessions)} <span className="help">세션</span>
      </div>

      <dl className="pf-kv">
        {VALUE_METRICS.filter((m) => m !== 'sessions').map((m) => (
          <div className="pf-kv-row" key={m}>
            <dt>{METRIC_LABEL[m]}</dt>
            <dd><Cell row={row} metric={m} /></dd>
          </div>
        ))}
        <div className="pf-kv-row">
          <dt>최근 수집</dt>
          <dd title={row.firstSeen ? `첫 수집 ${fmtTime(row.firstSeen)}` : undefined}>
            {fmtTime(row.lastSeen)}
          </dd>
        </div>
      </dl>
    </div>
  );
}

export default function PlatformSummary({ rows }: { rows: PlatformRow[] | null }) {
  const cur = usePlatformFilter();

  if (rows === null) {
    return (
      <section className="card glass">
        <h3>플랫폼별 사용량</h3>
        <p className="help mt-sm">
          플랫폼 요약을 불러오지 못했습니다 — 이 목록은 관리자만 열람합니다.
          아래 패널들은 그대로입니다.
        </p>
      </section>
    );
  }
  if (!rows.length) {
    return (
      <section className="card glass">
        <h3>플랫폼별 사용량</h3>
        <p className="help mt-sm">아직 어떤 플랫폼의 보고도 도착하지 않았습니다.</p>
      </section>
    );
  }

  return (
    <section className="card glass" aria-labelledby="pf-sum-h">
      <div className="between mb">
        <h3 id="pf-sum-h">플랫폼별 사용량</h3>
        <span className="help">전체 기간 누적 · {COST_LABEL}은 {COST_DISCLAIMER}</span>
      </div>

      {/* ① 카드 */}
      <div className="pf-cards">
        {rows.map((r) => <PlatformCard key={r.platform} row={r} selected={r.platform === cur} />)}
      </div>

      {/* ② 공통 코어 비교 */}
      <TableWrap>
        <table className="mt">
          <thead>
            <tr>
              <th>플랫폼</th>
              {COMMON_CORE.map((m) => (
                <th key={m} className="num" title={m === 'cost' ? COST_WHY : undefined}>
                  {m === 'cost' ? COST_LABEL_SHORT : METRIC_LABEL[m]}
                </th>
              ))}
              <th className="num">최근 수집</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.platform} aria-current={r.platform === cur ? 'true' : undefined}>
                <td>
                  <span className="pf-dot" style={{ background: platformMeta(r.platform).color }} aria-hidden="true" />
                  {platformMeta(r.platform).label}
                </td>
                {COMMON_CORE.map((m) => (
                  <td className="mono num" key={m}><Cell row={r} metric={m} /></td>
                ))}
                <td className="mono num">{fmtTime(r.lastSeen)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </TableWrap>
    </section>
  );
}
