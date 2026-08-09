// Package state 는 "어디까지 보냈는가"를 파일에 기록해 증분 실행을 가능하게 한다.
//
// ── 왜 세션 단위인가(파일 단위가 아니라) ───────────────────────────────────
// 세션이 재개되면 같은 sessionId 가 여러 파일에 흩어질 수 있다. 그런데 서버는 절대값으로
// 덮으므로, "바뀐 파일만" 다시 파싱해 보내면 그 세션의 **일부만** 반영된 값이 전체를 덮어
// 과소집계가 된다. 그래서 증분 판정은 파일 하나가 아니라 **세션에 속한 파일 중 하나라도
// 바뀌었는가**로 한다 — 바뀌었으면 그 세션의 모든 파일을 다시 읽어 온전한 절대값을 낸다.
//
// 지문은 (크기, 수정시각ns) 다. 내용 해시가 아니라 stat 만 보는 이유: 트랜스크립트는 append
// 전용이라 크기가 바뀌면 내용도 바뀐 것이고, stat 은 파일을 열지 않아 수천 개도 즉시 훑는다.
// 오탐(안 바뀐 걸 바뀌었다고 보는 것)은 재전송으로 이어질 뿐이고 그건 멱등이라 무해하다.
//
// 표준 라이브러리 말고 아무것도 import 하지 않는다.
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// fingerprint 는 파일 하나의 지문이다.
type fingerprint struct {
	Size    int64 `json:"size"`
	ModNano int64 `json:"modNano"`
}

// State 는 체크포인트다. 키는 파일 절대경로다.
type State struct {
	path  string
	Files map[string]fingerprint `json:"files"`
}

// Load 는 체크포인트를 읽는다. 파일이 없으면 빈 상태를 돌려준다(오류가 아니다 — 첫 실행이다).
// 깨진 체크포인트도 빈 상태로 시작한다: 잘못된 증분보다 한 번의 전량 재전송이 안전하다
// (재전송은 멱등이라 무해하다).
func Load(path string) (*State, error) {
	s := &State{path: path, Files: map[string]fingerprint{}}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	_ = json.Unmarshal(raw, s) // 깨졌으면 빈 상태 유지
	if s.Files == nil {
		s.Files = map[string]fingerprint{}
	}
	return s, nil
}

// Save 는 체크포인트를 원자적으로 쓴다(임시 파일 → rename). 중간에 죽어도 반쪽짜리
// 체크포인트가 남지 않게 — 반쪽 체크포인트는 다음 실행의 증분을 조용히 틀리게 만든다.
func (s *State) Save() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(struct {
		Files map[string]fingerprint `json:"files"`
	}{s.Files}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Changed 는 그 파일이 마지막 기록 이후 바뀌었는지 본다. stat 실패(파일 삭제 등)는
// "바뀜"으로 취급한다 — 호출부가 판단하게 두는 것보다 여기서 안전한 쪽으로 접는다.
func (s *State) Changed(path string, info os.FileInfo) bool {
	fp, ok := s.Files[path]
	if !ok {
		return true
	}
	return fp.Size != info.Size() || fp.ModNano != info.ModTime().UnixNano()
}

// Mark 는 그 파일을 현재 지문으로 기록한다(전송 성공 후 호출).
func (s *State) Mark(path string, info os.FileInfo) {
	s.Files[path] = fingerprint{Size: info.Size(), ModNano: info.ModTime().UnixNano()}
}
