package httpapi

/*
 * 계단(롱컨텍스트)의 HTTP 표면 — **비용을 계산하는 모든 경로**가 계단을 타는가.
 *
 * 이 파일의 존재 이유는 하나다: 한 경로라도 계단을 안 타면 같은 데이터의 비용이 화면마다
 * 달라지고, 그 사실이 어디에도 표시되지 않는다. 그래서 비용을 내는 지점을 전수로 건다.
 *
 *	세션 행    → sessionUsage        (분포 · 리더보드 · 좌석 · 세션 상세 · 일별 시계열)
 *	시간 버킷  → bucketUsage         (시간 시계열 · 세션 상세의 정확 계산)
 *	플랫폼 롤업 → platformModelUsage  (/api/usage/platforms)
 *
 * 세 변환 함수가 cost.Usage 를 만드는 **유일한 자리**이므로(패키지 전체 grep 기준),
 * 셋을 다 걸면 비용 경로가 다 걸린다.
 */

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/tscorp/user-usage/internal/cost"
	"github.com/tscorp/user-usage/internal/store"
	"github.com/tscorp/user-usage/internal/tenant"
)

// ① 세 변환 함수가 롱 몫을 **하나도 빠뜨리지 않고** 올린다.
//
// 값을 서로 다르게 준다 — 같은 값으로 두면 필드가 뒤바뀐 배선(InputLong 을 OutputLong 자리에)이
// 통과한다.
func TestLong_AllUsageConvertersCarryLongShare(t *testing.T) {
	want := cost.Usage{InputLong: 11, OutputLong: 22, CacheReadLong: 33}

	got := []struct {
		name string
		u    cost.Usage
	}{
		{"sessionUsage", sessionUsage(store.Session{
			Input: 100, Output: 100, CacheRead: 100,
			InputLong: 11, OutputLong: 22, CacheReadLong: 33,
		})},
		{"bucketUsage", bucketUsage(store.Bucket{
			Input: 100, Output: 100, CacheRead: 100,
			InputLong: 11, OutputLong: 22, CacheReadLong: 33,
		})},
		{"platformModelUsage", platformModelUsage(store.PlatformModelRow{
			Input: 100, Output: 100, CacheRead: 100,
			InputLong: 11, OutputLong: 22, CacheReadLong: 33,
		})},
	}
	for _, c := range got {
		if c.u.InputLong != want.InputLong || c.u.OutputLong != want.OutputLong ||
			c.u.CacheReadLong != want.CacheReadLong {
			t.Fatalf("%s 가 롱 몫을 떨어뜨렸다: in=%v out=%v cr=%v",
				c.name, c.u.InputLong, c.u.OutputLong, c.u.CacheReadLong)
		}
		// 총량은 의미 불변이다 — 롱 몫을 빼서 넘기면 안 된다(cost 가 스스로 뺀다).
		if c.u.Input != 100 || c.u.Output != 100 || c.u.CacheRead != 100 {
			t.Fatalf("%s 가 총량을 깎아 넘겼다: %+v", c.name, c.u)
		}
	}
}

