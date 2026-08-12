-- 0037 — 이미 저장된 자리표시자 모델 라벨(`<synthetic>` 등) 정리. PG 전용.
--
-- 무엇을 고치는가: Claude Code 는 중단·오류 메시지 같은 턴에 모델 이름 대신 `<synthetic>` 을
--   쓴다. 인테이크가 이제 그 값을 빈 값으로 접지만(go/internal/intake/intake.go 의 normModel),
--   **그 수정 이전에 저장된 행은 그대로 남는다.** 모델 축에 `<synthetic>` 이 계속 보이는 것이
--   그 잔여이고, 이 파일이 그 잔여를 되돌린다.
--
-- ⚠ 이것은 **데이터 마이그레이션**이다(스키마가 아니다). 0014~0036 과 성격이 다르므로 읽기 전에
--   아래 세 가지를 확인하라. 절차·되돌리기는 docs/OPERATIONS.md §8 이 소유한다.
--
--   ① **숫자는 움직이지 않는다.** 자리표시자 턴의 토큰은 전부 0 이다. 세션·버킷·카운터·턴을
--      하나도 버리지 않고 **모델 라벨만** 바꾼다. `①+②+③ == Totals` 불변식은 정리 전후 모두
--      성립한다(go/cmd/usage-server/maintenance_test.go 가 못 박는다).
--   ② **멱등이다.** 두 번 돌려도 같은 결과이고, 두 번째는 0행이다(정규식이 아무것도 못 잡는다).
--   ③ **테이블 잠금이 걸린다.** 아래 ALTER 는 usage_sessions·usage_series 에 ACCESS EXCLUSIVE
--      락을 트랜잭션 끝까지 잡는다 — 그동안 인테이크가 대기한다. 트래픽이 낮을 때 돌려라.
--
-- 판정 규칙은 한 벌이다: `^<[^<>]*>$` — 꺾쇠로 감싼 값 **전체**만 자리표시자이고, 꺾쇠가 일부만
--   있는 값(`a<b>c`)은 아니다. 단일 출처는 go/internal/intake/intake.go 의 placeholderModelRe 이고,
--   sqlite 쪽 같은 일은 go/cmd/usage-server/maintenance.go 가 소유한다(양 방언 동기).
--
-- 빈 값의 표시 규칙도 이 레포에 이미 한 벌씩 있고, 그 자리로 합류시킨다:
--   usage_sessions.model → NULL      (집계가 COALESCE(NULLIF(model,''),'(미상)') 로 읽는다)
--   usage_series.model   → '(미상)'  (PK 구성원이라 NOT NULL 이다 — NULL 로 둘 수 없다)
--
-- ⚠ **RLS 를 잠시 푼다(FORCE 만).** usage_* 는 FORCE ROW LEVEL SECURITY 라 **테이블 소유자에게도**
--   정책이 적용된다. 마이그레이션은 `app.tenant_id` GUC 없이 도므로
--   `tenant_id = current_setting('app.tenant_id', true)` 가 NULL 비교가 되어 **한 행도 보이지 않는다** —
--   그러면 이 파일은 오류 없이 0행을 고치고 끝난다(조용한 미적용). 그래서 소유자 예외를 잠깐
--   되살린다. ENABLE 은 건드리지 않는다(앱 롤 usage_app 의 격리는 내내 그대로다).
--   BEGIN/COMMIT 로 감싸므로 중간에 실패하면 FORCE 해제까지 함께 롤백된다.

BEGIN;

ALTER TABLE usage_sessions NO FORCE ROW LEVEL SECURITY;
ALTER TABLE usage_series   NO FORCE ROW LEVEL SECURITY;

-- ① 세션의 대표 모델 — 버리는 것이 아니라 "모른다"로 되돌린다.
UPDATE usage_sessions
   SET model = NULL
 WHERE model ~ '^<[^<>]*>$';

