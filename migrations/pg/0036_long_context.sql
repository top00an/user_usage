-- 0036 — 계단(롱컨텍스트) 요금의 토큰 분리분. usage_sessions · usage_series 양쪽.
--
-- 무엇을 재는 축인가: **총량 중 롱 구간 요청에서 발생한 몫**이다.
--   일부 모델은 한 요청의 입력 길이가 임계값을 넘으면 그 요청의 입력·출력 단가가 함께 오른다.
--     Google  gemini-2.5-pro · gemini-3.1-pro-preview — 입력 >200K 이면 입력·출력 모두 롱 단가
--             (공식: "If a query input context is longer than 200K tokens, all tokens
--              (input and output) are charged at long context rates")
--     OpenAI  gpt-5.5 · gpt-5.6 계열 — 입력 >272K 이면 요청 전체가 입력 2배 · 출력 1.5배
--
-- ⚠ **기존 컬럼(input·output·cache_read)의 의미는 바뀌지 않는다.** 여전히 전체 합계다.
--   표준 구간 몫은 `총량 − long` 으로 나온다. 총량에서 롱 몫을 빼서 저장하는 설계도 가능하지만
--   그러면 과거 행과 새 행의 의미가 갈려, 이미 저장된 값이 어느 규칙으로 쓰인 것인지 아무도
--   알 수 없게 된다. 총량은 건드리지 않고 부분집합을 하나 더 두는 쪽이 되돌릴 수 있다.
--
-- ⚠ **DEFAULT 0 이 하위호환의 전부다.** 지금까지 들어온 보고에는 이 분리분이 아예 없었고,
--   현행 수집기도 아직 보내지 않는다. 0 = "전부 표준 구간" 이므로 기존 행의 비용은 개편 전과
--   비트 동일하게 계산된다(go/internal/cost/regression_test.go 가 못박는다).
--   NULL 을 허용하지 않는 이유: "모른다"가 생기는 순간 `표준 = 총량 − long` 이라는 불변식이
--   깨지고, 그 행의 비용을 어느 쪽으로 계산해야 하는지 판정할 수 없게 된다.
--
-- ⚠ 불변식 `0 <= *_long <= 해당 총량` 은 **애플리케이션이** 지킨다
--   (go/internal/intake/intake.go 의 longNat 이 단일 출처 — 위반을 접고 그 수를 세어 로그로
--    올린다). CHECK 제약을 걸지 않는 이유는 0035 와 같다: 서버보다 새로운 수집기가 이상한
--   값을 보냈을 때 **보고가 통째로 거부되면 안 된다**(인테이크는 best-effort 경로다).
--   좁히는 일은 쓰기 직전에 한 곳에서 한다.
--
-- ⚠ 캐시 **생성**에는 분리분을 두지 않는다. 계단 요금을 매기는 두 공급사가 캐시 생성에 별도
--   롱 단가를 두지 않는다(Google 은 캐시 생성 무과금, OpenAI 는 5.6 계열의 단일 배수).
--   없는 축을 컬럼으로 만들면 언젠가 누군가 채우고, 그 값이 어디에도 쓰이지 않는다.
--
-- ⚠ 컬럼 추가다(새 테이블 아님) → RLS 정책을 새로 걸지 않는다(두 표에 이미 있다).
--   계단은 **격리 축이 아니다** — 테넌트 격리는 종전대로 tenant_id + RLS 가 한다.
--
-- ⚠ 인덱스를 만들지 않는다. 이 컬럼들은 필터·정렬 축이 아니라 **SUM 대상**이고, 조회는 항상
--   기존 인덱스(tenant_id + started_at/hour)로 좁힌 뒤 합산한다. 안 쓰는 인덱스는 쓰기만 느리게 한다.
--
-- sqlite 쪽 같은 컬럼은 go/internal/store/store.go 의 DDL·ensureColumn 이 소유한다(양 방언 동기).

ALTER TABLE usage_sessions ADD COLUMN IF NOT EXISTS input_long      bigint NOT NULL DEFAULT 0;
ALTER TABLE usage_sessions ADD COLUMN IF NOT EXISTS output_long     bigint NOT NULL DEFAULT 0;
ALTER TABLE usage_sessions ADD COLUMN IF NOT EXISTS cache_read_long bigint NOT NULL DEFAULT 0;

ALTER TABLE usage_series   ADD COLUMN IF NOT EXISTS input_long      bigint NOT NULL DEFAULT 0;
ALTER TABLE usage_series   ADD COLUMN IF NOT EXISTS output_long     bigint NOT NULL DEFAULT 0;
ALTER TABLE usage_series   ADD COLUMN IF NOT EXISTS cache_read_long bigint NOT NULL DEFAULT 0;
