# 포팅 현황 — Node → Go + Next.js

> ## ✅ 2026-08-09 — 컷오버 완료 (CLOSED)
>
> **Node 구현은 제거됐습니다**(`server.js`·`index.js`·`lib/`·`routes/`·`public/`·Node `test/`).
> 이제 백엔드 Go(`go/`) · 프런트 Next.js(`web/`) 단일 스택입니다. 게이트 통과 근거:
>
> - `contract:verify` 골든 **44/44 × 3회**(새 포트·새 빈 DB) — sqlite local 실측.
> - **PostgreSQL(remote) 실측 검증**(아래 리스크 1 해소): 앱 롤(NOSUPERUSER·NOBYPASSRLS)로
>   마이그레이션·RLS 테넌트 격리·`?→$n`·수제 커넥션 풀 PASS. **pg 전용 버그 1건 수정** —
>   `SUM/AVG(bigint)`→`numeric` 을 조용히 0 으로 떨구던 스캔 결함(`db/pg.go`).
> - **수집기 추가**(아래 리스크 7 해소): `collector/` — jsonl→`POST /api/usage`, 멱등·증분·정책필터.
> - 부동소수 파리티 잔여 2건 해소: 분위수 `idx` 도 값 배리어로 반올림(FMA 융합 차단),
>   결정적 정렬 타이브레이크 2곳(`store.UsageModelAxis`·`identity.Unmapped`).
>
> 골든(`contract/golden/`)은 제거 시점에 **동결**됐습니다 — 캡처는 Node 가 하던 것이라
> 더는 재생성하지 않고, 회귀 대조(`contract:verify`, Go 서버 대상)만 유효합니다.
>
> **잔여(후속):** 런타임 Docker 이미지가 `migrations/` 를 담지 않음 — remote(pg) 모드에서
> 마이그레이션 러너가 파일을 찾게 하려면 COPY 추가 필요(remote 는 사전 마이그레이션 전제라
> 조회만 하면 무해할 수 있음, 확인 요). docker 이미지 빌드 자체는 이 환경(docker 미설치)에서
> 미검증 — 네이티브 스모크만 통과.
>
> 아래는 컷오버 이전(2026-08-07)의 기록입니다 — 경위 참고용으로 보존합니다.

---

2026-08-07. **백엔드 Go · 프런트 Next.js(React) 전환이 착지했고, 현행 Node 서버는 그대로 살아
있습니다.** 둘이 공존하는 이유와 무엇이 아직 검증되지 않았는지를 여기 적습니다.

## 지금 두 구현이 같이 있습니다

| | 무엇 | 왜 남아 있나 |
|---|---|---|
| **Node** | `server.js` · `lib/` · `routes/` · `public/` | **골든의 출처**입니다. 44개 스냅샷이 이 서버의 응답이고, Go 포팅본은 그것과 대조해서 합격 판정을 받습니다. 지우면 판정 근거가 사라집니다 |
| **Go** | `go/` (바이너리 1개, Next.js 프런트 임베드) | 새 배포 대상 |

Node 를 언제 지울지는 **pg 왕복이 실측으로 검증된 뒤** 결정할 일입니다(아래 리스크 1).
그때까지 `npm test`(208개)와 `lib/`·`routes/`·`server.js`·`public/`·`contract/golden/` 은
**건드리지 않습니다.**

## 합격 기준

```bash
bash scripts/build.sh                    # web 빌드 → webroot 동기화 → go build (유일한 빌드 경로)

# 빈 데이터 디렉터리로 띄우고
USAGE_ADMIN_TOKEN=contract-admin-token-0123456789 \
USAGE_INTAKE_TOKEN=contract-intake-token-9876543210 \
USAGE_DATA_DIR=$(mktemp -d) USAGE_PORT=8080 USAGE_KEYWORD_RETENTION_DAYS=off \
  ./go/usage-server &

npm run contract:verify -- --base http://127.0.0.1:8080
```

⚠ **Go 의 맵 순회는 실행마다 순서가 달라서 한 번의 초록불은 증거가 아닙니다.** 최소 3회, 매 회차
새 포트·새 빈 DB 로 돌립니다. 실제로 이 반복이 버그를 하나 잡았습니다(아래 리스크 3).

⚠ `contract/golden/` 을 고쳐서 통과시키는 것은 **게이트를 무력화하는 것**입니다.
골든이 틀렸다고 판단되면 근거를 들고 사람에게 가십시오.

## 검증된 것 (2026-08-07 실측)

