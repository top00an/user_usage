-- team_members — org 안의 팀/그룹 계층 (Phase: 사용자별 분석 고도화 Slice 2).
--
-- 사용자(username)를 팀에 배정해 팀별 롤업(비용·토큰·좌석)을 낸다. 세션에는 username 만 있으므로
-- 팀 집계는 이 매핑을 거친다. 한 사용자는 한 팀(단순 모델) — 여러 팀이 필요해지면 확장한다.
--
-- ⚠ 이 표는 **tenant-scoped 다** — usage_* 와 같은 3종 규율(tenant_id DEFAULT · FORCE RLS ·
--   tenant_isolation). org/ingest_keys(라우팅 계층, RLS 없음)와 다르다: 팀 구성은 그 org 의
--   업무 데이터라 다른 org 에 보이면 안 된다.

CREATE TABLE IF NOT EXISTS team_members (
  tenant_id text NOT NULL DEFAULT COALESCE(current_setting('app.tenant_id', true), 'default'),
  username  text NOT NULL,
  team      text NOT NULL,
  PRIMARY KEY (tenant_id, username)
);

CREATE INDEX IF NOT EXISTS team_members_team ON team_members(tenant_id, team);

DO $$
BEGIN
  EXECUTE 'ALTER TABLE team_members ENABLE ROW LEVEL SECURITY';
  EXECUTE 'ALTER TABLE team_members FORCE ROW LEVEL SECURITY';
  EXECUTE 'DROP POLICY IF EXISTS tenant_isolation ON team_members';
  EXECUTE 'CREATE POLICY tenant_isolation ON team_members '
    || 'USING (tenant_id = current_setting(''app.tenant_id'', true)) '
    || 'WITH CHECK (tenant_id = current_setting(''app.tenant_id'', true))';
END $$;
