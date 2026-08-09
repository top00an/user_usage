-- member_tokens — 개인 열람 토큰(RBAC). 한 토큰 = 한 사용자, 자기 데이터만 본다.
--
-- 관리자 토큰(USAGE_ADMIN_TOKEN)은 전사 열람이고, 이 표의 토큰은 그 사용자 한 명으로 범위가
-- 좁혀진다(게이트가 user 필터를 그 사람 이름으로 강제하고, 교차 뷰는 403). 평문은 저장하지
-- 않고 sha256 해시만 둔다(인제스트 키와 같은 규율).
--
-- ⚠ tenant-scoped(usage_* 3종 규율: tenant_id DEFAULT · FORCE RLS · tenant_isolation).
--   개인 열람 자격은 그 org 의 것이다.

CREATE TABLE IF NOT EXISTS member_tokens (
  token_hash text PRIMARY KEY,
  tenant_id  text NOT NULL DEFAULT COALESCE(current_setting('app.tenant_id', true), 'default'),
  username   text NOT NULL,
  created_at text NOT NULL,
  revoked_at text
);

CREATE INDEX IF NOT EXISTS member_tokens_user ON member_tokens(tenant_id, username);

DO $$
BEGIN
  EXECUTE 'ALTER TABLE member_tokens ENABLE ROW LEVEL SECURITY';
  EXECUTE 'ALTER TABLE member_tokens FORCE ROW LEVEL SECURITY';
  EXECUTE 'DROP POLICY IF EXISTS tenant_isolation ON member_tokens';
  EXECUTE 'CREATE POLICY tenant_isolation ON member_tokens '
    || 'USING (tenant_id = current_setting(''app.tenant_id'', true)) '
    || 'WITH CHECK (tenant_id = current_setting(''app.tenant_id'', true))';
END $$;
