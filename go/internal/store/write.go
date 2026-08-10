package store

import (
	"context"
	"errors"
	"strings"
)

/*
 * ── 인테이크(쓰기) ────────────────────────────────────────────────────
 *
 * 세 함수 모두 **절대값 UPSERT** 다. 수집기는 트랜스크립트를 다시 읽어 세션 전체의 절대값을
 * 보내므로 PK 로 덮어쓰는 것이 정확하다. 누적(+=)을 쓰면 훅이 두 번 도는 순간 값이 두 배가
 * 되는데, 훅은 실패해도 재시도하는 best-effort 경로라 **중복 전송이 정상 동작에 포함된다.**
 *
 * 트랜스크립트는 append-only 라 한 번 생긴 버킷이 사라지지 않는다 → 덮어쓰기만으로 최신 상태와
 * 일치한다(지워진 버킷을 정리할 필요가 없다).
 */

// ErrEmptySessionID 는 세션 id 가 없는 보고다. 저장할 자리가 없다.
var ErrEmptySessionID = errors.New("store: sessionId 가 비었다")

var sessionCols = []string{
	"machine", "username", "project", "model", "platform",
	"input", "output", "cache_read", "cache_create",
	"web_search", "web_fetch", "turns",
	"started_at", "ended_at", "no_ts_turns",
	"lines_added", "lines_removed", "edits_accepted", "edits_rejected",
	"reported_at",
}

// SessionUpsert 는 세션 사용량 한 건을 덮어쓴다(멱등).
func SessionUpsert(ctx context.Context, s SessionInput) error {
	d, err := conn()
	if err != nil {
		return err
	}
	id := clip(s.SessionID, 120)
	if id == "" {
		return ErrEmptySessionID
	}

	// noTsTurns 는 int() 로 접지 않는다 — 그러면 NULL 이 0 이 되어 "모른다"가 "없다"로 바뀐다.
	var noTs any
	if s.NoTsTurns != nil {
		noTs = nonNeg(*s.NoTsTurns)
	}

	args := []any{
		id,
		nullStr(clip(s.Machine, 200)),
		nullStr(clip(s.Username, 200)),
		nullStr(clip(s.Project, 200)),
		nullStr(clip(s.Model, 120)),
		// platform 은 nullStr 로 감싸지 않는다 — 컬럼이 NOT NULL 이고, 무엇보다 "안 보냈다"의
		// 뜻이 여기서는 NULL 이 아니라 claude 다(NormalizePlatform 이 그 규칙의 단일 출처).
		NormalizePlatform(s.Platform),
		nonNeg(s.Input), nonNeg(s.Output), nonNeg(s.CacheRead), nonNeg(s.CacheCreate),
		nonNeg(s.WebSearch), nonNeg(s.WebFetch), nonNeg(s.Turns),
		nullStr(clip(s.StartedAt, 40)),
		// 구버전 수집기는 안 보낸다 — 그 경우 NULL 로 남아 "모른다"가 화면에 그대로 보인다.
		nullStr(clip(s.EndedAt, 40)),
		noTs,
		nonNeg(s.LinesAdded), nonNeg(s.LinesRemoved), nonNeg(s.EditsAccepted), nonNeg(s.EditsRejected),
		nowISO(),
	}
	return d.Exec(ctx, upsertSQL(
		"usage_sessions", "session_id", sessionCols,
		conflictTarget(d, "(session_id)", "(tenant_id, session_id)"),
	), args...)
}

var seriesCols = []string{
	"input", "output", "cache_read", "cache_create",
	"cc_5m", "cc_1h", "turns",
	"tool_errors", "stop_max_tokens", "stop_refusal",
	"latency_ms_sum", "latency_ms_max", "latency_turns",
	"username", "machine", "project",
}

// SeriesUpsert 는 시간 × 모델 버킷을 일괄 덮어쓴다.
func SeriesUpsert(ctx context.Context, in SeriesInput) error {
	_, err := SeriesUpsertN(ctx, in)
	return err
}

