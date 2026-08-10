package httpapi

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/tscorp/user-usage/internal/cost"
	"github.com/tscorp/user-usage/internal/store"
	"github.com/tscorp/user-usage/internal/tz"
)

// shiftDayLocal 은 YYYY-MM-DD 를 delta 일만큼 옮긴다. 파싱 실패면 원본을 돌려준다(방어).
func shiftDayLocal(day string, delta int) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	return t.AddDate(0, 0, delta).Format("2006-01-02")
}

/*
 * /api/usage/seats — 좌석(사용자)당 비용·ROI + 기간 비교.
 *
 * 미국 사용량 제품(Cursor Admin·Copilot metrics 류)이 관리자에게 처음 보여주는 화면: 1인당 비용,
 * 활성 좌석 수, 그리고 **전 기간 대비 증감(±%)**. 현재 leaderboard 는 한 기간 합계만 주므로
 * "지난주보다 늘었나" 를 못 답한다. 여기서 같은 길이의 두 윈도우(현재·직전)를 집계해 델타를 낸다.
 *
 *   GET /api/usage/seats?days=30   (기본 30. 현재=최근 N일, 직전=그 앞 N일)
 */

type seatRowDTO struct {
	Username      string  `json:"username"`
	Sessions      int     `json:"sessions"`
	Turns         int64   `json:"turns"`
	Tokens        int64   `json:"tokens"`
	USD           float64 `json:"usd"`
	UsdPerSession float64 `json:"usdPerSession"`
	CacheHitRate  float64 `json:"cacheHitRate"`
	Priced        bool    `json:"priced"`
	// 직전 기간 대비 비용 증감률(%). 직전이 0 이면 null(신규 좌석 — 증감이 정의되지 않는다).
	UsdDeltaPct *float64 `json:"usdDeltaPct"`
	// 직전 기간에 활동이 있었는지 — 신규/복귀 좌석 구분.
	IsNew bool `json:"isNew"`
}

type seatsSummaryDTO struct {
	ActiveSeats      int      `json:"activeSeats"`     // 현재 기간 세션 ≥1 인 사용자 수
	PrevActiveSeats  int      `json:"prevActiveSeats"` // 직전 기간
	TotalUsd         float64  `json:"totalUsd"`
	PrevTotalUsd     float64  `json:"prevTotalUsd"`
	UsdPerSeat       float64  `json:"usdPerSeat"` // 활성 좌석당 평균 비용
	TotalUsdDeltaPct *float64 `json:"totalUsdDeltaPct"`
}

type seatsResponse struct {
	Days     int             `json:"days"`
	From     string          `json:"from"`
	To       string          `json:"to"`
	PrevFrom string          `json:"prevFrom"`
	PrevTo   string          `json:"prevTo"`
	Summary  seatsSummaryDTO `json:"summary"`
	Seats    []seatRowDTO    `json:"seats"`
	PricedAt string          `json:"pricedAt"`
	Unpriced []string        `json:"unpriced"`
}

// seatAcc 는 한 사용자의 한 기간 집계다.
type seatAcc struct {
	sessions         int
	turns, tokens    int64
	cacheRead, denom int64
	usd              float64
	priced           bool
}