| 게이트 | 결과 |
|---|---|
| `contract:verify` × 3회 (새 포트·새 빈 DB) | 44/44 × 3 |
| `go build ./...` · `go vet ./...` · `gofmt -l` | 무경고 |
| `go test ./...` (11 패키지) | 전부 통과 |
| `npm test` (현행 Node) | 208/208 |
| `npm run contract:selfcheck` | 하네스 결정적 |
| `web`: lint(경고0·tsc0) · test | 76/76 |
| Go 단독 기동 브라우저 왕복 | 20/20 (CSP 위반 0 · 콘솔 오류 0) |
| Node + preview 프록시 브라우저 왕복 | 34/34 |

---

## 남은 리스크

고치지 않고 **의도적으로 남긴 것들**입니다. 각각 왜 지금 안 고쳤는지 함께 적습니다.

### 1. pg(remote) 왕복은 아무도 실행 검증하지 않았다 — **가장 큰 구멍**

sqlite 로만 돌렸습니다(PostgreSQL 클러스터가 없었습니다). 확인된 것은 **터널 미개통 경로뿐**입니다
— 부팅이 경고만 남기고 뜨는 것, 조회가 400 으로 접히는 것.

SQL 은 양 방언 공통 문법으로 썼고 UPSERT 충돌 대상만 갈랐지만, **그건 코드 리뷰이지 실행
증거가 아닙니다.** 특히 검증되지 않은 것:

- pg 의 RLS 테넌트 주입(`BEGIN → set_config('app.tenant_id', …, true) → 본문 → COMMIT`)
- `?` → `$n` 자리표시자 치환이 실제 쿼리 전부에서 맞는지
- `migrations/pg/*.sql` 러너
- 손으로 만든 커넥션 보관함(아래 리스크 5)

**해야 할 일:** PostgreSQL 을 띄우고 앱 롤(`CREATE ROLE usage_app LOGIN NOSUPERUSER NOBYPASSRLS`)로
`contract:verify` 를 remote 모드로 돌립니다. remote 는 읽기 전용이라 인테이크가 없으므로
하네스를 그대로 쓸 수 없습니다 — local 로 채운 DB 를 remote 로 다시 열어 조회 스냅샷만 대조하는
경로가 필요합니다.

### 2. `go/internal/httpapi/webroot/` 는 커밋되는 빌드 산출물이다

`go:embed` 는 패키지 디렉터리 밖을 참조하지 못하고 심링크를 따라가지 않으므로 **복사가 유일한
길**입니다. 그래서 `web/out/` 과 `webroot/` 두 벌이 존재합니다.

`.gitignore` 의 주석이 정확히 *"빌드로 재현되는 것을 커밋하면 소스와 어긋난 채 배포되는 날이
온다"* 고 적고 있고, `webroot/` 가 바로 그것입니다. 방어 넷을 걸었습니다:

1. `scripts/build.sh` — 유일한 빌드 경로. 동기화 후 파일 수를 세고 어긋나면 멈춥니다
2. `static.go` — `index.html` 이 임베드에 없으면 **init 에서 죽습니다**(셸 없는 바이너리가 조용히 떠서 404 만 내는 것보다 부팅 실패가 낫습니다)
3. `static_test.go` 의 `TestIndexHTMLReferencesOnlyEmbeddedAssets` — 드리프트를 **화면에서 보이기 전에** 잡습니다
4. `npm run verify:embed` — 재빌드 후 `git diff --exit-code -- go/internal/httpapi/webroot`

**해야 할 일:** 4번을 CI 에 겁니다. 그리고 PR 에 `webroot/` diff 가 있으면 `build.sh` 로 만든
것인지 확인하는 것을 리뷰 규칙으로 둡니다.

### 3. 계열 합계가 부동소수 덧셈 순서에 의존한다

`/api/usage/series` 의 칸 합계는 원행이 온 순서(`store.SeriesRows` 의 `ORDER BY hour DESC`)로
더한 값입니다. 현행 Node 와 바이트 단위로 맞추려고 그 순서를 그대로 옮겼습니다.

**저장 계층이 정렬을 바꾸면 이 합계의 마지막 자리가 바뀝니다**(값은 실질적으로 같지만 골든은
갈립니다). 근본 해결은 정렬된 순서로 접거나 `math/big` 을 쓰는 것인데, 그러면 Node 와 어긋나
지금 게이트가 빨개집니다. Node 를 지우는 시점에 정리할 자리입니다.

> 이 리스크는 **골든이 실제로 잡아낸 것**입니다. Go 맵 순회 순서로 더했더니
> `0.13329999999999997` 이 `0.1333` 으로 나왔고, Go 단위 테스트 270개는 전부 초록불이었습니다.

