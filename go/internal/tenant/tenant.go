// Package tenant 은 멀티테넌트 컨텍스트를 담는다.
//
// Node 의 AsyncLocalStorage 를 context.Context 로 옮긴 것이다. Go 에서는 이쪽이 더 자연스럽다 —
// 암묵 전파가 아니라 명시 인자라 "감싸는 것을 잊었다"가 컴파일·리뷰에서 잡힌다.
//
// pg 어댑터가 매 쿼리/tx 에서 From(ctx) 를 읽어 `SET LOCAL app.tenant_id` 로 주입하고,
// 그 값을 RLS 정책이 본다. sqlite 백엔드는 단일 테넌트라 이 값을 무시한다.
//
// ⚠ 요청 컨텍스트 **밖**(부팅 시드·타이머 워커·CLI)에서 DB 를 건드릴 때도 감싸야 한다.
// 감싸지 않으면 Default 로 흐르는데, remote 로 남의 DB 를 볼 때 그게 맞는다는 보장이 없다.
package tenant

import "context"

// Default 는 테넌트가 지정되지 않았을 때의 값이다. Node 의 DEFAULT_TENANT 와 같은 문자열이어야
// 한다 — migrations 의 `COALESCE(current_setting('app.tenant_id', true), 'default')` 가 같은
// 값을 기본으로 쓰기 때문이다.
const Default = "default"

// ctxKey 는 비공개 타입이라 이 패키지 밖에서는 같은 키를 만들 수 없다.
// 다른 패키지가 우연히(또는 의도적으로) 컨텍스트 값을 덮어쓰는 자리를 없앤다.
type ctxKey struct{}

// With 는 ctx 에 테넌트를 실어 새 컨텍스트를 돌려준다.
// 빈 문자열은 Default 로 정규화한다 — 빈 테넌트로 흘러 RLS 가 아무 행도 못 보는 것보다,
// 명시적으로 기본 테넌트를 쓰는 편이 진단 가능하다.
func With(ctx context.Context, tenant string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if tenant == "" {
		tenant = Default
	}
	return context.WithValue(ctx, ctxKey{}, tenant)
}

// From 은 현재 테넌트를 돌려준다. 없으면 Default.
func From(ctx context.Context) string {
	if ctx == nil {
		return Default
	}
	if v, ok := ctx.Value(ctxKey{}).(string); ok && v != "" {
		return v
	}
	return Default
}
