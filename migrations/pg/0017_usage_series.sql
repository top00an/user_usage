-- 0017 — 사용량 시계열. 시간 × 모델 버킷. PG 전용(SQLite 는 lib/usage/store.js DDL 이 소유).
--
-- 왜 usage_sessions 로 부족한가(0014 를 대체하지 않고 **옆에** 두는 이유):
--   usage_sessions 는 세션당 1행이고 시각이 started_at 하나뿐이다. 그래서 5시간짜리 세션이
--   시작일 한 칸에 전량 쌓인다 — 2026-08-03 실측으로 4,314턴 세션 하나가 23억 토큰을 시작일에
--   몰아넣었다. 그 화면으로는 "어제 몇 시에 튀었나"를 물을 수 없다. 없는 정밀도를 만들지 않으려고
--   /api/usage/series 는 interval=hour 를 400 으로 거절해 왔고, 이 테이블이 그 거절을 푼다.
--
--   모델도 같은 문제였다. usage_sessions.model 은 세션의 **최빈 모델 1개**라, 모델이 섞인 세션은
--   전량 한 단가로 계산됐다. 실측 표본에서 6개 세션에 opus-4-8 / opus-5 / fable-5 / sonnet-5 가
--   함께 나타났고 단가는 최대 10배 차이 난다($1 ~ $10/MTok). 그래서 model 을 PK 에 넣는다.
--
-- usage_sessions 는 그대로 둔다 — 세션 목록·드릴다운의 앵커이고, 구버전 수집기가 보내는
--   유일한 형태이기도 하다. 이 테이블은 신버전 수집기가 **추가로** 보내는 것이다.
--
-- ⚠ 멱등성이 0014 와 동일하게 이 스키마의 핵심 제약이다. 클라이언트 훅은 트랜스크립트를 다시
--    읽어 **절대값**을 보낸다(증분 델타가 아니다). 그래서 PK 를 (세션, 시간, 모델)로 두고 UPSERT 로
--    **덮어쓴다**. `+=` 로 누적하면 훅이 두 번 도는 순간 값이 두 배가 된다. 같은 파일이 커진 뒤
--    재전송되면 그 세션의 모든 버킷이 다시 오므로 덮어쓰기가 정확하다.
--    (지워진 버킷은 남는다 — 트랜스크립트는 append-only 라 버킷이 사라지는 경우가 없다.)
--
-- ⚠ 신규 테이블 규율(0013·0014 참조): ① tenant_id DEFAULT COALESCE(current_setting…)
--    ② ENABLE + FORCE ROW LEVEL SECURITY ③ POLICY tenant_isolation.
--    test/rls-coverage.test.js 가 tenant_id 를 가진 모든 테이블에 이 셋을 강제한다.

CREATE TABLE IF NOT EXISTS usage_series (
  tenant_id    text NOT NULL DEFAULT COALESCE(current_setting('app.tenant_id', true), 'default'),
  session_id   text NOT NULL,
  hour         text NOT NULL,              -- YYYY-MM-DDTHH — 트랜스크립트 timestamp 의 시(UTC)
  model        text NOT NULL,              -- PK 구성원. '최빈 모델' 근사를 여기서 해소한다
  input        bigint NOT NULL DEFAULT 0,
  output       bigint NOT NULL DEFAULT 0,
  cache_read   bigint NOT NULL DEFAULT 0,
  cache_create bigint NOT NULL DEFAULT 0,  -- 총량(아래 두 값의 합) — 구버전 비교용으로 유지한다
  -- 캐시 생성 TTL 분해. 배수가 1.25배(5분) vs 2배(1시간)로 1.6배 차이 나므로 뭉뚱그리면
  -- 비용이 그만큼 틀린다. 실측(2026-08-03): 표본 46.2 MTok 의 **100% 가 1시간** 이었고,
  -- 5분으로 가정하던 기존 계산은 실제의 1/1.60 이었다.
  cc_5m        bigint NOT NULL DEFAULT 0,
  cc_1h        bigint NOT NULL DEFAULT 0,
  turns        integer NOT NULL DEFAULT 0,
  -- 품질 축. 전부 숫자다 — 오류 **메시지**·stop_reason 원문·경로는 담지 않는다(수집기 계약).
  tool_errors  integer NOT NULL DEFAULT 0, -- tool_result.is_error === true 인 횟수
  stop_max_tokens integer NOT NULL DEFAULT 0,
  stop_refusal    integer NOT NULL DEFAULT 0,
  -- 턴 지연 = assistant 타임스탬프 − 직전 user/tool_result 타임스탬프.
  -- 실측으로 9,345턴 중 9,340턴에서 그 쌍이 존재했다(p50 4.8초 · p95 31초). sum 과 max 를 함께
  -- 두는 이유: sum/turns 로 평균을 내되, 평균이 감추는 꼬리를 max 가 잡는다.
  latency_ms_sum  bigint NOT NULL DEFAULT 0,
  latency_ms_max  integer NOT NULL DEFAULT 0,
  latency_turns   integer NOT NULL DEFAULT 0, -- 지연을 잴 수 있었던 턴 수(평균의 분모)
  username     text,
  machine      text,
  project      text,
  PRIMARY KEY (tenant_id, session_id, hour, model)
);

-- 시간 축 조회가 이 테이블의 존재 이유다 — 그 인덱스를 먼저 둔다.
CREATE INDEX IF NOT EXISTS idx_usage_series_hour ON usage_series(tenant_id, hour);
CREATE INDEX IF NOT EXISTS idx_usage_series_model ON usage_series(tenant_id, model, hour);
CREATE INDEX IF NOT EXISTS idx_usage_series_user ON usage_series(tenant_id, username, hour);

DO $$
BEGIN
  EXECUTE 'ALTER TABLE usage_series ENABLE ROW LEVEL SECURITY';
  EXECUTE 'ALTER TABLE usage_series FORCE ROW LEVEL SECURITY';
  EXECUTE 'DROP POLICY IF EXISTS tenant_isolation ON usage_series';
  EXECUTE 'CREATE POLICY tenant_isolation ON usage_series '
    || 'USING (tenant_id = current_setting(''app.tenant_id'', true)) '
    || 'WITH CHECK (tenant_id = current_setting(''app.tenant_id'', true))';
END $$;

-- usage_sessions 에 종료 시각을 더한다. 세션 duration(ended_at − started_at)은 시간 버킷 없이도
-- 답할 수 있는 질문이고, 구버전 수집기가 보낸 행은 NULL 로 남아 "모른다"가 그대로 보인다.
ALTER TABLE usage_sessions ADD COLUMN IF NOT EXISTS ended_at text;
