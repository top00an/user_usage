# Go 포팅 — 동결 계약

> ⚠ **역사 문서.** 이 문서는 2026-08 완료된 **Node→Go 포팅 계약**이다. 본문의 `lib/*.js`·`server.js`·`public/js/*` 참조는 포팅 당시의 **구(舊) Node 소스**(현재 삭제됨)를 가리키는 이력이며, 현행 소스가 아니다. Go 패키지 경계·시그니처의 계약으로는 여전히 유효하다.


이 문서가 **패키지 경계와 시그니처의 단일 출처**다. 오너는 자기 패키지 안에서 자유롭지만,
여기 적힌 외부 표면은 **바꾸지 않는다.** 바꿔야 할 근거가 생기면 코드가 아니라 이 파일을 먼저
고치고 PM 에게 알린다 — 네 명이 같은 워킹트리에서 동시에 움직이므로, 시그니처가 조용히 바뀌면
다른 오너의 컴파일이 이유 없이 깨진다.

## 합격 기준은 하나다

```bash
npm run contract:verify -- --base http://127.0.0.1:8080
```

`contract/golden/` 44개 스냅샷과 **상태코드·content-type·JSON 본문이 전부 같아야** 한다.
Go 쪽 단위 테스트가 아무리 초록색이어도 이게 빨간불이면 완료가 아니다. 반대도 참이다 —
이 게이트가 초록불이면 내부 구조는 오너 마음이다.

`npm test`(구 Node 208개)는 **건드리지 않는다.** 포팅 중에도 Node 서버는 살아 있어야 하고,
그것이 골든의 출처이기 때문이다.

## 레이아웃과 오너십

**파일 하나에 오너는 정확히 한 명이다.** 남의 파일은 읽기만 한다.

```
go/
  go.mod  go.sum                      [PM]
  CONTRACT.md                         [PM]  ← 이 파일
  cmd/usage-server/main.go            [go-http]
  internal/config/                    [go-http]
  internal/httpapi/                   [go-http]
  internal/db/                        [go-core]
  internal/tenant/                    [go-core]
  internal/store/                     [go-core]
  internal/cost/                      [go-pure]
  internal/stats/                     [go-pure]
  internal/tz/                        [go-pure]
  internal/intake/                    [go-pure]
  internal/identity/                  [go-core]
web/                                  [web-next]
```

의존 방향은 **한 방향뿐이다.** 역방향 import 가 생기면 순환이 되고 Go 는 컴파일을 거부한다:

```
httpapi ──▶ store ──▶ db ──▶ tenant
   │          │
   └──▶ cost, stats, tz, intake, identity
```

`cost` · `stats` · `tz` · `intake` 는 **아무것도 import 하지 않는다**(표준 라이브러리 제외).
순수 함수만 담는다 — 그래서 테이블 테스트로 완결되고, 그것이 이 포팅에서 가장 싸게 확신을
얻는 자리다.

---

## internal/tenant  [go-core]

Node 의 `AsyncLocalStorage` 를 `context.Context` 로 옮긴다. Go 에서는 이쪽이 더 자연스럽다 —
암묵 전파가 아니라 명시 인자라 "감싸는 것을 잊었다"가 컴파일에서 잡힌다.

```go
package tenant

// Key 는 컨텍스트 키다. 외부에서 만들 수 없게 비공개 타입으로 둔다.
func With(ctx context.Context, tenant string) context.Context
func From(ctx context.Context) string   // 없으면 "default"
```

## internal/db  [go-core]

```go
package db

type Dialect string
const (DialectSQLite Dialect = "sqlite"; DialectPostgres Dialect = "postgres")

// Rows 는 컬럼명 → 값 맵이다. Node 의 q().all()/get() 과 같은 계약.
type Row map[string]any

type DB interface {
    Query(ctx context.Context, sql string, args ...any) ([]Row, error)
    QueryRow(ctx context.Context, sql string, args ...any) (Row, error)  // 없으면 (nil, nil)
    Exec(ctx context.Context, sql string, args ...any) error
    Tx(ctx context.Context, fn func(context.Context) error) error
    Dialect() Dialect
    Close() error
}

func Open(ctx context.Context, cfg Options) (DB, error)

type Options struct {
    Mode      string // "local" | "remote"
    DataDir   string // local 전용
    URL       string // remote 전용
}
```