/*
 * ② 시간 버킷 — 라벨만 바꾸면 **PK 가 충돌한다.**
 *
 * usage_series 의 PK 는 (tenant_id, session_id, hour, model) 이라, 같은 시각에 이미 '(미상)'
 * 버킷이 있으면 단순 UPDATE 는 제약 위반으로 실패한다. 두 버킷은 정리 후 **같은 시각의 같은
 * 모델**이 되므로 합산 병합이 맞는 방향이다.
 *
 * 토큰은 0 이지만 turns·tool_errors·stop_*·latency_* 는 0 이 아닐 수 있다 — 그것들을 잃으면
 * 품질 축의 숫자가 조용히 줄어든다. 그래서 컬럼마다 결합 방식을 명시한다:
 *   합(SUM)        input·output·cache_read·cache_create·cc_5m·cc_1h·*_long·turns·
 *                  tool_errors·stop_max_tokens·stop_refusal·latency_ms_sum·latency_turns
 *   최댓값(GREATEST) latency_ms_max — 합이 아니다. 두 버킷 중 큰 쪽이 그 시각의 꼬리다.
 *   기존값 유지     username·machine·project — 라벨이라 더할 것이 없다. 기존이 비었을 때만 채운다.
 *
 * 한 (세션, 시각)에 자리표시자가 둘 이상일 수 있어(`<synthetic>` 과 `<none>`) 먼저 GROUP BY 로
 * 하나로 접은 뒤 INSERT 한다 — 그래야 ON CONFLICT 가 같은 행을 두 번 건드리지 않는다.
 * DELETE ... RETURNING 과 INSERT 의 대상 PK 는 서로 다르므로(자리표시자 vs '(미상)') 같은
 * 문장 안에서 충돌하지 않는다.
 */
WITH removed AS (
  DELETE FROM usage_series
   WHERE model ~ '^<[^<>]*>$'
  RETURNING *
), merged AS (
  SELECT tenant_id, session_id, hour,
         SUM(input)           AS input,
         SUM(output)          AS output,
         SUM(cache_read)      AS cache_read,
         SUM(cache_create)    AS cache_create,
         SUM(cc_5m)           AS cc_5m,
         SUM(cc_1h)           AS cc_1h,
         SUM(input_long)      AS input_long,
         SUM(output_long)     AS output_long,
         SUM(cache_read_long) AS cache_read_long,
         SUM(turns)           AS turns,
         SUM(tool_errors)     AS tool_errors,
         SUM(stop_max_tokens) AS stop_max_tokens,
         SUM(stop_refusal)    AS stop_refusal,
         SUM(latency_ms_sum)  AS latency_ms_sum,
         MAX(latency_ms_max)  AS latency_ms_max,
         SUM(latency_turns)   AS latency_turns,
         MAX(username)        AS username,
         MAX(machine)         AS machine,
         MAX(project)         AS project
    FROM removed
   GROUP BY tenant_id, session_id, hour
)
INSERT INTO usage_series (
  tenant_id, session_id, hour, model,
  input, output, cache_read, cache_create, cc_5m, cc_1h,
  input_long, output_long, cache_read_long,
  turns, tool_errors, stop_max_tokens, stop_refusal,
  latency_ms_sum, latency_ms_max, latency_turns,
  username, machine, project
)
SELECT tenant_id, session_id, hour, '(미상)',
       input, output, cache_read, cache_create, cc_5m, cc_1h,
       input_long, output_long, cache_read_long,
       turns, tool_errors, stop_max_tokens, stop_refusal,
       latency_ms_sum, latency_ms_max, latency_turns,
       username, machine, project
  FROM merged
ON CONFLICT (tenant_id, session_id, hour, model) DO UPDATE SET
  input           = usage_series.input           + EXCLUDED.input,
  output          = usage_series.output          + EXCLUDED.output,
  cache_read      = usage_series.cache_read      + EXCLUDED.cache_read,
  cache_create    = usage_series.cache_create    + EXCLUDED.cache_create,
  cc_5m           = usage_series.cc_5m           + EXCLUDED.cc_5m,
  cc_1h           = usage_series.cc_1h           + EXCLUDED.cc_1h,
  input_long      = usage_series.input_long      + EXCLUDED.input_long,
  output_long     = usage_series.output_long     + EXCLUDED.output_long,
  cache_read_long = usage_series.cache_read_long + EXCLUDED.cache_read_long,
  turns           = usage_series.turns           + EXCLUDED.turns,
  tool_errors     = usage_series.tool_errors     + EXCLUDED.tool_errors,
  stop_max_tokens = usage_series.stop_max_tokens + EXCLUDED.stop_max_tokens,
  stop_refusal    = usage_series.stop_refusal    + EXCLUDED.stop_refusal,
  latency_ms_sum  = usage_series.latency_ms_sum  + EXCLUDED.latency_ms_sum,
  -- ⚠ 여기만 합이 아니다.
  latency_ms_max  = GREATEST(usage_series.latency_ms_max, EXCLUDED.latency_ms_max),
  latency_turns   = usage_series.latency_turns   + EXCLUDED.latency_turns,
  -- 라벨은 기존 값을 유지한다(비어 있을 때만 채운다).
  username        = COALESCE(usage_series.username, EXCLUDED.username),
  machine         = COALESCE(usage_series.machine,  EXCLUDED.machine),
  project         = COALESCE(usage_series.project,  EXCLUDED.project);

ALTER TABLE usage_sessions FORCE ROW LEVEL SECURITY;
ALTER TABLE usage_series   FORCE ROW LEVEL SECURITY;

COMMIT;
