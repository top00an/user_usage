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
import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from 'react';
import { createPortal } from 'react-dom';
import { getSummary, getSeats, getDev, getPlatforms, getLeaderboard } from '@/lib/api';
import { softly, useResource } from '@/hooks/useResource';
import type { Dev, Leaderboard, PlatformsResponse, Seats, Summary } from '@/lib/types';
import { Empty, ErrorState, Loading } from '@/components/ui';
import PlatformFilter from '@/components/platform/PlatformFilter';
import RuntimeFilter from '@/components/platform/RuntimeFilter';
import UserFilter from '@/components/usagetrack/UserFilter';
import PlatformSummary from '@/components/platform/PlatformSummary';
import { useScope } from '@/lib/scope';
import { COST_DISCLAIMER, COST_LABEL, COST_WHY } from '@/lib/costLabels';
import EChart from '@/components/charts/EChart';
import CanvasGrid, { type CanvasItem } from './CanvasGrid';
import { useDashLayout, type SaveStatus } from '@/lib/layoutPrefs';
import { PALETTE, areaOption, gaugeOption, donutOption, barOption, hasValues, hasSeriesValues, short, fmtInt } from './options';
import ChartBuilder from './ChartBuilder';
import CustomPanelView from './CustomPanelView';
import {
  removePanel, insertPanel, subscribePanels, panelsSnapshot, takeBuilderPrefill,
  type CustomPanel,
} from '@/lib/customPanels';
import { compactLayout, resolveLayout, overlappingIds, type DashLayout } from '@/lib/dashLayout';

interface Data {
  summary: Summary | null;
  seats: Seats | null;
  dev: Dev | null;
  /** 플랫폼 롤업. 관리자 전용이라 member 스코프에서는 null 이다(카드가 스스로 안내한다). */
  platforms: PlatformsResponse | null;
}

const usd = (n: number) => '$' + n.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 });

/*
 * ── 계열 색은 인덱스가 아니라 이름으로 부른다 ────────────────────────────
 *
 * 이 파일에는 `options.ts` 의 `PALETTE` 에서 뽑은 색이 **10곳 인라인 문자열**로 박혀 있었다
 * (`#73bf69` · `#5794f2` · `#e0523e`). 팔레트를 한 벌 더 들고 있는 셈이라, 팔레트를 고쳐도
 * 이 열 자리는 옛 색으로 남는다.
 *
 * 그렇다고 `PALETTE[2]` 로만 바꾸면 더 나쁜 쪽으로 조용해진다: 다음 사람이 팔레트 순서를 한 칸
 * 옮기는 순간 '추가'가 빨강, '삭제'가 초록이 된다 — 화면은 멀쩡히 그려지고 **뜻만 뒤집힌다**.
 * 색이 뜻을 지는 자리에서는 인덱스가 아니라 이름이 계약이다.
 *
 * 짝을 이루는 규칙: 초록은 **나온 것·아낀 것**(출력·캐시읽기·추가된 줄), 파랑은 **들어간 것·
 * 새로 만든 것**(입력·캐시생성), 빨강은 **줄어든 것**(삭제된 줄) 하나뿐이다.
 * 색만으로 뜻을 전달하지는 않는다 — 모든 계열에 범례 이름이 함께 있다(options.ts 의 legend).
 */
const SERIES = {
  added: PALETTE[2]!,        // 추가된 줄
  removed: PALETTE[6]!,      // 삭제된 줄
  input: PALETTE[0]!,        // 입력 토큰
  output: PALETTE[2]!,       // 출력 토큰
  cacheRead: PALETTE[2]!,    // 캐시읽기 — 아낀 쪽
  cacheCreate: PALETTE[0]!,  // 캐시생성 — 새로 쓴 쪽
  tokens: PALETTE[2]!,       // 토큰 합계(단일 계열)
  sessions: PALETTE[2]!,     // 세션 수(단일 계열)
} as const;

/*
 * 게이지 호(arc) 색은 **계열이 아니라 타일 장식**이다 — 옆의 스탯 타일 톤(t-teal · t-blue)과
 * 맞추는 것이 전부고, 어떤 값을 뜻하지 않는다(값은 게이지 안 숫자가 말한다). 그래서 SERIES 와
 * 섞지 않는다: `캐시읽기 비중` 게이지가 파랑인 것을 '캐시생성'으로 이름 붙이면 그 이름이 거짓말이 된다.
 */
