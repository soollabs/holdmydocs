package main

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"log"
	"log/slog"
	"net/http"
	"os"
)

//go:embed all:web
var webFS embed.FS

// parseTemplates builds one template set per page: each page template
// defines "content", so they cannot share a set with one another and are
// each paired with the shared base layout instead.
func parseTemplates() (map[string]*template.Template, error) {
	tmpl := make(map[string]*template.Template)
	widgetFiles := []string{
		"web/templates/widgets/identity.html", "web/templates/widgets/search.html", "web/templates/widgets/pages.html",
		"web/templates/widgets/pinned.html", "web/templates/widgets/tags.html", "web/templates/widgets/log.html",
		"web/templates/widgets/health.html", "web/templates/widgets/keys.html",
		"web/templates/widgets/calendar.html", "web/templates/widgets/writing-stats.html",
		"web/templates/widgets/inbox.html", "web/templates/widgets/sources.html",
		"web/templates/widgets/outline.html", "web/templates/widgets/source-card.html",
		"web/templates/widgets/page-meta.html", "web/templates/widgets/backlinks.html", "web/templates/widgets/prev-entries.html",
	}
	for _, name := range []string{"login", "page", "edit", "conflict", "create", "search", "history", "tags", "settings", "admin", "hidden", "inbox"} {
		files := append([]string{"web/templates/base.html", "web/templates/" + name + ".html"}, widgetFiles...)
		t, err := template.ParseFS(webFS, files...)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", name, err)
		}
		tmpl[name] = t
	}
	// The garden template is deliberately standalone (not on base.html) so
	// nothing internal can leak into the public pages.
	t, err := template.ParseFS(webFS, "web/templates/garden.html")
	if err != nil {
		return nil, fmt.Errorf("parsing garden: %w", err)
	}
	tmpl["garden"] = t
	return tmpl, nil
}

func main() {
	loadThemeDefaults()

	cfg, err := LoadConfig()
	if err != nil {
		log.Fatalf("Loading config: %v", err)
	}

	// Wire slog level: debug when HMD_DEBUG is set, info otherwise.
	level := slog.LevelInfo
	if cfg.Debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	slog.Info("hmd starting", "bind", cfg.Bind, "debug", cfg.Debug, "sync_mode", cfg.SyncMode)

	// Open store
	store, err := OpenStore(cfg)
	if err != nil {
		log.Fatalf("open store failed: %v", err)
	}

	// Load all pages and build index
	paths, err := store.List()
	if err != nil {
		log.Fatalf("store.List failed: %v", err)
	}
	dailySlugs, err := store.DailyPages()
	if err != nil {
		log.Fatalf("store.DailyPages failed: %v", err)
	}

	var pages []Page
	hashes := make(map[string]string)
	for _, path := range paths {
		content, hash, err := store.Read(path)
		if err != nil {
			log.Fatalf("Reading %s: %v", path, err)
		}
		slug := path[:len(path)-3] // remove .md
		pages = append(pages, ParsePage(slug, content))
		hashes[slug] = hash
	}
	for _, slug := range dailySlugs {
		content, hash, err := store.Read(pageFile(slug))
		if err != nil {
			log.Fatalf("Reading %s: %v", pageFile(slug), err)
		}
		pages = append(pages, ParsePage(slug, content))
		hashes[slug] = hash
	}

	index, err := BuildIndex(pages)
	if err != nil {
		log.Fatalf("build index failed: %v", err)
	}
	slog.Info("index built", "pages", len(pages))

	// Pages can be added or edited directly on disk (outside the UI, e.g. by
	// git pull), so poll for changes rather than relying solely on handler
	// updates.
	go pollFS(store, index, hashes)

	// Create renderer and auth
	renderer := NewRenderer(index.Exists)
	auth, err := OpenAuth(cfg)
	if err != nil {
		log.Fatalf("open auth failed: %v", err)
	}

	tmpl, err := parseTemplates()
	if err != nil {
		log.Fatalf("Parsing templates: %v", err)
	}

	// Create app
	app := &App{
		Store:  store,
		Auth:   auth,
		Index:  index,
		Render: renderer,
		Tmpl:   tmpl,
	}
	app.SetConfig(cfg)

	// OIDC: run discovery at startup when configured; fail loudly if the
	// issuer is unreachable rather than serving a broken SSO button.
	if cfg.OIDC.Issuer != "" {
		app.OIDC, err = NewOIDCAuth(context.Background(), cfg)
		if err != nil {
			log.Fatalf("setting up OIDC failed: %v", err)
		}
		slog.Info("OIDC enabled", "issuer", cfg.OIDC.Issuer)
	}

	slog.Info("listening", "bind", cfg.Bind)
	log.Fatal(http.ListenAndServe(cfg.Bind, auth.Middleware(app.Routes())))
}