**자리표시자는 SQL 본문에 `?` 로 쓰고 pg 드라이버가 `$n` 으로 치환한다** — 현행
`lib/db/sql.js` 의 `toPg` 와 같은 규칙이고, **문자열 리터럴 안의 `?` 는 건드리지 않는다.**
이 규칙을 어기면 SQL 을 백엔드마다 두 벌 쓰게 되고, 그 순간 한 벌만 고쳐지는 날이 온다.

pg 경로는 매 쿼리에서 `tenant.From(ctx)` 를 RLS 에 주입한다.

### rlsguard

```go
package db

type RoleRow struct { Role string; Super bool; BypassRLS bool }
type Verdict struct { OK bool; Inconclusive bool; Message string }

func CheckRLS(row *RoleRow) Verdict   // row == nil → Inconclusive
func Remedy(msg string) string
```

판정 불가(터널 미개통·DB 다운)는 **거부하지 않는다.** 붙지 못한 DB 는 노출도 없고, 여기서
죽이면 "터널을 먼저 뚫는다"는 정상 절차가 부팅 실패로 보인다. 대신 stderr 로 크게 남긴다.

## internal/store  [go-core]

구 Node `lib/store.js` 의 모든 공개 함수를 옮긴다. 반환은 도메인 구조체이며 **JSON 태그를 달지
않는다** — 응답 shape 는 `httpapi` 가 소유한다. 여기서 태그를 달면 저장 계층 변경이 곧 API
변경이 되어, 스키마를 고칠 때마다 화면이 조용히 깨진다.

```go
func Init(ctx context.Context, d db.DB) error

// 인테이크(쓰기)
func SessionUpsert(ctx, s SessionInput) error
func SeriesUpsert(ctx, in SeriesInput) error
func CountersUpsert(ctx, in CountersInput) error
func CounterBump(ctx, in CounterBumpInput) error

// 집계(읽기)
func Totals(ctx) (Totals, error)
func UsageByDay(ctx, days int) ([]DayRow, error)         // days 는 1..365 로 클램프
func UsageByUser(ctx) ([]UserRow, error)
func UsageByModel(ctx) ([]ModelRow, error)               // ①fromSeries ②③fromSession
func UsageModelAxis(ctx) (ModelAxis, error)
func TopKeys(ctx, kind string, limit int) ([]KeyRow, error)
func ByUser(ctx, kind string, limit int) ([]UserKeys, error)
func ReporterCoverage(ctx) ([]Reporter, error)
func SeriesQualityTotals(ctx, f Filter) (QualityTotals, error)
func SessionRows(ctx, f Filter) ([]Session, error)
func SessionByID(ctx, id string) (*Session, error)       // 없으면 (nil, nil)
func SeriesRows(ctx, f Filter) ([]Bucket, error)
func SeriesOf(ctx, id string) ([]Bucket, error)
func CountersOf(ctx, id string, kinds []string) (map[string][]KeyCount, error)
func RecommendationGaps(ctx, limit int) ([]Gap, error)
func RecommendationSummary(ctx) (RecoSummary, error)

// 보존
func PruneKeywords(ctx, days int, now time.Time) (int, error)
// PruneSeries 는 **포팅하되 호출부를 만들지 않는다** — 모델별 값의 소급 교정이
// 이 표가 온전하다는 데 기댄다(구 Node lib/store.js 주석이 단일 출처).

const (SessionRowsDefault = …; SessionRowsMax = …)   // 현행 값을 그대로 읽어 온다
var CounterKinds = []string{"tool","bash","slash","skill","agent","mcp","keyword"}
```

**`UsageByModel` 이 이 포팅의 최난도다.** 세 경로(①series 정확값 ②series 없는 세션의 최빈 모델
귀속 ③series 가 못 덮은 잔여)를 더해야 하고, **`①+②+③ == Totals` 불변식**이 깨지면 모델별만
작아져 사람에게는 "유실"로 보인다. 골든에 그 불변식이 박혀 있다. 구 Node `lib/store.js:377~502`
주석을 반드시 읽고 시작하라 — 왜 ②를 버리면 안 되는지가 실측치와 함께 적혀 있다.

## internal/identity  [go-core]

