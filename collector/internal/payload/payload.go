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
	WebSearch   int64 `json:"webSearch"`
	WebFetch    int64 `json:"webFetch"`
	Turns       int64 `json:"turns"`

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
	CC5m        int64 `json:"cc5m"`
	CC1h        int64 `json:"cc1h"`

	Turns         int64 `json:"turns"`
	ToolErrors    int64 `json:"toolErrors"`
	StopMaxTokens int64 `json:"stopMaxTokens"`
	StopRefusal   int64 `json:"stopRefusal"`
	LatencyMsSum  int64 `json:"latencyMsSum"`
	LatencyMsMax  int64 `json:"latencyMsMax"`
	LatencyTurns  int64 `json:"latencyTurns"`
}
