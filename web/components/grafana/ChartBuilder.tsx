'use client';

/*
 * 차트 빌더 — 지표·차트종류·그룹기준·기간을 골라 대시보드에 커스텀 패널을 만든다.
 * 저장은 lib/customPanels(로컬). 대시보드의 '내 그래프' 섹션이 이 정의를 읽어 렌더한다.
 */
import { useId, useState } from 'react';
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
const DAYS: { v: number; label: string }[] = [7, 30, 90, 365].map((n) => ({ v: n, label: `${n}일` }));

/*
 * 세그먼트 한 줄.
 *
 * `<label>` 이 아니라 `<div role="group">` 이다 — `<button>` 은 HTML 스펙상 labelable 요소가
 * 아니라서 `<label>` 로 감싸도 **아무것도 라벨하지 않는다.** 보조기기에는 '지표'라는 말이 통째로
 * 사라지고 버튼 이름만 떠다녔다. `aria-labelledby` 로 묶어야 그룹 이름이 실제로 읽힌다.
 *
 * 상태는 `aria-pressed` 로 말한다. `role="radio"`+`aria-checked` 가 의미상 더 정확하지만
 * 화살표키 로빙 탭인덱스까지 갖춰야 규격에 맞고, 그러면 되돌리기 어려운 변경이 된다.
 * `.segbtn.on` 은 시각 표시로 그대로 남는다 — 색만으로 상태를 말하지 않기 위함이 아니라,
 * 그 CSS 가 이미 있고 이 변경은 보조기기 쪽만 메우기 때문이다.
 */
function SegRow<T extends string | number>({
  label, options, value, onPick,
}: {
  label: string;
  options: { v: T; label: string }[];
  value: T;
  onPick: (v: T) => void;
}) {
  const labelId = useId();
  return (
    <div className="builder-row" role="group" aria-labelledby={labelId}>
      <span id={labelId}>{label}</span>
      <span className="seg-group">
        {options.map((o) => (
          <button
            key={o.v}
            type="button"
            className={`segbtn${value === o.v ? ' on' : ''}`}
            aria-pressed={value === o.v}
            onClick={() => onPick(o.v)}
          >
            {o.label}
          </button>
        ))}
      </span>
    </div>
  );
}

export default function ChartBuilder({
  prefill, onClose, onAdded,
}: { prefill?: Partial<Omit<CustomPanel, 'id'>>; onClose: () => void; onAdded: () => void }) {
  const [metric, setMetric] = useState<Metric>(prefill?.metric ?? 'tokens');
  const [type, setType] = useState<ChartType>(prefill?.type ?? 'line');
  const [groupBy, setGroupBy] = useState<GroupBy>(prefill?.groupBy ?? 'none');
  const [days, setDays] = useState<number>(prefill?.days ?? 30);
  const [title, setTitle] = useState<string>(prefill?.title ?? '');
  const titleId = useId();

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
    /*
     * 자기 액션을 `footer` 로 넘긴다 — Modal 기본 `닫기` 를 대체한다.
     * 예전엔 본문 안 `.builder-actions` 에 `취소`·`대시보드에 추가` 를 두고 모달 기본 `닫기` 까지
     * 같이 떠서, 하는 일이 똑같은 종료 버튼이 화면에 둘이었다.
     */
    <Modal
      title="그래프 만들기"
      onClose={onClose}
      maxWidth={460}
      footer={(
        <>
          <button type="button" className="ghost" onClick={onClose}>취소</button>
          <button type="button" className="primary" onClick={submit}>대시보드에 추가</button>
        </>
      )}
    >
      <div className="builder">
        <SegRow label="지표" options={METRICS} value={metric} onPick={setMetric} />
        <SegRow label="차트" options={TYPES} value={type} onPick={setType} />
        <SegRow label="그룹" options={GROUPS} value={groupBy} onPick={setGroupBy} />
        <SegRow label="기간" options={DAYS} value={days} onPick={setDays} />
        {/*
          * 제목만 `<label>` 을 그대로 쓴다 — `<input>` 은 labelable 이라 여기서는 라벨이 실제로
          * 동작한다. 그래도 `htmlFor` 로 명시한다: 이 줄의 구조가 바뀌어도 연결이 끊기지 않게.
          * 클래스는 떼어 전역 `input` 규칙에 맡긴다(`.builder-input` 은 사라진다).
          */}
        <label className="builder-row" htmlFor={titleId}>
          <span>제목</span>
          <input id={titleId} value={title} placeholder={autoTitle()} onChange={(e) => setTitle(e.target.value)} />
        </label>
        {groupHint && <p className="help">{groupHint}</p>}
      </div>
    </Modal>
  );
}
