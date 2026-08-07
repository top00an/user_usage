package store

import (
	"context"
	"time"
)

/*
 * ── 보존(retention) ──────────────────────────────────────────────────
 *
 * 키워드 축만 짧은 기한을 둔다. 이유가 둘이다:
 *
 *	① 어휘가 무제한이다. 다른 축(tool·bash·slash·skill·agent·mcp)은 사실상 고정된 이름 집합이라
 *	   행 수가 수렴하지만, keyword 는 사람이 쓰는 말이라 계속 새 행이 생긴다.
 *	   실측: 한 사람 50세션에 3,590 키 / 130,670 카운트. 팀 전체면 수만 행이다.
 *	② 이 축만 사람이 입력한 말에서 나온다. 오래 들고 있을 이유가 가장 약하고, 오래 들고 있어서
 *	   생기는 위험은 가장 크다. 추세 파악에 필요한 창은 분기면 충분하다.
 *
 * 다른 축과 usage_sessions 는 지우지 않는다 — 행 수가 작고, 비용 추세는 길게 봐야 의미가 있다.
 *
 * usage_series 는 예외다(PruneSeries). 세션당 1행이 아니라 **시간 × 모델**당 1행이라 증가
 * 속도가 다르다 — 실측으로 4일짜리 세션 하나가 버킷 24개를 만들었다. 다만 기한은 훨씬 길게
 * 둔다. 이 축은 개인 발화가 아니라 숫자뿐이고(지울 사생활 근거가 약하다), 비용 추세를 연 단위로
 * 보는 것이 이 테이블을 만든 이유의 절반이기 때문이다.
 */

const (
	KeywordRetentionDefault = 90
	KeywordRetentionMin     = 7 // 너무 짧으면 축 자체가 쓸모없어진다
	KeywordRetentionMax     = 3650

	// SeriesRetentionDefault 는 키워드(90일)보다 훨씬 길다(위 주석의 이유).
	SeriesRetentionDefault = 365
)

/*
 * RetentionDays 는 설정값을 보존 일수로 정규화한다.
 *
 * nil 은 "설정하지 않음"이라 기본값(90)이다. 그 밖의 값은 하한·상한으로 **클램프한다** —
 * 0 이나 1 은 기본값으로 되돌리지 않고 하한(7)으로 접는다. 이게 현행 Node 의 동작이고,
 * "너무 짧게 설정했다"를 조용히 "기본값"으로 바꾸면 사람은 자기 설정이 먹은 줄 안다.
 */
func RetentionDays(v *int) int {
	if v == nil {
		return KeywordRetentionDefault
	}
	d := *v
	if d > KeywordRetentionMax {
		d = KeywordRetentionMax
	}
	if d < KeywordRetentionMin {
		d = KeywordRetentionMin
	}
	return d
}

// CutoffDay 는 경계 날짜(YYYY-MM-DD)다 — 이 날짜 **이전**(미포함) 행을 지운다.
func CutoffDay(days *int, now time.Time) string {
	if now.IsZero() {
		now = clock()
	}
	return now.UTC().Add(-time.Duration(RetentionDays(days)) * 24 * time.Hour).Format("2006-01-02")
}

// daysArg 는 계약 시그니처의 `days int` 를 RetentionDays 의 옵셔널로 옮긴다.
// 0 이하는 "설정하지 않음"으로 본다(호출부가 기본값을 원할 때 0 을 넘긴다).
func daysArg(days int) *int {
	if days <= 0 {
		return nil
	}
	return &days
}

// PruneKeywords 는 기한 지난 키워드 카운터를 지우고 지운 행 수를 돌려준다.
// days <= 0 이면 기본 보존 기한(90일)을 쓴다.
func PruneKeywords(ctx context.Context, days int, now time.Time) (int, error) {
	r, err := PruneKeywordsDetail(ctx, days, now)
	return r.Removed, err
}

/*
 * PruneKeywordsDetail 은 PruneKeywords 와 같되 화면이 말할 근거까지 돌려준다
 * (무엇을 기준으로 얼마를 지웠나 — 보존 기한은 화면에 남는 값이다).
 *
 * day 가 NULL 인 행도 지운다 — day 는 적재 시점에 항상 채워지므로(CountersUpsert), NULL 은
 * 스키마 이전에 들어온 잔재이고 **나이를 알 수 없다.** 나이를 모르는 개인 발화 데이터를 영구
 * 보관하는 쪽보다 지우는 쪽이 이 축의 취지에 맞다.
 */
func PruneKeywordsDetail(ctx context.Context, days int, now time.Time) (PruneResult, error) {
	var out PruneResult
	d, err := conn()
	if err != nil {
		return out, err
	}
	arg := daysArg(days)
	cutoff := CutoffDay(arg, now)

	before, err := countKeywords(ctx)
	if err != nil {
		return out, err
	}
	if err := d.Exec(ctx,
		"DELETE FROM usage_counters WHERE kind='keyword' AND (day IS NULL OR day < ?)", cutoff); err != nil {
		return out, err
	}
	after, err := countKeywords(ctx)
	if err != nil {
		return out, err
	}
	removed := before - after
	if removed < 0 {
		removed = 0
	}
	return PruneResult{Removed: removed, Cutoff: cutoff, Days: RetentionDays(arg), Kept: after}, nil
}

func countKeywords(ctx context.Context) (int, error) {
	d, err := conn()
	if err != nil {
		return 0, err
	}
	r, err := d.QueryRow(ctx, "SELECT COUNT(*) c FROM usage_counters WHERE kind='keyword'")
	if err != nil || r == nil {
		return 0, err
	}
	return int(r.Int("c")), nil
}

/*
 * PruneSeries 는 기한 지난 시간 버킷을 지운다. 기본 365일 — 키워드(90일)보다 훨씬 길다.
 *
 * ⚠ **포팅하되 호출부를 만들지 않는다.** 모델별 값의 소급 교정(UsageByModel ①)이 이 표가
 *   온전하다는 데 기댄다 — 여기를 자동 실행 경로에 올리는 순간 과거 모델별이 조용히 줄어든다.
 *   운영자가 필요할 때 명시적으로 부른다.
 *
 * hour 가 NULL 인 행은 애초에 들어올 수 없다(PK 구성원이다). 그래서 키워드 prune 과 달리
 * NULL 처리 분기를 두지 않는다 — 없는 경우를 방어하는 코드는 그 자체가 거짓말이 된다.
 */
func PruneSeries(ctx context.Context, days int, now time.Time) (PruneResult, error) {
	var out PruneResult
	d, err := conn()
	if err != nil {
		return out, err
	}
	arg := daysArg(days)
	if arg == nil {
		def := SeriesRetentionDefault
		arg = &def
	}
	cutoff := CutoffDay(arg, now)

	before, err := countSeries(ctx)
	if err != nil {
		return out, err
	}
	if err := d.Exec(ctx, "DELETE FROM usage_series WHERE substr(hour,1,10) < ?", cutoff); err != nil {
		return out, err
	}
	after, err := countSeries(ctx)
	if err != nil {
		return out, err
	}
	removed := before - after
	if removed < 0 {
		removed = 0
	}
	return PruneResult{Removed: removed, Cutoff: cutoff, Days: RetentionDays(arg), Kept: after}, nil
}

func countSeries(ctx context.Context) (int, error) {
	d, err := conn()
	if err != nil {
		return 0, err
	}
	r, err := d.QueryRow(ctx, "SELECT COUNT(*) c FROM usage_series")
	if err != nil || r == nil {
		return 0, err
	}
	return int(r.Int("c")), nil
}