### 4. 라이브러리 표면을 포팅하지 않았다

`index.js` 가 내보내는 임베드용 표면(`noteMcpCall` · `noteRecommendation` · `machineActivity`)은
Go 로 옮기지 않았습니다. 현행 Node 는 다른 서버에 라우트를 얹는 라이브러리로 쓸 수 있지만
Go 바이너리에는 그 호스트가 없습니다. 필요해지면 추가할 자리입니다.

`requireRole` 도 재현하지 않았습니다 — 현행은 항상 `true` 이고 스코프 판정은 게이트 한 곳
(`httpapi/server.go`)이 집니다. 응답은 같습니다.

### 5. pg 커넥션 보관함을 손으로 만들었다

`pgxpool` 도 `pgx/v5/stdlib` 도 `github.com/jackc/puddle/v2` 를 요구하는데, 웨이브 당시
`go.mod`/`go.sum` 오너십 때문에 추가하지 못했습니다. 기본 `pgx` 위에 동시 상한·유휴 재사용·고장
커넥션 폐기만 하는 얇은 보관함을 두었습니다.

**전환 조건:** 리스크 1 의 pg 실측에서 커넥션 고갈·누수가 관측되면 `puddle/v2` 를 넣고
`pgxpool` 로 바꿉니다. `DB` 인터페이스가 안 변하므로 `internal/db/pg.go` 한 파일만 고치면 됩니다.

### 6. 알려진 JS↔Go 동작 차이 (골든에는 안 닿음 — 실측 확인)

숨기지 않고 적습니다. 골든이 안 밟는다는 것이 "없다"는 뜻은 아닙니다.

| 차이 | 발현 조건 | 골든 |
|---|---|---|
| counters 동점 키의 생존 순서 | 한 축에 동점으로 80개 초과, 또는 한 세션 총 400개 초과 | 안 닿음 (실측 최대 축 3 · 세션 14) |
| 존 없는 타임스탬프 해석 (Go=UTC, JS=로컬) | `started_at` 에 존이 없을 때 | 안 닿음 (시드 8세션 전부 `...Z`) |
| 문자열 길이 단위 (JS=UTF-16, Go=룬) | 상한 40 근처의 이모지(astral) 키워드 | 한글·ASCII 는 동일 |
| `unpriced` 정렬 (JS=UTF-16, Go=UTF-8 바이트) | 비-ASCII 모델명 | 현행 모델명 전부 ASCII |

### 7. `intake` 와 `store` 가 상수를 각자 정의한다

`internal/intake` 는 내부 import 가 금지되어(순수 패키지) `CounterKinds` ·
`MaxSeriesPerSession(200)` · `MaxCountersPerSession(400)` 을 다시 정의합니다. 현행 JS 는
`lib/store.js` 것을 `require` 합니다.

**갈라지면 인테이크가 저장 계층이 받지 않는 행을 만듭니다.** 둘 중 하나를 고칠 때 반드시 함께
고칩니다. 현재 값은 일치합니다.

### 8. 이 전환과 무관하게 원래 비어 있던 것 — 수집기

`POST /api/usage` 로 보고를 올리는 클라이언트는 **여전히 이 레포에 없습니다.** 포팅 전에도
없었습니다. 원천 데이터는 각 PC 의 `~/.claude/projects/<슬러그>/<sessionId>.jsonl` 이고 필드가
인테이크 페이로드와 거의 1:1 로 맞습니다(`message.usage.*` → `input`/`output`/`cacheRead`/
`cacheCreate`, `cache_creation.ephemeral_1h_input_tokens` → `series[].cc1h`).

대시보드는 완성이지만 데이터가 들어오는 파이프가 없으므로, **실사용 전 첫 할 일**입니다.

### 9. 별건으로 미룬 것

`Cache-Control: no-cache` 를 유지했습니다. `_next/static/*` 는 파일명이 콘텐츠 해시라
`immutable` 로 굳혀도 안전하고 성능상 맞지만, 지금 바꾸면 "옛 화면"이 뜰 때 캐시 탓인지 동기화
누락인지 구분이 어려워집니다. 통합이 안정된 뒤 나눕니다.

---

## 참고 문서

- [`go/CONTRACT.md`](go/CONTRACT.md) — 패키지 경계·시그니처의 단일 출처 + 개정 이력 3건
- [`contract/README.md`](contract/README.md) — 골든이 무엇을 대조하고 시드가 어떤 함정을 밟는가
- [`web/README.md`](web/README.md) — 프런트 빌드·서빙·검증
- [`README.md`](README.md) — 현행 Node 서버(도구의 성격과 데이터 정책은 여기가 단일 출처)
