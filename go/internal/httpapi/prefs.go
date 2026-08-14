package httpapi

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/tscorp/user-usage/internal/store"
)

/*
 * ── routeSelfPrefs: 유저별 대시보드 배치. `/api/me/dashboard-layout` **정확 경로 하나**만 소유한다.
 *
 *   GET    현재 배치 — 저장한 적이 없으면 `{"layout": null, "updatedAt": ""}`
 *   PUT    `{"layout": PanelBox[]}` → `{"ok": true, "updatedAt": "<RFC3339>"}`
 *   DELETE 저장을 지워 기본 배치로 되돌린다 → `{"ok": true}`
 *
 * ⚠ **접두사를 소유하지 않는다.** `/api/me/` 의 주인은 routeSelfKeys(onboarding.go)이고, 그쪽은
 *   안 걸리면 404 를 직접 낸다. 그래서 이 라우트는 New 의 s.routes 에서 routeSelfKeys **앞**에
 *   있어야 하고(순서가 계약이다 — server.go 의 New 주석), 대신 여기서는 정확 경로만 잡아
 *   나머지를 전부 뒤로 흘려 보낸다. 접두사로 잡으면 이번엔 셀프서비스 키 API 가 통째로 죽는다.
 *
 * ⚠ **소유자는 언제나 요청자 본인(c.self)이다.** 본문·질의의 username 은 읽지 않는다 —
 *   읽는 순간 "남의 화면을 바꾸기"가 한 줄 실수로 열린다(routeSelfKeys 와 같은 규율).
 *
 * ⚠ c.self 가 비면(=관리자 토큰·인테이크처럼 사람 신원이 없는 자격) 403 이다. 빈 이름으로
 *   내려가면 저장 계층이 ErrNoUsername 을 내므로 사고는 아니지만, 그 실패를 500 으로 흘리면
 *   원인이 "서버 오류"로 흐려진다.
 *
 * 감사 로그(identity.AuditLog)를 남기지 않는다 — 그 로그는 귀속·권한이 바뀌는 자리의 것이고,
 * 자기 화면의 패널 위치는 남의 데이터에 아무 영향이 없다. 남기면 사람이 드래그할 때마다 감사
 * 로그가 쌓여 정작 봐야 할 항목이 묻힌다.
 */

const layoutPath = "/api/me/dashboard-layout"

/*
 * 검증 상수 — 계약 §2. **정본 좌표계는 web/lib/dashLayout.ts 이고 여기는 그 사본이다.**
 * 12열이라는 사실이 두 곳에 있는 것은 어쩔 수 없다(하나는 TS, 하나는 Go). 그래서 값을 바꿀 때
 * 반드시 양쪽을 본다 — 서버만 넓히면 프론트가 못 그리는 좌표가 저장되고, 프론트만 넓히면
 * 사용자가 만든 배치가 400 으로 거부된다.
 */
const (
	gridCols      = 12  // x·w 의 상한을 정한다: x ∈ [0,11] · w ∈ [1,12] · x+w ≤ 12
	maxPanels     = 200 // 한 사람의 패널 수 상한
	maxPanelIDLen = 200 // id 바이트 상한
	maxPanelY     = 10000
	maxPanelH     = 100
)

/*
 * panelBox — 요청 본문의 패널 한 장.
 *
 * 좌표를 int 가 아니라 **float64 로 받는 것이 의도**다. int 로 받으면 0.5 를 encoding/json 이
 * 자체 오류로 거부하는데, 그 문구는 영어이고 어느 패널이 문제인지 말해 주지 않는다. float 로
 * 받아서 "정수인가"를 우리가 판정해야 한국어 안내를 낼 수 있다.
 *
 * id 를 *string 으로 두는 이유: 키가 아예 없는 것("id":이 없다)과 빈 문자열은 다른 실수인데,
 * 둘 다 거부이므로 문구는 하나로 접는다 — 다만 nil 역참조가 없게 포인터로 받는다.
 */
type panelBox struct {
	ID *string `json:"id"`
	X  float64 `json:"x"`
	Y  float64 `json:"y"`
	W  float64 `json:"w"`
	H  float64 `json:"h"`
}

