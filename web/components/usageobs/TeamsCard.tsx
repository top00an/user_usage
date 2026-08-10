'use client';

/*
 * ── 팀/그룹 롤업 ──────────────────────────────────────────────────────────
 *
 * 사용자별 비용을 팀으로 묶어 본다(/api/usage/teams). 팀 미배정은 "미배정" 그룹으로 —
 * 숨기면 팀 합이 전체와 안 맞아 사람이 혼란한다. member 스코프는 403 → softly 로 빈 값 → 숨김.
 */
import type { Teams } from '@/lib/types';
import { n, usd } from '@/lib/format';
import { Card, TableWrap } from '@/components/ui';
import { COST_LABEL_SHORT, COST_WHY } from '@/lib/costLabels';
import PlatformScope from '@/components/platform/PlatformScope';

export default function TeamsCard({ d }: { d: Teams | null }) {
  if (!d || !d.teams?.length) return null;

  // 팀이 하나뿐이고 그게 "미배정"이면 아직 팀을 안 나눈 것 — 카드를 굳이 띄우지 않는다.
  if (d.teams.length === 1 && d.teams[0]?.team === '미배정') return null;

  return (
    <Card title="팀별 롤업" className="mt" aside={<PlatformScope applies={false} />}>
      <p className="help">최근 {d.days}일. 팀 배정은 `usage-server team assign` 으로 관리합니다.</p>
      <TableWrap>
        <table className="mt-sm">
          <thead>
            <tr>
              <th>팀</th>
              <th className="num">멤버</th>
              <th className="num">세션</th>
              <th className="num" title={COST_WHY}>{COST_LABEL_SHORT}</th>
              <th className="num">멤버당</th>
              <th>구성원</th>
            </tr>
          </thead>
          <tbody>
            {d.teams.map((t) => (
              <tr key={t.team}>
                <td>{t.team}</td>
                <td className="mono num">{n(t.members)}</td>
                <td className="mono num">{n(t.sessions)}</td>
                <td className="mono num">{usd(t.usd)}</td>
                <td className="mono num">{usd(t.usdPerMember)}</td>
                <td className="help">{t.usernames.join(', ')}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </TableWrap>
    </Card>
  );
}
