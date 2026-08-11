package main

/*
 * ── 저장된 자리표시자 모델 라벨 정리 ────────────────────────────────────────
 *
 * 무엇을 고치는가: Claude Code 는 중단·오류 메시지 같은 턴에 모델 이름 대신 `<synthetic>` 을
 * 쓴다. 인테이크가 이제 그 값을 빈 값으로 접지만(internal/intake/intake.go 의 normModel),
 * **그 수정 이전에 저장된 행은 그대로 남는다.** 모델 축에 `<synthetic>` 행이 계속 보이는 것은
 * 그 잔여다.
 *
 * 무엇을 하지 않는가: **숫자를 움직이지 않는다.** 자리표시자 턴의 토큰은 0 이므로 라벨만
 * 바꾸면 되고, 세션·버킷·카운터는 하나도 버리지 않는다. `①+②+③ == Totals` 불변식은 정리
 * 전후 모두 성립한다(maintenance_test.go 가 못 박는다).
 *
 * ⚠ **자동 실행 경로에 올리지 않는다.** 되돌리기 어려운 작업은 사람이 명시적으로 돌린다 —
 *   이 레포의 규율이고, 그래서 부팅·store.Init 어디에도 걸려 있지 않다. 기본값도 dry-run 이다.
 *
 * pg 쪽 같은 일은 migrations/pg/0037_placeholder_model_cleanup.sql 이 소유한다(양 방언 동기).
 * 이 CLI 는 서버와 같은 env(USAGE_DB_MODE·DATABASE_URL·USAGE_DATA_DIR)를 보므로 sqlite·pg
 * 양쪽에 듣는다. 다만 pg 는 테넌트당 한 번씩 돌려야 한다(--tenant) — RLS 가 한 번에 한
 * 테넌트만 보여 주기 때문이다.
 */

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/tscorp/user-usage/internal/db"
	"github.com/tscorp/user-usage/internal/store"
	"github.com/tscorp/user-usage/internal/tenant"
)

/*
 * 자리표시자 판정. **판정 규칙의 단일 출처는 internal/intake 의 placeholderModelRe 다** —
 * 꺾쇠로 감싼 값 **전체**가 자리표시자이고, 꺾쇠가 일부만 있는 값(`a<b>c`)은 아니다.
 *
 * 여기 같은 리터럴이 한 벌 더 있는 이유는 그쪽이 unexported 이고 이 파일이 internal/** 을
 * 고칠 수 없기 때문이다. 두 판정이 갈리는 것을 막는 것은 주석이 아니라 테스트다 —
 * maintenance_test.go 의 TestPlaceholderRuleMatchesIntake 가 intake 의 공개 표면을 통해
 * 두 판정을 대조한다. 한쪽만 고치면 그 테스트가 깨진다.
 */
var placeholderModelRe = regexp.MustCompile(`^<[^<>]*>$`)

