package store

import (
	"context"
	"sort"
	"strings"
)

/*
 * ── platform 축 ───────────────────────────────────────────────────────
 *
 * 무엇을 재는 축인가: 이 세션을 만든 **도구**(claude|codex|gemini)다. 모델(model)과 다르다 —
 * 같은 모델이 여러 도구에서 쓰일 수 있고, 도구가 달라도 모델은 같을 수 있다.
 *
 * 왜 usage_sessions 에만 두는가: 한 세션은 한 도구에서 만들어지므로 세션이 이 축의 자연스러운
 * 단위다. usage_series·usage_counters 에는 두지 않는다 — 같은 사실을 두 테이블에 적으면 언젠가
 * 한쪽만 갱신되고, 그때 어느 쪽이 진실인지 아무도 모른다. 버킷은 세션에 매달려 있으므로
 * 필요한 자리에서 세션 행으로 조인해 거른다.
 *
 * ⚠ **platform 은 격리 축이 아니다.** 테넌트 격리는 종전대로 tenant_id + RLS 가 한다.
 *   조회 필터일 뿐이므로 권한 판정에 쓰면 안 된다.
 */

/*
 * Platforms 는 허용 플랫폼 식별자다. 클라이언트가 보고하는 값이라 서버가 반드시 좁힌다
 * (CounterKinds 와 같은 규율).
 *
 * ⚠ gemini 와 antigravity 는 **다른 도구다.** 사람들이 "Gemini CLI" 라 부르는 것에는 두 가지가
 *   섞여 있다: 오픈소스 google-gemini/gemini-cli 와 Google Antigravity CLI(agy). 모델이 같아
 *   model 로는 갈리지 않지만 **수집 가능 범위가 다르다** — gemini 는 세션 파일에서 도구·MCP·
 *   subagent·skill·LOC 까지 나오는 반면 antigravity 는 statusLine 의 토큰·모델·세션·프로젝트가
 *   전부다(훅 protobuf 스키마·대화 DB·transcript 전수 확인 결과 usage 외 축이 없다).
 *   한 값으로 접으면 화면의 플랫폼별 "미수집/해당 없음" 지원표가 통째로 거짓이 된다.
 */
var Platforms = []string{"claude", "codex", "gemini", "antigravity"}

const (
	/*
	 * PlatformDefault — 미지정 보고의 플랫폼.
	 *
	 * **이 기본값이 하위호환의 전부다.** 현행 수집기는 이 필드를 아예 보내지 않으므로, 여기서
	 * 채우지 않으면 과거 데이터와 앞으로의 보고가 다른 축(빈 값 vs claude)으로 갈린다.
	 */
	PlatformDefault = "claude"
	/*
	 * PlatformOther — 허용목록 밖 값의 자리.
	 *
	 * 거부하지 않는다: 서버보다 새로운 수집기의 보고가 통째로 사라진다.
	 * claude 로 접지도 않는다: 다른 도구의 사용량이 claude 로 계산되는 **조용한 오분류**가 된다.
	 * 그래서 제3의 값으로 남긴다 — 화면에서 "모르는 플랫폼이 있다"가 보이는 편이 낫다.
	 */
	PlatformOther = "other"
)

// NormalizePlatform 은 보고된 값을 저장 가능한 식별자로 좁힌다.
// 빈 값 → claude, 허용목록 → 그대로, 그 밖 → other. **빈 문자열을 돌려주지 않는다.**
func NormalizePlatform(raw string) string {
	p := strings.ToLower(strings.TrimSpace(raw))
	if p == "" {
		return PlatformDefault
	}
	for _, v := range Platforms {
		if v == p {
			return p
		}
	}
	return PlatformOther
}

// PlatformFilterValues 는 조회 필터가 받는 값이다 — 허용목록 + other.
// other 를 넣는 이유: 저장은 그 값을 만들 수 있으므로 걸러볼 수도 있어야 한다.
func PlatformFilterValues() []string {
	return append(append([]string{}, Platforms...), PlatformOther)
}

// IsPlatformFilter 는 필터 값으로 받아도 되는지 본다.
//
// 필터에서는 **정규화하지 않는다.** 오타(`claud`)를 other 로 접으면 요청한 것과 다른 집합이
// 조용히 돌아온다 — 호출부가 400 으로 되돌려주는 편이 정직하다.
func IsPlatformFilter(s string) bool {
	for _, v := range PlatformFilterValues() {
		if v == s {
			return true
		}
	}
	return false
}