// SeriesUpsertN 은 SeriesUpsert 와 같되 실제로 저장된 버킷 수를 돌려준다
// (인테이크 응답이 그 수를 싣는다).
//
// **한 행이 깨져도 나머지는 넣는다.** 시계열이 전부-아니면-전무일 이유가 없다.
func SeriesUpsertN(ctx context.Context, in SeriesInput) (int, error) {
	d, err := conn()
	if err != nil {
		return 0, err
	}
	sid := clip(in.SessionID, 120)
	if sid == "" {
		return 0, ErrEmptySessionID
	}

	sql := upsertSQL(
		"usage_series", "session_id,hour,model", seriesCols,
		conflictTarget(d, "(session_id, hour, model)", "(tenant_id, session_id, hour, model)"),
	)

	user := nullStr(clip(in.Username, 200))
	mach := nullStr(clip(in.Machine, 200))
	proj := nullStr(clip(in.Project, 200))

	rows := in.Rows
	if len(rows) > MaxSeriesPerSession {
		rows = rows[:MaxSeriesPerSession]
	}

	n := 0
	for _, r := range rows {
		hour := clip(r.Hour, 13)
		if !hourRe.MatchString(hour) {
			continue // 시간 라벨이 아니면 시계열에 올릴 자리가 없다
		}
		model := clip(r.Model, 120)
		if model == "" {
			model = UnknownModel
		}
		args := []any{
			sid, hour, model,
			nonNeg(r.Input), nonNeg(r.Output), nonNeg(r.CacheRead), nonNeg(r.CacheCreate),
			nonNeg(r.CC5m), nonNeg(r.CC1h), nonNeg(r.Turns),
			nonNeg(r.ToolErrors), nonNeg(r.StopMaxTokens), nonNeg(r.StopRefusal),
			nonNeg(r.LatencyMsSum), nonNeg(r.LatencyMsMax), nonNeg(r.LatencyTurns),
			user, mach, proj,
		}
		if err := d.Exec(ctx, sql, args...); err != nil {
			continue // 한 행 실패가 나머지를 막지 않는다
		}
		n++
	}
	return n, nil
}

// CountersUpsert 는 축별 카운터를 일괄 덮어쓴다.
func CountersUpsert(ctx context.Context, in CountersInput) error {
	_, err := CountersUpsertN(ctx, in)
	return err
}

// CountersUpsertN 은 CountersUpsert 와 같되 저장된 행 수를 돌려준다.
//
// 축·키·개수를 여기서 **최종적으로** 좁힌다 — 클라이언트 검증을 신뢰하지 않는다.
// 한 행이 깨져도 나머지는 넣는다(텔레메트리가 전부-아니면-전무일 이유가 없다).
func CountersUpsertN(ctx context.Context, in CountersInput) (int, error) {
	d, err := conn()
	if err != nil {
		return 0, err
	}
	sid := clip(in.SessionID, 120)
	if sid == "" {
		return 0, ErrEmptySessionID
	}
	day := dayOf(in.StartedAt)
	user := nullStr(clip(in.Username, 200))
	mach := nullStr(clip(in.Machine, 200))

	sql := "INSERT INTO usage_counters(session_id,kind,key,count,day,username,machine)" +
		" VALUES(?,?,?,?,?,?,?)" +
		" ON CONFLICT " + conflictTarget(d, "(session_id, kind, key)", "(tenant_id, session_id, kind, key)") +
		" DO UPDATE SET count=excluded.count, day=excluded.day," +
		" username=excluded.username, machine=excluded.machine"

	rows := in.Rows
	if len(rows) > MaxCountersPerSession {
		rows = rows[:MaxCountersPerSession]
	}

	n := 0
	for _, r := range rows {
		if !isCounterKind(r.Kind) {
			continue
		}
		key := strings.TrimSpace(clip(r.Key, keyMax))
		count := nonNeg(r.Count)
		if key == "" || count == 0 {
			continue
		}
		if err := d.Exec(ctx, sql, sid, r.Kind, key, count, day, user, mach); err != nil {
			continue // 행 단위 best-effort
		}
		n++
	}
	return n, nil
}

