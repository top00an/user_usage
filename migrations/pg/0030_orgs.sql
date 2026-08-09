-- orgs · ingest_keys — 멀티테넌트 라우팅 표 (Phase 1 / SaaS).
--
-- ⚠ 이 두 표는 **tenant-scoped 가 아니다 — RLS 를 걸지 않는다.** usage_* 표(0014)의 3종 규율
--   (tenant_id DEFAULT + FORCE RLS + tenant_isolation)을 여기엔 적용하지 않는다. 이유:
--   인제스트 키 → tenant 를 **해석하는** 라우팅 계층이라, 아직 tenant 를 모르는 상태에서
--   조회돼야 한다(RLS 를 걸면 순환이 된다). 격리는 이 표가 돌려준 tenant_id 로 usage_* 에서 선다.
--
-- 앱 롤 권한: 기존 usage_* 와 같은 규칙 — migrations 는 스키마만 소유하고, SELECT/INSERT/UPDATE
--   권한은 롤 프로비저닝에서 부여한다(운영). NOSUPERUSER·NOBYPASSRLS 앱 롤로 라우팅 조회가
--   되려면 이 두 표에 테이블 권한이 있어야 한다.

CREATE TABLE IF NOT EXISTS orgs (
  id         text PRIMARY KEY,
  tenant_id  text NOT NULL,
  name       text NOT NULL,
  status     text NOT NULL DEFAULT 'active',
  created_at text NOT NULL
);

CREATE TABLE IF NOT EXISTS ingest_keys (
  key_hash     text PRIMARY KEY,
  org_id       text NOT NULL REFERENCES orgs(id),
  created_at   text NOT NULL,
  revoked_at   text,
  last_used_at text
);

CREATE INDEX IF NOT EXISTS ingest_keys_org ON ingest_keys(org_id);
