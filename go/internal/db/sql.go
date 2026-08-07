package db

import "strconv"

// ToPg 는 SQL 본문의 자리표시자 `?` 를 `$n` 으로 옮긴다. 현행 lib/db/sql.js 의 toPg 와 같은 규칙.
//
// **문자열 리터럴('...') 안의 `?` 는 건드리지 않는다.** 이 레포의 SQL 에는 `'(미상)'` 같은
// 리터럴이 흔하고, 언젠가 물음표가 든 리터럴이 들어오면 그때 조용히 자리표시자 번호가 밀린다 —
// 그러면 바인딩이 한 칸씩 어긋나 **오류 없이 틀린 값**이 나온다.
//
// SQL 표준의 리터럴 이스케이프는 홑따옴표를 연달아 두 번 쓰는 것이다. 그 경우 inStr 이 false→true 로
// 두 번 뒤집혀 결과적으로 같은 상태가 되므로 별도 처리가 필요 없다.
func ToPg(sql string) string {
	out := make([]byte, 0, len(sql)+8)
	n := 0
	inStr := false
	for i := 0; i < len(sql); i++ {
		ch := sql[i]
		if ch == '\'' {
			inStr = !inStr
			out = append(out, ch)
			continue
		}
		if ch == '?' && !inStr {
			n++
			out = append(out, '$')
			out = append(out, strconv.Itoa(n)...)
			continue
		}
		out = append(out, ch)
	}
	return string(out)
}

func parseInt(s string) int64 {
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		return v
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int64(f)
	}
	return 0
}

func parseFloat(s string) float64 {
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return 0
}
