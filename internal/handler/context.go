// context.go provides helpers for storing and retrieving a *zap.Logger from
// a request context. Storing the logger in the context avoids passing it as
// an explicit parameter through every function in the call chain, while still
// keeping it request-scoped (as opposed to a global logger).
package handler

import (
	"context"

	"go.uber.org/zap"
)

// contextKey is an unexported type for context keys defined in this package.
// Using a dedicated type prevents key collisions with other packages that also
// store values in context.
type contextKey struct{}

// withLogger returns a copy of ctx carrying the provided logger.
// Called once per request in the Middleware, before ServeHTTP is invoked.
func withLogger(ctx context.Context, log *zap.Logger) context.Context {
	return context.WithValue(ctx, contextKey{}, log)
}

// fromContext retrieves the *zap.Logger stored in ctx.
// If no logger was stored (e.g. in unit tests that don't run through Middleware),
// it returns a no-op logger to prevent nil-pointer panics.
func fromContext(ctx context.Context) *zap.Logger {
	if log, ok := ctx.Value(contextKey{}).(*zap.Logger); ok && log != nil {
		return log
	}
	// zap.NewNop() is a zero-allocation, side-effect-free logger — safe for tests.
	return zap.NewNop()
}
