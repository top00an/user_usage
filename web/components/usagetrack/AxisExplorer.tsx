'use client';

import { useState } from 'react';
import type { Dispatch, KeyRow, Summary } from '@/lib/types';
import { n } from '@/lib/format';
import { Empty, TableWrap, Tabs } from '@/components/ui';
import { AXES, type AxisId } from './labels';
import { platformMeta, supportOf } from '@/lib/platforms';
import { usePlatformFilter } from '@/lib/platformFilter';
import { SupportChip } from '@/components/platform/SupportBadge';

/*
 * ── 축 × 플랫폼: 0 과 '모른다'를 가른다 ───────────────────────────────────
 *
 * 아래 막대는 **전체 플랫폼 합계**다(summary 는 platform 축을 받지 않는다). 그 상태에서
 * "Codex 는 서브에이전트를 0회 썼다" 같은 결론이 나오면 그건 데이터가 아니라 착시다 —
 * Codex 는 서브에이전트 위임을 애초에 **기록하지 않는다**. 그래서 축을 고를 때마다 그 축을
 * 어느 플랫폼이 기록하는지 함께 말한다. 목록은 응답이 준 플랫폼만 세운다(하드코딩하지 않는다).
 */
function AxisSupport({ axis, platforms }: { axis: AxisId; platforms: string[] }) {
  const cur = usePlatformFilter();
  if (!platforms.length) return null;

  const blind = platforms.filter((p) => supportOf(p, axis).state !== 'yes');
  const curSupport = cur ? supportOf(cur, axis) : null;

  return (
    <div className="mt-sm">
      <div className="pf-chips">
        <span className="help">이 축을 기록하는 플랫폼:</span>
        {platforms.map((p) => (
          <SupportChip key={p} platform={p} metric={axis} label={platformMeta(p).label} />
        ))}
      </div>
      {curSupport && curSupport.state !== 'yes' && (
        <p className="help mt-sm">
          선택한 플랫폼 <b>{platformMeta(cur).label}</b> 은 이 축을 기록하지 않습니다 —
          아래 숫자에 {platformMeta(cur).label} 몫은 <b>0 이 아니라 애초에 없습니다</b>. {curSupport.why}
        </p>
      )}
      {!curSupport && blind.length > 0 && (
        <p className="help mt-sm">
          아래는 <b>전체 플랫폼 합계</b>입니다. {blind.map((p) => platformMeta(p).label).join(' · ')} 의 몫은
          이 축에 <b>포함돼 있지 않습니다</b>(0 이 아니라 수집 자체가 되지 않습니다).
        </p>
      )}
    </div>
  );
}

/* 가로 막대 — 축별 상위 키를 한눈에. 외부 차트 라이브러리 없이 폭으로 그린다(무의존성). */
function Bars({ rows }: { rows: KeyRow[] }) {
  if (!rows.length) return <Empty>아직 데이터가 없습니다.</Empty>;
  const max = Math.max(...rows.map((r) => Number(r.count) || 0), 1);
  return (
    <div className="ubars">
      {rows.map((r) => {
        const w = Math.max(2, Math.round(((Number(r.count) || 0) / max) * 100));
        return (
          <div className="ubar-row" key={r.key}>
            <div className="ubar-k mono" title={r.key}>{r.key}</div>
            {/* 막대는 장식이다 — 값은 옆 칸의 숫자가 말한다. 그래서 보조기술에서 숨긴다. */}
            <div className="ubar-track" aria-hidden="true"><div className="ubar-fill" style={{ width: `${w}%` }} /></div>
            <div className="ubar-v">{n(r.count)}</div>
            <div className="ubar-s help">{n(r.users)}명 · {n(r.sessions)}세션</div>
          </div>
        );
      })}
    </div>
  );
}

