'use client';

/*
 * ── 좌석당 비용·ROI + 기간 비교 ───────────────────────────────────────────
 *
 * 미국 사용량 제품(Cursor Admin·Copilot metrics 류)이 관리자에게 처음 보여주는 화면: 1인당 비용,
 * 활성 좌석, 그리고 **전 기간 대비 증감(±%)**. /api/usage/seats 를 그대로 쓴다.
 *
 * member(개인 열람) 스코프는 이 엔드포인트에서 403 → 상위 로더가 softly 로 빈 값을 준다.
 * 그때는 카드를 통째로 숨긴다(자기 화면에 전사 좌석 비교가 뜨면 안 된다).
 */
import type { Seats } from '@/lib/types';
import { n, pctOf, usd } from '@/lib/format';
import { Card, TableWrap } from '@/components/ui';
import { COST_LABEL, COST_LABEL_SHORT, COST_WHY } from '@/lib/costLabels';
import PlatformScope from '@/components/platform/PlatformScope';

function Delta({ pct, isNew }: { pct: number | null; isNew: boolean }) {
  if (isNew) return <span className="help">신규</span>;
  if (pct == null) return <span className="muted">—</span>;
  const up = pct >= 0;
  /*
   * 비용 증가는 주의(빨강), 감소는 안심(초록). 색이 없어도 부호로 읽히게 화살표를 둔다.
   *
   * ⚠ 여기는 `var(--danger, #c0392b)` 였다. **`--danger` 는 이 저장소에 없는 토큰이다**
   * (오류색 이름은 `--err`). 그래서 폴백 `#c0392b` 가 **항상** 적용됐고, 다크 배경에서 대비가
   * 3.22:1 — 제대로 된 `--err`(#f87171)의 6.33:1 절반이다. 비용이 **늘었다**고 경고하는 빨강이
   * 이 화면에서 가장 안 읽히는 글자였다.
   *
   * 폴백은 일부러 지운다. 폴백을 남기면 다음 오타도 똑같이 "그럭저럭 보이는 색"으로 조용히
   * 살아남아 아무도 눈치채지 못한다 — 토큰 이름이 틀리면 색이 사라져 바로 드러나는 편이 낫다.
   * (`--danger` 를 새로 정의하지 않는다: `--err` 와 두 벌이 되어 다음에 둘 중 하나만 고쳐진다.)
   */
  return (
    <span style={{ color: up ? 'var(--err)' : 'var(--ok)' }}>
      {up ? '▲' : '▼'} {pctOf(Math.abs(pct) / 100)}
    </span>
  );
}

export default function SeatsCard({ d }: { d: Seats | null }) {
  if (!d || !d.seats?.length) return null; // member 스코프·무데이터 → 숨김

  const s = d.summary;
  return (
    <Card
      title={`좌석당 ${COST_LABEL} · 기간 비교`}
      className="mt"
      /* 이 조회는 서버가 platform 축으로 거르지 못한다 — 필터가 걸려 있으면 그렇다고 말한다. */
      aside={<PlatformScope applies={false} />}
    >
      <p className="help" title={COST_WHY}>
        최근 {d.days}일({d.from}~{d.to}) 대 직전 {d.days}일({d.prevFrom}~{d.prevTo}). 증감은 {COST_LABEL} 기준.
      </p>
      <div className="row" style={{ display: 'flex', gap: '1.5rem', flexWrap: 'wrap', margin: '0.5rem 0' }}>
        <div><b>{n(s.activeSeats)}</b> 활성 좌석 <span className="help">(직전 {n(s.prevActiveSeats)})</span></div>
        <div><b>{usd(s.totalUsd)}</b> 총 {COST_LABEL_SHORT} <Delta pct={s.totalUsdDeltaPct} isNew={false} /></div>
        <div><b>{usd(s.usdPerSeat)}</b> 좌석당 평균</div>
      </div>
      <TableWrap>
        <table className="mt-sm">
          <thead>
            <tr>
              <th>사용자</th>
              <th className="num">세션</th>
              <th className="num" title={COST_WHY}>{COST_LABEL_SHORT}</th>
              <th className="num">세션당</th>
              <th className="num">캐시적중</th>
              <th className="num">직전 대비</th>
            </tr>
          </thead>
          <tbody>
            {d.seats.map((seat) => (
              <tr key={seat.username}>
                <td>{seat.username}</td>
                <td className="mono num">{n(seat.sessions)}</td>
                <td className="mono num">{usd(seat.usd)}</td>
                <td className="mono num">{usd(seat.usdPerSession)}</td>
                <td className="mono num">{pctOf(seat.cacheHitRate)}</td>
                <td className="mono num"><Delta pct={seat.usdDeltaPct} isNew={seat.isNew} /></td>
              </tr>
            ))}
          </tbody>
        </table>
      </TableWrap>
    </Card>
  );
}
