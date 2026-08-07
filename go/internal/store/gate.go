package store

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"time"
)

/*
 * ── 게이트로 보이는 선두 실행파일 ─────────────────────────────────────
 *
 * 클라이언트 수집기가 "검증 게이트"로 인정하는 명령들의 **선두 실행파일**만 모은 목록이다.
 * 이 축(usage_counters kind='bash')에는 설계상 **인자가 없다** — 그래서 `npm` 한 건이
 * `npm test`(게이트)인지 `npm install`(아님)인지 여기서는 **가릴 수 없다.** 그러니 결론이 아니라
 * **후보**라고만 말한다. 인자를 저장하는 쪽으로 바꾸는 선택지는 두지 않는다 — 이 축의
 * 프라이버시 계약(집계만·인자 없음)이 그 값보다 크다.
 *
 * 왜 서버에 이 목록이 필요한가: "동기화는 되는데 학습 보고가 없는 PC" 앞에서 사람이 물을 다음
 * 질문은 언제나 **"배선이 고장 났나, 아니면 게이트를 안 돌리나"** 다. 그 둘은 대응이 완전히
 * 다르다(전자는 우리가 고칠 결함, 후자는 그 PC 의 작업 방식).
 *
 * ── 확실 / 불확실을 가른다 ──────────────────────────────────────────
 * 첫 판에서 둘을 한 통에 넣었더니 라이브에서 이렇게 떴다:
 *   `게이트로 보이는 명령 168건 · 보고 0`  (내역: python.exe 122 · python 36 · node 5)
 * 빨간 배지는 "우리가 고칠 결함"이라는 뜻인데, 저 셋은 **인자 없이는 게이트인지 알 수 없는**
 * 인터프리터다(`python train.py` 도 python 이다). 근거 없는 단정이 경고를 켠 것이다.
 *
 *   CERTAIN   그 이름만으로 게이트다(pytest·jest·eslint·ruff …) → 빨간 판정의 유일한 근거
 *   AMBIGUOUS 하위명령·플래그가 있어야 게이트다(python·npm·go·make …) → **세되, 판정하지 않는다**
 *
 * 이 목록이 수집기 쪽 판정 규칙과 어긋나면 화면이 틀린 말을 한다 — 한쪽만 고치지 않는다.
 */

// GateLeadCertain 은 이름만으로 게이트인 러너다 — "게이트는 도는데 보고가 0" 판정의 유일한 근거.
var GateLeadCertain = []string{
	"pytest", "py.test", "tox", "nose", "nose2",
	"jest", "vitest", "mocha", "ava", "cypress", "playwright",
	"eslint", "biome", "tsc",
	"ruff", "mypy", "flake8", "pylint", "pyright",
	"rspec", "phpunit", "Invoke-Pester",
}

// GateLeadAmbiguous 는 하위명령·플래그가 있어야 게이트인 것들이다.
// `npm`(test vs install) · `python`(-m pytest vs script.py) · `go`(test vs run) ·
// `black`/`prettier`(--check 여야 게이트) 처럼 **인자가 판정을 가르는데 이 축엔 인자가 없다.**
// 그래서 세기만 하고 판정에는 쓰지 않는다.
var GateLeadAmbiguous = []string{
	"npm", "pnpm", "yarn", "bun", "npx", "bunx", "node",
	"python", "python3", "py", "uv", "poetry", "pipenv", "rye",
	"go", "cargo", "rails", "gradle", "mvn", "dotnet", "make",
	"black", "prettier", "coverage",
}

// GateLeadKeys 는 후보 전체(확실 + 불확실)다.
var GateLeadKeys = append(append([]string{}, GateLeadCertain...), GateLeadAmbiguous...)

var certainSet = toLowerSet(GateLeadCertain)
var gateLeadSet = toLowerSet(GateLeadKeys)

func toLowerSet(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[strings.ToLower(x)] = true
	}
	return m
}

// 윈도우 확장자는 접어서 본다(`npm.cmd`·`python.exe` 는 같은 실행파일이다 — 수집기와 같은 규율).
var winExtRe = regexp.MustCompile(`\.(?:exe|cmd|bat|ps1|com)$`)

func normLeadKey(key string) string {
	return winExtRe.ReplaceAllString(strings.ToLower(key), "")
}

// IsGateLeadKey 는 게이트 **후보**인지 본다(확실 + 불확실).
func IsGateLeadKey(key string) bool { return gateLeadSet[normLeadKey(key)] }

// IsCertainGateLeadKey 는 이름만으로 게이트인지 본다 — 빨간 판정의 유일한 근거.
func IsCertainGateLeadKey(key string) bool { return certainSet[normLeadKey(key)] }

/*
 * ActivityWindowDays — 활동 대조의 기본 창은 **14일**이다.
 *
 * 왜 창이 필요한가(라이브에서 배운 것): 전 기간 누계로 세면 화면이 **고쳐진 뒤에도 옛 데이터로
 * 빨간불을 켠다.** 하네스 훅 수정이 그 PC 에 10:07 에 도착했는데, 표는 그 전 몇 주치 명령까지
 * 합쳐 "게이트 168건인데 보고 0" 이라고 말했다 — 판정의 근거가 판정 대상 기간 밖에 있었다.
 * "지금도 그런가"를 묻는 카드에는 최근 창이 맞다. 14일인 이유: 주말·휴가 한 번을 흡수하면서도
 * 몇 주 전 습관이 오늘의 판정을 오염시키지 않는 가장 짧은 길이.
 */
