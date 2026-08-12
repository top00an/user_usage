---
type: system
tags: [프런트, nextjs, react]
updated: 2026-08-12
sources: ["web/README.md", "web/components/Dashboard.tsx", "go/CONTRACT.md", "web/lib/"]
---

# web — 대시보드 프런트

Next.js App Router + React + TypeScript. **정적 export** 라 런타임 서버가 없고,
[[usage-server]] 가 `go:embed` 로 서빙한다([[webroot-embed]]).

```bash
cd web
npm run build        # → out/ (정적 export + 인라인 스크립트 외부화 + embed-manifest.json)
npm run lint         # eslint --max-warnings=0 && tsc --noEmit
npm test             # vitest (jsdom) — 픽스처는 contract/golden/ 을 그대로 읽는다
npm run verify:live  # 실제 크로미움 + 실제 CSP + 실제 쿠키로 왕복
```

## 구조

```
app/            layout(셸·테마부트) · page(단일 라우트) · globals.css(토큰)
lib/api.ts      ★ 유일한 서버 호출구. 이 파일 밖에서는 fetch 를 부르지 않는다
lib/types.ts    contract/golden/ 에서 뽑은 응답 타입 — 추측한 필드가 없다
lib/costLabels.ts  ★ "API 환산 비용" 라벨의 단일 출처
lib/theme.ts    테마 — DOM(`<html data-theme>`)이 단일 출처, useSyncExternalStore 로 구독
lib/platforms.ts · platformFilter.ts   플랫폼 축
lib/customPanels.ts · format.ts
hooks/useResource.ts   낡은 응답 폐기(abort · cancelled · 요청 키 대조)
components/
  Dashboard.tsx   셸 — 탭 정의·해시 라우팅·권한별 탭 노출
  ThemeToggle.tsx 다크/라이트 토글(사이드바 발치) — lib/theme.ts
  Login · Modal · Toast · ui · Architecture · Onboarding · charts
  usagetrack/     사용 추적 (ModelTable · AxisExplorer)
  usageobs/       사용 관측 (CostCard · SeatsCard · TeamsCard · SessionsCard · UserDetailModal)
  grafana/        대시보드 탭 (GrafanaDash · DragGrid · ChartBuilder · CustomPanelView)
  admin/          관리 (UsersCard · KeysCard · UserSheet)
  onboarding/     연동 (IssuedKeyModal · RevokeCell · keyscope)
  platform/       플랫폼 필터·범위·요약·지원 배지
```

라우팅은 **해시**다(`#/usage` · `#/usageobs` …). 딥링크 계약을 잇고, 산출물이 `index.html`
한 장으로 끝나 Go 쪽 경로 목록이 늘어나지 않는다.

## 지켜야 할 다섯

`go/CONTRACT.md` 의 web 절이 계약이고, 각 항목마다 **강제하는 자리**가 있다.

| 규율 | 어디서 지키나 |
|---|---|
| 인증은 쿠키 `usage_tok`(`SameSite=Strict`, https 일 때만 `Secure`) | `lib/token.ts` |
| API 호출은 한 파일로 | `lib/api.ts` · eslint `no-restricted-globals` · `test/no-direct-fetch.test.ts` |
| **`status` 로 분기, 문구로 하지 않는다** | `lib/api.ts` 의 `failureKind` · `ui.tsx` 의 `ErrorState` |
| **근사값을 정확한 값으로 위장하지 않는다** | 아래 6곳 |
| 탭 전환 시 낡은 응답 폐기 | `hooks/useResource.ts` |

문구로 분기하면: 에러 문구는 사람이 읽는 글이라 언제든 다듬어지고, 그때 화면이 **조용히
틀린 쪽으로** 넘어간다. 그래서 [[golden-contract]] 가 오류 계약(401×2·403×2·400×5·404×2)을
따로 센다.

## 근사·미상을 말하는 6곳

지우면 화면이 깔끔해지는데, 그게 이 도구가 하지 않기로 한 일이다 → [[honest-uncertainty]].

1. 사용 추적 › 모델별 표 **`근거` 열** (`fromSeries` / `fromSession`) → [[model-three-paths]]
2. 사용 추적 › 모델별 카드 하단 **사용자별 series 커버리지** (`modelAxis`)
3. 사용 관측 › API 환산액 › **단가 미등록 모델** 이름 (`unpriced`) → [[cost-model]]
4. 사용 관측 › API 환산액 › **TTL 미상 N행 · 최대 1.6배 과소** (`ttlUnknownRows`)
5. 사용 추적 › 머리글의 **키워드 보존 기한** (`retention.keywordDays`) → [[data-policy]]
6. 사용 관측 › **수집 상태** — 어느 발신처가 보고를 멈췄나 (`coverage`)

