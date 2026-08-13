// 응답 DTO — **httpapi 가 응답 shape 를 소유한다.**
//
// store 의 도메인 구조체에는 JSON 태그가 없다(그건 go-core 소유이기도 하다). 저장 계층 변경이
// 곧 API 변경이 되면 스키마를 고칠 때마다 화면이 조용히 깨진다. 그래서 변환을 여기 한 곳에 둔다 —
// 컬럼 이름이 바뀌어도 고칠 자리가 이 파일뿐이다.
//
// ⚠ 슬라이스는 **반드시 비어 있지 않은 슬라이스로 만든다**(make(..., 0, n)). nil 슬라이스는
//
//	JSON 에서 null 로 나가고, 화면은 `arr.map` 을 부르다 죽는다 — 그리고 그건 데이터가 없는
//	테넌트에서만 나타난다. 맵도 같다: nil 맵은 null, 빈 맵은 {}.
package httpapi

import (
	"github.com/tscorp/user-usage/internal/cost"
	"github.com/tscorp/user-usage/internal/identity"
	"github.com/tscorp/user-usage/internal/store"
)

/* ── 세션 ─────────────────────────────────────────────────────────────── */

type sessionDTO struct {
	SessionID string `json:"sessionId"`
	Machine   string `json:"machine"`
	Username  string `json:"username"`
	Project   string `json:"project"`
	Model     string `json:"model"`
	/*
	 * 이 세션을 만든 도구(claude|codex|gemini|antigravity|other).
	 *
	 * **빈 문자열로 나가지 않는다** — 저장 계층이 미지정 보고를 claude 로 채우고 허용목록 밖을
	 * other 로 접기 때문이다(store.NormalizePlatform 이 그 규칙의 단일 출처). 여기서 기본값을
	 * 다시 정하거나 빈 값을 통과시키면 규칙이 두 곳이 되고, 화면에는 /api/usage/platforms 에
	 * 없는 "미상" 범주가 생겨 드릴다운과 집계의 모집단이 갈린다.
	 */
	Platform    string `json:"platform"`
	Input       int64  `json:"input"`
	Output      int64  `json:"output"`
	CacheRead   int64  `json:"cacheRead"`
	CacheCreate int64  `json:"cacheCreate"`
	WebSearch   int64  `json:"webSearch"`
	WebFetch    int64  `json:"webFetch"`
	Turns       int64  `json:"turns"`
	// 포인터다 — nil 은 **모른다**이고 화면에 null 로 나간다. 빈 문자열로 접으면
	// "시각이 없다"와 "시각이 빈 문자열이다"가 같은 화면이 된다.
	StartedAt  *string `json:"startedAt"`
	EndedAt    *string `json:"endedAt"`
	ReportedAt *string `json:"reportedAt"`
}

func toSessionDTO(s store.Session) sessionDTO {
	return sessionDTO{
		SessionID: s.SessionID, Machine: s.Machine, Username: s.Username,
		Project: s.Project, Model: s.Model,
		// 저장 계층이 이미 정규화한 값을 **그대로** 옮긴다(기본값을 여기서 다시 정하지 않는다).
		Platform: s.Platform,
		Input:    s.Input, Output: s.Output, CacheRead: s.CacheRead, CacheCreate: s.CacheCreate,
		WebSearch: s.WebSearch, WebFetch: s.WebFetch, Turns: s.Turns,
		StartedAt: s.StartedAt, EndedAt: s.EndedAt, ReportedAt: s.ReportedAt,
	}
}

// sessionListItemDTO 는 목록 전용이다 — 비용 두 필드를 덧붙인다.
// usd 는 **단가 미등록이면 null** 이다. 0 으로 접으면 "공짜"와 "모른다"가 같은 값이 된다.
type sessionListItemDTO struct {
	sessionDTO
	USD    *float64 `json:"usd"`
	Priced bool     `json:"priced"`
}

/* ── 시간 버킷 ────────────────────────────────────────────────────────── */