```go
func Init(ctx, d db.DB) error
func Resolve(ctx, machine, claimed string) (string, error)
func List(ctx) ([]Mapping, error)
func Set(ctx, in SetInput) (SetResult, error)   // 과거 행 소급 재스탬프 포함
func Remove(ctx, machine string) (bool, error)
func Unmapped(ctx) ([]string, error)
```

빈 username 은 **거부한다**(실수로 귀속을 지우지 못하게). 현행 테스트가 그 계약을 잡고 있다.

## internal/cost  [go-pure]

```go
package cost   // import 없음(표준 라이브러리만)

func CostOf(s Usage, table Table, mult Multipliers) Result
func Summarize(buckets []Usage) Result
func Pricing(today time.Time) Table
func PricedAt() string
func Multipliers() Multipliers
func NormalizeModel(m string) string
```

비용은 **저장하지 않고 읽을 때마다 계산한다.** 컬럼으로 굳히면 단가가 바뀌었을 때 과거 수치가
옛 단가에 묶인다. `config.json` 의 `usage.pricing` 이 시드 단가표를 **이긴다.**

모르는 모델은 `priced=false` 로 두고 이름을 `unpriced` 에 남긴다 — **조용히 $0 으로 처리하지
않는다.** 그러면 합계가 틀렸다는 사실이 화면에서 사라진다.

## internal/stats  [go-pure]

```go
func Summarize(xs []float64) Summary   // n, min, p50, p95, p99, max, avg
func QuantileSorted(sorted []float64, p float64) float64
```

빈 슬라이스에서 죽지 않는다. 부동소수 출력이 Node 와 **바이트 단위로** 같아야 하므로
(골든이 JSON 숫자를 그대로 비교한다) 분위수 보간식을 구 Node `lib/stats.js` 에서 그대로 옮겨라.
"같은 뜻의 다른 식"은 마지막 자리에서 갈린다.

## internal/tz  [go-pure]

```go
const DefaultOffsetMin = …   // 현행 값
func OffsetMin() int
func LocalDay(iso string, offsetMin int) string    // "YYYY-MM-DD"
func LocalHour(iso string, offsetMin int) string   // "YYYY-MM-DDTHH"
```

**고정 오프셋이다. IANA 시간대를 쓰지 않는다** — 매 행마다 시간대 변환을 하는 비용을 서머타임
없는 지역에 지불할 이유가 없다(구 Node `lib/tz.js` 의 결정). 서머타임 지역으로 옮길 일이 생기면
그때 바꾼다.

## internal/intake  [go-pure]

```go
func NormPayload(raw []byte) (Payload, error)
func NormSession(raw map[string]any) (Session, bool)
func NormKey(s string) string
func SafeKeyword(s string) (string, bool)   // false = 버린다
```

**`SafeKeyword` 가 이 레포의 신뢰 경계다.** 벤더 접두사 · 32자 이상 hex · 대소문자+숫자 섞인
24자 이상 · 이메일 · 접속 문자열 조각 · 10자리 이상 연속 숫자 · `키=값` 을 **저장 전에 버린다.**
판정은 언제나 **버리는 쪽으로** 기운다 — 수집기가 먼저 거른다는 전제이지만 신뢰하지 않고,
한 번 저장되면 지우는 비용이 훨씬 크다.

현행 정규식에 lookahead·backreference 가 **없음을 확인했다** → Go 의 `regexp`(RE2)로 그대로
옮겨진다. 옮긴 뒤 `test/usage-keyword-safety.test.js` 의 케이스를 전부 Go 테이블 테스트로
재현하라. 이 축만은 "대충 비슷하게"가 허용되지 않는다.

## internal/config + internal/httpapi + cmd  [go-http]

### 부팅 거부 게이트 — 하나라도 빠지면 조용한 사고다

```go
package config
func Read(env map[string]string) (Config, []error)   // 잘못된 설정은 **모아서** 돌려준다
```

| 거부 조건 | 왜 |
|---|---|
| `USAGE_ADMIN_TOKEN` 없음 | 사람별 사용량·비용이 무인증으로 열린다 |
| 토큰 16자 미만 | 짧은 토큰은 인증이 아니라 설정 실수다 |
| `USAGE_INTAKE_TOKEN == USAGE_ADMIN_TOKEN` | 분리한 것처럼 보이지만 아무것도 분리되지 않았다 |
| `USAGE_DB_MODE` 오타 | local 로 조용히 접지 않는다 |
| remote 인데 `DATABASE_URL` 없음 | |
| 포트가 WHATWG bad ports | 서버는 뜨고 curl 도 200 인데 **브라우저에서만** 죽는다 |

