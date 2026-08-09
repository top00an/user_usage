package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

// contentTypeJSON 은 현행 응답과 **바이트 단위로 같아야** 한다. 화면과 수집기가 이 값을 본다.
const contentTypeJSON = "application/json; charset=utf-8"

/*
 * writeJSON 은 모든 JSON 응답이 지나는 한 지점이다.
 *
 * HTML 이스케이프를 끈다: Go 의 기본 인코더는 `<`·`>`·`&` 를 < 류로 접지만 Node 의
 * JSON.stringify 는 그대로 둔다. 파싱하면 같은 값이라 골든 대조는 통과하겠지만, 바이트가
 * 갈리면 나중에 응답을 눈으로 비교하는 사람이 원인 없는 차이를 쫓게 된다.
 *
 * Encode 가 붙이는 개행도 뗀다 — JSON.stringify 는 개행을 붙이지 않는다.
 */
func writeJSON(w http.ResponseWriter, code int, body any, headers map[string]string) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(body); err != nil {
		// 여기까지 오는 것은 직렬화 불가한 값을 응답에 실은 프로그래밍 오류다.
		// 원문을 클라이언트로 보내지 않는다 — stderr 로만 남긴다.
		logf("응답 직렬화 실패: %v", err)
		w.Header().Set("Content-Type", contentTypeJSON)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal error"}`))
		return
	}
	out := bytes.TrimRight(buf.Bytes(), "\n")

	h := w.Header()
	h.Set("Content-Type", contentTypeJSON)
	for k, v := range headers {
		h.Set(k, v)
	}
	w.WriteHeader(code)
	_, _ = w.Write(out)
}

// errBody 는 오류 응답의 유일한 shape 다. 화면이 `error` 키에 분기를 걸고 있다.
type errBody struct {
	Error string `json:"error"`
}

func sendError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, errBody{Error: msg}, nil)
}

/*
 * failRequest — 예상 못 한 예외의 응답. **원문을 클라이언트로 보내지 않는다.**
 *
 * 여기로 오는 것은 라우트가 스스로 응답하지 못한 예외다 — 대개 DB 드라이버 에러(테이블·컬럼명,
 * 제약 이름, 때로는 접속 정보 조각을 문장에 담는다)다. 라우트가 의도해서 내는 400(검증 메시지)은
 * 이 경로로 오지 않으므로, 여기서 원문을 접어도 사용자가 고칠 수 있는 안내는 그대로 남는다.
 * 진단에 필요한 원문은 stderr 로 간다 — 그쪽은 운영자만 본다.
 */
func failRequest(w http.ResponseWriter, err any, where string) {
	logf("요청 처리 실패 [%s]: %v", where, err)
	sendError(w, http.StatusBadRequest, "bad request")
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}
