'use client';

/*
 * ── 사용자 선택 ───────────────────────────────────────────────────────────
 *
 * '사용 추적'을 한 사람 기준으로 좁힌다. 고르면 이 화면의 **모든** 카드가 그 사람의 값이 된다
 * (집계 타일 · 사용자별 · 모델별 · 축 패널 · 사람별 활용). 한쪽만 좁히면 같은 화면의 두 카드가
 * 서로 다른 모집단을 그리면서 그 사실을 말하지 않는다 — 그것이 이 축에서 가장 나쁜 실패다.
 *
 * ⚠ 선택지는 **필터가 걸리지 않은 응답**에서 온다(호출부가 roster 로 들고 있다).
 *   필터가 걸린 응답의 byUser 로 목록을 만들면 한 사람을 고른 순간 목록에 그 사람만 남아
 *   **다른 사람으로 갈아탈 방법이 사라진다.** 자기 자신을 좁히는 목록은 만들지 않는다.
 *
 * 선택지가 하나뿐이면 컨트롤을 그리지 않는다 — '전체'와 그 한 사람이 같은 화면이라 고를 것이
 * 없는 셀렉트만 남는다(PlatformFilter 와 같은 규율).
 */

import { useEffect } from 'react';

export default function UserFilter({
  users, value, onChange,
}: {
  /** 필터 없는 응답이 정한 전체 명단. */
  users: string[];
  value: string;
  onChange: (user: string) => void;
}) {
  const known = users.includes(value);

  /*
   * 명단에 없는 선택은 전체로 되돌린다(그 사람의 보고가 사라졌다). 안 그러면 없는 이름이
   * 계속 질의로 나가고 화면은 이유 없이 빈 채로 남는다.
   * 렌더 중에 부모 상태를 건드리면 안 되므로 이펙트에서 한다.
   */
  useEffect(() => {
    if (value && !known) onChange('');
  }, [value, known, onChange]);

  if (users.length < 2) return null;

  return (
    <div className="pf-bar">
      <label htmlFor="user-filter">사용자</label>
      {/*
        * 클래스를 주지 않는다 — 전역 `input, select, textarea` 규칙이 받는다.
        * 예전엔 `.builder-input`(차트 빌더 모달 전용)을 빌려 썼는데, 그 결과 이 칸만
        * 앱의 다른 어떤 칸과도 높이가 달랐고 채움이 `--surface` 라 다크에서는 카드와
        * 섞여 사라지고 라이트에서는 혼자 튀었다. 너비는 `.pf-bar select` 가 잡는다.
        */}
      <select
        id="user-filter"
        value={known ? value : ''}
        onChange={(e) => onChange(e.target.value)}
      >
        <option value="">전체</option>
        {users.map((u) => (
          <option key={u} value={u}>{u}</option>
        ))}
      </select>
      <span className="help">
        {value
          ? <>이 화면의 모든 수치가 <b>{value}</b> 기준입니다.</>
          : <>전체 기준입니다 — 한 사람을 고르면 이 화면 전체가 그 사람 기준으로 바뀝니다.</>}
      </span>
    </div>
  );
}
