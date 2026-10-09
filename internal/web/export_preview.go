package web

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"hmd/internal/api"
)

const exportPreviewTTL = 15 * time.Minute
const maxExportPreviews = 4

type exportPreview struct {
	mu                           sync.RWMutex
	dir, owner, namespace, nonce string
	expires                      time.Time
	timer                        *time.Timer
}

// CloseExportPreviews removes snapshots on orderly shutdown.
func (app *App) CloseExportPreviews() {
	app.previewMu.Lock()
	previews := app.previews
	app.previews = nil
	app.previewMu.Unlock()
	for _, preview := range previews {
		if preview.timer != nil {
			preview.timer.Stop()
		}
		preview.mu.Lock()
		removeExportPreview(preview.dir)
		preview.mu.Unlock()
	}
}

func removeExportPreview(dir string) {
	if err := os.RemoveAll(dir); err != nil {
		slog.Warn("removing static preview", "err", err)
	}
}

func (app *App) expireExportPreview(id string) {
	app.previewMu.Lock()
	defer app.previewMu.Unlock()
	preview := app.previews[id]
	delete(app.previews, id)
	if preview != nil {
		preview.mu.Lock()
		removeExportPreview(preview.dir)
		preview.mu.Unlock()
	}
}

func (app *App) handlePreviewExportNamespace(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := app.apiClient().RequireScope(r.Context(), api.ScopeSettings); err != nil {
		http.Error(w, "settings scope required", http.StatusForbidden)
		return
	}
	name := r.PathValue("name")
	if !app.requireTokenNamespace(w, r, name) {
		return
	}
	cfg, exists := app.Namespaces()[name]
	if !exists {
		http.NotFound(w, r)
		return
	}
	baseURL := cfg.Export.BaseURL
	if baseURL == "" && r.URL.Query().Get("use-main-base-url") == "1" {
		baseURL = app.config().BaseURL
	}
	if baseURL == "" {
		app.render(w, r, http.StatusOK, "export-options", TemplateData{Authed: true, Title: "Preview static site", Namespace: name, ExportMainBaseURL: app.config().BaseURL, ExportPreview: true})
		return
	}
	select {
	case exportSemaphore <- struct{}{}:
		defer func() { <-exportSemaphore }()
	default:
		w.Header().Set("Retry-After", "1")
		http.Error(w, "export is busy", http.StatusTooManyRequests)
		return
	}
	app.previewMu.Lock()
	owner, _ := api.Username(r.Context())
	replaces := false
	for _, previous := range app.previews {
		if previous.owner == owner && previous.namespace == name {
			replaces = true
		}
	}
	full := len(app.previews) >= maxExportPreviews && !replaces
	app.previewMu.Unlock()
	if full {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "preview capacity reached; wait up to 15 minutes for an existing preview to expire", http.StatusTooManyRequests)
		return
	}
	dir, err := os.MkdirTemp("", "hmd-export-preview-")
	if err != nil {
		http.Error(w, "preview failed", http.StatusInternalServerError)
		return
	}
	retained := false
	defer func() {
		if !retained {
			removeExportPreview(dir)
		}
	}()
	id := hex.EncodeToString(randBytes())
	nonce := hex.EncodeToString(randBytes())
	if err := app.apiClient().ExportNamespace(r.Context(), name, dir, webFS, "web/static", NewStaticExporter(app.Render), api.ExportOptions{BaseURL: baseURL, CSPNonce: nonce}); err != nil {
		slog.Error("generating static preview", "namespace", name, "err", err)
		http.Error(w, "preview failed", http.StatusInternalServerError)
		return
	}
	preview := &exportPreview{dir: dir, owner: owner, namespace: name, nonce: nonce, expires: time.Now().Add(exportPreviewTTL)}
	app.previewMu.Lock()
	if app.previews == nil {
		app.previews = make(map[string]*exportPreview)
	}
	var replaced []*exportPreview
	for previousID, previous := range app.previews {
		if previous.owner == owner && previous.namespace == name {
			if previous.timer != nil {
				previous.timer.Stop()
			}
			delete(app.previews, previousID)
			replaced = append(replaced, previous)
		}
	}
	app.previews[id] = preview
	preview.timer = time.AfterFunc(exportPreviewTTL, func() { app.expireExportPreview(id) })
	app.previewMu.Unlock()
	for _, previous := range replaced {
		previous.mu.Lock()
		removeExportPreview(previous.dir)
		previous.mu.Unlock()
	}
	retained = true
	http.Redirect(w, r, "/_/export-preview/"+id+"/", http.StatusSeeOther)
}

func randBytes() []byte {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("cryptographic randomness unavailable")
	}
	return b
}

func (app *App) handleExportPreviewFile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	if err := app.apiClient().RequireScope(r.Context(), api.ScopeSettings); err != nil {
		http.Error(w, "settings scope required", http.StatusForbidden)
		return
	}
	owner, _ := api.Username(r.Context())
	app.previewMu.Lock()
	preview := app.previews[r.PathValue("id")]
	if preview == nil {
		app.previewMu.Unlock()
		http.Error(w, "preview expired or unavailable; generate a new preview", http.StatusGone)
		return
	}
	if preview.owner != owner || time.Now().After(preview.expires) {
		app.previewMu.Unlock()
		http.NotFound(w, r)
		return
	}
	preview.mu.RLock()
	app.previewMu.Unlock()
	defer preview.mu.RUnlock()
	if !app.requireTokenNamespace(w, r, preview.namespace) {
		return
	}
	if _, exists := app.Namespaces()[preview.namespace]; !exists {
		http.NotFound(w, r)
		return
	}
	rel := r.PathValue("path")
	if rel == "" || strings.HasSuffix(rel, "/") {
		rel += "index.html"
	}
	if !fs.ValidPath(rel) || strings.Contains(rel, "\\") {
		http.NotFound(w, r)
		return
	}
	root, err := os.OpenRoot(preview.dir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = root.Close() }()
	file, err := root.Open(rel)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	// Only the export's trusted, nonce-bearing scripts may execute. Attachments
	// never inherit the HTML preview's script permission.
	if strings.HasPrefix(rel, "attachments/") {
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'")
		if !strings.HasPrefix(mime.TypeByExtension(path.Ext(rel)), "image/") {
			w.Header().Set("Content-Disposition", "attachment")
		}
	} else {
		w.Header().Set("Content-Security-Policy", fmt.Sprintf("sandbox allow-scripts allow-same-origin allow-popups allow-popups-to-escape-sandbox; default-src 'self'; base-uri 'none'; frame-ancestors 'none'; object-src 'none'; form-action 'none'; connect-src 'none'; frame-src 'none'; worker-src 'none'; img-src 'self' data:; script-src 'nonce-%s'; style-src 'self' 'nonce-%s'; font-src 'self' data:", preview.nonce, preview.nonce))
	}
	http.ServeContent(w, r, path.Base(rel), time.Time{}, file)
}
