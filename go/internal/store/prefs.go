package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tscorp/user-usage/internal/db"
)

/*
 * 대시보드 캔버스 배치(유저별)의 저장 계층.
 *
 * 표는 하나다 — user_dashboard_layout(사람당 한 행). pg 스키마는
 * migrations/pg/0041_dashboard_layout.sql 이 소유하고, sqlite 는 **이 파일의
 * ensureDashboardLayout 이** 소유한다(다른 표는 store.Init 의 sqliteDDL 이 소유하는데,
 * 이 표만 소유자가 다른 이유는 그 함수의 주석에 있다).
 *
 * 이 계층이 지는 것과 지지 않는 것:
 *
 *   · 진다 — 사람당 한 행(덮어쓰기)·남의 행이 안 섞이는 것·시각 문자열의 방언 통일.
 *   · 안 진다 — **좌표 검증**. 개수·id·x/y/w/h 범위와 정수 여부는 httpapi/prefs.go 가 막고,
 *     여기 오는 값은 이미 통과한 값이다. 검증을 두 곳에 두면 한쪽만 고쳐지는 날이 온다.
 *     다만 "비어 있지 않은 본문"만은 여기서도 본다 — 빈 문자열은 pg 의 jsonb 에서 파싱
 *     오류이고 sqlite 에서는 조용히 들어가서, 방언이 갈리는 유일한 값이기 때문이다.
 *
 * tenant 격리는 다른 store 함수와 같다 — pg 는 tenant.From(ctx) 를 RLS 로 받고(0041 의
 * tenant_isolation 정책), sqlite 는 단일 테넌트다.
 */

// ErrNoUsername — 사람 신원 없이 이 표를 건드리려 했다. 통과시키면 ""라는 유령 사용자의 행이
// 생기고, 그 행은 아무에게도 보이지 않은 채 남는다(호출부는 그 전에 403 을 낸다).
var ErrNoUsername = fmt.Errorf("store: 대시보드 레이아웃에는 username 이 필요하다")

// ErrEmptyLayout — 빈 본문. pg 의 jsonb 는 파싱 오류를 내고 sqlite 는 받아 준다 —
// 방언이 갈리기 전에 여기서 끊는다.
var ErrEmptyLayout = fmt.Errorf("store: 레이아웃 본문이 비었다")

/*
 * ensureDashboardLayout 은 sqlite 표를 멱등하게 보장한다.
 *
 * ⚠ 왜 store.Init 의 sqliteDDL 이 아니라 여기인가: 이 웨이브에서 store.go 는 이 오너의
 *   파일이 아니다(계약 §6). 같은 파일을 둘이 만지면 조용히 덮어쓰므로, 표의 소유를 이 파일로
 *   가져오고 **쓰기·읽기 직전에** 보장한다. `CREATE TABLE IF NOT EXISTS` 는 이미 있는 표에
 *   대해 사실상 무비용이고(카탈로그 조회 한 번), 이 경로는 사람이 대시보드를 저장할 때만
 *   돈다 — 인테이크 같은 뜨거운 경로가 아니다.
 *   ▷ PM 이 store.go 를 열어 줄 수 있으면 이 DDL 은 sqliteDDL 로 옮기는 편이 낫다.
 *     그때 이 함수는 지우고 호출부 세 줄만 빼면 된다(그것 말고 다른 데를 안 건드리게
 *     일부러 이 모양으로 뒀다).
 *
 * pg 는 아무것도 하지 않는다 — 스키마는 migrations 소유이고, 앱 롤에 CREATE 권한이 없을 수
 * 있다(org.Init·store.Init 과 같은 규칙).
 *
 * sqlite 에 tenant_id 가 없는 것은 이 레포의 관례다(team_members·member_tokens·auth_users
 * 전부 동일) — 단일 테넌트라 격리 대상이 없다. pg 의 PK 는 (tenant_id, username) 이라
 * 충돌 대상 목록이 방언마다 다르고, 그 차이는 conflictTarget 이 흡수한다.
 */
