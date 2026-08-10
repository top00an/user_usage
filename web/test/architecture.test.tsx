import { describe, it, expect, beforeEach } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import Dashboard from '@/components/Dashboard';
import Architecture from '@/components/Architecture';
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
  it('핵심 데이터 정책 4가지를 배지로 강조한다', () => {
    render(<Architecture />);
    const policy = within(screen.getByRole('list', { name: '핵심 데이터 정책' }));
    expect(policy.getByText('집계만 수집')).toBeInTheDocument();
    expect(policy.getByText('아웃바운드 HTTPS')).toBeInTheDocument();
    expect(policy.getByText('RLS 조직 격리')).toBeInTheDocument();
    expect(policy.getByText('키 해시 저장')).toBeInTheDocument();
  });

  it('구성도는 하나의 그림(role=img)으로 텍스트 대체를 제공한다', () => {
    render(<Architecture />);
    const img = screen.getByRole('img', { name: /데이터 흐름 구성도/ });
    expect(img.getAttribute('aria-label')).toMatch(/RLS/);
    expect(img.getAttribute('aria-label')).toMatch(/아웃바운드 HTTPS/);
  });
});
