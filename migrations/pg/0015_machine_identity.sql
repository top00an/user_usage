-- 0015 — 머신 → 계정 매핑. PostgreSQL 전용(SQLite 는 lib/identity.js 의 DDL 이 소유).
--
-- 왜 필요한가(실측): 팀원 PC 는 자기 신원을 **클라이언트가 보고**한다. 그런데 그 값이 OS 계정명이라
-- 팀에서 쓰는 계정명과 어긋났고, 어긋난 자리를 고치려면 수집기를 고쳐 재배포하고 그 PC 가
-- 재설치해야 했다. 같은 부류의 누락이 반복됐다.
--
-- 근본 원인은 "신원의 권위가 클라이언트에 있다"는 것이다. 그래서 **서버를 권위로 둔다**:
-- 이 표에 머신이 등록돼 있으면 클라이언트가 무엇을 보내든 그 값으로 덮어쓴다.
--   · 팀원 PC 를 건드리지 않고 관리자가 화면에서 고칠 수 있다(재설치 불필요).
--   · 클라이언트 경로가 하나 더 생겨도 인테이크가 한 곳이라 자동으로 교정된다.
--   · 설치 시점 신원은 그대로 둔다 — 신규 설치의 정상 경로이고,
--     이 표는 그게 어긋났을 때의 **교정 수단**이지 대체재가 아니다.
--
-- ⚠ 신규 테이블 3종 규율: tenant_id DEFAULT · ENABLE+FORCE RLS · POLICY tenant_isolation.

CREATE TABLE IF NOT EXISTS machine_identity (
  tenant_id  text NOT NULL DEFAULT COALESCE(current_setting('app.tenant_id', true), 'default'),
  machine    text NOT NULL,              -- os.hostname() 그대로(클라이언트가 보고하는 키)
  username   text NOT NULL,              -- 이 머신을 귀속시킬 계정명
  note       text,                       -- 왜 걸었는지(사람이 나중에 읽는다)
  updated_by text,
  updated_at text,
  PRIMARY KEY (tenant_id, machine)
);

CREATE INDEX IF NOT EXISTS idx_machine_identity_user ON machine_identity(tenant_id, username);

DO $$
DECLARE
  t text;
  tables text[] := ARRAY['machine_identity'];
BEGIN
  FOREACH t IN ARRAY tables LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
    EXECUTE format(
      'CREATE POLICY tenant_isolation ON %I '
      || 'USING (tenant_id = current_setting(''app.tenant_id'', true)) '
      || 'WITH CHECK (tenant_id = current_setting(''app.tenant_id'', true))', t);
  END LOOP;
END $$;
