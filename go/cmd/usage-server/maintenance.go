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
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/tscorp/user-usage/internal/db"
	"github.com/tscorp/user-usage/internal/identity"
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
		// 고속 모드 분리분도 **합**이다(총량의 부분집합이므로 병합 시 그대로 더해진다).
		"input_fast", "output_fast", "cache_read_fast", "cache_create_fast",
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

/*
 * ── 수집된 사용량 행 삭제 ─────────────────────────────────────────────
 *
 * 무엇을 하는가: 사람이 **지목한** 사용자(또는 머신)의 사용량 행을 지운다. 퇴사·삭제 요청에
 * 답하는 수단이다.
 *
 * 왜 필요한가: 계정을 지워도(사용자 관리 API) 그 사람의 usage_sessions·usage_series·
 * usage_counters 행은 남고, 화면에 계정명·머신명·프로젝트명이 계속 보인다. README 가 이 상황을
 * 예상해 두었다 — *"지워야 할 사정이 생기면 보존 정리기를 늘리는 것이 아니라 (a) 귀속 교정으로
 * 이름을 바꾸거나 (b) 해당 행을 직접 지우는 쪽이 맞습니다."* 이것이 (b)다.
 *
 * ⚠ **보존 정리기가 아니다.** 기한으로 자동 삭제하는 경로를 만들지 않는다 — 사람이 대상을
 *   지목해 한 번 돌리는 명령이고, 부팅·store.Init 어디에도 걸려 있지 않다. 기본값은 dry-run 이다.
 *
 * ⚠ **되돌릴 수 없다.** placeholder-models 는 라벨만 바꿨지만 이것은 행을 지운다. 복구 수단은
 *   스냅샷/PITR 뿐이다(docs/OPERATIONS.md §8-9). 그래서 화면에 그 사실을 **먼저** 찍는다.
 *
 * pg 는 테넌트당 한 번이다(--tenant) — RLS 가 한 번에 한 테넌트만 보여 준다.
 */

// purgeScope 는 삭제 대상을 지목하는 축이다. 정확히 하나만 쓴다.
type purgeScope string

const (
	purgeByUser    purgeScope = "user"
	purgeByMachine purgeScope = "machine"
)

// column 은 이 축이 보는 귀속 컬럼이다. 두 축의 SQL 이 이 한 값만으로 갈린다.
func (s purgeScope) column() string {
	if s == purgeByMachine {
		return "machine"
	}
	return "username"
}

// label 은 화면에 찍는 대상 표기다(`username=amy` · `machine=pc-1`).
func (s purgeScope) label(target string) string { return s.column() + "=" + target }

/*
 * purgeSel 은 표 하나를 어떻게 좁히는가다. **이 목록이 "무엇을 지우는가"의 단일 출처다** —
 * 화면 문구도, 스키마 드리프트 테스트도 여기를 읽는다.
 *
 * 조건이 둘로 갈리는 이유:
 *
 *	bySession — 대상 세션(usage_sessions)에 딸린 행. 자식 표의 귀속 컬럼이 **낡아 있어도**
 *	            빠뜨리지 않는다. 실재하는 드리프트다: identity.Restamp 는 usage_sessions 와
 *	            usage_counters 만 재스탬프하고 usage_series 는 건드리지 않으므로, 세션은 새
 *	            이름인데 그 세션의 버킷은 옛 OS 계정명을 지닌 행이 남는다. 이름으로만 좁히면
 *	            그 버킷이 살아남아 "지웠는데 화면에 옛 이름이 있다"가 된다.
 *	byName    — 그 표의 귀속 컬럼이 대상과 같은 행. 자식 표에서는 **세션 행이 없는 고아로
 *	            한정한다**(NOT EXISTS). 고아는 실재한다 — 인테이크가 세션 행만 실패하고 버킷은
 *	            들어가는 자리다(store/aggregate.go 의 ① 주석). 고아로 한정하는 것이 계약이다:
 *	            한정하지 않으면 이름만 낡은 행 하나 때문에 **다른 사람의 살아 있는 세션**에서
 *	            버킷을 뽑아 가게 된다.
 *
 * 두 조건은 서로 배타적이다(bySession 은 세션이 있어야 하고, 자식의 byName 은 없어야 한다) —
 * 그래서 두 수를 더한 것이 곧 그 표에서 지워지는 행 수다.
 */
