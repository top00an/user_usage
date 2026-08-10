'use client';

/*
 * Grafana 스타일 메인 대시보드. 실데이터(getSummary + getSeats)를 ECharts 패널로 그린다.
 * 섹션별 그리드, 패널은 드래그로 재배치(DragGrid). 사진의 레이아웃을 따른다.
 *
 * 데이터 갭(LOC·Edit 수락/거부·Active Time)은 백엔드 미수집이라 넣지 않고, 실제 수집 지표
 * (토큰 I/O·캐시·세션·도구·에이전트·스킬)로 채운다 — 가짜 숫자 금지.
 */
import { useCallback, useEffect, useState, useSyncExternalStore } from 'react';
import { createPortal } from 'react-dom';
import { getSummary, getSeats, getDev, getPlatforms } from '@/lib/api';
import { softly, useResource } from '@/hooks/useResource';
import type { Dev, PlatformsResponse, Seats, Summary } from '@/lib/types';
import { ErrorState, Loading } from '@/components/ui';
import PlatformFilter from '@/components/platform/PlatformFilter';
import PlatformSummary from '@/components/platform/PlatformSummary';
import { COST_DISCLAIMER, COST_LABEL, COST_WHY } from '@/lib/costLabels';
import EChart from '@/components/charts/EChart';
import DragGrid, { resetLayout, type GridItem } from './DragGrid';
import { areaOption, gaugeOption, donutOption, barOption, short, fmtInt } from './options';
import ChartBuilder from './ChartBuilder';
import CustomPanelView from './CustomPanelView';
import {
  removePanel, subscribePanels, panelsSnapshot, takeBuilderPrefill,
  type CustomPanel,
} from '@/lib/customPanels';

interface Data {
  summary: Summary | null;
  seats: Seats | null;
  dev: Dev | null;
  /** 플랫폼 롤업. 관리자 전용이라 member 스코프에서는 null 이다(카드가 스스로 안내한다). */
  platforms: PlatformsResponse | null;
}

const usd = (n: number) => '$' + n.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 });

