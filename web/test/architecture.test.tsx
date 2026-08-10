import { describe, it, expect, beforeEach } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import Dashboard from '@/components/Dashboard';
import Architecture from '@/components/Architecture';
import { METRIC_LABEL, METRICS, SUPPORT_LABEL, supportOf } from '@/lib/platforms';
import { authRoutes, mockFetch, obsRoutes, trackRoutes } from './helpers';

/*
 * 아키텍처(작동 방식) 탭 — 관리자 전용, 정적 설명 페이지(데이터 fetch 없음).
 *
 * 검증 둘: (1) 관리자에게 탭이 노출되고 열면 구성도·단계 설명이 렌더된다,
 *          (2) member 에게는 목록에서 숨고 딥링크(#/architecture)로도 열리지 않는다(이중 방어).
 */

const ADMIN = { username: 'admin', role: 'admin', tenant: 'acme' };
const MEMBER = { username: 'bob', role: 'member', tenant: 'acme' };

beforeEach(() => {
  location.hash = '';
});

describe('아키텍처 탭 — 셸 노출/숨김', () => {
  it('admin 에게는 아키텍처 탭이 보이고, 열면 구성도와 6단계 설명이 렌더된다', async () => {
    location.hash = '#/architecture'; // 딥링크로 바로 이 탭을 연다(overview fetch 회피)
    mockFetch([...authRoutes(ADMIN), ...trackRoutes(), ...obsRoutes()]);
    const user = userEvent.setup();
    render(<Dashboard />);

    // 탭이 목록에 있고 선택돼 있다.
    const tab = await screen.findByRole('tab', { name: '아키텍처' });
    expect(tab).toBeInTheDocument();
    expect(tab).toHaveAttribute('aria-selected', 'true');

    // 구성도(하나의 그림)와 단계 설명이 그려진다.
    expect(screen.getByRole('heading', { name: '수집 · 연동 · 수신 구성도' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: '데이터 흐름 — 6단계' })).toBeInTheDocument();
    expect(screen.getByRole('img', { name: /데이터 흐름 구성도/ })).toBeInTheDocument();

    // 클릭으로도 열린다(overview → architecture).
    location.hash = '#/overview';
    await user.click(tab);
    expect(screen.getByRole('heading', { name: '데이터 흐름 — 6단계' })).toBeInTheDocument();
  });

  it('member 에게는 아키텍처 탭이 노출되지 않는다', async () => {
    mockFetch([...authRoutes(MEMBER), ...trackRoutes(), ...obsRoutes()]);
    render(<Dashboard />);
    await screen.findByRole('tab', { name: '대시보드' });
    expect(screen.queryByRole('tab', { name: '아키텍처' })).not.toBeInTheDocument();
  });

  it('member 는 딥링크 #/architecture 로도 아키텍처 화면을 열 수 없다 (이중 방어)', async () => {
    location.hash = '#/architecture';
    mockFetch([...authRoutes(MEMBER), ...trackRoutes(), ...obsRoutes()]);
    render(<Dashboard />);
    await screen.findByRole('tab', { name: '대시보드' });
    // 렌더에서도 막히므로 구성도가 그려지지 않는다.
    expect(screen.queryByRole('heading', { name: '수집 · 연동 · 수신 구성도' })).not.toBeInTheDocument();
    expect(screen.queryByRole('img', { name: /데이터 흐름 구성도/ })).not.toBeInTheDocument();
  });
});

describe('아키텍처 — 정적 사실/접근성', () => {
  it('핵심 데이터 정책 5가지를 배지로 강조한다', () => {
    render(<Architecture />);
    const policy = within(screen.getByRole('list', { name: '핵심 데이터 정책' }));
    expect(policy.getByText('집계만 수집')).toBeInTheDocument();
    expect(policy.getByText('아웃바운드 HTTPS')).toBeInTheDocument();
    expect(policy.getByText('RLS 조직 격리')).toBeInTheDocument();
    expect(policy.getByText('키 해시 저장')).toBeInTheDocument();
    // 다섯 번째 — 빈칸을 버그로 읽지 않게 하는 유일한 문장이라 배지에서 사라지면 안 된다.
    expect(policy.getByText('플랫폼마다 수집 범위가 다르다')).toBeInTheDocument();
  });

  it('구성도는 하나의 그림(role=img)으로 텍스트 대체를 제공한다', () => {
    render(<Architecture />);
    const img = screen.getByRole('img', { name: /데이터 흐름 구성도/ });
    expect(img.getAttribute('aria-label')).toMatch(/RLS/);
    expect(img.getAttribute('aria-label')).toMatch(/아웃바운드 HTTPS/);
    // 도식 대체 텍스트도 단일 경로가 아니라 두 갈래를 말해야 한다(도식만 고치고 alt 를 두면 갈라진다).
    expect(img.getAttribute('aria-label')).toMatch(/파일 기반 수집/);
    expect(img.getAttribute('aria-label')).toMatch(/런타임 캡처/);
    expect(img.getAttribute('aria-label')).toMatch(/Antigravity/);
  });
});

/*
 * ── 멀티플랫폼 표기 ───────────────────────────────────────────────────────
 *
 * 이 화면이 한동안 Claude 단일 경로만 그렸다. 도식이 현실보다 좁으면 그 차이는 사용자의
 * 오해로 정산된다("Antigravity 도구 칸이 왜 비었나 — 버그인가"). 그래서 **4개 플랫폼 ·
 * 2가지 수집 방식**이 실제로 렌더되는지를 여기서 못박는다.
 */
