package store

/*
 * 도메인 구조체 — **JSON 태그를 달지 않는다.**
 *
 * 응답 shape 는 httpapi 가 소유한다. 여기서 태그를 달면 저장 계층 변경이 곧 API 변경이 되어,
 * 스키마를 고칠 때마다 화면이 조용히 깨진다(CONTRACT.md).
 *
 * 널 가능성이 뜻을 갖는 자리는 포인터다. 이 레포에서 "모른다"와 "없다"는 다른 사실이고,
 * 그 둘을 뭉치면 화면이 단정한다.
 */

// ── 쓰기(인테이크) 입력 ────────────────────────────────────────────────

// SessionInput 은 세션 한 건의 절대값이다(델타가 아니다).
type SessionInput struct {
	SessionID string
	Machine   string
	Username  string
	Project   string
	Model     string // 그 세션의 **최빈 모델 1개** — 모델 축이 아니다

	Input       int64
	Output      int64
	CacheRead   int64
	CacheCreate int64
	WebSearch   int64
	WebFetch    int64
	Turns       int64

	StartedAt string
	EndedAt   string // 구버전 수집기는 안 보낸다 → 빈 값이면 NULL 로 남아 "모른다"가 보인다

	// NoTsTurns 는 시각이 없어 series 에 못 올린 턴 수다.
	// nil = 구버전 수집기라 **모른다** · 0 = 전 턴에 시각이 있었다. 뭉치면 안 된다.
	NoTsTurns *int64
}

// SeriesRow 는 (시간, 모델) 버킷 하나다.
type SeriesRow struct {
	Hour  string // YYYY-MM-DDTHH
	Model string

	Input       int64
	Output      int64
	CacheRead   int64
	CacheCreate int64
	CC5m        int64
	CC1h        int64
	Turns       int64

	ToolErrors    int64
	StopMaxTokens int64
	StopRefusal   int64
	LatencyMsSum  int64
	LatencyMsMax  int64
	LatencyTurns  int64
}

// SeriesInput 은 한 세션의 버킷 묶음이다.
type SeriesInput struct {
	SessionID string
	Username  string
	Machine   string
	Project   string
	Rows      []SeriesRow
}

// CounterRow 는 축(kind)별 카운터 한 줄이다.
type CounterRow struct {
	Kind  string
	Key   string
	Count int64
}

// CountersInput 은 한 세션의 카운터 묶음이다.
type CountersInput struct {
	SessionID string
	Username  string
	Machine   string
	StartedAt string
	Rows      []CounterRow
}

// CounterBumpInput 은 서버가 직접 세는 축의 +1 이다.
type CounterBumpInput struct {
	Kind     string
	Key      string
	Day      string // 비면 오늘
	Username string
	Machine  string
}

// RecoInput 은 추천 관측 1건이다. 목표 **원문은 받지 않는다** — 호출부가 토큰으로 바꿔 넘긴다.
type RecoInput struct {
	GoalTokens []string
	Agent      string
	Skills     []string
	Score      float64
	Source     string
	Username   string
}

// ── 읽기(집계) 결과 ────────────────────────────────────────────────────

/*
 * TotalsResult 는 전체 규모다. 화면 상단 요약과 "데이터가 아직 없음" 판정에 쓴다.
 *
 * ⚠ 이름이 CONTRACT.md 와 다르다. 계약은 `func Totals(ctx) (Totals, error)` 로 적혀 있는데
 * Go 에서는 같은 패키지에 이름이 같은 타입과 함수가 공존할 수 없다 — 그대로 옮기면 컴파일이
 * 되지 않는다. 함수 이름(go-http 가 부르는 쪽)을 계약대로 두고 타입을 옮겼다.
 * 계약 문서의 정오표로 PM 에게 보고한다.
 */
type TotalsResult struct {
	Sessions    int
	Input       int64
	Output      int64
	CacheRead   int64
	CacheCreate int64
	Users       int
	Machines    int
}

// DayRow 는 일별 토큰 사용량이다. 캐시 읽기를 따로 둔다(합치면 비용이 왜곡된다).
type DayRow struct {
	Day         string
	Input       int64
	Output      int64
	CacheRead   int64
	CacheCreate int64
	Sessions    int
}

// UserRow 는 사람별 사용량이다 — 좌석·비용 배분의 근거.
type UserRow struct {
	Username    string
	Input       int64
	Output      int64
	CacheRead   int64
	CacheCreate int64
	Turns       int64
	Sessions    int
}

// Share 는 한 모델 행에서 **근거별 몫**이다.
// 밝히지 않으면 근사를 정확한 값으로 위장하는 것이고, 그게 이번 결함의 본질이었다.
type Share struct {
	Input       int64
	Output      int64
	CacheRead   int64
	CacheCreate int64
	Sessions    int
}

// ModelRow 는 모델별 집계 한 행이다.
//
// Input/Output/… 은 FromSeries + FromSession 이다(불변식이며 테스트가 못박는다).
type ModelRow struct {
	Model       string
	Input       int64
	Output      int64
	CacheRead   int64
	CacheCreate int64
	// Sessions 는 이 모델이 등장한 세션 수다. 모델이 섞인 세션은 여러 행에 세어지므로
	// **열의 합이 총 세션 수를 넘을 수 있다** — 화면이 그 사실을 말한다.
	Sessions int

	FromSeries  Share // ① usage_series 의 모델별 **정확한** 값
	FromSession Share // ②③ 세션 최빈 모델 귀속(근사)
}