bad ports 목록은 구 Node `server.js:71~77` 을 그대로 옮긴다(기본 포트 4191 — **4190 이 아니다**).

### 인증

```go
type Auth struct { Via string; Scope string }   // Via: "header"|"cookie"  Scope: "admin"|"intake"
func Authenticate(r *http.Request, cfg Config) *Auth
```

규칙 셋. 전부 골든의 오류 스냅샷이 잡고 있다:

1. **상수시간 비교** — 양쪽을 프로세스마다 무작위인 키로 HMAC-SHA256 접은 뒤 `hmac.Equal`.
   길이가 달라도 타이밍으로 새지 않게.
2. `Authorization` 이 있는데 틀렸으면 **쿠키로 흘리지 않는다**(폴백이 있으면 게이트가 흐려진다).
3. **인테이크 토큰은 쿠키로 인정하지 않는다** — 그 보고자는 수집기이지 브라우저가 아니고,
   쿠키로 받아 주면 브라우저를 꾀어 임의 사용량을 밀어 넣는 자리가 생긴다.

그리고 **쿠키 자격증명으로는 상태변경을 태우지 않는다**(403). 브라우저는 임의 헤더를 붙일 수
없으므로 화면은 자연히 조회 전용이 되고, CSRF 표면이 아예 생기지 않는다.
인테이크 스코프는 `POST /api/usage` **하나만** 연다(그 외 403).

### 라우트 순서는 계약이다

`analytics` 가 `admin` 보다 **앞**이어야 한다 — admin 이 `/api/usage` 접두사를 통째로 소유하고
안 걸리면 404 를 직접 내므로, 뒤로 가면 관측 화면이 통째로 404 가 된다.

`readOnly`(=remote)에서는 인테이크를 등록하지 않고, admin 라우트도 GET/HEAD 만 통과시킨다.
나머지는 **405 가 아니라 404** 다 — 이 모드에서 그 엔드포인트는 "지금은 막혔다"가 아니라
**존재하지 않는다.**

### 정적 서빙

**경로 화이트리스트다.** 디렉터리를 열고 `..` 를 막는 대신 나갈 수 있는 URL 을 통째로 열거한다 —
그러면 경로 탈출이라는 문제 자체가 성립하지 않는다(정규화·심링크·인코딩을 고민할 자리가 없다).
`go:embed` 로 바이너리에 넣는다. CSP·`X-Frame-Options`·`nosniff`·`Referrer-Policy` 는
구 Node `server.js:289~293, 309~317` 을 그대로 옮긴다.

### 오류 응답

예상 못 한 예외의 **원문을 클라이언트로 보내지 않는다.** 대개 DB 드라이버 에러(테이블·컬럼명,
제약 이름, 때로는 접속 정보 조각)다. 원문은 stderr 로 — 그쪽은 운영자만 본다.
라우트가 의도해서 내는 400(검증 메시지)은 이 경로로 오지 않으므로 안내는 그대로 남는다.

---

## web/  [web-next]

Next.js App Router + React + TypeScript. 현행 `public/` 의 두 뷰를 옮긴다.

**인증은 현행을 유지한다** — 클라이언트가 토큰을 쿠키(`usage_tok`, `SameSite=Strict`,
https 일 때만 `Secure`)에 담고 브라우저가 싣는다. Next 서버가 토큰을 들고 프록시하는 안은
지금의 "CSRF 표면 없음" 설계를 다시 짜야 하고, 붙일 사내 시스템을 아직 모른다.

대신 **API 호출을 한 파일로 모은다**(`web/lib/api.ts`). 나중에 SSO·프록시로 갈아탈 때
고칠 자리가 거기 하나여야 한다. 컴포넌트가 `fetch` 를 직접 부르면 그 전환이 전면 수정이 된다.

지켜야 할 것:

- **`status` 로 분기한다, 문구로 하지 않는다.** 에러 문구는 사람이 읽는 글이라 언제든 다듬어지고,
  분기를 문구에 걸면 그때 화면이 조용히 틀린 쪽으로 넘어간다(구 Node `public/js/core.js` 의 `fail`).