/*
 * layoutPutRequest — PUT 본문(신뢰 경계 — 검증 후 안으로).
 *
 * Layout 이 **포인터**인 것이 의도다: 키 없음/null 과 빈 배열 `[]` 을 갈라야 한다.
 * 앞의 둘은 "무엇을 저장하라는 건지 알 수 없다"라서 400 이고, `[]` 는 사람이 실제로 저장한
 * 값이다(패널을 전부 치운 상태). 뭉치면 빈 배열 저장이 조용히 400 이 된다.
 */
type layoutPutRequest struct {
	Layout *[]panelBox `json:"layout"`
}

// storedBox — 저장·응답에 나가는 패널 한 장. 검증을 통과한 **정수** 좌표만 담는다.
// 요청 타입과 나누는 이유: 검증 전 값(float·nil id)이 저장까지 흘러갈 경로를 타입으로 끊는다.
type storedBox struct {
	ID string `json:"id"`
	X  int    `json:"x"`
	Y  int    `json:"y"`
	W  int    `json:"w"`
	H  int    `json:"h"`
}

// layoutGetResponse — GET 응답(계약 §2).
//
// ⚠ Layout 은 nil 이면 JSON `null` 로, 길이 0 인 슬라이스면 `[]` 로 나간다. **그 둘이 다른
// 사실이다**: null 은 "저장한 적 없음"(화면은 기본 배치), []은 "패널을 전부 치운 배치를 저장함".
// 여기서 make(...) 로 비어 있는 슬라이스를 만들어 두면 전자가 사라진다.
type layoutGetResponse struct {
	Layout    []storedBox `json:"layout"`
	UpdatedAt string      `json:"updatedAt"`
}

// layoutPutResponse — PUT 응답(계약 §2).
type layoutPutResponse struct {
	OK        bool   `json:"ok"`
	UpdatedAt string `json:"updatedAt"`
}

// layoutOKResponse — DELETE 응답(계약 §2).
type layoutOKResponse struct {
	OK bool `json:"ok"`
}

/*
 * validateLayout 은 클라이언트가 보낸 배치를 검증해 **정수 좌표의 정규형**으로 접는다.
 * 두 번째 반환값이 비어 있지 않으면 그것이 곧 400 의 안내 문구다.
 *
 * 왜 이렇게 촘촘한가: 저장은 jsonb 이고 **한 번 들어간 쓰레기는 되돌릴 방법이 없다.**
 * 화면 검증은 방어가 아니다(요청은 curl 로도 온다). 서버가 마지막 방어선이다.
 *
 * 문구에 **몇 번째 패널·어떤 id** 인지 싣는다 — 패널이 200개까지 오는 본문이라 "좌표가
 * 잘못됐습니다"만으로는 사람이 무엇을 고쳐야 할지 알 수 없다.
 */
