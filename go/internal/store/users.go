package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tscorp/user-usage/internal/db"
)

/*
 * 사람 계정 관리(목록·역할 변경·삭제·비밀번호 재설정)의 저장 계층.
 *
 * auth.go 가 **로그인 경로**(생성·조회·세션 발급/해석)를 소유하고, 이 파일은 그 위의 **관리
 * 경로**를 소유한다. 표는 같다(auth_users·auth_sessions) — 스키마 소유자도 그대로다
 * (sqlite: store.go 의 sqliteDDL · pg: migrations/pg/0034_auth.sql).
 *
 * 이 계층이 지는 세 가지:
 *
 *   ① **비밀번호는 평문으로 이 패키지 밖에 나가지 않는다.** 평문을 받는 함수는
 *      CreateUserWithPassword·SetUserPassword 둘뿐이고, 둘 다 HashPassword(bcrypt)로 접어
 *      저장한 뒤 아무것도 돌려주지 않는다. 반환값·오류 문구에 평문을 싣지 않는다 —
 *      오류 문구는 그대로 HTTP 응답이 되는 자리가 있다.
 *
 *   ② **자기 강등 금지는 호출부(httpapi)가 진다** — 그 판정에는 "지금 요청한 사람이 누구인가"가
 *      필요한데 저장 계층은 그것을 모른다. 반면 **마지막 관리자 보호는 여기가 진다**: 세는 것과
 *      바꾸는 것이 한 트랜잭션이 아니면 동시 요청 2건이 둘 다 통과해 관리자 0명이 되기 때문이다
 *      (보안검토 H-1 — SetUserRoleGuarded·DeleteUserGuarded 의 주석에 방언별 성립 근거가 있다).
 *
 *   ③ **역할 변경·삭제는 세션 무효화와 짝이다.** 안 하면 강등된 사람이 세션 만료까지 관리자로
 *      남고, 요청은 200 이며 아무 증상이 없다. 그래서 DeleteSessionsForUser 가 여기 있고,
 *      SetUserRole·DeleteUser 의 주석이 그 짝을 가리킨다.
 *
 * tenant 격리는 다른 store 함수와 같다 — pg 는 tenant.From(ctx) 를 RLS 로 받고, sqlite 는
 * 단일 테넌트다.
 */

// 관리 경로가 호출부에 돌려주는 판정. 오류 문구가 곧 사용자에게 보이는 안내가 되므로 DB 원문을
// 흘리지 않고 이 셋으로 접는다.
var (
	// ErrUserNotFound — 그 tenant 에 그 사용자가 없다.
	ErrUserNotFound = errors.New("store: 사용자를 찾을 수 없습니다")
	// ErrUserExists — 같은 이름의 사용자가 이미 있다(덮어쓰기는 열지 않는다 — 실수로 비밀번호를
	// 바꾸는 자리가 된다).
	ErrUserExists = errors.New("store: 이미 있는 사용자입니다")
	// ErrWeakPassword — 비밀번호가 최소 길이에 못 미친다.
	ErrWeakPassword = fmt.Errorf("store: 비밀번호는 최소 %d자여야 합니다", MinPasswordLen)
	// ErrLastAdmin — 마지막 관리자를 강등·삭제하려 했다. **판정이 변경과 같은 트랜잭션에서
	// 났다**는 것이 이 오류의 값어치다(경쟁 조건에서도 성립한다 — 보안검토 H-1).
	ErrLastAdmin = errors.New("store: 마지막 관리자입니다")
	// ErrAdminCountUnavailable — 관리자 수를 셀 수 없었다. **막는다**: 여기서 통과시키면 조회
	// 실패 한 번이 관리자 0명 잠금으로 이어진다. 호출부가 503 으로 접는다(재시도 가능한 실패다).
	ErrAdminCountUnavailable = errors.New("store: 관리자 수를 확인할 수 없다")
)

