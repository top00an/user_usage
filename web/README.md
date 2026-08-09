# web/ — 사용량 대시보드 프런트

현행 `public/` 의 두 화면(**사용 추적** · **사용 관측**)을 Next.js App Router + React + TypeScript 로
옮긴 것. 산출물은 **정적 export** 라 런타임 서버가 없고, Go 바이너리가 `go:embed` 로 서빙한다.

```bash
npm ci
npm run build        # → out/ (정적 export + 인라인 스크립트 외부화 + embed-manifest.json)
npm run lint         # eslint --max-warnings=0 && tsc --noEmit
npm test             # vitest (jsdom) — 픽스처는 contract/golden/ 을 그대로 읽는다
npm run verify:live  # 실제 크로미움 + 실제 CSP + 실제 쿠키로 왕복 검증 (아래 참조)
```

## 구조

```
app/          layout(셸·테마부트) · page(단일 라우트) · globals.css(토큰)
lib/api.ts    ★ 유일한 서버 호출구. 이 파일 밖에서는 fetch 를 부르지 않는다
lib/token.ts  쿠키(usage_tok) 읽기/쓰기 + 외부 스토어(useSyncExternalStore)
lib/types.ts  contract/golden/ 에서 뽑은 응답 타입 — 추측한 필드가 없다
hooks/        useResource — 낡은 응답 폐기(abort · cancelled · 요청 키 대조)
components/   Dashboard(셸) · TokenGate · Modal · Toast · ui
  usagetrack/ 사용 추적
  usageobs/   사용 관측
scripts/      빌드 후처리 · 미리보기 서버 · 시드 API · 실물 검증
```

라우팅은 **해시**다(`#/usage` · `#/usageobs`). 현행 셸의 딥링크 계약을 그대로 잇고, 산출물이
`index.html` 한 장으로 끝나 Go 쪽 경로 목록이 늘어나지 않는다.

## 지켜야 할 것 (go/CONTRACT.md web/ 절)

| | 어디서 지키나 |
|---|---|
| 인증은 현행 유지(쿠키 `usage_tok`, `SameSite=Strict`, **https 일 때만** `Secure`) | `lib/token.ts` |
| API 호출은 한 파일로 | `lib/api.ts` · eslint `no-restricted-globals` · `test/no-direct-fetch.test.ts` |
| `status` 로 분기, 문구로 하지 않는다 | `lib/api.ts` 의 `failureKind` · `components/ui.tsx` 의 `ErrorState` |
| 근사값을 정확한 값으로 위장하지 않는다 | 아래 6곳 |
| 탭 전환 시 낡은 응답 폐기 | `hooks/useResource.ts` |

**근사·미상을 말하는 6곳** — 지우면 화면이 깔끔해지는데, 그게 이 도구가 하지 않기로 한 일이다.

1. 사용 추적 › 모델별 표 `근거` 열 (`fromSeries` / `fromSession`)
2. 사용 추적 › 모델별 카드 하단 **사용자별 series 커버리지** (`modelAxis`)
3. 사용 관측 › API 환산액 › **단가 미등록 모델** 이름 (`unpriced`)
4. 사용 관측 › API 환산액 › **TTL 미상 N행 · 최대 1.6배 과소** (`ttlUnknownRows`)
5. 사용 추적 › 머리글 문장의 **키워드 보존 기한** (`retention.keywordDays`)
6. 사용 관측 › **수집 상태** — 어느 발신처가 보고를 멈췄나 (`coverage`)

## Go 쪽에서 서빙할 때 (go-http 오너용)

- 정적 루트는 `web/out/` 전체다. `go:embed` 로 안고, **경로 화이트리스트는 임베드된 FS 를 순회해
  만든다.** 손으로 적지 말 것 — `_next/static/chunks/*` 의 파일명은 내용 해시라 빌드마다 바뀐다.
  참고용으로 `out/embed-manifest.json` 에 그 빌드의 전체 경로 목록이 들어 있다.
- `/` 요청은 `index.html` 을 낸다. 해시 라우팅이라 그 밖의 SPA 폴백은 필요 없다.
- **CSP 는 현행 그대로 `script-src 'self'` 로 둘 수 있다.** Next 가 남기는 인라인 스크립트를
  빌드 후처리가 파일로 뽑아내기 때문이다(`scripts/externalize-inline-scripts.mjs`).
  후처리는 인라인이 하나라도 남으면 빌드를 실패시킨다 — 하나만 남아도 하이드레이션이 죽고
  화면이 **에러 없이 빈 채로** 뜬다.
- MIME 은 `.html .css .js .json .svg .txt` 가 필요하다(`index.txt` 등 RSC 페이로드 포함).

## 검증

`npm run verify:live` 는 배포와 같은 조건을 만들어 왕복을 돌린다.

1. `contract/fixtures.mjs` 시드를 넣은 Node 서버를 임시 DB 로 띄운다(포트 4192)
2. `scripts/preview.mjs` 가 정적 산출물과 `/api` 프록시를 **한 오리진**에 모은다(포트 4300).
   다른 오리진이면 쿠키가 실리지 않아 로그인 자체가 성립하지 않는다 — 그러면 검증이 아니다.
   보안 헤더는 `server.js:289~293` 과 같은 값을 쓴다.
3. 크로미움으로 게이트 → 401 복구 → 두 탭 → 드릴다운 → 탭 레이스 → 토큰 지우기 →
   다크/라이트 → 390px 를 확인하고 `.verify/` 에 스크린샷을 남긴다.

리눅스에 크로미움 의존 라이브러리가 없으면(`libnspr4.so` 등) sudo 없이 이렇게 붙일 수 있다:

```bash
mkdir -p /tmp/pwlibs && cd /tmp/pwlibs
apt-get download libnspr4 libnss3 libasound2t64 && for f in *.deb; do dpkg -x "$f" root; done
LD_LIBRARY_PATH=/tmp/pwlibs/root/usr/lib/x86_64-linux-gnu npm run verify:live
```
