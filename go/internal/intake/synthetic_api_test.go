package intake_test

/*
 * 인테이크 → 저장 → API 응답까지의 **끝-끝** 대조.
 *
 * 왜 intake 패키지 단위 테스트로 부족한가: 이 결함은 "정규화가 틀렸다"가 아니라 "정규화하는
 * 코드가 없다"였다. 그런 결함은 경계를 하나만 보면 늘 통과한다 — 응답 본문에 그 문자열이
 * 실제로 안 나오는 것을 보는 자리가 있어야 한다.
 *
 * 그래서 이 파일은 **외부 테스트 패키지**(intake_test)다. intake 는 계약상 내부 패키지를 하나도
 * import 하지 않으므로(패키지 주석), 그 규율을 지키면서 store·httpapi 를 태우려면 이 방법뿐이다.
 * 테스트 전용 import 라 제품 코드의 의존 그래프는 그대로다.
 */

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tscorp/user-usage/internal/config"
	"github.com/tscorp/user-usage/internal/db"
	"github.com/tscorp/user-usage/internal/httpapi"
	"github.com/tscorp/user-usage/internal/identity"
	"github.com/tscorp/user-usage/internal/store"
	"github.com/tscorp/user-usage/internal/tenant"
)

const (
	e2eAdmin  = "test-admin-token-0123456789"
	e2eIntake = "test-intake-token-9876543210"

	// Claude Code 가 중단·오류 턴의 model 자리에 직접 쓰는 값. 어떤 응답에도 나오면 안 된다.
	synthetic = "<synthetic>"

	synthSession = "a1b2c3d4-0000-4000-8000-00000000e2e1"
	realSession  = "a1b2c3d4-0000-4000-8000-00000000e2e2"
)

// 읽기 API 는 최근 창을 본다 — 고정 날짜를 박으면 테스트가 언젠가 창 밖으로 밀린다.
func nowHour() (hour, ts string) {
	n := time.Now().UTC().Add(-30 * time.Minute)
	return n.Format("2006-01-02T15"), n.Format("2006-01-02T15:04:05.000Z")
}