type purgeSel struct {
	table     string
	bySession string
	byName    string
	// skipped 가 비어 있지 않으면 이 표는 이 축으로 좁힐 수 없다. **조용히 빼지 않는다** —
	// 0행과 "좁힐 수 없다"는 다른 사실이고, 그 차이가 화면에 보여야 사람이 다음 수를 안다.
	skipped string
}

/*
 * purgeKeepTables 는 귀속 컬럼을 가졌지만 이 명령이 **일부러 남기는** 표다.
 *
 * 근거: 이 표들은 수집된 관측치가 아니라 **계정·자격**이고, 그 회수는 사용자 관리 API 가
 * 소유한다(docs/OPERATIONS.md §9 — 삭제가 세션과 인제스트 키를 함께 거둔다). 사용량 행을 지우는
 * 명령이 계정까지 지우면 같은 표에 소유자가 둘이 되고, 그때는 어느 쪽이 지웠는지 알 수 없다.
 *
 * usage_audit(감사 로그)도 남긴다. 귀속 컬럼이 없어 아래 목록에는 없지만 판단은 같은 자리다 —
 * 이 레포는 그 표를 "어제 보던 이름이 왜 오늘 다른가에 답하는 표"라며 기한을 두지 않기로 했고
 * (identity/audit.go ③), 삭제의 부수효과로 그 근거를 함께 지우면 방금 지운 이유를 나중에
 * 아무도 답할 수 없다.
 */
var purgeKeepTables = []string{
	"auth_users", "auth_sessions", "member_tokens", "team_members", "ingest_keys",
}

// purgeKeepNote 는 위 판단을 화면 한 줄로 옮긴 것이다(문구와 목록이 갈리지 않게 목록에서 만든다).
func purgeKeepNote() string {
	return "남긴다: usage_audit(감사 로그 — 기한 없음) · " +
		strings.Join(purgeKeepTables, "·") + "(계정·자격 — 사용자 관리 API 가 소유한다)"
}

/*
 * purgeSelections 는 이 축의 삭제 계획이다. **순서가 계약이다** — usage_sessions 가 맨 끝이다.
 * 위 표들의 bySession 조건이 세션 행을 근거로 삼으므로, 세션을 먼저 지우면 자식 행이 통째로
 * 남아 "세션은 없는데 버킷이 있는" 상태가 된다(화면에서 진단이 거의 불가능한 상태다).
 */
func purgeSelections(scope purgeScope) []purgeSel {
	col := scope.column()
	owned := "session_id IN (SELECT session_id FROM usage_sessions WHERE " + col + " = ?)"
	orphan := func(table string) string {
		return col + " = ? AND NOT EXISTS (SELECT 1 FROM usage_sessions s" +
			" WHERE s.session_id = " + table + ".session_id)"
	}
	sels := []purgeSel{
		{table: "usage_series", bySession: owned, byName: orphan("usage_series")},
		{table: "usage_counters", bySession: owned, byName: orphan("usage_counters")},
	}
	/*
	 * usage_recommendations 는 세션도 머신도 없다 — username 하나뿐이다. 머신 축에서는 좁힐
	 * 방법이 아예 없으므로 그 사실을 밝히고 건너뛴다. 세션으로 되짚을 수도 없다(session_id 가
	 * 없는 표다).
	 */
	if scope == purgeByUser {
		sels = append(sels, purgeSel{table: "usage_recommendations", byName: "username = ?"})
	} else {
		sels = append(sels, purgeSel{table: "usage_recommendations",
			skipped: "session_id·machine 컬럼이 없다 → 머신으로 좁힐 수 없다(계정으로 지워라)"})
	}
	/*
	 * machine_identity 는 지운다. 그 행의 내용이 곧 우리가 지우려는 것이기 때문이다 —
	 * 머신명 + 계정명 한 쌍이고, 관리 화면의 매핑 목록에 그대로 보인다. 남기면 사용량은 지워졌는데
	 * 매핑 목록에 그 사람 이름이 계속 뜬다. 그리고 이 표의 기능은 **앞으로 들어올** 보고를
	 * 귀속시키는 것인데, 지목 대상에게는 앞으로의 보고가 없다.
	 *
	 * 되돌리기 비용도 다르다 — 관리자가 화면에서 한 줄 다시 걸면 복구된다(사용량 행과 달리).
	 */
	sels = append(sels, purgeSel{table: "machine_identity", byName: col + " = ?"})
	// 세션은 **마지막**이다(위 주석).
	sels = append(sels, purgeSel{table: "usage_sessions", byName: col + " = ?"})
	return sels
}

