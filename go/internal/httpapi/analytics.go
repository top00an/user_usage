package httpapi

import (
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tscorp/user-usage/internal/cost"
	"github.com/tscorp/user-usage/internal/stats"
	"github.com/tscorp/user-usage/internal/store"
	"github.com/tscorp/user-usage/internal/tz"
)

/*
 * routes/usage-analytics.js 의 포팅 — 비용·분포·차원 슬라이싱·드릴다운.
 *
 * usage.go 가 합계와 상위 키를 담당한다면 여기는 **"왜 그 숫자인가"** 를 담당한다.
 *
 * ⚠ 이 라우트는 자기 경로가 아니면 404 가 아니라 **false 를 돌려 흘려보낸다.**
 *   `/api/usage` 접두사의 주인은 usage.go 이고, 그쪽이 안 걸리면 404 를 직접 낸다.
 *   그래서 체인에서 이 라우트가 **앞**이어야 한다 — 뒤로 가면 관측 화면이 통째로 404 가 된다.
 */

// 그룹 축 화이트리스트 — 요청 문자열이 SQL 이나 필드 접근에 그대로 닿지 않게 한다.
// 값은 store 행의 필드이고, 키는 화면·URL 이 쓰는 이름이다.
var groupFields = map[string]bool{"user": true, "model": true, "project": true, "machine": true}

const maxGroupAxes = 3

var metrics = []string{"cost", "tokens", "sessions", "turns"}

// 세션 목록 정렬축. 여기서도 문자열을 SQL 로 흘리지 않는다.
var sortAxes = []string{"cost", "cacheRead", "output", "turns", "startedAt"}

var intervals = []string{"day", "hour", "week"}

