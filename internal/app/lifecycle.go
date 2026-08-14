package app

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

func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := requestID()
		w.Header().Set("X-Request-ID", id)
		started := time.Now()
		logged := &requestLogWriter{ResponseWriter: w}
		next.ServeHTTP(logged, r)
		if logged.status == 0 {
			logged.status = http.StatusOK
		}
		slog.Info("request", "request_id", id, "method", r.Method, "route", r.Pattern, "status", logged.status, "bytes", logged.bytes, "duration", time.Since(started))
	})
}

func recoverPanic(next http.Handler) http.Handler {
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
