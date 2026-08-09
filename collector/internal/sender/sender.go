// Package sender 는 좁혀진 보고를 `POST /api/usage` 로 보낸다.
//
// ── 신뢰성 기본값 ──────────────────────────────────────────────────────────
// 이 코드는 팀원 PC 에서 best-effort 로 도는 클라이언트다. 그래서 다음이 **옵션이 아니라
// 기본값**이다:
//
//	타임아웃    멈춘 서버가 수집기를 무한정 잡지 않게 한다.
//	재시도      5xx·네트워크 오류에 지수 백오프로 몇 번 다시 시도한다. 4xx 는 다시 시도하지
//	            않는다(요청 자체가 틀렸으니 반복해도 같다).
//	멱등        재전송이 안전한 근거는 **서버의 session_id 절대값 UPSERT** 다. 그래서 여기엔
//	            멱등키 헤더가 없다 — 재시도가 값을 두 배로 만들 수 없다.
//	청크        서버는 한 요청에 세션 50개까지만 받는다(초과분은 조용히 잘린다). 그래서
//	            MaxSessions 단위로 쪼개 여러 요청으로 보낸다 — 그러지 않으면 51번째부터가
//	            소리 없이 사라진다.
//
// 표준 라이브러리와 collector/internal/{payload,policy} 말고 아무것도 import 하지 않는다.
package sender

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tscorp/user-usage/collector/internal/payload"
	"github.com/tscorp/user-usage/collector/internal/policy"
)

// Result 는 서버 인테이크 응답(routes 의 intakeResponse)이다.
type Result struct {
	OK       bool `json:"ok"`
	Sessions int  `json:"sessions"`
	Counters int  `json:"counters"`
	Buckets  int  `json:"buckets"`
}

// Client 는 인테이크 전송기다. HTTP 는 주입 가능하게 둔다(테스트가 httptest 로 갈아끼운다).
type Client struct {
	BaseURL string       // 예: http://127.0.0.1:4191
	Token   string       // Authorization: Bearer 에 실을 인테이크 토큰
	HTTP    *http.Client // nil 이면 기본(타임아웃 30초)
	Retries int          // 5xx·네트워크 오류 재시도 횟수(0 이면 1회만 시도)
	Backoff time.Duration
}

// New 는 실전 기본값을 가진 클라이언트를 만든다.
func New(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
		Retries: 3,
		Backoff: 500 * time.Millisecond,
	}
}

// Send 는 세션들을 MaxSessions 청크로 쪼개 보내고, 서버가 센 합을 하나로 모아 돌려준다.
// 청크 하나가 끝내 실패하면 거기서 error 를 낸다(이미 보낸 청크는 서버 멱등성 덕에 재실행이
// 안전하다).
func (c *Client) Send(ctx context.Context, user, machine string, sessions []payload.Session) (Result, error) {
	var agg Result
	for start := 0; start < len(sessions); start += policy.MaxSessions {
		end := start + policy.MaxSessions
		if end > len(sessions) {
			end = len(sessions)
		}
		rep := payload.Report{User: user, Machine: machine, Sessions: sessions[start:end]}
		res, err := c.sendOne(ctx, rep)
		if err != nil {
			return agg, err
		}
		agg.OK = true
		agg.Sessions += res.Sessions
		agg.Counters += res.Counters
		agg.Buckets += res.Buckets
	}
	return agg, nil
}

func (c *Client) sendOne(ctx context.Context, rep payload.Report) (Result, error) {
	body, err := json.Marshal(rep)
	if err != nil {
		return Result{}, fmt.Errorf("보고 직렬화 실패: %w", err)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}

	var lastErr error
	attempts := c.Retries + 1
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			// 지수 백오프. ctx 취소를 존중한다 — 종료 신호가 오면 더 매달리지 않는다.
			select {
			case <-ctx.Done():
				return Result{}, ctx.Err()
			case <-time.After(c.Backoff << (attempt - 1)):
			}
		}
		res, retry, err := c.attempt(ctx, hc, body)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if !retry {
			return Result{}, err
		}
	}
	return Result{}, fmt.Errorf("인테이크 전송이 %d회 모두 실패: %w", attempts, lastErr)
}

// attempt 는 한 번의 POST 다. 두 번째 반환값(retry)이 true 면 재시도할 가치가 있는 실패다
// (네트워크·5xx). 4xx 는 retry=false 로 즉시 포기한다.
func (c *Client) attempt(ctx context.Context, hc *http.Client, body []byte) (Result, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/usage", bytes.NewReader(body))
	if err != nil {
		return Result{}, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := hc.Do(req)
	if err != nil {
		return Result{}, true, err // 네트워크 오류 — 재시도
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode >= 500 {
		return Result{}, true, fmt.Errorf("서버 %d: %s", resp.StatusCode, snippet(raw))
	}
	if resp.StatusCode != http.StatusOK {
		// 401/403/400 — 요청 자체가 틀렸다. 반복해도 같으므로 재시도하지 않는다.
		return Result{}, false, fmt.Errorf("서버 %d: %s", resp.StatusCode, snippet(raw))
	}

	var res Result
	if err := json.Unmarshal(raw, &res); err != nil {
		return Result{}, false, fmt.Errorf("응답 파싱 실패: %w (본문 %s)", err, snippet(raw))
	}
	return res, false, nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
