package store

// 개발 지표 조회 — LOC(추가·삭제 줄 수)와 편집 결정(accept/reject).
// 새 엔드포인트(/api/usage/dev)만 쓰므로 골든 계약(44개)에 닿지 않는다.

import (
	"context"
	"strings"
)

// DevTotals 는 전체 개발 지표 합계다.
type DevTotals struct {
	LinesAdded    int64 `json:"linesAdded"`
	LinesRemoved  int64 `json:"linesRemoved"`
	EditsAccepted int64 `json:"editsAccepted"`
	EditsRejected int64 `json:"editsRejected"`
}

// DevDay 는 하루치 개발 지표다.
type DevDay struct {
	Day           string `json:"day"`
	LinesAdded    int64  `json:"linesAdded"`
	LinesRemoved  int64  `json:"linesRemoved"`
	EditsAccepted int64  `json:"editsAccepted"`
	EditsRejected int64  `json:"editsRejected"`
}

// DevMetrics 는 전체 합계 + 일별을 함께 돌려준다.
func DevMetrics(ctx context.Context, days int) (DevTotals, []DevDay, error) {
	return DevMetricsWithFilter(ctx, days, Filter{})
}

// DevMetricsWithFilter 는 필터가 걸린 개발 지표다. 근거 표가 usage_sessions 하나라
// sessionWhere 를 그대로 재사용한다 — 되짚을 표가 없다.
func DevMetricsWithFilter(ctx context.Context, days int, f Filter) (DevTotals, []DevDay, error) {
	d, err := conn()
	if err != nil {
		return DevTotals{}, nil, err
	}
	where, args := sessionWhere(f)

	totSQL := "SELECT COALESCE(SUM(lines_added),0) la, COALESCE(SUM(lines_removed),0) lr," +
		" COALESCE(SUM(edits_accepted),0) ea, COALESCE(SUM(edits_rejected),0) er" +
		" FROM usage_sessions"
	if len(where) > 0 {
		totSQL += " WHERE " + strings.Join(where, " AND ")
	}
	var tot DevTotals
	if row, err := d.QueryRow(ctx, totSQL, args...); err != nil {
		return DevTotals{}, nil, err
	} else if row != nil {
		tot = DevTotals{
			LinesAdded: row.Int("la"), LinesRemoved: row.Int("lr"),
			EditsAccepted: row.Int("ea"), EditsRejected: row.Int("er"),
		}
	}

	lim := clampInt(days, 1, 365, 30)
	// UsageByDay 와 같은 관용구 — `started_at IS NOT NULL` 은 전제이고 필터는 그 뒤다.
	dayWhere := append([]string{"started_at IS NOT NULL"}, where...)
	dayArgs := append(append([]any{}, args...), lim)
	rows, err := d.Query(ctx,
		"SELECT substr(started_at,1,10) d,"+
			" COALESCE(SUM(lines_added),0) la, COALESCE(SUM(lines_removed),0) lr,"+
			" COALESCE(SUM(edits_accepted),0) ea, COALESCE(SUM(edits_rejected),0) er"+
			" FROM usage_sessions WHERE "+strings.Join(dayWhere, " AND ")+
			" GROUP BY d ORDER BY d DESC LIMIT ?", dayArgs...)
	if err != nil {
		return DevTotals{}, nil, err
	}
	byDay := make([]DevDay, 0, len(rows))
	for _, r := range rows {
		byDay = append(byDay, DevDay{
			Day: r.Str("d"), LinesAdded: r.Int("la"), LinesRemoved: r.Int("lr"),
			EditsAccepted: r.Int("ea"), EditsRejected: r.Int("er"),
		})
	}
	return tot, byDay, nil
}