/*
 * PlatformRollup — 플랫폼별 요약.
 *
 * 원행이 아니라 SQL 집계인 이유: 이 화면은 "이 서버에 어떤 플랫폼의 데이터가 얼마나 있나"에
 * 답해야 하는데, 원행 조회구(SessionRows)는 limit 로 잘리므로 큰 데이터에서 조용히 과소 보고가
 * 된다. 여기서는 자르지 않는다 — 행 수가 아니라 플랫폼 수만큼만 나온다.
 *
 * 모델별 분해(Models)를 함께 내보내는 이유: **단가는 모델별**이라 플랫폼 합계 토큰만으로는
 * 비용을 매길 수 없다. 비용 계산은 호출부(cost 패키지)가 한다 — 저장 계층은 단가를 모른다.
 * (모델별로 접은 뒤 값을 매기는 것은 세션별로 매겨 더한 것과 같다. 단가 계산이 토큰 수에
 * 선형이기 때문이다.)
 */
func PlatformRollup(ctx context.Context, f Filter) ([]PlatformRow, error) {
	d, err := conn()
	if err != nil {
		return nil, err
	}

	where, args := sessionWhere(f)
	/*
	 * 계단 분리분도 함께 합산한다. 총량과 분리분이 **같은 GROUP BY 에서** 나와야 둘의 관계
	 * (표준 = 총량 − long)가 모델 단위로 유지된다 — 여기서 빠뜨리면 플랫폼 화면만 전부 표준
	 * 구간으로 계산돼, 같은 데이터의 비용이 좌석·시계열 화면과 달라진다.
	 */
	sql := "SELECT platform p, COALESCE(NULLIF(model,''),'" + UnknownModel + "') m, COUNT(*) n," +
		" SUM(input) i, SUM(output) o, SUM(cache_read) cr, SUM(cache_create) cc," +
		" SUM(input_long) il, SUM(output_long) ol, SUM(cache_read_long) crl," +
		" MIN(started_at) first_at, MAX(started_at) last_at" +
		" FROM usage_sessions"
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	sql += " GROUP BY 1, 2"

	rows, err := d.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}

	order := []string{}
	out := map[string]*PlatformRow{}
	for _, r := range rows {
		p := r.Str("p")
		if p == "" {
			// 방어: 컬럼이 NOT NULL DEFAULT 라 실제로는 오지 않는다. 빈 키를 만들지 않는다.
			p = PlatformDefault
		}
		row, ok := out[p]
		if !ok {
			row = &PlatformRow{Platform: p, Models: []PlatformModelRow{}}
			out[p] = row
			order = append(order, p)
		}
		row.Sessions += int(r.Int("n"))
		row.Input += r.Int("i")
		row.Output += r.Int("o")
		row.CacheRead += r.Int("cr")
		row.CacheCreate += r.Int("cc")
		// 시각을 안 보낸 세션은 MIN/MAX 에서 빠진다(NULL). 빈 값을 경계로 삼지 않는다 —
		// 그러면 "모른다"가 "가장 오래된 관측"으로 둔갑한다.
		if v := r.Str("first_at"); v != "" && (row.FirstSeen == "" || v < row.FirstSeen) {
			row.FirstSeen = v
		}
		if v := r.Str("last_at"); v != "" && v > row.LastSeen {
			row.LastSeen = v
		}
		row.Models = append(row.Models, PlatformModelRow{
			Model:         r.Str("m"),
			Input:         r.Int("i"),
			Output:        r.Int("o"),
			CacheRead:     r.Int("cr"),
			CacheCreate:   r.Int("cc"),
			InputLong:     r.Int("il"),
			OutputLong:    r.Int("ol"),
			CacheReadLong: r.Int("crl"),
			Sessions:      int(r.Int("n")),
		})
	}

	res := make([]PlatformRow, 0, len(order))
	for _, p := range order {
		row := out[p]
		// 모델 순서도 결정론이어야 한다 — 화면이 흔들리면 안 되고, 비용 합산 순서가 바뀌면
		// 부동소수 마지막 자리가 갈린다.
		sort.SliceStable(row.Models, func(i, j int) bool {
			if row.Models[i].Output != row.Models[j].Output {
				return row.Models[i].Output > row.Models[j].Output
			}
			return row.Models[i].Model < row.Models[j].Model
		})
		res = append(res, *row)
	}
	sort.SliceStable(res, func(i, j int) bool {
		if res[i].Sessions != res[j].Sessions {
			return res[i].Sessions > res[j].Sessions
		}
		return res[i].Platform < res[j].Platform
	})
	return res, nil
}
