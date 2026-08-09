package db

import "testing"

func TestToPgNumbersPlaceholdersInOrder(t *testing.T) {
	got := ToPg("SELECT * FROM t WHERE a=? AND b=? ORDER BY c LIMIT ?")
	want := "SELECT * FROM t WHERE a=$1 AND b=$2 ORDER BY c LIMIT $3"
	if got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

/*
 * 이 케이스가 이 함수의 존재 이유다.
 *
 * 리터럴 안의 '?' 를 자리표시자로 세면 그 뒤 번호가 통째로 한 칸씩 밀린다. 그러면 SQL 은
 * 문법적으로 멀쩡하고 오류도 안 나는데 **바인딩만 어긋나** 조용히 틀린 값이 나온다.
 * 이 레포의 SQL 에는 '(미상)' 같은 리터럴이 흔하다.
 */
func TestToPgLeavesQuestionMarksInsideStringLiterals(t *testing.T) {
	got := ToPg("SELECT COALESCE(NULLIF(model,''),'(미상?)') m FROM t WHERE u=? AND k=?")
	want := "SELECT COALESCE(NULLIF(model,''),'(미상?)') m FROM t WHERE u=$1 AND k=$2"
	if got != want {
		t.Fatalf("리터럴 안의 ? 가 치환됐다\nwant %q\ngot  %q", want, got)
	}
}

func TestToPgHandlesEscapedQuotes(t *testing.T) {
	// SQL 표준 이스케이프는 '' 다 — 두 번 뒤집혀 같은 상태로 돌아온다.
	got := ToPg("SELECT 'it''s ?' , ? FROM t")
	want := "SELECT 'it''s ?' , $1 FROM t"
	if got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

func TestToPgNoPlaceholders(t *testing.T) {
	in := "SELECT COUNT(*) c FROM usage_sessions"
	if got := ToPg(in); got != in {
		t.Fatalf("자리표시자가 없으면 그대로여야 한다: %q", got)
	}
}

func TestRowAccessorsCoerceDriverTypes(t *testing.T) {
	// 드라이버마다 정수를 int64/float64/문자열로 준다. 한 자리라도 빠뜨리면 값이 조용히 0 이 된다.
	r := Row{"a": int64(7), "b": float64(9), "c": "11", "d": []byte("13"), "e": nil, "f": int32(3)}
	for _, tc := range []struct {
		col  string
		want int64
	}{{"a", 7}, {"b", 9}, {"c", 11}, {"d", 13}, {"e", 0}, {"f", 3}, {"nope", 0}} {
		if got := r.Int(tc.col); got != tc.want {
			t.Fatalf("Int(%q) want %d, got %d", tc.col, tc.want, got)
		}
	}
	if got := r.Str("d"); got != "13" {
		t.Fatalf("Str([]byte) want 13, got %q", got)
	}
	if got := r.Str("e"); got != "" {
		t.Fatalf("NULL 은 빈 문자열이다, got %q", got)
	}
}

/*
 * NULL 과 0 을 갈라야 하는 자리가 실제로 있다: usage_sessions.no_ts_turns 의 NULL 은
 * "구버전 수집기라 모른다" 이고 0 은 "전 턴에 시각이 있었다" 다. 뭉치면 화면이 구버전 PC 를
 * "정상" 으로 단정한다.
 */
func TestRowIsNullSeparatesUnknownFromZero(t *testing.T) {
	r := Row{"known": int64(0), "unknown": nil}
	if r.IsNull("known") {
		t.Fatal("0 을 NULL 로 읽었다 — '없다'가 '모른다'로 바뀐다")
	}
	if !r.IsNull("unknown") {
		t.Fatal("NULL 을 값으로 읽었다")
	}
	if !r.IsNull("absent") {
		t.Fatal("없는 컬럼은 NULL 로 본다")
	}
}

// 드라이버 타입 파서가 바뀌어 't'/'f' 문자열이 와도 참값만 참이어야 한다.
func TestRowBoolOnlyTrueIsTrue(t *testing.T) {
	r := Row{"a": true, "b": false, "c": "t", "d": "f", "e": nil, "f": "x", "g": int64(1)}
	want := map[string]bool{"a": true, "b": false, "c": true, "d": false, "e": false, "f": false, "g": false}
	for col, w := range want {
		if got := r.Bool(col); got != w {
			t.Fatalf("Bool(%q) want %v, got %v", col, w, got)
		}
	}
}