func newAPI(t *testing.T) http.Handler {
	t.Helper()
	ctx := tenant.With(context.Background(), "default")
	d, err := db.Open(ctx, db.Options{Mode: "local", DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := store.Init(ctx, d); err != nil {
		t.Fatalf("store.Init: %v", err)
	}
	if err := identity.Init(ctx, d); err != nil {
		t.Fatalf("identity.Init: %v", err)
	}
	return httpapi.New(config.Config{
		Token: e2eAdmin, IntakeToken: e2eIntake,
		Mode: "local", Host: "127.0.0.1", Port: 4193, Tenant: "default",
	})
}

func call(t *testing.T, h http.Handler, method, target, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// 실측 모양 그대로의 보고 본문. 중단 턴(<synthetic>)과 정상 턴이 같은 보고에 섞여 온다.
func syntheticReport(hour, ts string) string {
	return fmt.Sprintf(`{
	  "user": "user-a", "machine": "pc-a",
	  "sessions": [
	    {
	      "id": %q, "startedAt": %q, "endedAt": %q, "project": "proj-a",
	      "model": %q,
	      "input": 0, "output": 0, "cacheRead": 0, "cacheCreate": 0, "turns": 3,
	      "series": [
	        {"hour": %q, "model": %q, "input": 0, "output": 0, "turns": 3, "toolErrors": 1}
	      ],
	      "counters": {"tool": {"Bash": 2, "Read": 5}, "bash": {"git": 2}}
	    },
	    {
	      "id": %q, "startedAt": %q, "endedAt": %q, "project": "proj-a",
	      "model": "claude-opus-5",
	      "input": 100, "output": 200, "cacheRead": 300, "cacheCreate": 40, "turns": 4,
	      "series": [
	        {"hour": %q, "model": "claude-opus-5", "input": 100, "output": 200,
	         "cacheRead": 300, "cacheCreate": 40, "turns": 4}
	      ],
	      "counters": {"tool": {"Edit": 1}}
	    }
	  ]
	}`, synthSession, ts, ts, synthetic, hour, synthetic,
		realSession, ts, ts, hour)
}

// 읽기 API 전부. 하나라도 빠지면 그 화면으로 다시 샌다.
var readEndpoints = []string{
	"/api/usage/summary",
	"/api/usage/sessions?top=500",
	"/api/usage/series",
	"/api/usage/distribution",
	"/api/usage/quality",
	"/api/usage/coverage",
	"/api/usage/leaderboard",
	"/api/usage/platforms",
	"/api/usage/dev",
	"/api/usage/teams",
	"/api/usage/seats",
	"/api/usage/dispatch",
}

func TestSyntheticNeverReachesAnyAPIResponse(t *testing.T) {
	h := newAPI(t)
	hour, ts := nowHour()

	w := call(t, h, http.MethodPost, "/api/usage", syntheticReport(hour, ts), e2eIntake)
	if w.Code != http.StatusOK {
		t.Fatalf("인테이크 status = %d, body = %s", w.Code, w.Body.String())
	}
	var ing struct {
		Sessions int `json:"sessions"`
		Buckets  int `json:"buckets"`
		Counters int `json:"counters"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &ing); err != nil {
		t.Fatalf("인테이크 응답이 JSON 이 아니다: %s", w.Body.String())
	}
	// 세션을 버리지 않았다 — 둘 다 저장됐다.
	if ing.Sessions != 2 {
		t.Fatalf("저장된 세션 = %d, want 2 (자리표시자 때문에 세션을 버리면 안 된다): %s",
			ing.Sessions, w.Body.String())
	}
	if ing.Buckets != 2 {
		t.Fatalf("저장된 버킷 = %d, want 2: %s", ing.Buckets, w.Body.String())
	}

	for _, ep := range readEndpoints {
		r := call(t, h, http.MethodGet, ep, "", e2eAdmin)
		if strings.Contains(r.Body.String(), synthetic) {
			t.Errorf("%s 응답에 %s 가 그대로 나왔다 — 사용자에게 모델 이름처럼 보인다:\n%s",
				ep, synthetic, r.Body.String())
		}
	}
}

// 모델 축에서 빼는 것과 세션을 버리는 것은 다른 일이다 — 턴·도구 카운터가 살아 있는가.
func TestSyntheticSessionKeepsTurnsAndCounters(t *testing.T) {
	h := newAPI(t)
	hour, ts := nowHour()

	if w := call(t, h, http.MethodPost, "/api/usage", syntheticReport(hour, ts), e2eIntake); w.Code != 200 {
		t.Fatalf("인테이크 status = %d: %s", w.Code, w.Body.String())
	}

	w := call(t, h, http.MethodGet, "/api/usage/sessions?top=500", "", e2eAdmin)
	if w.Code != http.StatusOK {
		t.Fatalf("sessions status = %d: %s", w.Code, w.Body.String())
	}
	var sessResp struct {
		Sessions []struct {
			SessionID string `json:"sessionId"`
			Model     any    `json:"model"`
			Turns     int64  `json:"turns"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &sessResp); err != nil {
		t.Fatalf("sessions 응답 파싱: %v — %s", err, w.Body.String())
	}
	var found bool
	for _, s := range sessResp.Sessions {
		if s.SessionID != synthSession {
			continue
		}
		found = true
		if s.Turns != 3 {
			t.Errorf("세션 turns = %d, want 3 — 실재하는 사용을 버렸다", s.Turns)
		}
		if m, ok := s.Model.(string); ok && m == synthetic {
			t.Errorf("세션 모델 = %q", m)
		}
	}
	if !found {
		t.Fatalf("세션 %s 가 목록에서 사라졌다: %s", synthSession, w.Body.String())
	}

	// 도구 카운터도 남았다. summary.top.tool 에 Bash/Read 가 보여야 한다.
	sum := call(t, h, http.MethodGet, "/api/usage/summary", "", e2eAdmin)
	if sum.Code != http.StatusOK {
		t.Fatalf("summary status = %d: %s", sum.Code, sum.Body.String())
	}
	for _, key := range []string{`"Bash"`, `"Read"`} {
		if !strings.Contains(sum.Body.String(), key) {
			t.Errorf("summary 에 도구 카운터 %s 가 없다 — 카운터가 함께 버려졌다:\n%s",
				key, sum.Body.String())
		}
	}
}

/*
 * 불변식 ①+②+③ == totals.
 *
 * 이 결함의 수정은 **모델 라벨만** 바꾼다 — 행을 버리지도, 합치지도 않는다. 그 사실을 여기서
 * 못 박는다: 라벨을 바꾸다 행이 하나라도 사라지면 모델별 합이 totals 보다 작아지고,
 * 사람에게는 "유실"로 보인다(이 레포에서 실제로 일어났던 결함).
 */
func TestSyntheticKeepsByModelSumEqualsTotals(t *testing.T) {
	h := newAPI(t)
	hour, ts := nowHour()

	if w := call(t, h, http.MethodPost, "/api/usage", syntheticReport(hour, ts), e2eIntake); w.Code != 200 {
		t.Fatalf("인테이크 status = %d: %s", w.Code, w.Body.String())
	}

	ctx := tenant.With(context.Background(), "default")
	totals, err := store.Totals(ctx)
	if err != nil {
		t.Fatalf("store.Totals: %v", err)
	}
	rows, err := store.UsageByModel(ctx)
	if err != nil {
		t.Fatalf("store.UsageByModel: %v", err)
	}

	var in, out, cr, cc int64
	for _, r := range rows {
		if r.Model == synthetic {
			t.Errorf("모델 축에 %s 행이 있다: %+v", synthetic, r)
		}
		// ① series 몫 + ②③ 세션 몫 = 그 행의 합.
		if r.FromSeries.Output+r.FromSession.Output != r.Output {
			t.Errorf("행 %q 의 ①+②③ 이 합과 다르다: %+v", r.Model, r)
		}
		in += r.Input
		out += r.Output
		cr += r.CacheRead
		cc += r.CacheCreate
	}
	if in != totals.Input || out != totals.Output || cr != totals.CacheRead || cc != totals.CacheCreate {
		t.Fatalf("모델별 합 (%d/%d/%d/%d) != totals (%d/%d/%d/%d)",
			in, out, cr, cc, totals.Input, totals.Output, totals.CacheRead, totals.CacheCreate)
	}
}