// purgeTable 은 표 하나의 계획 또는 결과다. 계획과 결과의 모양이 같아야 "미리 보고 그대로
// 실행한다"가 성립한다(placeholder-models 의 cleanupReport 와 같은 규율).
type purgeTable struct {
	Table       string
	FromSession int    // 대상 세션에 딸려 지워지는 행
	ByName      int    // 귀속 컬럼이 대상과 같은 행(자식 표에서는 고아만)
	Skipped     string // 비어 있지 않으면 이 축으로 좁힐 수 없다
}

func (t purgeTable) Total() int { return t.FromSession + t.ByName }

type purgeReport struct {
	Applied bool
	Scope   purgeScope
	Target  string
	Tables  []purgeTable
}

// Total 은 지워지는(또는 지운) 전체 행 수다. 0 이면 할 일이 없다(멱등의 관측 지점).
func (r purgeReport) Total() int {
	n := 0
	for _, t := range r.Tables {
		n += t.Total()
	}
	return n
}

// purgeOptions 는 삭제 입력이다.
type purgeOptions struct {
	Scope  purgeScope
	Target string
	Apply  bool
	/*
	 * faultBeforeDelete 는 **테스트 전용 결함 주입 자리**다(nil 이면 실 동작).
	 *
	 * 표 다섯을 지우는 도중의 실패를 자연스럽게 만들 수단이 없는데, "중간에 끊겨도 반쪽이
	 * 남지 않는다"는 이 명령의 핵심 성질이라 검증 없이 두면 사고가 났을 때만 관측된다.
	 * 그래서 주입 자리를 하나 열어 두고 maintenance_test.go 가 롤백을 행동으로 못 박는다.
	 *
	 * 넘기는 ctx 는 **트랜잭션 컨텍스트**다 — 테스트가 그것으로 질의해 "끊기는 시점에 앞의
	 * 표는 이미 지워져 있었다"를 확인한다. 그 확인이 없으면 삭제가 아예 안 일어나도 롤백
	 * 테스트가 통과해 버린다(공허한 초록불).
	 */
	faultBeforeDelete func(ctx context.Context, table string) error
}

// errPurgeTargetRequired — 대상 없는 삭제는 거부다. 빈 값을 통과시키면 그 순간 "전부 삭제"가 된다.
var errPurgeTargetRequired = errors.New("cleanup usage-rows: 대상이 필요하다")

/*
 * purgeUsageRows 는 삭제를 계획하고(Apply=false) 또는 실행한다(Apply=true).
 *
 * 실행은 **한 트랜잭션**이다. 표 다섯을 지우다 중간에 끊기면 세션은 없는데 카운터가 남는
 * 상태가 되고, 그 상태는 화면에서 진단이 거의 불가능하다 — 고아 버킷은 모델 축 집계에서
 * 아예 빠지므로(store/aggregate.go 의 ①) 숫자만 보고는 알아챌 수 없다.
 */
func purgeUsageRows(ctx context.Context, d db.DB, opt purgeOptions) (purgeReport, error) {
	if strings.TrimSpace(opt.Target) == "" {
		return purgeReport{}, errPurgeTargetRequired
	}
	if !opt.Apply {
		return planPurge(ctx, d, opt)
	}
	var rep purgeReport
	err := d.Tx(ctx, func(txCtx context.Context) error {
		r, err := planPurge(txCtx, d, opt)
		rep = r
		return err
	})
	if err != nil {
		return purgeReport{}, err
	}
	rep.Applied = true
	return rep, nil
}