// MinPasswordLen — 비밀번호 최소 길이. 서버가 마지막 방어선이다(화면 검증은 방어가 아니다).
// 룬 수로 센다 — 한글 비밀번호가 바이트 길이로 통과/거부되면 사람이 이유를 알 수 없다.
const MinPasswordLen = 8

// UserSummary 는 관리 화면이 보는 사용자 한 행이다. **password_hash 를 담지 않는다** — 이
// 타입은 HTTP 응답까지 흘러가므로 해시조차 실을 자리가 아니다.
type UserSummary struct {
	Username  string
	Role      string
	CreatedAt string
}

// ListUsers 는 tenant(ctx) 안의 사용자를 이름순으로 돌려준다.
func ListUsers(ctx context.Context) ([]UserSummary, error) {
	d, err := conn()
	if err != nil {
		return nil, err
	}
	rows, err := d.Query(ctx, "SELECT username, role, created_at FROM auth_users ORDER BY username")
	if err != nil {
		return nil, fmt.Errorf("store: 사용자 목록 조회 실패: %w", err)
	}
	out := make([]UserSummary, 0, len(rows))
	for _, r := range rows {
		out = append(out, UserSummary{
			Username:  teamStr(r, "username"),
			Role:      teamStr(r, "role"),
			CreatedAt: teamStr(r, "created_at"),
		})
	}
	return out, nil
}

// GetUserSummary 는 사용자 한 행을 요약 shape 으로 돌려준다(관리 화면이 변경 직후 되읽는 자리).
// GetUser 와 달리 **해시를 담지 않고** created_at 을 담는다 — 응답으로 나가는 값이기 때문이다.
func GetUserSummary(ctx context.Context, username string) (UserSummary, bool, error) {
	d, err := conn()
	if err != nil {
		return UserSummary{}, false, err
	}
	row, err := d.QueryRow(ctx,
		"SELECT username, role, created_at FROM auth_users WHERE username=?", username)
	if err != nil {
		return UserSummary{}, false, fmt.Errorf("store: 사용자 조회 실패: %w", err)
	}
	if row == nil {
		return UserSummary{}, false, nil
	}
	u := UserSummary{
		Username:  teamStr(row, "username"),
		Role:      teamStr(row, "role"),
		CreatedAt: teamStr(row, "created_at"),
	}
	if u.Username == "" {
		return UserSummary{}, false, nil
	}
	return u, true, nil
}

// CountAdmins 는 tenant(ctx) 안의 admin 수다.
//
// ⚠ **이 값만으로 마지막 관리자 보호를 세우지 말라.** 세고 나서 바꾸면 그 사이에 남이 바꾼다
// (보안검토 H-1: 관리자 2명에게 동시 강등 2건 → 둘 다 n=2 를 읽고 둘 다 통과 → 관리자 0명).
// 보호가 필요한 자리는 SetUserRoleGuarded·DeleteUserGuarded 를 쓴다. 이 함수는 **표시·진단용**이다.
func CountAdmins(ctx context.Context) (int, error) {
	d, err := conn()
	if err != nil {
		return 0, err
	}
	rows, err := d.Query(ctx, "SELECT username FROM auth_users WHERE role='admin'")
	if err != nil {
		return 0, fmt.Errorf("store: 관리자 수 조회 실패: %w", err)
	}
	return len(rows), nil
}

