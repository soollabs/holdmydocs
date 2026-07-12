package main

import (
	"bufio"
	"embed"
	"fmt"
	"html/template"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strings"
)

//go:embed all:web
var webFS embed.FS

// parseTemplates builds one template set per page: each page template
// defines "content", so they cannot share a set with one another and are
// each paired with the shared base layout instead.
func parseTemplates() (map[string]*template.Template, error) {
	tmpl := make(map[string]*template.Template)
	for _, name := range []string{"login", "page", "edit", "conflict", "create", "search", "history", "tags", "settings", "hidden"} {
		t, err := template.ParseFS(webFS, "web/templates/base.html", "web/templates/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", name, err)
		}
		tmpl[name] = t
	}
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

	// Handle subcommands
	if len(os.Args) > 1 && os.Args[1] == "adduser" {
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "Usage: hmd adduser <name>")
			os.Exit(1)
		}
		name := os.Args[2]

		// Read password from stdin
		fmt.Fprintf(os.Stderr, "Password: ")
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			fmt.Fprintln(os.Stderr, "Failed to read password")
			os.Exit(1)
		}
		password := strings.TrimSpace(scanner.Text())

		auth, err := OpenAuth(cfg)
		if err != nil {
			log.Fatalf("OpenAuth failed: %v", err)
		}

		err = auth.AddUser(name, password)
		if err != nil {
			log.Fatalf("AddUser failed: %v", err)
		}

		fmt.Printf("User %q added\n", name)
		return
	}

	slog.Info("hmd starting", "bind", cfg.Bind, "debug", cfg.Debug, "sync_mode", cfg.SyncMode)

	// Open store
	store, err := OpenStore(cfg)
	if err != nil {
		log.Fatalf("OpenStore failed: %v", err)
	}

	// Load all pages and build index
	paths, err := store.List()
	if err != nil {
		log.Fatalf("Store.List failed: %v", err)
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

	index, err := BuildIndex(pages)
	if err != nil {
		log.Fatalf("BuildIndex failed: %v", err)
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
		log.Fatalf("OpenAuth failed: %v", err)
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

	slog.Info("listening", "bind", cfg.Bind)
	log.Fatal(http.ListenAndServe(cfg.Bind, auth.Middleware(app.Routes())))
}