func planPurge(ctx context.Context, d db.DB, opt purgeOptions) (purgeReport, error) {
	rep := purgeReport{Scope: opt.Scope, Target: opt.Target}
	for _, sel := range purgeSelections(opt.Scope) {
		row := purgeTable{Table: sel.table, Skipped: sel.skipped}
		if sel.skipped != "" {
			rep.Tables = append(rep.Tables, row)
			continue
		}
		if opt.Apply && opt.faultBeforeDelete != nil {
			if err := opt.faultBeforeDelete(ctx, sel.table); err != nil {
				return rep, err
			}
		}
		for _, part := range []struct {
			where string
			dst   *int
		}{{sel.bySession, &row.FromSession}, {sel.byName, &row.ByName}} {
			if part.where == "" {
				continue
			}
			/*
			 * 세고 나서 지운다. DB 인터페이스의 Exec 은 영향 행 수를 돌려주지 않으므로
			 * (동결 계약이다) 같은 트랜잭션 안에서 같은 WHERE 로 먼저 센다 — 트랜잭션 밖에서
			 * 세면 그 사이 인테이크가 행을 더해 보고한 수와 지운 수가 어긋난다.
			 * identity.Restamp 가 같은 이유로 같은 모양을 쓴다.
			 */
			n, err := countWhere(ctx, d, sel.table, part.where, opt.Target)
			if err != nil {
				return rep, err
			}
			*part.dst = n
			if !opt.Apply || n == 0 {
				continue
			}
			if err := d.Exec(ctx,
				"DELETE FROM "+sel.table+" WHERE "+part.where, opt.Target); err != nil {
				return rep, fmt.Errorf("%s 삭제 실패: %w", sel.table, err)
			}
		}
		rep.Tables = append(rep.Tables, row)
	}
	return rep, nil
}

func countWhere(ctx context.Context, d db.DB, table, where string, args ...any) (int, error) {
	r, err := d.QueryRow(ctx, "SELECT COUNT(*) c FROM "+table+" WHERE "+where, args...)
	if err != nil {
		return 0, fmt.Errorf("%s 행 수 조회 실패: %w", table, err)
	}
	if r == nil {
		return 0, nil
	}
	return int(r.Int("c")), nil
}

// purgeColWidth 는 표 이름 칸의 표시 폭이다(가장 긴 이름 usage_recommendations 가 22자).
const purgeColWidth = 22

/*
 * padCol 은 표 이름 칸을 채운다. **표시 폭**으로 센다 — `%-22s` 는 바이트를 세므로 한글 라벨
 * ("합계")만 오른쪽으로 밀려 숫자 열이 어긋난다. 어긋난 열은 사람이 표별 행 수를 눈으로
 * 대조하지 못하게 만들고, 이 명령의 dry-run 은 그 대조가 전부다.
 *
 * 폭 판정은 이 명령이 실제로 찍는 라벨(ASCII 표 이름 + 한글 낱말)만 덮는다 — 결합 문자나
 * 이모지 같은 일반 케이스를 다루려는 것이 아니다.
 */
