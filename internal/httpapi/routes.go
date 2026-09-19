// Package httpapi serves the browser's JSON data and multipart attachment
// requests. It is the HTTP adapter the browser JavaScript calls; it never
// renders templates or redirects content mutations. Every handler delegates to
// a shared application operation on internal/api, so browser, MCP and
// server-rendered paths cannot diverge.
package httpapi

import (
	"net/http"

	"hmd/internal/api"
	"hmd/internal/wiki"
)

// Handlers is the browser-facing data and upload adapter. It holds only the
// shared application operations and the renderer the editor preview endpoint
// needs; it has no templates, session or store of its own.
type Handlers struct {
	api       *api.API
	renderer  *wiki.Renderer
	documents bool
}

// Options configures the adapter at construction.
type Options struct {
	// Renderer renders the HTML fragment returned by the editor preview
	// endpoint. Preview is a data request, not a template render, so it only
	// needs the markdown renderer, not the page templates.
	Renderer *wiki.Renderer
	// DocumentsEnabled reports whether attachment search and document indexing
	// are configured. It gates the attachment-search route and the upload
	// status code.
	DocumentsEnabled bool
}

// New constructs the adapter over the shared application operations.
func New(client *api.API, opts Options) *Handlers {
	return &Handlers{api: client, renderer: opts.Renderer, documents: opts.DocumentsEnabled}
}

// Register mounts every data, upload and attachment route on mux. Every pattern
// is a specific /_/ path; the adapter never registers a root wildcard, so it
// cannot shadow the browser's page dispatcher. ServeMux selects by specificity,
// so these routes override the browser catch-all regardless of registration
// order.
func (h *Handlers) Register(mux *http.ServeMux) {
	h.registerJSON(mux, "GET /_/api/search", h.searchPages)
	if h.documents {
		h.registerJSON(mux, "GET /_/api/search/attachments", h.searchAttachments)
	}
	h.registerJSON(mux, "GET /_/api/health", h.health)
	h.registerJSON(mux, "GET /_/api/sync", h.syncStatus)
	h.registerJSON(mux, "POST /_/api/sync/push-now", h.syncPushNow)

	// Page mutations. The literal action segment precedes the slug wildcard
	// because a multi-segment wildcard must be the last pattern segment; these
	// literal routes are more specific than the save route and win by ServeMux
	// precedence.
	mux.HandleFunc("POST /_/api/pages/{slug...}", h.savePage)
	h.registerJSONPath(mux, "POST /_/api/pages/delete/{slug...}", h.deletePage)
	h.registerJSONPath(mux, "POST /_/api/pages/rename/{slug...}", h.renamePage)
	h.registerJSONPath(mux, "POST /_/api/pages/tags/{slug...}", h.setPageTags)
	h.registerJSONPath(mux, "POST /_/api/pages/revert/{slug...}", h.revertPage)

	h.registerSettings(mux)
	h.registerNamespaces(mux)

	mux.HandleFunc("GET /_/api/preview/{slug...}", h.previewPage)
	mux.HandleFunc("POST /_/api/preview", h.previewMarkdown)
	mux.HandleFunc("POST /_/api/attachments/{slug...}", h.uploadAttachment)
	mux.HandleFunc("POST /_/api/attachment-uploads/{token}", h.capabilityUpload)
	mux.HandleFunc("GET /_/attachments/{path...}", h.serveAttachment)
}
