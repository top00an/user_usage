package httpapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"net/http"
	"net/url"
	"strings"

	"github.com/tscorp/user-usage/internal/config"
)

// 자격증명 판정 결과. 호출부가 둘을 **다르게** 취급한다.
const (
	ViaHeader = "header" // Authorization: Bearer
	ViaCookie = "cookie" // usage_tok — 조회만 태운다(CSRF 표면 제거)
	//revive:disable-next-line
	ScopeAdmin  = "admin"  // 열람 + 상태변경
	ScopeIntake = "intake" // POST /api/usage 하나만
)

// Auth 는 통과한 자격증명이다. nil 은 "자격증명이 없거나 틀렸다"다.
type Auth struct {
	Via   string
	Scope string
}

/*
 * cmpKey — 상수시간 비교용 HMAC 키. **프로세스마다 무작위**라 다이제스트 자체는 비밀이 아니다.
 *
 * 왜 HMAC 으로 접고 비교하나: 길이가 다른 두 문자열을 그냥 비교하면 길이 자체가 타이밍으로
 * 샌다. 양쪽을 고정 32바이트로 접은 뒤 비교하면 입력 길이가 관찰되지 않는다.
 */
var cmpKey = randomKey(32)

func randomKey(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 가 실패하는 환경은 이 서비스가 뜰 자리가 아니다.
		panic("httpapi: 무작위 비교 키를 만들 수 없다: " + err.Error())
	}
	return b
}

func safeEqual(a, b string) bool {
	da := hmac.New(sha256.New, cmpKey)
	da.Write([]byte(a))
	dbg := hmac.New(sha256.New, cmpKey)
	dbg.Write([]byte(b))
	return hmac.Equal(da.Sum(nil), dbg.Sum(nil))
}

/*
 * Authenticate — 자격증명 판정. 규칙 셋이고, 전부 골든의 오류 스냅샷이 잡고 있다:
 *
 *  ① 상수시간 비교(위 safeEqual).
 *  ② `Authorization` 이 있는데 틀렸으면 **쿠키로 흘리지 않는다.** 폴백이 있으면 게이트가
 *     흐려진다 — 헤더를 틀리게 보낸 수집기가 브라우저 쿠키로 통과하는 자리가 생긴다.
 *  ③ **인테이크 토큰은 쿠키로 인정하지 않는다.** 그 토큰의 보고자는 수집기이지 브라우저가
 *     아니고, 쿠키로 받아 주면 브라우저를 꾀어 임의 사용량을 밀어 넣는 자리가 생긴다.
 */
func Authenticate(r *http.Request, cfg config.Config) *Auth {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		got := strings.TrimSpace(h[len("Bearer "):])
		if safeEqual(got, cfg.Token) {
			return &Auth{Via: ViaHeader, Scope: ScopeAdmin}
		}
		if cfg.IntakeToken != "" && safeEqual(got, cfg.IntakeToken) {
			return &Auth{Via: ViaHeader, Scope: ScopeIntake}
		}
		return nil // ② 쿠키로 흘리지 않는다
	}
	if c, ok := parseCookies(r)["usage_tok"]; ok && c != "" {
		if safeEqual(c, cfg.Token) {
			return &Auth{Via: ViaCookie, Scope: ScopeAdmin}
		}
		return nil // ③ 쿠키로는 admin 만 — intake 는 대조조차 하지 않는다
	}
	return nil
}

/*
 * parseCookies 는 Cookie 헤더를 파싱한다.
 *
 * net/http 의 r.Cookie 를 쓰지 않는 이유: 그쪽은 값을 퍼센트 디코딩하지 않는데 클라이언트
 * 프런트(web/)는 encodeURIComponent 로 넣는다. 디코딩을 빼면 특수문자가 든 토큰이
 * 조용히 안 맞는다.
 *
 * PathUnescape 를 쓴다(QueryUnescape 아님) — 후자는 '+' 를 공백으로 바꾸지만
 * decodeURIComponent 는 '+' 를 그대로 둔다. 토큰에 '+' 가 있으면 갈리는 자리다.
 * 디코딩이 실패하면 **원문을 쓴다** — 여기서 죽으면 잘못된 쿠키 하나가 요청을 400 으로
 * 떨어뜨리고, 그건 "토큰이 틀렸다"보다 진단하기 어렵다.
 */
func parseCookies(r *http.Request) map[string]string {
	out := map[string]string{}
	raw := r.Header.Get("Cookie")
	if raw == "" {
		return out
	}
	for _, part := range strings.Split(raw, ";") {
		i := strings.Index(part, "=")
		if i <= 0 {
			continue
		}
		k := strings.TrimSpace(part[:i])
		v := strings.TrimSpace(part[i+1:])
		if dec, err := url.PathUnescape(v); err == nil {
			v = dec
		}
		out[k] = v
	}
	return out
}
