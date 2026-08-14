package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/tscorp/user-usage/internal/store"
	"github.com/tscorp/user-usage/internal/tenant"
)

/*
 * /api/me/dashboard-layout — 유저별 대시보드 배치.
 *
 * 여기서 재는 것은 셋이다:
 *   ① 응답 shape(계약 §2) — 특히 **미저장은 null 이고 빈 배열이 아니다.**
 *   ② 검증(계약 §2) — jsonb 에 한 번 들어간 쓰레기는 되돌릴 방법이 없으므로, 위반은 400 이고
 *      **저장은 일어나지 않는다.**
 *   ③ 자격·배선 — 남의 배치가 안 섞이는 것, 사람 신원 없는 자격의 403, 그리고 라우트 순서와
 *      memberSelfKeys 배선(둘 다 빠뜨리면 404·403 으로만 보인다).
 */

// 경로 상수는 prefs.go 의 layoutPath 를 그대로 쓴다 — 여기에 사본을 두면 라우트가 옮겨간 날
// 테스트만 옛 경로를 두드리며 초록으로 남는다.

// putLayout 은 `{"layout": …}` 본문을 만든다(테스트가 좌표만 신경 쓰게).
func putLayout(items string) string { return `{"layout":` + items + `}` }

// layoutSession 은 로그인한 사람 하나짜리 하네스다. 돌려주는 것은 핸들러와 세션 쿠키 opt.
func layoutSession(t *testing.T, username, role string) (http.Handler, reqOpt) {
	t.Helper()
	openDB(t)
	seedUser(t, username, role, "layout-password-1")
	h := New(authCfg())
	return h, sessionCookie(login(t, h, username, "layout-password-1"))
}

// 저장한 적이 없으면 layout 은 **null** 이다. 빈 배열로 접으면 "패널을 전부 치운 사람"과
// "아직 저장 안 한 사람"이 같아지고, 화면은 전자에서 기본 배치를 되살려 버린다.
func TestLayoutMissingIsNullNotEmptyArray(t *testing.T) {
	h, sess := layoutSession(t, "amy", "admin")

	rec := do(t, h, http.MethodGet, layoutPath, "", sess)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	// 바이트로 본다 — decode 를 거치면 null 과 [] 의 차이가 흐려진다.
	if body := rec.Body.String(); !strings.Contains(body, `"layout":null`) {
		t.Fatalf("미저장 응답에 layout:null 이 없다: %s", body)
	}
	got := decode(t, rec)
	if got["layout"] != nil {
		t.Fatalf("layout=%v (기대 null)", got["layout"])
	}
	if got["updatedAt"] != "" {
		t.Fatalf("updatedAt=%v (기대 \"\")", got["updatedAt"])
	}
}

// PUT → GET 왕복이 좌표를 그대로 보존하고, updatedAt 이 RFC3339 로 온다.
func TestLayoutPutThenGetRoundTrip(t *testing.T) {
	h, sess := layoutSession(t, "amy", "admin")

	const items = `[{"id":"cost","x":0,"y":0,"w":6,"h":4},{"id":"tokens","x":6,"y":2,"w":6,"h":3}]`
	rec := do(t, h, http.MethodPut, layoutPath, putLayout(items), sess)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT code=%d body=%s", rec.Code, rec.Body.String())
	}
	put := decode(t, rec)
	if put["ok"] != true {
		t.Fatalf("PUT ok=%v", put["ok"])
	}
	stamp, _ := put["updatedAt"].(string)
	if !strings.HasSuffix(stamp, "Z") || len(stamp) < 20 {
		t.Fatalf("updatedAt 이 RFC3339 UTC 가 아니다: %q", stamp)
	}

	rec = do(t, h, http.MethodGet, layoutPath, "", sess)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET code=%d body=%s", rec.Code, rec.Body.String())
	}
	// 원시 JSON 으로 본다 — 좌표가 **정수**로 나가는지까지 재기 위해서다(4 가 4.0 이 되면
	// 프론트의 칸 계산이 소수로 흐른다).
	body := rec.Body.String()
	for _, want := range []string{
		`{"id":"cost","x":0,"y":0,"w":6,"h":4}`,
		`{"id":"tokens","x":6,"y":2,"w":6,"h":3}`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("왕복에서 값이 바뀌었다 — %s 가 없다:\n%s", want, body)
		}
	}
	if got := decode(t, rec)["updatedAt"]; got != stamp {
		t.Fatalf("GET updatedAt=%v, PUT=%v — 같은 시각이어야 한다", got, stamp)
	}
}

