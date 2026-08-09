'use client';

import type { ModelAxis, ModelRow } from '@/lib/types';
import { n } from '@/lib/format';
import { Empty, Flag, TableWrap, TokenCount } from '@/components/ui';
import { AX_CACHE_CREATE, AX_CACHE_READ, IN_HINT, IN_LABEL, MODEL_SESSIONS_HINT } from './labels';

/*
 * ── 모델별 표: 값의 **근거**를 같이 낸다 ──────────────────────────────
 *
 * 서버는 두 축을 더해 이 표를 만든다. series(시간×모델 버킷)가 있는 세션은 모델별 정확값,
 * 없는 세션은 **세션 최빈 모델 1개**에 통째로 귀속된 근사값이다. 둘을 구분 없이 보여주면
 * 근사를 정확한 값으로 위장하게 된다 — 지난 결함(모델 간 상호 오귀속)의 본질이 그것이었다.
 *
 * 행마다 fromSeries/fromSession 이 실린다. 없으면(구 서버) 그 열을 아예 그리지 않고 종전 표
 * 그대로 나간다 — 화면이 새 필드를 요구하지 않는다.
 */

interface Share { pct: number; num: number; den: number }

export function fallbackShare(r: ModelRow): Share | null {
  const fs = r.fromSession;
  if (!fs) return null;
  // 출력 기준. 출력이 0 인 행(토큰 0 세션 등)은 네 축 합으로 대신한다 — NaN 을 만들지 않는다.
  const num = r.output ? fs.output : fs.input + fs.output + fs.cacheRead + fs.cacheCreate;
  const den = r.output ? r.output : r.input + r.output + r.cacheRead + r.cacheCreate;
  if (!den) return { pct: 0, num: 0, den: 0 };
  return { pct: Math.round((num / den) * 1000) / 10, num, den };
}

function BasisCell({ share }: { share: Share | null }) {
  if (!share || !share.den) return <td className="help">—</td>;
  if (share.pct <= 0.05) return <td className="help">series — 모델별 정확</td>;
  const title = `세션 최빈 모델 기준 ${n(share.num)} / ${n(share.den)}`;
  return (
    <td className="help">
      {share.pct >= 50
        ? <Flag title={title}>세션 최빈 기준 {share.pct}%</Flag>
        : <span title={title}>세션 최빈 기준 {share.pct}%</span>}
    </td>
  );
}

export default function ModelTable({ rows, axis }: { rows: ModelRow[]; axis?: ModelAxis }) {
  if (!rows.length) return <Empty>아직 데이터가 없습니다.</Empty>;
  const hasBasis = rows.some((r) => r.fromSession);

  return (
    <>
      <TableWrap>
        <table>
          <thead>
            <tr>
              <th>모델</th>
              <th className="num">출력</th>
              <th className="num" title={IN_HINT}>{IN_LABEL}</th>
              <th className="num" title={AX_CACHE_READ}>캐시읽기</th>
              <th className="num" title={AX_CACHE_CREATE}>캐시생성</th>
              <th className="num" title={MODEL_SESSIONS_HINT}>세션</th>
              {hasBasis && (
                <th title="이 행의 값 중 세션 최빈 모델에 귀속된 몫(출력 기준). series 가 없는 세션과, series 가 덮지 못한 잔여입니다">
                  근거
                </th>
              )}
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.model}>
                <td className="mono">{r.model}</td>
                <td className="num"><TokenCount v={r.output} /></td>
                <td className="num"><TokenCount v={r.input} /></td>
                <td className="num"><TokenCount v={r.cacheRead} /></td>
                <td className="num"><TokenCount v={r.cacheCreate} /></td>
                <td className="num">{n(r.sessions)}</td>
                {hasBasis && <BasisCell share={fallbackShare(r)} />}
              </tr>
            ))}
          </tbody>
        </table>
      </TableWrap>
      <CoverageByUser axis={axis} rows={rows} />
    </>
  );
}

/*
 * 근거 커버리지 — 사람별로 낸다.
 *
 * 왜 사람별인가(실측 2026-08-05): 커버리지가 사람마다 갈린다. 어떤 사람은 91%, 어떤 사람은
 * 2.2% 다. 2.2% 인 사람의 모델별 값은 사실상 전부 세션 최빈 모델 기준인데, 전체 비율 하나로는
 * 그 사람 행을 볼 때 알 수가 없다.
 *
 * **원인은 말하지 않는다.** 왜 낮은지는 그 PC 쪽 사실이고 서버는 모른다 —
 * 화면이 추측하면 사람이 엉뚱한 데를 판다.
 */
