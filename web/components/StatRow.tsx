'use client';

/*
 * ── Live Status — 최상단 상태 타일 행 ─────────────────────────────────────
 *
 * Grafana 관제 대시보드의 첫 줄처럼, "지금 이 팀의 규모"를 한눈에: 세션·비용·토큰·사용자 +
 * 캐시 적중률 게이지. 두 탭 위에 항상 떠 있다.
 *
 * summary(세션·토큰·캐시)와 seats(비용)를 스스로 부른다 — 둘 다 관리자 전용이라 member(개인)
 * 스코프는 403 → softly 로 빈 값 → 행 전체를 숨긴다(개인 화면에 전사 규모가 뜨지 않는다).
 */
import { useCallback } from 'react';
import { getSummary, getSeats } from '@/lib/api';
import { softly, useResource } from '@/hooks/useResource';
import type { Seats, Summary } from '@/lib/types';
import { shortTokens, n, usd, pctOf } from '@/lib/format';

interface Live {
  summary: Summary | null;
  seats: Seats | null;
}

/* 반원 게이지 — 0~1 비율. 색만이 아니라 숫자도 함께 둔다. */
function Gauge({ value, label }: { value: number; label: string }) {
  const v = Math.max(0, Math.min(1, value));
  const r = 46;
  const circ = Math.PI * r; // 반원 둘레
  const dash = circ * v;
  return (
    <div className="gauge">
      <svg viewBox="0 0 120 68" role="img" aria-label={`${label} ${pctOf(v)}`}>
        <path d="M6 60 A54 54 0 0 1 114 60" className="gauge-track" />
        <path
          d="M6 60 A54 54 0 0 1 114 60"
          className="gauge-fill"
          strokeDasharray={`${dash} ${circ}`}
        />
        <text x="60" y="52" className="gauge-val">{pctOf(v)}</text>
      </svg>
      <span className="stat-k">{label}</span>
    </div>
  );
}

export default function StatRow() {
  const load = useCallback(async ({ signal }: { signal: AbortSignal }): Promise<Live> => {
    const [summary, seats] = await Promise.all([
      softly(getSummary({ signal }), null as Summary | null),
      softly(getSeats(30, { signal }), null as Seats | null),
    ]);
    return { summary, seats };
  }, []);
  const { state } = useResource(load, []);

  if (state.status !== 'ready') return null;
  const { summary, seats } = state.data;
  if (!summary?.totals) return null; // member 스코프·무데이터 → 숨김

  const t = summary.totals;
  const tokens = t.input + t.output + t.cacheRead + t.cacheCreate;
  const denom = t.cacheRead + t.cacheCreate + t.input;
  const hit = denom > 0 ? t.cacheRead / denom : 0;
  const cost = seats?.summary?.totalUsd ?? null;
  const costDelta = seats?.summary?.totalUsdDeltaPct ?? null;

  return (
    <section className="stat-row" aria-label="Live Status">
      <div className="stat-tile tone-teal">
        <span className="stat-k">세션</span>
        <span className="stat-v">{n(t.sessions)}</span>
        <span className="stat-s">사용자 {n(t.users)} · 머신 {n(t.machines)}</span>
      </div>
      <div className="stat-tile tone-indigo">
        <span className="stat-k">총비용</span>
        <span className="stat-v">{cost == null ? '—' : usd(cost)}</span>
        <span className="stat-s">
          {costDelta == null ? '30일' : `직전 대비 ${costDelta >= 0 ? '+' : ''}${pctOf(Math.abs(costDelta) / 100)}`}
        </span>
      </div>
      <div className="stat-tile tone-amber">
        <span className="stat-k">총 토큰</span>
        <span className="stat-v">{shortTokens(tokens)}</span>
        <span className="stat-s">캐시읽기 {shortTokens(t.cacheRead)}</span>
      </div>
      <div className="stat-tile tone-violet">
        <span className="stat-k">출력 토큰</span>
        <span className="stat-v">{shortTokens(t.output)}</span>
        <span className="stat-s">입력 {shortTokens(t.input)}</span>
      </div>
      <div className="stat-tile stat-gauge">
        <Gauge value={hit} label="캐시 적중률" />
      </div>
    </section>
  );
}
