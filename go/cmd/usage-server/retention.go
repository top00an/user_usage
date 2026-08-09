package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/tscorp/user-usage/internal/store"
)

/*
 * 키워드 보존 정리기 — 기한 지난 keyword 카운터를 주기적으로 지운다(lib/retention.js).
 *
 * 왜 별도 타이머인가: 인테이크(POST /api/usage)마다 DELETE 를 돌리면 보고 한 건에 전체 스캔이
 * 따라붙는다. 보존은 하루 단위 정책이라 하루 한 번이면 충분하고, 보고 경로는 가볍게 두는 편이 낫다.
 *
 * 첫 실행은 **부팅 직후가 아니라 조금 뒤**다. 타이머만 두면 매일 재시작되는 컨테이너에서
 * 24시간을 못 채우고 죽어 영원히 안 돌지만, 부팅과 동시에 돌리면 부팅 경로에 DB 쓰기가 얹힌다.
 * 보존 정리는 하루 단위 정책이라 1분 늦어도 아무 차이가 없고, 부팅은 늦어지면 안 된다.
 *
 * 절대 죽지 않는다 — 정리 실패가 서빙을 흔들면 안 된다.
 */
const (
	retentionFirstRun = time.Minute
	retentionInterval = 24 * time.Hour
)

// startRetention 은 정리기를 띄우고 멈추는 함수를 돌려준다. 보존이 꺼져 있는지는 **호출부가**
// 판단한다(config.KeywordRetentionDays == nil) — 여기까지 오면 무조건 돈다.
func startRetention(ctx context.Context, days int) func() {
	stop := make(chan struct{})
	go func() {
		first := time.NewTimer(retentionFirstRun)
		defer first.Stop()
		select {
		case <-first.C:
		case <-stop:
			return
		case <-ctx.Done():
			return
		}
		tick := time.NewTicker(retentionInterval)
		defer tick.Stop()
		for {
			pruneOnce(ctx, days)
			select {
			case <-tick.C:
			case <-stop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return func() { close(stop) }
}

func pruneOnce(ctx context.Context, days int) {
	res, err := store.PruneKeywordsDetail(ctx, days, time.Now())
	if err != nil {
		// 실패해도 던지지 않는다. 다만 조용하지도 않다 — 정리가 돌지 않았다는 사실은 남아야 한다.
		fmt.Fprintf(os.Stderr, "  ⚠ 키워드 보존 정리 실패: %v\n", err)
		return
	}
	if res.Removed > 0 {
		// 조용히 지우지 않는다 — 데이터가 사라지는 동작은 흔적을 남긴다.
		fmt.Printf("  · 키워드 보존 정리: %d행 삭제(%s 이전, 보존 %d일, 잔여 %d행)\n",
			res.Removed, res.Cutoff, res.Days, res.Kept)
	}
}
