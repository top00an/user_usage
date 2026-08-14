'use client';

/*
 * Grafana 스타일 메인 대시보드. 실데이터(getSummary + getSeats)를 ECharts 패널로 그린다.
 * 패널은 **하나의 자유 캔버스**(CanvasGrid)에 얹히고, 편집 모드에서 어디로든 옮기고 칸 단위로
 * 크기를 바꾼다. 배치는 유저별로 서버에 저장된다(lib/layoutPrefs.ts).
 *
 * ── 섹션 제목이 사라진 이유 ───────────────────────────────────────────────
 *
 * 예전에는 7개 섹션(`실시간 현황`·`비용 · 토큰` …)이 각자 그리드를 들고, 패널은 **자기 섹션
 * 안에서만** 순서가 바뀌었다. 자유 캔버스에서는 패널이 섹션 밖으로 나간다 — 그러면 남은 제목은
 * 아래 있는 것을 설명하지 못하는 **거짓말**이 된다("비용 · 토큰" 아래에 도구 도넛이 앉는다).
 * 그래서 제목은 지우고, 그 제목들이 하던 일은 둘로 나눠 흡수했다:
 *   ① 무엇이 있는 화면인가 → 캔버스 위 한 줄(CanvasBar)이 말한다.
 *   ② 이 패널이 무엇인가   → 패널 제목이 이미 말한다(스탯 타일은 라벨 자체가 제목이다).
 * 패널 id 는 **저장된 레이아웃의 키**라 하나도 바꾸지 않았다.
 *
 * LOC·편집 수락/거부는 **백엔드가 실제로 수집한다**(실측: 추가 26,328줄 · 수락 279 · 거부 1).
 * 다만 아직 보고가 없는 팀이나 구버전 수집기에서는 그 축이 통째로 비어 온다. 그때 0 을 그리면
 * 화면이 "안 썼다"고 **단정**하고(도넛은 합계 0 을 균등 분할로 그려 가짜 비율까지 만든다),
 * 그래서 값이 없는 패널은 차트 대신 `미수집` 안내를 띄운다 — 가짜 숫자 금지.
 */
import { useCallback, useEffect, useState, useSyncExternalStore } from 'react';
import { createPortal } from 'react-dom';
import { getSummary, getSeats, getDev, getPlatforms, getLeaderboard } from '@/lib/api';
import { softly, useResource } from '@/hooks/useResource';
import type { Dev, Leaderboard, PlatformsResponse, Seats, Summary } from '@/lib/types';
import { Empty, ErrorState, Loading } from '@/components/ui';
import PlatformFilter from '@/components/platform/PlatformFilter';
import UserFilter from '@/components/usagetrack/UserFilter';
import PlatformSummary from '@/components/platform/PlatformSummary';
import { usePlatformFilter } from '@/lib/platformFilter';
import { COST_DISCLAIMER, COST_LABEL, COST_WHY } from '@/lib/costLabels';
import EChart from '@/components/charts/EChart';
import CanvasGrid, { type CanvasItem } from './CanvasGrid';
import { useDashLayout, type SaveStatus } from '@/lib/layoutPrefs';
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
 * ── 기본 배치(defaultBox) ────────────────────────────────────────────────
 *
 * 저장된 레이아웃이 없을 때의 첫 화면이다. **지금까지 보던 화면과 같아 보이게** 옛 섹션의 열
 * 수를 그대로 12칸으로 옮겼다: `.g6`→2칸 · `.g-cost`(5fr 7fr)→5칸+7칸 · `.g3`→4칸 ·
 * `.g2`→6칸 · `.g1`→12칸.
 *
 * y 는 **순서 힌트**다. normalizeLayout 이 위로 당기므로(compact) 절대값이 아니라 블록 사이의
 * 상대 순서만 맞으면 된다 — 그래서 블록마다 10씩 벌려 둔다. 사이에 패널을 하나 끼워 넣을 때
 * 아래 전부를 다시 세지 않아도 된다.
 *
 * 높이는 ROW_H=56 기준이고, 안의 차트/표가 **잘리지 않을 만큼** 넉넉히 준다 —
 * `.dc-cell` 은 overflow:hidden 이라 모자라면 조용히 잘린다(빈 칸보다 나쁜 쪽이다).
 */
