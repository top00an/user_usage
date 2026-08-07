package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeMigration(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateAppliesInVersionOrderAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	d := openTemp(t)
	dir := t.TempDir()

	// 파일명 순서가 아니라 **버전 순**이다(2 < 10).
	writeMigration(t, dir, "0002_first.sql", "CREATE TABLE a(x INTEGER);")
	writeMigration(t, dir, "0010_second.sql", "CREATE TABLE b(y INTEGER);")
	writeMigration(t, dir, "notes.txt", "무시돼야 한다")

	res, err := Migrate(ctx, d, dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 2 {
		t.Fatalf("sql 파일만 세야 한다: %+v", res)
	}
	if len(res.Applied) != 2 || res.Applied[0] != 2 || res.Applied[1] != 10 {
		t.Fatalf("버전 순이 아니다: %+v", res.Applied)
	}

	// 두 번째 실행은 아무것도 적용하지 않는다 — 적용본을 기록해 두었기 때문이다.
	res2, err := Migrate(ctx, d, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Applied) != 0 {
		t.Fatalf("이미 적용된 것을 다시 돌렸다: %+v", res2.Applied)
	}
}

// 실패한 마이그레이션은 기록되지 않는다 — 기록되면 다음 실행이 그 파일을 건너뛰어
// 스키마가 반쯤 적용된 채로 굳는다.
func TestMigrateFailureIsNotRecorded(t *testing.T) {
	ctx := context.Background()
	d := openTemp(t)
	dir := t.TempDir()
	writeMigration(t, dir, "0001_bad.sql", "CREATE TABLE ok(x INTEGER); THIS IS NOT SQL;")

	if _, err := Migrate(ctx, d, dir); err == nil {
		t.Fatal("깨진 마이그레이션이 통과했다")
	}
	rows, err := d.Query(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("실패한 버전이 기록됐다: %v", rows)
	}
}

func TestMigrateMissingDirIsNotAnError(t *testing.T) {
	ctx := context.Background()
	d := openTemp(t)
	res, err := Migrate(ctx, d, filepath.Join(t.TempDir(), "없는디렉터리"))
	if err != nil {
		t.Fatalf("디렉터리가 없으면 적용할 것도 없다: %v", err)
	}
	if res.Total != 0 || len(res.Applied) != 0 {
		t.Fatalf("%+v", res)
	}
}

func TestMigrationsDirUsesPgForPostgres(t *testing.T) {
	// pg 스키마의 단일 출처는 migrations/pg/*.sql 이다(디렉터리 이름을 바꾸면 러너가 못 찾는다).
	if got := MigrationsDir("/repo", DialectPostgres); got != filepath.Join("/repo", "migrations", "pg") {
		t.Fatalf("got %q", got)
	}
	if got := MigrationsDir("/repo", DialectSQLite); got != filepath.Join("/repo", "migrations", "sqlite") {
		t.Fatalf("got %q", got)
	}
}

func TestLeadingInt(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
	}{{"0014_usage.sql", 14}, {"1_x.sql", 1}, {"x.sql", -1}, {"0026_usage_no_ts_turns.sql", 26}} {
		if got := leadingInt(tc.in); got != tc.want {
			t.Fatalf("leadingInt(%q) want %d, got %d", tc.in, tc.want, got)
		}
	}
}
