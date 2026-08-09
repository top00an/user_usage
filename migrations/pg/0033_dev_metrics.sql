-- usage_sessions 개발 지표 컬럼 — LOC(추가·삭제 줄 수)와 편집 결정(accept/reject).
--
-- Edit/Write/MultiEdit tool_use 에서 **줄 수·횟수만** 집계한다(코드 내용은 저장하지 않는다 —
-- 집계-온리 정책 유지). 새 조회 엔드포인트 /api/usage/dev 만 이 컬럼을 읽으므로 기존 골든
-- 계약(44개)에는 닿지 않는다.
--
-- ⚠ 컬럼 추가다(새 테이블 아님) → RLS 정책을 새로 걸지 않는다(usage_sessions 에 이미 있다).
-- ⚠ integer · NULL 허용. 구버전 수집기는 안 보내고, 그때는 NULL(=모름)로 남는다.

ALTER TABLE usage_sessions ADD COLUMN IF NOT EXISTS lines_added integer;
ALTER TABLE usage_sessions ADD COLUMN IF NOT EXISTS lines_removed integer;
ALTER TABLE usage_sessions ADD COLUMN IF NOT EXISTS edits_accepted integer;
ALTER TABLE usage_sessions ADD COLUMN IF NOT EXISTS edits_rejected integer;
