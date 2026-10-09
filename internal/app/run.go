package app

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"hmd/internal/api"
	"hmd/internal/auth"
	"hmd/internal/config"
	"hmd/internal/export"
	"hmd/internal/oauthserver"
	"hmd/internal/search"
	"hmd/internal/store"
	"hmd/internal/web"
	"hmd/internal/wiki"
)

var buildVersion = "dev"

func debugStartupStage(name string, attrs ...any) func(...any) {
	started := time.Now()
	base := append([]any{"stage", name}, attrs...)
	slog.Debug("startup stage started", base...)
	return func(doneAttrs ...any) {
		completed := append([]any{}, base...)
		completed = append(completed, "duration", time.Since(started))
		completed = append(completed, doneAttrs...)
		slog.Debug("startup stage completed", completed...)
	}
}

func Run() {
	exportNS := flag.String("export-namespace", "", "export this namespace to static HTML and exit, instead of serving")
	exportDir := flag.String("export-dir", "", "output directory for -export-namespace")
	exportTitle := flag.String("export-title", "", "override the configured namespace title in the static export")
	exportBaseURL := flag.String("export-base-url", "", "published static site URL; overrides namespace export.base_url")
	exportUseMain := flag.Bool("export-use-main-base-url", false, "explicitly use the main base_url for the static export")
	exportSitemap := flag.Bool("export-sitemap", true, "generate sitemap.xml; overrides namespace setting when supplied")
	exportBots := flag.String("export-robots-allow", "", "comma-separated allowed bot names; all other bots are blocked")
	exportLinks := flag.String("export-links", "", "JSON array of external links with label, url, optional Font Awesome icon, location (sidebar/topbar) and icon_only")
	healthcheck := flag.Bool("healthcheck", false, "check local readiness and exit")
	provisionOAuthClient := flag.Bool("oauth-client-add", false, "provision a pre-registered MCP OAuth client and exit")
	disableOAuthClient := flag.Bool("oauth-client-disable", false, "permanently disable an OAuth client and revoke its grants")
	oauthClientName := flag.String("oauth-client-name", "", "display name for the OAuth client")
	oauthClientID := flag.String("oauth-client-id", "", "OAuth client ID to disable")
	oauthClientAuth := flag.String("oauth-client-auth-method", "none", "OAuth client authentication: none, client_secret_basic or client_secret_post")
	oauthClientScopeText := flag.String("oauth-client-scopes", "read", "comma-separated action scopes allowed for the client")
	var oauthClientRedirects stringListFlag
	flag.Var(&oauthClientRedirects, "oauth-client-redirect-uri", "exact client callback URI; may be repeated")
	flag.Parse()
	if *healthcheck {
		if err := CheckReadiness(); err != nil {
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
	if *provisionOAuthClient && *disableOAuthClient {
		log.Fatal("-oauth-client-add and -oauth-client-disable cannot be used together")
	}
	if *disableOAuthClient {
		if *oauthClientID == "" {
			log.Fatal("-oauth-client-disable requires -oauth-client-id")
		}
		if err := config.ValidateWritableDataDir(cfg.AppDir); err != nil {
			log.Fatalf("unsafe app directory: %v", err)
		}
		if err := DisableOAuthClient(cfg, *oauthClientID); err != nil {
			log.Fatalf("disabling OAuth client failed: %v", err)
		}
		_, _ = fmt.Fprintf(os.Stdout, "OAuth client disabled and grants revoked: %s\n", *oauthClientID)
		return
	}
	level := slog.LevelInfo
	if cfg.Debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
	slog.Debug("startup configuration loaded", "app_dir", cfg.AppDir, "repo_dir", cfg.RepoDir, "sync_mode", cfg.SyncMode, "document_search", cfg.TikaURL != "", "oidc", cfg.OIDC.Issuer != "")
	if *provisionOAuthClient {
		if *oauthClientName == "" || len(oauthClientRedirects) == 0 {
			log.Fatal("-oauth-client-add requires -oauth-client-name and at least one -oauth-client-redirect-uri")
		}
		if err := config.ValidateWritableDataDir(cfg.AppDir); err != nil {
			log.Fatalf("unsafe app directory: %v", err)
		}
		scopes := strings.Split(*oauthClientScopeText, ",")
		client, err := ProvisionOAuthClient(cfg, *oauthClientName, oauthClientRedirects, *oauthClientAuth, scopes)
		if err != nil {
			log.Fatalf("provisioning OAuth client failed: %v", err)
		}
		_, _ = fmt.Fprintf(os.Stdout, "Client ID: %s\n", client.ClientID)
		if client.Secret != "" {
			_, _ = fmt.Fprintf(os.Stdout, "Client secret (shown once): %s\n", client.Secret)
		}
		return
	}
	finishStage := debugStartupStage("validate data directories")
	for _, path := range []string{cfg.AppDir, cfg.RepoDir} {
		slog.Debug("validating data directory", "path", path)
		if err := config.ValidateWritableDataDir(path); err != nil {
			log.Fatalf("unsafe data directory %s: %v", path, err)
		}
	}
	finishStage()

	finishStage = debugStartupStage("open repository", "path", cfg.RepoDir, "remote", cfg.Git.RemoteURL != "")
	content, err := store.Open(store.Options{
		RepoDir: cfg.RepoDir, DefaultBranch: cfg.DefaultBranch,
		Git: store.GitOptions{RemoteURL: cfg.Git.RemoteURL, User: cfg.Git.User, Token: cfg.Git.Token},
	})
	if err != nil {
		log.Fatalf("open store failed: %v", err)
	}
	finishStage()
	finishStage = debugStartupStage("load wiki configuration")
	wikiConfig, _, err := wiki.LoadWikiConfig(cfg.RepoDir)
	if err != nil {
		slog.Warn("loading wiki config", "err", err)
		wikiConfig = wiki.DefaultConfig()
	}
	finishStage()
	finishStage = debugStartupStage("list repository pages")
	paths, err := content.List()
	if err != nil {
		log.Fatalf("store.List failed: %v", err)
	}
	finishStage("pages", len(paths))
	finishStage = debugStartupStage("read repository pages", "pages", len(paths))
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
	finishStage()

	var documents *search.DocumentSearch
	if cfg.TikaURL != "" {
		finishStage = debugStartupStage("initialize document search", "model", cfg.DocumentSearch.Model)
		documents, err = search.NewDocumentSearch(search.DocumentOptions{
			TikaURL: cfg.TikaURL, ModelDir: cfg.DocumentSearch.ModelDir, Model: cfg.DocumentSearch.Model,
		})
		if err != nil {
			log.Fatalf("setting up document search failed: %v", err)
		}
		documents.SetStore(content)
		finishStage()
	}
	attachmentHashes := map[string]string{}
	if documents != nil {
		finishStage = debugStartupStage("list attachments")
		attachmentPaths, err := content.ListAttachments()
		if err != nil {
			log.Fatalf("store.ListAttachments failed: %v", err)
		}
		finishStage("attachments", len(attachmentPaths))
		finishStage = debugStartupStage("read attachment hashes", "attachments", len(attachmentPaths))
		for _, path := range attachmentPaths {
			file, hash, err := content.OpenAttachment(path)
			if err != nil {
				slog.Warn("reading attachment hash", "path", path, "err", err)
				continue
			}
			_ = file.Close()
			attachmentHashes[path] = hash
		}
		finishStage()
	}
	indexDir := cfg.DocumentSearch.IndexDir
	if indexDir == "" {
		indexDir = filepath.Join(cfg.AppDir, "search.bleve")
	}
	finishStage = debugStartupStage("open search index", "path", indexDir, "pages", len(pages), "attachments", len(attachmentHashes))
	index, err := search.OpenIndexAt(indexDir, pages, hashes, attachmentHashes, documents)
	if err != nil {
		log.Fatalf("open index failed: %v", err)
	}
	finishStage()
	finishStage = debugStartupStage("build namespace registry")
	namespaces, err := api.BuildNamespaceRegistryFromStore(content)
	if err != nil {
		log.Fatalf("build namespace registry failed: %v", err)
	}
	finishStage("namespaces", len(namespaces))
	renderer := wiki.NewRenderer(index.ResolveLink)
	if *exportNS != "" {
		if *exportDir == "" {
			log.Fatal("-export-namespace requires -export-dir")
		}
		exportConfig := namespaces[*exportNS]
		provided := map[string]bool{}
		flag.Visit(func(f *flag.Flag) {
			provided[f.Name] = true
		})
		exportConfig.Export, err = resolveExportSettings(exportConfig.Export, cfg.BaseURL, exportCLIOptions{
			BaseURL: *exportBaseURL, UseMain: *exportUseMain, Sitemap: *exportSitemap, Bots: *exportBots, Links: *exportLinks, Provided: provided,
		})
		if err != nil {
			log.Fatalf("export failed: %v", err)
		}
		if err := export.Namespace(export.NamespaceRequest{
			Pages:      pages,
			Namespace:  *exportNS,
			Config:     exportConfig,
			OutDir:     *exportDir,
			Title:      *exportTitle,
			Assets:     web.StaticAssets(),
			AssetsRoot: "web/static",
			Store:      content,
		}, web.NewStaticExporter(renderer)); err != nil {
			log.Fatalf("export failed: %v", err)
		}
		return
	}

	finishStage = debugStartupStage("open authentication store")
	authn, err := auth.Open(auth.Options{AppDir: cfg.AppDir, AdminUser: cfg.AdminUser, AdminPass: cfg.AdminPass})
	if err != nil {
		log.Fatalf("open auth failed: %v", err)
	}
	finishStage()
	if !authn.HasUsers() && cfg.OIDC.Issuer == "" {
		log.Fatal("no users.json: set HMD_ADMIN_USER and HMD_ADMIN_PASSWORD or configure OIDC admission")
	}
	var oauthService *oauthserver.Service
	if cfg.OAuth.Enabled {
		oauthState, err := oauthserver.OpenStore(cfg.AppDir)
		if err != nil {
			log.Fatalf("open OAuth state failed: %v", err)
		}
		oauthService, err = oauthserver.NewService(oauthserver.ServerOptions{
			Issuer: cfg.BaseURL, AllowAdminDelegation: cfg.OAuth.AllowAdminDelegation,
			DynamicRegistration: cfg.OAuth.DynamicRegistration,
		}, oauthState, authn)
		if err != nil {
			_ = oauthState.Close()
			log.Fatalf("configure OAuth failed: %v", err)
		}
		authn.SetOAuthBearerVerifier(oauthService.BearerVerifier())
	}
	finishStage = debugStartupStage("parse templates")
	templates, err := web.ParseTemplates()
	if err != nil {
		log.Fatalf("Parsing templates: %v", err)
	}
	finishStage()
	apiClient := api.New(content, index, authn)
	if oauthService != nil {
		apiClient.SetOAuthUploadAuthorizer(oauthService)
	}
	application := &web.App{API: apiClient, Auth: authn, Render: renderer, Tmpl: templates, OAuth: oauthService}
	application.SetConfig(cfg)
	application.SetWikiConfig(wikiConfig)
	application.SetNamespaces(namespaces)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go pollFS(ctx, application.API, hashes)
	if cfg.OIDC.Issuer != "" {
		finishStage = debugStartupStage("initialize OIDC", "issuer", cfg.OIDC.Issuer)
		application.OIDC, err = web.NewOIDCAuth(context.Background(), cfg)
		if err != nil {
			log.Fatalf("setting up OIDC failed: %v", err)
		}
		finishStage()
	}

	server := newHTTPServer(cfg.Bind, newHandler(application))
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			slog.Error("graceful shutdown failed", "err", err)
			_ = server.Close()
		}
		application.CloseExportPreviews()
		if err := content.WaitForPushes(shutdown); err != nil {
			slog.Error("waiting for Git pushes", "err", err)
		}
		if err := index.Close(); err != nil {
			slog.Warn("closing search index", "err", err)
		}
		if oauthService != nil {
			if err := oauthService.Close(); err != nil {
				slog.Warn("closing OAuth state store", "err", err)
			}
		}
	}()
	slog.Debug("startup completed; HTTP server listening", "bind", cfg.Bind)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

type stringListFlag []string

func (s *stringListFlag) String() string { return strings.Join(*s, ",") }

func (s *stringListFlag) Set(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("value must not be empty")
	}
	*s = append(*s, value)
	return nil
}
