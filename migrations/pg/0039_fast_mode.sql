-- 0039 — 고속 모드(fast mode) 토큰 분리분. usage_sessions · usage_series 양쪽.
--
-- 무엇을 재는 축인가: **총량 중 고속 모드로 처리된 몫**이다. 0036(롱컨텍스트)과 같은 계약이다 —
-- 총량의 부분집합이고, 표준 속도 몫은 `총량 − fast` 로 나온다.
--
-- 왜 필요한가: 고속 모드는 같은 모델에 **다른 단가**가 붙는다.
--   Anthropic  Claude Opus 5 / Opus 4.8 — 고속 $10/$50 (표준 $5/$25 의 정확히 2배).
--              캐시 배수는 그 위에 얹힌다(공식: "Prompt caching multipliers apply on top of
--              fast mode pricing") → 캐시 축도 2배다.
--   OpenAI     공식 단가 표 각주 "Fast mode pricing is doubled."
-- 분리분이 없으면 고속 세션이 표준가로 계산돼 **비용이 절반으로** 나온다. 방향이 나쁘다 —
-- 과대가 아니라 과소다(사람은 싸게 나온 숫자를 의심하지 않는다).
--
-- ⚠ **기존 컬럼(input·output·cache_read·cache_create)의 의미는 바뀌지 않는다.** 여전히 전체
--   합계다. 총량에서 고속 몫을 빼서 저장하는 설계도 가능하지만 그러면 과거 행과 새 행의 의미가
--   갈려, 이미 저장된 값이 어느 규칙으로 쓰인 것인지 아무도 알 수 없게 된다(0036 과 같은 판단).
--
-- ⚠ **DEFAULT 0 이 하위호환의 전부다.** 지금까지 들어온 보고에는 이 분리분이 없었다.
--   0 = "전부 표준 속도" 이므로 기존 행의 비용은 개편 전과 **비트 동일**하게 계산된다
--   (go/internal/cost/ratecard_audit_test.go 의 무회귀 단정이 못박는다).
--   NULL 을 허용하지 않는 이유도 0036 과 같다: "모른다"가 생기면 `표준 = 총량 − fast` 라는
--   불변식이 깨지고, 그 행의 비용을 어느 쪽으로 계산해야 하는지 판정할 수 없다.
--
-- ⚠ 불변식 `0 <= *_fast <= 해당 총량` 은 **애플리케이션이** 지킨다
--   (go/internal/intake/intake.go 의 longNat 을 재사용한다 — 같은 종류의 몫이라 같은 규율이다).
--   CHECK 제약을 걸지 않는 이유는 0035·0036 과 같다: 서버보다 새로운 수집기가 이상한 값을
--   보냈을 때 **보고가 통째로 거부되면 안 된다**(인테이크는 best-effort 경로다).
--
-- ⚠ 캐시 **생성**에도 분리분을 둔다. 0036 은 두지 않았는데(그때는 근거가 없다고 판단했다),
--   고속 모드는 기준 입력가를 올리고 캐시 배수가 그 위에 얹히므로 캐시 쓰기도 2배다.
--   캐시 축을 빼면 고속 세션의 캐시 비용이 절반으로 잡힌다 — 이 워크로드에서 캐시가 토큰의
--   대부분이라(실측: 캐시읽기가 전체 토큰의 90% 이상) 그 누락이 가장 크다.
--
-- ⚠ 컬럼 추가다(새 테이블 아님) → RLS 정책을 새로 걸지 않는다(두 표에 이미 있다).
--   속도는 **격리 축이 아니다** — 테넌트 격리는 종전대로 tenant_id + RLS 가 한다.
--
-- ⚠ 인덱스를 만들지 않는다. 필터·정렬 축이 아니라 **SUM 대상**이고, 조회는 항상 기존
--   인덱스(tenant_id + started_at/hour)로 좁힌 뒤 합산한다.
--
-- sqlite 쪽 같은 컬럼은 go/internal/store/store.go 의 DDL·ensureColumn 이 소유한다(양 방언 동기).

ALTER TABLE usage_sessions ADD COLUMN IF NOT EXISTS input_fast        bigint NOT NULL DEFAULT 0;
ALTER TABLE usage_sessions ADD COLUMN IF NOT EXISTS output_fast       bigint NOT NULL DEFAULT 0;
ALTER TABLE usage_sessions ADD COLUMN IF NOT EXISTS cache_read_fast   bigint NOT NULL DEFAULT 0;
ALTER TABLE usage_sessions ADD COLUMN IF NOT EXISTS cache_create_fast bigint NOT NULL DEFAULT 0;

ALTER TABLE usage_series   ADD COLUMN IF NOT EXISTS input_fast        bigint NOT NULL DEFAULT 0;
ALTER TABLE usage_series   ADD COLUMN IF NOT EXISTS output_fast       bigint NOT NULL DEFAULT 0;
ALTER TABLE usage_series   ADD COLUMN IF NOT EXISTS cache_read_fast   bigint NOT NULL DEFAULT 0;
ALTER TABLE usage_series   ADD COLUMN IF NOT EXISTS cache_create_fast bigint NOT NULL DEFAULT 0;
