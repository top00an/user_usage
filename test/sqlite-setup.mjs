/*
 * SQLite 테스트 격리 하네스 — node --test 프로세스마다 전용 데이터 디렉터리를 물린다.
 *
 * 사용(package.json 의 test 스크립트가 붙인다):
 *   node --import ./test/sqlite-setup.mjs --test test/*.test.js
 *
 * ── 왜 필요한가 ─────────────────────────────────────────────────────────
 * USAGE_DATA_DIR 을 설정하지 않고 `require('../lib/store')` 를 하면, lib/db 가 로드 시점에
 * **레포의 실제 data/usage.db** 를 연다. node --test 는 파일마다 별도 프로세스를 띄우므로
 * 여러 프로세스가 같은 파일을 동시에 열고 DDL·PRAGMA 를 건다. 그러면 전체 실행에서만 이런
 * 실패가 난다:
 *
 *     Error: database is locked   (errcode 261)
 *       at lib/store.js:20  ← require 시점
 *
 * 파일 단위로 죽으므로 개별 테스트 이름이 남지 않고, 어느 파일이 걸릴지는 타이밍이라 **매번 다른
 * 파일**이 실패한다. 단독 실행에서는 동시 접근자가 없어 재현되지 않는다 — "전체 실행에서만
 * 깨진다" 가 이것이다. 게다가 개발자의 실제 사용량 DB 를 테스트가 건드리게 된다.
 *
 * ── 왜 진입점에서 고치나 ───────────────────────────────────────────────
 * 대안 둘은 다 나쁘다:
 *   ① 테스트 파일마다 USAGE_DATA_DIR 을 심는다 — 새 파일이 생길 때마다 빠뜨리고, 빠뜨린 것이
 *      조용한 간헐 실패로만 드러난다.
 *   ② lib/db 가 락을 재시도하게 만든다 — **운영 코드를 테스트 사정으로 바꾸는** 것이다.
 * 프로세스 진입점에서 한 번 돌리면 새 파일도 자동으로 격리된다.
 *
 * ── 계약 ────────────────────────────────────────────────────────────────
 * · **이미 설정돼 있으면 건드리지 않는다.** 스스로 임시 디렉터리를 만드는 테스트와
 *   pg 모드(USAGE_PG_URL)의 의도를 덮지 않는다.
 * · 자식 프로세스를 spawn 하는 테스트는 자기 USAGE_DATA_DIR 을 명시로 넘기므로 영향 없다.
 * · 프로세스 종료 시 디렉터리를 지운다(실패해도 무시 — 정리 실패가 테스트 실패는 아니다).
 */
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

if (!process.env.USAGE_PG_URL && !process.env.USAGE_DATA_DIR) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), `usage-suite-${process.pid}-`));
  process.env.USAGE_DATA_DIR = dir;

  // 종료 시 정리. exit 훅에서 동기 삭제 — 비동기는 프로세스가 먼저 죽어 안 돌 수 있다.
  process.on('exit', () => {
    try { fs.rmSync(dir, { recursive: true, force: true }); } catch { /* 비치명 */ }
  });
}
