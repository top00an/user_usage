import next from 'eslint-config-next';
import nextCoreWebVitals from 'eslint-config-next/core-web-vitals';
import nextTypescript from 'eslint-config-next/typescript';

/*
 * 린트는 리뷰가 매번 하지 못하는 것을 대신 잡는 자리다. 아래 셋은 전부 "지금은 아무것도
 * 깨지지 않고, 나중에 비싸게 돌아오는" 종류라 사람이 놓친다.
 */
const config = [
  /*
   * 산출물·도구 디렉터리는 린트하지 않는다.
   *
   * `.claude/**` 는 에이전트 스캐폴딩이다(스킬 스크립트·그 백업). git 은 이미 무시하지만
   * eslint 는 .gitignore 를 보지 않으므로, 이 줄이 없으면 **우리 코드가 아닌 파일 때문에**
   * 게이트가 빨간불이 된다(실측: 남의 스킬 스크립트에서 18 error/30 warning).
   * `.verify-canvas/**` 는 실물 검증 스크린샷 자리다(scripts/verify-canvas.mjs).
   */
  { ignores: ['.next/**', 'out/**', 'node_modules/**', 'next-env.d.ts', '.verify/**', '.verify-canvas/**', '.claude/**'] },
  ...next,
  ...nextCoreWebVitals,
  ...nextTypescript,
  {
    rules: {
      /*
       * 컴포넌트가 fetch 를 직접 부르면 SSO·프록시로 갈아탈 때 전면 수정이 된다.
       * 서버 호출구는 lib/api.ts 하나다(go/CONTRACT.md web/ 절).
       */
      'no-restricted-globals': [
        'error',
        { name: 'fetch', message: 'fetch 직접 호출 금지 — 서버 호출은 lib/api.ts 를 경유한다.' },
      ],
      'no-restricted-properties': [
        'error',
        { object: 'window', property: 'fetch', message: 'fetch 직접 호출 금지 — lib/api.ts 를 경유한다.' },
        { object: 'globalThis', property: 'fetch', message: 'fetch 직접 호출 금지 — lib/api.ts 를 경유한다.' },
      ],
      'no-restricted-syntax': [
        'error',
        {
          selector: "JSXAttribute[name.name='dangerouslySetInnerHTML']",
          message: 'dangerouslySetInnerHTML 금지 — 서버 값은 React 기본 이스케이프를 통과시킨다.',
        },
      ],
    },
  },
  {
    // lib/api.ts 는 유일하게 fetch 를 부르는 자리다 — 그 예외를 여기 한 줄로 못 박는다.
    files: ['lib/api.ts'],
    rules: { 'no-restricted-globals': 'off' },
  },
  {
    files: ['test/**', 'scripts/**'],
    rules: {
      'no-restricted-globals': 'off',
      'no-restricted-properties': 'off',
      '@typescript-eslint/no-explicit-any': 'off',
    },
  },
];

export default config;