/*
 * 서브에이전트·스킬 활용 — **사람별로** 나눠 본다.
 *
 * 왜(실측): 같은 지침이 모든 세션에 실리는데 실제 사용은 사람마다 갈렸다 —
 * 한 사람은 역할 에이전트를 65회 썼고 다른 사람은 0회에 general-purpose 만 25회였다.
 * 전체 합(위 막대)으로는 이 차이가 보이지 않아 아무도 몰랐다.
 *
 * 강제하지 않는다. 보이면 사람이 판단한다.
 */
function DispatchTable({ axis, data, failed }: { axis: AxisId; data: Dispatch | null; failed: boolean }) {
  if (axis !== 'agent' && axis !== 'skill') return null;
  if (failed) {
    return <p className="help mt">사람별 활용을 불러오지 못했습니다 — 위의 전체 집계는 그대로입니다.</p>;
  }
  if (!data) return <p className="help mt">사람별 활용을 불러오는 중…</p>;

  const rows = axis === 'agent' ? data.agents : data.skills;
  if (!rows?.length) {
    return <p className="help mt">사람별 데이터가 아직 없습니다 — 세션 보고가 도착하면 채워집니다.</p>;
  }

  return (
    <div className="mt" style={{ borderTop: '1px solid var(--border-soft)', paddingTop: 8 }}>
      <div className="row">
        <b style={{ fontSize: 13 }}>사람별 활용</b>
        <span className="help">
          {axis === 'agent'
            ? `역할 에이전트 vs 범용(${(data.generic || []).join(', ')}) — 역할 0 은 팬아웃이 일어나지 않은 것입니다`
            : '스킬을 명시적으로 지시했는가'}
        </span>
      </div>
      <div className="table-wrap mt">
        <table className="tbl">
          <thead>
            {axis === 'agent' ? (
              <tr><th>사용자</th><th className="num">역할</th><th className="num">범용</th><th className="num">합</th><th>상위</th></tr>
            ) : (
              <tr><th>사용자</th><th className="num">합</th><th>상위</th></tr>
            )}
          </thead>
          <tbody>
            {rows.map((u) => {
              const top = u.items.slice(0, 4).map((i) => `${i.key} ${i.count}`).join(' · ');
              if (axis !== 'agent') {
                return (
                  <tr key={u.username}>
                    <td>{u.username}</td>
                    <td className="num">{n(u.total)}</td>
                    <td className="help">{top}</td>
                  </tr>
                );
              }
              const a = u as Dispatch['agents'][number];
              return (
                <tr key={a.username}>
                  <td>{a.username}</td>
                  {/* 역할 0 은 지침이 닿지 않은 자리다 — 눈에 띄게 표시한다. */}
                  <td className={`num${a.role === 0 ? ' txt-err' : ''}`}>{n(a.role)}</td>
                  <td className="num">{n(a.generic)}</td>
                  <td className="num">{n(a.total)}</td>
                  <td className="help wrap-any" style={{ minWidth: 0 }}>{top}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}

export default function AxisExplorer({
  top, dispatch, dispatchFailed, platforms = [],
}: {
  top: Summary['top'];
  dispatch: Dispatch | null;
  dispatchFailed: boolean;
  /** /api/usage/platforms 가 돌려준 플랫폼 id 들. 비면 지원 안내를 그리지 않는다. */
  platforms?: string[];
}) {
  const [axis, setAxis] = useState<AxisId>('bash');
  const def = AXES.find((a) => a.id === axis) ?? AXES[0];

  return (
    <section className="card glass">
      <div className="between mb">
        <h3>사용 현황</h3>
        <span className="help">{def.hint}</span>
      </div>
      <Tabs tabs={AXES} active={axis} onSelect={setAxis} label="사용 현황 축" idPrefix="axis" />
      <div id="axis-panel" role="tabpanel" aria-labelledby={`axis-${axis}`} className="mt">
        <AxisSupport axis={axis} platforms={platforms} />
        <TableWrap>
          <Bars rows={top?.[axis] ?? []} />
        </TableWrap>
        <DispatchTable axis={axis} data={dispatch} failed={dispatchFailed} />
      </div>
    </section>
  );
}
