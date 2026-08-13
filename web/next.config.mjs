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

  /*
   * 빌드 ID 를 **고정한다.** 안 고정하면 Next 가 매 빌드 무작위 ID 를 만들고, 그 ID 가
   * `_next/static/<buildId>/` 경로에 들어간다 — 같은 소스로 두 번 빌드해도 산출물이 달라진다.
   *
   * 그것이 `npm run verify:embed`(= build.sh && git diff --exit-code -- webroot)를 **구조적으로
   * 통과 불가**로 만들고 있었다. 소스와 webroot 를 함께 커밋해도 다음 빌드가 경로를 바꿔
   * 항상 diff 가 났다. 실측(2026-08-13): 같은 트리로 연속 두 빌드 →
   * LMdtoBrxbGa7SIQMPVwLh / Ngn3RNXfW_rI7otiwgoYZ. 드리프트를 잡으라고 만든 게이트가
   * 늘 빨간 상태였으니 아무 신호도 주지 못했다.
   *
   * ⚠ 상수로 굳혀도 캐시가 안전한 근거: 이 산출물을 서빙하는 Go 정적 핸들러가 **모든 경로에
   *   `Cache-Control: no-cache`** 를 준다(go/internal/httpapi/static.go — `_next/static/*` 도
   *   예외가 아니다). 브라우저가 매번 재검증하므로 경로가 고정돼도 낡은 매니페스트가 굳지
   *   않는다. 그리고 청크 파일명은 여전히 **콘텐츠 해시**라 코드가 바뀌면 파일명이 바뀐다 —
   *   캐시 무효화는 그쪽이 담당하고 buildId 는 담당하지 않는다.
   *
   *   ⚠ static.go 가 `_next/static/*` 를 immutable 로 굳히는 날에는 이 상수를 다시 검토하라.
   *     그때는 buildId 가 고정된 채 내용만 바뀌는 파일(_buildManifest 계열)이 문제가 된다.
   */
  generateBuildId: () => 'usage-dashboard',

  // 이 화면은 검색 대상이 아니다(현행 index.html 의 robots noindex 와 같은 뜻).
  poweredByHeader: false,

  // next/image 를 쓰지 않는다 — 정적 export 에 이미지 최적화 런타임이 없고, 이 도구에 사진이 없다.
  images: { unoptimized: true },
};

export default nextConfig;
