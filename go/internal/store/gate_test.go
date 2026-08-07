package store

import (
	"testing"
)

/*
 * 게이트 판정 — **확실과 불확실을 가른다.**
 *
 * 빨간 배지는 "우리가 고칠 결함"이라는 뜻이다. 인자 없이는 게이트인지 알 수 없는 인터프리터를
 * 그 근거로 쓰면 근거 없는 단정이 경고를 켠다(라이브에서 실제로 일어났다:
 * "게이트 168건 · 보고 0" 의 내역이 python.exe 122 · python 36 · node 5 였다).
 */
func TestGateLeadClassification(t *testing.T) {
	for _, tc := range []struct {
		key           string
		gate, certain bool
	}{
		{"pytest", true, true},
		{"jest", true, true},
		{"eslint", true, true},
		{"Invoke-Pester", true, true},
		{"npm", true, false},    // npm test 인지 npm install 인지 여기서는 못 가린다
		{"python", true, false}, // python train.py 도 python 이다
		{"node", true, false},
		{"go", true, false},
		{"git", false, false},
		{"ls", false, false},
	} {
		if got := IsGateLeadKey(tc.key); got != tc.gate {
			t.Errorf("IsGateLeadKey(%q) want %v, got %v", tc.key, tc.gate, got)
		}
		if got := IsCertainGateLeadKey(tc.key); got != tc.certain {
			t.Errorf("IsCertainGateLeadKey(%q) want %v, got %v", tc.key, tc.certain, got)
		}
	}
}

// 윈도우 확장자는 접어서 본다 — `npm.cmd`·`python.exe` 는 같은 실행파일이다.
func TestGateLeadFoldsWindowsExtensionsAndCase(t *testing.T) {
	for _, k := range []string{"pytest.exe", "PyTest", "PYTEST.EXE", "pytest.cmd", "pytest.bat", "pytest.ps1"} {
		if !IsCertainGateLeadKey(k) {
			t.Errorf("%q 가 확실 게이트로 안 잡혔다", k)
		}
	}
	if IsCertainGateLeadKey("pytest.py") {
		t.Error("스크립트 파일명을 실행파일로 접었다")
	}
	if !IsGateLeadKey("PYTHON.EXE") || IsCertainGateLeadKey("PYTHON.EXE") {
		t.Error("불확실 러너의 확장자/대소문자 접기가 틀렸다")
	}
}

/*
 * MachineActivity 는 **최근 창**으로만 판정한다.
 *
 * 전 기간 누계로 세면 화면이 고쳐진 뒤에도 옛 데이터로 빨간불을 켠다 — 판정의 근거가 판정
 * 대상 기간 밖에 있었던 실제 사고가 이 창을 만든 이유다.
 */
