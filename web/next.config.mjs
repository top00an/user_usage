/**
 * Next 설정 — 정적 export 전용.
 *
 * `output: 'export'` 인 이유는 배포 형태가 정해져 있기 때문이다. Go 바이너리가 `go:embed` 로
 * 산출물을 안고 서빙한다(go/CONTRACT.md 의 "정적 서빙"). 그래서 이 앱에는 런타임 서버가 없다 —
 * 서버 컴포넌트에서 데이터를 가져오면 그 값이 **빌드 시각에 굳어** 화면이 조용히 낡는다.
 * 데이터는 전부 클라이언트에서 온다(lib/api.ts).
 *
 * 라우팅은 해시(`#/usage`, `#/usageobs`)다. 현행 셸의 딥링크 계약을 그대로 잇고, 산출물이
 * `index.html` 한 장으로 끝나 Go 쪽 경로 화이트리스트가 늘어나지 않는다.
 */
/** @type {import('next').NextConfig} */
const nextConfig = {
  output: 'export',
  reactStrictMode: true,

  // 이 화면은 검색 대상이 아니다(현행 index.html 의 robots noindex 와 같은 뜻).
  poweredByHeader: false,

  // next/image 를 쓰지 않는다 — 정적 export 에 이미지 최적화 런타임이 없고, 이 도구에 사진이 없다.
  images: { unoptimized: true },
};

export default nextConfig;