/*
 * CounterBump — 서버측 카운터 원자적 +1.
 *
 * 클라이언트 보고(CountersUpsert)는 세션 절대값을 **덮어쓰지만**, 서버가 직접 세는 축(mcp)은
 * 이벤트마다 하나씩 늘어난다. 읽고-고쳐-쓰면 동시 요청에서 카운트가 샌다 — 한 문장 UPSERT 로
 * DB 가 더하게 한다.
 *
 * 세션 자리에 날짜를 넣어 일별 누적 행 하나로 유지한다(PK 가 (세션, 축, 키)라 세션 없이는 못 넣는다).
 */
func CounterBump(ctx context.Context, in CounterBumpInput) error {
	d, err := conn()
	if err != nil {
		return err
	}
	if !isCounterKind(in.Kind) {
		return errors.New("store: 모르는 축 " + in.Kind)
	}
	k := strings.TrimSpace(clip(in.Key, keyMax))
	if k == "" {
		return errors.New("store: 키가 비었다")
	}
	day := dayOf(in.Day)
	sid := "server-" + day

	sql := "INSERT INTO usage_counters(session_id,kind,key,count,day,username,machine) VALUES(?,?,?,1,?,?,?)" +
		" ON CONFLICT " + conflictTarget(d, "(session_id, kind, key)", "(tenant_id, session_id, kind, key)") +
		" DO UPDATE SET count = usage_counters.count + 1"
	return d.Exec(ctx, sql, sid, in.Kind, k, day,
		nullStr(clip(in.Username, 200)), nullStr(clip(in.Machine, 200)))
}

// RecommendationAdd 는 추천 관측 1건을 남긴다.
// 목표 **원문은 받지 않는다** — 고객사명·경로·비밀이 목표 문장에 섞여 들어오기 때문이다.
func RecommendationAdd(ctx context.Context, in RecoInput) error {
	d, err := conn()
	if err != nil {
		return err
	}
	return d.Exec(ctx,
		"INSERT INTO usage_recommendations(goal_tokens,agent,skills,score,source,username,at)"+
			" VALUES(?,?,?,?,?,?,?)",
		nullStr(clip(strings.Join(in.GoalTokens, " "), 600)),
		nullStr(clip(in.Agent, 200)),
		nullStr(clip(strings.Join(in.Skills, ","), 400)),
		in.Score,
		nullStr(clip(in.Source, 40)),
		nullStr(clip(in.Username, 200)),
		nowISO(),
	)
}

/*
 * upsertSQL 은 `INSERT … VALUES(…) ON CONFLICT … DO UPDATE SET c=excluded.c` 를 만든다.
 *
 * 컬럼 목록에서 자리표시자 수와 SET 절을 함께 뽑기 때문에 셋이 어긋날 수 없다. 손으로 쓰면
 * 컬럼을 하나 추가할 때 물음표 하나를 빠뜨리는 날이 오고, 그때 바인딩이 한 칸씩 밀려
 * **오류 없이 틀린 값**이 저장된다.
 *
 * keyCols 는 쉼표로 이어 붙인 PK 컬럼들이고, 값은 args 앞쪽에 그 순서로 온다.
 */
func upsertSQL(table, keyCols string, cols []string, conflict string) string {
	nKeys := strings.Count(keyCols, ",") + 1
	marks := make([]string, nKeys+len(cols))
	for i := range marks {
		marks[i] = "?"
	}
	sets := make([]string, len(cols))
	for i, c := range cols {
		sets[i] = c + "=excluded." + c
	}
	return "INSERT INTO " + table + "(" + keyCols + "," + strings.Join(cols, ",") + ")" +
		" VALUES(" + strings.Join(marks, ",") + ")" +
		" ON CONFLICT " + conflict + " DO UPDATE SET " + strings.Join(sets, ",")
}
