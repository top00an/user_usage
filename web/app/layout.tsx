import type { Metadata, Viewport } from 'next';
import './globals.css';

/*
 * 셸 — 현행 public/index.html 의 계약을 그대로 잇는다.
 *
 *   ① 테마 선반영은 **스타일시트보다 먼저** 동기 실행돼야 한다(FOUC). 그래서 파일 스크립트를
 *      beforeInteractive 로 head 에 심는다.
 *   ② 인라인 <script> 를 우리 손으로 두지 않는다 — 하나라도 있으면 CSP 의 script-src 'self' 를
 *      포기해야 한다. (Next 가 빌드 산출물에 남기는 인라인 스크립트는 빌드 후처리로 파일로
 *      뽑아낸다 — scripts/externalize-inline-scripts.mjs)
 *   ③ 배경 광원(.bg)과 토스트 앵커는 셸이 소유한다.
 */
export const metadata: Metadata = {
  title: '사용량 대시보드',
  description: '동기화된 PC 들의 토큰·도구 사용량과 API 환산 비용.',
  robots: { index: false, follow: false },
  icons: { icon: '/favicon.svg' },
};

export const viewport: Viewport = {
  width: 'device-width',
  initialScale: 1,
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="ko">
      <head>
        {/*
          렌더-블로킹 고전 스크립트여야 한다. next/script 의 beforeInteractive 는 App Router 에서
          **인라인** 큐(self.__next_s)로 나가므로 두 가지가 동시에 깨진다:
          CSP 의 script-src 'self' 에 걸리고, 하이드레이션 뒤에 실행돼 다크 사용자에게 흰 화면이
          한 프레임 번쩍인다. 그래서 head 에 직접 심는다.
        */}
        {/* eslint-disable-next-line @next/next/no-sync-scripts -- 동기 실행이 목적이다: 이 스크립트가 늦으면 다크 사용자에게 흰 화면이 한 프레임 번쩍인다(FOUC). */}
        <script src="/theme-boot.js" />
      </head>
      <body>
        <div className="bg" aria-hidden="true" />
        <div id="app">{children}</div>
      </body>
    </html>
  );
}