const ROW = {
  live: 0, cost: 10, dev: 20, cache: 30, tools: 40, rates: 50, top: 60,
  /** 커스텀 패널은 **맨 아래**에 붙인다 — 새로 만든 패널이 남의 배치를 밀어내지 않게. */
  custom: 900,
} as const;

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

/*
 * ── 패널 껍데기 ──────────────────────────────────────────────────────────
 *
 * 옛 제목바에 있던 `⋮⋮` 그립을 걷었다. 자유 캔버스에서는 **카드 전체가** 드래그 대상이고
 * 그것도 편집 모드에서만이다 — 그립은 "여기만 잡힌다"고 말하고, 읽기 전용일 때는 아예 거짓이다.
 * 잡을 수 있다는 신호는 편집 모드의 격자·호버 링·리사이즈 핸들이 준다(globals.css 의 .dashcanvas).
 *
 * ⚠ **본문은 넘치면 스크롤한다.** 캔버스의 칸은 높이가 정해져 있고 `.dc-cell`·`.gpanel-card` 는
 * overflow:hidden 이라, 그냥 두면 표의 아래 몇 줄이 **말없이 잘린다**. 그 규칙은 여기가 아니라
 * `globals.css` 의 `.gpanel-card` 에 있다 — 앞으로 생길 패널과 커스텀 패널이 자동으로 같은
 * 규율을 타야 하기 때문이다(근거는 그 자리에 적혀 있다).
 */
function Panel({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="gpanel-card">
      <div className="gpanel-head">{title}</div>
      <div className="gpanel-body">{children}</div>
    </div>
  );
}
/*
 * 값이 없는 패널의 자리.
 *
 * ① **차트가 쓰는 높이를 그대로 따라간다** — 값이 들어오고 나갈 때 그리드가 튀면 사람은 그
 *    움직임을 데이터 변화로 읽는다. 차트가 남는 세로 공간을 먹게 된 지금(EChart 머리말),
 *    빈 상태도 **같은 방법으로** 먹어야 한다: `height` 를 고정으로 박으면 값이 오는 순간
 *    패널 안이 그 차이만큼 튄다. 그래서 `.fillv` + min-height 로, 차트와 한 글자도 다르지 않게.
 * ② 색이 아니라 **글자**로 상태를 말한다(components/platform/SupportBadge.tsx 와 같은 규율).
 * ③ 왜 비었는지를 함께 남긴다 — 이유 없는 빈 칸은 "버그인가?"로 읽힌다.
 *
 * 문구의 `미수집` 은 이 레포의 어휘다: 값이 존재하지 않는 `해당 없음` 과 달리, **올 수 있는
 * 값인데 오지 않았다**는 뜻이다(README 의 미수집/해당 없음 구분).
 */
