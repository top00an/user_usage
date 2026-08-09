package store

// 개발 지표 조회 — LOC(추가·삭제 줄 수)와 편집 결정(accept/reject).
// 새 엔드포인트(/api/usage/dev)만 쓰므로 골든 계약(44개)에 닿지 않는다.

import "context"

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
	d, err := conn()
	if err != nil {
		return DevTotals{}, nil, err
	}

	var tot DevTotals
	if row, err := d.QueryRow(ctx,
		"SELECT COALESCE(SUM(lines_added),0) la, COALESCE(SUM(lines_removed),0) lr,"+
			" COALESCE(SUM(edits_accepted),0) ea, COALESCE(SUM(edits_rejected),0) er"+
			" FROM usage_sessions"); err != nil {
		return DevTotals{}, nil, err
	} else if row != nil {
		tot = DevTotals{
			LinesAdded: row.Int("la"), LinesRemoved: row.Int("lr"),
			EditsAccepted: row.Int("ea"), EditsRejected: row.Int("er"),
		}
	}

	lim := clampInt(days, 1, 365, 30)
	rows, err := d.Query(ctx,
		"SELECT substr(started_at,1,10) d,"+
			" COALESCE(SUM(lines_added),0) la, COALESCE(SUM(lines_removed),0) lr,"+
			" COALESCE(SUM(edits_accepted),0) ea, COALESCE(SUM(edits_rejected),0) er"+
			" FROM usage_sessions WHERE started_at IS NOT NULL"+
			" GROUP BY d ORDER BY d DESC LIMIT ?", lim)
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