// ② 인테이크가 세 필드를 저장까지 배선한다(세션·버킷 양쪽). 없으면 0 이다.
func TestLong_IntakeWiresLongShareToStore(t *testing.T) {
	openDB(t)
	h := New(testCfg(false))

	body := `{"sessions":[
		{"id":"longctx-0001","model":"gemini-2.5-pro","startedAt":"2026-08-10T09:00:00.000Z",
		 "input":400000,"output":40000,"cacheRead":200000,
		 "inputLong":300000,"outputLong":30000,"cacheReadLong":150000,
		 "series":[{"hour":"2026-08-10T09","model":"gemini-2.5-pro",
			"input":400000,"output":40000,"cacheRead":200000,
			"inputLong":300000,"outputLong":30000,"cacheReadLong":150000}]},
		{"id":"longctx-0002","model":"gemini-2.5-pro","startedAt":"2026-08-10T10:00:00.000Z",
		 "input":400000,"output":40000,"cacheRead":200000}
	]}`
	rec := do(t, h, http.MethodPost, "/api/usage", body, withIntake)
	if rec.Code != http.StatusOK {
		t.Fatalf("인테이크: %d %s", rec.Code, rec.Body.String())
	}

	ctx := tenant.With(t.Context(), "default")
	rows, err := store.SessionRows(ctx, store.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]store.Session{}
	for _, r := range rows {
		byID[r.SessionID] = r
	}
	if s := byID["longctx-0001"]; s.InputLong != 300000 || s.OutputLong != 30000 || s.CacheReadLong != 150000 {
		t.Fatalf("세션 롱 몫이 저장까지 안 갔다: %+v", s)
	}
	// 안 보낸 세션은 0 — 구버전 수집기의 보고가 그대로 동작한다.
	if s := byID["longctx-0002"]; s.InputLong != 0 || s.OutputLong != 0 || s.CacheReadLong != 0 {
		t.Fatalf("안 보낸 세션에 롱 몫이 생겼다: %+v", s)
	}

	buckets, err := store.SeriesOf(ctx, "longctx-0001")
	if err != nil || len(buckets) != 1 {
		t.Fatalf("버킷: %d개 / %v", len(buckets), err)
	}
	if b := buckets[0]; b.InputLong != 300000 || b.OutputLong != 30000 || b.CacheReadLong != 150000 {
		t.Fatalf("버킷 롱 몫이 저장까지 안 갔다: %+v", b)
	}
}

/*
 * ③ 실제 엔드포인트의 비용이 계단을 탄다.
 *
 * 같은 토큰 총량을 가진 두 세션(하나는 롱 분리분 있음, 하나는 없음)을 넣고, 각 화면이 내는
 * 비용이 **다른지**를 본다. 안 다르면 그 경로가 계단을 안 타는 것이다.
 *
 * 절대값이 아니라 "계단 있는 쪽이 더 비싸다"로 판정하는 이유: 단가가 바뀌면 절대값은 바뀌지만
 * 이 관계는 안 바뀐다. 절대값 검산은 cost 패키지(longcontext_test.go)가 손으로 한다.
 */