function NoData({ height, why }: { height: number; why: string }) {
  return (
    <div className="fillv" style={{ minHeight: height, display: 'grid', placeItems: 'center', textAlign: 'center', padding: '0 12px' }}>
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
/*
 * 게이지 타일 — 이것도 캔버스의 칸 안에 있고, **같은 규율을 탄다.**
 *
 * `.gstat` 은 이미 세로 flex 라(globals.css) EChart 의 `.fillv` 가 여기서도 그대로 먹는다 —
 * 따로 붙일 것이 없다. 그래도 되는지가 판단할 지점이었고, 답은 "그래야 한다"다: 이 타일을
 * 세로로 늘렸을 때만 게이지가 90px 에 멈춰 아래가 텅 비는 것은 패널 차트와 **똑같은 결함**이고,
 * 한 화면 안에서 어떤 칸은 따라 크고 어떤 칸은 안 크면 사람은 그 차이를 규칙으로 배우지 못한다.
 *
 * 늘어나도 게이지가 부풀지는 않는다 — echarts 의 gauge `radius` 백분율은 min(폭,높이) 기준이라
 * 타일이 세로로만 길어지면 반지름은 폭에 묶인 채고, 원이 아래로 늘어져 찌그러지지 않는다.
 * (`options.ts` 의 gaugeOption: radius '100%', center ['50%','62%'].)
 */
function GaugeTile({ tone, label, value, color }: { tone: string; label: string; value: number; color: string }) {
  return (
    <div className={`gstat ${tone} gstat-gauge`}>
      <span className="gstat-k">{label}</span>
      <EChart option={gaugeOption(value, color)} height={90} />
    </div>
  );
}

/*
 * ── 편집 툴바 ────────────────────────────────────────────────────────────
 *
 * 평소에는 읽기 전용이다. 대시보드는 **보러 오는 화면**이라 상시 드래그 가능하면 스크롤하다
 * 패널을 잘못 옮기고, 그걸 되돌리는 방법을 사람은 모른다. "편집"을 눌러야 열린다.
 *
 * 저장 상태는 항상 렌더한다(편집을 끈 뒤에 PUT 이 실패할 수 있다 — 그때 알릴 자리가 없어지면
 * 사람은 저장된 줄 안다). role="status" 라 화면을 못 봐도 결과가 들린다.
 *
 * ⚠ 이 컴포넌트는 **모듈 스코프에 있어야 한다.** 렌더 함수 안에서 선언하면 매 렌더마다 새
 * 타입이 되고 React 가 서브트리를 리마운트한다(react-hooks/static-components).
 */
const STATUS_TEXT: Record<SaveStatus, string> = {
  idle: '',
  saving: '저장 중…',
  saved: '배치 저장됨',
  // 저장이 안 된 것과 화면이 죽은 것은 다르다 — 무엇이 사실인지 그대로 말한다.
  error: '저장 실패 — 이 배치는 이 화면에만 남습니다',
};

function CanvasBar({
  editing, onToggle, onReset, status,
}: { editing: boolean; onToggle: () => void; onReset: () => void; status: SaveStatus }) {
  return (
    <div className="dc-bar">
      <span className="help">
        {editing
          ? '패널을 끌어 옮기고 우하단 모서리로 크기를 바꿉니다 · 키보드는 화살표(이동) · Shift+화살표(크기)'
          : '실시간 현황 · 비용 · 토큰 · 개발 지표 · 도구 분석이 한 캔버스에 있습니다'}
      </span>
      <span className="sp" />
      <span className={status === 'error' ? 'txt-err' : undefined} role="status" aria-live="polite">
        {STATUS_TEXT[status]}
      </span>
      {editing && (
        <button className="ghost" type="button" onClick={onReset}>기본 배치로 되돌리기</button>
      )}
      <button className="ghost" type="button" aria-pressed={editing} onClick={onToggle}>
        {editing ? '편집 완료' : '배치 편집'}
      </button>
    </div>
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
  /*
   * 사용자 선택 — 이 탭 안에서만 산다(사용 추적·사용 관측과 같은 규율).
   *
   * 이 축 하나로 **패널 전체**가 따라온다: 아래 패널들은 summary·seats·dev 에서 파생되므로
   * 그 세 조회에 user 를 실으면 비용·토큰·캐시·LOC 차트가 모두 그 사람 기준이 된다.
   * (커스텀 패널도 같은 summary 를 근거로 그린다 — 별도 배선이 필요 없다.)
   */
  const [user, setUser] = useState('');
  const platform = usePlatformFilter();

  /*
   * 배치 — 서버가 주인이다. `ready` 전에는 캔버스를 그리지 않는다: 저장된 배치가 도착하기 전에
   * 기본 배치를 한 프레임 그리면 패널이 혼자 움직인 것처럼 보인다(lib/layoutPrefs.ts 머리말).
   */
  const { layout, save, reset, status, ready: layoutReady } = useDashLayout();
  const [editing, setEditing] = useState(false);

  /* 명단은 **필터를 절대 싣지 않는** 조회로 받는다 — 걸린 응답으로 만들면 갈아탈 수 없다. */
  const rosterLoad = useCallback(
    ({ signal }: { signal: AbortSignal }) => softly(getLeaderboard({}, { signal }), null as Leaderboard | null),
    [],
  );
  const rosterRes = useResource(rosterLoad, []);
  const roster = rosterRes.state.status === 'ready'
    ? (rosterRes.state.data?.users ?? []).map((u) => u.username).filter((u): u is string => !!u)
    : [];

  const load = useCallback(async ({ signal }: { signal: AbortSignal }): Promise<Data> => {
    /*
     * 두 축을 함께 싣는다. platform 도 이 셋이 원래부터 받는다 — 예전 주석이 "이 패널은
     * 플랫폼 축으로 걸러지지 않는다"고 적어 두었지만 사실이 아니었다(2026-08-13 실측).
     */
    const scope = { platform: platform || undefined, user: user || undefined };
    const [summary, seats, dev, platforms] = await Promise.all([
      softly(getSummary(scope, { signal }), null as Summary | null),
      softly(getSeats(3650, { signal }, scope), null as Seats | null),
      softly(getDev(365, { signal }, scope), null as Dev | null),
      // 플랫폼 목록은 선택지의 원천이라 필터를 싣지 않는다(사용자 축도 마찬가지).
      softly(getPlatforms({ signal }), null as PlatformsResponse | null),
    ]);
    return { summary, seats, dev, platforms };
  }, [platform, user]);
  const { state, reload } = useResource(load, [platform, user]);

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

  // 저장된 배치를 아직 못 읽었으면 기다린다 — 기본 배치를 한 프레임 그렸다가 튀지 않게.
  if (state.status === 'loading' || !layoutReady) return <Loading />;
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

  // ── 패널 ──
  /*
   * 옛 섹션 단위로 묶어 둔다. 캔버스는 하나지만 **기본 배치**는 여전히 이 묶음 순서대로 위에서
   * 아래로 쌓이므로, 여기서 순서를 읽을 수 있어야 첫 화면을 눈으로 예측할 수 있다.
   *
   * `label` 은 스크린리더가 읽는 이름이다(CanvasGrid 가 "<이름> · 3열 2행 · 4칸 폭 · 2행" 으로
   * 읽어 준다). 안 주면 `live-cost` 같은 id 를 소리로 듣게 된다.
   *
   * 라벨은 한국어로 통일한다. 비용 타일만 한글(COST_LABEL)이고 나머지가 영어면, 한 행 안에서
   * 두 언어가 섞여 사람은 그 차이를 **의미의 차이**로 읽는다("한글 타일만 우리가 계산한 값인가?").
   * 여기서 바꾸는 것은 표시 문자열뿐이다 — 패널 id(저장된 레이아웃의 키)는 건드리지 않는다.
   */
  const live: CanvasItem[] = [
    { id: 'live-sessions', label: '활성 세션', defaultBox: { x: 0, y: ROW.live, w: 2, h: 2 },
      node: <StatTile tone="t-teal" k="활성 세션" v={fmtInt(t.sessions)} s={`사용자 ${t.users} · 머신 ${t.machines}`} /> },
    /*
     * 'Total Cost' 였던 자리. 그 이름은 청구액으로 읽힌다 — 우리 값은 환산 추정치다(lib/costLabels.ts).
     * 부제의 '90-day' 도 사실이 아니었다: 이 타일은 getSeats(3650) 의 합계라 전체 기간이다.
     */
    { id: 'live-cost', label: COST_LABEL, defaultBox: { x: 2, y: ROW.live, w: 2, h: 2 }, node: (
      <StatTile
        tone="t-blue"
        k={COST_LABEL}
        v={cost == null ? '—' : usd(cost)}
        s="전체 기간 · 실제 청구액 아님"
        title={`${COST_DISCLAIMER}. ${COST_WHY}`}
      />
    ) },
    { id: 'live-tokens', label: '전체 토큰', defaultBox: { x: 4, y: ROW.live, w: 2, h: 2 },
      node: <StatTile tone="t-orange" k="전체 토큰" v={short(tokensTotal)} s={`캐시읽기 ${short(t.cacheRead)}`} /> },
    { id: 'live-output', label: '출력 토큰', defaultBox: { x: 6, y: ROW.live, w: 2, h: 2 },
      node: <StatTile tone="t-purple" k="출력 토큰" v={short(t.output)} s={`입력 ${short(t.input)}`} /> },
    { id: 'live-hit', label: '캐시 적중률', defaultBox: { x: 8, y: ROW.live, w: 2, h: 2 },
      node: <GaugeTile tone="t-teal" label="캐시 적중률" value={cacheHit} color="#73bf69" /> },
    { id: 'live-share', label: '캐시읽기 비중', defaultBox: { x: 10, y: ROW.live, w: 2, h: 2 },
      node: <GaugeTile tone="t-blue" label="캐시읽기 비중" value={cacheReadShare} color="#5794f2" /> },
  ];

  const cost2: CanvasItem[] = [
    { id: 'cost-models', label: '모델별 토큰 분포', defaultBox: { x: 0, y: ROW.cost, w: 5, h: 6 },
      node: <Panel title="모델별 토큰 분포"><BarTable rows={modelRows} unit="토큰" fmt={short} /></Panel> },
    { id: 'cost-rate', label: '일별 토큰 추이', defaultBox: { x: 5, y: ROW.cost, w: 7, h: 6 },
      node: <Panel title="일별 토큰 추이"><EChart option={areaOption(x, [{ name: '토큰', color: '#73bf69', data: days.map((d) => d.input + d.output + d.cacheRead + d.cacheCreate) }], short)} height={180} /></Panel> },
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
  const dev2: CanvasItem[] = [
    { id: 'dev-loc', label: '일별 LOC', defaultBox: { x: 0, y: ROW.dev, w: 4, h: 4 }, node: (
      <Panel title="일별 LOC (추가 · 삭제)">
        {hasSeriesValues(locSeries)
          ? <EChart option={areaOption(devX, locSeries, fmtInt)} height={180} />
          : <NoData height={180} why="이 기간에 보고된 LOC 변경이 없습니다. 구버전 수집기이거나 아직 보고가 없는 팀입니다 — 0 줄을 썼다는 뜻이 아닙니다." />}
      </Panel>
    ) },
    { id: 'dev-edit', label: '코드 편집 결정', defaultBox: { x: 4, y: ROW.dev, w: 4, h: 4 }, node: (
      <DonutPanel
        title="코드 편집 결정 (수락 · 거부)"
        rows={[
          { name: '수락', value: dt?.editsAccepted ?? 0 },
          { name: '거부', value: dt?.editsRejected ?? 0 },
        ]}
        why="이 기간에 보고된 편집 수락·거부가 없습니다. 구버전 수집기이거나 아직 보고가 없는 팀입니다 — 0 건이라는 뜻이 아닙니다."
      />
    ) },
    { id: 'dev-io', label: '토큰 입출력 추이', defaultBox: { x: 8, y: ROW.dev, w: 4, h: 4 },
      node: <Panel title="토큰 입출력 추이"><EChart option={areaOption(x, [{ name: '입력', color: '#5794f2', data: days.map((d) => d.input) }, { name: '출력', color: '#73bf69', data: days.map((d) => d.output) }], short)} height={180} /></Panel> },
  ];

  const cache: CanvasItem[] = [
    { id: 'cache-usage', label: '일별 캐시 읽기 · 생성', defaultBox: { x: 0, y: ROW.cache, w: 12, h: 5 },
      node: <Panel title="일별 캐시 읽기 · 생성"><EChart option={areaOption(x, [{ name: '캐시읽기', color: '#73bf69', data: days.map((d) => d.cacheRead) }, { name: '캐시생성', color: '#5794f2', data: days.map((d) => d.cacheCreate) }], short)} height={200} /></Panel> },
  ];

  /* 세 도넛도 편집 결정과 같은 경로다 — 비면 빈 회색 링 대신 무엇이 없는지 말한다. */
  const notReported = (what: string) =>
    `이 기간에 보고된 ${what} 기록이 없습니다. 구버전 수집기이거나 이 축을 기록하지 않는 플랫폼입니다.`;
  const tools: CanvasItem[] = [
    { id: 'tool-usage', label: '도구 사용', defaultBox: { x: 0, y: ROW.tools, w: 4, h: 4 },
      node: <DonutPanel title="도구 사용" rows={donutRows(summary.top?.tool).slice(0, 10)} why={notReported('내장 도구')} /> },
    { id: 'tool-agents', label: '서브에이전트', defaultBox: { x: 4, y: ROW.tools, w: 4, h: 4 },
      node: <DonutPanel title="서브에이전트" rows={donutRows(summary.top?.agent)} why={notReported('서브에이전트')} /> },
    { id: 'tool-skills', label: '스킬', defaultBox: { x: 8, y: ROW.tools, w: 4, h: 4 },
      node: <DonutPanel title="스킬" rows={donutRows((summary.top?.skill ?? []).map((k) => ({ key: k.key.replace(/^superpowers:/, ''), count: k.count })))} why={notReported('스킬')} /> },
  ];

  const rates: CanvasItem[] = [
    { id: 'rate-sessions', label: '일별 세션 수', defaultBox: { x: 0, y: ROW.rates, w: 4, h: 6 },
      node: <Panel title="일별 세션 수"><EChart option={areaOption(x, [{ name: '세션', color: '#73bf69', data: days.map((d) => d.sessions) }], fmtInt)} height={170} /></Panel> },
    { id: 'rate-bash', label: '개발 명령', defaultBox: { x: 4, y: ROW.rates, w: 4, h: 6 },
      node: <Panel title="개발 명령"><BarTable rows={(summary.top?.bash ?? []).slice(0, 10).map((k) => ({ label: k.key, value: k.count }))} unit="횟수" fmt={fmtInt} /></Panel> },
    { id: 'rate-mcp', label: 'MCP 호출', defaultBox: { x: 8, y: ROW.rates, w: 4, h: 6 },
      node: <Panel title="MCP 호출"><BarTable rows={(summary.top?.mcp ?? []).slice(0, 10).map((k) => ({ label: k.key.replace(/^mcp__/, '').slice(0, 24), value: k.count }))} unit="횟수" fmt={fmtInt} /></Panel> },
  ];

  const topTools = (summary.top?.tool ?? []).slice(0, 10).map((k) => ({ name: k.key, value: k.count }));
  const top: CanvasItem[] = [
    { id: 'top-tools', label: '상위 도구', defaultBox: { x: 0, y: ROW.top, w: 12, h: 7 },
      node: <Panel title="상위 도구 (호출 수)"><EChart option={barOption(topTools, fmtInt)} height={Math.max(120, topTools.length * 30)} /></Panel> },
  ];

  /*
   * '내 그래프' — 사용자가 만든 커스텀 패널. 각 패널에 삭제 버튼(편집 모드에서도 눌린다:
   * CanvasGrid 는 button 에서 시작한 포인터를 드래그로 뺏지 않는다).
   *
   * 기본 자리는 **맨 아래**다(ROW.custom). 위에 두면 새로 만든 패널이 이미 저장된 배치의
   * 패널들과 자리를 다투고, 사람은 그래프 하나를 추가했을 뿐인데 화면 전체가 흔들리는 것을 본다.
   */
  const customItems: CanvasItem[] = customPanels.map((p, i) => ({
    id: p.id,
    label: p.title,
    defaultBox: { x: (i % 2) * 6, y: ROW.custom + Math.floor(i / 2), w: 6, h: 5 },
    node: (
      <div className="gpanel-card">
        <div className="gpanel-head">
          {p.title}
          <span style={{ flex: 1 }} />
          <button className="panel-x" type="button" title="삭제" onClick={() => removePanel(p.id)}>✕</button>
        </div>
        <div className="gpanel-body"><CustomPanelView panel={p} /></div>
      </div>
    ),
  }));

  const items: CanvasItem[] = [...live, ...cost2, ...dev2, ...cache, ...tools, ...rates, ...top, ...customItems];

  return (
    <div className="gdash">
      {headSlot && createPortal(
        <button className="primary" type="button" onClick={() => { setPrefill(undefined); setBuilderOpen(true); }}>＋ 그래프 추가</button>,
        headSlot,
      )}

      {/*
        * 플랫폼 선택 — applies 를 true 로 바꿨다(2026-08-13). 예전에는 false 로 두고 "이 패널은
        * 플랫폼 축으로 걸러지지 않는다"고 말했는데, 서버는 summary·seats·dev 모두 platform 을
        * 받는다 — 화면이 안 싣고 있었을 뿐이다. 이제 싣는다.
        */}
      <PlatformFilter rows={platformRows} applies what="아래 패널은 이 플랫폼만 집계합니다" />
      <UserFilter users={roster} value={user} onChange={setUser} />

      {/*
        * 플랫폼 롤업은 캔버스 **밖**에 남긴다. 옛 화면에서도 이것만은 드래그 대상이 아니었고
        * (그리드 밖이라 패널 id 가 없다), 조회 범위를 고르는 필터 바로 아래에서 "지금 무엇을
        * 보고 있는가"를 말하는 머리글이다. 캔버스에 넣으면 그 맥락이 아무 데로나 끌려간다.
        */}
      <section className="gsect">
        <div className="gsect-h"><span className="caret">▾</span> 플랫폼</div>
        <PlatformSummary rows={platformRows} />
      </section>

      <CanvasBar
        editing={editing}
        onToggle={() => setEditing((v) => !v)}
        onReset={reset}
        status={status}
      />
      {/*
        * 자리를 바꾸면 save 가 로컬 배치를 즉시 갈아끼우고 PUT 은 디바운스로 뒤따른다.
        * CanvasGrid 는 controlled 라 여기서 layout 을 갱신하지 않으면 놓는 순간 제자리로 튄다.
        */}
      <CanvasGrid items={items} layout={layout} editable={editing} onLayoutChange={save} />

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
