package app

import (
	"context"
	"flag"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"hmd/internal/auth"
	"hmd/internal/config"
	"hmd/internal/search"
	"hmd/internal/store"
	"hmd/internal/web"
	"hmd/internal/wiki"
)

var buildVersion = "dev"

func Run() {
	exportNS := flag.String("export-namespace", "", "export this namespace to static HTML and exit, instead of serving")
	exportDir := flag.String("export-dir", "", "output directory for -export-namespace")
	exportTitle := flag.String("export-title", "", "override the configured namespace title in the static export")
	healthcheck := flag.Bool("healthcheck", false, "check local readiness and exit")
	flag.Parse()
	if *healthcheck {
		if err := web.CheckReadiness(); err != nil {
			log.Fatal(err)
		}
		return
	}

	web.SetBuildVersion(buildVersion)
	web.LoadThemeDefaults()
	cfg, err := config.LoadConfig()
	if err != nil {
		log.Fatalf("Loading config: %v", err)
	}
	level := slog.LevelInfo
	if cfg.Debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
	for _, path := range []string{cfg.AppDir, cfg.RepoDir} {
		if err := config.ValidateWritableDataDir(path); err != nil {
			log.Fatalf("unsafe data directory %s: %v", path, err)
		}
	}

	content, err := store.Open(store.Options{
		RepoDir: cfg.RepoDir, DefaultBranch: cfg.DefaultBranch,
		Git: store.GitOptions{RemoteURL: cfg.Git.RemoteURL, User: cfg.Git.User, Token: cfg.Git.Token},
	})
	if err != nil {
		log.Fatalf("open store failed: %v", err)
	}
	wikiConfig, _, err := wiki.LoadWikiConfig(cfg.RepoDir)
	if err != nil {
		slog.Warn("loading wiki config", "err", err)
		wikiConfig = wiki.DefaultConfig()
	}
	paths, err := content.List()
	if err != nil {
		log.Fatalf("store.List failed: %v", err)
	}
	pages := make([]wiki.Page, 0, len(paths))
	hashes := make(map[string]string, len(paths))
	for _, path := range paths {
		raw, hash, err := content.Read(path)
		if err != nil {
			log.Fatalf("Reading %s: %v", path, err)
		}
		slug := path[:len(path)-3]
		pages = append(pages, wiki.ParsePage(slug, raw))
		hashes[slug] = hash
	}

	var documents *search.DocumentSearch
	if cfg.TikaURL != "" {
		documents, err = search.NewDocumentSearch(search.DocumentOptions{
			TikaURL: cfg.TikaURL, ModelDir: cfg.DocumentSearch.ModelDir, Model: cfg.DocumentSearch.Model,
		})
		if err != nil {
			log.Fatalf("setting up document search failed: %v", err)
		}
		documents.SetStore(content)
	}
	attachmentHashes := map[string]string{}
	if documents != nil {
		attachmentPaths, err := content.ListAttachments()
		if err != nil {
			log.Fatalf("store.ListAttachments failed: %v", err)
		}
		for _, path := range attachmentPaths {
			file, hash, err := content.OpenAttachment(path)
			if err != nil {
				slog.Warn("reading attachment hash", "path", path, "err", err)
				continue
			}
			_ = file.Close()
			attachmentHashes[path] = hash
		}
	}
	indexDir := cfg.DocumentSearch.IndexDir
	if indexDir == "" {
		indexDir = filepath.Join(cfg.AppDir, "search.bleve")
	}
	index, err := search.OpenIndexAt(indexDir, pages, hashes, attachmentHashes, documents)
	if err != nil {
		log.Fatalf("open index failed: %v", err)
	}
	namespaces, err := web.BuildNamespaceRegistryFromStore(content)
	if err != nil {
		log.Fatalf("build namespace registry failed: %v", err)
	}
	renderer := wiki.NewRenderer(index.ResolveLink)
	if *exportNS != "" {
		if *exportDir == "" {
			log.Fatal("-export-namespace requires -export-dir")
		}
		if err := web.ExportNamespace(pages, renderer, namespaces, content, *exportNS, *exportDir, *exportTitle); err != nil {
			log.Fatalf("export failed: %v", err)
		}
		return
	}

	authn, err := auth.Open(auth.Options{AppDir: cfg.AppDir, AdminUser: cfg.AdminUser, AdminPass: cfg.AdminPass})
	if err != nil {
		log.Fatalf("open auth failed: %v", err)
	}
	if !authn.HasUsers() && cfg.OIDC.Issuer == "" {
		log.Fatal("no users.json: set HMD_ADMIN_USER and HMD_ADMIN_PASSWORD or configure OIDC admission")
	}
	templates, err := web.ParseTemplates()
	if err != nil {
		log.Fatalf("Parsing templates: %v", err)
	}
	application := &web.App{Store: content, Auth: authn, Index: index, Render: renderer, Tmpl: templates}
	application.SetConfig(cfg)
	application.SetWikiConfig(wikiConfig)
	application.SetNamespaces(namespaces)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go web.PollFS(ctx, content, index, hashes, application.SetNamespaces, application.SetWikiConfig)
	if cfg.OIDC.Issuer != "" {
		application.OIDC, err = web.NewOIDCAuth(context.Background(), cfg)
		if err != nil {
			log.Fatalf("setting up OIDC failed: %v", err)
		}
	}

	server := newHTTPServer(cfg.Bind, web.Handler(application))
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			slog.Error("graceful shutdown failed", "err", err)
			_ = server.Close()
		}
		if err := content.WaitForPushes(shutdown); err != nil {
			slog.Error("waiting for Git pushes", "err", err)
		}
		if err := index.Close(); err != nil {
			slog.Warn("closing search index", "err", err)
		}
	}()
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func newHTTPServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr: address, Handler: handler, ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 1 << 20,
	}
}
