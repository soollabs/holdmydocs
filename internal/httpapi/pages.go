package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"hmd/internal/api"
	"hmd/internal/wiki"
)

// previewPage returns the hover-card JSON for one page: title, opening snippet,
// tags and a relative edit age.
func (h *Handlers) previewPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !api.AllowSlug(r.Context(), slug) {
		writeNamespaceDenied(w)
		return
	}
	if !wiki.ValidPageSlug(slug) {
		http.NotFound(w, r)
		return
	}

	view, err := h.api.ViewPage(r.Context(), slug)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	resp := map[string]any{
		"title":   view.Title,
		"snippet": extractSnippet(view.Body, 40),
		"tags":    view.Tags,
		"age":     "just now",
	}
	if history, err := h.api.PageHistory(r.Context(), slug); err == nil && len(history) > 0 {
		resp["age"] = wiki.RelativeTime(history[0].When)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Error("encoding preview response", "err", err)
	}
}

// extractSnippet returns the first wordCount words of body.
func extractSnippet(body string, wordCount int) string {
	words := strings.Fields(body)
	if len(words) > wordCount {
		words = words[:wordCount]
	}
	return strings.Join(words, " ")
}

// previewMarkdown renders an editor body to an HTML fragment. It is a data
// request, not a template render, so it uses the markdown renderer directly and
// never the page templates.
func (h *Handlers) previewMarkdown(w http.ResponseWriter, r *http.Request) {
	var body string
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		if !parseForm(w, r) {
			return
		}
		body = r.FormValue("body")
	} else {
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, api.MaxPageBodyBytes))
		if err != nil {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		body = string(b)
	}
	if len(body) > api.MaxPageBodyBytes {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}

	ns, _ := wiki.NamespaceFor(r.URL.Query().Get("slug"))
	rendered, err := h.renderer.Render(body, ns)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := w.Write([]byte(rendered)); err != nil {
		slog.Error("writing preview response", "err", err)
	}
}

// parseForm reads an urlencoded form, mapping an oversized body to 413.
func parseForm(w http.ResponseWriter, r *http.Request) bool {
	if err := r.ParseForm(); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "bad request", http.StatusBadRequest)
		}
		return false
	}
	return true
}
