package db

import (
	"strings"
	"testing"
)

func TestCheckRLSPlainAppRolePasses(t *testing.T) {
	v := CheckRLS(&RoleRow{Role: "usage_app"})
	if !v.OK || v.Rejects() {
		t.Fatalf("평범한 앱 롤이 막혔다: %+v", v)
	}
}

func TestCheckRLSSuperuserIsViolation(t *testing.T) {
	v := CheckRLS(&RoleRow{Role: "postgres", Super: true})
	if v.OK || !v.Rejects() {
		t.Fatalf("SUPERUSER 를 통과시켰다: %+v", v)
	}
	if !strings.Contains(v.Message, "SUPERUSER") {
		t.Fatalf("무엇이 문제인지 말하지 않는다: %q", v.Message)
	}
	if !strings.Contains(v.Message, "postgres") {
		t.Fatalf("어느 롤이 문제인지 말하지 않으면 고칠 수가 없다: %q", v.Message)
	}
}

func TestCheckRLSBypassAloneIsViolation(t *testing.T) {
	// 슈퍼가 아니어도 격리가 깨진다.
	v := CheckRLS(&RoleRow{Role: "admin_role", BypassRLS: true})
	if v.OK || !strings.Contains(v.Message, "BYPASSRLS") {
		t.Fatalf("BYPASSRLS 단독을 놓쳤다: %+v", v)
	}
}

func TestCheckRLSBothAttrsAreBothNamed(t *testing.T) {
	v := CheckRLS(&RoleRow{Role: "root", Super: true, BypassRLS: true})
	if !strings.Contains(v.Message, "SUPERUSER+BYPASSRLS") {
		t.Fatalf("둘 다면 둘 다 말해야 한다: %q", v.Message)
	}
}

/*
 * 판정 불가는 위반과 **갈라서** 다룬다.
 *
 * 접속 실패를 위반으로 취급하면 터널을 뚫기 전 기동이 통째로 막힌다 — "터널을 먼저 뚫는다"는
 * 정상 절차가 부팅 실패로 보인다. 반대로 통과로 접으면 가드가 거짓말을 한다.
 */
func TestCheckRLSNilIsInconclusiveNotRejection(t *testing.T) {
	v := CheckRLS(nil)
	if !v.Inconclusive {
		t.Fatal("판정 불가가 표시되지 않았다")
	}
	if v.Rejects() {
		t.Fatal("판정 불가로 부팅을 거부하면 안 된다")
	}
	if v.OK {
		t.Fatal("확인 못 한 것을 '안전하다'로 기록하면 가드가 거짓말을 한다")
	}
	if v.Message == "" {
		t.Fatal("왜 확인 못 했는지가 남아야 한다")
	}
}

func TestRemedyCarriesTheFix(t *testing.T) {
	msg := Remedy("앱 DB 롤이 SUPERUSER 입니다")
	for _, want := range []string{"NOSUPERUSER", "NOBYPASSRLS", "DATABASE_URL"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("해결 문장에 %q 가 없다: %q", want, msg)
		}
	}
}

/*
 * 프로브 SQL 이 판정에 필요한 세 값을 묻는가. 이 문장이 조용히 바뀌면 CheckRLS 가 늘 빈 값을
 * 받아 **전부 통과**시킨다 — 가드가 있는데 아무것도 안 잡는 상태가 된다.
 */
func TestProbeSQLAsksForTheThreeValues(t *testing.T) {
	for _, want := range []string{"current_user", "rolsuper", "rolbypassrls", "pg_roles"} {
		if !strings.Contains(ProbeSQL, want) {
			t.Fatalf("프로브 SQL 에 %q 가 없다: %q", want, ProbeSQL)
		}
	}
}

func TestScanRoleRowReadsDriverBooleans(t *testing.T) {
	if got := ScanRoleRow(nil); got != nil {
		t.Fatal("행이 없으면 nil 이다")
	}
	r := ScanRoleRow(Row{"role": "postgres", "rolsuper": "t", "rolbypassrls": false})
	if r.Role != "postgres" || !r.Super || r.BypassRLS {
		t.Fatalf("스캔 결과가 틀렸다: %+v", r)
	}
	// 드라이버가 'f' 를 줘도 참으로 접히면 안 된다(전수 위반이 된다).
	r2 := ScanRoleRow(Row{"role": "x", "rolsuper": "f", "rolbypassrls": "f"})
	if !CheckRLS(r2).OK {
		t.Fatal("'f' 를 참으로 읽었다")
	}
}
