'use client';

import type { SessionsResponse } from '@/lib/types';
import { n, ratio, usd } from '@/lib/format';
import { Flag, TableWrap } from '@/components/ui';
import { COST_LABEL, COST_LABEL_SHORT, COST_WHY } from '@/lib/costLabels';
import PlatformScope from '@/components/platform/PlatformScope';

/*
 * 비용 카드 — 이 화면의 존재 이유.
 *
 * 만든 이유는 구체적이다: 같은 사용량을 두고 도구마다 176배 차이 나는 숫자가 나왔다. 원인은
 * 화면이 가장 크게 띄운 값이 **출력 토큰 하나**였다는 것이다 — 실제로는 캐시읽기가 비용의
 * 64.5%, 출력은 11% 였다. 그래서 이 화면의 첫 카드는 합계가 아니라 **비용의 축별 분해**다.
 *
 * 축을 **비중 내림차순**으로 세운다. 토큰 수 순서로 세우면 화면이 다시 "제일 큰 축이 제일 안
 * 비싼 축"을 보여주게 된다(그게 애초의 사고였다).
 */

const AXES = [
  { k: 'cacheRead', label: '캐시 읽기', hint: '입력가의 0.1배' },
  { k: 'cacheCreate', label: '캐시 생성', hint: '' },
  { k: 'output', label: '출력', hint: '' },
  { k: 'input', label: '입력', hint: '' },
] as const;

export default function CostCard({ s }: { s: SessionsResponse }) {
  const ttlUnknown = Number(s.ttlUnknownRows) || 0;
  const unpriced = s.unpriced ?? [];

  const rows = AXES
    .map((a) => ({
      ...a,
      // TTL 을 아는 행만 있으면 "가정"이라 말하지 않는다 — 아는 것을 모른다고 말하는 것도 거짓이다.
      hint: a.k === 'cacheCreate'
        ? (ttlUnknown ? '5분 1.25배 · 1시간 2배(일부 TTL 미상)' : '5분 1.25배 · 1시간 2배')
        : a.hint,
      v: Number(s.byAxis?.[a.k]) || 0,
    }))
    .sort((a, b) => b.v - a.v);

  return (
    <section className="card glass">
      <div className="between">
        <h3>{COST_LABEL}</h3>
        <span className="row">
          <PlatformScope applies />
          <span className="help">단가 기준일 {s.pricedAt || '-'}</span>
        </span>
      </div>
      <div className="v" style={{ fontSize: '1.6rem' }} title={COST_WHY}>{usd(s.usd)}</div>
      <p className="help mt-sm">
        <b>실제 청구액이 아닙니다.</b> 구독 요금제(Claude Max·Team, ChatGPT Plus·Pro)는 토큰당 과금이
        아니라 <b>정액</b>이고, 이 숫자는 같은 사용량을 <b>공개 API 단가로 종량 과금했다면</b> 나왔을
        금액입니다 — 즉 구독이 얼마나 절약하고 있는지를 보여줍니다. 사용자·모델·플랫폼 간
        <b> 상대 비교</b>와 어느 축이 비싼지를 보는 용도입니다.
      </p>

      <TableWrap>
        <table className="mt-sm">
          <caption className="help" style={{ captionSide: 'top', textAlign: 'left', paddingBottom: 4 }}>
            {COST_LABEL}의 축별 분해 (비중 내림차순)
          </caption>
          <thead>
            <tr>
              <th>축</th><th aria-label="비중 막대" />
              <th className="num" title={COST_WHY}>{COST_LABEL_SHORT}</th>
              <th className="num">비중</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((a) => (
              <tr key={a.k}>
                <td>{a.label}{a.hint && <> <span className="help">{a.hint}</span></>}</td>
                <td style={{ width: '45%' }} aria-hidden="true">
                  <span className="inbar" style={{ width: `${ratio(a.v, s.usd).toFixed(1)}%` }} />
                </td>
                <td className="mono num">{usd(a.v)}</td>
                <td className="mono num">{ratio(a.v, s.usd).toFixed(1)}%</td>
              </tr>
            ))}
          </tbody>
        </table>
      </TableWrap>

      {/*
        ── 근사값을 정확한 값으로 위장하지 않는다 ──────────────────────────
        아래 둘은 지우면 화면이 훨씬 깔끔해진다. 그게 이 도구가 하지 않기로 한 일이다.
        지우면 "합계가 틀렸을 수 있다"는 사실이 화면에서 사라지고, 사람은 이 숫자를 정확한
        값으로 읽는다.
      */}
      <div className="mt" style={{ borderTop: '1px solid var(--border-soft)', paddingTop: 8 }}>
        <div className="row">
          <b style={{ fontSize: 13 }}>이 숫자가 정확하지 않은 자리</b>
        </div>

        <p className="help mt-sm">
          {unpriced.length ? (
            <>
              <Flag title={`단가표에 없어 이 모델의 사용량은 ${COST_LABEL_SHORT} 0 으로 잡혔습니다. 즉 위 합계는 그만큼 적습니다.`}>
                단가 미등록 모델 {n(unpriced.length)}종
              </Flag>{' '}
              <span className="mono wrap-any">{unpriced.join(', ')}</span>
              {' '}— 단가표에 없어 <b>{COST_LABEL_SHORT} 0</b> 으로 잡혔습니다. 조용히 $0 으로 처리하지 않고 이름을 남깁니다.
            </>
          ) : (
            <>단가 미등록 모델 없음 — 등장한 모든 모델이 단가표에 있습니다.</>
          )}
        </p>

        <p className="help mt-sm">
          {ttlUnknown ? (
            <>
              <Flag title="캐시 TTL(5분 1.25배 / 1시간 2배)을 모르는 행입니다. 낮은 배수로 잡으므로 실제보다 최대 1.6배 적게 나옵니다.">
                TTL 미상 {n(ttlUnknown)}행
              </Flag>{' '}
              — 캐시 생성의 TTL 을 몰라 낮은 배수로 계산했습니다. 그만큼 <b>최대 1.6배 과소</b> 추정입니다.
            </>
          ) : (
            <>캐시 TTL 미상 행 없음 — 캐시 생성 단가가 전부 실제 TTL 로 계산됐습니다.</>
          )}
        </p>

        {s.sample?.truncated && (
          <p className="help mt-sm">
            <Flag>표본 잘림</Flag> 최근 {n(s.sample.rows)}건까지만 집계했습니다 — 더 넓게 보려면 기간을 좁히세요.
          </p>
        )}
      </div>
    </section>
  );
}
