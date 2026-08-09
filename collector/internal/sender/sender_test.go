package sender

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/tscorp/user-usage/collector/internal/payload"
)

func sess(id string) payload.Session {
	return payload.Session{ID: id, Input: 10, Output: 20, Counters: map[string]map[string]int64{}}
}

// 정상 경로: 올바른 Bearer 헤더로 POST /api/usage, 응답 파싱.
func TestSendHappyPath(t *testing.T) {
	var gotAuth, gotPath, gotMethod string
	var gotBody payload.Report
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotMethod = r.Method
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(200)
		_ = json.NewEncoder(w).Encode(Result{OK: true, Sessions: len(gotBody.Sessions), Counters: 3, Buckets: 2})
	}))
	defer srv.Close()

	c := New(srv.URL, "tok-secret")
	res, err := c.Send(context.Background(), "alice", "pc-1", []payload.Session{sess("s1"), sess("s2")})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" || gotPath != "/api/usage" {
		t.Fatalf("메서드/경로: %s %s", gotMethod, gotPath)
	}
	if gotAuth != "Bearer tok-secret" {
		t.Fatalf("Authorization 헤더가 틀렸다: %q", gotAuth)
	}
	if gotBody.User != "alice" || gotBody.Machine != "pc-1" {
		t.Fatalf("귀속이 실리지 않았다: %+v", gotBody)
	}
	if res.Sessions != 2 {
		t.Fatalf("서버 응답 집계: %+v", res)
	}
}

// 멱등: 같은 페이로드를 두 번 보내면 서버가 받는 본문이 **동일**하다(수집기가 절대값을 보내고,
// 재전송에 시각·난수 같은 것을 끼우지 않는다). 서버의 session_id UPSERT 가 중복집계를 막는다.
func TestResendIsByteIdentical(t *testing.T) {
	var bodies [][]byte
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, raw)
		mu.Unlock()
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true,"sessions":1}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	sessions := []payload.Session{sess("dup-session")}
	for i := 0; i < 2; i++ {
		if _, err := c.Send(context.Background(), "u", "m", sessions); err != nil {
			t.Fatal(err)
		}
	}
	if len(bodies) != 2 {
		t.Fatalf("요청 수 %d", len(bodies))
	}
	if string(bodies[0]) != string(bodies[1]) {
		t.Fatalf("재전송 본문이 다르다:\n%s\n%s", bodies[0], bodies[1])
	}
}

// 5xx 는 재시도한다. 두 번 실패 후 성공하면 최종 성공.
func TestRetryOn5xx(t *testing.T) {
	var calls int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n < 3 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true,"sessions":1}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	c.Backoff = time.Millisecond
	res, err := c.Send(context.Background(), "u", "m", []payload.Session{sess("s")})
	if err != nil {
		t.Fatalf("재시도 후에도 실패: %v", err)
	}
	if calls != 3 {
		t.Fatalf("호출 수 %d, want 3", calls)
	}
	if !res.OK {
		t.Fatal("성공으로 접히지 않았다")
	}
}

// 4xx 는 재시도하지 않는다 — 요청 자체가 틀렸다.
func TestNoRetryOn4xx(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(403)
		_, _ = w.Write([]byte("forbidden"))
	}))
	defer srv.Close()

	c := New(srv.URL, "wrong")
	c.Backoff = time.Millisecond
	if _, err := c.Send(context.Background(), "u", "m", []payload.Session{sess("s")}); err == nil {
		t.Fatal("403 인데 성공했다")
	}
	if calls != 1 {
		t.Fatalf("4xx 를 재시도했다: 호출 %d회", calls)
	}
}

// 청크: 세션이 서버 상한(50)을 넘으면 여러 요청으로 쪼갠다 — 51번째부터 소리 없이 사라지지 않게.
func TestChunksOverMaxSessions(t *testing.T) {
	var total, requests int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body payload.Report
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		requests++
		total += len(body.Sessions)
		mu.Unlock()
		if len(body.Sessions) > 50 {
			t.Errorf("한 요청에 세션이 50개를 넘었다: %d", len(body.Sessions))
		}
		w.WriteHeader(200)
		_ = json.NewEncoder(w).Encode(Result{OK: true, Sessions: len(body.Sessions)})
	}))
	defer srv.Close()

	sessions := make([]payload.Session, 120)
	for i := range sessions {
		sessions[i] = sess("s")
	}
	c := New(srv.URL, "tok")
	res, err := c.Send(context.Background(), "u", "m", sessions)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 3 {
		t.Fatalf("요청 수 %d, want 3(50+50+20)", requests)
	}
	if total != 120 || res.Sessions != 120 {
		t.Fatalf("세션 합계 total=%d res=%d, want 120", total, res.Sessions)
	}
}
