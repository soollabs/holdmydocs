package web

import (
	"bytes"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"

	"hmd/internal/auth"
	"hmd/internal/httpmiddleware"
)

// StartupHandler serves the page a browser gets while the server is still
// starting, with status 503, and the embedded assets that page links. It
// shares the login page's stylesheets, theme script and card, so a visitor
// sees the same look before and after startup. The page refreshes itself,
// and the first refresh after startup gets the page that was asked for.
func StartupHandler() (http.Handler, error) {
	page, err := template.New("starting").Funcs(templateFuncs).ParseFS(webFS,
		"web/templates/base.html", "web/templates/components/*.html", "web/templates/starting.html")
	if err != nil {
		return nil, fmt.Errorf("parsing starting: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /_/static/", staticFiles())
	mux.HandleFunc("GET /_/static/style.css", handleStyleSheet)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var body bytes.Buffer
		data := TemplateData{CSPNonce: httpmiddleware.CspNonce(r.Context()), Skin: defaultSkin, ThemeStyle: buildThemeStyle(auth.UserRecord{Palette: skins[defaultSkin].Palette})}
		if err := page.ExecuteTemplate(&body, "starting", data); err != nil {
			slog.Error("rendering starting page", "err", err)
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		if r.Method == http.MethodGet {
			_, _ = w.Write(body.Bytes())
		}
	})
	return mux, nil
}