type bucketDTO struct {
	SessionID     string `json:"sessionId"`
	Hour          string `json:"hour"`
	Model         string `json:"model"`
	Input         int64  `json:"input"`
	Output        int64  `json:"output"`
	CacheRead     int64  `json:"cacheRead"`
	CacheCreate   int64  `json:"cacheCreate"`
	CacheCreate5m int64  `json:"cacheCreate5m"`
	CacheCreate1h int64  `json:"cacheCreate1h"`
	Turns         int64  `json:"turns"`
	ToolErrors    int64  `json:"toolErrors"`
	StopMaxTokens int64  `json:"stopMaxTokens"`
	StopRefusal   int64  `json:"stopRefusal"`
	LatencyMsSum  int64  `json:"latencyMsSum"`
	LatencyMsMax  int64  `json:"latencyMsMax"`
	LatencyTurns  int64  `json:"latencyTurns"`
	Username      string `json:"username"`
	Machine       string `json:"machine"`
	Project       string `json:"project"`
}

func toBucketDTOs(bs []store.Bucket) []bucketDTO {
	out := make([]bucketDTO, 0, len(bs))
	for _, b := range bs {
		out = append(out, bucketDTO{
			SessionID: b.SessionID, Hour: b.Hour, Model: b.Model,
			Input: b.Input, Output: b.Output, CacheRead: b.CacheRead, CacheCreate: b.CacheCreate,
			CacheCreate5m: b.CacheCreate5m, CacheCreate1h: b.CacheCreate1h,
			Turns: b.Turns, ToolErrors: b.ToolErrors,
			StopMaxTokens: b.StopMaxTokens, StopRefusal: b.StopRefusal,
			LatencyMsSum: b.LatencyMsSum, LatencyMsMax: b.LatencyMsMax, LatencyTurns: b.LatencyTurns,
			Username: b.Username, Machine: b.Machine, Project: b.Project,
		})
	}
	return out
}

/* ── 집계 ─────────────────────────────────────────────────────────────── */

type totalsDTO struct {
	Sessions    int   `json:"sessions"`
	Input       int64 `json:"input"`
	Output      int64 `json:"output"`
	CacheRead   int64 `json:"cacheRead"`
	CacheCreate int64 `json:"cacheCreate"`
	Users       int   `json:"users"`
	Machines    int   `json:"machines"`
}

type dayDTO struct {
	Day         string `json:"day"`
	Input       int64  `json:"input"`
	Output      int64  `json:"output"`
	CacheRead   int64  `json:"cacheRead"`
	CacheCreate int64  `json:"cacheCreate"`
	Sessions    int    `json:"sessions"`
}

type userDTO struct {
	Username    string `json:"username"`
	Input       int64  `json:"input"`
	Output      int64  `json:"output"`
	CacheRead   int64  `json:"cacheRead"`
	CacheCreate int64  `json:"cacheCreate"`
	Turns       int64  `json:"turns"`
	Sessions    int    `json:"sessions"`
}

// shareDTO 는 모델별 값의 **근거**다. fromSeries 는 시간 버킷의 정확값, fromSession 은
// 세션 최빈 모델로 귀속한 근사값이다. 이 구분을 지우면 화면이 근사를 정확값으로 위장한다.
type shareDTO struct {
	Input       int64 `json:"input"`
	Output      int64 `json:"output"`
	CacheRead   int64 `json:"cacheRead"`
	CacheCreate int64 `json:"cacheCreate"`
	Sessions    int   `json:"sessions"`
}

type modelDTO struct {
	Model       string   `json:"model"`
	Input       int64    `json:"input"`
	Output      int64    `json:"output"`
	CacheRead   int64    `json:"cacheRead"`
	CacheCreate int64    `json:"cacheCreate"`
	Sessions    int      `json:"sessions"`
	FromSeries  shareDTO `json:"fromSeries"`
	FromSession shareDTO `json:"fromSession"`
}

type modelAxisUserDTO struct {
	Username   string `json:"username"`
	Sessions   int    `json:"sessions"`
	WithSeries int    `json:"withSeries"`
	NoTsTurns  int64  `json:"noTsTurns"`
	// NoTsUnknown 은 "구버전 수집기라 모른다"의 개수다 — 0(전 턴에 시각 있음)과 다른 사실이다.
	NoTsUnknown int `json:"noTsUnknown"`
}

type modelAxisDTO struct {
	Sessions     int                `json:"sessions"`
	WithSeries   int                `json:"withSeries"`
	Users        []modelAxisUserDTO `json:"users"`
	OverSessions int                `json:"overSessions"`
}

