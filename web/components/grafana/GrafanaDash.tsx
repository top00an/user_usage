'use client';

/*
 * Grafana 스타일 메인 대시보드. 실데이터(getSummary + getSeats)를 ECharts 패널로 그린다.
 * 섹션별 그리드, 패널은 드래그로 재배치(DragGrid). 사진의 레이아웃을 따른다.
 *
 * LOC·편집 수락/거부는 **백엔드가 실제로 수집한다**(실측: 추가 26,328줄 · 수락 279 · 거부 1).
 * 다만 아직 보고가 없는 팀이나 구버전 수집기에서는 그 축이 통째로 비어 온다. 그때 0 을 그리면
 * 화면이 "안 썼다"고 **단정**하고(도넛은 합계 0 을 균등 분할로 그려 가짜 비율까지 만든다),
 * 그래서 값이 없는 패널은 차트 대신 `미수집` 안내를 띄운다 — 가짜 숫자 금지.
 */
import { useCallback, useEffect, useState, useSyncExternalStore } from 'react';
import { createPortal } from 'react-dom';
import { getSummary, getSeats, getDev, getPlatforms } from '@/lib/api';
import { softly, useResource } from '@/hooks/useResource';
import type { Dev, PlatformsResponse, Seats, Summary } from '@/lib/types';
import { Empty, ErrorState, Loading } from '@/components/ui';
import PlatformFilter from '@/components/platform/PlatformFilter';
import PlatformSummary from '@/components/platform/PlatformSummary';
import { COST_DISCLAIMER, COST_LABEL, COST_WHY } from '@/lib/costLabels';
import EChart from '@/components/charts/EChart';
import DragGrid, { resetLayout, type GridItem } from './DragGrid';
import { areaOption, gaugeOption, donutOption, barOption, hasValues, hasSeriesValues, short, fmtInt } from './options';
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

/*
 * ── 상단 액션 슬롯(#head-actions) ────────────────────────────────────────
 *
 * 이 노드는 우리가 아니라 셸(components/Dashboard.tsx)이 렌더한다. 그래서 이 탭의 첫 렌더에는
 * 아직 DOM 에 없고 커밋 뒤에야 잡힌다. 이펙트에서 setState 로 잡으면 렌더가 한 번 더 돌면서 그
 * 사이 한 프레임 동안 버튼 없는 헤더가 보인다(react-hooks/set-state-in-effect).
 *
 * DOM 은 우리 밖의 저장소다 — Dashboard.tsx 가 쿠키·해시를 읽는 방법을 그대로 쓴다.
 * useSyncExternalStore 는 구독을 붙인 직후 스냅샷을 한 번 다시 확인하므로, 커밋으로 막 생긴
 * 슬롯을 그 자리에서 잡아낸다. getElementById 는 같은 노드에 같은 참조를 주므로 스냅샷이
 * 안정적이다(매번 새 값을 주면 무한 렌더가 된다).
 */
const readHeadSlot = (): HTMLElement | null =>
  (typeof document === 'undefined' ? null : document.getElementById('head-actions'));

function subscribeHeadSlot(onChange: () => void): () => void {
  // 이미 있으면 관찰할 것이 없다 — 셸이 사는 동안 이 노드는 교체되지 않는다(보통 이 경로).
  if (readHeadSlot()) return () => {};
  // 아직 없다면 생길 때까지만 지켜보고 끊는다. 상시 관찰이 아니라 비용이 남지 않는다.
  const mo = new MutationObserver(() => { if (readHeadSlot()) { mo.disconnect(); onChange(); } });
  mo.observe(document.body, { childList: true, subtree: true });
  return () => mo.disconnect();
}

/* ── 패널 껍데기(드래그 핸들 = 제목바) ── */
function Panel({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="gpanel-card">
      <div className="gpanel-head"><span className="grip" aria-hidden="true">⋮⋮</span> {title}</div>
      <div className="gpanel-body">{children}</div>
    </div>
  );
}
/*
 * 값이 없는 패널의 자리.
 *
 * ① **차트가 쓰던 높이를 그대로 지킨다** — 값이 들어오고 나갈 때 그리드가 튀면 사람은 그
 *    움직임을 데이터 변화로 읽는다.
 * ② 색이 아니라 **글자**로 상태를 말한다(components/platform/SupportBadge.tsx 와 같은 규율).
 * ③ 왜 비었는지를 함께 남긴다 — 이유 없는 빈 칸은 "버그인가?"로 읽힌다.
 *
 * 문구의 `미수집` 은 이 레포의 어휘다: 값이 존재하지 않는 `해당 없음` 과 달리, **올 수 있는
 * 값인데 오지 않았다**는 뜻이다(README 의 미수집/해당 없음 구분).
 */
