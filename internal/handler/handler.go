// Package handler implements the HTTP request handlers for the go-lgtm demo services.
//
// Design decisions:
//   - Handlers receive a *zap.Logger via the Handler struct instead of relying on a
//     package-level global. This makes dependencies explicit and enables easy testing
//     with a no-op logger (zap.NewNop()).
//   - Every incoming request is logged with a consistent set of structured fields
//     (method, path, status, latency). Structured fields let Loki/LogQL filter and
//     aggregate logs without fragile regex parsing.
//   - We use logger.With(...) at request entry to create a child logger pre-populated
//     with per-request context. All subsequent log calls on that child automatically
//     include those fields, avoiding repetition.
//   - A custom responseWriter wraps http.ResponseWriter so we can capture the HTTP
//     status code written by each handler for inclusion in the access log.
package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"go.uber.org/zap"
)

// Handler groups the HTTP handlers together and carries the shared logger.
// Instantiate it once in main and register its methods against your ServeMux.
type Handler struct {
	log *zap.Logger
}

// New creates a Handler with the provided logger.
// The logger should already carry service-level fields (service name, version)
// so every log line emitted from a handler automatically inherits them.
func New(log *zap.Logger) *Handler {
	return &Handler{log: log}
}

// responseWriter wraps http.ResponseWriter to intercept and record the HTTP
// status code. The standard ResponseWriter doesn't expose the written status
// after the fact, so this thin wrapper is the idiomatic Go solution.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

// newResponseWriter returns a responseWriter with status pre-set to 200 OK,
// matching the default behaviour of http.ResponseWriter when WriteHeader is
// never explicitly called.
func newResponseWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{w, http.StatusOK}
}

// WriteHeader captures the status code before delegating to the real writer.
func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// Middleware returns an http.Handler that wraps h with access-log instrumentation.
// It records the HTTP method, URL path, response status code, and request latency
// as structured Zap fields so these values are queryable in Loki/LogQL.
//
// Usage:
//
//	mux.Handle("/", h.Middleware(myHandlerFunc))
func (h *Handler) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Create a child logger pre-scoped to this request. Fields added here
		// propagate to every log call made with reqLog, keeping log lines consistent.
		reqLog := h.log.With(
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.String("remote_addr", r.RemoteAddr),
			zap.String("user_agent", r.UserAgent()),
		)

		// Wrap the writer to capture the status code set by downstream handlers.
		wrapped := newResponseWriter(w)

		// Propagate the scoped logger via context so handlers can retrieve it
		// if they need to emit additional fields (e.g. "user_id", "trace_id").
		r = r.WithContext(withLogger(r.Context(), reqLog))

		next.ServeHTTP(wrapped, r)

		// Access log: one structured line per request with all relevant fields.
		reqLog.Info("request completed",
			zap.Int("status", wrapped.statusCode),
			zap.Duration("latency", time.Since(start)),
		)
	})
}

// Health handles GET /health.
// It returns HTTP 200 with a JSON body indicating service liveness.
// Kubernetes liveness/readiness probes and load-balancer health checks typically
// hit this endpoint; logging at Debug avoids polluting dashboards with high-frequency noise.
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	log := fromContext(r.Context())
	log.Debug("health check")

	w.Header().Set("Content-Type", "application/json")
	// http.StatusOK is the default, but being explicit documents intent.
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
	})
}

// writeJSON encodes v as JSON and writes it to w with the given status code.
// It always sets Content-Type before WriteHeader so headers are sent correctly.
// Shared by all resource handlers (customer, product, order).
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a JSON error response with a consistent shape:
//
//	{"error": "<message>"}
//
// A consistent error shape means clients and log parsers never have to guess
// the field name for error messages. All resource handlers use this helper.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