- **근사값을 정확한 값으로 위장하지 않는다.** 모델별 표의 "근거" 열(`fromSeries`/`fromSession`),
  커버리지, `unpriced`, `ttlUnknownRows`, 키워드 보존 기한 — 전부 화면에 남는다.
  이걸 지우면 화면이 더 깔끔해지는데, 그게 이 도구가 하지 않기로 한 일이다.
- **탭 전환 시 낡은 응답 폐기.** 현행은 `seq` 토큰으로 한다. React 에서는 `AbortController` +
  `useEffect` 정리 함수가 자연스럽다. 없으면 탭을 빠르게 오갈 때 이전 탭 응답이 현재 탭을
  덮어쓴다 — 화면이 틀린 값을 보여주는데 아무 에러도 안 난다.
- 다크/라이트 토큰과 반응형은 `team-design` 스킬의 규율을 따른다.

빌드 산출물은 정적 export 로 뽑아 Go 바이너리가 `go:embed` 로 서빙할 수 있어야 한다.

---

# 개정 이력

계약은 코드보다 먼저 바뀐다. 오너가 발견한 불일치를 PM 이 여기 기록한다.

## 개정 1 — go-pure 웨이브 후 (PM 확인 완료)

Go 언어 제약과 구 Node JS 실제 시그니처 때문에 다음이 계약과 달라졌다. **모두 승인한다** —
호출 형태는 계약대로 유지되고 추가된 것뿐이다.

| 계약 원문 | 실제 | 왜 |
|---|---|---|
| `func Multipliers() Multipliers` + 타입 `Multipliers` | 타입만 **`Mult`** 로 | Go 는 한 패키지에서 같은 이름의 타입과 함수를 공존시키지 못한다. 호출부가 타이핑하는 것은 함수 이름이라 함수를 남겼다 |
| `NormKey(s string) string` | **`NormKeyOf(kind, raw)` 추가** | JS 는 `normKey(kind, raw)` 이고 축마다 규칙이 다르다(bash=basename · slash=첫 토큰 · keyword=소문자 · tool=공백 유지) |
| `Summarize(xs []float64) Summary` | **`SummarizeAny([]any, ...)` 추가** | 아래 ⚠ 참조 |
| `NormSession(raw)` | `NormSession(raw, ctx ...Ctx)` | 페이로드 수준 귀속(username·machine)이 필요하다. 가변 인자라 기존 호출 형태 유효 |
| `tz` 4함수 | **`WeekStart` · `WidenUTCRange` · `InRange` · `Label` 추가** | `routes/usage-analytics.js` 가 넷 다 쓴다 |

⚠ **`store`·`httpapi` 오너는 분포 계산에 `stats.SummarizeAny` 를 써라.**
`[]float64` 로 먼저 변환하면 `dropped`(관측이 아닌 값의 개수)가 0 으로 나가고, 화면이 표본이
깎인 사실을 말할 수 없게 된다. JS 의 규율은 `Number(null)===0` 이라 null 을 "0 이라는 관측"으로
둔갑시키지 않는 것인데, `[]float64` 경계를 넘는 순간 그 구분이 사라진다.

⚠ **`stats.QuantileSorted` 는 빈 슬라이스에서 0 을 돌려준다**(계약 반환형이 `float64` 라 null 을
낼 수 없다). "표본 없음"을 구분해야 하면 `Summarize` 를 쓸 것 — 그쪽은 `*float64`(null)로 낸다.

### 감시 항목: intake 와 store 의 상수 중복

`intake` 는 내부 import 가 금지되어 `CounterKinds` · `MaxSeriesPerSession(200)` ·
`MaxCountersPerSession(400)` 을 **자기 패키지에 다시 정의한다.** 구 Node JS 는 `lib/store.js` 것을
`require` 한다.

**갈라지면 인테이크가 저장 계층이 받지 않는 행을 만든다.** 둘 중 하나를 고칠 때 **반드시 함께**
고친다. 현재 값은 일치한다:
`['tool','bash','slash','skill','agent','mcp','keyword']` · 200 · 400.

### 알려진 JS↔Go 동작 차이 (골든에는 영향 없음 — PM 실측 확인)

숨기지 않고 적어 둔다. 골든이 안 밟는다는 것이 "없다"는 뜻은 아니다.

