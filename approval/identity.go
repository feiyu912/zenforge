package approval

import "context"

// NamespaceFrom reports the caller identity a context carries, if any. It is how
// a host hands the identity it authenticated to the work that identity asked for,
// without every function in between growing a parameter for it: an HTTP
// middleware puts the caller in the request context, and the code that starts a
// run reads it back and makes it the run's ApprovalNamespace.
//
// A context with no identity is not an error, and it is not the same as the zero
// Namespace: a host that requires nothing (or admits an anonymous loopback
// caller) has no identity to report, and the run it starts keeps whatever
// namespace the host was configured with.
func WithNamespace(ctx context.Context, namespace Namespace) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, namespaceContextKey{}, namespace)
}

// NamespaceFrom is the reader for [WithNamespace].
func NamespaceFrom(ctx context.Context) (Namespace, bool) {
	if ctx == nil {
		return Namespace{}, false
	}
	namespace, ok := ctx.Value(namespaceContextKey{}).(Namespace)
	if !ok {
		return Namespace{}, false
	}
	return namespace, true
}

// namespaceContextKey is private so no other package can store a value under it:
// the identity a run is recorded with must come from the host's own
// authentication, never from anything that merely has the context.
type namespaceContextKey struct{}