function NoData({ height, why }: { height: number; why: string }) {
  return (
    <div style={{ height, display: 'grid', placeItems: 'center', textAlign: 'center', padding: '0 12px' }}>
      {/* 이유는 .help(작고 흐린 보조 문구) — 상태어와 크기·명도로 위계를 만든다. 폭은 measure 로
          묶는다: 넓은 패널에서 한 줄이 화면 끝까지 늘어나면 읽는 눈이 줄을 잃는다. */}
      <div style={{ maxWidth: '34ch' }}>
        <Empty><b>미수집</b></Empty>
        <p className="help" style={{ marginTop: 2 }}>{why}</p>
      </div>
    </div>
  );
}

/*
 * 도넛 패널 — **네 곳(편집 결정 · 도구 · 서브에이전트 · 스킬)이 전부 이 경로를 탄다.**
 * 한 곳만 고치면 나머지가 같은 사고를 그대로 낸다(합계 0 → echarts 균등 분할, 빈 배열 → 회색 링).
 */
function DonutPanel({
  title, rows, why, height = 190,
}: { title: string; rows: { name: string; value: number }[]; why: string; height?: number }) {
  return (
    <Panel title={title}>
      {hasValues(rows)
        ? <EChart option={donutOption(rows)} height={height} />
        : <NoData height={height} why={why} />}
    </Panel>
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
/*
 * 섹션 껍데기.
 *
 * ⚠ 이 컴포넌트는 **모듈 스코프에 있어야 한다.** GrafanaDash 의 렌더 함수 안에서 선언하면 매
 * 렌더마다 새 타입이 되고, React 는 같은 자리를 다른 컴포넌트로 보아 섹션 서브트리를 통째로
 * 언마운트·리마운트한다. 그러면 안에 있는 DragGrid 가 상태(드래그 순서·하이라이트)를 잃고
 * DOM 노드가 새로 생긴다 — 화면에서는 패널이 깜빡이고 애니메이션이 끊긴다
 * (react-hooks/static-components · test/grafana-layout.test.tsx 가 노드 동일성으로 잰다).
 */
function Sect({ title, gid, cls, items }: { title: string; gid: string; cls?: string; items: GridItem[] }) {
  return (
    <section className="gsect">
      <div className="gsect-h"><span className="caret">▾</span> {title}</div>
      <DragGrid gridId={gid} className={cls} items={items} />
    </section>
  );
}

function BarTable({ rows, unit, fmt }: { rows: { label: string; value: number }[]; unit: string; fmt: (n: number) => string }) {
  const max = Math.max(...rows.map((r) => r.value), 1);
  const palette = ['#5794f2', '#e0742f', '#73bf69', '#f2cc0c', '#b877d9', '#37872d', '#e0523e', '#8ab8ff'];
  return (
    <table className="gtable">
      <thead><tr><th>이름</th><th className="r">{unit}</th></tr></thead>
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
  const headSlot = useSyncExternalStore(subscribeHeadSlot, readHeadSlot, () => null);

  /*
   * 추적/관측 탭의 '그래프로 추가'로 넘어온 프리필이 있으면 빌더를 연다.
   *
   * ⚠ 여기만 set-state-in-effect 예외다 — 규칙이 요구하는 두 대안이 **둘 다 이 자리에서 틀린다.**
   *   ① useState 초기화 함수: takeBuilderPrefill 은 읽으면서 지우는 소비형 읽기다. 렌더 중
   *      부수효과라 StrictMode(next.config.mjs 의 reactStrictMode: true)가 초기화 함수를 두 번
   *      부르면 두 번째는 이미 비어 있어 프리필이 조용히 사라진다. 정적 export 라
   *      프리렌더에서도 한 번 도는데, 그때는 localStorage 가 없어 서버·클라이언트 결과가
   *      갈리고 하이드레이션이 어긋난다.
   *   ② useSyncExternalStore: 소비형 읽기는 스냅샷이 될 수 없다(읽을 때마다 값이 달라진다).
   * 저장소를 비파괴 읽기로 바꾸는 것이 정공법이지만 lib/customPanels.ts 는 이 웨이브에서 내
   * 소유가 아니다. 대가는 빌더가 한 프레임 늦게 열리는 것뿐이고 — 그 프레임에 틀린 데이터가
   * 보이지는 않는다(모달이 아직 없을 뿐이다).
   */
  useEffect(() => {
    const p = takeBuilderPrefill();
    // eslint-disable-next-line react-hooks/set-state-in-effect -- 소비형(read-and-clear) 읽기: 위 주석 참조
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
  /*
   * 라벨은 한국어로 통일한다. 비용 타일만 한글(COST_LABEL)이고 나머지가 영어면, 한 행 안에서
   * 두 언어가 섞여 사람은 그 차이를 **의미의 차이**로 읽는다("한글 타일만 우리가 계산한 값인가?").
   * 여기서 바꾸는 것은 표시 문자열뿐이다 — 패널 id(DragGrid 레이아웃 키)는 건드리지 않는다.
   */
  const live: GridItem[] = [
    { id: 'live-sessions', node: <StatTile tone="t-teal" k="활성 세션" v={fmtInt(t.sessions)} s={`사용자 ${t.users} · 머신 ${t.machines}`} /> },
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
    { id: 'live-tokens', node: <StatTile tone="t-orange" k="전체 토큰" v={short(tokensTotal)} s={`캐시읽기 ${short(t.cacheRead)}`} /> },
    { id: 'live-output', node: <StatTile tone="t-purple" k="출력 토큰" v={short(t.output)} s={`입력 ${short(t.input)}`} /> },
    { id: 'live-hit', node: <GaugeTile tone="t-teal" label="캐시 적중률" value={cacheHit} color="#73bf69" /> },
    { id: 'live-share', node: <GaugeTile tone="t-blue" label="캐시읽기 비중" value={cacheReadShare} color="#5794f2" /> },
  ];

  const cost2: GridItem[] = [
    { id: 'cost-models', node: <Panel title="모델별 토큰 분포"><BarTable rows={modelRows} unit="토큰" fmt={short} /></Panel> },
    { id: 'cost-rate', node: <Panel title="일별 토큰 추이"><EChart option={areaOption(x, [{ name: '토큰', color: '#73bf69', data: days.map((d) => d.input + d.output + d.cacheRead + d.cacheCreate) }], short)} height={180} /></Panel> },
  ];

  /*
   * 개발 지표(LOC·편집 결정) — 실제 수집값이다. dev 가 null 이거나 그 축이 전부 0 이면
   * **대체 없이 안내**한다(차트를 다른 지표로 바꿔치기하지 않는다 — 그 자리에 무엇이 없는지가
   * 정보다). LOC 면적 차트도 같은 판정을 받는다: 전부 0 인 계열은 0 평선을 그려 "추가 0줄"이라고
   * 단정하는데, 서버 응답만으로는 그것이 관측된 0 인지 미수집인지 가릴 수 없기 때문이다.
   */
  const devDays = [...(dev?.byDay ?? [])].reverse();
  const devX = devDays.map((d) => d.day.slice(5));
  const dt = dev?.totals;
  const locSeries = [
    { name: '추가', color: '#73bf69', data: devDays.map((d) => d.linesAdded) },
    { name: '삭제', color: '#e0523e', data: devDays.map((d) => d.linesRemoved) },
  ];
  const dev2: GridItem[] = [
    { id: 'dev-loc', node: (
      <Panel title="일별 LOC (추가 · 삭제)">
        {hasSeriesValues(locSeries)
          ? <EChart option={areaOption(devX, locSeries, fmtInt)} height={180} />
          : <NoData height={180} why="이 기간에 보고된 LOC 변경이 없습니다. 구버전 수집기이거나 아직 보고가 없는 팀입니다 — 0 줄을 썼다는 뜻이 아닙니다." />}
      </Panel>
    ) },
    { id: 'dev-edit', node: (
      <DonutPanel
        title="코드 편집 결정 (수락 · 거부)"
        rows={[
          { name: '수락', value: dt?.editsAccepted ?? 0 },
          { name: '거부', value: dt?.editsRejected ?? 0 },
        ]}
        why="이 기간에 보고된 편집 수락·거부가 없습니다. 구버전 수집기이거나 아직 보고가 없는 팀입니다 — 0 건이라는 뜻이 아닙니다."
      />
    ) },
    { id: 'dev-io', node: <Panel title="토큰 입출력 추이"><EChart option={areaOption(x, [{ name: '입력', color: '#5794f2', data: days.map((d) => d.input) }, { name: '출력', color: '#73bf69', data: days.map((d) => d.output) }], short)} height={180} /></Panel> },
  ];

  const cache: GridItem[] = [
    { id: 'cache-usage', node: <Panel title="일별 캐시 읽기 · 생성"><EChart option={areaOption(x, [{ name: '캐시읽기', color: '#73bf69', data: days.map((d) => d.cacheRead) }, { name: '캐시생성', color: '#5794f2', data: days.map((d) => d.cacheCreate) }], short)} height={200} /></Panel> },
  ];

  /* 세 도넛도 편집 결정과 같은 경로다 — 비면 빈 회색 링 대신 무엇이 없는지 말한다. */
  const notReported = (what: string) =>
    `이 기간에 보고된 ${what} 기록이 없습니다. 구버전 수집기이거나 이 축을 기록하지 않는 플랫폼입니다.`;
  const tools: GridItem[] = [
    { id: 'tool-usage', node: <DonutPanel title="도구 사용" rows={donutRows(summary.top?.tool).slice(0, 10)} why={notReported('내장 도구')} /> },
    { id: 'tool-agents', node: <DonutPanel title="서브에이전트" rows={donutRows(summary.top?.agent)} why={notReported('서브에이전트')} /> },
    { id: 'tool-skills', node: <DonutPanel title="스킬" rows={donutRows((summary.top?.skill ?? []).map((k) => ({ key: k.key.replace(/^superpowers:/, ''), count: k.count })))} why={notReported('스킬')} /> },
  ];

  const rates: GridItem[] = [
    { id: 'rate-sessions', node: <Panel title="일별 세션 수"><EChart option={areaOption(x, [{ name: '세션', color: '#73bf69', data: days.map((d) => d.sessions) }], fmtInt)} height={170} /></Panel> },
    { id: 'rate-bash', node: <Panel title="개발 명령"><BarTable rows={(summary.top?.bash ?? []).slice(0, 10).map((k) => ({ label: k.key, value: k.count }))} unit="횟수" fmt={fmtInt} /></Panel> },
    { id: 'rate-mcp', node: <Panel title="MCP 호출"><BarTable rows={(summary.top?.mcp ?? []).slice(0, 10).map((k) => ({ label: k.key.replace(/^mcp__/, '').slice(0, 24), value: k.count }))} unit="횟수" fmt={fmtInt} /></Panel> },
  ];

  const topTools = (summary.top?.tool ?? []).slice(0, 10).map((k) => ({ name: k.key, value: k.count }));
  const top: GridItem[] = [
    { id: 'top-tools', node: <Panel title="상위 도구 (호출 수)"><EChart option={barOption(topTools, fmtInt)} height={Math.max(120, topTools.length * 30)} /></Panel> },
  ];

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

      {/* 섹션 제목도 한국어로 통일한다 — '플랫폼'·'내 그래프'가 이미 한국어라 영어 제목만 섞여 있었다. */}
      <Sect title="실시간 현황" gid="live" cls="g6" items={live} />
      <Sect title="비용 · 토큰" gid="cost" cls="g-cost" items={cost2} />
      <Sect title="개발 지표" gid="dev" cls="g3" items={dev2} />
      <Sect title="캐시 토큰 사용" gid="cache" cls="g1" items={cache} />
      <Sect title="도구 · 에이전트 분석" gid="tools" cls="g3" items={tools} />
      <Sect title="추이 · 상세" gid="rates" cls="g3" items={rates} />
      <Sect title="상위 도구" gid="top" cls="g1" items={top} />

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
