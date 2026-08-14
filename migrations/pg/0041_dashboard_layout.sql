-- 0041 — user_dashboard_layout: 대시보드 캔버스 레이아웃을 **사람에게 묶어** 서버에 둔다.
--
-- 무엇을 저장하는가: 관측 탭 대시보드의 패널 배치(12열 캔버스의 x·y·w·h, 단위는 칸이다).
-- 정본 좌표계는 web/lib/dashLayout.ts 이고 서버는 그 값을 검증한 뒤 **그대로** 담는다.
--
-- 왜 서버인가: 지금까지 이 배치는 브라우저 localStorage 에만 있었다. 즉 PC 를 바꾸면 배치가
-- 사라지고, 같은 사람이 두 대에서 서로 다른 화면을 본다. "내 대시보드"는 사람에 묶인 사실이지
-- 브라우저에 묶인 사실이 아니다.
--
-- ⚠ tenant-scoped 다(auth_users·member_tokens 규율 그대로: tenant_id DEFAULT current_setting ·
--   FORCE RLS · tenant_isolation 정책). 사람 계정이 그 org 의 것이므로 그 사람의 화면 설정도
--   같은 경계 안이다. PK 에 tenant_id 가 **먼저** 들어가는 것도 그 표들과 같다 —
--   (tenant_id, username) 이 아니라 username 단독 PK 로 두면 서로 다른 org 의 동명이인이
--   한 행을 덮어쓴다.
--   (0038/0039/0040 이 RLS 를 안 건드린 것은 그쪽이 **컬럼 추가**여서다. 여기는 새 표라
--    0034_auth.sql 이 새 표에 하던 절차를 그대로 따른다.)
--
-- ⚠ updated_at 은 timestamptz 가 아니라 **text(RFC3339 UTC)** 다. 이 레포의 시간 컬럼 관례이고
--   (orgs·member_tokens·auth_users·auth_sessions 전부 text), 0034 가 그 근거를 이렇게 적었다:
--   "`?`→`$n` 치환과 방언 무관 SQL 을 유지하려면 두 방언이 같은 타입이어야 한다."
--   여기서는 그 이유가 하나 더 있다 — 드라이버가 타입을 다르게 접는다:
--     · pg(pgx)  timestamptz 는 **바이너리**로 오가서 파라미터가 Go time.Time 이어야 하고,
--                읽을 때도 time.Time 으로 돌아온다.
--     · sqlite(modernc) time.Time 파라미터를 `t.String()`("2026-08-14 12:00:00 +0000 UTC")로
--                적는다 — RFC3339 가 아니다.
--   두 방언이 같은 컬럼에 서로 다른 문자열을 남기면, 응답의 updatedAt 이 배포마다 달라지는데
--   **오류가 아니라 다른 문자열**로만 보인다. text 로 두면 저장 계층이 한 벌의 RFC3339 를 쓴다.
--   RFC3339 Zulu 는 사전식 정렬이 곧 시간 정렬이라 정렬·비교도 성립한다(0034 와 같은 근거).
--   ⚠ 계약 문서(CONTRACT §2)는 이 컬럼을 timestamptz 로 적었다 — 그 한 줄과 다르다. 응답
--     shape(`updatedAt`: RFC3339 문자열)은 그대로이므로 관측 가능한 계약은 바뀌지 않는다.
--
-- ⚠ layout 은 jsonb 다(계약 그대로). 검증을 통과한 값만 들어온다 — 개수·id·좌표 범위·정수
--   여부를 httpapi/prefs.go 가 막는다. **jsonb 에 한 번 들어간 쓰레기는 되돌릴 방법이 없다.**
--   CHECK 제약으로 그 규칙을 옮기지 않는 이유: 규칙이 12열 좌표계(코드 상수)에 묶여 있어서
--   열 수가 바뀌는 날 DB 제약과 코드가 갈린다. 검증의 단일 출처는 한 곳이라야 한다.
--
-- ⚠ 인덱스를 만들지 않는다. 조회는 언제나 PK 로 한 행씩이다(내 레이아웃 하나).
--
-- ⚠ auth_users 로 FK 를 걸지 않는다 — 0038 과 같은 판단이다. 계정을 지우면 이 행은 남지만,
--   남은 것은 좌표 몇 개일 뿐이고(개인정보가 아니다) 같은 이름으로 계정을 다시 만들면 그
--   사람의 배치가 되돌아온다. 반대로 FK 를 걸면 계정 삭제 경로(users.go DeleteUser)가 이 표를
--   모르는 채로 실패하기 시작한다.
--
-- 멱등: IF NOT EXISTS + DROP POLICY IF EXISTS 라 두 번 돌려도 같다.
--
-- sqlite 쪽 같은 표는 go/internal/store/prefs.go 의 ensureDashboardLayout 이 소유한다
-- (양 방언 동기 — 다른 표들이 store.Init 의 DDL 에 있는 것과 소유자만 다르고 규율은 같다).
--
-- 되돌리기:
--   DROP TABLE IF EXISTS user_dashboard_layout;
--   -- 되돌리면 저장된 배치가 사라지고 화면은 기본 레이아웃으로 돌아간다(데이터 손실은 배치뿐).

CREATE TABLE IF NOT EXISTS user_dashboard_layout (
  tenant_id  text  NOT NULL DEFAULT COALESCE(current_setting('app.tenant_id', true), 'default'),
  username   text  NOT NULL,
  layout     jsonb NOT NULL,
  updated_at text  NOT NULL,
  PRIMARY KEY (tenant_id, username)
);

DO $$
BEGIN
  EXECUTE 'ALTER TABLE user_dashboard_layout ENABLE ROW LEVEL SECURITY';
  EXECUTE 'ALTER TABLE user_dashboard_layout FORCE ROW LEVEL SECURITY';
  EXECUTE 'DROP POLICY IF EXISTS tenant_isolation ON user_dashboard_layout';
  EXECUTE 'CREATE POLICY tenant_isolation ON user_dashboard_layout '
    || 'USING (tenant_id = current_setting(''app.tenant_id'', true)) '
    || 'WITH CHECK (tenant_id = current_setting(''app.tenant_id'', true))';
END $$;

COMMENT ON TABLE user_dashboard_layout IS
  '유저별 대시보드 캔버스 배치(12열, 단위는 칸). 정본 좌표계는 web/lib/dashLayout.ts.';