func (s *server) routeSeats(w http.ResponseWriter, r *http.Request, c *rctx) (bool, error) {
	ctx := r.Context()
	days := clampInt(int(numOr(c.query.Get("days"), 30)), 1, 3650)

	// 로컬(집계 시간대) 기준 오늘. 윈도우는 로컬 day 라벨로 필터된다(store.Filter From/To).
	today := tz.LocalDay(time.Now().UTC().Format(time.RFC3339), tz.OffsetMin())
	from := shiftDayLocal(today, -(days - 1))
	to := today
	prevTo := shiftDayLocal(from, -1)
	prevFrom := shiftDayLocal(from, -days)

	cur, curCosts, err := seatWindow(ctx, from, to)
	if err != nil {
		return true, err
	}
	prev, _, err := seatWindow(ctx, prevFrom, prevTo)
	if err != nil {
		return true, err
	}

	// 사용자별 집계.
	curAgg := aggregateSeats(cur, curCosts)
	prevAgg := aggregateSeatsUSDOnly(prev)

	seats := make([]seatRowDTO, 0, len(curAgg))
	var totalUsd float64
	for user, a := range curAgg {
		row := seatRowDTO{
			Username: user, Sessions: a.sessions, Turns: a.turns, Tokens: a.tokens,
			USD: a.usd, Priced: a.priced,
		}
		if a.sessions > 0 {
			row.UsdPerSession = a.usd / float64(a.sessions)
		}
		if a.denom > 0 {
			row.CacheHitRate = float64(a.cacheRead) / float64(a.denom)
		}
		if pu, ok := prevAgg[user]; ok && pu > 0 {
			d := (a.usd - pu) / pu * 100
			row.UsdDeltaPct = &d
		} else {
			row.IsNew = true // 직전 기간에 활동 없음(신규/복귀)
		}
		seats = append(seats, row)
		totalUsd += a.usd
	}
	// 비용 내림차순(동률이면 토큰).
	sort.SliceStable(seats, func(i, j int) bool {
		if seats[i].USD != seats[j].USD {
			return seats[i].USD > seats[j].USD
		}
		return seats[i].Tokens > seats[j].Tokens
	})

	var prevTotalUsd float64
	for _, u := range prevAgg {
		prevTotalUsd += u
	}
	summary := seatsSummaryDTO{
		ActiveSeats: len(curAgg), PrevActiveSeats: len(prevAgg),
		TotalUsd: totalUsd, PrevTotalUsd: prevTotalUsd,
	}
	if len(curAgg) > 0 {
		summary.UsdPerSeat = totalUsd / float64(len(curAgg))
	}
	if prevTotalUsd > 0 {
		d := (totalUsd - prevTotalUsd) / prevTotalUsd * 100
		summary.TotalUsdDeltaPct = &d
	}

	writeJSON(w, http.StatusOK, seatsResponse{
		Days: days, From: from, To: to, PrevFrom: prevFrom, PrevTo: prevTo,
		Summary: summary, Seats: seats,
		PricedAt: cost.PricedAt(), Unpriced: unpricedModels(curCosts),
	}, nil)
	return true, nil
}

// ── 팀 롤업 ─────────────────────────────────────────────────────────────────

type teamRowDTO struct {
	Team         string   `json:"team"`
	Members      int      `json:"members"`
	Sessions     int      `json:"sessions"`
	Turns        int64    `json:"turns"`
	Tokens       int64    `json:"tokens"`
	USD          float64  `json:"usd"`
	UsdPerMember float64  `json:"usdPerMember"`
	Usernames    []string `json:"usernames"`
}

type teamsResponse struct {
	Days     int          `json:"days"`
	From     string       `json:"from"`
	To       string       `json:"to"`
	Teams    []teamRowDTO `json:"teams"`
	PricedAt string       `json:"pricedAt"`
	Unpriced []string     `json:"unpriced"`
}

