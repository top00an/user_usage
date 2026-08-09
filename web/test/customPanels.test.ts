/** @vitest-environment jsdom */
import { describe, it, expect, beforeEach } from 'vitest';
import { addPanel, listPanels, removePanel, setBuilderPrefill, takeBuilderPrefill } from '@/lib/customPanels';

beforeEach(() => localStorage.clear());

describe('lib/customPanels — 커스텀 패널 저장소', () => {
  it('추가·목록·삭제', () => {
    expect(listPanels()).toHaveLength(0);
    addPanel({ title: '모델별 토큰', metric: 'tokens', type: 'bar', groupBy: 'model', days: 30 });
    addPanel({ title: '비용 추이', metric: 'cost', type: 'line', groupBy: 'none', days: 90 });
    const ps = listPanels();
    expect(ps).toHaveLength(2);
    expect(ps[0]!.title).toBe('모델별 토큰');
    expect(ps[0]!.id).toBeTruthy();

    removePanel(ps[0]!.id);
    const after = listPanels();
    expect(after).toHaveLength(1);
    expect(after[0]!.title).toBe('비용 추이');
  });

  it('빌더 프리필은 한 번만 소비된다', () => {
    setBuilderPrefill({ metric: 'cost', groupBy: 'user', type: 'bar' });
    const first = takeBuilderPrefill();
    expect(first?.metric).toBe('cost');
    expect(first?.groupBy).toBe('user');
    // 소비 후엔 비어 있다(대시보드가 매 마운트마다 다시 열지 않게).
    expect(takeBuilderPrefill()).toBeNull();
  });

  it('깨진 저장값은 빈 목록으로 방어한다', () => {
    localStorage.setItem('ccdash-custom-panels-v1', '{not json');
    expect(listPanels()).toEqual([]);
  });
});
