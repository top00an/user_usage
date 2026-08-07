'use strict';
/*
 * 데이터 계층 진입점 — 이 프로젝트가 DB 에 닿는 **유일한 문**이다.
 *
 * 여기 있는 것은 재export 뿐이고, 그것이 의도다. 드라이버 선택(sqlite | pg)은 adapter 가 하고,
 * 호출부(lib/store.js·lib/identity.js)는 드라이버를 모른 채 q()/tx()/dialect 만 쓴다.
 * 그래서 `USAGE_PG_URL` 하나로 로컬 파일과 원격 PostgreSQL 을 바꿔 낄 수 있다.
 *
 * 계약(sqlite·pg 양쪽이 동일):
 *   q(sql) → { async get(...p), async all(...p), async run(...p) }
 *     get  행이 없으면 null
 *     run  { changes, lastInsertRowid }
 *   tx(async (t) => …)   t 는 q 와 같은 인터페이스. 커밋/롤백 자동, 중첩 금지.
 *   execMulti(sqlText)   멀티 스테이트먼트 원시 실행(스키마 DDL·마이그레이션 러너용)
 *   dialect              'sqlite' | 'pg'
 *
 * SQL 본문은 방언 무관하게 '?' 자리표시자를 쓴다 — pg 어댑터가 로드 시 '$n' 으로 옮긴다.
 */
const adapter = require('./adapter');

module.exports = adapter;
