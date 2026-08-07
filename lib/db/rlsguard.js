'use strict';
/*
 * RLS 성립 전제 가드 — 앱이 붙은 DB 롤이 테넌트 격리를 무력화하는 롤인지 판정한다.
 *
 * 왜 별도 모듈인가: 이 판정은 이 시스템에서 **가장 조용한 치명적 오설정**을 잡는다. 앱이
 * SUPERUSER 또는 BYPASSRLS 롤로 접속하면 `FORCE ROW LEVEL SECURITY` 조차 무시되어 모든 테넌트의
 * 행이 서로 보인다. 그런데 증상이 없다 — 요청은 200 이고 데이터도 잘 보인다(남의 것까지).
 * 그래서 "판정"을 서버 부팅 코드에 묻어두지 않고 여기로 꺼내 **양성·음성 양쪽을 단위 테스트로
 * 못박는다**(실제 슈퍼유저 계정 없이도 결정론적으로 검증 가능).
 *
 * 흔한 사고 경로: USAGE_PG_URL 을 관리/마이그레이션 롤로 지정하는 것 —
 *   · postgres 공식 이미지의 POSTGRES_USER 는 SUPERUSER + BYPASSRLS 로 만들어진다
 *   · RDS 마스터 유저도 넓은 권한을 갖는다
 * 앱은 반드시 비-슈퍼·비-BYPASSRLS 롤로 붙어야 한다.
 */

/**
 * @param {{role?:string, rolsuper?:boolean, rolbypassrls?:boolean}|null} row
 *        `SELECT current_user AS role, rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`
 * @returns {{ok:true}|{ok:false, message:string}} ok:false 면 RLS 격리가 성립하지 않는다.
 */
function check(row) {
  if (!row) return { ok: true }; // 확인 불가(행 없음)는 여기서 판정하지 않는다 — 호출부가 모드별로 결정한다.
  const sup = row.rolsuper === true;
  const byp = row.rolbypassrls === true;
  if (!sup && !byp) return { ok: true };
  const attrs = [sup ? 'SUPERUSER' : null, byp ? 'BYPASSRLS' : null].filter(Boolean).join('+');
  return {
    ok: false,
    message: `앱 DB 롤 '${row.role || '?'}' 이 ${attrs} 입니다 — RLS 테넌트 격리가 성립하지 않습니다`,
  };
}

/** 위반 시 사람이 바로 고칠 수 있는 해결 문장까지 붙인 메시지(saas fail-fast 용). */
function remedy(message) {
  return `${message}. USAGE_PG_URL 을 비-슈퍼·비-BYPASSRLS 앱 롤로 바꾸세요 `
    + `(CREATE ROLE … NOSUPERUSER NOBYPASSRLS). 관리/마이그레이션 롤로 서빙하면 `
    + `전 테넌트 데이터가 서로 노출됩니다.`;
}

module.exports = { check, remedy };
