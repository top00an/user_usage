-- 0035 — usage_sessions.platform. 멀티플랫폼(Claude·Codex·Gemini) 관측의 기반 축.
--
-- 무엇을 재는 축인가: 이 세션을 만든 **도구**다(claude|codex|gemini). 모델(model)과 다르다 —
-- 같은 모델이 여러 도구에서 쓰일 수 있고, 도구가 달라도 모델은 같을 수 있다. 그래서 model 을
-- 재활용하지 않고 컬럼을 하나 더 둔다.
--
-- ⚠ **DEFAULT 'claude' 가 이 마이그레이션의 핵심이다.** 지금까지 들어온 보고는 전부 Claude Code
--   수집기의 것이고, 현행 수집기는 이 필드를 아직 보내지 않는다. NOT NULL DEFAULT 로 추가하면
--   기존 행이 전부 claude 로 채워지는데 그것이 맞는 사실이다 — 여기서 NULL 을 허용하면
--   "모른다"가 생기고, 그 순간 플랫폼별 합계가 전체 합계와 어긋난다.
--
-- ⚠ 허용목록(claude|codex|gemini) 밖 값은 애플리케이션이 'other' 로 접는다
--   (go/internal/store/platform.go 의 NormalizePlatform 이 단일 출처). CHECK 제약을 걸지 않는
--   이유: 서버보다 새로운 수집기가 모르는 값을 보냈을 때 **보고가 통째로 거부되면 안 된다**
--   (인테이크는 best-effort 경로다). 좁히는 일은 쓰기 직전에 한 곳에서 한다.
--
-- ⚠ 컬럼 추가다(새 테이블 아님) → RLS 정책을 새로 걸지 않는다(usage_sessions 에 이미 있다).
--   platform 은 **격리 축이 아니다** — 테넌트 격리는 종전대로 tenant_id + RLS 가 한다.
--
-- sqlite 쪽 같은 컬럼은 go/internal/store/store.go 의 DDL·ensureColumn 이 소유한다(양 방언 동기).

ALTER TABLE usage_sessions ADD COLUMN IF NOT EXISTS platform text NOT NULL DEFAULT 'claude';

-- 조회는 항상 테넌트 안에서 도므로 선행 컬럼이 tenant_id 다(idx_usage_sessions_at·_user 와 같은 규율).
CREATE INDEX IF NOT EXISTS idx_usage_sessions_platform ON usage_sessions(tenant_id, platform);