// routeTeams 는 사용자별 집계를 team_members 매핑으로 팀에 롤업한다. 팀 미배정 사용자는
// "미배정" 그룹으로 모은다(숨기면 총합이 팀 합과 안 맞아 사람이 혼란한다).
func (s *server) routeTeams(w http.ResponseWriter, r *http.Request, c *rctx) (bool, error) {
	ctx := r.Context()
	days := clampInt(int(numOr(c.query.Get("days"), 30)), 1, 3650)
	today := tz.LocalDay(time.Now().UTC().Format(time.RFC3339), tz.OffsetMin())
	from := shiftDayLocal(today, -(days - 1))
	to := today

	rows, costs, err := seatWindow(ctx, from, to)
	if err != nil {
		return true, err
	}
	teamOf, err := store.TeamOf(ctx)
	if err != nil {
		return true, err
	}
	perUser := aggregateSeats(rows, costs)

	type tacc struct {
		members       map[string]bool
		sessions      int
		turns, tokens int64
		usd           float64
	}
	const unassigned = "미배정"
	by := map[string]*tacc{}
	for user, a := range perUser {
		team := teamOf[user]
		if team == "" {
			team = unassigned
		}
		e := by[team]
		if e == nil {
			e = &tacc{members: map[string]bool{}}
			by[team] = e
		}
		e.members[user] = true
		e.sessions += a.sessions
		e.turns += a.turns
		e.tokens += a.tokens
		e.usd += a.usd
	}

	teams := make([]teamRowDTO, 0, len(by))
	for team, e := range by {
		names := make([]string, 0, len(e.members))
		for u := range e.members {
			names = append(names, u)
		}
		sort.Strings(names)
		row := teamRowDTO{
			Team: team, Members: len(e.members), Sessions: e.sessions,
			Turns: e.turns, Tokens: e.tokens, USD: e.usd, Usernames: names,
		}
		if len(e.members) > 0 {
			row.UsdPerMember = e.usd / float64(len(e.members))
		}
		teams = append(teams, row)
	}
	sort.SliceStable(teams, func(i, j int) bool {
		if teams[i].USD != teams[j].USD {
			return teams[i].USD > teams[j].USD
		}
		return teams[i].Team < teams[j].Team
	})

	writeJSON(w, http.StatusOK, teamsResponse{
		Days: days, From: from, To: to, Teams: teams,
		PricedAt: cost.PricedAt(), Unpriced: unpricedModels(costs),
	}, nil)
	return true, nil
}

// seatWindow 는 한 기간의 세션 rows 와 그 비용을 돌려준다(leaderboard 와 같은 계산).
func seatWindow(ctx context.Context, from, to string) ([]store.Session, []cost.Result, error) {
	rows, err := store.SessionRows(ctx, store.Filter{From: from, To: to, Limit: store.SessionRowsMax})
	if err != nil {
		return nil, nil, err
	}
	table := cost.Pricing(time.Now())
	mult := cost.Multipliers()
	costs := make([]cost.Result, 0, len(rows))
	for _, row := range rows {
		costs = append(costs, cost.CostOf(sessionUsage(row), table, mult))
	}
	return rows, costs, nil
}

func aggregateSeats(rows []store.Session, costs []cost.Result) map[string]*seatAcc {
	by := map[string]*seatAcc{}
	for i, row := range rows {
		key := row.Username
		if key == "" {
			key = unknownValue
		}
		e := by[key]
		if e == nil {
			e = &seatAcc{}
			by[key] = e
		}
		e.sessions++
		e.turns += row.Turns
		e.tokens += row.Input + row.Output + row.CacheRead + row.CacheCreate
		e.cacheRead += row.CacheRead
		e.denom += row.CacheRead + row.CacheCreate + row.Input
		if i < len(costs) && costs[i].Priced {
			e.usd += costs[i].USD
			e.priced = true
		}
	}
	return by
}

// aggregateSeatsUSDOnly 는 직전 기간의 사용자별 비용만 접는다(델타 분모).
func aggregateSeatsUSDOnly(rows []store.Session) map[string]float64 {
	table := cost.Pricing(time.Now())
	mult := cost.Multipliers()
	by := map[string]float64{}
	for _, row := range rows {
		key := row.Username
		if key == "" {
			key = unknownValue
		}
		res := cost.CostOf(sessionUsage(row), table, mult)
		if res.Priced {
			by[key] += res.USD
		} else if _, ok := by[key]; !ok {
			by[key] = 0 // 활동은 있었으나 unpriced — 키는 존재(신규 아님)
		}
	}
	return by
}
