// Package payload 는 수집기가 `POST /api/usage` 로 보내는 본문의 모양이다.
//
// 이 모양은 서버(`go/internal/intake`)의 NormPayload 가 **읽는 것과 정확히 같아야** 한다.
// 서버는 모르는 키를 조용히 버리므로 여분 필드가 사고를 내지는 않지만, 이름이 어긋나면
// 그 값이 통째로 0 으로 저장된다(거부가 아니라 침묵이라 더 나쁘다). 그래서 JSON 태그는
// 서버 NormSession 이 읽는 키(`id`·`cacheRead`·`cc1h` 등)에 한 글자도 틀리지 않게 맞춘다.
//
// 표준 라이브러리 말고 아무것도 import 하지 않는다.
package payload

// Report 는 보고 본문 하나다. 세션이 여럿이면 한 번에 실어 보낸다(서버 상한은 50).
//
// User·Machine 은 페이로드 수준의 귀속이다 — 세션이 자기 값을 갖고 있으면 서버가 그쪽을
// 쓰고, 없으면 이 값으로 채운다. 최종 귀속은 어차피 서버가 머신 매핑으로 덮으므로(권위는
// 서버에 있다) 여기서는 "가장 그럴듯한 기본값"만 준다.
type Report struct {
	User     string    `json:"user"`
	Machine  string    `json:"machine"`
	Sessions []Session `json:"sessions"`
}