const ActivityWindowDays = 14

// cutoffDayBefore 는 now 에서 days 일 전 날짜(YYYY-MM-DD)다.
func cutoffDayBefore(days int, now time.Time) string {
	if days < 1 {
		days = ActivityWindowDays
	}
	return now.UTC().Add(-time.Duration(days) * 24 * time.Hour).Format("2006-01-02")
}

/*
 * MachineActivity — 머신별 활동. "그 PC 에서 Claude 가 돌기는 하는가, 게이트를 부르기는 하는가"
 * (최근 창 기준). "보고가 안 온다" 진단의 대조축이다.
 *
 * 세 상태를 가른다 — 이 구분이 이 함수의 존재 이유다:
 *
 *	· 머신 키가 아예 없다           → 그 PC 에서 **사용량 보고조차 오지 않는다**(수집기 설치를 의심)
 *	· Sessions>0 · CertainTotal=0  → 확실한 러너 호출이 창 안에 없다(작업 방식일 수 있다 — 단정 금지)
 *	· Sessions>0 · CertainTotal>0  → 러너는 도는데 **학습 보고가 없다** = 우리가 고칠 결함
 *
 * now 는 주입한다 — 창 경계가 걸린 판정이라 테스트가 시계를 고정할 수 있어야 한다.
 */
func MachineActivity(ctx context.Context, windowDays int, now time.Time) (map[string]*Activity, error) {
	d, err := conn()
	if err != nil {
		return nil, err
	}
	days := windowDays
	if days < 1 {
		days = ActivityWindowDays
	}
	if now.IsZero() {
		now = clock()
	}
	sinceDay := cutoffDayBefore(days, now)

	sess, err := d.Query(ctx,
		"SELECT COALESCE(NULLIF(machine,''),'') m, COUNT(*) c, MAX(started_at) last_at"+
			" FROM usage_sessions WHERE substr(started_at,1,10) >= ? GROUP BY 1", sinceDay)
	if err != nil {
		return nil, err
	}
	/*
	 * day 가 비어 있는 행(구버전 수집기)은 **창 밖으로 친다.** 시각을 모르는 것을 최근으로 세면
	 * 옛 데이터가 오늘의 판정을 다시 오염시킨다 — 그게 이 창을 만든 이유다.
	 */
	bash, err := d.Query(ctx,
		"SELECT COALESCE(NULLIF(machine,''),'') m, key, SUM(count) c"+
			" FROM usage_counters WHERE kind='bash' AND day >= ? GROUP BY 1, key", sinceDay)
	if err != nil {
		return nil, err
	}

	out := map[string]*Activity{}
	at := func(m string) *Activity {
		if e, ok := out[m]; ok {
			return e
		}
		e := &Activity{WindowDays: days, SinceDay: sinceDay, GateKeys: []GateKey{}, CertainKeys: []KeyCount{}}
		out[m] = e
		return e
	}
	for _, r := range sess {
		m := r.Str("m")
		if m == "" {
			continue // 머신을 모르는 보고는 대조에 쓸 수 없다(빈 키로 뭉개지 않는다)
		}
		e := at(m)
		e.Sessions = int(r.Int("c"))
		e.LastSessionAt = r.Str("last_at")
	}
	for _, r := range bash {
		m := r.Str("m")
		if m == "" {
			continue
		}
		e := at(m)
		key := r.Str("key")
		n := r.Int("c")
		e.Bash += n
		if !IsGateLeadKey(key) {
			continue
		}
		certain := IsCertainGateLeadKey(key)
		e.GateTotal += n
		e.GateKeys = append(e.GateKeys, GateKey{Key: key, Count: n, Certain: certain})
		if certain {
			e.CertainTotal += n
			e.CertainKeys = append(e.CertainKeys, KeyCount{Key: key, Count: n})
		}
	}
	// 정렬은 결정론이다(화면이 흔들리면 안 된다) — 횟수 내림차순, 동률은 코드포인트 오름차순.
	for _, e := range out {
		sort.SliceStable(e.GateKeys, func(i, j int) bool {
			if e.GateKeys[i].Count != e.GateKeys[j].Count {
				return e.GateKeys[i].Count > e.GateKeys[j].Count
			}
			return e.GateKeys[i].Key < e.GateKeys[j].Key
		})
		if len(e.GateKeys) > 8 {
			e.GateKeys = e.GateKeys[:8]
		}
		sort.SliceStable(e.CertainKeys, func(i, j int) bool {
			if e.CertainKeys[i].Count != e.CertainKeys[j].Count {
				return e.CertainKeys[i].Count > e.CertainKeys[j].Count
			}
			return e.CertainKeys[i].Key < e.CertainKeys[j].Key
		})
		if len(e.CertainKeys) > 8 {
			e.CertainKeys = e.CertainKeys[:8]
		}
	}
	return out, nil
}