/*
 * lockAdmins 는 **트랜잭션이 끝날 때까지** 관리자 행을 잠그고 그 수를 돌려준다. 반드시 Tx 안에서
 * 부른다 — 밖에서 부르면 잠금이 즉시 풀려 아무것도 지키지 못한다.
 *
 * 왜 방언마다 근거가 다른가:
 *
 *   · **sqlite** — 커넥션이 하나다(db/sqlite.go 의 SetMaxOpenConns(1)). Tx 가 그 유일한 커넥션을
 *     쥐고 있는 동안 다른 요청의 질의는 커넥션을 못 얻어 대기한다. 즉 트랜잭션 자체가 전면
 *     직렬화이고, FOR UPDATE 는 문법으로도 없다(붙이면 파싱 오류다).
 *
 *   · **pg** — 기본 격리수준이 READ COMMITTED 라 **트랜잭션만으로는 부족하다.** 두 트랜잭션이
 *     각자의 스냅샷에서 "관리자 2명"을 읽고 서로 다른 행을 바꾸면 충돌 없이 둘 다 커밋된다
 *     (write skew — 조건부 UPDATE 한 방으로도 같은 이유로 안 막힌다). 그래서 관리자 행 전체에
 *     FOR UPDATE 로 실제 잠금을 건다. 뒤에 온 트랜잭션은 앞이 커밋될 때까지 여기서 멈추고,
 *     깨어난 뒤 READ COMMITTED 규칙대로 **갱신된 행을 다시 판정**한다 — 강등된 행은 role='admin'
 *     조건에서 빠지고 삭제된 행은 사라지므로, 그 시점의 참값(1)을 읽는다.
 *
 * 새 관리자가 **생기는** 쪽(INSERT)은 이 잠금이 막지 않는다. 그 방향의 어긋남은 "관리자가 늘었는데
 * 거부했다"라서 안전한 쪽으로 틀린다 — 사람은 다시 누르면 된다.
 */
func lockAdmins(ctx context.Context, d db.DB) (int, error) {
	q := "SELECT username FROM auth_users WHERE role='admin'"
	if d.Dialect() == db.DialectPostgres {
		q += " FOR UPDATE"
	}
	rows, err := d.Query(ctx, q)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrAdminCountUnavailable, err)
	}
	return len(rows), nil
}

/*
 * SetUserRoleGuarded 는 role 을 바꾸되 **마지막 관리자 보호를 같은 트랜잭션 안에서** 판정한다.
 * 돌려주는 from 은 그 트랜잭션이 읽은 이전 role 이다 — 감사 로그의 `from` 과 "세션을 끊어야
 * 하는가"(강등인가)의 근거가 되므로, 트랜잭션 밖에서 미리 읽은 값이 아니라 이 값을 써야 한다.
 *
 * 반환 오류: ErrUserNotFound · ErrLastAdmin · ErrAdminCountUnavailable · 알 수 없는 role.
 * ③(자기 자신 금지)은 여기가 아니라 호출부다 — 요청 신원은 저장 계층이 모르고, 그 판정은
 * DB 상태에 의존하지 않아 경쟁 조건도 없다.
 */
