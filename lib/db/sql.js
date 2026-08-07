'use strict';
/*
 * SQL 방언 유틸. 자리표시자 정책:
 *   SQL 본문은 '?' 를 유지하고, pg 드라이버일 때만 로드 시 '?'→'$n' 로 치환한다.
 *   문자열 리터럴('...') 안의 '?' 는 자리표시자가 아니므로 건드리지 않는다.
 */
function toPg(sql) {
  let out = '';
  let n = 0;
  let inStr = false; // 홑따옴표 문자열 리터럴 내부 여부
  for (let i = 0; i < sql.length; i++) {
    const ch = sql[i];
    if (ch === "'") { inStr = !inStr; out += ch; continue; }
    if (ch === '?' && !inStr) { out += '$' + (++n); continue; }
    out += ch;
  }
  return out;
}

module.exports = { toPg };