// DELETE 는 저장을 지워 **미저장 상태(null)** 로 되돌린다(빈 배열이 아니다). 멱등이다.
func TestLayoutDeleteRestoresDefault(t *testing.T) {
	h, sess := layoutSession(t, "amy", "admin")

	if rec := do(t, h, http.MethodPut, layoutPath,
		putLayout(`[{"id":"cost","x":0,"y":0,"w":6,"h":4}]`), sess); rec.Code != http.StatusOK {
		t.Fatalf("PUT code=%d body=%s", rec.Code, rec.Body.String())
	}
	rec := do(t, h, http.MethodDelete, layoutPath, "", sess)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE code=%d body=%s", rec.Code, rec.Body.String())
	}
	if decode(t, rec)["ok"] != true {
		t.Fatalf("DELETE 응답=%s", rec.Body.String())
	}
	if body := do(t, h, http.MethodGet, layoutPath, "", sess).Body.String(); !strings.Contains(body, `"layout":null`) {
		t.Fatalf("삭제 후에도 배치가 남아 있다: %s", body)
	}
	// 두 번째 삭제 — 초기화 버튼을 두 번 눌렀다고 500 이 나면 안 된다.
	if rec := do(t, h, http.MethodDelete, layoutPath, "", sess); rec.Code != http.StatusOK {
		t.Fatalf("두 번째 DELETE code=%d", rec.Code)
	}
}

/*
 * 검증(계약 §2). 표 하나로 전부 돌린다 — 규칙이 촘촘해서 케이스마다 함수를 쓰면 빠진 규칙이
 * 눈에 안 띈다. **위반은 400 이고, 저장은 일어나지 않아야 한다**(뒤에서 GET 으로 확인).
 */
func TestLayoutValidationRejectsAndStoresNothing(t *testing.T) {
	h, sess := layoutSession(t, "amy", "admin")

	// 개수 초과(201개) — 상한은 200.
	many := make([]string, 0, 201)
	for i := 0; i < 201; i++ {
		many = append(many, fmt.Sprintf(`{"id":"p%d","x":0,"y":%d,"w":1,"h":1}`, i, i))
	}

	cases := []struct{ name, body string }{
		{"본문이 JSON 이 아니다", `{"layout":`},
		{"layout 키가 없다", `{}`},
		{"layout 이 null", `{"layout":null}`},
		{"layout 이 배열이 아니다", `{"layout":{"id":"cost"}}`},
		{"x+w 가 12 를 넘는다", putLayout(`[{"id":"cost","x":8,"y":0,"w":6,"h":4}]`)},
		{"x 가 음수", putLayout(`[{"id":"cost","x":-1,"y":0,"w":4,"h":4}]`)},
		{"x 가 11 을 넘는다", putLayout(`[{"id":"cost","x":12,"y":0,"w":1,"h":1}]`)},
		{"w 가 0", putLayout(`[{"id":"cost","x":0,"y":0,"w":0,"h":4}]`)},
		{"w 가 12 를 넘는다", putLayout(`[{"id":"cost","x":0,"y":0,"w":13,"h":4}]`)},
		{"y 가 음수", putLayout(`[{"id":"cost","x":0,"y":-1,"w":4,"h":4}]`)},
		{"y 가 10000 을 넘는다", putLayout(`[{"id":"cost","x":0,"y":10001,"w":4,"h":4}]`)},
		{"h 가 0", putLayout(`[{"id":"cost","x":0,"y":0,"w":4,"h":0}]`)},
		{"h 가 100 을 넘는다", putLayout(`[{"id":"cost","x":0,"y":0,"w":4,"h":101}]`)},
		{"소수 좌표", putLayout(`[{"id":"cost","x":0.5,"y":0,"w":4,"h":4}]`)},
		{"소수 크기", putLayout(`[{"id":"cost","x":0,"y":0,"w":4.25,"h":4}]`)},
		{"id 중복", putLayout(`[{"id":"cost","x":0,"y":0,"w":4,"h":4},{"id":"cost","x":4,"y":0,"w":4,"h":4}]`)},
		{"id 가 공백", putLayout(`[{"id":"   ","x":0,"y":0,"w":4,"h":4}]`)},
		{"id 가 없다", putLayout(`[{"x":0,"y":0,"w":4,"h":4}]`)},
		{"id 가 200 바이트 초과", putLayout(`[{"id":"` + strings.Repeat("a", 201) + `","x":0,"y":0,"w":4,"h":4}]`)},
		{"패널 개수 초과", putLayout("[" + strings.Join(many, ",") + "]")},
	}

	for _, tc := range cases {
		rec := do(t, h, http.MethodPut, layoutPath, tc.body, sess)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: code=%d (기대 400) body=%s", tc.name, rec.Code, rec.Body.String())
		}
		msg, _ := decode(t, rec)["error"].(string)
		if strings.TrimSpace(msg) == "" {
			t.Fatalf("%s: 400 인데 안내 문구가 없다: %s", tc.name, rec.Body.String())
		}
	}

	// 거부된 값이 하나도 저장되지 않았다 — 화면은 여전히 미저장이다.
	if body := do(t, h, http.MethodGet, layoutPath, "", sess).Body.String(); !strings.Contains(body, `"layout":null`) {
		t.Fatalf("거부된 요청이 저장을 남겼다: %s", body)
	}
}

