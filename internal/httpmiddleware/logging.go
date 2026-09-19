// Package httpmiddleware holds the HTTP middleware shared by the browser, data
// and MCP adapters: security headers, request security, compression, access
// logging and panic recovery. The composition root applies it once; it must not
// import any adapter.
package httpmiddleware

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"
)

type requestLogWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *requestLogWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *requestLogWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += n
	return n, err
}

func (w *requestLogWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func requestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "unavailable"
	}
	return hex.EncodeToString(b)
}

// AccessLog records one debug log line per request and stamps a request ID that
// recovery and clients can correlate.
func AccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := requestID()
		w.Header().Set("X-Request-ID", id)
		started := time.Now()
		slog.Debug("request started", "request_id", id, "method", r.Method, "path", r.URL.Path)
		logged := &requestLogWriter{ResponseWriter: w}
		next.ServeHTTP(logged, r)
		if logged.status == 0 {
			logged.status = http.StatusOK
		}
		route := r.Pattern
		if route == "" {
			// Because this middleware wraps the mux, r.Pattern may be empty here.
			route = r.URL.Path
		}
		slog.Debug("request", "request_id", id, "method", r.Method, "route", route, "status", logged.status, "bytes", logged.bytes, "duration", time.Since(started))
	})
}

// RecoverPanic converts a handler panic into a generic 500 without leaking
// internals, keeping the request ID set by AccessLog for correlation.
func RecoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				id := w.Header().Get("X-Request-ID")
				slog.Error("panic serving request", "request_id", id, "panic", recovered)
				if logged, ok := w.(*requestLogWriter); !ok || logged.status == 0 {
					http.Error(w, "internal error", http.StatusInternalServerError)
				}
			}
		}()
		next.ServeHTTP(w, r)
	})
}