func SetUserRoleGuarded(ctx context.Context, username, role string) (from string, err error) {
	d, err := conn()
	if err != nil {
		return "", err
	}
	if !isAuthRole(role) {
		return "", fmt.Errorf("store: 알 수 없는 role %q — admin|member 중 하나여야 한다", role)
	}
	err = d.Tx(ctx, func(ctx context.Context) error {
		// 잠금을 **먼저** 건다. 대상 행을 먼저 읽으면 그 사이에 남이 바꾼 값으로 판정하게 된다.
		n, lerr := lockAdmins(ctx, d)
		if lerr != nil {
			return lerr
		}
		u, ok, gerr := GetUser(ctx, username)
		if gerr != nil {
			return gerr
		}
		if !ok {
			return ErrUserNotFound
		}
		from = u.Role
		// ② 강등일 때만 본다 — 승격은 아무도 잠그지 않는다.
		if u.Role == "admin" && role != "admin" && n <= 1 {
			return ErrLastAdmin
		}
		if eerr := d.Exec(ctx, "UPDATE auth_users SET role=? WHERE username=?", role, username); eerr != nil {
			return fmt.Errorf("store: 역할 변경 실패: %w", eerr)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return from, nil
}

/*
 * DeleteUserGuarded 는 사용자를 지우되 **마지막 관리자 보호를 같은 트랜잭션 안에서** 판정한다.
 * 돌려주는 role 은 지워진 사람의 role 이다(감사 로그가 남긴다).
 *
 * ⚠ 호출부는 이 뒤에 DeleteSessionsForUser 를 반드시 부른다 — DeleteUser 의 주석 참조.
 */
func DeleteUserGuarded(ctx context.Context, username string) (role string, err error) {
	d, err := conn()
	if err != nil {
		return "", err
	}
	err = d.Tx(ctx, func(ctx context.Context) error {
		n, lerr := lockAdmins(ctx, d)
		if lerr != nil {
			return lerr
		}
		u, ok, gerr := GetUser(ctx, username)
		if gerr != nil {
			return gerr
		}
		if !ok {
			return ErrUserNotFound
		}
		role = u.Role
		// 대상이 admin 이 아니면 마지막 관리자 판정은 성립하지 않는다(지워도 관리자 수는 그대로다).
		if u.Role == "admin" && n <= 1 {
			return ErrLastAdmin
		}
		if eerr := d.Exec(ctx, "DELETE FROM auth_users WHERE username=?", username); eerr != nil {
			return fmt.Errorf("store: 사용자 삭제 실패: %w", eerr)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return role, nil
}

/*
 * CreateUserWithPassword 는 평문 비밀번호를 받아 bcrypt 로 접어 계정을 만든다.
 *
 * **평문은 이 함수 안에서 끝난다** — 반환값에도 오류 문구에도 실리지 않는다. auth.go 의
 * CreateUser 를 직접 부르는 대신 이쪽을 쓰면 "해시를 깜빡하고 평문을 넣는" 경로가 아예
 * 생기지 않는다(관리 API 는 전부 이 함수를 탄다).
 */
func CreateUserWithPassword(ctx context.Context, username, role, plaintext string) error {
	username = strings.TrimSpace(username)
	if username == "" {
		return fmt.Errorf("store: 사용자 생성에 username 이 필요하다")
	}
	if !isAuthRole(role) {
		return fmt.Errorf("store: 알 수 없는 role %q — admin|member 중 하나여야 한다", role)
	}
	if len([]rune(plaintext)) < MinPasswordLen {
		return ErrWeakPassword
	}
	if _, ok, err := GetUser(ctx, username); err != nil {
		return err
	} else if ok {
		return ErrUserExists
	}
	hash, err := HashPassword(plaintext)
	if err != nil {
		return err
	}
	return CreateUser(ctx, username, role, hash)
}

/*
 * SetUserRole 은 사용자의 role 을 바꾼다.
 *
 * ⚠ **호출부는 이 뒤에 DeleteSessionsForUser 를 반드시 부른다.** 안 부르면 강등된 사람의 살아
 *   있는 세션이 만료까지 admin 스코프를 그대로 들고 있다 — 요청은 200 이고 증상이 없다.
 *   (그 짝을 여기서 자동으로 부르지 않는 이유: 승격도 이 함수를 타는데, 승격에서 세션을 지우면
 *   방금 승격된 사람이 이유 없이 로그아웃된다. 무엇을 지울지는 호출부가 안다.)
 *
 * ⚠ **마지막 관리자 보호가 필요한 경로(관리 API)는 이 함수가 아니라 SetUserRoleGuarded 를 쓴다.**
 *   여기에는 보호가 없다 — 부트스트랩·CLI 처럼 판정 자체가 성립하지 않는 자리를 위해 남겨 둔다.
 */
func SetUserRole(ctx context.Context, username, role string) error {
	d, err := conn()
	if err != nil {
		return err
	}
	if !isAuthRole(role) {
		return fmt.Errorf("store: 알 수 없는 role %q — admin|member 중 하나여야 한다", role)
	}
	if _, ok, err := GetUser(ctx, username); err != nil {
		return err
	} else if !ok {
		return ErrUserNotFound
	}
	if err := d.Exec(ctx, "UPDATE auth_users SET role=? WHERE username=?", role, username); err != nil {
		return fmt.Errorf("store: 역할 변경 실패: %w", err)
	}
	return nil
}

// SetUserPassword 는 비밀번호를 재설정한다(bcrypt). 평문은 이 함수 안에서 끝난다.
//
// ⚠ 호출부는 이 뒤에 DeleteSessionsForUser 를 부른다 — 비밀번호를 바꾸는 이유의 절반은
// "그 계정이 털린 것 같다"이고, 그때 살아 있는 세션을 남겨 두면 재설정이 아무것도 막지 못한다.
func SetUserPassword(ctx context.Context, username, plaintext string) error {
	d, err := conn()
	if err != nil {
		return err
	}
	if len([]rune(plaintext)) < MinPasswordLen {
		return ErrWeakPassword
	}
	if _, ok, err := GetUser(ctx, username); err != nil {
		return err
	} else if !ok {
		return ErrUserNotFound
	}
	hash, err := HashPassword(plaintext)
	if err != nil {
		return err
	}
	if err := d.Exec(ctx, "UPDATE auth_users SET password_hash=? WHERE username=?", hash, username); err != nil {
		// ⚠ 원문에 평문이 실릴 자리가 없게 한다 — 위 Exec 의 인자는 해시다.
		return fmt.Errorf("store: 비밀번호 재설정 실패: %w", err)
	}
	return nil
}

/*
 * DeleteUser 는 사용자를 지운다.
 *
 * ⚠ 호출부는 이 뒤에 DeleteSessionsForUser 를 반드시 부른다. auth_sessions 는 auth_users 를
 *   FK 로 참조하지 않으므로(세션 표는 token_hash 가 PK 다) 사용자를 지워도 세션 행은 남고,
 *   ResolveSession 은 그 행만 보고 통과시킨다 — **삭제된 계정이 만료까지 살아 있다.**
 *
 * 팀 배정(team_members)·개인 열람 토큰(member_tokens)은 건드리지 않는다. 그쪽은 사용량 귀속에
 * 쓰이는 이름표라, 계정을 지웠다고 과거 데이터의 팀이 사라지면 집계가 조용히 달라진다.
 *
 * ⚠ **마지막 관리자 보호가 필요한 경로(관리 API)는 이 함수가 아니라 DeleteUserGuarded 를 쓴다.**
 */
func DeleteUser(ctx context.Context, username string) error {
	d, err := conn()
	if err != nil {
		return err
	}
	if _, ok, err := GetUser(ctx, username); err != nil {
		return err
	} else if !ok {
		return ErrUserNotFound
	}
	if err := d.Exec(ctx, "DELETE FROM auth_users WHERE username=?", username); err != nil {
		return fmt.Errorf("store: 사용자 삭제 실패: %w", err)
	}
	return nil
}

/*
 * DeleteSessionsForUser 는 그 사용자의 **모든** 세션을 무효화한다(멱등).
 *
 * DeleteSession 은 평문 토큰으로만 지운다 — 그 토큰은 그 사람의 브라우저 쿠키에만 있으므로
 * 관리자가 남의 세션을 끊을 수단이 없다. 역할 변경·삭제·비밀번호 재설정이 실제로 효력을 가지려면
 * 이름으로 지우는 자리가 필요하고, 그것이 이 함수다.
 */
func DeleteSessionsForUser(ctx context.Context, username string) error {
	d, err := conn()
	if err != nil {
		return err
	}
	if username == "" {
		return fmt.Errorf("store: 세션 무효화에 username 이 필요하다")
	}
	if err := d.Exec(ctx, "DELETE FROM auth_sessions WHERE username=?", username); err != nil {
		return fmt.Errorf("store: 세션 무효화 실패: %w", err)
	}
	return nil
}
