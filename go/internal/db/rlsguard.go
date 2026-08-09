package db

import (
	"context"
	"fmt"
	"strings"
)

/*
 * RLS 성립 전제 가드 — 앱이 붙은 DB 롤이 테넌트 격리를 무력화하는 롤인지 판정한다.
 *
 * 왜 별도 판정 함수인가: 이 판정은 이 시스템에서 **가장 조용한 치명적 오설정**을 잡는다. 앱이
 * SUPERUSER 또는 BYPASSRLS 롤로 접속하면 `FORCE ROW LEVEL SECURITY` 조차 무시되어 모든 테넌트의
 * 행이 서로 보인다. 그런데 **증상이 없다** — 요청은 200 이고 데이터도 잘 보인다(남의 것까지).
 * 그래서 판정을 부팅 코드에 묻어두지 않고 여기로 꺼내 양성·음성 양쪽을 단위 테스트로 못박는다
 * (실제 슈퍼유저 계정 없이도 결정론적으로 검증 가능하다).
 *
 * 흔한 사고 경로: 접속 URL 을 관리/마이그레이션 롤로 지정하는 것 —
 *   · postgres 공식 이미지의 POSTGRES_USER 는 SUPERUSER + BYPASSRLS 로 만들어진다
 *   · RDS 마스터 유저도 넓은 권한을 갖는다
 */

// ProbeSQL 은 판정에 필요한 세 값을 묻는다. 이 문장이 조용히 바뀌면 CheckRLS 가 늘 빈 값을 받아
// **전부 통과**시킨다 — 가드가 있는데 아무것도 안 잡는 상태가 된다.
const ProbeSQL = `SELECT current_user AS role, rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`

// RoleRow 는 ProbeSQL 한 행이다.
type RoleRow struct {
	Role      string
	Super     bool
	BypassRLS bool
}

// Verdict 는 판정 결과다.
//
// 세 상태를 가른다 — 이 구분이 이 타입의 존재 이유다:
//
//	OK=true                     격리가 성립한다
//	OK=false, Inconclusive=false 위반이다 → 부팅을 거부한다
//	OK=false, Inconclusive=true  판정하지 못했다 → **거부하지 않는다**(아래 이유)
//
// 판정 불가(터널 미개통·DB 다운)를 거부로 다루면 안 된다. 붙지 못한 DB 는 노출도 없고,
// 여기서 죽이면 "터널을 먼저 뚫는다"는 정상 절차가 부팅 실패로 보인다. 대신 조용하지도 않다 —
// 호출부가 stderr 로 크게 남긴다.
type Verdict struct {
	OK           bool
	Inconclusive bool
	Message      string
}

// Rejects 는 이 판정으로 부팅을 거부해야 하는지 말한다.
// 호출부가 `!v.OK` 로만 분기하면 판정 불가에서 부팅이 막힌다 — 그 실수를 한 곳에서 막는다.
func (v Verdict) Rejects() bool { return !v.OK && !v.Inconclusive }

// CheckRLS 는 롤 한 행을 판정한다. row == nil 이면 Inconclusive 다.
func CheckRLS(row *RoleRow) Verdict {
	if row == nil {
		// 확인 못 한 것을 "안전하다"로 기록하면 가드가 거짓말을 한다. 통과로 접지 않는다.
		return Verdict{
			OK:           false,
			Inconclusive: true,
			Message:      "DB 롤을 확인하지 못했습니다 — RLS 테넌트 격리 전제를 판정할 수 없습니다",
		}
	}
	if !row.Super && !row.BypassRLS {
		return Verdict{OK: true}
	}
	attrs := make([]string, 0, 2)
	if row.Super {
		attrs = append(attrs, "SUPERUSER")
	}
	if row.BypassRLS {
		attrs = append(attrs, "BYPASSRLS")
	}
	role := row.Role
	if role == "" {
		role = "?"
	}
	return Verdict{
		OK:      false,
		Message: fmt.Sprintf("앱 DB 롤 '%s' 이 %s 입니다 — RLS 테넌트 격리가 성립하지 않습니다", role, strings.Join(attrs, "+")),
	}
}

// Remedy 는 위반 메시지에 사람이 바로 고칠 수 있는 해결 문장을 붙인다.
func Remedy(message string) string {
	return message + ". 접속 URL(DATABASE_URL)을 비-슈퍼·비-BYPASSRLS 앱 롤로 바꾸세요 " +
		"(CREATE ROLE … NOSUPERUSER NOBYPASSRLS). 관리/마이그레이션 롤로 서빙하면 " +
		"전 테넌트 데이터가 서로 노출됩니다."
}

// ScanRoleRow 는 ProbeSQL 의 결과 행을 RoleRow 로 옮긴다. 행이 없으면 nil 이다.
//
// ⚠ 불리언은 반드시 참값으로만 참이다. 드라이버 타입 파서가 바뀌어 't'/'f' 문자열이 오더라도
// Row.Bool 이 그것만 참으로 접는다 — 아무 truthy 값이나 참으로 세면 조용히 전수 위반이 되어
// 아무도 못 뜨거나, 반대로 전수 통과가 된다.
func ScanRoleRow(r Row) *RoleRow {
	if r == nil {
		return nil
	}
	return &RoleRow{
		Role:      r.Str("role"),
		Super:     r.Bool("rolsuper"),
		BypassRLS: r.Bool("rolbypassrls"),
	}
}

// ProbeRLS 는 실제 DB 에 물어 판정한다. sqlite 는 애초에 단일 테넌트라 판정 대상이 아니다.
// 접속 실패는 **위반이 아니라 판정 불가**로 접는다.
func ProbeRLS(ctx context.Context, d DB) Verdict {
	if d == nil {
		return CheckRLS(nil)
	}
	if d.Dialect() != DialectPostgres {
		return Verdict{OK: true, Message: "sqlite 는 단일 테넌트라 RLS 판정 대상이 아닙니다"}
	}
	row, err := d.QueryRow(ctx, ProbeSQL)
	if err != nil {
		v := CheckRLS(nil)
		// 왜 확인 못 했는지가 남아야 한다 — 없으면 사람이 엉뚱한 데를 판다.
		v.Message = v.Message + ": " + err.Error()
		return v
	}
	return CheckRLS(ScanRoleRow(row))
}