func validateLayout(in []panelBox) ([]storedBox, string) {
	if len(in) > maxPanels {
		return nil, fmt.Sprintf("패널이 너무 많습니다 — 최대 %d개인데 %d개가 왔습니다", maxPanels, len(in))
	}
	out := make([]storedBox, 0, len(in))
	seen := make(map[string]bool, len(in))
	for i, p := range in {
		if p.ID == nil {
			return nil, fmt.Sprintf("%d번째 패널에 id 가 없습니다", i+1)
		}
		id := strings.TrimSpace(*p.ID)
		if id == "" {
			return nil, fmt.Sprintf("%d번째 패널의 id 가 비어 있습니다", i+1)
		}
		// **바이트**로 센다(계약 §2). 저장 상한이 바이트 단위라 룬으로 세면 한글 id 가
		// 검증을 통과하고 저장에서 잘린다.
		if len(id) > maxPanelIDLen {
			// 문구에 id 를 싣지 않는다 — 200바이트가 넘는 문자열을 잘라 되비추면 룬이 중간에서
			// 갈라져 알아볼 수 없는 안내가 된다. 몇 번째인지와 길이면 고치기에 충분하다.
			return nil, fmt.Sprintf("%d번째 패널의 id 가 너무 깁니다 — 최대 %d바이트인데 %d바이트입니다",
				i+1, maxPanelIDLen, len(id))
		}
		if seen[id] {
			// 중복을 통과시키면 어느 좌표가 그 패널의 자리인지 답이 둘이 된다.
			return nil, fmt.Sprintf("패널 id 가 중복됩니다: %q", id)
		}
		seen[id] = true

		x, ok := gridInt(p.X)
		if !ok || x < 0 || x > gridCols-1 {
			return nil, fmt.Sprintf("%q 패널의 x 가 범위 밖입니다 — 0..%d 의 정수여야 합니다", id, gridCols-1)
		}
		w, ok := gridInt(p.W)
		if !ok || w < 1 || w > gridCols {
			return nil, fmt.Sprintf("%q 패널의 w 가 범위 밖입니다 — 1..%d 의 정수여야 합니다", id, gridCols)
		}
		if x+w > gridCols {
			// 캔버스 밖으로 삐져나온 패널은 프론트가 그릴 자리가 없다 — 저장 전에 끊는다.
			return nil, fmt.Sprintf("%q 패널이 캔버스를 넘칩니다 — x+w 는 %d 이하여야 하는데 %d 입니다",
				id, gridCols, x+w)
		}
		y, ok := gridInt(p.Y)
		if !ok || y < 0 || y > maxPanelY {
			return nil, fmt.Sprintf("%q 패널의 y 가 범위 밖입니다 — 0..%d 의 정수여야 합니다", id, maxPanelY)
		}
		h, ok := gridInt(p.H)
		if !ok || h < 1 || h > maxPanelH {
			return nil, fmt.Sprintf("%q 패널의 h 가 범위 밖입니다 — 1..%d 의 정수여야 합니다", id, maxPanelH)
		}
		out = append(out, storedBox{ID: id, X: x, Y: y, W: w, H: h})
	}
	return out, ""
}

/*
 * gridInt 는 JSON 숫자를 칸 좌표(정수)로 읽는다. ok=false 는 "칸이 아니다"라는 뜻이다.
 *
 * 걸러 내는 것 셋:
 *   · 소수(0.5) — 칸 스냅이 계약이다(§1). 반올림해서 받아 주면 사용자가 보던 자리와 저장된
 *     자리가 달라지고, 그 차이는 다음 로그인에서야 보인다.
 *   · NaN·±Inf — JSON 문법에는 없지만 float64 로 가는 길이 있으므로(1e400 은 +Inf 로 파싱된다)
 *     여기서 끊는다. 아래 int 변환이 정의되지 않은 값이다.
 *   · int 범위 밖 — 변환 자체가 의미를 잃는다. 범위 판정은 호출부가 하지만, 그 전에 이 캐스트가
 *     성립해야 한다.
 */
func gridInt(v float64) (int, bool) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	if v != math.Trunc(v) {
		return 0, false
	}
	if v > math.MaxInt32 || v < math.MinInt32 {
		return 0, false
	}
	return int(v), true
}

