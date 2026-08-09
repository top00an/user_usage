package store

import (
	"context"
	"fmt"

	"github.com/tscorp/user-usage/internal/db"
)

// AssignTeam 은 사용자를 팀에 배정한다(멱등 — 재배정은 팀을 덮는다). tenant 격리는 DB 계층이
// tenant.From(ctx) 를 주입해 pg RLS 가 진다(sqlite 는 단일 테넌트).
func AssignTeam(ctx context.Context, username, team string) error {
	d, err := conn()
	if err != nil {
		return err
	}
	if username == "" || team == "" {
		return fmt.Errorf("store: username·team 이 비어 있다")
	}
	// pg 는 tenant_id 가 DEFAULT current_setting 이라 컬럼 목록에서 뺀다 — 방언 공통 INSERT.
	sql := "INSERT INTO team_members(username, team) VALUES(?,?)" +
		" ON CONFLICT " + conflictTarget(d, "(username)", "(tenant_id, username)") +
		" DO UPDATE SET team=excluded.team"
	return d.Exec(ctx, sql, username, team)
}

// TeamOf 는 tenant 안의 username→team 매핑 전체를 돌려준다(팀 롤업의 조인 대체).
func TeamOf(ctx context.Context) (map[string]string, error) {
	d, err := conn()
	if err != nil {
		return nil, err
	}
	rows, err := d.Query(ctx, "SELECT username, team FROM team_members")
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[teamStr(r, "username")] = teamStr(r, "team")
	}
	return out, nil
}

// TeamMember 는 목록 조회용 한 행이다.
type TeamMember struct {
	Username string
	Team     string
}

// ListTeamMembers 는 팀·사용자 순으로 정렬된 멤버십 목록이다(프로비저닝 CLI 용).
func ListTeamMembers(ctx context.Context) ([]TeamMember, error) {
	d, err := conn()
	if err != nil {
		return nil, err
	}
	rows, err := d.Query(ctx, "SELECT username, team FROM team_members ORDER BY team, username")
	if err != nil {
		return nil, err
	}
	out := make([]TeamMember, 0, len(rows))
	for _, r := range rows {
		out = append(out, TeamMember{Username: teamStr(r, "username"), Team: teamStr(r, "team")})
	}
	return out, nil
}

func teamStr(r db.Row, col string) string {
	switch v := r[col].(type) {
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return ""
	}
}