describe('아키텍처 — 4개 플랫폼 · 2가지 수집 방식', () => {
  /* 도식 안에서 찾는다 — 같은 사실이 아래 6단계 설명에도 있어서(그게 정본이다) 전역 조회는 중복이다. */
  const diagram = () => within(screen.getByRole('img', { name: /데이터 흐름 구성도/ }));

  it('파일 기반 수집 3종의 이름과 읽는 경로를 그린다', () => {
    render(<Architecture />);
    const d = diagram();
    expect(d.getByText(/파일 기반 수집/)).toBeInTheDocument();
    expect(d.getByText('~/.claude/projects/**/*.jsonl')).toBeInTheDocument();
    expect(d.getByText('~/.codex/sessions/**/*.jsonl')).toBeInTheDocument();
    expect(d.getByText('~/.gemini/tmp/<slug>/chats/*.jsonl')).toBeInTheDocument();
    // Claude 만 훅 이름이 트리거로 확정돼 있다.
    expect(d.getByText('SessionEnd 훅이 트리거')).toBeInTheDocument();
  });

  it('런타임 캡처 1종은 statusLine → 로컬 스풀 → Stop 훅 플러시로 그린다', () => {
    render(<Architecture />);
    const d = diagram();
    expect(d.getByText(/런타임 캡처/)).toBeInTheDocument();
    expect(d.getByText('statusLine → 로컬 스풀')).toBeInTheDocument();
    expect(d.getByText('Stop 훅이 서버로 플러시')).toBeInTheDocument();
  });

  it('네 플랫폼 이름이 모두 나온다 (수집 범위 표의 열 머리 포함)', () => {
    render(<Architecture />);
    for (const label of ['Claude', 'Codex', 'Gemini', 'Antigravity']) {
      expect(screen.getByRole('columnheader', { name: label })).toBeInTheDocument();
    }
  });

  /*
   * 수집 범위는 **파생이어야 한다.** 여기서 값을 손으로 적어 두면 이 테스트는 화면이 아니라
   * 사본을 지키게 되고, lib/platforms.ts 가 바뀐 날 초록불인 채로 갈라진다. 그래서 기대값을
   * supportOf() 에서 만들어 전 칸을 대조한다 — 화면이 지원표를 그대로 옮기는지만 본다.
   */
  it('수집 범위 표의 모든 칸이 lib/platforms.ts 의 판정과 일치한다', () => {
    render(<Architecture />);
    const platforms = ['claude', 'codex', 'gemini', 'antigravity'] as const;

    for (const metric of METRICS) {
      const head = screen.getByRole('rowheader', { name: METRIC_LABEL[metric] });
      const cells = within(head.closest('tr')!).getAllByRole('cell');
      expect(cells).toHaveLength(platforms.length);
      platforms.forEach((p, i) => {
        expect(cells[i]!.textContent?.trim()).toBe(SUPPORT_LABEL[supportOf(p, metric).state]);
      });
    }
  });

  it('플랫폼별 차이를 눈으로 확인할 수 있다 — Antigravity 행동 축은 미수집, 캐시생성은 해당 없음', () => {
    render(<Architecture />);
    const cellsOf = (label: string) =>
      within(screen.getByRole('rowheader', { name: label }).closest('tr')!)
        .getAllByRole('cell').map((td) => td.textContent?.trim());

    // 열 순서: Claude · Codex · Gemini · Antigravity
    expect(cellsOf(METRIC_LABEL.mcp)?.[3]).toBe('미수집');
    expect(cellsOf(METRIC_LABEL.loc)?.[3]).toBe('미수집');
    expect(cellsOf(METRIC_LABEL.cacheCreate)?.[3]).toBe('해당 없음');
    // Antigravity 의 슬래시·키워드는 조건부다 — 미수집으로 적으면 올라온 값을 숨긴다.
    expect(cellsOf(METRIC_LABEL.slash)?.[3]).toBe('조건부 수집');
    expect(cellsOf(METRIC_LABEL.keyword)?.[3]).toBe('조건부 수집');
    // Codex: 미수집은 슬래시 하나뿐이고, 스킬·서브에이전트는 수집기가 보낸다.
    expect(cellsOf(METRIC_LABEL.slash)?.[1]).toBe('미수집');
    expect(cellsOf(METRIC_LABEL.skill)?.[1]).toBe('수집됨');
    expect(cellsOf(METRIC_LABEL.agent)?.[1]).toBe('수집됨');
    expect(cellsOf(METRIC_LABEL.cacheCreate)?.[1]).toBe('해당 없음');
    // Gemini 수집기는 이미 있다 — 이 탭이 '준비 중'이라고 말하면 안 된다.
    expect(cellsOf(METRIC_LABEL.tool)?.[2]).toBe('수집됨');
    expect(cellsOf(METRIC_LABEL.skill)?.[2]).toBe('수집됨');
    expect(cellsOf(METRIC_LABEL.slash)?.[2]).toBe('미수집');
    expect(screen.queryByText('준비 중')).not.toBeInTheDocument();
    // Claude 는 행동 축까지 전부 수집된다.
    expect(cellsOf(METRIC_LABEL.mcp)?.[0]).toBe('수집됨');
    expect(cellsOf(METRIC_LABEL.loc)?.[0]).toBe('수집됨');
  });
});
