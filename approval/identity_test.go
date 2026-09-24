package approval

import (
	"context"
	"testing"
)

func TestNamespaceFromReportsWhatWithNamespaceCarried(t *testing.T) {
	namespace := Namespace{Tenant: "acme", Subject: "ci"}
	ctx := WithNamespace(context.Background(), namespace)
	got, ok := NamespaceFrom(ctx)
	if !ok {
		t.Fatal("a context that carries an identity reported none")
	}
	if got != namespace {
		t.Fatalf("identity = %+v, want %+v", got, namespace)
	}
}

func TestNamespaceFromReportsNothingForAPlainContext(t *testing.T) {
	if got, ok := NamespaceFrom(context.Background()); ok {
		t.Fatalf("a plain context reported identity %+v", got)
	}
	if _, ok := NamespaceFrom(nil); ok {
		t.Fatal("a nil context reported an identity")
	}
}

func TestWithNamespaceAcceptsANilContext(t *testing.T) {
	// A nil context is a programming error elsewhere, but carrying an identity
	// into one must not panic: the middleware that resolves a caller may run
	// before anything has established a context.
	ctx := WithNamespace(nil, Namespace{Tenant: "acme", Subject: "ci"})
	if _, ok := NamespaceFrom(ctx); !ok {
		t.Fatal("identity was lost through a nil context")
	}
}

func TestNamespaceFromDoesNotAcceptAForeignValue(t *testing.T) {
	// The key is private, so nothing outside this package can store a value under
	// it; a value stored under some other key must not be mistaken for an
	// identity, because a forged caller identity would be recorded as fact.
	ctx := context.WithValue(context.Background(), struct{ name string }{"tenant"}, Namespace{Tenant: "forged"})
	if got, ok := NamespaceFrom(ctx); ok {
		t.Fatalf("a foreign context value was read as identity %+v", got)
	}
}
