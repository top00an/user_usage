package policy

import "testing"

// LOC 규칙의 유일한 정의를 못 박는 표다. 세 파서(Claude·Codex·Gemini)가 전부 이 함수를
// 부르므로, 여기 기대값이 곧 플랫폼 공통 규칙이다.
func TestLineCount(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int64
	}{
		{"빈 문자열은 줄이 없다", "", 0},
		{"개행 없는 한 줄", "a", 1},
		{"끝 개행 하나는 줄을 만들지 않는다", "a\n", 1},
		{"개행으로 나뉜 두 줄", "a\nb", 2},
		{"끝 개행이 붙은 두 줄", "a\nb\n", 2},
		{"의도적 빈 줄은 살아 있다", "a\nb\n\n", 3},
		{"개행 하나는 빈 줄 하나", "\n", 1},
		{"개행 둘은 빈 줄 둘", "\n\n", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := LineCount(c.in); got != c.want {
				t.Fatalf("LineCount(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

// 끝 개행은 **한 개만** 뗀다. 여러 개를 떼면 사람이 일부러 넣은 마지막 빈 줄이 사라져
// 같은 파일이 편집 방향에 따라 다른 LOC 로 잡힌다.
func TestLineCountStripsOnlyOneTrailingNewline(t *testing.T) {
	if got, want := LineCount("a\n\n\n"), int64(3); got != want {
		t.Fatalf(`LineCount("a\n\n\n") = %d, want %d`, got, want)
	}
}
