package store

import "testing"

/*
 * runtime 축 — 저장·기본값·정규화·필터.
 *
 * 이 스위트가 지키는 계약은 platform 축과 같은 모양이다: **기존 수집기는 이 필드를 안 보낸다.**
 * 미지정이 cloud 로 채워지지 않으면 과거 데이터와 현행 보고가 통째로 다른 축으로 갈린다.
 *
 * 다만 platform 과 **다른 점이 하나** 있다: 허용목록 밖 값을 위한 제3의 값(other)을 두지
 * 않는다. runtime 은 이분법이라 늘어날 이유가 없고, 모르는 값은 "로컬이라는 표시가 없다"와
 * 실질적으로 같다(runtime.go 주석).
 */

func TestRuntimeDefaultsToCloudWhenUnset(t *testing.T) {
	ctx := fresh(t)
	// 기존 수집기의 보고 그대로 — runtime 필드가 아예 없다.
	mustSession(t, ctx, SessionInput{SessionID: "s1", StartedAt: "2026-08-03T09:00:00.000Z", Output: 10})

	rows, err := SessionRows(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("행 수 = %d", len(rows))
	}
	if rows[0].Runtime != RuntimeDefault {
		t.Fatalf("runtime = %q (기대 %q) — 하위호환이 깨졌다", rows[0].Runtime, RuntimeDefault)
	}
}

func TestNormalizeRuntime(t *testing.T) {
	cases := map[string]string{
		"":        "cloud",
		"   ":     "cloud",
		"cloud":   "cloud",
		"local":   "local",
		" Local ": "local",
		"LOCAL":   "local",
		// 허용목록 밖은 기본값으로 접는다. platform 의 other 같은 제3의 값을 두지 않는다 —
		// runtime 은 이분법이고, 제3의 값을 두면 화면에 영원히 비어 있는 칸이 생긴다.
		"onprem": RuntimeDefault,
		"로컬":     RuntimeDefault,
		"loc al": RuntimeDefault,
		"locl":   RuntimeDefault, // 오타는 local 로 붙지 않는다(접두 매칭 없음)
		"other":  RuntimeDefault,
	}
	for in, want := range cases {
		if got := NormalizeRuntime(in); got != want {
			t.Fatalf("NormalizeRuntime(%q) = %q (기대 %q)", in, got, want)
		}
	}
}

func TestRuntimeRoundTripsLocal(t *testing.T) {
	ctx := fresh(t)
	mustSession(t, ctx, SessionInput{
		SessionID: "s-local", StartedAt: "2026-08-03T09:00:00.000Z", Output: 10,
		Platform: "codex", Runtime: "local",
	})

	rows, err := SessionRows(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Runtime != "local" {
		t.Fatalf("runtime 왕복 실패: %+v", rows)
	}
	// platform 과 **직교**한다 — 로컬이어도 platform 은 여전히 그 도구다.
	if rows[0].Platform != "codex" {
		t.Fatalf("platform = %q — runtime 이 platform 을 덮었다", rows[0].Platform)
	}
}

// 필터가 실제로 모집단을 좁히는지. 미지정은 전체다(무회귀의 근거).
func TestRuntimeFilterNarrowsSessions(t *testing.T) {
	ctx := fresh(t)
	mustSession(t, ctx, SessionInput{SessionID: "c1", StartedAt: "2026-08-03T09:00:00.000Z", Output: 1})
	mustSession(t, ctx, SessionInput{
		SessionID: "l1", StartedAt: "2026-08-03T09:00:00.000Z", Output: 2, Runtime: "local"})

	all, err := SessionRows(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("미지정에서 %d행 — 전체(2)가 와야 한다", len(all))
	}

	only, err := SessionRows(ctx, Filter{Runtime: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if len(only) != 1 || only[0].SessionID != "l1" {
		t.Fatalf("runtime=local 에서 %+v", only)
	}

	cloud, err := SessionRows(ctx, Filter{Runtime: "cloud"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cloud) != 1 || cloud[0].SessionID != "c1" {
		t.Fatalf("runtime=cloud 에서 %+v", cloud)
	}
}

// platform 과 runtime 을 함께 걸면 교집합이다 — 두 서브쿼리 별칭이 겹치면 값만 틀린다.
func TestRuntimeAndPlatformFilterCombine(t *testing.T) {
	ctx := fresh(t)
	mustSession(t, ctx, SessionInput{SessionID: "cx-cloud", StartedAt: "2026-08-03T09:00:00.000Z",
		Output: 1, Platform: "codex"})
	mustSession(t, ctx, SessionInput{SessionID: "cx-local", StartedAt: "2026-08-03T09:00:00.000Z",
		Output: 2, Platform: "codex", Runtime: "local"})
	mustSession(t, ctx, SessionInput{SessionID: "cl-local", StartedAt: "2026-08-03T09:00:00.000Z",
		Output: 4, Platform: "claude", Runtime: "local"})

	rows, err := SessionRows(ctx, Filter{Platform: "codex", Runtime: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SessionID != "cx-local" {
		t.Fatalf("교집합이 아니다: %+v", rows)
	}
}

func TestIsRuntimeFilter(t *testing.T) {
	for _, ok := range []string{"cloud", "local"} {
		if !IsRuntimeFilter(ok) {
			t.Errorf("IsRuntimeFilter(%q) = false", ok)
		}
	}
	// 필터에서는 정규화하지 않는다 — 오타를 cloud 로 접으면 요청과 다른 집합이 조용히 온다.
	for _, bad := range []string{"", "  ", "LOCAL", "locl", "other", "onprem"} {
		if IsRuntimeFilter(bad) {
			t.Errorf("IsRuntimeFilter(%q) = true — 400 을 내야 한다", bad)
		}
	}
}