// Session 은 트랜스크립트 하나에서 뽑은 세션 합계다.
//
// 토큰 필드는 **절대값**이다(델타가 아니다). 서버가 session_id 로 UPSERT 하며 최신 절대값으로
// 덮으므로, 같은 세션을 몇 번 다시 보내도 값이 부풀지 않는다 — 그것이 이 수집기가 재실행에
// 안전한 이유다(멱등성은 서버의 session_id 키가 진다).
type Session struct {
	ID string `json:"id"`

	// Platform 은 이 세션이 어느 CLI 에서 나왔는지다(`claude` | `codex`).
	//
	// 왜 Report 가 아니라 세션에 붙나: 한 번의 보고에 두 플랫폼의 세션이 섞여 실린다.
	// 보고 수준에 두면 그 값이 어느 세션을 가리키는지 말할 수 없다.
	//
	// 서버는 이 필드를 **선택적으로** 받는다 — 없으면 `claude` 로 본다(구버전 수집기 호환).
	// 그래서 omitempty 여도 안전하지만, 이 수집기는 항상 명시해 서버의 기본값에 기대지 않는다.
	Platform string `json:"platform,omitempty"`

	/*
	 * Runtime 은 이 세션이 **로컬 모델**로 돌았는지다(`local` 또는 부재).
	 *
	 * platform 과 다른 축이다 — platform 은 "어느 도구"이고 이건 "어디서 돌았나"다. 로컬
	 * 모델을 클라우드 에이전트가 물고 돌면 platform 은 여전히 `codex` 이고, 그 사실만으로는
	 * 클라우드 세션과 구별되지 않는다. 그래서 `codex-local` 같은 합성 platform 을 만들지
	 * 않고 축을 하나 더 둔다(docs/PLAN-local-llm.md §2.2·§2.3).
	 *
	 * ⚠ **omitempty 가 하위호환의 전부다.** 서버는 이 키를 아직 읽지 않고(모르는 키를 조용히
	 *   버린다), 로컬 세션이 하나도 없으면 이 필드는 직렬화되지 않아 **보고 본문이 바이트
	 *   동일하다.** 골든 스냅샷이 흔들리지 않는 이유가 이것이다.
	 *
	 * 판정은 `internal/runtime` 이 엔드포인트 하나만 보고 하며, 호스트명·포트·경로는
	 * 여기까지 오지 않는다 — 이 필드가 받는 것은 낱말 하나다.
	 */
	Runtime string `json:"runtime,omitempty"`

	Username  string `json:"username,omitempty"`
	Machine   string `json:"machine,omitempty"`
	StartedAt string `json:"startedAt,omitempty"`
	EndedAt   string `json:"endedAt,omitempty"`
	Project   string `json:"project,omitempty"`
	Model     string `json:"model,omitempty"`

	Input       int64 `json:"input"`
	Output      int64 `json:"output"`
	CacheRead   int64 `json:"cacheRead"`
	CacheCreate int64 `json:"cacheCreate"`

	/*
	 * 고속 모드(fast mode) 몫 — 총량의 **부분집합**이다(서버 intake 의 longNat 이 같은
	 * 불변식으로 검증한다: 0 <= fast <= 해당 총량).
	 *
	 * 왜 보내는가: 고속 모드는 같은 모델에 2배 단가가 붙는다(Claude Opus 5 고속 $10/$50 vs
	 * 표준 $5/$25, 캐시 배수는 그 위에 얹힌다). 이 몫을 안 보내면 서버가 전부 표준가로 계산해
	 * 고속 세션의 비용이 **절반**으로 나온다. 원천은 트랜스크립트의 `usage.speed` 다.
	 *
	 * omitempty 를 쓰지 않는다 — 0 은 "표준 속도였다"는 관측이고, 필드가 사라지면 서버가
	 * "안 보냈다"와 구분할 수 없다. 둘의 계산 결과는 같지만 의미가 달라서 그 구분을 남긴다.
	 */

	/*
	 * 계단(롱컨텍스트) 몫 — 총량의 **부분집합**이다(서버 intake 의 longNat 이 같은 불변식으로
	 * 검증한다: 0 <= long <= 해당 총량).
	 *
	 * 판정: 한 요청의 **입력 컨텍스트**가 임계를 넘으면 그 요청의 입력·출력·캐시가 **전부**
	 * 롱 단가다(공식 문구: "all tokens (input and output) are charged at long context rates").
	 * 임계는 공급사마다 다르다 — OpenAI 272K · Google 200K. Anthropic 4.6+ 는 1M 컨텍스트가
	 * 표준가라 계단이 없어 claude 수집기는 이 값을 채우지 않는다.
	 *
	 * 판정을 수집기가 하는 이유: 서버는 **집계된 행**을 받아 "이 요청이 임계를 넘었는가"를
	 * 알 수 없다(하루 합계가 272K 를 넘는 것과 한 요청이 넘는 것은 다른 얘기다).
	 * 요청 단위를 보는 것은 세션 로그를 읽는 이쪽뿐이다.
	 */
	InputLong       int64 `json:"inputLong"`
	OutputLong      int64 `json:"outputLong"`
	CacheReadLong   int64 `json:"cacheReadLong"`
	CacheCreateLong int64 `json:"cacheCreateLong"`

	InputFast       int64 `json:"inputFast"`
	OutputFast      int64 `json:"outputFast"`
	CacheReadFast   int64 `json:"cacheReadFast"`
	CacheCreateFast int64 `json:"cacheCreateFast"`
	WebSearch       int64 `json:"webSearch"`
	WebFetch        int64 `json:"webFetch"`
	Turns           int64 `json:"turns"`

	// NoTsTurns 는 시각을 파싱하지 못해 series 버킷에 올리지 못한 턴 수다. **관측 축이다.**
	//
	// 포인터인 이유: 서버가 nil("모른다", 구버전 수집기)과 0("안다, 0이다")을 다르게 취급한다.
	// 이 수집기는 항상 값을 세므로 항상 0 이상의 값을 실어 nil 이 아님을 밝힌다.
	NoTsTurns *int64 `json:"noTsTurns,omitempty"`

	// 개발 지표(파생) — Edit/Write/MultiEdit 에서 센 줄 수와 편집 결과(accept/reject).
	// 코드 내용은 싣지 않는다(줄 수·횟수만). omitempty 로 구버전 수집기와 호환.
	LinesAdded    int64 `json:"linesAdded,omitempty"`
	LinesRemoved  int64 `json:"linesRemoved,omitempty"`
	EditsAccepted int64 `json:"editsAccepted,omitempty"`
	EditsRejected int64 `json:"editsRejected,omitempty"`

	// Counters 는 { 축: { 키: 횟수 } } 다. 서버 normCounters 가 이 객체 모양을 기본으로 받는다.
	Counters map[string]map[string]int64 `json:"counters"`

	// Series 는 시간×모델 버킷이다. 없으면 그냥 없는 것이다(서버가 400 을 내지 않는다).
	Series []Bucket `json:"series"`
}