type keyDTO struct {
	Key      string `json:"key"`
	Count    int64  `json:"count"`
	Sessions int    `json:"sessions"`
	Users    int    `json:"users"`
}

type keyCountDTO struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}

type userKeysDTO struct {
	Username string        `json:"username"`
	Total    int64         `json:"total"`
	Items    []keyCountDTO `json:"items"`
}

// dispatchUserDTO 는 userKeysDTO 에 역할/범용 분해를 덧붙인다. generic 이 이번에 문제를
// 드러낸 축이다 — 역할 없는 범용 에이전트만 쓰는 사람이 보이지 않던 자리.
type dispatchUserDTO struct {
	userKeysDTO
	Generic int64 `json:"generic"`
	Role    int64 `json:"role"`
}

type reporterDTO struct {
	Machine  string `json:"machine"`
	Username string `json:"username"`
	Sessions int    `json:"sessions"`
	// 문자열이다(빈 문자열 = 모른다). 현행 응답이 그렇고 화면이 그 값을 그대로 찍는다.
	LastReportedAt string `json:"lastReportedAt"`
	LastStartedAt  string `json:"lastStartedAt"`
	SendsSeries    bool   `json:"sendsSeries"`
}

type gapDTO struct {
	Token string `json:"token"`
	Count int    `json:"count"`
}

type recommendationDTO struct {
	Total int      `json:"total"`
	Miss  int      `json:"miss"`
	Gaps  []gapDTO `json:"gaps"`
}

type retentionDTO struct {
	// nil = 무기한 보관. 화면이 "이 축은 N일 뒤 사라진다"를 말할 수 있어야 하고,
	// 무기한이라는 사실도 말해야 한다 — 그래서 0 이 아니라 null 이다.
	KeywordDays *int `json:"keywordDays"`
}

type mappingDTO struct {
	Machine   string `json:"machine"`
	Username  string `json:"username"`
	Note      string `json:"note"`
	UpdatedBy string `json:"updatedBy"`
	UpdatedAt string `json:"updatedAt"`
}

type unmappedDTO struct {
	Machine  string `json:"machine"`
	Username string `json:"username"`
	Sessions int    `json:"sessions"`
}

type movedDTO struct {
	Sessions int `json:"sessions"`
	Counters int `json:"counters"`
}

/* ── 변환기 ───────────────────────────────────────────────────────────── */

func toTotalsDTO(t store.TotalsResult) totalsDTO {
	return totalsDTO{
		Sessions: t.Sessions, Input: t.Input, Output: t.Output,
		CacheRead: t.CacheRead, CacheCreate: t.CacheCreate,
		Users: t.Users, Machines: t.Machines,
	}
}

func toDayDTOs(rows []store.DayRow) []dayDTO {
	out := make([]dayDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, dayDTO{
			Day: r.Day, Input: r.Input, Output: r.Output,
			CacheRead: r.CacheRead, CacheCreate: r.CacheCreate, Sessions: r.Sessions,
		})
	}
	return out
}

func toUserDTOs(rows []store.UserRow) []userDTO {
	out := make([]userDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, userDTO{
			Username: r.Username, Input: r.Input, Output: r.Output,
			CacheRead: r.CacheRead, CacheCreate: r.CacheCreate,
			Turns: r.Turns, Sessions: r.Sessions,
		})
	}
	return out
}

func toShareDTO(s store.Share) shareDTO {
	return shareDTO{
		Input: s.Input, Output: s.Output, CacheRead: s.CacheRead,
		CacheCreate: s.CacheCreate, Sessions: s.Sessions,
	}
}

func toModelDTOs(rows []store.ModelRow) []modelDTO {
	out := make([]modelDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, modelDTO{
			Model: r.Model, Input: r.Input, Output: r.Output,
			CacheRead: r.CacheRead, CacheCreate: r.CacheCreate, Sessions: r.Sessions,
			FromSeries: toShareDTO(r.FromSeries), FromSession: toShareDTO(r.FromSession),
		})
	}
	return out
}

