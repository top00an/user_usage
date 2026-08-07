-- 0014 — 사용량·명령·키워드 텔레메트리. PG 전용(SQLite 는 lib/usage/store.js DDL 이 소유).
--
-- 왜 learning_signals 에 얹지 않았나:
--   learning_signals 는 **품질 판정**(failure/correction/praise/pattern)이고 증류 입력이다.
--   여기 들어오는 것은 판정이 아니라 **관측치**(무엇을 몇 번 썼나)다. 한 테이블에 섞으면
--   distill 이 "Bash 를 85회 썼다"를 교훈으로 삼으려 든다. 축이 다르므로 테이블을 가른다.
--   연결은 읽기 시점에 한다 — 실사용 빈도를 증류 우선순위 가중치로 쓰는 식(routes/usage.js).
--
-- 멱등성이 이 스키마의 핵심 제약이다. 클라이언트 훅은 세션 파일을 다시 읽어 **절대값**을 보낸다
--   (증분 델타가 아니다). 재전송·중복 전송이 카운트를 부풀리면 안 되므로 PK 를 (세션, 축, 키)로
--   두고 UPSERT 로 **덮어쓴다**. 델타 누적(+=)을 쓰면 훅이 두 번 돌 때 값이 두 배가 된다.
--
-- ⚠ 신규 테이블 3종 규율(0013 참조): ① tenant_id DEFAULT COALESCE(current_setting…)
--    ② ENABLE + FORCE ROW LEVEL SECURITY ③ POLICY tenant_isolation.
--    test/rls-coverage.test.js 가 tenant_id 를 가진 모든 테이블에 이 셋을 강제한다.
--
-- 컬럼명이 username 인 이유: `user` 는 PostgreSQL 예약어라 매 쿼리에서 따옴표가 필요해진다.

CREATE TABLE IF NOT EXISTS usage_sessions (
  tenant_id    text NOT NULL DEFAULT COALESCE(current_setting('app.tenant_id', true), 'default'),
  session_id   text NOT NULL,              -- Claude Code 세션 uuid(트랜스크립트 파일명)
  machine      text,                       -- 발신 머신(os.hostname)
  username     text,                       -- 발신 OS 사용자
  project      text,                       -- 프로젝트 디렉터리 슬러그(경로 원문 아님)
  model        text,                       -- 그 세션에서 가장 많이 쓰인 모델
  input        bigint NOT NULL DEFAULT 0,
  output       bigint NOT NULL DEFAULT 0,
  cache_read   bigint NOT NULL DEFAULT 0,  -- 캐시 읽기는 입력의 대부분을 차지한다. 분리 안 하면
  cache_create bigint NOT NULL DEFAULT 0,  -- 비용이 수십 배로 과대 추정된다(실측 71887 vs 2).
  web_search   integer NOT NULL DEFAULT 0,
  web_fetch    integer NOT NULL DEFAULT 0,
  turns        integer NOT NULL DEFAULT 0, -- assistant 응답 수 — 사용량을 "세션 크기"로 정규화할 때 쓴다
  started_at   text,
  reported_at  text,
  PRIMARY KEY (tenant_id, session_id)
);

CREATE INDEX IF NOT EXISTS idx_usage_sessions_at ON usage_sessions(tenant_id, started_at);
CREATE INDEX IF NOT EXISTS idx_usage_sessions_user ON usage_sessions(tenant_id, username);

-- 축(kind)별 카운터. 한 테이블로 통합하는 이유: 화면이 축을 하나씩 늘릴 때마다 테이블이 늘면
-- 집계 쿼리가 갈라진다. kind 로 가르면 UI 는 같은 쿼리에 필터만 바꾼다.
--   tool      Claude Code 내장 도구(Bash/Edit/Read…)
--   bash      Bash 명령의 **선두 실행파일만**(git/npm/pytest…) — 인자는 절대 저장하지 않는다
--   slash     슬래시 명령(/clear, /compact…)
--   skill     Skill 도구로 호출된 스킬 이름
--   agent     Agent 도구의 subagent_type
--   mcp       MCP 도구 이름 — 서버가 직접 계측하는 것과 교차 검증된다
--   keyword   정규화된 프롬프트 토큰. 원문은 저장하지 않는다
CREATE TABLE IF NOT EXISTS usage_counters (
  tenant_id  text NOT NULL DEFAULT COALESCE(current_setting('app.tenant_id', true), 'default'),
  session_id text NOT NULL,
  kind       text NOT NULL,
  key        text NOT NULL,
  count      integer NOT NULL DEFAULT 0,
  day        text,                         -- YYYY-MM-DD — 일별 집계를 인덱스 하나로 끝낸다
  username   text,
  machine    text,
  PRIMARY KEY (tenant_id, session_id, kind, key)
);

CREATE INDEX IF NOT EXISTS idx_usage_counters_kind ON usage_counters(tenant_id, kind, key);
CREATE INDEX IF NOT EXISTS idx_usage_counters_day ON usage_counters(tenant_id, day, kind);

-- 추천 관측 — 카탈로그의 구멍을 찾는 표.
--
-- "무엇을 요청했는데 무엇이 추천됐고 점수가 얼마였나". score 가 0 이거나 낮은 목표가 모이면
-- 그 자리가 **새 에이전트를 만들 자리**다. 목표 원문은 저장하지 않고 토큰만 남긴다
-- (goal_tokens) — 고객사명·경로·비밀이 목표 문장에 섞여 들어오기 때문이다.
CREATE TABLE IF NOT EXISTS usage_recommendations (
  tenant_id   text NOT NULL DEFAULT COALESCE(current_setting('app.tenant_id', true), 'default'),
  id          bigserial,
  goal_tokens text,                        -- 공백 구분 정규화 토큰(원문 아님)
  agent       text,                        -- 추천된 에이전트(없으면 NULL)
  skills      text,                        -- 추천된 스킬 목록(쉼표 구분)
  score       real,                        -- 매칭 점수. 0 이면 매칭 실패(= 카탈로그 공백 후보)
  source      text,                        -- 'mcp' | 'web'
  username    text,
  at          text,
  PRIMARY KEY (tenant_id, id)
);

CREATE INDEX IF NOT EXISTS idx_usage_reco_at ON usage_recommendations(tenant_id, at);
CREATE INDEX IF NOT EXISTS idx_usage_reco_score ON usage_recommendations(tenant_id, score);

DO $$
DECLARE
  t text;
  tables text[] := ARRAY['usage_sessions', 'usage_counters', 'usage_recommendations'];
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
