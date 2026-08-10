-- auth_users / auth_sessions — 대시보드 사람 로그인(ID/PW → 세션 쿠키).
--
-- 기존 인증(USAGE_ADMIN_TOKEN·인제스트 키·개인 열람 토큰)은 그대로 살아 있다. 이 두 표는
-- 사람이 아이디/비밀번호로 로그인해 세션 쿠키를 받는 경로만 추가한다. 세션 쿠키의 스코프는
-- 사용자의 role(admin|member)이고, member 세션은 자기 데이터만 본다(게이트가 강제).
--
-- ⚠ tenant-scoped(usage_*·member_tokens 규율 그대로: tenant_id DEFAULT · FORCE RLS ·
--   tenant_isolation 정책). 사람 계정과 세션은 그 org 의 것이므로 크로스테넌트로 새면 안 된다.
--
-- ⚠ 비밀번호는 평문 저장 금지 — password_hash 에 bcrypt 해시만 둔다.
-- ⚠ 세션 토큰은 평문 저장 금지 — token_hash 에 sha256(token) 만 둔다(인제스트 키·개인 토큰과
--   같은 사상). 평문은 쿠키에만 있고 DB 는 해시로만 조회한다.
--
-- created_at·expires_at 는 text(RFC3339 UTC)다 — 이 레포의 시간 컬럼 관례(member_tokens·orgs)를
-- 그대로 따른다. `?`→`$n` 치환과 방언 무관 SQL 을 유지하려면 두 방언(text/sqlite·text/pg)이 같은
-- 타입이어야 하고, RFC3339 Zulu 는 사전식 정렬이 곧 시간 정렬이라 `expires_at > ?` 비교가 성립한다.

CREATE TABLE IF NOT EXISTS auth_users (
  tenant_id     text NOT NULL DEFAULT COALESCE(current_setting('app.tenant_id', true), 'default'),
  username      text NOT NULL,
  password_hash text NOT NULL,
  role          text NOT NULL CHECK (role IN ('admin', 'member')),
  created_at    text NOT NULL,
  PRIMARY KEY (tenant_id, username)
);

CREATE TABLE IF NOT EXISTS auth_sessions (
  token_hash text PRIMARY KEY,
  tenant_id  text NOT NULL DEFAULT COALESCE(current_setting('app.tenant_id', true), 'default'),
  username   text NOT NULL,
  role       text NOT NULL,
  expires_at text NOT NULL,
  created_at text NOT NULL
);

CREATE INDEX IF NOT EXISTS auth_sessions_tenant_expiry ON auth_sessions(tenant_id, expires_at);

DO $$
BEGIN
  EXECUTE 'ALTER TABLE auth_users ENABLE ROW LEVEL SECURITY';
  EXECUTE 'ALTER TABLE auth_users FORCE ROW LEVEL SECURITY';
  EXECUTE 'DROP POLICY IF EXISTS tenant_isolation ON auth_users';
  EXECUTE 'CREATE POLICY tenant_isolation ON auth_users '
    || 'USING (tenant_id = current_setting(''app.tenant_id'', true)) '
    || 'WITH CHECK (tenant_id = current_setting(''app.tenant_id'', true))';

  EXECUTE 'ALTER TABLE auth_sessions ENABLE ROW LEVEL SECURITY';
  EXECUTE 'ALTER TABLE auth_sessions FORCE ROW LEVEL SECURITY';
  EXECUTE 'DROP POLICY IF EXISTS tenant_isolation ON auth_sessions';
  EXECUTE 'CREATE POLICY tenant_isolation ON auth_sessions '
    || 'USING (tenant_id = current_setting(''app.tenant_id'', true)) '
    || 'WITH CHECK (tenant_id = current_setting(''app.tenant_id'', true))';
END $$;
