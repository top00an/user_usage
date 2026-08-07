'use strict';
/*
 * 백엔드 선택기(동결 계약의 단일 진입점).
 *   드라이버 선택: env USAGE_PG_URL 이 있으면 pg, 없으면 sqlite(기본).
 *   모듈 코드는 드라이버를 모르고 q()/tx()/dialect 만 쓴다.
 *
 * 재export 계약:
 *   const { q, tx, dialect } = require('./db/adapter');   // 또는 require('./db')
 */
const backend = process.env.USAGE_PG_URL ? require('./pg') : require('./sqlite');

module.exports = backend;