func ensureDashboardLayout(ctx context.Context, d db.DB) error {
	if d.Dialect() != db.DialectSQLite {
		return nil
	}
	if err := d.Exec(ctx, `CREATE TABLE IF NOT EXISTS user_dashboard_layout (
		username TEXT PRIMARY KEY,
		layout TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("store: 대시보드 레이아웃 표 보장 실패: %w", err)
	}
	return nil
}

// layoutConn 은 커넥션을 얻고 표를 보장하고 username 을 다듬는다 — 세 함수의 공통 서두다.
func layoutConn(ctx context.Context, username string) (db.DB, string, error) {
	d, err := conn()
	if err != nil {
		return nil, "", err
	}
	u := strings.TrimSpace(username)
	if u == "" {
		return nil, "", ErrNoUsername
	}
	if err := ensureDashboardLayout(ctx, d); err != nil {
		return nil, "", err
	}
	return d, u, nil
}

/*
 * GetDashboardLayout 은 그 사람의 배치를 **저장된 바이트 그대로** 돌려준다.
 *
 * ok=false 는 "저장한 적이 없다"이고 raw=nil 이다. **빈 배열과 다르다** — 화면은 전자에서만
 * 기본 배치로 떨어져야 하고, 둘을 뭉치면 "패널을 전부 치운 사람"의 설정이 매번 되살아난다.
 *
 * ⚠ layout 을 `CAST(... AS text)` 로 읽는다. pg 의 jsonb 는 드라이버(pgx)가 **JSON 을
 *   디코드해서** 돌려주므로(map/slice) 그대로 읽으면 문자열이 아니고, teamStr 은 그것을 빈
 *   문자열로 접는다 — 즉 캐스트가 없으면 pg 에서만 배치가 조용히 사라진다. CAST 는 두 방언
 *   공통 문법이라 SQL 을 두 벌 쓰지 않아도 된다.
 */
func GetDashboardLayout(ctx context.Context, username string) ([]byte, time.Time, bool, error) {
	d, u, err := layoutConn(ctx, username)
	if err != nil {
		return nil, time.Time{}, false, err
	}
	row, err := d.QueryRow(ctx,
		"SELECT CAST(layout AS text) AS layout, updated_at FROM user_dashboard_layout WHERE username=?", u)
	if err != nil {
		return nil, time.Time{}, false, fmt.Errorf("store: 대시보드 레이아웃 조회 실패: %w", err)
	}
	if row == nil {
		return nil, time.Time{}, false, nil
	}
	raw := teamStr(row, "layout")
	if strings.TrimSpace(raw) == "" {
		// 행은 있는데 본문이 비었다 — NOT NULL 이라 정상 경로로는 생길 수 없다. "저장 없음"으로
		// 접는다: 여기서 빈 문자열을 응답에 실으면 화면의 JSON 파싱이 대신 죽는다.
		return nil, time.Time{}, false, nil
	}
	return []byte(raw), parseLayoutTime(teamStr(row, "updated_at")), true, nil
}

/*
 * PutDashboardLayout 은 그 사람의 배치를 덮어쓰고 저장 시각을 돌려준다.
 *
 * 누적이 아니라 **덮어쓰기**다(PK 가 (tenant_id, username) 인 이유). 화면이 보내는 것은 항상
 * 전체 배치이고, 부분 갱신을 열면 "지금 배치가 무엇인가"의 답이 두 곳에 생긴다.
 */
func PutDashboardLayout(ctx context.Context, username string, raw []byte) (time.Time, error) {
	d, u, err := layoutConn(ctx, username)
	if err != nil {
		return time.Time{}, err
	}
	body := string(raw)
	if strings.TrimSpace(body) == "" {
		return time.Time{}, ErrEmptyLayout
	}
	at := clock().UTC()
	// pg 는 tenant_id 가 DEFAULT current_setting 이라 컬럼 목록에서 뺀다(member_tokens 와 같은 규율).
	// 충돌 대상만 방언이 갈린다 — pg 의 PK 에는 tenant_id 가 들어 있다.
	sql := "INSERT INTO user_dashboard_layout(username, layout, updated_at) VALUES(?,?,?)" +
		" ON CONFLICT " + conflictTarget(d, "(username)", "(tenant_id, username)") +
		" DO UPDATE SET layout=excluded.layout, updated_at=excluded.updated_at"
	/*
	 * ⚠ layout 도 updated_at 도 **문자열**로 넘긴다.
	 *   · jsonb — pgx 는 string 파라미터를 원본 JSON 바이트로 그대로 싣는다(다시 감싸지 않는다).
	 *   · 시각 — 컬럼이 text(RFC3339)인 이유는 0041 주석이 단일 출처다. 요약하면 time.Time 을
	 *     넘기는 순간 sqlite 드라이버가 `t.String()`("… +0000 UTC")로 적어 방언마다 다른
	 *     문자열이 남는다. nowISO 는 이 레포의 다른 시각 컬럼과 같은 포맷이다.
	 */
	if err := d.Exec(ctx, sql, u, body, nowISO()); err != nil {
		return time.Time{}, fmt.Errorf("store: 대시보드 레이아웃 저장 실패: %w", err)
	}
	return at, nil
}

// DeleteDashboardLayout 은 그 사람의 배치를 지운다(멱등 — 없는 것을 지워도 오류가 아니다).
// 지우면 화면은 기본 배치로 돌아간다. 남의 행은 건드리지 않는다.
func DeleteDashboardLayout(ctx context.Context, username string) error {
	d, u, err := layoutConn(ctx, username)
	if err != nil {
		return err
	}
	if err := d.Exec(ctx, "DELETE FROM user_dashboard_layout WHERE username=?", u); err != nil {
		return fmt.Errorf("store: 대시보드 레이아웃 삭제 실패: %w", err)
	}
	return nil
}

/*
 * parseLayoutTime 은 저장된 시각 문자열을 time.Time 으로 읽는다.
 *
 * 못 읽으면 **제로값**이다(오류가 아니다): 시각은 화면의 "언제 저장됨" 표시에만 쓰이는 부가
 * 정보라, 그것 하나 때문에 배치 전체를 못 돌려주는 쪽이 더 나쁘다. 호출부는 제로값을 빈
 * 문자열로 내보낸다.
 */
func parseLayoutTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}
