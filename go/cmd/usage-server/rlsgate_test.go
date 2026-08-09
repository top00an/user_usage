package main

import (
	"testing"

	"github.com/tscorp/user-usage/internal/db"
)

/*
 * 이 파일이 재는 것은 **부팅이 조용히 틀리는 두 방향**이다:
 *   ① 위반인데 뜬다  → 전 테넌트의 행이 서로 보이는데 요청은 200 이고 증상이 없다
 *   ② 판정 불가인데 안 뜬다 → "터널을 먼저 뚫는다"는 정상 절차가 부팅 실패로 보인다
 *
 * ②가 `!v.OK` 로 분기했을 때 정확히 생기는 사고다. 실제 pg 슈퍼유저 계정 없이도 여기서 잡힌다.
 */

func TestRLSViolationRejectsBoot(t *testing.T) {
	for name, row := range map[string]*db.RoleRow{
		"SUPERUSER": {Role: "postgres", Super: true},
		"BYPASSRLS": {Role: "app", BypassRLS: true},
		"둘 다":       {Role: "admin", Super: true, BypassRLS: true},
	} {
		if got := rlsGate(db.CheckRLS(row)); got != rlsReject {
			t.Fatalf("%s → %v, want rlsReject (뜨고 나면 증상이 없는 사고다)", name, got)
		}
	}
}

func TestRLSInconclusiveDoesNotRejectBoot(t *testing.T) {
	// 붙지 못한 DB 는 노출도 없다. 여기서 죽이면 터널을 뚫는 정상 절차가 부팅 실패로 보인다.
	v := db.CheckRLS(nil)
	if !v.Inconclusive {
		t.Fatalf("CheckRLS(nil) 이 판정 불가가 아니다: %+v", v)
	}
	if got := rlsGate(v); got != rlsWarn {
		t.Fatalf("판정 불가 → %v, want rlsWarn — `!v.OK` 로 분기했을 때 정확히 나는 회귀다", got)
	}
}

func TestRLSCleanRoleProceeds(t *testing.T) {
	v := db.CheckRLS(&db.RoleRow{Role: "usage_app"})
	if got := rlsGate(v); got != rlsProceed {
		t.Fatalf("비-슈퍼·비-BYPASSRLS → %v, want rlsProceed", got)
	}
}

func TestSQLiteIsNotAnRLSSubject(t *testing.T) {
	// sqlite 는 단일 테넌트라 격리 대상이 없다. 여기서 막히면 로컬 모드가 통째로 안 뜬다.
	if got := rlsGate(db.ProbeRLS(nil, nil)); got != rlsWarn {
		t.Fatalf("nil DB → %v, want rlsWarn(판정 불가)", got)
	}
}
