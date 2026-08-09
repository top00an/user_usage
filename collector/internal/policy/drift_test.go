package policy

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// 계약 드리프트 감시.
//
// policy 의 상한·축 목록은 서버 `go/internal/intake/intake.go` 의 값과 **같아야 한다.**
// 별도 모듈이라 서버 패키지를 import 할 수 없어(Go 가 모듈 밖 internal 을 막는다) 값을 여기
// 다시 적었다 — 갈라지면 수집기가 서버가 버릴 행을 만들어 보낸다(과소집계인데 조용하다).
//
// 그래서 이 테스트가 **서버 소스를 직접 읽어** 값이 갈라졌는지 본다. 서버 소스를 못 찾으면
// (수집기만 따로 배포된 환경) 건너뛴다 — 그건 실패가 아니라 "레포 밖에서 도는 중"이다.
const serverIntakePath = "../../../go/internal/intake/intake.go"

func TestConstantsMatchServer(t *testing.T) {
	raw, err := os.ReadFile(serverIntakePath)
	if err != nil {
		t.Skipf("서버 소스를 찾지 못해 드리프트 검사를 건너뛴다(%s): %v", serverIntakePath, err)
	}
	src := string(raw)

	ints := map[string]int{
		"KeyMax":                KeyMax,
		"KeywordMax":            KeywordMax,
		"PerKindMax":            PerKindMax,
		"MaxSessions":           MaxSessions,
		"MaxSeriesPerSession":   MaxSeriesPerSession,
		"MaxCountersPerSession": MaxCountersPerSession,
	}
	for name, local := range ints {
		got, err := serverConstInt(src, name)
		if err != nil {
			t.Errorf("서버에서 %s 를 읽지 못했다: %v", name, err)
			continue
		}
		if got != local {
			t.Errorf("드리프트: %s — policy=%d, 서버=%d (둘 중 하나를 고칠 때 반드시 함께 고친다)", name, local, got)
		}
	}

	serverKinds, err := serverCounterKinds(src)
	if err != nil {
		t.Fatalf("서버 CounterKinds 를 읽지 못했다: %v", err)
	}
	if strings.Join(serverKinds, ",") != strings.Join(CounterKinds, ",") {
		t.Errorf("드리프트: CounterKinds — policy=%v, 서버=%v", CounterKinds, serverKinds)
	}
}

func serverConstInt(src, name string) (int, error) {
	re := regexp.MustCompile(name + `\s*=\s*(\d+)`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		return 0, fmt.Errorf("선언을 찾지 못함")
	}
	return strconv.Atoi(m[1])
}

func serverCounterKinds(src string) ([]string, error) {
	re := regexp.MustCompile(`CounterKinds\s*=\s*\[\]string\{([^}]*)\}`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		return nil, fmt.Errorf("CounterKinds 선언을 찾지 못함")
	}
	var out []string
	for _, tok := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(m[1], -1) {
		out = append(out, tok[1])
	}
	return out, nil
}