| 차이 | 발현 조건 | 골든 |
|---|---|---|
| counters 동점 키의 생존 순서 | 한 축에 동점 카운트로 **80개 초과** 또는 한 세션 총 **400개 초과** | ✅ 안 닿음 — 실측 최대 축 3개 · 세션 14개 |
| 존 없는 타임스탬프 해석 (Go=UTC, JS=로컬) | `started_at` 에 존이 없을 때 | ✅ 안 닿음 — 시드 8세션 전부 `...Z` |
| 문자열 길이 단위 (JS=UTF-16, Go=룬) | 상한 40 근처의 **이모지(astral)** 키워드 | 한글·ASCII 는 동일 |
| `unpriced` 정렬 (JS=UTF-16, Go=UTF-8 바이트) | 비-ASCII 모델명 | 현행 모델명 전부 ASCII |

### 단가 파일 경로 — go-http 가 배선할 것

JS 는 모듈 파일 기준(`__dirname/..`)으로 `config.json` 을 찾지만 컴파일된 바이너리엔 그 기준이
없다. Go 는 `USAGE_CONFIG` → **작업 디렉터리의 `config.json`** 순으로 읽는다.

⚠ **cwd 가 레포 루트가 아니면 단가가 조용히 시드로 떨어진다.** 배포와 골든 실행에서
`USAGE_CONFIG` 를 명시하라. (현재 레포에 `config.json` 은 **없다** — Node·Go 모두 시드 단가표를
쓰므로 골든 대조 조건은 동일하다. PM 실측 확인.)

## 개정 2 — go-core 웨이브 후 (PM 확인 완료)

| 계약 원문 | 실제 | 왜 |
|---|---|---|
| `func Totals(ctx) (Totals, error)` | 타입만 **`TotalsResult`** 로 | 개정 1 의 `Multipliers` 와 같은 Go 제약. 함수 이름을 남겼다 |
| `SeriesUpsert`/`CountersUpsert` 가 `error` 만 | **`SeriesUpsertN`·`CountersUpsertN`(`(int, error)`) 추가** | 인테이크 응답이 저장 개수를 싣는다(`{"ok":true,"sessions":N,"counters":N,"buckets":N}`). **골든을 맞추려면 go-http 는 `…N` 쪽을 부른다** |
| — | `RecommendationGapsAt` · `PruneKeywordsDetail` · `MachineActivity` 추가 | 구 Node JS 가 쓰는 표면 |

### ⚠ go-http 가 반드시 배선할 것 두 가지

**둘 다 안 걸면 조용히 틀린다** — 요청은 200 이고 화면도 정상으로 보인다.

1. **`db.SetTenantResolver(tenant.From)`** — 안 걸면 pg 의 모든 쿼리가 `default` 테넌트로 흐른다.
   remote 로 남의 DB 를 볼 때 그게 맞는다는 보장이 없다.
2. **RLS 판정은 `Verdict.Rejects()` 로만 분기한다.** `!v.OK` 로 분기하면 터널 미개통(판정 불가)에서
   부팅이 막혀, "터널을 먼저 뚫는다"는 정상 절차가 부팅 실패로 보인다.

### 보류한 것 — pg 커넥션 풀

go-core 가 `pgxpool` 대신 얇은 보관함을 손으로 만들었다(`go.sum` 이 PM 소유라 `puddle/v2` 를
추가할 수 없었다). **지금은 그대로 둔다** — 동시 상한·유휴 재사용·고장 커넥션 폐기가 있고
테스트가 붙어 있으며, `DB` 인터페이스가 안 변하므로 나중에 `internal/db/pg.go` 한 파일만 고치면
갈아탈 수 있다. 쓰지 않는 의존성을 미리 넣으면 `go mod tidy` 가 그것을 지우거나 사람이 그
불일치를 계속 설명해야 한다.

**전환 조건:** pg 왕복 실측에서 커넥션 고갈·누수가 관측되면 그때 `puddle/v2` 를 넣고 바꾼다.

### 아직 아무도 검증하지 않은 것 — pg 경로

**PostgreSQL 왕복은 실행 검증이 없다.** go-core 는 sqlite 로만 돌렸다(클러스터 없음).
SQL 은 양 방언 공통 문법으로 썼고 UPSERT 충돌 대상만 갈랐지만, 그것은 **코드 리뷰이지 실행
증거가 아니다.** 이 사실을 최종 보고의 잔여 리스크에 반드시 남긴다.

