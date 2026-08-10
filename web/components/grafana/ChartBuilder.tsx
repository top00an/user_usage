'use client';

/*
 * 차트 빌더 — 지표·차트종류·그룹기준·기간을 골라 대시보드에 커스텀 패널을 만든다.
 * 저장은 lib/customPanels(로컬). 대시보드의 '내 그래프' 섹션이 이 정의를 읽어 렌더한다.
 */
import { useState } from 'react';
import Modal from '@/components/Modal';
import { addPanel, type ChartType, type GroupBy, type Metric, type CustomPanel } from '@/lib/customPanels';
import { COST_LABEL_SHORT } from '@/lib/costLabels';

const METRICS: { v: Metric; label: string }[] = [
  // '비용'이 아니라 '환산 비용'이다 — 청구액과 다르다(lib/costLabels.ts).
  { v: 'tokens', label: '토큰' }, { v: 'cost', label: COST_LABEL_SHORT },
  { v: 'sessions', label: '세션' }, { v: 'turns', label: '턴' },
];
const TYPES: { v: ChartType; label: string }[] = [
  { v: 'line', label: '추이(선)' }, { v: 'bar', label: '막대' }, { v: 'donut', label: '도넛' },
];
const GROUPS: { v: GroupBy; label: string }[] = [
  { v: 'none', label: '전체' }, { v: 'model', label: '모델별' }, { v: 'user', label: '사용자별' },
];

export default function ChartBuilder({
  prefill, onClose, onAdded,
}: { prefill?: Partial<Omit<CustomPanel, 'id'>>; onClose: () => void; onAdded: () => void }) {
  const [metric, setMetric] = useState<Metric>(prefill?.metric ?? 'tokens');
  const [type, setType] = useState<ChartType>(prefill?.type ?? 'line');
  const [groupBy, setGroupBy] = useState<GroupBy>(prefill?.groupBy ?? 'none');
  const [days, setDays] = useState<number>(prefill?.days ?? 30);
  const [title, setTitle] = useState<string>(prefill?.title ?? '');

  const autoTitle = () => {
    const m = METRICS.find((x) => x.v === metric)?.label ?? metric;
    const g = GROUPS.find((x) => x.v === groupBy)?.label ?? '';
    return `${m} · ${g}`;
  };

  const submit = () => {
    addPanel({ title: title.trim() || autoTitle(), metric, type, groupBy, days });
    onAdded();
    onClose();
  };

  // 도넛/막대는 '전체' 그룹이면 나눌 축이 없다 — 모델별을 기본 권장.
  const groupHint = type !== 'line' && groupBy === 'none'
    ? '도넛·막대는 모델별 또는 사용자별로 나눌 때 의미가 있습니다.' : '';

  return (
    <Modal title="그래프 만들기" onClose={onClose} maxWidth={460}>
      <div className="builder">
        <label className="builder-row">
          <span>지표</span>
          <span className="seg-group">
            {METRICS.map((m) => (
              <button key={m.v} type="button" className={`segbtn${metric === m.v ? ' on' : ''}`} onClick={() => setMetric(m.v)}>{m.label}</button>
            ))}
          </span>
        </label>
        <label className="builder-row">
          <span>차트</span>
          <span className="seg-group">
            {TYPES.map((t) => (
              <button key={t.v} type="button" className={`segbtn${type === t.v ? ' on' : ''}`} onClick={() => setType(t.v)}>{t.label}</button>
            ))}
          </span>
        </label>
        <label className="builder-row">
          <span>그룹</span>
          <span className="seg-group">
            {GROUPS.map((g) => (
              <button key={g.v} type="button" className={`segbtn${groupBy === g.v ? ' on' : ''}`} onClick={() => setGroupBy(g.v)}>{g.label}</button>
            ))}
          </span>
        </label>
        <label className="builder-row">
          <span>기간</span>
          <span className="seg-group">
            {[7, 30, 90, 365].map((n) => (
              <button key={n} type="button" className={`segbtn${days === n ? ' on' : ''}`} onClick={() => setDays(n)}>{n}일</button>
            ))}
          </span>
        </label>
        <label className="builder-row">
          <span>제목</span>
          <input className="builder-input" value={title} placeholder={autoTitle()} onChange={(e) => setTitle(e.target.value)} />
        </label>
        {groupHint && <p className="help">{groupHint}</p>}
        <div className="builder-actions">
          <button type="button" className="ghost" onClick={onClose}>취소</button>
          <button type="button" className="primary" onClick={submit}>대시보드에 추가</button>
        </div>
      </div>
    </Modal>
  );
}
