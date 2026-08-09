package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

/*
 * 경량 마이그레이션 러너 — 양 방언 공통. 현행 lib/db/migrate.js 를 그대로 옮긴다.
 *
 *   · schema_migrations(version bigint PK, applied_at text) 버전 테이블
 *   · <dir>/NNNN_*.sql 을 버전 순 적용, 적용본은 기록·스킵(멱등)
 *   · 각 파일은 BEGIN … <body> … INSERT version … COMMIT 을 한 번의 원시 실행으로 원자 적용
 *
 * ⚠ **되돌리기 어려운 작업이라 자동 실행 경로에 올리지 않는다.** 운영자가 명시적으로 돌린다.
 * pg 스키마의 단일 출처는 migrations/pg/*.sql 이고 이 러너가 그 파일들을 읽는다 — sqlite 는
 * store.Init 의 DDL 이 소유한다(방언마다 소유자가 하나다).
 */

// rawExecer 는 자리표시자 치환·테넌트 주입 없이 멀티 스테이트먼트를 실행한다.
// 두 드라이버가 모두 구현한다.
type rawExecer interface {
	ExecRaw(ctx context.Context, sqlText string) error
}

// MigrateResult 는 한 번의 실행 결과다.
type MigrateResult struct {
	Dialect Dialect
	Applied []int64 // 이번에 적용된 버전들(이미 적용된 것은 들어가지 않는다)
	Total   int     // 디렉터리에서 발견한 파일 수
}

var migrationFileRe = regexp.MustCompile(`^\d+.*\.sql$`)

// MigrationsDir 는 레포 루트 기준 방언별 마이그레이션 디렉터리다.
func MigrationsDir(root string, d Dialect) string {
	name := string(d)
	if d == DialectPostgres {
		name = "pg" // 디렉터리 이름은 현행 레포를 따른다(migrations/pg)
	}
	return filepath.Join(root, "migrations", name)
}

// Migrate 는 dir 의 마이그레이션을 버전 순으로 적용한다.
func Migrate(ctx context.Context, d DB, dir string) (MigrateResult, error) {
	res := MigrateResult{Dialect: d.Dialect(), Applied: []int64{}}

	raw, ok := d.(rawExecer)
	if !ok {
		return res, fmt.Errorf("migrate: 이 백엔드는 원시 실행을 지원하지 않는다")
	}

	if err := raw.ExecRaw(ctx,
		"CREATE TABLE IF NOT EXISTS schema_migrations (version bigint PRIMARY KEY, applied_at text)"); err != nil {
		return res, fmt.Errorf("migrate: 버전 테이블 생성 실패: %w", err)
	}

	rows, err := d.Query(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return res, fmt.Errorf("migrate: 적용본 조회 실패: %w", err)
	}
	applied := make(map[int64]bool, len(rows))
	for _, r := range rows {
		applied[r.Int("version")] = true
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return res, nil // 디렉터리가 없으면 적용할 것도 없다
		}
		return res, err
	}
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && migrationFileRe.MatchString(e.Name()) {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	res.Total = len(files)

	for _, f := range files {
		version := leadingInt(f)
		if version < 0 || applied[version] {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			return res, fmt.Errorf("migrate: %s 읽기 실패: %w", f, err)
		}
		text := strings.TrimSpace(string(body))
		if text != "" && !strings.HasSuffix(text, ";") {
			text += ";"
		}
		// applied_at 은 내부 생성값이라 주입 표면이 없다(자리표시자를 쓸 수 없는 원시 경로다).
		at := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
		script := strings.Join([]string{
			"BEGIN;",
			text,
			fmt.Sprintf("INSERT INTO schema_migrations(version, applied_at) VALUES(%d, '%s');", version, at),
			"COMMIT;",
		}, "\n")

		if err := raw.ExecRaw(ctx, script); err != nil {
			_ = raw.ExecRaw(ctx, "ROLLBACK;") // 이미 롤백됐을 수 있다 — 실패는 무시한다
			return res, fmt.Errorf("migrate: %s 실패: %w", f, err)
		}
		res.Applied = append(res.Applied, version)
	}
	return res, nil
}

// leadingInt 는 파일명 앞의 숫자를 버전으로 읽는다("0014_usage.sql" → 14). 없으면 -1.
func leadingInt(name string) int64 {
	i := 0
	for i < len(name) && name[i] >= '0' && name[i] <= '9' {
		i++
	}
	if i == 0 {
		return -1
	}
	v, err := strconv.ParseInt(name[:i], 10, 64)
	if err != nil {
		return -1
	}
	return v
}
