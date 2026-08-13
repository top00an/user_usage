-- 0040 — 캐시 **생성**의 롱 구간 몫. usage_sessions · usage_series 양쪽.
--
-- 0036 이 롱컨텍스트 분리분을 넣을 때 이 컬럼은 **일부러 빼고** 이렇게 적었다:
--   "캐시 생성에는 분리분을 두지 않는다. 계단 요금을 매기는 두 공급사가 캐시 생성에 별도 롱
--    단가를 두지 않는다(Google 은 캐시 생성 무과금, OpenAI 는 5.6 계열의 단일 배수)."
--
-- **그 판단이 틀렸다**(2026-08-13 단가표 전수 감사에서 잡았다). OpenAI 공식 표는 5.6 계열의
-- 캐시 쓰기를 **두 값**으로 싣는다:
--     gpt-5.6-sol    $6.25 / $12.50
--     gpt-5.6-terra  $2.50 / $5.00
--     gpt-5.6-luna   $0.25 / $0.50
-- 앞이 표준 구간, 뒤가 롱 구간이고 각각 `1.25 × 해당 구간 입력가` 다. 컬럼이 없으면 롱 구간의
-- 캐시 쓰기가 표준가로 계산돼 **과소**계상된다.
--
-- Google 은 여전히 해당 없다(쓰기 토큰 과금 자체가 없다 — cacheWriteMult 0).
-- Anthropic 도 해당 없다(4.6+ 는 1M 컨텍스트가 표준가라 계단이 없다).
-- 즉 이 컬럼이 값을 갖는 것은 **OpenAI 5.6 계열뿐**이고, 그쪽은 TTL 로 갈리지 않아
-- (5분·1시간 배수가 같은 1.25) 롱 몫에 어느 TTL 배수를 쓸지 모호하지 않다.
--
-- ⚠ 계약은 0036·0039 와 같다: 총량의 **부분집합**이고, DEFAULT 0 이 하위호환의 전부다.
--   불변식 `0 <= cache_create_long <= cache_create` 는 애플리케이션이 지킨다(intake 의 longNat).
--   CHECK 를 걸지 않는 이유도 같다 — 인테이크는 best-effort 경로라 보고가 통째로 거부되면 안 된다.
--
-- ⚠ 컬럼 추가라 RLS 를 새로 걸지 않는다. 인덱스도 만들지 않는다(SUM 대상이다).
--
-- sqlite 쪽 같은 컬럼은 go/internal/store/store.go 의 DDL·ensureColumn 이 소유한다(양 방언 동기).

ALTER TABLE usage_sessions ADD COLUMN IF NOT EXISTS cache_create_long bigint NOT NULL DEFAULT 0;
ALTER TABLE usage_series   ADD COLUMN IF NOT EXISTS cache_create_long bigint NOT NULL DEFAULT 0;