// Bucket 은 시간(YYYY-MM-DDTHH, UTC)×모델 버킷이다. 필드 이름은 서버 normSeries 가 읽는 키다.
type Bucket struct {
	Hour  string `json:"hour"`
	Model string `json:"model"`

	Input       int64 `json:"input"`
	Output      int64 `json:"output"`
	CacheRead   int64 `json:"cacheRead"`
	CacheCreate int64 `json:"cacheCreate"`

	/*
	 * 고속 모드(fast mode) 몫 — 총량의 **부분집합**이다(서버 intake 의 longNat 이 같은
	 * 불변식으로 검증한다: 0 <= fast <= 해당 총량).
	 *
	 * 왜 보내는가: 고속 모드는 같은 모델에 2배 단가가 붙는다(Claude Opus 5 고속 $10/$50 vs
	 * 표준 $5/$25, 캐시 배수는 그 위에 얹힌다). 이 몫을 안 보내면 서버가 전부 표준가로 계산해
	 * 고속 세션의 비용이 **절반**으로 나온다. 원천은 트랜스크립트의 `usage.speed` 다.
	 *
	 * omitempty 를 쓰지 않는다 — 0 은 "표준 속도였다"는 관측이고, 필드가 사라지면 서버가
	 * "안 보냈다"와 구분할 수 없다. 둘의 계산 결과는 같지만 의미가 달라서 그 구분을 남긴다.
	 */

	/*
	 * 계단(롱컨텍스트) 몫 — 총량의 **부분집합**이다(서버 intake 의 longNat 이 같은 불변식으로
	 * 검증한다: 0 <= long <= 해당 총량).
	 *
	 * 판정: 한 요청의 **입력 컨텍스트**가 임계를 넘으면 그 요청의 입력·출력·캐시가 **전부**
	 * 롱 단가다(공식 문구: "all tokens (input and output) are charged at long context rates").
	 * 임계는 공급사마다 다르다 — OpenAI 272K · Google 200K. Anthropic 4.6+ 는 1M 컨텍스트가
	 * 표준가라 계단이 없어 claude 수집기는 이 값을 채우지 않는다.
	 *
	 * 판정을 수집기가 하는 이유: 서버는 **집계된 행**을 받아 "이 요청이 임계를 넘었는가"를
	 * 알 수 없다(하루 합계가 272K 를 넘는 것과 한 요청이 넘는 것은 다른 얘기다).
	 * 요청 단위를 보는 것은 세션 로그를 읽는 이쪽뿐이다.
	 */
	InputLong       int64 `json:"inputLong"`
	OutputLong      int64 `json:"outputLong"`
	CacheReadLong   int64 `json:"cacheReadLong"`
	CacheCreateLong int64 `json:"cacheCreateLong"`

	InputFast       int64 `json:"inputFast"`
	OutputFast      int64 `json:"outputFast"`
	CacheReadFast   int64 `json:"cacheReadFast"`
	CacheCreateFast int64 `json:"cacheCreateFast"`
	CC5m            int64 `json:"cc5m"`
	CC1h            int64 `json:"cc1h"`

	Turns         int64 `json:"turns"`
	ToolErrors    int64 `json:"toolErrors"`
	StopMaxTokens int64 `json:"stopMaxTokens"`
	StopRefusal   int64 `json:"stopRefusal"`
	LatencyMsSum  int64 `json:"latencyMsSum"`
	LatencyMsMax  int64 `json:"latencyMsMax"`
	LatencyTurns  int64 `json:"latencyTurns"`
}
