package app

import (
	"net/http"

	"hmd/internal/httpapi"
	"hmd/internal/httpmiddleware"
	"hmd/internal/mcp"
	"hmd/internal/web"
)

// newHandler assembles the root mux once and wraps it in the shared HTTP
// middleware. Order is significant: access logging and panic recovery wrap
// everything, compression and security headers run before authentication, and
// request security (body bounds, HSTS, CSRF/origin checks) runs after
// authentication so it can tell a bearer principal from a cookie session.
//
// Go's ServeMux selects by specificity, not registration order, so the explicit
// /_/mcp patterns dominate the browser catch-all without a method-less MCP
// pattern conflicting with the browser page dispatcher.
func newHandler(application *web.App) http.Handler {
	mux := http.NewServeMux()
	httpapi.New(application.API, httpapi.Options{
		Renderer:         application.Render,
		DocumentsEnabled: application.API.DocumentsEnabled(),
	}).Register(mux)
	if application.Config().MCP.Enabled {
		mcpHandler := mcp.NewServer(application.API, mcp.Options{
			Version:          web.Version(),
			DocumentsEnabled: application.API.DocumentsEnabled(),
		}).Handler()
		mux.Handle("GET /_/mcp", mcpHandler)
		mux.Handle("POST /_/mcp", mcpHandler)
		mux.Handle("DELETE /_/mcp", mcpHandler)
	}
	mux.Handle("/", application.Routes())

	security := application.Security()
	return httpmiddleware.AccessLog(httpmiddleware.RecoverPanic(httpmiddleware.Compression(httpmiddleware.SecurityHeaders(application.Auth.Middleware(security.Handler(mux))))))
}