func TestMachineActivityWindowExcludesOldRows(t *testing.T) {
	ctx := fresh(t)
	now := at(t, "2026-08-03T00:00:00Z")

	// 창 안(14일): 2026-07-25
	mustSession(t, ctx, SessionInput{SessionID: "s-in", Machine: "pc-1", Username: "u",
		StartedAt: "2026-07-25T09:00:00.000Z", Output: 1})
	mustCounters(t, ctx, CountersInput{SessionID: "s-in", Machine: "pc-1", Username: "u",
		StartedAt: "2026-07-25T09:00:00.000Z",
		Rows:      []CounterRow{{Kind: "bash", Key: "pytest", Count: 5}, {Kind: "bash", Key: "git", Count: 3}}})

	// 창 밖: 2026-06-01
	mustSession(t, ctx, SessionInput{SessionID: "s-out", Machine: "pc-1", Username: "u",
		StartedAt: "2026-06-01T09:00:00.000Z", Output: 1})
	mustCounters(t, ctx, CountersInput{SessionID: "s-out", Machine: "pc-1", Username: "u",
		StartedAt: "2026-06-01T09:00:00.000Z",
		Rows:      []CounterRow{{Kind: "bash", Key: "pytest", Count: 100}}})

	act, err := MachineActivity(ctx, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	e := act["pc-1"]
	if e == nil {
		t.Fatal("머신 키가 없다")
	}
	if e.WindowDays != ActivityWindowDays || e.SinceDay != "2026-07-20" {
		t.Fatalf("창이 틀렸다: %+v", e)
	}
	if e.Sessions != 1 {
		t.Fatalf("창 밖 세션이 섞였다: %d", e.Sessions)
	}
	if e.CertainTotal != 5 {
		t.Fatalf("창 밖 명령이 판정 근거에 섞였다: certainTotal=%d", e.CertainTotal)
	}
	if e.Bash != 8 {
		t.Fatalf("bash 합=%d", e.Bash)
	}
}

// 불확실 러너는 세되 **빨간 판정의 근거로 쓰지 않는다**.
func TestMachineActivitySeparatesCertainFromAmbiguous(t *testing.T) {
	ctx := fresh(t)
	now := at(t, "2026-08-03T00:00:00Z")
	mustSession(t, ctx, SessionInput{SessionID: "s", Machine: "pc-2", Username: "u",
		StartedAt: "2026-08-01T09:00:00.000Z", Output: 1})
	mustCounters(t, ctx, CountersInput{SessionID: "s", Machine: "pc-2", Username: "u",
		StartedAt: "2026-08-01T09:00:00.000Z", Rows: []CounterRow{
			{Kind: "bash", Key: "python.exe", Count: 122},
			{Kind: "bash", Key: "node", Count: 5},
		}})

	act, _ := MachineActivity(ctx, 0, now)
	e := act["pc-2"]
	if e.GateTotal != 127 {
		t.Fatalf("후보 총계=%d", e.GateTotal)
	}
	if e.CertainTotal != 0 {
		t.Fatalf("근거 없는 단정이 경고를 켰다: certainTotal=%d", e.CertainTotal)
	}
	if len(e.CertainKeys) != 0 {
		t.Fatalf("%+v", e.CertainKeys)
	}
	// 후보 목록에는 남되 certain=false 로 표시된다 — 사람이 내역을 보고 판단한다.
	for _, k := range e.GateKeys {
		if k.Certain {
			t.Fatalf("%s 가 확실로 표시됐다", k.Key)
		}
	}
}

// 머신을 모르는 보고는 대조에 쓸 수 없다 — 빈 키로 뭉개지 않는다.
func TestMachineActivitySkipsUnknownMachine(t *testing.T) {
	ctx := fresh(t)
	mustSession(t, ctx, SessionInput{SessionID: "s", Username: "u",
		StartedAt: "2026-08-01T09:00:00.000Z", Output: 1})
	act, _ := MachineActivity(ctx, 0, at(t, "2026-08-03T00:00:00Z"))
	if len(act) != 0 {
		t.Fatalf("빈 머신 키가 생겼다: %+v", act)
	}
}

// 정렬은 결정론이다 — 횟수 내림차순, 동률은 코드포인트 오름차순, 상위 8개.
func TestMachineActivityGateKeysAreSortedAndCapped(t *testing.T) {
	ctx := fresh(t)
	mustSession(t, ctx, SessionInput{SessionID: "s", Machine: "pc-3", Username: "u",
		StartedAt: "2026-08-01T09:00:00.000Z", Output: 1})
	rows := []CounterRow{
		{Kind: "bash", Key: "pytest", Count: 5},
		{Kind: "bash", Key: "jest", Count: 5}, // 동률 → 코드포인트 오름차순으로 jest 가 앞
		{Kind: "bash", Key: "eslint", Count: 9},
	}
	for _, r := range []string{"ruff", "mypy", "tox", "vitest", "mocha", "ava", "biome"} {
		rows = append(rows, CounterRow{Kind: "bash", Key: r, Count: 1})
	}
	mustCounters(t, ctx, CountersInput{SessionID: "s", Machine: "pc-3", Username: "u",
		StartedAt: "2026-08-01T09:00:00.000Z", Rows: rows})

	act, _ := MachineActivity(ctx, 0, at(t, "2026-08-03T00:00:00Z"))
	e := act["pc-3"]
	if len(e.GateKeys) != 8 {
		t.Fatalf("상위 8개로 안 잘렸다: %d", len(e.GateKeys))
	}
	if e.GateKeys[0].Key != "eslint" {
		t.Fatalf("횟수 내림차순이 아니다: %+v", e.GateKeys)
	}
	if e.GateKeys[1].Key != "jest" || e.GateKeys[2].Key != "pytest" {
		t.Fatalf("동률 정렬이 결정론이 아니다: %+v", e.GateKeys[:3])
	}
}
