package app

import (
	"net/http"
	"strings"
	"sync/atomic"
)

// startupGate lets the server listen before startup finishes. Until Ready is
// called a browser gets the loading page from starting, which refreshes
// itself into the requested page, and every other request gets 503 with
// Retry-After, so nothing waits on a connection that is not being answered.
// Readiness stays 503 until then; liveness is 200 because the process is up.
type startupGate struct {
	starting http.Handler
	handler  atomic.Pointer[http.Handler]
}

// Ready sends every later request to handler.
func (g *startupGate) Ready(handler http.Handler) { g.handler.Store(&handler) }

func (g *startupGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if handler := g.handler.Load(); handler != nil {
		(*handler).ServeHTTP(w, r)
		return
	}
	header := w.Header()
	header.Set("X-Content-Type-Options", "nosniff")
	if r.Method == http.MethodGet && r.URL.Path == "/_/live" {
		header.Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		return
	}
	readOnly := r.Method == http.MethodGet || r.Method == http.MethodHead
	if readOnly && strings.HasPrefix(r.URL.Path, "/_/static/") {
		// The loading page's stylesheets and icon.
		g.starting.ServeHTTP(w, r)
		return
	}
	header.Set("Cache-Control", "no-store")
	header.Set("Retry-After", "2")
	if readOnly && strings.Contains(r.Header.Get("Accept"), "text/html") {
		g.starting.ServeHTTP(w, r)
		return
	}
	http.Error(w, "starting", http.StatusServiceUnavailable)
}
