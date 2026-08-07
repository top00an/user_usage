'use strict';
/*
 * 키워드 보존 정리기 — 기한 지난 keyword 카운터를 주기적으로 지운다.
 *
 * 왜 별도 타이머인가: 인테이크(POST /api/usage)마다 DELETE 를 돌리면 보고 한 건에 전체 스캔이
 * 따라붙는다. 보존은 하루 단위 정책이라 하루 한 번이면 충분하고, 보고 경로는 가볍게 두는 편이 낫다.
 *
 * 기동 계약:
 *   - start() 는 { stop() } 핸들을 돌려주고 server.js 가 disposers 에 등록한다(SIGTERM 시 정리).
 *   - 절대 throw 하지 않는다. 정리 실패가 부팅이나 다른 작업을 흔들면 안 된다.
 *   - 첫 실행은 **부팅 직후가 아니라 조금 뒤**다(FIRST_RUN_DELAY_MS). 타이머만 두면 매일
 *     재시작되는 컨테이너에서 24시간을 못 채우고 죽어 영원히 안 돌지만, 부팅과 동시에 돌리면
 *     부팅 경로에 DB 쓰기가 얹힌다. 보존 정리는 하루 단위 정책이라 1분 늦어도 아무 차이가 없고,
 *     부팅은 늦어지면 안 된다 — 그래서 뒤로 민다.
 *
 * 설정(env 우선, 기본 90일):
 *   USAGE_KEYWORD_RETENTION_DAYS=90   0 이하나 'off' 면 정리기를 아예 띄우지 않는다(무기한 보관).
 *   USAGE_RETENTION_INTERVAL_H=24     정리 주기(시간). 테스트·운영 조정용.
 */
const store = require('./store');

const DEFAULT_INTERVAL_H = 24;
// 첫 실행 지연 — 부팅과 DB 쓰기가 겹치지 않게 뒤로 민다. 테스트는 서버를 자주 띄우고 같은
// 데이터 디렉터리를 공유하므로, 부팅마다 DELETE 를 얹으면 락 경합을 키운다.
const FIRST_RUN_DELAY_MS = 60_000;
let TIMER = null;
let FIRST = null;

/* 'off'·0·음수 → null(정리 안 함). 그 외는 store 가 하한·상한으로 클램프한다. */
function configuredDays() {
  const raw = String(process.env.USAGE_KEYWORD_RETENTION_DAYS || '').trim();
  if (!raw) return store.KEYWORD_RETENTION_DEFAULT;
  if (/^(off|no|false|0)$/i.test(raw)) return null;
  const n = Number(raw);
  if (!Number.isFinite(n) || n <= 0) return store.KEYWORD_RETENTION_DEFAULT;
  return store.retentionDays(n);
}

function intervalMs() {
  const n = Number(process.env.USAGE_RETENTION_INTERVAL_H);
  const h = Number.isFinite(n) && n > 0 ? Math.min(24 * 30, n) : DEFAULT_INTERVAL_H;
  return Math.max(60_000, Math.round(h * 3600_000));
}

/* 1회 정리. 호출부가 결과를 로그로 쓸 수 있게 요약을 돌려준다. 실패는 null. */
async function runOnce(days) {
  try {
    const d = days == null ? configuredDays() : days;
    if (d == null) return null;
    return await store.pruneKeywords({ days: d });
  } catch { return null; }
}

/*
 * 정리기 기동. 보존이 꺼져 있으면 **타이머를 걸지 않고** null 을 돌려준다
 * (server.js 가 null 이면 아무것도 등록하지 않는다 — 완전 no-op).
 */
function start() {
  const days = configuredDays();
  if (days == null) return null;
  stop();
  const tick = async () => {
    const r = await runOnce(days);
    if (r && r.removed > 0) {
      // 조용히 지우지 않는다 — 데이터가 사라지는 동작은 흔적을 남긴다.
      console.log(`  · 키워드 보존 정리: ${r.removed}행 삭제(${r.cutoff} 이전, 보존 ${r.days}일, 잔여 ${r.kept}행)`);
    }
  };
  FIRST = setTimeout(tick, FIRST_RUN_DELAY_MS);   // 부팅 직후가 아니라 조금 뒤 1회
  if (FIRST && typeof FIRST.unref === 'function') FIRST.unref();
  TIMER = setInterval(tick, intervalMs());
  if (TIMER && typeof TIMER.unref === 'function') TIMER.unref();
  return { stop, days };
}

function stop() {
  if (FIRST) { clearTimeout(FIRST); FIRST = null; }
  if (TIMER) { clearInterval(TIMER); TIMER = null; }
}

module.exports = { start, stop, runOnce, configuredDays, intervalMs, FIRST_RUN_DELAY_MS };
