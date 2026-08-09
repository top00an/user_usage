package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string) os.FileInfo {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// 첫 실행(체크포인트 없음)에서는 모든 파일이 "바뀜"이다.
func TestChangedOnFirstRun(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a.jsonl")
	info := writeFile(t, f, "x")

	st, err := Load(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !st.Changed(f, info) {
		t.Fatal("첫 실행인데 안 바뀜으로 봤다")
	}
}

// Mark → Save → Load 왕복 뒤, 같은 파일은 "안 바뀜"이다.
func TestMarkThenUnchangedAfterRoundtrip(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a.jsonl")
	info := writeFile(t, f, "hello")
	statePath := filepath.Join(dir, "state.json")

	st, _ := Load(statePath)
	st.Mark(f, info)
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}

	st2, err := Load(statePath)
	if err != nil {
		t.Fatal(err)
	}
	info2, _ := os.Stat(f)
	if st2.Changed(f, info2) {
		t.Fatal("기록 후 왕복했는데 바뀜으로 봤다")
	}
}

// 내용이 바뀌면(크기·수정시각 변화) "바뀜"으로 잡힌다.
func TestChangedAfterModify(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a.jsonl")
	info := writeFile(t, f, "hello")
	statePath := filepath.Join(dir, "state.json")

	st, _ := Load(statePath)
	st.Mark(f, info)
	_ = st.Save()

	time.Sleep(10 * time.Millisecond)
	info2 := writeFile(t, f, "hello world longer") // 크기 변화

	st2, _ := Load(statePath)
	if !st2.Changed(f, info2) {
		t.Fatal("파일이 커졌는데 안 바뀜으로 봤다")
	}
}

// 깨진 체크포인트는 빈 상태로 시작한다(전량 재전송 — 멱등이라 무해).
func TestCorruptStateStartsEmpty(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := os.WriteFile(statePath, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := Load(statePath)
	if err != nil {
		t.Fatalf("깨진 체크포인트에 error 를 냈다: %v", err)
	}
	f := filepath.Join(dir, "a.jsonl")
	info := writeFile(t, f, "x")
	if !st.Changed(f, info) {
		t.Fatal("깨진 체크포인트인데 안 바뀜으로 봤다")
	}
}