## 개정 3 — go-embed 웨이브 후 (PM 결정)

### 오너십 이탈 1건 — **승인함**

go-embed 가 `go/internal/httpapi/server_test.go`(go-http 소유)의 정적 테스트 2개를 삭제했다
(`TestStaticWhitelistCoversEveryShellAsset` · `TestHeadOnStaticSendsHeadersWithoutBody`).

**승인 근거:** 그 두 테스트는 **구 바닐라 프런트의 파일 목록을 손으로 열거**한다
(`/app.js` · `/js/core.js` · `/views/usagetrack.js` …). `webroot/` 를 Next.js 산출물로 교체하면
그 URL 들은 존재하지 않으므로 **반드시 빨개진다** — 브리프의 임무와 AC 가 이 파일에서 정면
충돌했고, 손대지 않고 게이트를 초록불로 만들 방법이 없었다.

보장은 약해지지 않고 **강해졌다.** 같은 축을 `static_test.go` 가 *생성된* 표에 대해 잰다:
표 == 임베드 집합 · 셸이 가리키는 자산 전부 200 · HEAD 계약(같은 이름으로 이전) ·
그리고 구 URL 7개가 이제 404 라는 것 자체. 손 표가 생성 표로 바뀌었으므로 다음 빌드에서
다시 깨지지 않는다. 기능 코드는 한 줄도 바뀌지 않았다.

### 정적 서빙 설계 변경 — 손 표 → embed FS 순회

`static.go` 는 이제 `init` 에서 `fs.WalkDir` 로 화이트리스트를 만든다. Next 산출물의 파일명이
콘텐츠 해시라 손 표는 빌드마다 깨진다.

**화이트리스트의 성질은 그대로다** — 나갈 수 있는 것은 바이너리에 박힌 파일뿐이고, 판정은 서버가
실제로 받은 문자열(`EscapedPath`) 위의 맵 조회 한 번이다. **정규화하지 않으므로** `/%2e%2e/x` 와
`/../x` 가 같은 것으로 접히는 자리가 없다. 즉 경로 탈출이라는 문제가 여전히 성립하지 않는다.

⚠ **`//go:embed all:webroot` 의 `all:` 은 필수다.** go:embed 는 `_`·`.` 로 시작하는 이름을 기본
적으로 건너뛰는데 Next 산출물의 본체가 전부 `_next/` 아래다. 빼면 셸만 나가고 스크립트가 통째로
빠지며, **그 증상은 404 가 아니라 빈 화면이다.** `TestEmbedIncludesNextUnderscoreDirs` 가 지킨다.

### 남는 구조적 리스크 — `webroot/` 는 커밋되는 빌드 산출물이다

`go:embed` 가 트리 안의 파일을 요구하므로 커밋을 피할 수 없다. `.gitignore` 의 주석이 정확히
*"빌드로 재현되는 것을 커밋하면 소스와 어긋난 채 배포되는 날이 온다"* 고 적고 있고,
`webroot/` 가 바로 그것이다.

방어를 네 겹 걸었다:

1. `scripts/build.sh` — 유일한 빌드 경로. `web 빌드 → webroot 동기화 → go build`. 파일 수를 세고 어긋나면 멈춘다
2. `static.go` — `index.html` 이 임베드에 없으면 **init 에서 죽는다**(셸 없는 바이너리가 조용히 뜨는 것보다 부팅 실패가 낫다)
3. `static_test.go` — `TestIndexHTMLReferencesOnlyEmbeddedAssets` 가 드리프트를 **화면에서 보이기 전에** 잡는다
4. `npm run verify:embed` — `build.sh` 재빌드 후 `git diff --exit-code -- go/internal/httpapi/webroot`.
   **CI 에 걸 것.** ⚠ `webroot/` 가 **커밋된 뒤에** 의미를 갖는다(미추적 상태에서는 전체가 신규 diff 로 나온다)

### 별건으로 미룬 것

`Cache-Control: no-cache` 를 유지했다. `_next/static/*` 는 파일명이 콘텐츠 해시라 `immutable` 로
굳혀도 안전하고 성능상 맞지만, 지금 바꾸면 "옛 화면"이 뜰 때 캐시 탓인지 동기화 누락인지
구분이 어려워진다. 통합이 안정된 뒤 나눈다.