/* ── 패널 껍데기(드래그 핸들 = 제목바) ── */
function Panel({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="gpanel-card">
      <div className="gpanel-head"><span className="grip" aria-hidden="true">⋮⋮</span> {title}</div>
      <div className="gpanel-body">{children}</div>
    </div>
  );
}
function StatTile({ tone, k, v, s, title }: { tone: string; k: string; v: string; s?: string; title?: string }) {
  return (
    <div className={`gstat ${tone}`} title={title}>
      <span className="gstat-k">{k}</span>
      <span className="gstat-v num">{v}</span>
      {s && <span className="gstat-s">{s}</span>}
    </div>
  );
}
function GaugeTile({ tone, label, value, color }: { tone: string; label: string; value: number; color: string }) {
  return (
    <div className={`gstat ${tone} gstat-gauge`}>
      <span className="gstat-k">{label}</span>
      <EChart option={gaugeOption(value, color)} height={90} />
    </div>
  );
}
function BarTable({ rows, unit, fmt }: { rows: { label: string; value: number }[]; unit: string; fmt: (n: number) => string }) {
  const max = Math.max(...rows.map((r) => r.value), 1);
  const palette = ['#5794f2', '#e0742f', '#73bf69', '#f2cc0c', '#b877d9', '#37872d', '#e0523e', '#8ab8ff'];
  return (
    <table className="gtable">
      <thead><tr><th>Name</th><th className="r">{unit}</th></tr></thead>
      <tbody>
        {rows.map((r, i) => (
          <tr key={r.label}>
            <td className="gbarcell">
              <span className="gbar" style={{ width: `${(r.value / max) * 100}%`, background: palette[i % palette.length] }} />
              <span className="glab">{r.label}</span>
            </td>
            <td className="r num">{fmt(r.value)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

export default function GrafanaDash() {
  const load = useCallback(async ({ signal }: { signal: AbortSignal }): Promise<Data> => {
    const [summary, seats, dev, platforms] = await Promise.all([
      softly(getSummary({ signal }), null as Summary | null),
      softly(getSeats(3650, { signal }), null as Seats | null),
      softly(getDev(365, { signal }), null as Dev | null),
      softly(getPlatforms({ signal }), null as PlatformsResponse | null),
    ]);
    return { summary, seats, dev, platforms };
  }, []);
  const { state, reload } = useResource(load, []);

  // 커스텀 패널(내 그래프) — 저장소 구독으로 추가/삭제 즉시 반영.
  const panelsSnap = useSyncExternalStore(subscribePanels, panelsSnapshot, () => '[]');
  const customPanels: CustomPanel[] = (() => { try { return JSON.parse(panelsSnap); } catch { return []; } })();
  const [builderOpen, setBuilderOpen] = useState(false);
  const [prefill, setPrefill] = useState<Partial<Omit<CustomPanel, 'id'>> | undefined>(undefined);
  // 상단 액션 버튼은 공용 헤더(content-head)의 우측 슬롯에 포털로 얹는다 — 제목 라인과 같은 줄.
  const [headSlot, setHeadSlot] = useState<HTMLElement | null>(null);
  useEffect(() => { setHeadSlot(document.getElementById('head-actions')); }, []);

  // 추적/관측 탭의 '그래프로 추가'로 넘어온 프리필이 있으면 빌더를 연다.
  useEffect(() => {
    const p = takeBuilderPrefill();
    if (p) { setPrefill(p); setBuilderOpen(true); }
  }, []);

  if (state.status === 'loading') return <Loading />;
  if (state.status === 'error') return <ErrorState what="대시보드를 불러오지 못했습니다." error={state.error} onRetry={reload} />;

  const { summary, seats, dev, platforms } = state.data;
  const platformRows = platforms?.platforms ?? null;
  if (!summary?.totals) {
    // member 스코프(전사 지표 403) 또는 무데이터.
    return <p className="hint" style={{ padding: '24px 0' }}>이 대시보드는 관리자 토큰이 필요합니다(전사 지표). 개인 열람 토큰은 사용 관측 탭에서 자기 데이터를 봅니다.</p>;
  }

  const t = summary.totals;
  const tokensTotal = t.input + t.output + t.cacheRead + t.cacheCreate;
  const denom = t.cacheRead + t.cacheCreate + t.input;
  const cacheHit = denom ? t.cacheRead / denom : 0;
  const cacheReadShare = tokensTotal ? t.cacheRead / tokensTotal : 0;

  const days = [...(summary.byDay ?? [])].reverse(); // 오름차순
  const x = days.map((d) => d.day.slice(5));
  const modelRows = (summary.byModel ?? [])
    .map((m) => ({ label: m.model.replace(/^.*[./]/, '').replace(/^claude-/, ''), value: m.input + m.output + m.cacheRead + m.cacheCreate }))
    .filter((r) => r.value > 0);
  const donutRows = (arr?: { key: string; count: number }[]) => (arr ?? []).map((k) => ({ name: k.key, value: k.count }));

  const cost = seats?.summary?.totalUsd ?? null;

  // ── 섹션별 아이템 ──
  const live: GridItem[] = [
    { id: 'live-sessions', node: <StatTile tone="t-teal" k="Active Sessions" v={fmtInt(t.sessions)} s={`users ${t.users} · machines ${t.machines}`} /> },
    /*
     * 'Total Cost' 였던 자리. 그 이름은 청구액으로 읽힌다 — 우리 값은 환산 추정치다(lib/costLabels.ts).
     * 부제의 '90-day' 도 사실이 아니었다: 이 타일은 getSeats(3650) 의 합계라 전체 기간이다.
     */
    { id: 'live-cost', node: (
      <StatTile
        tone="t-blue"
        k={COST_LABEL}
        v={cost == null ? '—' : usd(cost)}
        s="전체 기간 · 실제 청구액 아님"
        title={`${COST_DISCLAIMER}. ${COST_WHY}`}
      />
    ) },
    { id: 'live-tokens', node: <StatTile tone="t-orange" k="Total Tokens" v={short(tokensTotal)} s={`cache read ${short(t.cacheRead)}`} /> },
    { id: 'live-output', node: <StatTile tone="t-purple" k="Output Tokens" v={short(t.output)} s={`input ${short(t.input)}`} /> },
    { id: 'live-hit', node: <GaugeTile tone="t-teal" label="Cache Hit Ratio" value={cacheHit} color="#73bf69" /> },
    { id: 'live-share', node: <GaugeTile tone="t-blue" label="Cache Read Share" value={cacheReadShare} color="#5794f2" /> },
  ];

  const cost2: GridItem[] = [
    { id: 'cost-models', node: <Panel title="Token Distribution by Model"><BarTable rows={modelRows} unit="Tokens" fmt={short} /></Panel> },
    { id: 'cost-rate', node: <Panel title="Token Rate (daily)"><EChart option={areaOption(x, [{ name: 'tokens', color: '#73bf69', data: days.map((d) => d.input + d.output + d.cacheRead + d.cacheCreate) }], short)} height={180} /></Panel> },
  ];

  // 개발 지표(LOC·편집 결정) — 실제 수집값. dev 가 null 이거나 값이 0 이면 해당 패널은 대체 없이 안내.
  const devDays = [...(dev?.byDay ?? [])].reverse();
  const devX = devDays.map((d) => d.day.slice(5));
  const dt = dev?.totals;
  const dev2: GridItem[] = [
    { id: 'dev-loc', node: (
      <Panel title="Lines of Code (Added vs Removed, daily)">
        <EChart option={areaOption(devX, [
          { name: 'added', color: '#73bf69', data: devDays.map((d) => d.linesAdded) },
          { name: 'removed', color: '#e0523e', data: devDays.map((d) => d.linesRemoved) },
        ], fmtInt)} height={180} />
      </Panel>
    ) },
    { id: 'dev-edit', node: (
      <Panel title="Code Edit Decisions (Accept vs Reject)">
        <EChart option={donutOption([
          { name: 'accept', value: dt?.editsAccepted ?? 0 },
          { name: 'reject', value: dt?.editsRejected ?? 0 },
        ])} height={190} />
      </Panel>
    ) },
    { id: 'dev-io', node: <Panel title="Token I/O Rate"><EChart option={areaOption(x, [{ name: 'input', color: '#5794f2', data: days.map((d) => d.input) }, { name: 'output', color: '#73bf69', data: days.map((d) => d.output) }], short)} height={180} /></Panel> },
  ];

  const cache: GridItem[] = [
    { id: 'cache-usage', node: <Panel title="Cache Read vs Creation (daily)"><EChart option={areaOption(x, [{ name: 'Cache Read', color: '#73bf69', data: days.map((d) => d.cacheRead) }, { name: 'Cache Creation', color: '#5794f2', data: days.map((d) => d.cacheCreate) }], short)} height={200} /></Panel> },
  ];

  const tools: GridItem[] = [
    { id: 'tool-usage', node: <Panel title="Tool Usage"><EChart option={donutOption(donutRows(summary.top?.tool).slice(0, 10))} height={190} /></Panel> },
    { id: 'tool-agents', node: <Panel title="Subagents"><EChart option={donutOption(donutRows(summary.top?.agent))} height={190} /></Panel> },
    { id: 'tool-skills', node: <Panel title="Skills"><EChart option={donutOption(donutRows((summary.top?.skill ?? []).map((k) => ({ key: k.key.replace(/^superpowers:/, ''), count: k.count }))))} height={190} /></Panel> },
  ];

  const rates: GridItem[] = [
    { id: 'rate-sessions', node: <Panel title="Sessions per Day"><EChart option={areaOption(x, [{ name: 'sessions', color: '#73bf69', data: days.map((d) => d.sessions) }], fmtInt)} height={170} /></Panel> },
    { id: 'rate-bash', node: <Panel title="Bash Commands"><BarTable rows={(summary.top?.bash ?? []).slice(0, 10).map((k) => ({ label: k.key, value: k.count }))} unit="Count" fmt={fmtInt} /></Panel> },
    { id: 'rate-mcp', node: <Panel title="MCP Calls"><BarTable rows={(summary.top?.mcp ?? []).slice(0, 10).map((k) => ({ label: k.key.replace(/^mcp__/, '').slice(0, 24), value: k.count }))} unit="Count" fmt={fmtInt} /></Panel> },
  ];

  const topTools = (summary.top?.tool ?? []).slice(0, 10).map((k) => ({ name: k.key, value: k.count }));
  const top: GridItem[] = [
    { id: 'top-tools', node: <Panel title="Top Tools (calls)"><EChart option={barOption(topTools, fmtInt)} height={Math.max(120, topTools.length * 30)} /></Panel> },
  ];

  const Sect = ({ title, gid, cls, items }: { title: string; gid: string; cls?: string; items: GridItem[] }) => (
    <section className="gsect">
      <div className="gsect-h"><span className="caret">▾</span> {title}</div>
      <DragGrid gridId={gid} className={cls} items={items} />
    </section>
  );

  // '내 그래프' — 사용자가 만든 커스텀 패널. 각 패널에 삭제 버튼.
  const customItems: GridItem[] = customPanels.map((p) => ({
    id: p.id,
    node: (
      <div className="gpanel-card">
        <div className="gpanel-head">
          <span className="grip" aria-hidden="true">⋮⋮</span> {p.title}
          <span style={{ flex: 1 }} />
          <button className="panel-x" type="button" title="삭제" onClick={() => removePanel(p.id)}>✕</button>
        </div>
        <div className="gpanel-body"><CustomPanelView panel={p} /></div>
      </div>
    ),
  }));

  return (
    <div className="gdash">
      {headSlot && createPortal(
        <>
          <button className="primary" type="button" onClick={() => { setPrefill(undefined); setBuilderOpen(true); }}>＋ 그래프 추가</button>
          <button className="ghost" type="button" onClick={resetLayout}>⤢ 레이아웃 초기화</button>
        </>,
        headSlot,
      )}

      {/*
        플랫폼 선택 — 이 탭의 패널들(summary·seats·dev)은 서버가 platform 축으로 거르지 못한다.
        그래서 applies={false} 로 두고 화면이 그 사실을 말한다. 선택은 탭을 넘어 유지되므로
        여기서 고른 값이 '사용 관측'의 조회에 그대로 실린다.
      */}
      <PlatformFilter rows={platformRows} applies={false} what="아래 패널은 플랫폼 축으로 걸러지지 않습니다" />

      <section className="gsect">
        <div className="gsect-h"><span className="caret">▾</span> 플랫폼</div>
        <PlatformSummary rows={platformRows} />
      </section>

      {customItems.length > 0 && (
        <Sect title="내 그래프" gid="custom" cls="g2" items={customItems} />
      )}

      <Sect title="Live Status" gid="live" cls="g6" items={live} />
      <Sect title="Cost & Tokens" gid="cost" cls="g-cost" items={cost2} />
      <Sect title="Development" gid="dev" cls="g3" items={dev2} />
      <Sect title="Cache Token Usage" gid="cache" cls="g1" items={cache} />
      <Sect title="Tool & Agent Analytics" gid="tools" cls="g3" items={tools} />
      <Sect title="Rates & Details" gid="rates" cls="g3" items={rates} />
      <Sect title="Top Tools" gid="top" cls="g1" items={top} />

      {builderOpen && (
        <ChartBuilder
          prefill={prefill}
          onClose={() => setBuilderOpen(false)}
          onAdded={() => { /* 저장소 구독이 자동 반영 */ }}
        />
      )}
    </div>
  );
}