func (s *server) routeSelfPrefs(w http.ResponseWriter, r *http.Request, c *rctx) (bool, error) {
	// 정확 경로만 소유한다 — 접두사는 routeSelfKeys 것이다(파일 상단 주석).
	if c.path != layoutPath {
		return false, nil
	}
	// 자격 규율은 routeSelfKeys 와 **같다**: 로그인한 사람만, 그리고 사람 신원이 있어야 한다.
	// (상태변경을 로그인 세션으로만 좁히는 것은 게이트가 이미 한다 — server.go 의 memberSelfKeys.)
	if c.scope != ScopeAdmin && c.scope != ScopeMember {
		sendError(w, http.StatusForbidden, "로그인이 필요합니다")
		return true, nil
	}
	if c.self == "" {
		sendError(w, http.StatusForbidden,
			"이 경로는 로그인한 사용자만 쓸 수 있습니다 — 관리자 토큰에는 사용자 신원이 없습니다")
		return true, nil
	}

	ctx := r.Context()

	switch r.Method {
	case http.MethodGet, http.MethodHead:
		raw, at, ok, err := store.GetDashboardLayout(ctx, c.self)
		if err != nil {
			return true, err
		}
		if !ok {
			// **null 이지 빈 배열이 아니다.** 화면은 이 값에서만 기본 배치로 떨어진다.
			writeJSON(w, http.StatusOK, layoutGetResponse{Layout: nil, UpdatedAt: ""}, nil)
			return true, nil
		}
		var boxes []storedBox
		if err := json.Unmarshal(raw, &boxes); err != nil {
			/*
			 * 저장된 값이 파싱되지 않는다 — 검증을 통과한 값만 넣으므로 정상 경로로는 생길 수
			 * 없다(사람이 DB 를 직접 고쳤거나, 옛 형식이 남았다). **미저장으로 접는다**:
			 * 화면이 기본 배치로 뜨는 쪽이, 파싱 못 하는 본문을 받아 통째로 깨지는 쪽보다 낫다.
			 * 사실이 조용히 사라지지 않게 stderr 에는 남긴다.
			 */
			logf("대시보드 레이아웃 파싱 실패(기본 배치로 접는다) user=%s: %v", c.self, err)
			writeJSON(w, http.StatusOK, layoutGetResponse{Layout: nil, UpdatedAt: ""}, nil)
			return true, nil
		}
		if boxes == nil {
			// 저장된 본문이 `null` 이었다 — 위 미저장과 같은 뜻이지만, 여기서 nil 그대로 두면
			// updatedAt 만 있고 layout 이 null 인 응답이 나가 프론트가 두 상태를 구분 못 한다.
			boxes = []storedBox{}
		}
		writeJSON(w, http.StatusOK, layoutGetResponse{Layout: boxes, UpdatedAt: rfc3339(at)}, nil)
		return true, nil

	case http.MethodPut:
		var body layoutPutRequest
		if err := decodeJSONBody(r, &body); err != nil {
			// 원문을 되비추지 않는다 — 본문은 사용자가 보낸 것이지만 로그·응답에 그대로 싣는
			// 습관이 생기면 언젠가 자격증명이 실린 본문에서도 같은 일이 벌어진다.
			sendError(w, http.StatusBadRequest, "본문이 JSON 이 아닙니다")
			return true, nil
		}
		if body.Layout == nil {
			sendError(w, http.StatusBadRequest, "layout 배열이 필요합니다")
			return true, nil
		}
		boxes, msg := validateLayout(*body.Layout)
		if msg != "" {
			sendError(w, http.StatusBadRequest, msg)
			return true, nil
		}
		/*
		 * ⚠ 클라이언트 원문이 아니라 **검증을 통과한 정규형**을 저장한다. 원문을 그대로 넣으면
		 * 검증이 보지 않은 키(예: 프론트가 실수로 실은 필드)가 jsonb 에 함께 들어가고, 그것을
		 * 되돌릴 방법이 없다. 나가는 값과 들어간 값이 같은 타입인 것도 여기서 보장된다.
		 */
		raw, err := json.Marshal(boxes)
		if err != nil {
			return true, err // storedBox 는 직렬화 불가능한 값을 담을 수 없다 — 오면 프로그래밍 오류다
		}
		at, err := store.PutDashboardLayout(ctx, c.self, raw)
		if err != nil {
			return true, err
		}
		writeJSON(w, http.StatusOK, layoutPutResponse{OK: true, UpdatedAt: rfc3339(at)}, nil)
		return true, nil

	case http.MethodDelete:
		if err := store.DeleteDashboardLayout(ctx, c.self); err != nil {
			return true, err
		}
		// 멱등이다 — 저장이 없어도 200 이다. 초기화 버튼을 두 번 누르는 것은 사고가 아니다.
		writeJSON(w, http.StatusOK, layoutOKResponse{OK: true}, nil)
		return true, nil
	}

	/*
	 * 경로는 소유하지만 메서드가 아니다 → **405**(404 가 아니다). 이 경로는 존재하고, 404 로
	 * 접으면 프론트가 "이 서버에는 그 API 가 없다"(구버전 배포)로 오해해 저장을 통째로 포기한다.
	 * readOnly 모드에서 404 인 것과 갈리는 지점이 정확히 여기다 — 그쪽은 진짜로 없다.
	 */
	w.Header().Set("Allow", "GET, HEAD, PUT, DELETE")
	sendError(w, http.StatusMethodNotAllowed, "지원하지 않는 메서드입니다 — GET·PUT·DELETE 만 받습니다")
	return true, nil
}

// rfc3339 는 저장 시각을 응답 문자열로 접는다. 제로값은 **빈 문자열**이다(계약 §2) —
// 0001-01-01 같은 값을 내보내면 화면이 그것을 실제 시각으로 표시한다.
func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