function CoverageByUser({ axis, rows }: { axis?: ModelAxis; rows: ModelRow[] }) {
  if (!axis || !Array.isArray(axis.users) || !axis.users.length) return null;

  const tot = rows.reduce(
    (a, r) => (r.fromSession
      ? { num: a.num + r.fromSession.output, den: a.den + (Number(r.output) || 0) }
      : a),
    { num: 0, den: 0 },
  );
  const totPct = tot.den ? Math.round((tot.num / tot.den) * 1000) / 10 : null;

  return (
    <div className="mt" style={{ borderTop: '1px solid var(--border-soft)', paddingTop: 8 }}>
      <p className="help">
        모델별 값은 두 근거를 더한 것입니다 — <b>series</b>(시간×모델 버킷)가 있는 세션은 모델별
        정확값, 없는 세션은 <b>그 세션에서 가장 많이 쓴 모델 하나</b>에 통째로 귀속된 값입니다.
        {totPct != null && <> 지금 이 표의 출력 <b>{totPct}%</b> 가 후자입니다.</>}
        {' '}후자는 세션에 모델이 섞였다면 틀린 자리에 들어가 있습니다.
      </p>
      {!!axis.overSessions && (
        <p className="help txt-warn mt-sm">
          series 합이 세션 행보다 큰 세션 {n(axis.overSessions)}건 — 그 초과분은 잔여 계산에서 0 으로 끊었습니다.
        </p>
      )}
      <div className="table-wrap mt">
        <table className="tbl">
          <caption className="help" style={{ captionSide: 'top', textAlign: 'left', paddingBottom: 4 }}>
            사용자별 series 커버리지
          </caption>
          <thead>
            <tr>
              <th>사용자</th>
              <th className="num">세션</th>
              <th className="num" title="usage_series(시간×모델 버킷)가 하나라도 있는 세션">series 있음</th>
              <th className="num">커버리지</th>
              <th>모델별 값의 근거</th>
              <th title="커버리지가 낮은 이유. 서버가 아는 것은 '시각 없는 턴이 N개였다' 까지입니다 — 왜 없는지는 그 PC 의 기록을 봐야 압니다">
                왜 낮은가
              </th>
            </tr>
          </thead>
          <tbody>
            {axis.users.map((u) => {
              const pct = u.sessions ? Math.round((u.withSeries / u.sessions) * 1000) / 10 : 0;
              const basis = pct >= 99.95
                ? 'series — 모델별 정확'
                : pct <= 0.05 ? '전부 세션 최빈 모델 기준' : '섞여 있음';
              return (
                <tr key={u.username}>
                  <td className="mono">{u.username}</td>
                  <td className="num">{n(u.sessions)}</td>
                  <td className="num">{n(u.withSeries)}</td>
                  <td className={`num${pct < 50 ? ' txt-warn' : ''}`}>{pct}%</td>
                  <td className="help">{basis}</td>
                  <td><WhyLow user={u} pct={pct} /></td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}

/*
 * 커버리지가 낮은 **이유**를 한 칸으로 말한다.
 *
 * 커버리지만 보면 사람은 "보고가 안 온다" 로 읽는다 — 실제로는 보고가 오고 있고 턴에 **시각이
 * 없어** series 버킷을 만들지 못한 것이다. 그 구분이 없으면 엉뚱한 데(토큰·서버 주소·수집기
 * 설치)를 판다.
 *
 * ⚠ 원인을 여기서 단정하지 않는다. 서버가 아는 것은 "시각 없는 턴이 N개였다" 까지다.
 * ⚠ noTsUnknown(구버전 수집기라 이 값을 안 보낸 세션)은 0 과 갈라 말한다 — 뭉치면 구버전 PC 가
 *   "시각 문제 없음" 으로 단정된다.
 */
function WhyLow({ user, pct }: { user: ModelAxis['users'][number]; pct: number }) {
  const nts = Number(user.noTsTurns) || 0;
  const unknown = Number(user.noTsUnknown) || 0;
  /*
   * ⚠ 현행과 한 가지 다르다. 현행은 커버리지가 100% 면 이 칸을 통째로 비웠다.
   *   그런데 커버리지는 **세션 단위**이고 noTsTurns 는 **턴 단위**다 — 세션은 전부 series 를
   *   갖고 있는데 그 안의 턴 일부에 시각이 없는 경우가 실제로 있다(계약 시드의 carol: 세션
   *   2/2 = 100%, 시각 없는 턴 4). 그 잔여는 세션 최빈 모델로 귀속되므로 모델별 값에 근사가
   *   섞이는데, 현행 화면에서는 그 이유가 **보이지 않는다.** 그래서 nts > 0 이면 항상 말한다.
   */
  if (nts > 0) {
    return (
      <span
        className="help"
        title="이 턴들은 시각이 없어 시간×모델 버킷에 올릴 자리가 없었습니다. 세션 합계에는 그대로 남아 있습니다 — 총량은 맞고 모델별 분해만 못 합니다. 왜 시각이 없는지는 그 PC 의 기록을 봐야 압니다."
      >
        시각 없는 턴 {n(nts)}
      </span>
    );
  }
  if (pct >= 99.95) return null;
  if (unknown >= user.sessions) {
    return (
      <span
        className="help"
        title="이 사용자의 세션은 전부 구버전 수집기가 보낸 것이라 시각 유무를 알 수 없습니다. 수집기가 갱신되면 채워집니다."
      >
        알 수 없음(구버전 수집기)
      </span>
    );
  }
  return null;
}