// isJSSpace 는 intake 의 같은 이름 함수와 같다 — unicode.IsSpace 에 BOM 을 더한 것이 JS 의 `\s` 다.
// intake 는 판정 **전에** 이 규칙으로 앞뒤를 떼므로(clip), 여기서도 떼야 판정이 갈리지 않는다.
func isJSSpace(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' }

func isPlaceholderModel(m string) bool {
	return placeholderModelRe.MatchString(strings.TrimFunc(m, isJSSpace))
}

/*
 * usage_series 병합 규칙 — 컬럼마다 결합 방식이 다르다.
 *
 * PK 에 model 이 들어 있어(session_id, hour, model) 라벨만 바꾸면 같은 시각의 기존 버킷과
 * 충돌한다. 두 버킷은 정리 후 **같은 시각의 같은 모델**이 되므로 합산 병합이 맞다.
 * 다만 전부 합이 아니다: 지연 최댓값은 MAX 이고, 라벨 컬럼은 기존 값을 유지한다.
 *
 * ⚠ 컬럼이 늘면 여기에 배정해야 한다. 빠뜨리면 병합된 버킷에서 그 축이 조용히 사라진다 —
 *   TestSeriesMergeCoversEveryColumn 이 스키마를 직접 물어 그 누락을 막는다.
 */
var (
	seriesKeyCols = []string{"session_id", "hour", "model"}
	seriesSumCols = []string{
		"input", "output", "cache_read", "cache_create",
		"cc_5m", "cc_1h",
		"input_long", "output_long", "cache_read_long",
		"turns", "tool_errors", "stop_max_tokens", "stop_refusal",
		"latency_ms_sum", "latency_turns",
	}
	// 지연 최댓값은 합이 아니다 — 두 버킷 중 큰 쪽이 그 시각의 꼬리다.
	seriesMaxCols = []string{"latency_ms_max"}
	// 라벨 컬럼. 기존 행 값을 유지하되, 비어 있으면 들어오는 값으로 채운다.
	seriesKeepCols = []string{"username", "machine", "project"}
)

// cleanupReport 는 계획(dry-run) 또는 실제 결과다. 둘의 모양이 같아야 "미리 보고 그대로
// 실행한다"가 성립한다.
type cleanupReport struct {
	Applied       bool
	Sessions      int // model 을 NULL 로 되돌린 usage_sessions 행 수
	SeriesRenamed int // 충돌이 없어 라벨만 바뀐 usage_series 행 수
	SeriesMerged  int // 기존 '(미상)' 버킷에 합산 병합된 usage_series 행 수
	Values        map[string]int
}

// Total 은 손대는 전체 행 수다. 0 이면 할 일이 없다(멱등의 관측 지점).
func (r cleanupReport) Total() int { return r.Sessions + r.SeriesRenamed + r.SeriesMerged }

func (r *cleanupReport) sawValue(m string) {
	if r.Values == nil {
		r.Values = map[string]int{}
	}
	r.Values[m]++
}

/*
 * 자리표시자 후보를 좁히는 SQL 전치 조건. 정확한 판정은 Go 정규식이 한다 —
 * `^<[^<>]*>$` 를 SQL 로 옮기면 방언마다 다른 문법(GLOB vs ~)이 되고, 그 순간 판정이 세 벌이
 * 된다. LIKE 는 **상위집합**만 만들면 되므로 방언 차이가 없는 이 형태로 충분하다.
 */
func placeholderCond(col string) string {
	return col + " LIKE '%<%'"
}

/*
 * cleanPlaceholderModels 는 정리를 계획하고(apply=false) 또는 실행한다(apply=true).
 *
 * 실행은 **한 트랜잭션**이다. 병합은 "합치고 지운다" 두 문장이라 중간에서 끊기면 그 시각의
 * 턴 수가 두 배가 되거나 사라진다 — 그건 되돌릴 근거가 남지 않는 종류의 손상이다.
 */
func cleanPlaceholderModels(ctx context.Context, d db.DB, apply bool) (cleanupReport, error) {
	if !apply {
		return planPlaceholderCleanup(ctx, d, false)
	}
	var rep cleanupReport
	err := d.Tx(ctx, func(txCtx context.Context) error {
		r, err := planPlaceholderCleanup(txCtx, d, true)
		rep = r
		return err
	})
	if err != nil {
		return cleanupReport{}, err
	}
	rep.Applied = true
	return rep, nil
}

func planPlaceholderCleanup(ctx context.Context, d db.DB, apply bool) (cleanupReport, error) {
	rep := cleanupReport{}
	if err := cleanSessions(ctx, d, apply, &rep); err != nil {
		return rep, err
	}
	if err := cleanSeries(ctx, d, apply, &rep); err != nil {
		return rep, err
	}
	return rep, nil
}

/*
 * 세션의 대표 모델. 빈 값의 표시 규칙은 이 레포에 이미 한 벌 있다 — 세션은 NULL 이다
 * (store/write.go 의 nullStr, 집계의 COALESCE(NULLIF(model,''),'(미상)')). 그 자리로 합류시킨다.
 */
func cleanSessions(ctx context.Context, d db.DB, apply bool, rep *cleanupReport) error {
	rows, err := d.Query(ctx,
		"SELECT session_id, model FROM usage_sessions WHERE "+placeholderCond("model")+
			" ORDER BY session_id")
	if err != nil {
		return fmt.Errorf("usage_sessions 후보 조회 실패: %w", err)
	}
	for _, r := range rows {
		m := r.Str("model")
		if !isPlaceholderModel(m) {
			continue // LIKE 는 상위집합이다 — 최종 판정은 정규식이 한다
		}
		rep.Sessions++
		rep.sawValue(m)
		if !apply {
			continue
		}
		if err := d.Exec(ctx,
			"UPDATE usage_sessions SET model = NULL WHERE session_id = ? AND model = ?",
			r.Str("session_id"), m); err != nil {
			return fmt.Errorf("usage_sessions 갱신 실패(session=%s): %w", r.Str("session_id"), err)
		}
	}
	return nil
}

/*
 * 시간 버킷. 빈 값의 표시 규칙은 버킷 쪽도 이미 한 벌 있다 — '(미상)'(store.UnknownModel)이다.
 * model 이 NOT NULL PK 라 NULL 로 둘 수 없고, 인테이크도 같은 자리로 접는다(write.go).
 */
func cleanSeries(ctx context.Context, d db.DB, apply bool, rep *cleanupReport) error {
	rows, err := d.Query(ctx,
		"SELECT session_id, hour, model FROM usage_series WHERE "+placeholderCond("model")+
			" ORDER BY session_id, hour, model")
	if err != nil {
		return fmt.Errorf("usage_series 후보 조회 실패: %w", err)
	}
	/*
	 * 한 (세션, 시각)에 자리표시자가 둘 이상일 수 있다(`<synthetic>` 과 `<none>`). 첫 행이
	 * '(미상)' 으로 개명되면 둘째는 그 행과 충돌하므로, "이 실행이 만든 대상"을 기억해 둔다.
	 * dry-run 은 DB 를 안 바꾸므로 이 기억이 없으면 병합/개명 집계가 실제와 갈린다.
	 */
	created := map[string]bool{}
	for _, r := range rows {
		sid, hour, m := r.Str("session_id"), r.Str("hour"), r.Str("model")
		if !isPlaceholderModel(m) {
			continue
		}
		rep.sawValue(m)
		key := sid + "\x00" + hour
		exists := created[key]
		if !exists {
			has, err := seriesExists(ctx, d, sid, hour, store.UnknownModel)
			if err != nil {
				return err
			}
			exists = has
		}
		if !exists {
			rep.SeriesRenamed++
			created[key] = true
			if apply {
				if err := d.Exec(ctx,
					"UPDATE usage_series SET model = ? WHERE session_id = ? AND hour = ? AND model = ?",
					store.UnknownModel, sid, hour, m); err != nil {
					return fmt.Errorf("usage_series 개명 실패(%s/%s/%s): %w", sid, hour, m, err)
				}
			}
			continue
		}
		rep.SeriesMerged++
		created[key] = true
		if apply {
			if err := mergeSeriesRow(ctx, d, sid, hour, m); err != nil {
				return err
			}
		}
	}
	return nil
}

func seriesExists(ctx context.Context, d db.DB, sid, hour, model string) (bool, error) {
	r, err := d.QueryRow(ctx,
		"SELECT 1 c FROM usage_series WHERE session_id = ? AND hour = ? AND model = ?",
		sid, hour, model)
	if err != nil {
		return false, fmt.Errorf("usage_series 조회 실패(%s/%s/%s): %w", sid, hour, model, err)
	}
	return r != nil, nil
}

/*
 * mergeSeriesRow 는 자리표시자 버킷을 같은 시각의 '(미상)' 버킷에 합치고 원본을 지운다.
 *
 * 값을 Go 로 한 번 읽어 바인딩하는 이유: 방언 중립이다. `UPDATE ... FROM` 은 sqlite 와 pg 의
 * 문법이 갈리고, 상관 서브쿼리를 컬럼마다 쓰면 19개가 되어 어느 컬럼이 어떤 규칙인지 눈으로
 * 확인할 수 없게 된다. 규칙이 안 보이는 SQL 은 다음 사람이 컬럼 하나를 조용히 빠뜨린다.
 */
func mergeSeriesRow(ctx context.Context, d db.DB, sid, hour, model string) error {
	cols := append(append(append([]string{}, seriesSumCols...), seriesMaxCols...), seriesKeepCols...)
	src, err := d.QueryRow(ctx,
		"SELECT "+strings.Join(cols, ", ")+" FROM usage_series"+
			" WHERE session_id = ? AND hour = ? AND model = ?", sid, hour, model)
	if err != nil {
		return fmt.Errorf("병합 원본 조회 실패(%s/%s/%s): %w", sid, hour, model, err)
	}
	if src == nil {
		return nil // 그새 사라졌다면 병합할 것이 없다
	}

	sets := make([]string, 0, len(cols))
	args := make([]any, 0, len(cols)+3)
	for _, c := range seriesSumCols {
		sets = append(sets, c+" = "+c+" + ?")
		args = append(args, src.Int(c))
	}
	for _, c := range seriesMaxCols {
		// 방언 중립: GREATEST/MAX(a,b) 는 sqlite·pg 가 갈린다(집계의 CASE 규율과 같다).
		sets = append(sets, c+" = CASE WHEN ? > "+c+" THEN ? ELSE "+c+" END")
		args = append(args, src.Int(c), src.Int(c))
	}
	for _, c := range seriesKeepCols {
		sets = append(sets, c+" = COALESCE("+c+", ?)")
		args = append(args, nullIfEmpty(src.Str(c)))
	}
	args = append(args, sid, hour, store.UnknownModel)

	if err := d.Exec(ctx,
		"UPDATE usage_series SET "+strings.Join(sets, ", ")+
			" WHERE session_id = ? AND hour = ? AND model = ?", args...); err != nil {
		return fmt.Errorf("병합 갱신 실패(%s/%s): %w", sid, hour, err)
	}
	if err := d.Exec(ctx,
		"DELETE FROM usage_series WHERE session_id = ? AND hour = ? AND model = ?",
		sid, hour, model); err != nil {
		return fmt.Errorf("병합 원본 삭제 실패(%s/%s/%s): %w", sid, hour, model, err)
	}
	return nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ── CLI ───────────────────────────────────────────────────────────────

const cleanupUsage = `usage-server cleanup:
  placeholder-models [--apply] [--tenant <t>]
      저장된 자리표시자 모델 라벨(<synthetic> 등)을 정리한다.
      기본은 dry-run — 몇 행이 바뀌는지만 보고하고 아무것도 바꾸지 않는다.
      --apply 를 붙여야 실제로 바꾼다. --tenant 는 pg(멀티테넌트)에서만 의미가 있다.`

/*
 * cleanupCmd — 정리 명령의 진입점.
 *
 * ⚠ 기본값이 dry-run 인 것이 이 명령의 설계다. 되돌리기 어려운 명령의 기본값이 "실행"이면
 *   누군가는 반드시 실수한다. 사람이 --apply 를 치는 그 한 동작이 확인 절차다.
 */
func cleanupCmd(ctx context.Context, d db.DB, out io.Writer, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, cleanupUsage)
		return 2
	}
	switch args[0] {
	case "placeholder-models":
		fs := flag.NewFlagSet("cleanup placeholder-models", flag.ContinueOnError)
		apply := fs.Bool("apply", false, "실제로 바꾼다(기본은 dry-run)")
		tn := fs.String("tenant", "", "테넌트(pg 전용 — 비우면 기본 테넌트)")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if *tn != "" {
			ctx = tenant.With(ctx, *tn)
		}
		// store 표를 직접 만지므로 store 초기화가 선행돼야 한다(sqlite 는 멱등 DDL,
		// pg 는 조기 반환 — 원격 DB 에 아무것도 쓰지 않는다).
		if err := store.Init(ctx, d); err != nil {
			fmt.Fprintf(os.Stderr, "cleanup: store 초기화 실패: %v\n", err)
			return 1
		}
		rep, err := cleanPlaceholderModels(ctx, d, *apply)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cleanup placeholder-models 실패: %v\n", err)
			return 1
		}
		printCleanupReport(out, rep)
		return 0
	default:
		fmt.Fprintln(os.Stderr, cleanupUsage)
		return 2
	}
}