// ModelAxisUser 는 사람 한 명의 모델 축 커버리지다.
type ModelAxisUser struct {
	Username   string
	Sessions   int
	WithSeries int
	// NoTsTurns 는 시각이 없어 series 에 못 올린 턴 수, NoTsUnknown 은 그 값을 안 보낸 세션 수다.
	// 커버리지가 낮은 것만 보면 사람은 "보고가 안 온다"로 읽는다 — 보고는 오고 있고 시각이
	// 없을 뿐이라는 구분이 없으면 엉뚱한 데(토큰·서버 주소)를 판다.
	NoTsTurns   int64
	NoTsUnknown int
}

// ModelAxis 는 "이 표의 어디까지가 정확한 값인가"의 근거다.
type ModelAxis struct {
	Sessions   int
	WithSeries int
	Users      []ModelAxisUser
	// OverSessions 는 series 합이 세션 행보다 큰 세션 수다. 정상이면 0 이다.
	// >0 이면 그만큼이 ③에서 0 으로 끊겼다는 뜻 — 조용히 두지 않는다.
	OverSessions int
}

// KeyRow 는 축별 상위 키 한 줄이다.
type KeyRow struct {
	Key      string
	Count    int64
	Sessions int
	Users    int
}

// KeyCount 는 키와 횟수만 담는다.
type KeyCount struct {
	Key   string
	Count int64
}

// UserKeys 는 사용자별 축 집계다 — "누가 어떤 서브에이전트·스킬을 쓰는가".
type UserKeys struct {
	Username string
	Total    int64
	Items    []KeyCount
}

// Reporter 는 발신처(머신)별 수집 커버리지다. "왜 데이터가 없나"를 화면이 답하게 한다.
type Reporter struct {
	Machine        string
	Username       string
	Sessions       int
	LastReportedAt string
	LastStartedAt  string
	SendsSeries    bool
}

// QualityTotals 는 품질 축 합계다.
// SessionsWithSeries 를 함께 내보내 화면이 "이 지표는 세션 N/M 만 덮는다"고 정직하게 말하게 한다.
type QualityTotals struct {
	SessionsWithSeries int
	Turns              int64
	ToolErrors         int64
	StopMaxTokens      int64
	StopRefusal        int64
	LatencyMsSum       int64
	LatencyMsMax       int64
	LatencyTurns       int64
}

// Session 은 세션 원행이다. 분포·비용·드릴다운이 공유하는 단일 조회구.
//
// 시각 셋이 포인터인 이유: 안 보낸 값을 0 이나 "" 로 지어내지 않는다 — "모른다"가 그대로 보여야 한다.
type Session struct {
	SessionID   string
	Machine     string
	Username    string
	Project     string
	Model       string
	Input       int64
	Output      int64
	CacheRead   int64
	CacheCreate int64
	WebSearch   int64
	WebFetch    int64
	Turns       int64
	StartedAt   *string
	EndedAt     *string
	ReportedAt  *string
}

// Bucket 은 시간 버킷 원행이다.
type Bucket struct {
	SessionID   string
	Hour        string
	Model       string
	Input       int64
	Output      int64
	CacheRead   int64
	CacheCreate int64
	// TTL 분해. 배수가 1.25배(5분) vs 2배(1시간)로 1.6배 차이 나므로 뭉뚱그리면 비용이 그만큼 틀린다.
	CacheCreate5m int64
	CacheCreate1h int64
	Turns         int64
	ToolErrors    int64
	StopMaxTokens int64
	StopRefusal   int64
	LatencyMsSum  int64
	LatencyMsMax  int64
	LatencyTurns  int64
	Username      string
	Machine       string
	Project       string
}

// Gap 은 추천 공백 토큰 한 줄이다.
type Gap struct {
	Token string
	Count int
}

// RecoSummary 는 추천 전환 요약이다 — 몇 건 중 몇 건이 매칭 실패였나.
type RecoSummary struct {
	Total int
	Miss  int
}

// Filter 는 원행 조회구의 필터다. **값만 바인딩한다** — 컬럼명·정렬축을 문자열로 이어 붙이지
// 않으므로 주입 표면이 0 이다.
type Filter struct {
	From     string // YYYY-MM-DD (형식이 아니면 무시한다)
	To       string // YYYY-MM-DD
	Username string
	Model    string
	Limit    int // 0 이면 기본값
}

// GateKey 는 "게이트로 보이는 명령" 한 줄이다.
type GateKey struct {
	Key   string
	Count int64
	// Certain 은 그 이름만으로 게이트인지다. **빨간 판정의 유일한 근거**이며,
	// false 는 "인자가 있어야 게이트인데 이 축엔 인자가 없다"는 뜻이다(판정하지 않는다).
	Certain bool
}

// Activity 는 머신 한 대의 최근 창 활동이다.
type Activity struct {
	Sessions      int
	LastSessionAt string
	Bash          int64
	GateTotal     int64 // 후보 전체(확실 + 불확실) — 사람이 내역을 보고 판단하는 축
	GateKeys      []GateKey
	CertainTotal  int64 // 이름만으로 게이트인 것 — 빨간 판정의 유일한 근거
	CertainKeys   []KeyCount
	WindowDays    int
	SinceDay      string
}

// PruneResult 는 보존 정리 결과다.
type PruneResult struct {
	Removed int
	Cutoff  string
	Days    int
	Kept    int
}