// 경계값은 통과해야 한다 — 상한을 한 칸 좁게 잡으면 12칸 폭 패널을 아무도 못 만든다.
func TestLayoutAcceptsBoundaryValues(t *testing.T) {
	h, sess := layoutSession(t, "amy", "admin")

	const items = `[{"id":"full","x":0,"y":0,"w":12,"h":100},{"id":"edge","x":11,"y":10000,"w":1,"h":1}]`
	rec := do(t, h, http.MethodPut, layoutPath, putLayout(items), sess)
	if rec.Code != http.StatusOK {
		t.Fatalf("경계값이 거부됐다: code=%d body=%s", rec.Code, rec.Body.String())
	}
	// 빈 배열도 유효한 값이다("전부 기본 자리로" 와는 다른, 사람이 저장한 사실이다).
	if rec := do(t, h, http.MethodPut, layoutPath, `{"layout":[]}`, sess); rec.Code != http.StatusOK {
		t.Fatalf("빈 배열이 거부됐다: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if body := do(t, h, http.MethodGet, layoutPath, "", sess).Body.String(); !strings.Contains(body, `"layout":[]`) {
		t.Fatalf("빈 배열이 null 로 접혔다 — 둘은 다른 사실이다: %s", body)
	}
}

// **남의 배치가 섞이지 않는다.** A 가 저장하고 B 로 조회하면 미저장이다.
func TestLayoutIsPerUser(t *testing.T) {
	openDB(t)
	seedUser(t, "amy", "member", "amy-password-11")
	seedUser(t, "bob", "member", "bob-password-11")
	h := New(authCfg())
	amy := sessionCookie(login(t, h, "amy", "amy-password-11"))
	bob := sessionCookie(login(t, h, "bob", "bob-password-11"))

	if rec := do(t, h, http.MethodPut, layoutPath,
		putLayout(`[{"id":"cost","x":0,"y":0,"w":6,"h":4}]`), amy); rec.Code != http.StatusOK {
		t.Fatalf("amy PUT: code=%d body=%s", rec.Code, rec.Body.String())
	}
	body := do(t, h, http.MethodGet, layoutPath, "", bob).Body.String()
	if !strings.Contains(body, `"layout":null`) {
		t.Fatalf("bob 이 amy 의 배치를 봤다: %s", body)
	}
	// 소유자는 언제나 요청자 본인이다 — 본문에 남의 이름을 실어도 그것을 읽지 않는다.
	if rec := do(t, h, http.MethodPut, layoutPath,
		`{"username":"amy","layout":[{"id":"x","x":0,"y":0,"w":1,"h":1}]}`, bob); rec.Code != http.StatusOK {
		t.Fatalf("bob PUT: code=%d body=%s", rec.Code, rec.Body.String())
	}
	amyBody := do(t, h, http.MethodGet, layoutPath, "", amy).Body.String()
	if !strings.Contains(amyBody, `"cost"`) {
		t.Fatalf("본문의 username 이 읽혀 amy 의 배치가 덮였다: %s", amyBody)
	}
}

/*
 * 자격 경계.
 *   · 무자격 → 401(게이트)
 *   · cfg 관리자 토큰 → 403. 그 토큰에는 **사람 신원이 없다** — 누구의 배치인지 정할 수 없다.
 *   · 개인 열람 토큰 → 조회는 되고 상태변경은 403(게이트의 ViaSession 규율).
 *   · member 로그인 세션 → 상태변경까지 된다. 이것이 memberSelfKeys 배선을 재는 자리다 —
 *     맵에 경로를 안 넣으면 여기가 403 으로 떨어진다.
 */
func TestLayoutCredentialBoundary(t *testing.T) {
	openDB(t)
	seedUser(t, "amy", "member", "amy-password-11")
	ctx := tenant.With(context.Background(), "default")
	memberTok, err := store.IssueMemberToken(ctx, "amy")
	if err != nil {
		t.Fatalf("IssueMemberToken: %v", err)
	}
	h := New(authCfg())
	sess := sessionCookie(login(t, h, "amy", "amy-password-11"))
	withMember := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+memberTok) }

	if rec := do(t, h, http.MethodGet, layoutPath, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("무자격: code=%d (기대 401)", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, layoutPath, "", withAdmin); rec.Code != http.StatusForbidden {
		t.Fatalf("관리자 토큰 조회: code=%d (기대 403 — 사람 신원이 없다)", rec.Code)
	}
	if rec := do(t, h, http.MethodPut, layoutPath,
		putLayout(`[{"id":"cost","x":0,"y":0,"w":4,"h":4}]`), withAdmin); rec.Code != http.StatusForbidden {
		t.Fatalf("관리자 토큰 저장: code=%d (기대 403)", rec.Code)
	}
	// member 로그인 세션은 상태변경까지 된다(memberSelfKeys 배선).
	if rec := do(t, h, http.MethodPut, layoutPath,
		putLayout(`[{"id":"cost","x":0,"y":0,"w":4,"h":4}]`), sess); rec.Code != http.StatusOK {
		t.Fatalf("member 세션 저장: code=%d body=%s (기대 200 — memberSelfKeys 에 경로가 빠졌나)",
			rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodDelete, layoutPath, "", sess); rec.Code != http.StatusOK {
		t.Fatalf("member 세션 삭제: code=%d (기대 200)", rec.Code)
	}
	// 개인 열람 토큰: 조회는 되고,
	if rec := do(t, h, http.MethodGet, layoutPath, "", withMember); rec.Code != http.StatusOK {
		t.Fatalf("member 토큰 조회: code=%d body=%s (기대 200)", rec.Code, rec.Body.String())
	}
	// 저장은 안 된다 — 조회 자격이 화면 상태를 바꾸는 자리가 되면 안 된다.
	if rec := do(t, h, http.MethodPut, layoutPath,
		putLayout(`[{"id":"cost","x":0,"y":0,"w":4,"h":4}]`), withMember); rec.Code != http.StatusForbidden {
		t.Fatalf("member 토큰 저장: code=%d (기대 403)", rec.Code)
	}
}

/*
 * ⚠ **라우트 순서 계약.** routeSelfPrefs 가 routeSelfKeys 보다 앞에 있어야 한다 —
 * 뒤로 가면 `/api/me/` 접두사를 소유한 routeSelfKeys 가 먼저 잡아 404 를 낸다.
 * 그리고 그 앞섬이 반대로 셀프서비스 **키** 경로를 삼켜서도 안 된다(정확 경로만 소유한다).
 * 두 방향을 한 테스트에서 잰다 — 한쪽만 재면 다른 쪽이 조용히 깨진다.
 */
func TestLayoutRouteOrderKeepsSelfKeysAlive(t *testing.T) {
	initOrg(t)
	seedUser(t, "amy", "member", "amy-password-11")
	h := New(authCfg())
	sess := sessionCookie(login(t, h, "amy", "amy-password-11"))

	if rec := do(t, h, http.MethodGet, layoutPath, "", sess); rec.Code != http.StatusOK {
		t.Fatalf("레이아웃 경로가 404 다 — routeSelfPrefs 가 routeSelfKeys 뒤에 있나: code=%d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/api/me/keys", "", sess); rec.Code != http.StatusOK {
		t.Fatalf("셀프서비스 키 경로가 죽었다 — routeSelfPrefs 가 접두사를 삼켰나: code=%d body=%s",
			rec.Code, rec.Body.String())
	}
	// 소유하는 경로에 없는 메서드는 405 다(경로는 존재한다 — 404 로 접으면 프론트가 "그런 API 가
	// 없다"로 오해한다).
	if rec := do(t, h, http.MethodPost, layoutPath, "", sess); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: code=%d (기대 405)", rec.Code)
	}
}

/*
 * readOnly(=remote) 모드에서는 이 엔드포인트가 **존재하지 않는다**(404).
 *
 * 그 모드의 배포는 남의 운영 DB 를 조회만 한다 — PUT/DELETE 가 닿으면 안 되고, 셀프서비스
 * 라우트는 종전에도 통째로 빠져 있었다(New 의 readOnly 분기). 조회만 열어 두는 선택지도
 * 있었지만, 그러면 "읽기는 되는데 저장은 404" 라는 반쪽 화면이 되고 프론트가 그 상태를 따로
 * 다뤄야 한다. 없는 편이 정직하다 — 프론트는 비-200 을 "저장된 것 없음"으로 접으면 된다.
 */
func TestLayoutAbsentInReadOnlyMode(t *testing.T) {
	openDB(t)
	seedUser(t, "amy", "admin", "amy-password-11")
	cfg := testCfg(true) // readOnly
	h := New(cfg)

	// readOnly 에는 로그인 라우트가 있으므로 세션은 만들 수 있다.
	if rec := do(t, h, http.MethodGet, layoutPath, "", withAdmin); rec.Code != http.StatusNotFound {
		t.Fatalf("readOnly 조회: code=%d (기대 404 — 그 모드에 이 엔드포인트는 없다)", rec.Code)
	}
	if rec := do(t, h, http.MethodPut, layoutPath,
		putLayout(`[{"id":"cost","x":0,"y":0,"w":4,"h":4}]`), withAdmin); rec.Code != http.StatusNotFound {
		t.Fatalf("readOnly 저장: code=%d (기대 404)", rec.Code)
	}
}

// 상한 근처의 큰 본문(200개)은 통과한다 — 상한 자체가 방어선이고, 그 아래를 막으면
// 패널이 많은 사람이 이유 없이 저장에 실패한다.
func TestLayoutAcceptsMaxPanels(t *testing.T) {
	h, sess := layoutSession(t, "amy", "admin")

	items := make([]string, 0, 200)
	for i := 0; i < 200; i++ {
		items = append(items, fmt.Sprintf(`{"id":"p%d","x":%d,"y":%d,"w":1,"h":1}`, i, i%12, i/12))
	}
	rec := do(t, h, http.MethodPut, layoutPath, putLayout("["+strings.Join(items, ",")+"]"), sess)
	if rec.Code != http.StatusOK {
		t.Fatalf("200개 저장: code=%d body=%s", rec.Code, rec.Body.String())
	}

	var got struct {
		Layout []json.RawMessage `json:"layout"`
	}
	body := do(t, h, http.MethodGet, layoutPath, "", sess).Body.Bytes()
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("응답 파싱: %v", err)
	}
	if len(got.Layout) != 200 {
		t.Fatalf("패널 %d개가 돌아왔다 (기대 200)", len(got.Layout))
	}
}