func printCleanupReport(out io.Writer, r cleanupReport) {
	if r.Applied {
		fmt.Fprintln(out, "cleanup placeholder-models (--apply · 실제로 바꿨다)")
	} else {
		fmt.Fprintln(out, "cleanup placeholder-models (dry-run · 아무것도 바꾸지 않았다)")
	}
	if r.Total() == 0 {
		fmt.Fprintln(out, "  · 정리할 행이 없다.")
		return
	}
	fmt.Fprintf(out, "  · usage_sessions : %d행 → model=NULL\n", r.Sessions)
	fmt.Fprintf(out, "  · usage_series   : %d행 → model=%s (개명 %d · 기존 버킷과 합산 병합 %d)\n",
		r.SeriesRenamed+r.SeriesMerged, store.UnknownModel, r.SeriesRenamed, r.SeriesMerged)
	keys := make([]string, 0, len(r.Values))
	for k := range r.Values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s(%d)", k, r.Values[k]))
	}
	fmt.Fprintf(out, "  · 자리표시자 값  : %s\n", strings.Join(parts, " "))
	fmt.Fprintln(out, "  · 토큰 합계는 움직이지 않는다 — 자리표시자 턴의 토큰은 0 이고, 라벨만 바꾼다.")
	if !r.Applied {
		fmt.Fprintln(out, "  · 실제로 바꾸려면 --apply 를 붙여라.")
	}
}
