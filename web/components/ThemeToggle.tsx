'use client';

/*
 * 테마 토글 — 사이드바 발치.
 *
 * **글자로 상태를 말한다**(components/platform/SupportBadge.tsx 와 같은 규율). 해·달 아이콘만
 * 두면 그것이 "지금 상태"인지 "누르면 될 상태"인지 사람마다 다르게 읽는다. 그래서 버튼 글자는
 * 언제나 **누르면 되는 것**이고, 지금 상태는 `aria-pressed` 로만 말한다(보조기기용).
 */

import { toggleTheme, useTheme } from '@/lib/theme';

/** 해(라이트로 감) · 달(다크로 감). 사이드바 탭 아이콘과 같은 stroke 규격을 쓴다. */
const ICON = {
  light: 'M12 4V2m0 20v-2m8-8h2M2 12h2m13.66-5.66 1.41-1.41M4.93 19.07l1.41-1.41m11.32 0 1.41 1.41M4.93 4.93l1.41 1.41M16 12a4 4 0 1 1-8 0 4 4 0 0 1 8 0Z',
  dark: 'M20 14.5A8 8 0 0 1 9.5 4a7 7 0 1 0 10.5 10.5Z',
} as const;

export default function ThemeToggle() {
  const theme = useTheme();
  const next = theme === 'dark' ? 'light' : 'dark';

  return (
    <button
      className="ghost theme-toggle"
      type="button"
      onClick={toggleTheme}
      aria-pressed={theme === 'dark'}
      title="이 브라우저에 저장됩니다. 고르기 전에는 OS 설정을 따릅니다."
    >
      <svg className="side-icon" viewBox="0 0 24 24" aria-hidden="true">
        <path d={ICON[next]} />
      </svg>
      <span>{next === 'dark' ? '다크 모드' : '라이트 모드'}</span>
    </button>
  );
}