func toModelAxisDTO(a store.ModelAxis) modelAxisDTO {
	users := make([]modelAxisUserDTO, 0, len(a.Users))
	for _, u := range a.Users {
		users = append(users, modelAxisUserDTO{
			Username: u.Username, Sessions: u.Sessions, WithSeries: u.WithSeries,
			NoTsTurns: u.NoTsTurns, NoTsUnknown: u.NoTsUnknown,
		})
	}
	return modelAxisDTO{
		Sessions: a.Sessions, WithSeries: a.WithSeries, Users: users, OverSessions: a.OverSessions,
	}
}

func toKeyDTOs(rows []store.KeyRow) []keyDTO {
	out := make([]keyDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, keyDTO{Key: r.Key, Count: r.Count, Sessions: r.Sessions, Users: r.Users})
	}
	return out
}

func toKeyCountDTOs(rows []store.KeyCount) []keyCountDTO {
	out := make([]keyCountDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, keyCountDTO{Key: r.Key, Count: r.Count})
	}
	return out
}

func toUserKeysDTOs(rows []store.UserKeys) []userKeysDTO {
	out := make([]userKeysDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, userKeysDTO{
			Username: r.Username, Total: r.Total, Items: toKeyCountDTOs(r.Items),
		})
	}
	return out
}

func toReporterDTOs(rows []store.Reporter) []reporterDTO {
	out := make([]reporterDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, reporterDTO{
			Machine: r.Machine, Username: r.Username, Sessions: r.Sessions,
			LastReportedAt: r.LastReportedAt, LastStartedAt: r.LastStartedAt,
			SendsSeries: r.SendsSeries,
		})
	}
	return out
}

func toGapDTOs(rows []store.Gap) []gapDTO {
	out := make([]gapDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, gapDTO{Token: r.Token, Count: r.Count})
	}
	return out
}

func toMappingDTOs(rows []identity.Mapping) []mappingDTO {
	out := make([]mappingDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, mappingDTO{
			Machine: r.Machine, Username: r.Username, Note: r.Note,
			UpdatedBy: r.UpdatedBy, UpdatedAt: r.UpdatedAt,
		})
	}
	return out
}

func toUnmappedDTOs(rows []identity.UnmappedMachine) []unmappedDTO {
	out := make([]unmappedDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, unmappedDTO{Machine: r.Machine, Username: r.Username, Sessions: r.Sessions})
	}
	return out
}

/* ── 비용 ─────────────────────────────────────────────────────────────── */

// axisDTO 는 비용의 축별 분해다. 토큰 수가 큰 축과 비용이 큰 축이 다르기 때문에 이 분해가
// 없으면 화면이 "출력 토큰이 제일 비싸다"는 틀린 그림을 보여준다(cost 패키지 헤더 참조).
type axisDTO struct {
	Input       float64 `json:"input"`
	Output      float64 `json:"output"`
	CacheRead   float64 `json:"cacheRead"`
	CacheCreate float64 `json:"cacheCreate"`
}

func toAxisDTO(a cost.Axis) axisDTO {
	return axisDTO{Input: a.Input, Output: a.Output, CacheRead: a.CacheRead, CacheCreate: a.CacheCreate}
}

// sampleDTO — 표본이 잘렸는지. 화면이 "이 수치는 최근 N건 기준"이라고 말할 수 있어야 한다.
type sampleDTO struct {
	Rows      int  `json:"rows"`
	Limit     int  `json:"limit"`
	Truncated bool `json:"truncated"`
}

func sampleInfo(rows, limit int) sampleDTO {
	return sampleDTO{Rows: rows, Limit: limit, Truncated: rows >= limit}
}