func TestLong_CostEndpointsApplyTheStep(t *testing.T) {
	openDB(t)
	h := New(testCfg(false))
	ctx := tenant.With(t.Context(), "default")

	// alice: 전부 롱 구간 · bob: 전부 표준 구간. 토큰 총량은 같다.
	for _, in := range []store.SessionInput{
		{SessionID: "lc-alice", Username: "alice", Machine: "m1", Model: "gemini-2.5-pro",
			Platform: "gemini", StartedAt: "2026-08-10T09:00:00.000Z", Turns: 1,
			Input: 400000, Output: 40000, CacheRead: 200000,
			InputLong: 400000, OutputLong: 40000, CacheReadLong: 200000},
		{SessionID: "lc-bob", Username: "bob", Machine: "m2", Model: "gemini-2.5-pro",
			Platform: "codex", StartedAt: "2026-08-10T09:00:00.000Z", Turns: 1,
			Input: 400000, Output: 40000, CacheRead: 200000},
	} {
		if err := store.SessionUpsert(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	// 시간 버킷도 같은 모양으로 — 시간 뷰는 별도 표를 읽는다.
	for _, in := range []store.SeriesInput{
		{SessionID: "lc-alice", Username: "alice", Machine: "m1", Rows: []store.SeriesRow{{
			Hour: "2026-08-10T09", Model: "gemini-2.5-pro",
			Input: 400000, Output: 40000, CacheRead: 200000,
			InputLong: 400000, OutputLong: 40000, CacheReadLong: 200000}}},
		{SessionID: "lc-bob", Username: "bob", Machine: "m2", Rows: []store.SeriesRow{{
			Hour: "2026-08-10T09", Model: "gemini-2.5-pro",
			Input: 400000, Output: 40000, CacheRead: 200000}}},
	} {
		if _, err := store.SeriesUpsertN(ctx, in); err != nil {
			t.Fatal(err)
		}
	}

	// ── 플랫폼 화면(platformModelUsage) ──
	rec := do(t, h, http.MethodGet, "/api/usage/platforms?from=2026-08-01&to=2026-08-31", "", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("platforms: %d %s", rec.Code, rec.Body.String())
	}
	var plat struct {
		Platforms []struct {
			Platform string  `json:"platform"`
			CostUsd  float64 `json:"costUsd"`
		} `json:"platforms"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &plat); err != nil {
		t.Fatal(err)
	}
	costByPlatform := map[string]float64{}
	for _, p := range plat.Platforms {
		costByPlatform[p.Platform] = p.CostUsd
	}
	if !(costByPlatform["gemini"] > costByPlatform["codex"]) {
		t.Fatalf("플랫폼 화면이 계단을 안 탄다: gemini=%v codex=%v (같은 토큰인데 롱 쪽이 더 비싸야 한다)",
			costByPlatform["gemini"], costByPlatform["codex"])
	}
	// gemini-2.5-pro 는 입력·출력 모두 정확히 2배(1.25→2.5) / 1.5배(10→15) 구간이다.
	// 캐시읽기까지 전부 롱이므로 총 비용은 표준 대비 2배를 넘는다(출력만 1.5배라 정확히 2배는 아니다).
	if costByPlatform["codex"] <= 0 {
		t.Fatalf("표준 구간 비용이 0 이다 — 단가가 사라졌다: %+v", plat)
	}

	// ── 좌석 화면(sessionUsage) ──
	rec = do(t, h, http.MethodGet, "/api/usage/seats?from=2026-08-01&to=2026-08-31", "", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("seats: %d %s", rec.Code, rec.Body.String())
	}
	var seats struct {
		Seats []struct {
			Username string  `json:"username"`
			USD      float64 `json:"usd"`
		} `json:"seats"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &seats); err != nil {
		t.Fatal(err)
	}
	seatCost := map[string]float64{}
	for _, s := range seats.Seats {
		seatCost[s.Username] = s.USD
	}
	if len(seatCost) == 0 {
		t.Fatalf("좌석 응답에서 비용 축을 못 찾았다: %s", rec.Body.String())
	}
	if !(seatCost["alice"] > seatCost["bob"]) {
		t.Fatalf("좌석 화면이 계단을 안 탄다: alice=%v bob=%v", seatCost["alice"], seatCost["bob"])
	}

	// ── 시간 시계열(bucketUsage) — byHour 경로가 usage_series 를 읽는다 ──
	rec = do(t, h, http.MethodGet,
		"/api/usage/series?metric=cost&interval=hour&group_by=user&from=2026-08-10&to=2026-08-10", "", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("series: %d %s", rec.Code, rec.Body.String())
	}
	var series struct {
		Series []struct {
			Key    map[string]string `json:"key"`
			Points []struct {
				V float64 `json:"v"`
			} `json:"points"`
		} `json:"series"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &series); err != nil {
		t.Fatal(err)
	}
	hourCost := map[string]float64{}
	for _, s := range series.Series {
		for _, p := range s.Points {
			hourCost[s.Key["user"]] += p.V
		}
	}
	if len(hourCost) == 0 {
		t.Fatalf("시간 시계열이 비었다: %s", rec.Body.String())
	}
	if !(hourCost["alice"] > hourCost["bob"]) {
		t.Fatalf("시간 시계열이 계단을 안 탄다: alice=%v bob=%v", hourCost["alice"], hourCost["bob"])
	}

	// ── 세션 상세(버킷이 있으면 bucketUsage, 없으면 sessionUsage) ──
	for _, tc := range []struct{ id, other string }{{"lc-alice", "lc-bob"}} {
		a := sessionDetailCost(t, h, tc.id)
		b := sessionDetailCost(t, h, tc.other)
		if !(a > b) {
			t.Fatalf("세션 상세가 계단을 안 탄다: %s=%v %s=%v", tc.id, a, tc.other, b)
		}
	}
}

func sessionDetailCost(t *testing.T, h http.Handler, id string) float64 {
	t.Helper()
	rec := do(t, h, http.MethodGet, "/api/usage/sessions/"+id, "", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("session %s: %d %s", id, rec.Code, rec.Body.String())
	}
	var body struct {
		Cost struct {
			USD float64 `json:"usd"`
		} `json:"cost"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Cost.USD
}
