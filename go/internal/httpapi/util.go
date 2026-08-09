package httpapi

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
)

/*
 * jsNum 은 JavaScript 의 `Number(s)` 다. 왜 그대로 옮기는가: 현행 라우트가 쿼리 문자열을
 * `Number(x) || 기본값` 으로 읽고 그 판정이 응답에 그대로 남는다. Go 의 ParseFloat 로
 * "대충 비슷하게" 옮기면 빈 문자열·공백·NaN 의 처리가 갈리고, 그 차이는 기본값이 달라지는
 * 모양으로만 보인다(에러가 아니다).
 *
 * ok=false 는 NaN 이라는 뜻이다.
 */
func jsNum(s string) (float64, bool) {
	t := strings.TrimSpace(s)
	if t == "" {
		return 0, true // Number('') === 0
	}
	// 0x·0o·0b 리터럴도 Number() 는 받는다.
	if len(t) > 2 && (t[0] == '0') {
		switch t[1] {
		case 'x', 'X':
			if n, err := strconv.ParseInt(t[2:], 16, 64); err == nil {
				return float64(n), true
			}
			return 0, false
		case 'o', 'O':
			if n, err := strconv.ParseInt(t[2:], 8, 64); err == nil {
				return float64(n), true
			}
			return 0, false
		case 'b', 'B':
			if n, err := strconv.ParseInt(t[2:], 2, 64); err == nil {
				return float64(n), true
			}
			return 0, false
		}
	}
	n, err := strconv.ParseFloat(t, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

/*
 * numOr 은 `Math.floor(Number(raw)) || dflt` 의 정수 버전이다.
 *
 * JS 에서 falsy 는 0 과 NaN 이므로 **0 도 기본값으로 떨어진다.** 그 규칙까지 옮긴다 —
 * `?top=0` 이 "0개"가 아니라 기본값인 것은 현행 동작이고 화면이 그것에 기대고 있다.
 */
func numOr(raw string, dflt float64) float64 {
	n, ok := jsNum(raw)
	if !ok {
		return dflt
	}
	n = math.Floor(n)
	if n == 0 || math.IsNaN(n) {
		return dflt
	}
	return n
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func hasPrefix(s, prefix string) bool { return strings.HasPrefix(s, prefix) }

// decodeJSONBody 는 본문을 상한까지만 읽고 파싱한다. 실패는 호출부가 판단한다 —
// 이 레포에서 잘못된 본문은 대개 "안내 문구가 있는 400" 이고, 그 문구는 라우트가 만든다.
func decodeJSONBody(r *http.Request, dst any) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, bodyLimit+1))
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dst)
}
