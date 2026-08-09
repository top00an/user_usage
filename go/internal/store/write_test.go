package store

import (
	"strings"
	"testing"

	"github.com/tscorp/user-usage/internal/db"
)

/*
 * upsertSQL 은 자리표시자 수·컬럼 목록·SET 절을 **한 곳에서** 뽑는다.
 *
 * 손으로 쓰면 컬럼을 하나 추가할 때 물음표 하나를 빠뜨리는 날이 오고, 그때 바인딩이 한 칸씩
 * 밀려 **오류 없이 틀린 값**이 저장된다. 그 정렬을 여기서 못박는다.
 */
func TestUpsertSQLKeepsPlaceholdersAlignedWithColumns(t *testing.T) {
	for _, tc := range []struct {
		name    string
		keyCols string
		cols    []string
	}{
		{"sessions", "session_id", sessionCols},
		{"series", "session_id,hour,model", seriesCols},
	} {
		sql := upsertSQL("t", tc.keyCols, tc.cols, "(x)")
		nKeys := strings.Count(tc.keyCols, ",") + 1
		wantMarks := nKeys + len(tc.cols)
		if got := strings.Count(sql, "?"); got != wantMarks {
			t.Errorf("%s: 자리표시자 %d개, 컬럼 %d개 — 어긋나면 바인딩이 밀린다\n%s",
				tc.name, got, wantMarks, sql)
		}
		for _, c := range tc.cols {
			if !strings.Contains(sql, c+"=excluded."+c) {
				t.Errorf("%s: %s 의 SET 절이 없다 — 재보고 때 그 컬럼만 옛값으로 남는다", tc.name, c)
			}
		}
	}
}

/*
 * 실제 저장 SQL 이 pg 자리표시자로 옮겨져도 번호가 밀리지 않는다.
 *
 * 이 레포의 SQL 에는 '(미상)' 같은 리터럴이 흔하다. 리터럴 안의 물음표를 자리표시자로 세는
 * 회귀는 오류 없이 값만 어긋나게 만든다.
 */
func TestRealUpsertSQLConvertsToPgWithoutDrift(t *testing.T) {
	sql := upsertSQL("usage_sessions", "session_id", sessionCols, "(tenant_id, session_id)")
	pg := db.ToPg(sql)

	if strings.Contains(pg, "?") {
		t.Fatalf("치환되지 않은 자리표시자가 남았다:\n%s", pg)
	}
	n := strings.Count(sql, "?")
	// $1..$n 이 빠짐없이, 그리고 $n+1 은 없어야 한다.
	for i := 1; i <= n; i++ {
		if !strings.Contains(pg, "$"+itoa(i)) {
			t.Fatalf("$%d 가 없다 — 번호가 밀렸다:\n%s", i, pg)
		}
	}
	if strings.Contains(pg, "$"+itoa(n+1)) {
		t.Fatalf("$%d 가 생겼다 — 리터럴 안의 ? 를 세었다", n+1)
	}
}

// UsageByUser 의 SQL 처럼 '(미상)' 리터럴이 든 문장도 그대로 지난다.
func TestUnknownModelLiteralSurvivesPgConversion(t *testing.T) {
	sql := "SELECT COALESCE(NULLIF(username,''),'" + UnknownModel + "') u FROM t WHERE kind=? LIMIT ?"
	pg := db.ToPg(sql)
	if !strings.Contains(pg, "'"+UnknownModel+"'") {
		t.Fatalf("리터럴이 훼손됐다: %s", pg)
	}
	if !strings.Contains(pg, "kind=$1") || !strings.Contains(pg, "LIMIT $2") {
		t.Fatalf("번호가 밀렸다: %s", pg)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// 방언별 충돌 대상이 갈린다 — pg 는 PK 에 tenant_id 가 들어 있다(migrations 가 단일 출처).
func TestConflictTargetSplitsByDialect(t *testing.T) {
	ctx := fresh(t)
	d, err := conn()
	if err != nil {
		t.Fatal(err)
	}
	_ = ctx
	if got := conflictTarget(d, "(session_id)", "(tenant_id, session_id)"); got != "(session_id)" {
		t.Fatalf("sqlite 충돌 대상=%s", got)
	}
}

// clip 은 룬 단위다 — 바이트로 자르면 한글이 중간에서 깨진다.
func TestClipIsRuneBased(t *testing.T) {
	if got := clip("가나다라마", 3); got != "가나다" {
		t.Fatalf("got %q", got)
	}
	if got := clip("abc", 10); got != "abc" {
		t.Fatalf("got %q", got)
	}
	if got := clip("", 5); got != "" {
		t.Fatalf("got %q", got)
	}
}

// 음수 사용량은 0 으로 접는다.
func TestNonNegFoldsNegatives(t *testing.T) {
	for _, tc := range []struct{ in, want int64 }{{5, 5}, {0, 0}, {-3, 0}} {
		if got := nonNeg(tc.in); got != tc.want {
			t.Errorf("nonNeg(%d)=%d, want %d", tc.in, got, tc.want)
		}
	}
}

// dayOf 는 ISO 앞 10자를 쓰고, 형식이 아니면 오늘이다.
func TestDayOf(t *testing.T) {
	freezeClock(t, "2026-08-07T12:00:00Z")
	if got := dayOf("2026-08-03T09:00:00.000Z"); got != "2026-08-03" {
		t.Fatalf("got %q", got)
	}
	if got := dayOf("nonsense"); got != "2026-08-07" {
		t.Fatalf("형식이 아니면 오늘이다: %q", got)
	}
	if got := dayOf(""); got != "2026-08-07" {
		t.Fatalf("got %q", got)
	}
}

// nowISO 는 JS toISOString 과 같은 모양이다(밀리초 3자리 + Z). 골든이 이 문자열을 비교한다.
func TestNowISOShape(t *testing.T) {
	freezeClock(t, "2026-08-07T12:34:56Z")
	if got := nowISO(); got != "2026-08-07T12:34:56.000Z" {
		t.Fatalf("got %q", got)
	}
}
