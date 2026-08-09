package tenant

import (
	"context"
	"testing"
)

func TestFromWithoutTenantIsDefault(t *testing.T) {
	if got := From(context.Background()); got != Default {
		t.Fatalf("감싸지 않은 컨텍스트는 %q 여야 한다, got %q", Default, got)
	}
	//nolint:staticcheck // nil ctx 는 실수로 들어올 수 있는 값이다 — 죽지 않아야 한다.
	if got := From(nil); got != Default {
		t.Fatalf("nil 컨텍스트에서 죽거나 빈 값을 주면 안 된다, got %q", got)
	}
}

func TestWithRoundTrip(t *testing.T) {
	ctx := With(context.Background(), "acme")
	if got := From(ctx); got != "acme" {
		t.Fatalf("want acme, got %q", got)
	}
}

func TestWithEmptyNormalizesToDefault(t *testing.T) {
	// 빈 테넌트로 흘려 RLS 가 아무 행도 못 보게 두지 않는다.
	ctx := With(context.Background(), "")
	if got := From(ctx); got != Default {
		t.Fatalf("빈 문자열은 %q 로 정규화돼야 한다, got %q", Default, got)
	}
}

func TestNestedWithOverrides(t *testing.T) {
	ctx := With(With(context.Background(), "a"), "b")
	if got := From(ctx); got != "b" {
		t.Fatalf("가장 안쪽 값이 이겨야 한다, got %q", got)
	}
}

func TestOtherPackagesCannotForgeKey(t *testing.T) {
	// 외부 코드가 문자열 키로 심어도 잡히지 않아야 한다(키 타입이 비공개다).
	type foreignKey struct{}
	ctx := context.WithValue(context.Background(), foreignKey{}, "evil")
	if got := From(ctx); got != Default {
		t.Fatalf("남의 키가 테넌트로 읽혔다: %q", got)
	}
}