## 왜 프런트가 토큰을 직접 든다 (프록시가 아니라)

`go/CONTRACT.md` 의 결정: Next 서버가 토큰을 들고 프록시하는 안은 지금의 **"CSRF 표면 없음"**
설계를 다시 짜야 하고, 붙일 사내 시스템을 아직 모른다. 대신 API 호출을 `lib/api.ts` 한
파일로 모아 **SSO·프록시로 갈아탈 때 고칠 자리가 하나**이게 했다.

쿠키로는 **조회만** 된다. 상태변경은 `Authorization` 헤더 인증만 인정한다(403) —
브라우저는 임의 헤더를 붙일 수 없으므로 화면은 자연히 조회 전용이 되고 CSRF 표면이 아예
생기지 않는다 → [[auth-scopes]].

## 테마 (다크 / 라이트)

**값의 단일 출처는 DOM 의 `<html data-theme>`** 이고, 그것을 처음 세우는 것은
`public/theme-boot.js` 다. 우선순위:

```
localStorage 'usage-theme'  →  없으면 OS prefers-color-scheme  →  그것도 모르면 dark
```

CSS 의 기본값(`:root`)이 다크이고 `[data-theme=light]` 일 때만 밝아진다. **OS 가 라이트 모드면
라이트로 뜬다 — 버그가 아니라 설계다.**

부팅 스크립트가 **인라인이 아니라 파일**인 이유가 둘이다:

- 인라인 `<script>` 가 하나라도 있으면 CSP 에 `script-src 'self'` 를 걸 수 없다
- `next/script` 의 `beforeInteractive` 는 App Router 에서 인라인 큐로 나가고 하이드레이션 뒤에
  실행돼, **다크 사용자에게 흰 화면이 한 프레임 번쩍인다**(FOUC)

`lib/theme.ts` 는 그 값을 읽고 바꾸는 얇은 층이지 **두 번째 진실이 아니다** — React state 에
사본을 두면 부팅 스크립트가 세운 값과 갈리고, 그때 화면과 저장값이 서로 다른 말을 한다.
구독은 `useSyncExternalStore`(탭 해시와 같은 패턴)이고 `storage` 이벤트로 **다른 탭의 변경도
따라간다.**

**2026-08-12 추가** — 사이드바 발치에 `ThemeToggle` 을 넣었다. 버튼 글자는 언제나 *누르면 되는
것*("다크 모드"/"라이트 모드")이고 지금 상태는 `aria-pressed` 로만 말한다 — 아이콘만 두면 그것이
"지금 상태"인지 "누르면 될 상태"인지 사람마다 다르게 읽는다(`SupportBadge` 와 같은 규율).
한 번 고르면 그 뒤로는 OS 를 따라 바뀌지 않는다. **OS 추종은 "아직 고른 적 없음"의 기본값이다.**

## Go 서빙 조건

- 정적 루트는 `web/out/` **전체**. 경로 화이트리스트는 임베드 FS 를 순회해 만든다 —
  손으로 적으면 `_next/static/chunks/*` 의 내용 해시 파일명 때문에 빌드마다 깨진다.
- `/` 요청 → `index.html`. 해시 라우팅이라 SPA 폴백이 필요 없다.
- **CSP 는 `script-src 'self'` 로 둘 수 있다.** Next 가 남기는 인라인 스크립트를 빌드
  후처리(`scripts/externalize-inline-scripts.mjs`)가 파일로 뽑아낸다. 하나라도 남으면
  빌드를 실패시킨다 — 하나만 남아도 하이드레이션이 죽고 화면이 **에러 없이 빈 채로** 뜬다.
- MIME: `.html .css .js .json .svg .txt`(RSC 페이로드의 `index.txt` 포함).

## verify:live

배포와 같은 조건을 만들어 왕복을 돌린다.

1. `contract/fixtures.mjs` 시드를 넣은 서버를 임시 DB 로 띄운다(4192)
2. `scripts/preview.mjs` 가 정적 산출물과 `/api` 프록시를 **한 오리진**에 모은다(4300).
   다른 오리진이면 쿠키가 안 실려 로그인 자체가 성립하지 않는다 — 그러면 검증이 아니다.
3. 크로미움으로 게이트 → 401 복구 → 탭 → 드릴다운 → **탭 레이스** → 토큰 삭제 →
   다크/라이트 → 390px 을 확인하고 `.verify/` 에 스크린샷을 남긴다.

## 관련

[[usage-server]] · [[webroot-embed]] · [[honest-uncertainty]] · [[auth-scopes]] ·
[[golden-contract]] · [[cost-model]]