var (
	dayRE    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	hourRE   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}$`)
	detailRE = regexp.MustCompile(`^/api/usage/sessions/([A-Za-z0-9._-]{1,200})$`)
)

/*
 * 복합 그룹 키를 잇는 구분자. 사용자명·모델명 같은 자유 문자열을 이어 붙이는 자리라 값에
 * 나타날 수 없는 문자여야 한다. 평범한 구분자(빈 문자열·하이픈)를 쓰면 ('a','bc') 와
 * ('ab','c') 가 같은 계열로 합쳐진다. U+0000 은 이 데이터에 올 수 없어 그 충돌이 불가능하다.
 */
const keySep = "\x00"

// unknownValue — 축 값이 비었을 때의 라벨. 빈 문자열 키를 만들지 않는다.
const unknownValue = "(미상)"

const seriesAll = "전체"

/* ── 응답 shape ───────────────────────────────────────────────────────── */

type coverageDTO struct {
	SessionsWithSeries int `json:"sessionsWithSeries"`
	SessionsTotal      int `json:"sessionsTotal"`
}

type pointDTO struct {
	T string  `json:"t"`
	V float64 `json:"v"`
}

type seriesDTO struct {
	Key    map[string]string `json:"key"`
	Label  string            `json:"label"`
	Points []pointDTO        `json:"points"`
	Total  float64           `json:"total"`
}

type seriesResponse struct {
	Metric   string      `json:"metric"`
	Interval string      `json:"interval"`
	GroupBy  []string    `json:"groupBy"`
	Series   []seriesDTO `json:"series"`
	PricedAt string      `json:"pricedAt"`
	Unpriced []string    `json:"unpriced"`
	// 라벨이 어느 시간대 기준인가. 저장은 UTC 인데 라벨은 로컬이라, 이걸 안 밝히면 화면과
	// 원본 데이터를 대조하는 사람이 9시간 어긋난 값을 보고 버그로 오해한다.
	Timezone string    `json:"timezone"`
	Sample   sampleDTO `json:"sample"`
	// 이 응답의 한계를 응답이 스스로 말한다. 화면이 이걸 표시해야 "어제 스파이크가 없었다"를
	// 데이터의 진실로 오해하지 않는다.
	Attribution      string       `json:"attribution"`
	Coverage         *coverageDTO `json:"coverage,omitempty"`
	ModelAttribution string       `json:"modelAttribution"`
}

type distributionsDTO struct {
	CacheReadPerTurn stats.Summary `json:"cacheReadPerTurn"`
	SessionCostUsd   stats.Summary `json:"sessionCostUsd"`
	TurnsPerSession  stats.Summary `json:"turnsPerSession"`
	CacheHitRate     stats.Summary `json:"cacheHitRate"`
}

type distributionResponse struct {
	Distributions distributionsDTO `json:"distributions"`
	PricedAt      string           `json:"pricedAt"`
	Unpriced      []string         `json:"unpriced"`
	Sample        sampleDTO        `json:"sample"`
}

type sessionsResponse struct {
	Sessions []sessionListItemDTO `json:"sessions"`
	Sort     string               `json:"sort"`
	USD      float64              `json:"usd"`
	ByAxis   axisDTO              `json:"byAxis"`
	PricedAt string               `json:"pricedAt"`
	Unpriced []string             `json:"unpriced"`
	// TTL 을 모르는 행 수. 구버전 수집기가 캐시 생성 TTL 을 안 보내서 5분(1.25배)으로 가정한
	// 행이고, 실제가 1시간이면 그만큼(최대 1.6배) 과소 추정이다. 합계만 보여주면 누락이 안 보인다.
	TTLUnknownRows int       `json:"ttlUnknownRows"`
	Sample         sampleDTO `json:"sample"`
}

type sessionCostDTO struct {
	USD      float64 `json:"usd"`
	ByAxis   axisDTO `json:"byAxis"`
	Priced   bool    `json:"priced"`
	Exact    bool    `json:"exact"`
	PricedAt string  `json:"pricedAt"`
}

type sessionDetailResponse struct {
	Session sessionDTO               `json:"session"`
	Cost    sessionCostDTO           `json:"cost"`
	Series  []bucketDTO              `json:"series"`
	Counter map[string][]keyCountDTO `json:"counters"`
	Note    string                   `json:"note"`
}

type coverageResponse struct {
	Reporters []reporterDTO `json:"reporters"`
	Now       string        `json:"now"`
}

type qualityResponse struct {
	Coverage      coverageDTO `json:"coverage"`
	Turns         int64       `json:"turns"`
	ToolErrors    int64       `json:"toolErrors"`
	ToolErrorRate float64     `json:"toolErrorRate"`
	StopMaxTokens int64       `json:"stopMaxTokens"`
	StopMaxRate   float64     `json:"stopMaxRate"`
	StopRefusal   int64       `json:"stopRefusal"`
	RefusalRate   float64     `json:"refusalRate"`
	LatencyAvgMs  float64     `json:"latencyAvgMs"`
	LatencyMaxMs  int64       `json:"latencyMaxMs"`
	LatencyTurns  int64       `json:"latencyTurns"`
	Sample        sampleDTO   `json:"sample"`
}

type leaderRowDTO struct {
	Username      string  `json:"username"`
	Sessions      int     `json:"sessions"`
	Turns         int64   `json:"turns"`
	Tokens        int64   `json:"tokens"`
	USD           float64 `json:"usd"`
	Priced        bool    `json:"priced"`
	CacheHitRate  float64 `json:"cacheHitRate"`
	UsdPerSession float64 `json:"usdPerSession"`
}

type leaderboardResponse struct {
	Users    []leaderRowDTO `json:"users"`
	PricedAt string         `json:"pricedAt"`
	Unpriced []string       `json:"unpriced"`
	Sample   sampleDTO      `json:"sample"`
}

type dispatchResponse struct {
	Agents  []dispatchUserDTO `json:"agents"`
	Skills  []userKeysDTO     `json:"skills"`
	Generic []string          `json:"generic"`
}

/*
 * genericAgents — 역할이 없는 범용 에이전트다.
 *
 * 왜(실측): 같은 지침이 모든 세션에 실리는데 실제 사용은 사람마다 갈렸다 — 한 사람은 역할
 * 에이전트를 65회 썼고, 다른 사람은 0회에 general-purpose 만 25회였다. 지침만 있고 측정이
 * 없어 아무도 몰랐다. 강제하지 않는다 — 보이면 사람이 판단한다.
 *
 * 순서가 응답에 그대로 나간다(현행 JS 의 Set 삽입 순서). 정렬하지 않는다.
 */
var genericAgents = []string{"general-purpose", "Explore", "Plan", "claude"}

/* ── 라우트 ───────────────────────────────────────────────────────────── */

func (s *server) routeAnalytics(w http.ResponseWriter, r *http.Request, c *rctx) (bool, error) {
	// 우리 경로가 아니면 **흘려보낸다**(404 를 내지 않는다 — usage.go 가 접두사 주인이다).
	if r.Method != http.MethodGet || !strings.HasPrefix(c.path, "/api/usage/") {
		return false, nil
	}
	p := c.path
	detail := detailRE.FindStringSubmatch(p)
	known := map[string]bool{
		"/api/usage/series": true, "/api/usage/distribution": true,
		"/api/usage/sessions": true, "/api/usage/quality": true,
		"/api/usage/coverage": true, "/api/usage/leaderboard": true,
		"/api/usage/dispatch": true,
	}
	if !known[p] && detail == nil {
		return false, nil
	}

	ctx := r.Context()

	from := c.query.Get("from")
	to := c.query.Get("to")
	for _, pair := range [][2]string{{"from", from}, {"to", to}} {
		if pair[1] != "" && !dayRE.MatchString(pair[1]) {
			sendError(w, http.StatusBadRequest, pair[0]+" 은 YYYY-MM-DD 형식이어야 합니다")
			return true, nil
		}
	}
	limit := clampInt(int(numOr(c.query.Get("limit"), float64(store.SessionRowsDefault))),
		1, store.SessionRowsMax)

	// ── 서브에이전트·스킬 활용 ────────────────────────────────────────────
	if p == "/api/usage/dispatch" {
		agents, err := store.ByUser(ctx, "agent", 0)
		if err != nil {
			return true, err
		}
		skills, err := store.ByUser(ctx, "skill", 0)
		if err != nil {
			return true, err
		}
		isGeneric := map[string]bool{}
		for _, g := range genericAgents {
			isGeneric[g] = true
		}
		out := make([]dispatchUserDTO, 0, len(agents))
		for _, u := range toUserKeysDTOs(agents) {
			var generic int64
			for _, it := range u.Items {
				if isGeneric[it.Key] {
					generic += it.Count
				}
			}
			out = append(out, dispatchUserDTO{userKeysDTO: u, Generic: generic, Role: u.Total - generic})
		}
		writeJSON(w, http.StatusOK, dispatchResponse{
			Agents: out, Skills: toUserKeysDTOs(skills), Generic: genericAgents,
		}, nil)
		return true, nil
	}

	// ── 세션 상세(드릴다운) ───────────────────────────────────────────────
	if detail != nil {
		id := detail[1]
		row, err := store.SessionByID(ctx, id)
		if err != nil {
			return true, err
		}
		if row == nil {
			sendError(w, http.StatusNotFound, "없는 세션")
			return true, nil
		}
		/*
		 * keyword 축은 내보내지 않는다. 정규화 토큰이라도 한 세션치를 모아 놓으면 그 사람이
		 * 무엇을 물었는지 재구성할 여지가 생긴다 — 집계(topKeys)로는 공개하되 세션 단위로는 닫는다.
		 */
		kinds := make([]string, 0, len(store.CounterKinds))
		for _, k := range store.CounterKinds {
			if k != "keyword" {
				kinds = append(kinds, k)
			}
		}
		counters, err := store.CountersOf(ctx, id, kinds)
		if err != nil {
			return true, err
		}
		buckets, err := store.SeriesOf(ctx, id)
		if err != nil {
			return true, err
		}
		/*
		 * 세션 비용은 시간 버킷이 있으면 **그쪽에서** 합산한다. 세션 행의 model 은 최빈 모델
		 * 하나라 모델이 섞인 세션을 한 단가로 계산하고, TTL 분해도 세션 행에는 없다.
		 * 버킷이 없으면(구버전 수집기) 세션 행으로 계산하고 approx(exact:false)로 표시한다.
		 */
		var cd sessionCostDTO
		if len(buckets) > 0 {
			usages := make([]cost.Usage, 0, len(buckets))
			for _, b := range buckets {
				usages = append(usages, bucketUsage(b))
			}
			sum := cost.Summarize(usages)
			cd = sessionCostDTO{USD: sum.USD, ByAxis: toAxisDTO(sum.ByAxis), Priced: true, Exact: true}
		} else {
			one := cost.CostOf(sessionUsage(*row), nil, cost.Mult{})
			cd = sessionCostDTO{USD: one.USD, ByAxis: toAxisDTO(one.ByAxis), Priced: one.Priced, Exact: false}
		}
		cd.PricedAt = cost.PricedAt()

		writeJSON(w, http.StatusOK, sessionDetailResponse{
			Session: toSessionDTO(*row),
			Cost:    cd,
			Series:  toBucketDTOs(buckets),
			Counter: countersDTO(counters),
			Note:    "keyword 축은 세션 단위로 제공하지 않는다(프롬프트 재구성 방지).",
		}, nil)
		return true, nil
	}

	username := c.query.Get("user")
	model := c.query.Get("model")
	rows, err := store.SessionRows(ctx, store.Filter{
		From: from, To: to, Username: username, Model: model, Limit: limit,
	})
	if err != nil {
		return true, err
	}
	table := cost.Pricing(time.Now())
	mult := cost.Multipliers()
	costs := make([]cost.Result, 0, len(rows))
	for _, row := range rows {
		costs = append(costs, cost.CostOf(sessionUsage(row), table, mult))
	}
	unpriced := unpricedModels(costs)
	pricedAt := cost.PricedAt()

	// ── 분포 ──────────────────────────────────────────────────────────────
	if p == "/api/usage/distribution" {
		/*
		 * ⚠ SummarizeAny 를 쓴다. []float64 로 먼저 접으면 `dropped`(관측이 아닌 값의 개수)가
		 * 항상 0 으로 나가고, 화면이 표본이 깎인 사실을 말할 수 없게 된다. JS 의 규율은
		 * Number(null)===0 이라 null 을 "0 이라는 관측"으로 둔갑시키지 않는 것인데,
		 * []float64 경계를 넘는 순간 그 구분이 사라진다.
		 */
		perTurnCacheRead := []any{}
		sessionCost := []any{}
		turns := []any{}
		hitRate := []any{}
		for i, row := range rows {
			if row.Turns > 0 {
				perTurnCacheRead = append(perTurnCacheRead, float64(row.CacheRead)/float64(row.Turns))
			}
			if costs[i].Priced {
				sessionCost = append(sessionCost, costs[i].USD)
			}
			turns = append(turns, row.Turns)
			if denom := row.CacheRead + row.CacheCreate + row.Input; denom > 0 {
				hitRate = append(hitRate, float64(row.CacheRead)/float64(denom))
			}
		}
		writeJSON(w, http.StatusOK, distributionResponse{
			Distributions: distributionsDTO{
				CacheReadPerTurn: stats.SummarizeAny(perTurnCacheRead),
				SessionCostUsd:   stats.SummarizeAny(sessionCost),
				TurnsPerSession:  stats.SummarizeAny(turns),
				CacheHitRate:     stats.SummarizeAny(hitRate),
			},
			PricedAt: pricedAt, Unpriced: unpriced, Sample: sampleInfo(len(rows), limit),
		}, nil)
		return true, nil
	}

	// ── 세션 목록 ─────────────────────────────────────────────────────────
	if p == "/api/usage/sessions" {
		sortRaw := c.query.Get("sort")
		if sortRaw == "" {
			sortRaw = "cost"
		}
		if !contains(sortAxes, sortRaw) {
			sendError(w, http.StatusBadRequest, "정렬축은 "+strings.Join(sortAxes, "|")+" 중 하나입니다")
			return true, nil
		}
		top := clampInt(int(numOr(c.query.Get("top"), 50)), 1, 500)

		merged := make([]sessionListItemDTO, 0, len(rows))
		for i, row := range rows {
			item := sessionListItemDTO{sessionDTO: toSessionDTO(row), Priced: costs[i].Priced}
			if costs[i].Priced {
				v := costs[i].USD
				item.USD = &v
			}
			merged = append(merged, item)
		}
		// JS 의 Array.prototype.sort 는 안정 정렬이다 — 동률의 순서가 응답에 남으므로
		// SliceStable 이어야 한다(Slice 로 하면 같은 값끼리 순서가 흔들린다).
		sort.SliceStable(merged, func(i, j int) bool {
			a, b := merged[i], merged[j]
			switch sortRaw {
			case "startedAt":
				return strPtr(b.StartedAt) < strPtr(a.StartedAt)
			case "cost":
				return usdOrZero(b.USD) < usdOrZero(a.USD)
			case "cacheRead":
				return b.CacheRead < a.CacheRead
			case "output":
				return b.Output < a.Output
			default: // turns
				return b.Turns < a.Turns
			}
		})

		/*
		 * 비용 카드의 축별 분해는 **전 윈도우 합계**라야 한다 — sessions 는 top-N 만 담으므로
		 * 슬라이스 이전의 costs(전 rows)를 그대로 접는다. priced 행만 더해 usd 계약과 정합.
		 * 이 필드가 없으면 화면이 축별 $0 으로 떨어진다.
		 */
		var byAxis axisDTO
		usdTotal := 0.0
		ttlUnknown := 0
		for i, cr := range costs {
			if !cr.Priced {
				continue
			}
			if !cr.TTLKnown && rows[i].CacheCreate > 0 {
				ttlUnknown++
			}
			usdTotal += cr.USD
			byAxis.Input += cr.ByAxis.Input
			byAxis.Output += cr.ByAxis.Output
			byAxis.CacheRead += cr.ByAxis.CacheRead
			byAxis.CacheCreate += cr.ByAxis.CacheCreate
		}

		if top < len(merged) {
			merged = merged[:top]
		}
		writeJSON(w, http.StatusOK, sessionsResponse{
			Sessions: merged, Sort: sortRaw, USD: usdTotal, ByAxis: byAxis,
			PricedAt: pricedAt, Unpriced: unpriced,
			TTLUnknownRows: ttlUnknown, Sample: sampleInfo(len(rows), limit),
		}, nil)
		return true, nil
	}

	// ── 수집 커버리지 ─────────────────────────────────────────────────────
	// "왜 데이터가 없나"를 화면이 답한다 — 발신처(머신)별 마지막 보고 시각·세션 수·신수집기 여부.
	if p == "/api/usage/coverage" {
		reporters, err := store.ReporterCoverage(ctx)
		if err != nil {
			return true, err
		}
		writeJSON(w, http.StatusOK, coverageResponse{
			Reporters: toReporterDTOs(reporters),
			Now:       time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		}, nil)
		return true, nil
	}

	// ── 품질축 ────────────────────────────────────────────────────────────
	if p == "/api/usage/quality" {
		qt, err := store.SeriesQualityTotals(ctx, store.Filter{From: from, To: to, Username: username})
		if err != nil {
			return true, err
		}
		rate := func(a, b int64) float64 {
			if b > 0 {
				return float64(a) / float64(b)
			}
			return 0
		}
		latencyAvg := 0.0
		if qt.LatencyTurns > 0 {
			latencyAvg = float64(qt.LatencyMsSum) / float64(qt.LatencyTurns)
		}
		writeJSON(w, http.StatusOK, qualityResponse{
			Coverage: coverageDTO{
				SessionsWithSeries: qt.SessionsWithSeries,
				SessionsTotal:      distinctSessions(rows),
			},
			Turns:         qt.Turns,
			ToolErrors:    qt.ToolErrors,
			ToolErrorRate: rate(qt.ToolErrors, qt.Turns),
			StopMaxTokens: qt.StopMaxTokens,
			StopMaxRate:   rate(qt.StopMaxTokens, qt.Turns),
			StopRefusal:   qt.StopRefusal,
			RefusalRate:   rate(qt.StopRefusal, qt.Turns),
			LatencyAvgMs:  latencyAvg,
			LatencyMaxMs:  qt.LatencyMsMax,
			LatencyTurns:  qt.LatencyTurns,
			Sample:        sampleInfo(len(rows), limit),
		}, nil)
		return true, nil
	}

	// ── 사람별 리더보드 ───────────────────────────────────────────────────
	// 전 윈도우 rows/costs 를 사용자별로 접는다(top-N 슬라이스 없음 — 전원 집계).
	if p == "/api/usage/leaderboard" {
		type acc struct {
			username                        string
			sessions                        int
			turns, tokens, cacheRead, denom int64
			usd                             float64
			priced                          bool
		}
		order := []string{}
		by := map[string]*acc{}
		for i, row := range rows {
			key := row.Username
			if key == "" {
				key = unknownValue
			}
			e := by[key]
			if e == nil {
				e = &acc{username: key}
				by[key] = e
				order = append(order, key)
			}
			e.sessions++
			e.turns += row.Turns
			e.tokens += row.Input + row.Output + row.CacheRead + row.CacheCreate
			e.cacheRead += row.CacheRead
			e.denom += row.CacheRead + row.CacheCreate + row.Input
			if costs[i].Priced {
				e.usd += costs[i].USD
				e.priced = true
			}
		}
		users := make([]leaderRowDTO, 0, len(order))
		for _, k := range order {
			e := by[k]
			hit := 0.0
			if e.denom > 0 {
				hit = float64(e.cacheRead) / float64(e.denom)
			}
			per := 0.0
			if e.sessions > 0 {
				per = e.usd / float64(e.sessions)
			}
			users = append(users, leaderRowDTO{
				Username: e.username, Sessions: e.sessions, Turns: e.turns, Tokens: e.tokens,
				USD: e.usd, Priced: e.priced, CacheHitRate: hit, UsdPerSession: per,
			})
		}
		// JS: `b.usd - a.usd || b.tokens - a.tokens` — usd 가 정확히 같을 때만 tokens 로 간다.
		sort.SliceStable(users, func(i, j int) bool {
			a, b := users[i], users[j]
			if b.USD != a.USD {
				return b.USD < a.USD
			}
			return b.Tokens < a.Tokens
		})
		writeJSON(w, http.StatusOK, leaderboardResponse{
			Users: users, PricedAt: pricedAt, Unpriced: unpriced, Sample: sampleInfo(len(rows), limit),
		}, nil)
		return true, nil
	}

	// ── 시계열 ────────────────────────────────────────────────────────────
	metric := c.query.Get("metric")
	if metric == "" {
		metric = "cost"
	}
	if !contains(metrics, metric) {
		sendError(w, http.StatusBadRequest, "metric 은 "+strings.Join(metrics, "|")+" 중 하나입니다")
		return true, nil
	}
	interval := c.query.Get("interval")
	if interval == "" {
		interval = "day"
	}
	/*
	 * week 를 여는 근거: 이건 **없는 정밀도를 만드는 게 아니라 있는 정밀도를 접는 것**이다.
	 * day 라벨을 그 주의 월요일로 내리는 순수 롤업이라 새 데이터가 필요 없다.
	 * minute 이 여전히 닫혀 있는 이유와 정확히 반대다 — 그쪽은 근거 데이터가 아예 없다.
	 */
	if !contains(intervals, interval) {
		sendError(w, http.StatusBadRequest, "interval 은 "+strings.Join(intervals, "|")+" 중 하나입니다")
		return true, nil
	}
	axes, gerr := parseGroupBy(c.query.Get("group_by"))
	if gerr != "" {
		sendError(w, http.StatusBadRequest, gerr)
		return true, nil
	}

	/*
	 * 두 축은 **다른 테이블**에서 온다. 섞지 않는 것이 핵심이다.
	 *
	 *   day  → usage_sessions. 모든 수집기가 보내므로 커버리지가 완전하다. 대신 값이 세션
	 *          시작일에 전량 귀속되고, 모델은 세션 최빈 모델 1개로 근사된다.
	 *   hour → usage_series.  턴이 일어난 시각·모델에 정확히 귀속된다. 대신 신버전 수집기를
	 *          가진 사람만 보낸다.
	 *
	 * 한쪽이 비었다고 다른 쪽으로 조용히 대체하지 않는다. 그러면 같은 화면의 두 눈금이 서로
	 * 다른 모집단을 그리게 되고, 그 사실이 어디에도 표시되지 않는다.
	 */
	byHour := interval == "hour"
	/*
	 * SQL 필터는 UTC 라벨 위에서 돈다. 로컬 날짜를 그대로 넘기면 경계가 오프셋만큼 어긋나
	 * 요청한 하루의 앞뒤가 잘린다 — 그래서 **하루씩 넓혀 뜨고** 아래에서 로컬 라벨로 정확히
	 * 거른다. (넓히지 않고 거르기만 하면 경계 행이 애초에 조회되지 않아 조용히 사라진다.)
	 */
	wFrom, wTo := tz.WidenUTCRange(from, to)
	offset := tz.OffsetMin()

	var src []srcRow
	srcLimit := limit
	if byHour {
		srcLimit = store.SeriesRowsDefault
		buckets, err := store.SeriesRows(ctx, store.Filter{
			From: wFrom, To: wTo, Username: username, Model: model, Limit: srcLimit,
		})
		if err != nil {
			return true, err
		}
		src = make([]srcRow, 0, len(buckets))
		for _, b := range buckets {
			src = append(src, srcRow{
				sessionID: b.SessionID, username: b.Username, model: b.Model,
				project: b.Project, machine: b.Machine,
				input: b.Input, output: b.Output, cacheRead: b.CacheRead, cacheCreate: b.CacheCreate,
				turns: b.Turns, timeRaw: b.Hour, usage: bucketUsage(b),
			})
		}
	} else {
		sessions, err := store.SessionRows(ctx, store.Filter{
			From: wFrom, To: wTo, Username: username, Model: model, Limit: srcLimit,
		})
		if err != nil {
			return true, err
		}
		src = make([]srcRow, 0, len(sessions))
		for _, sr := range sessions {
			src = append(src, srcRow{
				sessionID: sr.SessionID, username: sr.Username, model: sr.Model,
				project: sr.Project, machine: sr.Machine,
				input: sr.Input, output: sr.Output, cacheRead: sr.CacheRead, cacheCreate: sr.CacheCreate,
				turns: sr.Turns, timeRaw: strPtr(sr.StartedAt), usage: sessionUsage(sr),
			})
		}
	}
	/*
	 * ⚠ 비용은 **src 기준으로 다시** 계산한다. 위쪽 costs 는 넓히기 전 rows 의 것이라
	 * 두 배열의 길이·순서가 다르다 — 그대로 쓰면 비용이 엉뚱한 계열에 붙는 조용한 오염이다.
	 */
	srcCosts := make([]cost.Result, 0, len(src))
	for _, row := range src {
		srcCosts = append(srcCosts, cost.CostOf(row.usage, table, mult))
	}
	srcUnpriced := unpricedModels(srcCosts)

	acc := newSeriesAcc()
	for i, row := range src {
		/*
		 * ⚠ 저장은 UTC, **집계 라벨은 로컬**이다. 여기서 옮기지 않으면 00:00~09:00 KST 에
		 * 한 일이 전날로 집계되고 시간축 그래프가 9시간 밀린다 — "몇 시에 몰리나"를 보려고
		 * 만든 축이 그 질문에 거짓말을 한다.
		 */
		var t string
		if byHour {
			t = tz.LocalHour(row.timeRaw, offset)
			if !hourRE.MatchString(t) {
				continue // 시각 라벨이 없는 행은 시계열에 올릴 자리가 없다
			}
		} else {
			t = tz.LocalDay(row.timeRaw, offset)
			if !dayRE.MatchString(t) {
				continue
			}
		}
		// 넓혀 뜬 만큼 로컬 라벨 기준으로 정확히 걸러낸다.
		if !tz.InRange(t, from, to) {
			continue
		}
		// week: 라벨을 그 주의 **월요일**로 내린다(ISO 주 시작). 로컬로 옮긴 뒤에 접어야
		// 주 경계가 오프셋만큼 밀린 채로 굳지 않는다.
		if interval == "week" {
			t = tz.WeekStart(t)
		}
		key, sk := keyOf(axes, row)
		acc.add(sk, key, t, metricValue(metric, row, srcCosts[i]), row.sessionID, metric == "sessions")
	}

	series := acc.build(metric, axes)

	body := seriesResponse{
		Metric: metric, Interval: interval, GroupBy: axes, Series: series,
		PricedAt: pricedAt, Unpriced: srcUnpriced, Timezone: tz.Label(offset),
		Sample: sampleInfo(len(src), srcLimit),
	}
	if byHour {
		body.Attribution = "turn-hour"
		/*
		 * 시간 뷰가 몇 개 세션을 덮는지. 이 값이 낮으면 화면은 "아직 일부만 시간 버킷을
		 * 보낸다"고 말해야 한다 — 안 그러면 수집기가 덜 퍼진 기간의 낮은 수치를 사용량
		 * 감소로 오해한다.
		 */
		body.Coverage = &coverageDTO{
			SessionsWithSeries: distinctSrcSessions(src),
			SessionsTotal:      distinctSessions(rows),
		}
		body.ModelAttribution = "exact"
	} else {
		body.Attribution = "session-start-day"
		body.ModelAttribution = "session-dominant"
	}
	writeJSON(w, http.StatusOK, body, nil)
	return true, nil
}

/* ── 시계열 집계 ──────────────────────────────────────────────────────── */

// srcRow 는 세션 행과 시간 버킷을 같은 모양으로 접은 것이다 — 두 테이블이 같은 지표를
// 기여하므로 아래 집계는 한 벌이면 된다.
type srcRow struct {
	sessionID                             string
	username, model, project, machine     string
	input, output, cacheRead, cacheCreate int64
	turns                                 int64
	timeRaw                               string
	usage                                 cost.Usage
}

func fieldOf(axis string, row srcRow) string {
	switch axis {
	case "user":
		return row.username
	case "model":
		return row.model
	case "project":
		return row.project
	case "machine":
		return row.machine
	}
	return ""
}

func keyOf(axes []string, row srcRow) (map[string]string, string) {
	key := make(map[string]string, len(axes))
	parts := make([]string, 0, len(axes))
	for _, a := range axes {
		v := fieldOf(a, row)
		if v == "" {
			v = unknownValue
		}
		key[a] = v
		parts = append(parts, v)
	}
	if len(axes) == 0 {
		return key, seriesAll
	}
	return key, strings.Join(parts, keySep)
}

/*
 * metricValue — 한 행이 기여하는 지표값.
 *
 * ⚠ interval=day 경로에서 이 값은 세션이 **시작한 날**에 전부 귀속된다. 5시간짜리 세션은
 *   시작일 한 칸에 전량 쌓인다. 응답의 attribution 이 그 사실을 화면에 실어 보낸다.
 *
 * sessions 지표만 여기서 계산하지 않는다 — 시간 단위에서는 한 세션이 여러 버킷에 걸치므로
 * 행을 세면 중복이 된다. 호출부가 세션 id 집합으로 센다.
 */
func metricValue(metric string, row srcRow, c cost.Result) float64 {
	switch metric {
	case "cost":
		if c.Priced {
			return c.USD
		}
		return 0
	case "tokens":
		return float64(row.input + row.output + row.cacheRead + row.cacheCreate)
	case "turns":
		return float64(row.turns)
	}
	return 0 // sessions — 호출부가 distinct 로 센다
}

/*
 * seriesAcc 는 계열 → 칸 → 값이다. **삽입 순서를 기억한다** — 최종 정렬이 안정 정렬이라
 * 동률 계열의 순서가 응답에 그대로 남고, Go 의 맵은 순서가 없어서 그걸 잃으면 골든이 흔들린다.
 */
type seriesAcc struct {
	order  []string
	keys   map[string]map[string]string
	values map[string]map[string]float64
	/*
	 * ⚠ **칸(t)의 삽입 순서도 기억해야 한다.** 합계는 칸 값을 앞에서부터 더한 것이고, 부동소수
	 * 덧셈은 순서가 바뀌면 마지막 자리가 달라진다. 현행 JS 는 Map 의 삽입 순서(원행이 온 순서 =
	 * `ORDER BY hour DESC`)로 reduce 하는데, Go 의 맵을 그냥 훑으면 그 순서가 매 실행마다 다르다.
	 *
	 * 실측: sonnet 계열 4칸을 정렬 순으로 더하면 0.1333, 원행 순으로 더하면 0.13329999999999997 이다.
	 * 골든이 그 차이를 잡았다 — "같은 뜻의 다른 식"이 마지막 자리에서 갈리는 바로 그 자리다.
	 * (점 목록은 별도로 t 오름차순 정렬해 내보낸다. 표시 순서와 합산 순서는 다르다.)
	 */
	tOrder   map[string][]string
	sessions map[string]map[string]map[string]bool
}

func newSeriesAcc() *seriesAcc {
	return &seriesAcc{
		keys:     map[string]map[string]string{},
		values:   map[string]map[string]float64{},
		tOrder:   map[string][]string{},
		sessions: map[string]map[string]map[string]bool{},
	}
}

func (a *seriesAcc) add(sk string, key map[string]string, t string, v float64, sessionID string, trackSessions bool) {
	if _, ok := a.values[sk]; !ok {
		a.order = append(a.order, sk)
		a.keys[sk] = key
		a.values[sk] = map[string]float64{}
		a.sessions[sk] = map[string]map[string]bool{}
	}
	if _, seen := a.values[sk][t]; !seen {
		a.tOrder[sk] = append(a.tOrder[sk], t)
	}
	a.values[sk][t] += v
	if trackSessions {
		if a.sessions[sk][t] == nil {
			a.sessions[sk][t] = map[string]bool{}
		}
		a.sessions[sk][t][sessionID] = true
	}
}

func (a *seriesAcc) build(metric string, axes []string) []seriesDTO {
	isSessions := metric == "sessions"
	out := make([]seriesDTO, 0, len(a.order))
	for _, sk := range a.order {
		/*
		 * sessions 는 행 수가 아니라 **서로 다른 세션 수**다. 시간 단위에서 한 세션이 여러 칸에
		 * 걸치므로 두 군데에서 각각 중복을 걷어내야 한다:
		 *   칸 값  — 그 시간에 살아 있던 서로 다른 세션 수
		 *   합계   — 기간 전체의 서로 다른 세션 수. **칸 값의 합이 아니다.**
		 * 합으로 두면 2시간 걸친 세션이 2로 세어진다 — 이 화면이 없애려는 그 이중 계산이다.
		 */
		vals := a.values[sk]
		// 원행이 온 순서다 — 합계는 이 순서로 더해야 골든과 마지막 자리까지 같다(위 주석 참조).
		insertion := a.tOrder[sk]
		total := 0.0
		if isSessions {
			vals = map[string]float64{}
			union := map[string]bool{}
			for t, set := range a.sessions[sk] {
				vals[t] = float64(len(set))
				for id := range set {
					union[id] = true
				}
			}
			total = float64(len(union))
		} else {
			for _, t := range insertion {
				total += vals[t]
			}
		}

		ts := append([]string(nil), insertion...)
		sort.Strings(ts)
		points := make([]pointDTO, 0, len(ts))
		for _, t := range ts {
			points = append(points, pointDTO{T: t, V: vals[t]})
		}

		key := a.keys[sk]
		labelParts := make([]string, 0, len(axes))
		for _, ax := range axes {
			labelParts = append(labelParts, key[ax])
		}
		label := seriesAll
		if len(axes) > 0 {
			label = strings.Join(labelParts, " · ")
		}
		out = append(out, seriesDTO{Key: key, Label: label, Points: points, Total: total})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Total > out[j].Total })
	return out
}

/* ── 잡동사니 ─────────────────────────────────────────────────────────── */

func parseGroupBy(raw string) ([]string, string) {
	out := make([]string, 0, maxGroupAxes)
	for _, part := range strings.Split(raw, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		if !groupFields[p] {
			return nil, "알 수 없는 그룹 축: " + p
		}
		if !contains(out, p) {
			out = append(out, p)
		}
	}
	if len(out) > maxGroupAxes {
		return nil, "그룹 축은 최대 3개입니다"
	}
	return out, ""
}

// unpricedModels — 단가 미등록 모델 목록. 비어 있지 않으면 화면이 "이 모델들은 비용에서
// 빠져 있다"고 말해야 한다. **조용히 $0 으로 접지 않는다.**
func unpricedModels(costs []cost.Result) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, c := range costs {
		if c.Priced || c.Model == "" || seen[c.Model] {
			continue
		}
		seen[c.Model] = true
		out = append(out, c.Model)
	}
	sort.Strings(out)
	return out
}

func countersDTO(m map[string][]store.KeyCount) map[string][]keyCountDTO {
	out := make(map[string][]keyCountDTO, len(m))
	for k, v := range m {
		out[k] = toKeyCountDTOs(v)
	}
	return out
}

func distinctSessions(rows []store.Session) int {
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.SessionID] = true
	}
	return len(seen)
}

func distinctSrcSessions(rows []srcRow) int {
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.sessionID] = true
	}
	return len(seen)
}

func usdOrZero(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func strPtr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