// sessionUsage / bucketUsage — store 행을 cost 입력으로 옮긴다.
//
// ⚠ 세션 행에는 TTL 분해(cc5m/cc1h)가 **없다.** 그래서 cost 가 5분으로 가정하고
// ttlKnown=false 를 붙이며, 호출부가 그 개수를 ttlUnknownRows 로 화면에 올린다.
// 여기서 0 이 아닌 값을 지어내면 그 경고가 사라진다.
func sessionUsage(s store.Session) cost.Usage {
	return cost.Usage{
		Model:       s.Model,
		Input:       float64(s.Input),
		Output:      float64(s.Output),
		CacheRead:   float64(s.CacheRead),
		CacheCreate: float64(s.CacheCreate),
		// 계단(롱컨텍스트) 분리분. **세 변환 함수 전부**가 이걸 올려야 한다 — 하나라도 빠지면
		// 그 화면만 계단을 안 타고, 같은 데이터의 비용이 화면마다 달라진다(dto_longcontext_test.go).
		InputLong:       float64(s.InputLong),
		OutputLong:      float64(s.OutputLong),
		CacheReadLong:   float64(s.CacheReadLong),
		CacheCreateLong: float64(s.CacheCreateLong),
		// 고속 모드 분리분 — 계단과 같은 규율이다. 셋 다 올려야 한다.
		InputFast:       float64(s.InputFast),
		OutputFast:      float64(s.OutputFast),
		CacheReadFast:   float64(s.CacheReadFast),
		CacheCreateFast: float64(s.CacheCreateFast),
	}
}

/*
 * platformDTO — 플랫폼별 요약 한 행.
 *
 * ⚠ 이 축이 **여기서만** 나가던 시절의 주의문이 여기 있었다("기존 응답에는 추가하지 않는다").
 *   멀티플랫폼을 무회귀로 얹기 위한 한시적 규율이었고, 그 목적은 달성됐다. 지금은 sessionDTO
 *   도 platform 을 싣는다 — 집계는 플랫폼을 아는데 드릴다운은 모르던 비대칭을 없앤 것이다.
 *   골든은 그 변경을 **재캡처로** 반영했다(손으로 고치지 않았다). 추가는 계약 절차를 밟으면
 *   되는 일이고, 금지였던 적은 없다.
 *
 * firstSeen/lastSeen 은 빈 문자열일 수 있다(시각을 안 보낸 세션뿐인 플랫폼). 그건 "모른다"다.
 */
type platformDTO struct {
	Platform    string  `json:"platform"`
	Sessions    int     `json:"sessions"`
	Input       int64   `json:"input"`
	Output      int64   `json:"output"`
	CacheRead   int64   `json:"cacheRead"`
	CacheCreate int64   `json:"cacheCreate"`
	CostUsd     float64 `json:"costUsd"`
	FirstSeen   string  `json:"firstSeen"`
	LastSeen    string  `json:"lastSeen"`
}

type platformsResponse struct {
	Platforms []platformDTO `json:"platforms"`
}

// platformModelUsage — 플랫폼 안의 모델별 합계를 cost 입력으로 옮긴다.
//
// ⚠ 세션 행과 같은 한계를 갖는다: TTL 분해(cc5m/cc1h)가 없어 cost 가 5분으로 가정한다.
// 여기서 0 이 아닌 값을 지어내면 그 가정이 사실로 둔갑한다.
func platformModelUsage(m store.PlatformModelRow) cost.Usage {
	return cost.Usage{
		Model:           m.Model,
		Input:           float64(m.Input),
		Output:          float64(m.Output),
		CacheRead:       float64(m.CacheRead),
		CacheCreate:     float64(m.CacheCreate),
		InputLong:       float64(m.InputLong),
		OutputLong:      float64(m.OutputLong),
		CacheReadLong:   float64(m.CacheReadLong),
		CacheCreateLong: float64(m.CacheCreateLong),
		InputFast:       float64(m.InputFast),
		OutputFast:      float64(m.OutputFast),
		CacheReadFast:   float64(m.CacheReadFast),
		CacheCreateFast: float64(m.CacheCreateFast),
	}
}

func bucketUsage(b store.Bucket) cost.Usage {
	return cost.Usage{
		Model:           b.Model,
		Input:           float64(b.Input),
		Output:          float64(b.Output),
		CacheRead:       float64(b.CacheRead),
		CacheCreate:     float64(b.CacheCreate),
		CacheCreate5m:   float64(b.CacheCreate5m),
		CacheCreate1h:   float64(b.CacheCreate1h),
		InputLong:       float64(b.InputLong),
		OutputLong:      float64(b.OutputLong),
		CacheReadLong:   float64(b.CacheReadLong),
		CacheCreateLong: float64(b.CacheCreateLong),
		InputFast:       float64(b.InputFast),
		OutputFast:      float64(b.OutputFast),
		CacheReadFast:   float64(b.CacheReadFast),
		CacheCreateFast: float64(b.CacheCreateFast),
	}
}
