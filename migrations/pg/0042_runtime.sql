-- 0042 — usage_sessions.runtime. 로컬 LLM 관측의 기반 축.
--
-- 무엇을 재는 축인가: 이 세션이 **어디서 돌았나**다(cloud|local). platform 과 다르다 —
-- platform 은 "어느 도구"이고, 같은 도구가 클라우드 모델도 로컬 모델도 물 수 있다. 그래서
-- platform 을 재활용하거나 'codex-local' 같은 합성 값을 만들지 않고 컬럼을 하나 더 둔다.
--
-- 왜 필요한가: 로컬 모델은 API 과금이 $0 이고 단가표에 없어 비용에서 빠진다. 그런데 데이터에
-- 로컬이라고 적힌 자리가 없으면 "우리 작업의 얼마가 로컬로 옮겨갔나"에 답할 수 없고, 더 나쁘게는
-- 로컬 모델 이름이 클라우드 과금 ID 와 겹치는 날 **쓰지 않은 비용이 계산된다.**
-- go/internal/cost/seed_openai.go 가 gpt-oss-120b 를 미등록으로 둔 이유가 정확히 그것이다:
-- "어디서 돌렸는지를 사용량 레코드가 말해 주지 않는다."
--
-- ⚠ **DEFAULT 'cloud' 가 이 마이그레이션의 핵심이다.** 지금까지 들어온 보고에는 이 필드가
--   없었고, 현행 수집기도 판정에 실패하면 보내지 않는다. NOT NULL DEFAULT 로 추가하면 기존 행이
--   전부 cloud 로 채워진다. NULL 을 허용하면 "모른다"가 생기고, 그 순간 runtime 별 합계가 전체
--   합계와 어긋난다(0035 의 판단과 같다).
--
--   다만 이 기본값은 **"클라우드였다"는 관측이 아니다** — "로컬이라는 표시가 없다"는 뜻이다.
--   압도적 다수가 실제로 클라우드이므로 그 이름을 쓰지만, 화면이 이 값을 "확인된 클라우드"로
--   말하면 과장이다. 그 구분은 UI 문안이 진다.
--
-- ⚠ 허용목록(cloud|local) 밖 값은 애플리케이션이 'cloud' 로 접는다
--   (go/internal/store/runtime.go 의 NormalizeRuntime 이 단일 출처). CHECK 제약을 걸지 않는
--   이유는 0035 와 같다 — 서버보다 새로운 수집기의 보고가 통째로 거부되면 안 된다.
--   platform 과 달리 제3의 값('other')을 두지 않는다: runtime 은 이분법이라 늘어날 이유가 없고,
--   모르는 값은 "로컬이라는 표시가 없다"와 실질적으로 같다.
--
-- ⚠ 컬럼 추가다(새 테이블 아님) → RLS 정책을 새로 걸지 않는다(usage_sessions 에 이미 있다).
--   runtime 은 **격리 축이 아니다** — 테넌트 격리는 종전대로 tenant_id + RLS 가 한다.
--
-- sqlite 쪽 같은 컬럼은 go/internal/store/store.go 의 DDL·ensureColumn 이 소유한다(양 방언 동기).

ALTER TABLE usage_sessions ADD COLUMN IF NOT EXISTS runtime text NOT NULL DEFAULT 'cloud';

-- 조회는 항상 테넌트 안에서 도므로 선행 컬럼이 tenant_id 다(idx_usage_sessions_platform 과 같은 규율).
CREATE INDEX IF NOT EXISTS idx_usage_sessions_runtime ON usage_sessions(tenant_id, runtime);
