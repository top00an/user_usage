package store

import (
	"context"
	"testing"
	"time"

	"github.com/tscorp/user-usage/internal/db"
)

/*
 * 테스트 하네스 — 이 스위트가 실제로 쓰는 것만 담는다.
 * "언젠가 쓸지도"로 헬퍼를 늘리지 않는다 — 하네스가 커지면 테스트가 하네스의 버그를 검증하게 된다.
 */

// fresh 는 빈 sqlite 저장소를 연다. store 의 DB 핸들이 패키지 변수라 테스트는 병렬로 돌지 않는다
// (t.Parallel 을 쓰지 않는 것이 계약이다).
func fresh(t *testing.T) context.Context {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, db.Options{Mode: "local", DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close(); handle = nil })
	if err := Init(ctx, d); err != nil {
		t.Fatalf("store init: %v", err)
	}
	return ctx
}

// freezeClock 은 시계를 고정한다. 보존·활동창처럼 날짜 경계가 판정을 가르는 자리에 쓴다.
func freezeClock(t *testing.T, iso string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		t.Fatalf("bad time %q: %v", iso, err)
	}
	prev := clock
	clock = func() time.Time { return at }
	t.Cleanup(func() { clock = prev })
	return at
}

func mustSession(t *testing.T, ctx context.Context, s SessionInput) {
	t.Helper()
	if err := SessionUpsert(ctx, s); err != nil {
		t.Fatalf("SessionUpsert(%s): %v", s.SessionID, err)
	}
}

func mustSeries(t *testing.T, ctx context.Context, in SeriesInput) int {
	t.Helper()
	n, err := SeriesUpsertN(ctx, in)
	if err != nil {
		t.Fatalf("SeriesUpsert(%s): %v", in.SessionID, err)
	}
	return n
}

func mustCounters(t *testing.T, ctx context.Context, in CountersInput) int {
	t.Helper()
	n, err := CountersUpsertN(ctx, in)
	if err != nil {
		t.Fatalf("CountersUpsert(%s): %v", in.SessionID, err)
	}
	return n
}

// modelRow 는 모델 행을 찾는다. 없으면 곧 "그 모델의 사용량이 통째로 사라졌다"는 뜻이다.
func modelRow(t *testing.T, rows []ModelRow, model string) ModelRow {
	t.Helper()
	for _, r := range rows {
		if r.Model == model {
			return r
		}
	}
	t.Fatalf("%s 행이 없다 — 그 모델의 사용량이 통째로 사라졌다", model)
	return ModelRow{}
}

// axisSum 은 모델 행 전체의 축별 합이다.
type axes struct{ Input, Output, CacheRead, CacheCreate int64 }

func sumAxes(rows []ModelRow) axes {
	var a axes
	for _, r := range rows {
		a.Input += r.Input
		a.Output += r.Output
		a.CacheRead += r.CacheRead
		a.CacheCreate += r.CacheCreate
	}
	return a
}

func sumShare(rows []ModelRow, pick func(ModelRow) Share) axes {
	var a axes
	for _, r := range rows {
		s := pick(r)
		a.Input += s.Input
		a.Output += s.Output
		a.CacheRead += s.CacheRead
		a.CacheCreate += s.CacheCreate
	}
	return a
}
