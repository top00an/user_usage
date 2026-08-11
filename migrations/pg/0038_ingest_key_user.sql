-- 0038 — ingest_keys.username: 인제스트 키를 **사람에게 묶는다.**
--
-- 왜 필요한가: 0030 의 ingest_keys 는 (key_hash, org_id, …) 라 **누구의 키인지 모른다.** 한 org
--   키가 팀원 수만큼 복제되어 각자 디스크에 놓이므로, 사본을 가진 누구나 payload.user 에 남의
--   이름을 실어 보고할 수 있고 화면은 그것을 사실로 표시한다. 이 컬럼이 그 구멍을 닫는다 —
--   키에 username 이 있으면 서버가 payload 를 무시하고 **키 주인**으로 귀속한다.
--
--   귀속 우선순위(단일 출처는 go/internal/httpapi/usage.go 의 storeSessions 주석):
--     ① 키에 묶인 username   ← 있으면 무조건 이긴다("그 사용자의 키를 실제로 갖고 있음"의 증명)
--     ② machine_identity 매핑 ← 관리자가 뒤늦게 고친 값
--     ③ payload.user          ← 클라이언트 주장(최후)
--
-- ⚠ **nullable 이 하위호환의 전부다.** 기존 키는 NULL 로 남고, NULL 인 키의 보고는 종전대로
--   ②→③ 을 탄다. 여기에 NOT NULL 이나 DEFAULT 를 걸면 이미 배포된 모든 키의 귀속이 그날로
--   바뀐다 — 이 마이그레이션은 **기존 행을 한 줄도 건드리지 않는다.**
--
-- ⚠ RLS 를 만지지 않는다. orgs·ingest_keys 는 **tenant-scoped 가 아니다**(0030 의 주석이 단일
--   출처다): 아직 tenant 를 모르는 상태에서 조회되는 라우팅 계층이라 RLS 를 걸면 순환이 된다.
--   격리는 이 표가 돌려준 tenant_id 로 usage_* 에서 선다. 그래서 0037 처럼 FORCE 를 껐다 켜는
--   절차가 여기엔 없다.
--
-- ⚠ auth_users 로 FK 를 걸지 않는다. 두 가지 이유다:
--     · auth_users 의 PK 는 (tenant_id, username) 인데 ingest_keys 에는 tenant_id 컬럼이 없다
--       (위 RLS 주석 참조) — 참조 대상을 가리킬 수 없다.
--     · 계정을 지웠다고 과거 키의 귀속 기록까지 사라지면, 그 사람이 남긴 사용량의 출처를
--       되짚을 근거가 없어진다. 계정 삭제는 세션을 끊고(불변식 ④) 키는 관리자가 해지한다.
--
-- 잠금: ADD COLUMN(기본값 없음)은 PostgreSQL 11+ 에서 테이블 재작성 없이 카탈로그만 갱신한다.
--   ACCESS EXCLUSIVE 락을 잡지만 순간이다 — 0037 과 달리 인테이크가 오래 대기하지 않는다.
--
-- 멱등: IF NOT EXISTS 라 두 번 돌려도 같다.
--
-- sqlite 쪽 같은 일은 go/internal/org/org.go 의 Init 이 소유한다(양 방언 동기). 그쪽은
--   `CREATE TABLE IF NOT EXISTS` 가 **기존 표에 컬럼을 안 넣으므로** ensureColumn 으로 보강한다.
--
-- 되돌리기:
--   ALTER TABLE ingest_keys DROP COLUMN IF EXISTS username;
--   -- 되돌리면 결속이 사라지고 귀속은 ②→③ 으로 돌아간다(데이터 손실은 결속 정보뿐이다).

BEGIN;

ALTER TABLE ingest_keys ADD COLUMN IF NOT EXISTS username text;

-- 셀프서비스 목록("내 키만 보여 줘")이 이 인덱스를 탄다. 부분 인덱스인 이유: 묶이지 않은 키가
-- 다수인 기존 배포에서 NULL 행까지 색인할 이유가 없다.
CREATE INDEX IF NOT EXISTS ingest_keys_username
    ON ingest_keys (username)
    WHERE username IS NOT NULL;

COMMENT ON COLUMN ingest_keys.username IS
  '이 키가 묶인 사람. NULL 이면 org 공용(레거시) 키이고 귀속은 machine 매핑 → payload 를 탄다.';

COMMIT;