const GAUGE_ARC = { teal: PALETTE[2]!, blue: PALETTE[0]! } as const;

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
      {/* 차트의 접근 이름은 패널 제목이다 — 안 주면 도넛 네 개가 전부 "차트, 이미지"로 들린다. */}
      {hasValues(rows)
        ? <EChart option={donutOption(rows)} height={height} label={title} />
        : <NoData height={height} why={why} />}
    </Panel>
  );
}

function StatTile({ tone, k, v, s, title }: { tone: string; k: string; v: string; s?: string; title?: string }) {
  return (
    <div className={`gstat ${tone}`} title={title}>
      <span className="gstat-k">{k}</span>
      {/* `num` 이 붙어 있었지만 CSS 의 그 규칙은 `td.num, th.num`(표 셀 우측 정렬)뿐이라 span 에는
          아무 효과가 없었다. 타일은 세로 flex 라 왼쪽 정렬이 의도한 모습이고(.gstat-k 와 줄이
          맞는다), 우측 정렬은 표에서만 뜻이 있다 — 그래서 지운다. */}
      <span className="gstat-v">{v}</span>
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
      {/* 타일 라벨이 곧 이 게이지의 이름이다(값은 canvas 안에 그려져 소리로는 안 읽힌다). */}
      <EChart option={gaugeOption(value, color)} height={90} label={label} />
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
/**
 * 되돌릴 수 있는 단계 수. 무제한으로 두면 화살표를 오래 누른 세션이 배치 수천 벌을 메모리에
 * 쌓는다. 50 은 "방금 망친 것"을 되짚기에 충분하고, 그보다 옛날 배치는 아무도 기억하지 못한다.
 */
const UNDO_LIMIT = 50;

/**
 * 되돌리기 한 칸. **바뀐 뒤가 아니라 바뀌기 전 상태**를 담는다 — 되돌리기는 "그때로 되돌려라"이지
 * "반대 동작을 해라"가 아니다(반대 동작으로 적으면 밀려난 이웃 패널까지는 못 돌려놓는다).
 */
type UndoEntry =
  | { kind: 'layout'; prev: DashLayout | null }
  | { kind: 'panel'; panel: CustomPanel; index: number };

const STATUS_TEXT: Record<SaveStatus, string> = {
  idle: '',
  saving: '저장 중…',
  saved: '배치 저장됨',
  // 저장이 안 된 것과 화면이 죽은 것은 다르다 — 무엇이 사실인지 그대로 말한다.
  error: '저장 실패 — 이 배치는 이 화면에만 남습니다',
};

function CanvasBar({
  editing, onToggle, onReset, onUndo, onCompact, canUndo, status, hidden,
}: {
  editing: boolean;
  onToggle: () => void;
  onReset: () => void;
  onUndo: () => void;
  onCompact: () => void;
  canUndo: boolean;
  status: SaveStatus;
  /** 서로 겹쳐 있는 패널 수. 0 이면 아무 말도 하지 않는다. */
  hidden: number;
}) {
  return (
    <div className="dc-bar">
      <span className="help">
        {editing
          /* 툴바 한 줄에 들어가야 한다 — 넘치면 버튼이 두 줄로 접히고, 그 줄바꿈이 화면을 흔든다. */
          ? '끌어 옮기고 우하단 모서리로 크기 조절 · 겹쳐도 됩니다(클릭하면 앞으로) · Esc 취소 · Ctrl+Z 되돌리기 · 화살표 이동 / Shift+화살표 크기 / Enter 앞으로'
          : '실시간 현황 · 비용 · 토큰 · 개발 지표 · 도구 분석이 한 캔버스에 있습니다'}
      </span>
      {/*
        * 겹침 안내 — 겹침을 허용한 대가다. 위 카드가 아래를 **완전히 가리므로**, 실수로 겹친
        * 사람에게 화면은 "패널이 사라졌다"로 보인다. 몇 장이 겹쳤는지 말해 주고, 편집 중이면
        * 옆의 "겹침·빈 줄 정리"가 한 번에 되돌린다. 겹침이 없으면 이 자리는 비어 있다.
        */}
      {hidden > 0 && (
        <span className="dc-warn" role="status">
          패널 {hidden}장이 겹쳐 있습니다{editing ? '' : ' — 배치 편집에서 정리할 수 있습니다'}
        </span>
      )}
      <span className="sp" />
      <span className={status === 'error' ? 'txt-err' : undefined} role="status" aria-live="polite">
        {STATUS_TEXT[status]}
      </span>
      {/*
        * 되돌리기는 **편집 중이 아니어도** 되돌릴 것이 있으면 보인다. 커스텀 패널의 ✕ 는 읽는
        * 화면에서도 눌리기 때문이다 — 지운 직후에 되돌릴 방법이 없으면 그 그래프 정의는 그대로
        * 사라진다. 스택에는 이 세션에서 **자기가 한 일**만 쌓이므로, 남의 배치를 되돌릴 위험은 없다.
        */}
      {(editing || canUndo) && (
        <button className="ghost" type="button" onClick={onUndo} disabled={!canUndo}>되돌리기 (Ctrl+Z)</button>
      )}
      {editing && (
        /*
         * 겹침·빈 줄 정리 — 옛날에 **자동으로** 돌던 그 계산이다. 자동일 때는 사고였지만(놓은
         * 자리에 안 놓인다), 눌러서 돌리면 도구다. 겹쳐서 가려진 패널을 한 번에 드러내는 길도
         * 이것뿐이다. 되돌리기 대상이라 눌러 보기 무섭지 않다.
         */
        <button className="ghost" type="button" onClick={onCompact}>겹침·빈 줄 정리</button>
      )}
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
  /* 여기 있던 8색 배열은 `options.ts` 의 PALETTE 와 문자 단위로 같았다 — 팔레트가 두 벌이면
     차트는 새 색, 이 막대표만 옛 색이 되는 날이 온다. 이 자리의 색은 순서일 뿐 뜻이 없다
     (n번째 행 = n번째 색)이라 인덱스로 돌려도 안전하다. */
  return (
    <table className="gtable">
      <thead><tr><th>이름</th><th className="r">{unit}</th></tr></thead>
      <tbody>
        {rows.map((r, i) => (
          <tr key={r.label}>
            <td className="gbarcell">
              <span className="gbar" style={{ width: `${(r.value / max) * 100}%`, background: PALETTE[i % PALETTE.length] }} />
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
  // 스코프 세 축을 한 곳에서 읽는다(lib/scope.ts) — 축을 하나 잊는 사고를 구조로 막는다.
  const scope = useScope(user || undefined);

  /*
   * 배치 — 서버가 주인이다. `ready` 전에는 캔버스를 그리지 않는다: 저장된 배치가 도착하기 전에
   * 기본 배치를 한 프레임 그리면 패널이 혼자 움직인 것처럼 보인다(lib/layoutPrefs.ts 머리말).
   */
  const { layout, save, reset, status, ready: layoutReady } = useDashLayout();
  const [editing, setEditing] = useState(false);
  /** 플랫폼 롤업 접기 — 캐럿이 있으니 실제로 접혀야 한다(아래 머리글 주석). */
  const [platformOpen, setPlatformOpen] = useState(true);

  /*
   * ── 되돌리기 한 벌 ──────────────────────────────────────────────────────
   *
   * 사람이 되돌리고 싶은 것은 "방금 한 일"이고, 그 일은 세 종류다: 배치 변경 · 기본 배치로
   * 되돌리기 · 커스텀 패널 삭제. 스택이 종류별로 따로 있으면 Ctrl+Z 가 **순서를 건너뛴다**
   * (패널을 지우고 패널을 옮긴 뒤 되돌렸는데 삭제가 먼저 살아나는 식). 그래서 한 벌이다.
   */
  const historyRef = useRef<UndoEntry[]>([]);
  const [canUndo, setCanUndo] = useState(false);
  const remember = useCallback((e: UndoEntry) => {
    const h = historyRef.current;
    h.push(e);
    if (h.length > UNDO_LIMIT) h.shift();   // 오래된 쪽부터 버린다.
    setCanUndo(true);
  }, []);

  const undo = useCallback(() => {
    const e = historyRef.current.pop();
    if (!e) return;                         // 되돌릴 것이 없다 — 조용히 아무 일도 하지 않는다.
    setCanUndo(historyRef.current.length > 0);
    if (e.kind === 'panel') { insertPanel(e.panel, e.index); return; }
    // null 은 그 시점이 "저장된 것 없음"이었다는 뜻이다 — 기본 배치로 돌아간다(DELETE).
    if (e.prev === null) reset(); else save(e.prev);
  }, [reset, save]);

  /*
   * Ctrl+Z(맥은 ⌘Z). 스택에는 이 세션에서 자기가 한 일만 쌓이므로 편집 모드로 가두지 않는다 —
   * 읽는 화면에서 지운 커스텀 패널도 여기로 돌아온다. 되돌릴 것이 없으면 아무 일도 없다.
   *
   * 입력 요소 안에서는 양보한다 — 그래프 추가 모달의 제목을 고치다 누른 Ctrl+Z 는 그 글자를
   * 되돌리라는 뜻이지 대시보드 배치를 되돌리라는 뜻이 아니다.
   */
  const undoRef = useRef(undo);
  useEffect(() => { undoRef.current = undo; });
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'z' && e.key !== 'Z') return;
      if (!(e.ctrlKey || e.metaKey) || e.altKey || e.shiftKey) return;
      const t = e.target;
      if (t instanceof HTMLElement
        && (t.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(t.tagName))) return;
      e.preventDefault();
      undoRef.current();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  /* 명단은 **필터를 절대 싣지 않는** 조회로 받는다 — 걸린 응답으로 만들면 갈아탈 수 없다. */
  const rosterLoad = useCallback(
    ({ signal }: { signal: AbortSignal }) => softly(getLeaderboard({}, { signal }), null as Leaderboard | null),
    [],
  );
  const rosterRes = useResource(rosterLoad, []);
  const roster = rosterRes.state.status === 'ready'
    ? (rosterRes.state.data?.users ?? []).map((u) => u.username).filter((u): u is string => !!u)
    : [];

  /*
   * 의존성으로 쓸 **원시값**을 먼저 뽑는다 — useResource 는 deps 를 문자열로 이어 키를
   * 만들므로 객체를 넣으면 키가 굳어 재조회가 안 된다(lib/scope.ts 의 경고).
   */
  const { platform, runtime } = scope;

  const load = useCallback(async ({ signal }: { signal: AbortSignal }): Promise<Data> => {
    /*
     * 스코프 세 축(platform · runtime · user)을 함께 싣는다. platform 도 이 셋이 원래부터
     * 받는다 — 예전 주석이 "이 패널은 플랫폼 축으로 걸러지지 않는다"고 적어 두었지만
     * 사실이 아니었다(2026-08-13 실측).
     */
    const s = { platform, runtime, user: user || undefined };
    const [summary, seats, dev, platforms] = await Promise.all([
      softly(getSummary(s, { signal }), null as Summary | null),
      softly(getSeats(3650, { signal }, s), null as Seats | null),
      softly(getDev(365, { signal }, s), null as Dev | null),
      // 플랫폼 목록은 선택지의 원천이라 필터를 싣지 않는다(사용자 축도 마찬가지).
      softly(getPlatforms({ signal }), null as PlatformsResponse | null),
    ]);
    return { summary, seats, dev, platforms };
  }, [platform, runtime, user]);
  const { state, reload } = useResource(load, [platform, runtime, user]);

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
    /* `.hint` 는 이 저장소에 **없는 클래스**였다 — 규칙이 없으니 이 안내가 본문 크기·기본 색으로
       렌더돼, 보조 문구로 쓰려던 자리가 빈 화면에서 가장 큰 글자 중 하나가 됐다. 보조 문구의
       이름은 `.help` 다(globals.css: --fg-faint / .78rem). */
    return <p className="help" style={{ padding: '24px 0' }}>이 대시보드는 관리자 토큰이 필요합니다(전사 지표). 개인 열람 토큰은 사용 관측 탭에서 자기 데이터를 봅니다.</p>;
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
      node: <GaugeTile tone="t-teal" label="캐시 적중률" value={cacheHit} color={GAUGE_ARC.teal} /> },
    { id: 'live-share', label: '캐시읽기 비중', defaultBox: { x: 10, y: ROW.live, w: 2, h: 2 },
      node: <GaugeTile tone="t-blue" label="캐시읽기 비중" value={cacheReadShare} color={GAUGE_ARC.blue} /> },
  ];

  const cost2: CanvasItem[] = [
    { id: 'cost-models', label: '모델별 토큰 분포', defaultBox: { x: 0, y: ROW.cost, w: 5, h: 6 },
      node: <Panel title="모델별 토큰 분포"><BarTable rows={modelRows} unit="토큰" fmt={short} /></Panel> },
    { id: 'cost-rate', label: '일별 토큰 추이', defaultBox: { x: 5, y: ROW.cost, w: 7, h: 6 },
      /* label 은 차트의 접근 이름 — Panel 제목과 **같은 문자열**이어야 한다(눈으로 보는 이름과
         소리로 듣는 이름이 갈리면 "이 차트"라고 서로 가리킬 수 없다). 아래 패널들도 같다. */
      node: <Panel title="일별 토큰 추이"><EChart option={areaOption(x, [{ name: '토큰', color: SERIES.tokens, data: days.map((d) => d.input + d.output + d.cacheRead + d.cacheCreate) }], short)} height={180} label="일별 토큰 추이" /></Panel> },
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
    { name: '추가', color: SERIES.added, data: devDays.map((d) => d.linesAdded) },
    { name: '삭제', color: SERIES.removed, data: devDays.map((d) => d.linesRemoved) },
  ];
  const dev2: CanvasItem[] = [
    { id: 'dev-loc', label: '일별 LOC', defaultBox: { x: 0, y: ROW.dev, w: 4, h: 4 }, node: (
      <Panel title="일별 LOC (추가 · 삭제)">
        {hasSeriesValues(locSeries)
          ? <EChart option={areaOption(devX, locSeries, fmtInt)} height={180} label="일별 LOC (추가 · 삭제)" />
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
      node: <Panel title="토큰 입출력 추이"><EChart option={areaOption(x, [{ name: '입력', color: SERIES.input, data: days.map((d) => d.input) }, { name: '출력', color: SERIES.output, data: days.map((d) => d.output) }], short)} height={180} label="토큰 입출력 추이" /></Panel> },
  ];

  const cache: CanvasItem[] = [
    { id: 'cache-usage', label: '일별 캐시 읽기 · 생성', defaultBox: { x: 0, y: ROW.cache, w: 12, h: 5 },
      node: <Panel title="일별 캐시 읽기 · 생성"><EChart option={areaOption(x, [{ name: '캐시읽기', color: SERIES.cacheRead, data: days.map((d) => d.cacheRead) }, { name: '캐시생성', color: SERIES.cacheCreate, data: days.map((d) => d.cacheCreate) }], short)} height={200} label="일별 캐시 읽기 · 생성" /></Panel> },
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
      node: <Panel title="일별 세션 수"><EChart option={areaOption(x, [{ name: '세션', color: SERIES.sessions, data: days.map((d) => d.sessions) }], fmtInt)} height={170} label="일별 세션 수" /></Panel> },
    { id: 'rate-bash', label: '개발 명령', defaultBox: { x: 4, y: ROW.rates, w: 4, h: 6 },
      node: <Panel title="개발 명령"><BarTable rows={(summary.top?.bash ?? []).slice(0, 10).map((k) => ({ label: k.key, value: k.count }))} unit="횟수" fmt={fmtInt} /></Panel> },
    { id: 'rate-mcp', label: 'MCP 호출', defaultBox: { x: 8, y: ROW.rates, w: 4, h: 6 },
      node: <Panel title="MCP 호출"><BarTable rows={(summary.top?.mcp ?? []).slice(0, 10).map((k) => ({ label: k.key.replace(/^mcp__/, '').slice(0, 24), value: k.count }))} unit="횟수" fmt={fmtInt} /></Panel> },
  ];

  const topTools = (summary.top?.tool ?? []).slice(0, 10).map((k) => ({ name: k.key, value: k.count }));
  const top: CanvasItem[] = [
    { id: 'top-tools', label: '상위 도구', defaultBox: { x: 0, y: ROW.top, w: 12, h: 7 },
      node: <Panel title="상위 도구 (호출 수)"><EChart option={barOption(topTools, fmtInt)} height={Math.max(120, topTools.length * 30)} label="상위 도구 (호출 수)" /></Panel> },
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
          {/* 제목과 ✕ 사이를 벌리는 빈 칸. `.gpanel-head` 안이라 붙일 클래스가 없어 인라인이었는데,
              이제 globals.css 에 전역 `.sp { flex:1 }` 이 있다 — 같은 뜻의 인라인을 남겨 두면
              다음 사람이 `.sp` 가 아니라 이걸 복사한다. */}
          <span className="sp" />
          {/* 지우기 전에 그 패널 정의와 순서를 히스토리에 넣는다 — Ctrl+Z 로 그대로 돌아온다. */}
          <button
            className="panel-x"
            type="button"
            title="삭제 (Ctrl+Z 로 되돌릴 수 있습니다)"
            onClick={() => { remember({ kind: 'panel', panel: p, index: i }); removePanel(p.id); }}
          >✕</button>
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
      {/* `.pf-bars` 로 감싸 한 줄에 세운다 — 감싸지 않으면 축마다 한 줄을 먹는다. */}
      <div className="pf-bars">
        <PlatformFilter rows={platformRows} applies what="아래 패널은 이 플랫폼만 집계합니다" />
        <RuntimeFilter />
        <UserFilter users={roster} value={user} onChange={setUser} />
      </div>

      {/*
        * 플랫폼 롤업은 캔버스 **밖**에 남긴다. 옛 화면에서도 이것만은 드래그 대상이 아니었고
        * (그리드 밖이라 패널 id 가 없다), 조회 범위를 고르는 필터 바로 아래에서 "지금 무엇을
        * 보고 있는가"를 말하는 머리글이다. 캔버스에 넣으면 그 맥락이 아무 데로나 끌려간다.
        */}
      {/*
        * 머리글은 **버튼**이다. 예전에는 ▾ 캐럿만 그려 놓고 아무 핸들러가 없었다 — 접을 수 있게
        * 생긴 것을 눌러도 아무 일이 없으면 사람은 화면이 고장 났다고 읽는다. 캐럿을 지우는 대신
        * 실제로 접히게 했다(플랫폼 표는 여덟 줄까지 길어져 실제로 접고 싶은 자리다).
        * 접힌 상태는 저장하지 않는다: 저장하면 첫 프레임에 펼친 표를 그린 뒤 접히는 튐이 생기고,
        * 그 튐은 이 화면이 가장 경계하는 증상이다(lib/layoutPrefs.ts 머리말).
        */}
      <section className="gsect">
        <button
          type="button"
          className="gsect-h"
          aria-expanded={platformOpen}
          onClick={() => setPlatformOpen((v) => !v)}
        >
          <span className={`caret${platformOpen ? '' : ' closed'}`} aria-hidden="true">▾</span> 플랫폼
        </button>
        {platformOpen && <PlatformSummary rows={platformRows} />}
      </section>

      <CanvasBar
        editing={editing}
        onToggle={() => setEditing((v) => !v)}
        onReset={() => { remember({ kind: 'layout', prev: layout }); reset(); }}
        onUndo={undo}
        /* 지금 화면의 배치를 그대로 위로 당긴다 — 저장된 적 없는 패널까지 포함해야 하므로 resolve 부터. */
        onCompact={() => {
          remember({ kind: 'layout', prev: layout });
          save(compactLayout(resolveLayout(layout, items)));
        }}
        canUndo={canUndo}
        status={status}
        hidden={overlappingIds(resolveLayout(layout, items)).length}
      />
      {/*
        * 자리를 바꾸면 save 가 로컬 배치를 즉시 갈아끼우고 PUT 은 디바운스로 뒤따른다.
        * CanvasGrid 는 controlled 라 여기서 layout 을 갱신하지 않으면 놓는 순간 제자리로 튄다.
        */}
      <CanvasGrid
        items={items}
        layout={layout}
        editable={editing}
        onLayoutChange={(next) => { remember({ kind: 'layout', prev: layout }); save(next); }}
      />

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