func padCol(s string, width int) string {
	w := 0
	for _, r := range s {
		if unicode.Is(unicode.Hangul, r) || unicode.Is(unicode.Han, r) {
			w += 2
			continue
		}
		w++
	}
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

func printPurgeReport(out io.Writer, r purgeReport) {
	if r.Applied {
		fmt.Fprintln(out, "cleanup usage-rows (--apply · 실제로 지웠다)")
	} else {
		fmt.Fprintln(out, "cleanup usage-rows (dry-run · 아무것도 지우지 않았다)")
	}
	// **되돌릴 수 없음을 먼저 말한다.** 계획을 읽기 전에 알아야 하는 사실이다.
	fmt.Fprintln(out, "  ⚠ 이 명령은 행을 지운다 — 되돌릴 수 없다. 복구는 스냅샷/PITR 뿐이다.")
	fmt.Fprintf(out, "  · 대상: %s\n", r.Scope.label(r.Target))
	if r.Total() == 0 {
		fmt.Fprintln(out, "  · 지울 행이 없다.")
		return
	}
	for _, t := range r.Tables {
		if t.Skipped != "" {
			fmt.Fprintf(out, "  · %s: 건너뜀 — %s\n", padCol(t.Table, purgeColWidth), t.Skipped)
			continue
		}
		detail := ""
		// 내역은 두 몫이 **함께** 있을 때만 붙인다. 한쪽만 있으면 그 수가 곧 전부다.
		if t.FromSession > 0 && t.ByName > 0 {
			detail = fmt.Sprintf(" (세션 소유 %d · 고아 잔여 %d)", t.FromSession, t.ByName)
		}
		fmt.Fprintf(out, "  · %s: %d행%s\n", padCol(t.Table, purgeColWidth), t.Total(), detail)
	}
	fmt.Fprintf(out, "  · %s: %d행\n", padCol("합계", purgeColWidth), r.Total())
	fmt.Fprintf(out, "  · %s\n", purgeKeepNote())
	if !r.Applied {
		fmt.Fprintln(out, "  · 실제로 지우려면 --apply 를 붙여라.")
	}
}

// ── CLI ───────────────────────────────────────────────────────────────

const cleanupUsage = `usage-server cleanup:
  placeholder-models [--apply] [--tenant <t>]
      저장된 자리표시자 모델 라벨(<synthetic> 등)을 정리한다.
      기본은 dry-run — 몇 행이 바뀌는지만 보고하고 아무것도 바꾸지 않는다.
      --apply 를 붙여야 실제로 바꾼다. --tenant 는 pg(멀티테넌트)에서만 의미가 있다.

  usage-rows (--user <u> | --machine <m>) [--apply] [--tenant <t>]
      지목한 사용자(또는 머신)의 **수집된 사용량 행을 지운다** — usage_sessions ·
      usage_series · usage_counters · usage_recommendations · machine_identity.
      퇴사·삭제 요청에 답하는 수단이고, 기한으로 자동 삭제하는 보존 정리기가 아니다.
      기본은 dry-run — 표별로 몇 행이 지워지는지만 보고하고 아무것도 지우지 않는다.
      ⚠ --apply 는 되돌릴 수 없다(복구는 스냅샷/PITR 뿐).
      감사 로그(usage_audit)와 계정·자격 표는 건드리지 않는다 — 계정·인제스트 키 회수는
      사용자 관리 API 가 소유한다. --tenant 는 pg 에서만 의미가 있다(테넌트당 한 번).`

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
	case "usage-rows":
		fs := flag.NewFlagSet("cleanup usage-rows", flag.ContinueOnError)
		user := fs.String("user", "", "사용량을 지울 사용자명")
		machine := fs.String("machine", "", "사용량을 지울 머신명")
		apply := fs.Bool("apply", false, "실제로 지운다(기본은 dry-run · 되돌릴 수 없다)")
		tn := fs.String("tenant", "", "테넌트(pg 전용 — 비우면 기본 테넌트)")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		/*
		 * 대상은 **정확히 하나**다.
		 *
		 * 없으면 거부한다 — 빈 대상을 통과시키면 그 순간 "전부 삭제"가 되고, 그것이 기본 동작인
		 * 명령은 언젠가 반드시 사고를 낸다. 공백만 있는 값도 여기서 걸린다(identity.Set 이
		 * 빈 username 을 거부하는 것과 같은 규율).
		 * 둘을 동시에 주는 것도 거부한다 — 무엇을 기준으로 지웠는지가 모호해지고, 사람은 보통
		 * 둘 중 하나를 잘못 적은 것이다.
		 */
		u, m := strings.TrimSpace(*user), strings.TrimSpace(*machine)
		if (u == "") == (m == "") {
			fmt.Fprintln(os.Stderr,
				"cleanup usage-rows: --user 또는 --machine 중 **하나**가 필요하다(둘 다는 안 된다)")
			fmt.Fprintln(os.Stderr, cleanupUsage)
			return 2
		}
		scope, target := purgeByUser, u
		if m != "" {
			scope, target = purgeByMachine, m
		}
		if *tn != "" {
			ctx = tenant.With(ctx, *tn)
		}
		// store 표와 machine_identity 를 직접 만지므로 두 스키마가 선행돼야 한다
		// (sqlite 는 멱등 DDL, pg 는 조기 반환 — 원격 DB 에 아무것도 쓰지 않는다).
		if err := store.Init(ctx, d); err != nil {
			fmt.Fprintf(os.Stderr, "cleanup: store 초기화 실패: %v\n", err)
			return 1
		}
		if err := identity.Init(ctx, d); err != nil {
			fmt.Fprintf(os.Stderr, "cleanup: identity 초기화 실패: %v\n", err)
			return 1
		}
		rep, err := purgeUsageRows(ctx, d, purgeOptions{Scope: scope, Target: target, Apply: *apply})
		if err != nil {
			fmt.Fprintf(os.Stderr, "cleanup usage-rows 실패: %v\n", err)
			return 1
		}
		printPurgeReport(out, rep)
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
