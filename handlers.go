package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// buildVersion is baked in at compile time via -ldflags "-X main.buildVersion=..."
// (see Dockerfile's VERSION build arg). HMD_VERSION overrides it at runtime
// if set. Shown on the login screen and sidebar footer.
var buildVersion = "dev"
var version = envOr("HMD_VERSION", buildVersion)

type App struct {
	cfg        atomic.Pointer[Config]
	namespaces atomic.Pointer[NamespaceRegistry]
	Store      *Store
	Auth       *Auth
	Index      *Index
	Render     *Renderer
	Tmpl       map[string]*template.Template
	OIDC       *OIDCAuth // nil when OIDC is disabled
}

// config returns the current configuration value.
func (app *App) config() Config {
	return *app.cfg.Load()
}

// SetConfig stores a new configuration value atomically.
func (app *App) SetConfig(cfg Config) {
	app.cfg.Store(&cfg)
}

// Namespaces returns the current namespace registry.
func (app *App) Namespaces() NamespaceRegistry {
	return *app.namespaces.Load()
}

// SetNamespaces stores a newly rebuilt namespace registry atomically —
// called at startup and on every pollFS tick (search.go), since the repo
// mutates underneath the app via sync and external edits.
func (app *App) SetNamespaces(reg NamespaceRegistry) {
	app.namespaces.Store(&reg)
}

type BacklinkEntry struct{ Slug, Title string }

type TagChip struct{ Tag, Slug string }

type SearchResult struct {
	Slug    string
	Title   string
	Snippet template.HTML // bleve highlight fragments contain <mark> tags
	Tags    []string
}

type HistoryEntry struct {
	Hash      string
	ShortHash string
	Message   string
	Author    string
	When      string
	Current   bool
}

type TemplateData struct {
	SiteName         string
	Authed           bool
	Title            string
	Slug             string
	Content          template.HTML
	Body             string
	BaseHash         string
	Backlinks        []BacklinkEntry
	TagsInput        string
	PageTags         []TagChip
	AllTags          []TagCount
	TagName          string
	TagPages         []BacklinkEntry
	SyncState        string
	Error            string
	Query            string
	SearchResults    []SearchResult
	HistoryEntries   []HistoryEntry
	RevHash          string
	OldVersionDate   string
	TotalHistory     int
	MermaidNeeded    bool
	RevisionCount    int
	HeadShortHash    string
	HeadAuthor       string
	HeadWhen         string // relative, e.g. "3 hours ago"
	Username         string
	StatusMode       string // view|edit|search|log|conflict, drives the statusline mode block
	StatusContext    string // right-aligned context: revision count / word counts
	Version          string // shown on login intro
	RemoteHost       string // host of the git remote, for login intro (empty if none)
	OIDCEnabled      bool   // show the SSO button on the login page
	OIDCButtonText   string // SSO button label
	OIDCLocalLogin   bool   // show the password form alongside SSO
	OIDCIcon         bool   // show the icon (served at /auth/oidc/icon) on the SSO button
	SearchElapsed    string // search timing, e.g. "3ms"
	SearchPages      int    // total pages, for search stats
	SearchHits       int    // match count, for search stats
	SyncPollMs       int    // injected as a JS global for sync polling
	SyncMode         string
	BlobHash         string // current page blob hash, for client-side change detection
	ThemeStyle       template.CSS
	Skin             string // structural skin name; empty = default, only ever a known skinNames entry
	Settings         *SettingsData
	SetupHomePreview template.HTML
	SetupHelpPreview template.HTML
	NeedsSetup       bool
	NeedsHomeSetup   bool
	NeedsHelpSetup   bool
	HomeFileExists   bool
	HelpFileExists   bool
	HomeFilename     string
	RoutePrefix      string
	IsHidden         bool
	IsPublic         bool       // frontmatter public flag, drives the editor checkbox
	RecentCommits    []LogEntry // sidebar LOG section: last commits for the current page
	HealthMissing    int
	HealthOrphans    int
	SyncAge          string // relative age of the last successful sync, e.g. "12 seconds ago"
	SyncLastUnix     int64  // raw timestamp for the client-side sync-age ticker
	SidebarWidgets   []*widget
	RailWidgets      []*widget
	PageHeadWidgets  []*widget
	PageFootWidgets  []*widget
	StatusVariant    string // skin.Status: full | write | quiet

	// Widget data — populated in app.render only when something on the page
	// actually reads it (see populateWidgetData).
	Calendar     CalendarMonth
	WritingStats WritingStats
	PinnedPages  []BacklinkEntry
	PrevEntries  []PrevEntry
	DailyEnabled bool // skin.DailyKey != "" — gates the ctrl-j shortcut and >daily verb client-side
}

// LogEntry is one row in the sidebar LOG section.
type LogEntry struct {
	Age     string
	Message string
}

// FieldState describes one config field's display state for the settings page.
type FieldState struct {
	Value           string // current runtime value (or "set"/"not set" for secrets)
	Editable        bool   // can the user change this from the UI?
	RestartRequired bool   // does the change only take effect on restart?
	EnvVar          string // non-empty if an env var overrides this field
	BootstrapOnly   bool   // true for AdminUser/AdminPass (CLI-managed)
}

// SettingsData is passed to the settings template.
type SettingsData struct {
	Fields           map[string]FieldState
	ConfigPath       string
	Flash            string // success message after a save
	Error            string // validation error
	MaxUploadBytes   int64
	SyncPollMs       int
	SyncMode         string
	Palette          string
	PaletteNames     []string
	Palettes         map[string]themePreset // JSON-encoded into the settings page script
	FontUI           string
	FontMono         string
	FontsMono        []string // option groups for the font selects
	FontsSans        []string
	FontsSerif       []string
	FontStacks       map[string]string // JSON-encoded for the live font preview
	Skin             string
	SkinNames        []string
	Skins            map[string]skin
	SkinPalettes     map[string]string // skin -> default palette, JSON-encoded to move the selection on change
	Namespaces       []NamespaceListEntry
	HelpDrifted      bool
	UserGitAuthor    string        // current user's per-user git author override
	HomeFilename     string        // read-only display; restart required to change
	HomeFilenameEnv  string        // env var name if it overrides the file, else ""
	Tokens           []TokenView   // current user's personal access tokens
	NewToken         string        // freshly minted token value, shown exactly once
	TokenError       string        // token create/revoke validation error
	HasEnvOverrides  bool          // any field currently sourced from an env var — shows the "export to file" action
	ExportSecretVars []string      // env vars naming secrets export would write into the file in plaintext, e.g. "HMD_GIT_TOKEN"
	Users            []UserSummary // every user, for the users tab
	AllScopes        []string      // "read", "write", "settings" — the scope checkbox options
	CurrentUser      string        // name of the logged-in user, so the users tab can block self-lockout
	UserError        string        // create/scope-update validation error
}

// TokenView is a PAT as listed on the settings page (metadata only).
type TokenView struct {
	Name    string
	Created string
	Expires string // "never", a date, or "expired"
}

// relativeTime renders t as a short "N units ago" string, falling back to
// the date once it's more than a week old.
func relativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		n := int(d / time.Minute)
		return fmt.Sprintf("%d minute%s ago", n, plural(n))
	case d < 24*time.Hour:
		n := int(d / time.Hour)
		return fmt.Sprintf("%d hour%s ago", n, plural(n))
	case d < 7*24*time.Hour:
		n := int(d / (24 * time.Hour))
		return fmt.Sprintf("%d day%s ago", n, plural(n))
	default:
		return t.Format("2006-01-02")
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// remoteHost extracts the host portion of a remote URL (HTTPS or SSH).
// Returns the empty string when raw is empty so the login intro can omit the line.
func remoteHost(raw string) string {
	if raw == "" {
		return ""
	}
	s := raw
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	} else if i := strings.Index(s, "@"); i >= 0 {
		// git@host:path form
		s = s[i+1:]
	}
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[:i]
	}
	if i := strings.Index(s, ":"); i >= 0 {
		s = s[:i]
	}
	return s
}

// buildSettingsData constructs the display state for every config field.
// hasConfigFile is false when HMD_CONFIG_FILE is unset (whole page read-only).
// buildSettingsData constructs the display state for every config field.
// The config file itself is always writable (created on first save if
// missing) — a field is read-only only when an env var overrides it
// (cfg.EnvOverrides, keyed by fileConfig field name) or it's bootstrap-only.
func buildSettingsData(cfg Config, prefs userRecord, ns NamespaceRegistry) SettingsData {
	fields := make(map[string]FieldState)

	// mkField: editable = no env var and not bootstrap-only.
	mkField := func(value, fieldName string, restartRequired, bootstrapOnly bool) FieldState {
		envVar := ""
		if !bootstrapOnly {
			envVar = cfg.EnvOverrides[fieldName]
		}
		editable := envVar == "" && !bootstrapOnly
		return FieldState{
			Value:           value,
			Editable:        editable,
			RestartRequired: restartRequired,
			EnvVar:          envVar,
			BootstrapOnly:   bootstrapOnly,
		}
	}

	fields["Bind"] = mkField(cfg.Bind, "Bind", true, false)
	fields["RepoDir"] = mkField(cfg.RepoDir, "RepoDir", true, false)
	appDirEnv := ""
	if os.Getenv("HMD_APP_DIR") != "" {
		appDirEnv = "HMD_APP_DIR"
	}
	fields["AppDir"] = FieldState{Value: cfg.AppDir, Editable: false, RestartRequired: true, EnvVar: appDirEnv, BootstrapOnly: true}
	fields["RemoteURL"] = mkField(cfg.Git.RemoteURL, "Git.RemoteURL", false, false)
	fields["GitUser"] = mkField(cfg.Git.User, "Git.User", false, false)
	fields["GitAuthor"] = mkField(cfg.Git.Author, "Git.Author", false, false)

	// Token is special: masked, and locked if a token file is configured.
	tokenValue := "not set"
	if cfg.Git.Token != "" {
		tokenValue = "set"
	}
	tokenEnvVar := cfg.EnvOverrides["Git.Token"]
	if tokenEnvVar == "" {
		tokenEnvVar = cfg.EnvOverrides["Git.TokenFile"]
	}
	fields["GitToken"] = FieldState{
		Value:           tokenValue,
		Editable:        tokenEnvVar == "",
		RestartRequired: false,
		EnvVar:          tokenEnvVar,
		BootstrapOnly:   false,
	}

	fields["SiteName"] = mkField(cfg.SiteName, "SiteName", false, false)
	adminUserEnv := ""
	if os.Getenv("HMD_ADMIN_USER") != "" {
		adminUserEnv = "HMD_ADMIN_USER"
	}
	fields["AdminUser"] = FieldState{Value: cfg.AdminUser, Editable: false, EnvVar: adminUserEnv, BootstrapOnly: true}
	adminPassEnv := ""
	if os.Getenv("HMD_ADMIN_PASSWORD") != "" {
		adminPassEnv = "HMD_ADMIN_PASSWORD"
	}
	fields["AdminPass"] = FieldState{Value: cfg.AdminPass, Editable: false, EnvVar: adminPassEnv, BootstrapOnly: true}

	fields["SyncMode"] = mkField(cfg.SyncMode, "SyncMode", false, false)

	activeName, activeSkin := effectiveSkin(cfg, prefs)

	return SettingsData{
		Fields:           fields,
		ConfigPath:       cfg.ConfigFile,
		MaxUploadBytes:   cfg.MaxUploadBytes,
		SyncPollMs:       cfg.SyncPollMs,
		SyncMode:         cfg.SyncMode,
		Palette:          effectivePalette(prefs, activeSkin),
		PaletteNames:     themePresetNames,
		Palettes:         themePresets,
		FontUI:           prefs.FontUI,
		FontMono:         prefs.FontMono,
		FontsMono:        fontsMono,
		FontsSans:        fontsSans,
		FontsSerif:       fontsSerif,
		FontStacks:       fontStacks,
		Skin:             activeName,
		SkinNames:        skinNames,
		Skins:            skins,
		SkinPalettes:     skinPalettes(),
		Namespaces:       namespaceListEntries(ns),
		HomeFilename:     cfg.HomeFilename,
		HomeFilenameEnv:  cfg.EnvOverrides["HomeFilename"],
		HasEnvOverrides:  len(cfg.EnvOverrides) > 0,
		ExportSecretVars: exportSecretVars(cfg),
	}
}

// exportSecretVars lists the env vars naming secrets that toFileConfig
// would write into config.yaml in plaintext, for the settings-page warning.
// Git.TokenFile isn't listed: toFileConfig omits the resolved token when a
// token file is set, so exporting doesn't touch that secret.
func exportSecretVars(cfg Config) []string {
	var vars []string
	if v := cfg.EnvOverrides["Git.Token"]; v != "" {
		vars = append(vars, v)
	}
	if v := cfg.EnvOverrides["OIDC.ClientSecret"]; v != "" {
		vars = append(vars, v)
	}
	return vars
}

// render executes the named page template inside the shared layout.
// Every template set was parsed from base.html plus one content template.
func (app *App) render(w http.ResponseWriter, r *http.Request, status int, name string, data TemplateData) {
	data.SiteName = app.config().SiteName
	data.Version = version
	data.RemoteHost = remoteHost(app.config().Git.RemoteURL)
	if data.SyncState == "" {
		data.SyncState, _ = app.Store.SyncState()
	}
	if data.Authed && data.Username == "" {
		data.Username = app.currentUser(r)
	}
	if data.Authed && data.AllTags == nil {
		data.AllTags = app.Index.Tags()
	}
	if data.Authed {
		missing, orphans := app.Index.Health(app.config().HomeSlug())
		data.HealthMissing = len(missing)
		data.HealthOrphans = len(orphans)
	}
	if data.Authed && data.StatusMode == "" {
		data.StatusMode = "view"
	}
	if data.Authed {
		data.SyncLastUnix = app.Store.LastSyncUnix()
		if ts := data.SyncLastUnix; ts > 0 {
			if d := time.Since(time.Unix(ts, 0)); d < time.Minute {
				data.SyncAge = fmt.Sprintf("%ds ago", int(d.Seconds()))
			} else {
				data.SyncAge = relativeTime(time.Unix(ts, 0))
			}
		} else {
			data.SyncAge = "—"
		}
	}
	if data.Authed {
		cfg := app.config()
		prefs := app.Auth.prefs(app.currentUser(r))
		data.SyncPollMs = cfg.SyncPollMs
		data.SyncMode = cfg.SyncMode
		activeName, activeSkin := effectiveSkin(cfg, prefs)
		themePrefs := prefs
		themePrefs.Palette = effectivePalette(prefs, activeSkin)
		data.ThemeStyle = buildThemeStyle(themePrefs)
		data.Skin = activeName
		data.StatusVariant = activeSkin.Status
		data.DailyEnabled = activeSkin.DailyKey != ""
		data.HomeFilename = cfg.HomeFilename
		if app.Store.NeedsSetup.Load() || app.Store.ForceSetup.Load() {
			forced := app.Store.ForceSetup.Load()

			_, homeErr := os.Stat(filepath.Join(cfg.RepoDir, cfg.HomeFilename))
			homeMissing := homeErr != nil
			if homeMissing || forced {
				data.NeedsHomeSetup = true
				data.HomeFileExists = !homeMissing
				homePreview, _ := app.Render.Render(defaultHomeMD)
				data.SetupHomePreview = template.HTML(homePreview)
			}

			_, helpErr := os.Stat(filepath.Join(app.config().RepoDir, ".help.md"))
			helpMissing := helpErr != nil
			if helpMissing || forced {
				data.NeedsHelpSetup = true
				data.HelpFileExists = !helpMissing
				helpPreview, _ := app.Render.Render(defaultHelpMD)
				data.SetupHelpPreview = template.HTML(helpPreview)
			}

			data.NeedsSetup = data.NeedsHomeSetup || data.NeedsHelpSetup
		}

		nsCfg := app.Namespaces().Resolve(data.Slug)
		data.SidebarWidgets = widgetsForSlot(slotSidebar, nsCfg.Widgets)
		data.RailWidgets = widgetsForSlot(slotRail, nsCfg.Widgets)
		data.PageHeadWidgets = widgetsForSlot(slotPageHead, nsCfg.Widgets)
		data.PageFootWidgets = widgetsForSlot(slotPageFoot, nsCfg.Widgets)

		app.populateWidgetData(&data, activeSkin)
	}

	// Load mermaid only when the page content or editor body contains
	// mermaid code blocks. This avoids a ~1MB script on every page.
	if strings.Contains(string(data.Content), "class=\"mermaid\"") ||
		strings.Contains(data.Body, "mermaid") {
		data.MermaidNeeded = true
	}

	tmpl, ok := app.Tmpl[name]
	if !ok {
		http.Error(w, "template not found: "+name, http.StatusInternalServerError)
		return
	}

	// Render to a buffer first so a template error becomes a clean 500
	// rather than a half-written page.
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "layout", data); err != nil {
		slog.Error("rendering template", "name", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if _, err := buf.WriteTo(w); err != nil {
		slog.Error("writing response", "name", name, "err", err)
	}
}

func (app *App) currentUser(r *http.Request) string {
	// Bearer-authenticated requests carry the username in the context.
	if user, ok := r.Context().Value(ctxUserKey{}).(string); ok {
		return user
	}
	cookie, err := r.Cookie("hmd_session")
	if err != nil {
		return ""
	}
	user, _ := app.Auth.UserFor(cookie.Value)
	return user
}

// gitAuthor resolves the commit identity for username: the user's own override,
// else the global HMD_GIT_AUTHOR default, else "<username> <username@hmd.local>".
func (app *App) gitAuthor(username string) (name, email string) {
	raw := app.Auth.AuthorFor(username)
	if raw == "" {
		raw = app.config().Git.Author
	}
	return parseAuthor(raw, username)
}

// tocToken matches a <!-- hmd:toc --> or <!-- hmd:toc:tag1,tag2 --> token.
var tocToken = regexp.MustCompile(`<!-- hmd:toc(?::([a-z0-9,-]+))? -->`)

// injectTOC replaces hmd:toc tokens in body with markdown bullet lists of
// pages. With no tag list, all pages are listed (excluding the home page).
// With a comma-separated tag list, only pages matching ANY tag are included
// (OR). Results are sorted alphabetically by title; the home page is always
// excluded. The list is built as [[wiki-links]] so the existing wiki-link
// preprocessor renders the anchors.
func injectTOC(body string, ix *Index, homeSlug string) string {
	if !strings.Contains(body, "hmd:toc") {
		return body
	}
	titles := ix.Titles()
	return tocToken.ReplaceAllStringFunc(body, func(match string) string {
		tagList := ""
		if m := tocToken.FindStringSubmatch(match); m != nil {
			tagList = m[1]
		}
		var slugs []string
		if tagList == "" {
			for slug := range titles {
				if slug != homeSlug {
					slugs = append(slugs, slug)
				}
			}
		} else {
			tagSlugs := strings.Split(tagList, ",")
			for _, s := range ix.PagesForTags(tagSlugs) {
				if s != homeSlug {
					slugs = append(slugs, s)
				}
			}
		}
		type entry struct{ title, slug string }
		entries := make([]entry, 0, len(slugs))
		for _, s := range slugs {
			title := titles[s]
			if title == "" {
				title = s
			}
			entries = append(entries, entry{title, s})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].title < entries[j].title })
		var b strings.Builder
		for _, e := range entries {
			b.WriteString("- [[")
			b.WriteString(e.title)
			b.WriteString("]]\n")
		}
		return b.String()
	})
}

// handleRoot resolves the current user's skin (falling back to the
// site-wide default) and redirects "/" to that skin's Landing target:
// "home" -> the configured home page, "daily" -> today's daily entry (in
// edit mode — journal is the only skin that sets this). An
// unauthenticated request never reaches here (the auth
// middleware redirects to /login first), so app.currentUser is always valid.
func (app *App) handleRoot(w http.ResponseWriter, r *http.Request) {
	cfg := app.config()
	prefs := app.Auth.prefs(app.currentUser(r))
	_, s := effectiveSkin(cfg, prefs)

	switch s.Landing {
	case "daily":
		http.Redirect(w, r, "/daily/"+time.Now().Format("2006-01-02")+"?do=edit", http.StatusSeeOther)
	default:
		http.Redirect(w, r, "/"+cfg.HomeSlug(), http.StatusSeeOther)
	}
}

func (app *App) Routes() http.Handler {
	mux := http.NewServeMux()

	// Root: redirect to the current user's skin landing target.
	mux.HandleFunc("GET /{$}", app.handleRoot)

	// Everything under /_/ is the app itself — the one reserved top-level
	// segment a namespace may never take. Content owns everything else.

	// Setup endpoint: seeds the home file + .help.md, clears the setup flag
	mux.HandleFunc("POST /_/setup", app.handleSetup)

	// Static files. embed.FS carries no real mtime/ETag, so browsers have
	// nothing to conditionally revalidate against and can cache a stale
	// copy indefinitely across binary rebuilds — force revalidation instead.
	fsys, _ := fs.Sub(webFS, "web/static")
	staticHandler := http.StripPrefix("/_/static/", http.FileServerFS(fsys))
	mux.Handle("GET /_/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		staticHandler.ServeHTTP(w, r)
	}))

	// Login handlers
	mux.HandleFunc("GET /_/login", app.handleLoginGet)
	mux.HandleFunc("POST /_/login", app.handleLoginPost)
	mux.HandleFunc("POST /_/logout", app.handleLogout)
	mux.HandleFunc("GET /_/auth/oidc/login", app.handleOIDCLogin)
	mux.HandleFunc("GET /_/auth/oidc/callback", app.handleOIDCCallback)
	mux.HandleFunc("GET /_/auth/oidc/icon", app.handleOIDCIcon)

	// Tags: page index, not real content.
	mux.HandleFunc("GET /_/tags", app.handleTagsIndex)
	mux.HandleFunc("GET /_/tags/{tag}", app.handleTagPages)

	// Settings
	mux.HandleFunc("GET /_/settings", app.handleSettingsGet)
	mux.HandleFunc("GET /_/admin", app.handleAdminGet)
	mux.HandleFunc("POST /_/admin", app.handleSettingsPost)
	mux.HandleFunc("POST /_/settings/appearance", app.handleSettingsAppearance)
	mux.HandleFunc("POST /_/settings/export", app.handleSettingsExport)
	mux.HandleFunc("POST /_/settings/author", app.handleSetAuthor)
	mux.HandleFunc("POST /_/settings/tokens", app.handleCreateToken)
	mux.HandleFunc("POST /_/settings/tokens/revoke", app.handleRevokeToken)
	mux.HandleFunc("POST /_/settings/users", app.handleCreateUser)
	mux.HandleFunc("POST /_/settings/users/scopes", app.handleSetUserScopes)
	mux.HandleFunc("POST /_/settings/setup", app.handleRerunSetup)
	mux.HandleFunc("POST /_/settings/help/reset", app.handleResetHelp)

	// Search
	mux.HandleFunc("GET /_/search", app.handleSearch)
	mux.HandleFunc("GET /_/health-report", app.handleHealthReport)

	// Hidden page handlers (dot-prefixed files, an app-internal drafting
	// namespace — never addressable content, so it lives under /_/ too).
	// Actions are ?do= query params, same as ordinary pages.
	mux.HandleFunc("GET /_/hidden", app.handleHiddenIndex)
	mux.HandleFunc("GET /_/hidden/{path...}", app.handleHiddenGet)
	mux.HandleFunc("POST /_/hidden/{path...}", app.handleHiddenPost)

	// Digital garden: public read-only namespace. 404s unless
	// HMD_GARDEN_ENABLED (and stays behind the auth redirect when disabled).
	mux.HandleFunc("GET /garden", app.handleGardenIndex)
	mux.HandleFunc("GET /garden/{slug}", app.handleGardenPage)
	mux.HandleFunc("GET /garden/feed.xml", app.handleGardenFeed)
	mux.HandleFunc("GET /garden/attachments/{slug}/{file}", app.handleGardenAttachment)

	// MCP server (opt-in, restart-required): agents read and write the wiki
	// over streamable HTTP. Same middleware as /_/api/ — Bearer PAT, 401 JSON.
	if app.config().MCP.Enabled {
		mcpHandler := app.mcpHandler()
		// Explicit methods, not a bare "/_/mcp" pattern: a method-less
		// pattern can't coexist with the "GET /{path...}"/"POST /{path...}"
		// page dispatcher below — Go's mux requires one pattern to
		// dominate the other in both path and method specificity.
		mux.Handle("GET /_/mcp", mcpHandler)
		mux.Handle("POST /_/mcp", mcpHandler)
		mux.Handle("DELETE /_/mcp", mcpHandler)
	}

	// API endpoints
	mux.HandleFunc("GET /_/api/search", app.handleSearchAPI)
	mux.HandleFunc("GET /_/api/sync", app.handleSyncAPI)
	mux.HandleFunc("POST /_/api/sync/push-now", app.handleSyncPushNow)
	mux.HandleFunc("GET /_/api/preview/{slug}", app.handleAPIPreview)
	mux.HandleFunc("POST /_/api/preview", app.handlePreview)
	mux.HandleFunc("POST /_/api/attachments/{slug}", app.handleUploadAttachment)
	mux.HandleFunc("GET /_/attachments/{slug}/{file}", app.handleServeAttachment)

	// Content owns the root: one dispatcher for every page, GET and POST,
	// actions selected by ?do= rather than a path suffix. Go's ServeMux
	// prefers the more specific /_/... patterns above over this wildcard,
	// so /_/... never resolves here.
	mux.HandleFunc("GET /{path...}", app.handlePageGet)
	mux.HandleFunc("POST /{path...}", app.handlePagePost)

	return mux
}

// isSecureRequest reports whether the request arrived over TLS, either
// directly or (trusting the proxy) via X-Forwarded-Proto.
// trusts X-Forwarded-Proto unconditionally, no allowed-proxy list — fine here since a
// spoofed header only affects whether the cookie is marked Secure, add an allowlist if that changes.
func isSecureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// loginData builds the login page TemplateData, including OIDC display state.
func (app *App) loginData(errMsg string) TemplateData {
	cfg := app.config()
	return TemplateData{
		Title:          "Login",
		Error:          errMsg,
		OIDCEnabled:    cfg.OIDC.Issuer != "",
		OIDCButtonText: cfg.OIDC.ButtonText,
		OIDCLocalLogin: cfg.OIDC.Issuer == "" || cfg.OIDC.LocalLogin,
		OIDCIcon:       app.OIDC != nil && app.OIDC.icon != nil,
	}
}

func (app *App) handleLoginGet(w http.ResponseWriter, r *http.Request) {
	app.render(w, r, http.StatusOK, "login", app.loginData(""))
}

func (app *App) handleLoginPost(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")

	token, ok := app.Auth.Login(username, r.FormValue("password"))
	if !ok {
		slog.Warn("login failed", "username", username, "remote", r.RemoteAddr)
		app.render(w, r, http.StatusUnauthorized, "login", app.loginData("Invalid username or password"))
		return
	}

	slog.Info("login", "username", username, "remote", r.RemoteAddr)

	cookie := &http.Cookie{
		Name:     "hmd_session",
		Value:    token,
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		Path:     "/",
	}
	if r.FormValue("remember") != "" {
		cookie.MaxAge = 30 * 24 * 60 * 60 // 30 days
	}
	http.SetCookie(w, cookie)

	http.Redirect(w, r, "/"+app.config().HomeSlug(), http.StatusSeeOther)
}

func (app *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, _ := r.Cookie("hmd_session")
	if cookie != nil {
		app.Auth.Logout(cookie.Value)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "hmd_session",
		Value:    "",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		Path:     "/",
	})

	http.Redirect(w, r, "/_/login", http.StatusSeeOther)
}

// handleSetup processes the setup form. Nothing is seeded without explicit
// consent: action=="add" seeds whichever of the home file / .help.md the user
// ticked (offered when missing, or always when the modal was forced open
// via "re-run setup" — ticking an existing file overwrites it); action==
// "skip" seeds nothing. Either way NeedsSetup/ForceSetup are cleared.
func (app *App) handleSetup(w http.ResponseWriter, r *http.Request) {
	action := r.FormValue("action")
	cfg := app.config()
	authorName, authorEmail := app.gitAuthor(cfg.Git.User)

	if action == "add" {
		if r.FormValue("add_home") == "on" {
			homeSlug := cfg.HomeSlug()
			content := Page{Slug: homeSlug, Title: homeSlug, Body: defaultHomeMD}.Encode()
			if _, err := app.Store.Save(cfg.HomeFilename, content, "Add "+cfg.HomeFilename, authorName, authorEmail); err != nil {
				http.Error(w, "failed to seed home page", http.StatusInternalServerError)
				return
			}
			if err := app.Index.Update(ParsePage(homeSlug, content)); err != nil {
				slog.Error("updating search index", "slug", homeSlug, "err", err)
			}
		}
		if r.FormValue("add_help") == "on" {
			content := Page{Slug: "help", Title: "Help", Tags: []string{"meta"}, Body: defaultHelpMD}.Encode()
			if _, err := app.Store.Save(".help.md", content, "Add .help.md", authorName, authorEmail); err != nil {
				http.Error(w, "failed to seed help guide", http.StatusInternalServerError)
				return
			}
		}
	}

	app.Store.NeedsSetup.Store(false)
	app.Store.ForceSetup.Store(false)
	http.Redirect(w, r, refererPath(r, "/"+cfg.HomeSlug()), http.StatusSeeOther)
}

// refererPath returns the path+query of the request's Referer header, so
// dismissing a modal returns the user to the page they were on rather than
// always redirecting to a fixed page. Falls back to fallback if the header
// is missing or unparseable.
func refererPath(r *http.Request, fallback string) string {
	ref := r.Referer()
	if ref == "" {
		return fallback
	}
	u, err := url.Parse(ref)
	if err != nil || u.Path == "" {
		return fallback
	}
	if u.RawQuery != "" {
		return u.Path + "?" + u.RawQuery
	}
	return u.Path
}

// handleRerunSetup reopens the setup modal with both items. Nothing is written
// until submission; selecting an existing file overwrites it.
func (app *App) handleRerunSetup(w http.ResponseWriter, r *http.Request) {
	app.Store.ForceSetup.Store(true)
	http.Redirect(w, r, "/_/admin", http.StatusSeeOther)
}

// handleSetAuthor stores the current user's git author override ("Name <email>",
// or empty to clear and fall back to the global default).
func (app *App) handleSetAuthor(w http.ResponseWriter, r *http.Request) {
	author := strings.TrimSpace(r.FormValue("git_author"))
	if err := app.Auth.SetAuthor(app.currentUser(r), author); err != nil {
		http.Error(w, "failed to save git author", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/_/settings", http.StatusSeeOther)
}

// handleResetHelp overwrites .help.md with the built-in default, clearing
// the drift warning on the settings page. Destructive to any local edits.
func (app *App) handleResetHelp(w http.ResponseWriter, r *http.Request) {
	authorName, authorEmail := app.gitAuthor(app.currentUser(r))
	content := Page{Slug: "help", Title: "Help", Tags: []string{"meta"}, Body: defaultHelpMD}.Encode()
	if _, err := app.Store.Save(".help.md", content, "Reset .help.md to built-in", authorName, authorEmail); err != nil {
		http.Error(w, "failed to reset .help.md", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/_/admin", http.StatusSeeOther)
}

func (app *App) handleViewPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")

	content, blobHash, err := app.Store.Read(pageFile(slug))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			app.render(w, r, http.StatusNotFound, "create", TemplateData{
				Authed: true,
				Title:  "Page not found",
				Slug:   slug,
			})
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	page := ParsePage(slug, content)
	page.Body = injectTOC(page.Body, app.Index, app.config().HomeSlug())
	renderedBody, err := app.Render.Render(page.Body)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	titles := app.Index.Titles()
	var backlinks []BacklinkEntry
	for _, bslug := range app.Index.Backlinks(slug) {
		backlinks = append(backlinks, BacklinkEntry{Slug: bslug, Title: titles[bslug]})
	}

	var pageTags []TagChip
	for _, tag := range page.Tags {
		pageTags = append(pageTags, TagChip{Tag: tag, Slug: Slugify(tag)})
	}

	revisionCount := 0
	headAuthor, headWhen := "", ""
	var recentCommits []LogEntry
	if history, err := app.Store.History(pageFile(slug)); err == nil {
		revisionCount = len(history)
		if len(history) > 0 {
			headAuthor = history[0].Author
			headWhen = relativeTime(history[0].When)
		}
		for _, c := range history[:min(3, len(history))] {
			recentCommits = append(recentCommits, LogEntry{
				Age:     relativeTime(c.When),
				Message: strings.TrimSpace(c.Message),
			})
		}
	}

	app.render(w, r, http.StatusOK, "page", TemplateData{
		Authed:        true,
		Title:         page.Title,
		Slug:          slug,
		Content:       renderedBody,
		Backlinks:     backlinks,
		PageTags:      pageTags,
		RevisionCount: revisionCount,
		HeadAuthor:    headAuthor,
		HeadWhen:      headWhen,
		BlobHash:      blobHash,
		StatusContext: fmt.Sprintf("%d revision%s", revisionCount, plural(revisionCount)),
		RecentCommits: recentCommits,
	})
}

// reservedPath reports whether path (as captured by {path...}) falls under
// the reserved "_" segment. Only an *exact* app route ever matches it as a
// literal http.ServeMux pattern; anything else under "_/" would otherwise
// fall through to this wildcard dispatcher and be treated as a page slug —
// "/_/…" must never resolve to content, registered route or not.
func reservedPath(path string) bool {
	return path == "_" || strings.HasPrefix(path, "_/")
}

// handlePageGet dispatches every read of a page's own URL. The action is
// selected by ?do= (edit/history/diff/rev), never by a path suffix — a page
// literally named "edit" is unambiguous, since "do" can never be part of
// the path. No do= at all means view. Any other value 404s rather than
// silently falling back to view, so a typoed ?do= doesn't look like success.
func (app *App) handlePageGet(w http.ResponseWriter, r *http.Request) {
	if reservedPath(r.PathValue("path")) {
		http.NotFound(w, r)
		return
	}
	r.SetPathValue("slug", r.PathValue("path"))
	switch r.URL.Query().Get("do") {
	case "":
		app.handleViewPage(w, r)
	case "edit":
		app.handleEditPage(w, r)
	case "history":
		app.handleHistory(w, r)
	case "diff":
		app.handlePageDiff(w, r)
	case "rev":
		app.handleViewRev(w, r)
	default:
		http.NotFound(w, r)
	}
}

// handlePagePost dispatches every write to a page's own URL, selected by
// ?do=.
func (app *App) handlePagePost(w http.ResponseWriter, r *http.Request) {
	if reservedPath(r.PathValue("path")) {
		http.NotFound(w, r)
		return
	}
	r.SetPathValue("slug", r.PathValue("path"))
	switch r.URL.Query().Get("do") {
	case "save":
		app.handleSavePage(w, r)
	case "revert":
		app.handleRevert(w, r)
	case "rename":
		app.handleRenamePage(w, r)
	case "tags":
		app.handleSetTags(w, r)
	default:
		http.NotFound(w, r)
	}
}

// handleHiddenGet/Post mirror handlePageGet/Post for the /_/hidden/{path...}
// subtree: only view/edit/save make sense for a hidden page (no history,
// diff, revert or rename UI exists for it today).
func (app *App) handleHiddenGet(w http.ResponseWriter, r *http.Request) {
	r.SetPathValue("slug", r.PathValue("path"))
	switch r.URL.Query().Get("do") {
	case "":
		app.handleViewHidden(w, r)
	case "edit":
		app.handleEditHidden(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (app *App) handleHiddenPost(w http.ResponseWriter, r *http.Request) {
	r.SetPathValue("slug", r.PathValue("path"))
	switch r.URL.Query().Get("do") {
	case "save":
		app.handleSaveHidden(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (app *App) handleEditPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")

	page := Page{Slug: slug, Title: slug}
	baseHash := ""

	content, hash, _ := app.Store.Read(pageFile(slug))
	if content != nil {
		page = ParsePage(slug, content)
		baseHash = hash
	} else if strings.HasPrefix(slug, dailyDatePrefix) {
		page.Tags = []string{"daily"}
	}

	app.render(w, r, http.StatusOK, "edit", TemplateData{
		Authed:     true,
		Title:      page.Title,
		Slug:       slug,
		Body:       page.Body,
		BaseHash:   baseHash,
		TagsInput:  strings.Join(page.Tags, ", "),
		StatusMode: "edit",
		IsHidden:   false,
		IsPublic:   page.Public,
	})
}

func (app *App) handleSavePage(w http.ResponseWriter, r *http.Request) {
	app.handleSave(w, r, pageFile(r.PathValue("slug")))
}

// handleSave is shared by /page/{slug}/save and /hidden/{slug}/save. The
// "hidden" checkbox in the edit form decides the destination file; if it
// differs from oldFile, the page moves between the two namespaces (and the
// search index is updated or cleared to match).
func (app *App) handleSave(w http.ResponseWriter, r *http.Request, oldFile string) {
	slug := r.PathValue("slug")
	title := r.FormValue("title")
	body := r.FormValue("body")
	tagsInput := r.FormValue("tags")
	basehash := r.FormValue("basehash")
	hidden := r.FormValue("hidden") == "on"
	public := r.FormValue("public") == "on"
	username := app.currentUser(r)

	newFile := pageFile(slug)
	newPrefix := ""
	if hidden {
		newFile = hiddenFile(slug)
		newPrefix = "/_/hidden"
	}

	cfg := app.config()
	if cfg.SyncMode == "bidirectional" && cfg.Git.RemoteURL != "" {
		if _, err := app.Store.FetchAndFF(); err != nil {
			slog.Warn("save-time fetch", "slug", slug, "err", err)
		}
	}

	page := Page{Slug: slug, Title: title, Tags: ParseTags(tagsInput), Body: body, Public: public}
	// pin has no editor UI yet — round-trip it from whatever was on disk
	// before this save, untouched.
	if oldContent, _, err := app.Store.Read(oldFile); err == nil {
		old := ParsePage(slug, oldContent)
		page.Pin = old.Pin
	}

	message := "Update " + title
	if basehash == "" {
		message = "Create " + title
	}
	if oldFile != newFile {
		message = "Move " + title
	}

	// SaveChecked verifies basehash against oldFile's current hash and
	// performs the write atomically under the store lock, so two concurrent
	// saves against the same basehash can't both succeed.
	authorName, authorEmail := app.gitAuthor(username)
	_, err := app.Store.SaveChecked(oldFile, newFile, basehash, page.Encode(), message, authorName, authorEmail)
	if errors.Is(err, ErrConflict) {
		_, currentHash, readErr := app.Store.Read(oldFile)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		oldPrefix := ""
		if oldFile == hiddenFile(slug) {
			oldPrefix = "/_/hidden"
		}
		headAuthor, headWhen, headShortHash := "", "", ""
		if history, herr := app.Store.History(oldFile); herr == nil && len(history) > 0 {
			headAuthor = history[0].Author
			headWhen = relativeTime(history[0].When)
			headShortHash = history[0].Hash[:8]
		}
		app.render(w, r, http.StatusConflict, "conflict", TemplateData{
			Authed:        true,
			Title:         title,
			Slug:          slug,
			Body:          body,
			BaseHash:      currentHash,
			TagsInput:     tagsInput,
			HeadShortHash: headShortHash,
			HeadAuthor:    headAuthor,
			HeadWhen:      headWhen,
			StatusMode:    "conflict",
			RoutePrefix:   oldPrefix,
			IsHidden:      hidden,
			IsPublic:      public,
		})
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	slog.Info("saved", "slug", slug, "file", newFile, "author", authorName, "message", message)

	if hidden {
		app.Index.Remove(slug)
	} else {
		if err := app.Index.Update(page); err != nil {
			slog.Error("updating search index", "slug", page.Slug, "err", err)
		}
	}

	http.Redirect(w, r, newPrefix+"/"+slug, http.StatusSeeOther)
}

func (app *App) handlePreview(w http.ResponseWriter, r *http.Request) {
	// The editor posts the raw markdown as the request body; the tests
	// post it form-encoded. Support both.
	var body string
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		body = r.FormValue("body")
	} else {
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		body = string(b)
	}

	html, err := app.Render.Render(body)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := w.Write([]byte(html)); err != nil {
		slog.Error("writing preview response", "err", err)
	}
}

func (app *App) handleUploadAttachment(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	username := app.currentUser(r)

	// Limit request body to the configured maximum
	maxBytes := app.config().MaxUploadBytes
	if maxBytes <= 0 {
		maxBytes = 10 * 1024 * 1024
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "no file uploaded", http.StatusBadRequest)
		return
	}
	defer func() {
		if err := file.Close(); err != nil {
			slog.Warn("closing uploaded file", "err", err)
		}
	}()

	// Sanitise filename: base name only, slugify name part, keep extension
	filename := filepath.Base(header.Filename)
	ext := filepath.Ext(filename)
	name := filename[:len(filename)-len(ext)]
	name = Slugify(name)

	if name == "" {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}

	// Check extension whitelist (SVG excluded — can carry scripts that execute
	// when served as image/svg+xml)
	allowedExts := map[string]bool{
		".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
		".webp": true, ".pdf": true,
	}
	if !allowedExts[strings.ToLower(ext)] {
		http.Error(w, "file type not allowed", http.StatusBadRequest)
		return
	}

	filename = name + ext
	path := "attachments/" + slug + "/" + filename

	// Verify the cleaned path stays under attachments/ (same check as
	// handleServeAttachment) — slug is a raw URL path segment and could be "..".
	repoPath := filepath.Join(app.config().RepoDir, path)
	absRepo := filepath.Join(app.config().RepoDir, "attachments")
	absPath, _ := filepath.Abs(repoPath)
	absRepoAbs, _ := filepath.Abs(absRepo)
	if !strings.HasPrefix(absPath, absRepoAbs+string(filepath.Separator)) {
		http.Error(w, "invalid slug", http.StatusBadRequest)
		return
	}

	// Read file content
	content, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "error reading file", http.StatusInternalServerError)
		return
	}

	// Save via store
	authorName, authorEmail := app.gitAuthor(username)
	_, err = app.Store.Save(path, content, "Add attachment "+filename, authorName, authorEmail)
	if err != nil {
		http.Error(w, "error saving file", http.StatusInternalServerError)
		return
	}

	// Return JSON response
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]string{"url": fmt.Sprintf("/_/attachments/%s/%s", slug, filename)}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Error("encoding attachment response", "err", err)
	}
}

func (app *App) handleServeAttachment(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	file := r.PathValue("file")

	// Sanitise path - prevent directory traversal
	cleanPath := filepath.Join("attachments", slug, file)
	repoPath := filepath.Join(app.config().RepoDir, cleanPath)

	// Verify the cleaned path is still under attachments/
	absRepo := filepath.Join(app.config().RepoDir, "attachments")
	absPath, _ := filepath.Abs(repoPath)
	absRepoAbs, _ := filepath.Abs(absRepo)

	if !strings.HasPrefix(absPath, absRepoAbs+string(filepath.Separator)) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, repoPath)
}

func (app *App) handleTagsIndex(w http.ResponseWriter, r *http.Request) {
	app.render(w, r, http.StatusOK, "tags", TemplateData{
		Authed:  true,
		Title:   "Tags",
		AllTags: app.Index.Tags(),
	})
}

func (app *App) handleTagPages(w http.ResponseWriter, r *http.Request) {
	tagSlug := r.PathValue("tag")
	name := app.Index.TagName(tagSlug)
	if name == "" {
		name = tagSlug
	}

	titles := app.Index.Titles()
	var pages []BacklinkEntry
	for _, slug := range app.Index.PagesForTag(tagSlug) {
		pages = append(pages, BacklinkEntry{Slug: slug, Title: titles[slug]})
	}

	app.render(w, r, http.StatusOK, "tags", TemplateData{
		Authed:   true,
		Title:    "Tag: " + name,
		TagName:  name,
		TagPages: pages,
	})
}

func (app *App) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.FormValue("q")
	start := time.Now()
	hits, err := app.Index.Search(q)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	elapsed := time.Since(start)

	results := make([]SearchResult, 0, len(hits))
	for _, hit := range hits {
		results = append(results, SearchResult{
			Slug:    hit.Slug,
			Title:   hit.Title,
			Snippet: template.HTML(hit.Snippet),
			Tags:    hit.Tags,
		})
	}

	pageCount := len(app.Index.Titles())
	title := "Search"
	if q != "" {
		title = q
	}
	app.render(w, r, http.StatusOK, "search", TemplateData{
		Authed:        true,
		Title:         title,
		Query:         q,
		SearchResults: results,
		SearchHits:    len(results),
		SearchPages:   pageCount,
		SearchElapsed: elapsed.String(),
		StatusMode:    "search",
	})
}

type AutocompleteResult struct {
	Slug    string   `json:"slug"`
	Title   string   `json:"title"`
	Snippet string   `json:"snippet"`
	Tags    []string `json:"tags"`
}

func (app *App) handleSearchAPI(w http.ResponseWriter, r *http.Request) {
	q := r.FormValue("q")
	hits, err := app.Index.Search(q)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	results := make([]AutocompleteResult, 0, len(hits))
	for _, hit := range hits {
		results = append(results, AutocompleteResult(hit))
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(results); err != nil {
		slog.Error("encoding search response", "err", err)
	}
}

type SyncStatus struct {
	State           string       `json:"state"`
	Detail          string       `json:"detail"`
	At              string       `json:"at"`
	PagesChanged    []string     `json:"pagesChanged,omitempty"`
	Commits         []SyncCommit `json:"commits,omitempty"`
	LastSuccessUnix int64        `json:"last_success_unix"`
}

type SyncCommit struct {
	Hash      string `json:"hash"`
	ShortHash string `json:"shortHash"`
	Message   string `json:"message"`
	Author    string `json:"author"`
	When      string `json:"when"`
}

func htmlEscape(s string) string {
	return html.EscapeString(s)
}

func extractSnippet(body string, wordCount int) string {
	words := strings.Fields(body)
	if len(words) > wordCount {
		words = words[:wordCount]
	}
	return strings.Join(words, " ")
}

func (app *App) handleAPIPreview(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")

	content, _, err := app.Store.Read(pageFile(slug))
	if err != nil || content == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	page := ParsePage(slug, content)
	snippet := extractSnippet(page.Body, 40)

	resp := map[string]interface{}{
		"title":   page.Title,
		"snippet": snippet,
		"tags":    page.Tags,
		"age":     "just now",
	}

	// Try to get the age from history
	if history, err := app.Store.History(pageFile(slug)); err == nil && len(history) > 0 {
		resp["age"] = relativeTime(history[0].When)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Error("encoding preview response", "err", err)
	}
}

func (app *App) handleSyncAPI(w http.ResponseWriter, r *http.Request) {
	state, detail := app.Store.SyncState()
	resp := SyncStatus{
		State:           state,
		Detail:          detail,
		At:              time.Now().Format("15:04"),
		LastSuccessUnix: app.Store.LastSyncUnix(),
	}

	cfg := app.config()
	if cfg.SyncMode == "bidirectional" && cfg.Git.RemoteURL != "" {
		result, err := app.Store.FetchAndFF()
		if err != nil {
			state, detail = app.Store.SyncState()
			resp.State = state
			resp.Detail = detail
		} else {
			resp.PagesChanged = result.ChangedPaths
			for _, c := range result.Commits {
				shortHash := c.Hash
				if len(shortHash) > 8 {
					shortHash = shortHash[:8]
				}
				resp.Commits = append(resp.Commits, SyncCommit{
					Hash:      c.Hash,
					ShortHash: shortHash,
					Message:   c.Message,
					Author:    c.Author,
					When:      c.When.Format("2006-01-02 15:04"),
				})
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Error("encoding sync status response", "err", err)
	}
}

func (app *App) handleSyncPushNow(w http.ResponseWriter, r *http.Request) {
	state, detail := app.Store.PushNow()

	resp := map[string]interface{}{
		"ok":                state == "ok",
		"state":             state,
		"detail":            detail,
		"last_success_unix": app.Store.LastSyncUnix(),
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Error("encoding sync push response", "err", err)
	}
}

var wikiLinkRe = regexp.MustCompile(`\[\[([^\[\]]+)\]\]`)

// handleRenamePage changes a page's title and slug (the ">rename" palette
// verb), moving the file and rewriting wiki-links in every referencing page.
func (app *App) handleRenamePage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	newTitle := strings.TrimSpace(r.FormValue("title"))
	if newTitle == "" {
		http.Error(w, "missing title", http.StatusBadRequest)
		return
	}
	newSlug := Slugify(newTitle)
	if newSlug == "" {
		http.Error(w, "invalid title", http.StatusBadRequest)
		return
	}

	content, hash, err := app.Store.Read(pageFile(slug))
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if newSlug != slug && app.Index.Exists(newSlug) {
		http.Error(w, "a page with that title already exists", http.StatusConflict)
		return
	}

	// Capture backlinks before touching the index — Index.Remove drops them.
	sources := app.Index.Backlinks(slug)

	page := ParsePage(slug, content)
	oldTitle := page.Title
	page.Title = newTitle
	page.Slug = newSlug
	authorName, authorEmail := app.gitAuthor(app.currentUser(r))
	message := fmt.Sprintf("Rename %s to %s", oldTitle, newTitle)
	if _, err := app.Store.SaveChecked(pageFile(slug), pageFile(newSlug), hash, page.Encode(), message, authorName, authorEmail); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if newSlug != slug {
		app.Index.Remove(slug)
	}
	if err := app.Index.Update(page); err != nil {
		slog.Error("updating search index", "slug", page.Slug, "err", err)
	}

	// Rewrite [[wiki-links]] that resolved to the old slug.
	for _, src := range sources {
		srcContent, srcHash, err := app.Store.Read(pageFile(src))
		if err != nil {
			continue
		}
		srcPage := ParsePage(src, srcContent)
		updated := wikiLinkRe.ReplaceAllStringFunc(srcPage.Body, func(m string) string {
			if Slugify(m[2:len(m)-2]) == slug {
				return "[[" + newTitle + "]]"
			}
			return m
		})
		if updated == srcPage.Body {
			continue
		}
		srcPage.Body = updated
		if _, err := app.Store.SaveChecked(pageFile(src), pageFile(src), srcHash, srcPage.Encode(), "Update links after rename of "+oldTitle, authorName, authorEmail); err == nil {
			if err := app.Index.Update(srcPage); err != nil {
				slog.Error("updating search index", "slug", srcPage.Slug, "err", err)
			}
		}
	}

	slog.Info("renamed", "from", slug, "to", newSlug, "links", len(sources))
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"ok": true, "slug": newSlug}); err != nil {
		slog.Error("encoding rename response", "err", err)
	}
}

// handleSetTags replaces a page's tags (the ">tag" palette verb).
func (app *App) handleSetTags(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	content, hash, err := app.Store.Read(pageFile(slug))
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	page := ParsePage(slug, content)
	page.Tags = ParseTags(r.FormValue("tags"))
	authorName, authorEmail := app.gitAuthor(app.currentUser(r))
	if _, err := app.Store.SaveChecked(pageFile(slug), pageFile(slug), hash, page.Encode(), "Update tags for "+page.Title, authorName, authorEmail); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := app.Index.Update(page); err != nil {
		slog.Error("updating search index", "slug", page.Slug, "err", err)
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"ok": true, "slug": slug}); err != nil {
		slog.Error("encoding tags response", "err", err)
	}
}

func (app *App) handleHealthReport(w http.ResponseWriter, r *http.Request) {
	titles := app.Index.Titles()
	missing, orphans := app.Index.Health(app.config().HomeSlug())

	missingSlugs := make([]string, 0, len(missing))
	for slug := range missing {
		missingSlugs = append(missingSlugs, slug)
	}
	sort.Strings(missingSlugs)

	var b strings.Builder
	if len(missingSlugs) > 0 {
		fmt.Fprintf(&b, `<section><h2>Missing pages (%d)</h2><p>Wiki-linked but not yet created:</p><ul>`, len(missingSlugs))
		for _, m := range missingSlugs {
			fmt.Fprintf(&b, `<li><a href="/%s?do=edit" class="missing">%s</a> — linked from `, m, htmlEscape(m))
			for i, src := range missing[m] {
				if i > 0 {
					b.WriteString(", ")
				}
				title := titles[src]
				if title == "" {
					title = src
				}
				fmt.Fprintf(&b, `<a href="/%s">%s</a>`, src, htmlEscape(title))
			}
			b.WriteString(`</li>`)
		}
		b.WriteString(`</ul></section>`)
	}

	if len(orphans) > 0 {
		fmt.Fprintf(&b, `<section><h2>Orphaned pages (%d)</h2><p>Pages with no incoming links:</p><ul>`, len(orphans))
		for _, o := range orphans {
			title := titles[o]
			if title == "" {
				title = o
			}
			fmt.Fprintf(&b, `<li><a href="/%s">%s</a></li>`, o, htmlEscape(title))
		}
		b.WriteString(`</ul></section>`)
	}

	if len(missingSlugs) == 0 && len(orphans) == 0 {
		b.WriteString(`<p>✓ Your wiki is healthy!</p>`)
	}

	app.render(w, r, http.StatusOK, "page", TemplateData{
		Authed:        true,
		Title:         "wiki health",
		Slug:          "health-report",
		Content:       template.HTML(b.String()),
		StatusContext: fmt.Sprintf("%d missing · %d orphan%s", len(missingSlugs), len(orphans), plural(len(orphans))),
	})
}

func (app *App) handlePageDiff(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	hashA := r.URL.Query().Get("a")
	hashB := r.URL.Query().Get("b")

	if hashA == "" || hashB == "" {
		http.Error(w, "missing hashes", http.StatusBadRequest)
		return
	}

	diff, err := app.Store.Diff(pageFile(slug), hashA, hashB)
	if err != nil {
		http.Error(w, "diff failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if _, err := w.Write([]byte(diff)); err != nil {
		slog.Error("writing diff response", "err", err)
	}
}

func (app *App) handleHistory(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")

	// Get page to get title
	content, _, err := app.Store.Read(pageFile(slug))
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	page := ParsePage(slug, content)

	history, err := app.Store.History(pageFile(slug))
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	entries := make([]HistoryEntry, 0, len(history))
	maxEntries := 50
	for i, commit := range history {
		if i >= maxEntries {
			break
		}
		entries = append(entries, HistoryEntry{
			Hash:      commit.Hash,
			ShortHash: commit.Hash[:8],
			Message:   commit.Message,
			Author:    commit.Author,
			When:      commit.When.Format("2006-01-02 15:04"),
			Current:   i == 0,
		})
	}

	totalHistory := len(history)

	app.render(w, r, http.StatusOK, "history", TemplateData{
		Authed:         true,
		Title:          page.Title,
		Slug:           slug,
		HistoryEntries: entries,
		TotalHistory:   totalHistory,
		StatusMode:     "log",
	})
}

func (app *App) handleViewRev(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	hash := r.URL.Query().Get("hash")

	// Get old version
	content, err := app.Store.FileAt(pageFile(slug), hash)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	page := ParsePage(slug, content)
	page.Body = injectTOC(page.Body, app.Index, app.config().HomeSlug())
	renderedBody, err := app.Render.Render(page.Body)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Get commit info for the date
	history, _ := app.Store.History(pageFile(slug))
	commitTime := ""
	for _, commit := range history {
		if commit.Hash == hash {
			commitTime = commit.When.Format("2006-01-02 15:04")
			break
		}
	}

	app.render(w, r, http.StatusOK, "page", TemplateData{
		Authed:         true,
		Title:          page.Title,
		Slug:           slug,
		Content:        renderedBody,
		RevHash:        hash,
		OldVersionDate: commitTime,
	})
}

func (app *App) handleRevert(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	hash := r.FormValue("hash")
	username := app.currentUser(r)

	// Get old version
	content, err := app.Store.FileAt(pageFile(slug), hash)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// Save as new commit
	authorName, authorEmail := app.gitAuthor(username)
	_, err = app.Store.Save(pageFile(slug), content, "Revert "+slug+" to "+hash[:8], authorName, authorEmail)
	if err != nil {
		http.Error(w, "error reverting", http.StatusInternalServerError)
		return
	}
	slog.Info("reverted", "slug", slug, "to", hash[:8], "author", authorName)

	// Update index
	page := ParsePage(slug, content)
	if err := app.Index.Update(page); err != nil {
		slog.Error("updating search index", "slug", page.Slug, "err", err)
	}

	// Redirect to page
	http.Redirect(w, r, "/"+slug, http.StatusSeeOther)
}

func (app *App) handleHiddenIndex(w http.ResponseWriter, r *http.Request) {
	paths, err := app.Store.ListHidden()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var pages []BacklinkEntry
	for _, p := range paths {
		slug := strings.TrimPrefix(p, ".")
		slug = slug[:len(slug)-3] // strip .md
		content, _, err := app.Store.Read(p)
		if err != nil {
			continue
		}
		page := ParsePage(slug, content)
		pages = append(pages, BacklinkEntry{Slug: slug, Title: page.Title})
	}
	app.render(w, r, http.StatusOK, "hidden", TemplateData{
		Authed:      true,
		Title:       "Hidden",
		StatusMode:  "view",
		RoutePrefix: "/_/hidden",
		TagPages:    pages,
	})
}

func (app *App) handleViewHidden(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	file := hiddenFile(slug)

	content, _, err := app.Store.Read(file)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			app.render(w, r, http.StatusNotFound, "create", TemplateData{
				Authed:      true,
				Title:       "Page not found",
				Slug:        slug,
				RoutePrefix: "/_/hidden",
			})
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	page := ParsePage(slug, content)
	renderedBody, err := app.Render.Render(page.Body)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	app.render(w, r, http.StatusOK, "page", TemplateData{
		Authed:      true,
		Title:       page.Title,
		Slug:        slug,
		Content:     renderedBody,
		RoutePrefix: "/_/hidden",
	})
}

func (app *App) handleEditHidden(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	file := hiddenFile(slug)

	page := Page{Slug: slug, Title: slug}
	baseHash := ""

	content, hash, _ := app.Store.Read(file)
	if content != nil {
		page = ParsePage(slug, content)
		baseHash = hash
	}

	app.render(w, r, http.StatusOK, "edit", TemplateData{
		Authed:      true,
		Title:       page.Title,
		Slug:        slug,
		Body:        page.Body,
		BaseHash:    baseHash,
		TagsInput:   strings.Join(page.Tags, ", "),
		StatusMode:  "edit",
		RoutePrefix: "/_/hidden",
		IsHidden:    true,
		IsPublic:    page.Public,
	})
}

func (app *App) handleSaveHidden(w http.ResponseWriter, r *http.Request) {
	app.handleSave(w, r, hiddenFile(r.PathValue("slug")))
}

// settingsData assembles the SettingsData for the current user, shared by
// the GET handler and the token handlers (which re-render rather than
// redirect, so a freshly minted token can be shown exactly once).
func (app *App) settingsData(r *http.Request) SettingsData {
	cfg := app.config()
	user := app.currentUser(r)

	sd := buildSettingsData(cfg, app.Auth.prefs(user), app.Namespaces())
	sd.HelpDrifted = HelpDrifted(app.Store)
	sd.UserGitAuthor = app.Auth.AuthorFor(user)
	sd.Users = app.Auth.Users()
	sd.AllScopes = []string{string(scopeRead), string(scopeWrite), string(scopeSettings)}
	sd.CurrentUser = user
	for _, t := range app.Auth.TokensFor(user) {
		expires := "never"
		switch {
		case t.expired():
			expires = "expired"
		case !t.Expires.IsZero():
			expires = t.Expires.Format("2006-01-02")
		}
		sd.Tokens = append(sd.Tokens, TokenView{Name: t.Name, Created: relativeTime(t.Created), Expires: expires})
	}
	return sd
}

// tokenTTLs maps the expiry select options to durations; zero means never.
// 30 days is the form's default.
var tokenTTLs = map[string]time.Duration{
	"1d":    24 * time.Hour,
	"7d":    7 * 24 * time.Hour,
	"30d":   30 * 24 * time.Hour,
	"1y":    365 * 24 * time.Hour,
	"never": 0,
}

func (app *App) renderSettings(w http.ResponseWriter, r *http.Request, sd SettingsData, tmpl string) {
	data := TemplateData{
		Authed:        true,
		Title:         "Settings",
		StatusMode:    "settings",
		StatusContext: "config",
		Settings:      &sd,
	}
	app.render(w, r, http.StatusOK, tmpl, data)
}

func (app *App) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	sd := app.settingsData(r)
	if q := r.URL.Query().Get("saved"); q == "1" {
		sd.Flash = "Settings saved"
	}
	app.renderSettings(w, r, sd, "settings")
}

// handleAdminGet renders the system-configuration page (server, git
// remote, users, advanced overrides, wiki setup) — split out from the
// personal /settings page since only accounts with the "settings" scope ever
// reach either one, but the two cover very different ground.
func (app *App) handleAdminGet(w http.ResponseWriter, r *http.Request) {
	sd := app.settingsData(r)
	if q := r.URL.Query().Get("saved"); q == "1" {
		sd.Flash = "Settings saved"
	}
	if q := r.URL.Query().Get("exported"); q == "1" {
		sd.Flash = "Config exported to " + sd.ConfigPath
	}
	app.renderSettings(w, r, sd, "admin")
}

// handleCreateToken mints a PAT for the current user and re-renders the
// settings page with the value — the only time it is ever displayed.
func (app *App) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	label := strings.TrimSpace(r.FormValue("label"))
	if label == "" {
		sd := app.settingsData(r)
		sd.TokenError = "Token needs a name"
		app.renderSettings(w, r, sd, "settings")
		return
	}
	ttl, ok := tokenTTLs[r.FormValue("expiry")]
	if !ok {
		ttl = tokenTTLs["30d"]
	}
	var expires time.Time
	if ttl > 0 {
		expires = time.Now().Add(ttl)
	}
	user := app.currentUser(r)
	token, err := app.Auth.AddToken(user, label, expires)
	if err != nil {
		sd := app.settingsData(r)
		sd.TokenError = err.Error()
		app.renderSettings(w, r, sd, "settings")
		return
	}
	slog.Info("token created", "user", user, "label", label)
	sd := app.settingsData(r)
	sd.NewToken = token
	app.renderSettings(w, r, sd, "settings")
}

// handleRevokeToken revokes the current user's token named by the form.
func (app *App) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	user := app.currentUser(r)
	label := r.FormValue("label")
	if err := app.Auth.RemoveToken(user, label); err != nil {
		sd := app.settingsData(r)
		sd.TokenError = err.Error()
		app.renderSettings(w, r, sd, "settings")
		return
	}
	slog.Info("token revoked", "user", user, "label", label)
	http.Redirect(w, r, "/_/settings", http.StatusSeeOther)
}

// handleCreateUser adds a new user from the settings page, with the scopes
// selected in the form (none checked = full access, matching AddUser's
// existing default for CLI-added users).
func (app *App) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	password := r.FormValue("password")
	scopes := r.Form["scopes"]

	fail := func(msg string) {
		sd := app.settingsData(r)
		sd.UserError = msg
		app.renderSettings(w, r, sd, "admin")
	}

	if name == "" || password == "" {
		fail("Name and password are required")
		return
	}
	if app.Auth.UserExists(name) {
		fail(fmt.Sprintf("User %q already exists", name))
		return
	}
	if err := app.Auth.AddUser(name, password); err != nil {
		fail(err.Error())
		return
	}
	if err := app.Auth.SetScopes(name, scopes); err != nil {
		fail(err.Error())
		return
	}
	slog.Info("user created", "user", name, "by", app.currentUser(r))
	http.Redirect(w, r, "/_/admin?saved=1", http.StatusSeeOther)
}

// handleSetUserScopes updates an existing user's scopes from the settings
// page. The bootstrap admin (HMD_ADMIN_USER) always keeps full access —
// it's not listed with editable checkboxes, but this also rejects a
// hand-crafted request against it, since it's the one account that can't be
// recreated from the UI if it were ever locked out. Beyond that, a user may
// not strip their own settings scope — with hmd scopes gone, that would lock
// them out of /settings with no way back short of hand-editing users.json.
func (app *App) handleSetUserScopes(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	scopes := r.Form["scopes"]

	if name == app.config().AdminUser {
		sd := app.settingsData(r)
		sd.UserError = "the bootstrap admin user always has full access"
		app.renderSettings(w, r, sd, "admin")
		return
	}

	if name == app.currentUser(r) {
		hasSettings := len(scopes) == 0
		for _, s := range scopes {
			if s == string(scopeSettings) {
				hasSettings = true
			}
		}
		if !hasSettings {
			sd := app.settingsData(r)
			sd.UserError = "cannot remove your own settings access"
			app.renderSettings(w, r, sd, "admin")
			return
		}
	}

	if err := app.Auth.SetScopes(name, scopes); err != nil {
		sd := app.settingsData(r)
		sd.UserError = err.Error()
		app.renderSettings(w, r, sd, "admin")
		return
	}
	slog.Info("user scopes updated", "user", name, "by", app.currentUser(r))
	http.Redirect(w, r, "/_/admin?saved=1", http.StatusSeeOther)
}

func (app *App) handleSettingsPost(w http.ResponseWriter, r *http.Request) {
	configPath := app.config().ConfigFile

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return
	}

	bind := r.FormValue("bind")
	repoDir := r.FormValue("repo_dir")
	remoteURL := r.FormValue("remote_url")
	gitUser := r.FormValue("git_user")
	gitAuthor := r.FormValue("git_author")
	gitToken := r.FormValue("git_token")
	siteName := r.FormValue("site_name")
	maxUploadStr := r.FormValue("max_upload_bytes")
	syncPollStr := r.FormValue("sync_poll_ms")
	syncMode := r.FormValue("sync_mode")

	if bind == "" {
		http.Error(w, "bind cannot be empty", http.StatusBadRequest)
		return
	}
	if repoDir == "" {
		http.Error(w, "repo directory cannot be empty", http.StatusBadRequest)
		return
	}
	if gitUser == "" {
		http.Error(w, "git user cannot be empty", http.StatusBadRequest)
		return
	}
	if siteName == "" {
		http.Error(w, "site name cannot be empty", http.StatusBadRequest)
		return
	}
	if remoteURL != "" && !strings.HasPrefix(remoteURL, "https://") && !strings.HasPrefix(remoteURL, "git@") {
		http.Error(w, "remote URL must be HTTPS or git@ SSH format", http.StatusBadRequest)
		return
	}
	if syncMode != "push" && syncMode != "bidirectional" {
		http.Error(w, "sync mode must be push or bidirectional", http.StatusBadRequest)
		return
	}

	maxUploadBytes, err := strconv.ParseInt(maxUploadStr, 10, 64)
	if err != nil || maxUploadBytes < 1 {
		http.Error(w, "invalid upload size", http.StatusBadRequest)
		return
	}
	syncPollMs, err := strconv.Atoi(syncPollStr)
	if err != nil || syncPollMs < 100 {
		http.Error(w, "sync poll must be at least 100ms", http.StatusBadRequest)
		return
	}

	fc, err := LoadFileConfig(configPath)
	if err != nil && !os.IsNotExist(err) {
		http.Error(w, "Failed to read config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	fc.Bind = bind
	fc.RepoDir = repoDir
	fc.Git.RemoteURL = remoteURL
	fc.Git.User = gitUser
	fc.Git.Author = gitAuthor
	fc.SiteName = siteName
	fc.MaxUploadBytes = int64Ptr(maxUploadBytes)
	fc.SyncPollMs = intPtr(syncPollMs)
	fc.SyncMode = syncMode

	if gitToken != "" {
		fc.Git.Token = gitToken
	}

	if err := SaveFileConfig(configPath, fc); err != nil {
		slog.Error("saving config", "err", err)
		http.Error(w, "failed to save config", http.StatusInternalServerError)
		return
	}

	newCfg, err := LoadConfig()
	if err != nil {
		slog.Error("reloading config after save", "err", err)
		http.Error(w, "config saved but reload failed", http.StatusInternalServerError)
		return
	}
	app.SetConfig(newCfg)

	if err := app.Store.UpdateRemote(newCfg); err != nil {
		slog.Warn("updating store remote", "err", err)
	}

	slog.Info("settings updated", "by", app.currentUser(r))
	http.Redirect(w, r, "/_/admin?saved=1", http.StatusSeeOther)
}

// handleSettingsAppearance saves the current user's personal preferences —
// fonts, theme colours, sidebar tags — separately from the system-wide
// config handled by handleSettingsPost. These live per-user (app.Auth.prefs),
// never touch config.yaml, and take effect only for the saving user.
func (app *App) handleSettingsAppearance(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return
	}

	palette := r.FormValue("palette")
	if _, ok := themePresets[palette]; palette != "" && !ok {
		http.Error(w, "unknown palette", http.StatusBadRequest)
		return
	}

	fontUI := r.FormValue("font_ui")
	fontMono := r.FormValue("font_mono")
	if _, ok := fontStacks[fontUI]; fontUI != "" && !ok {
		http.Error(w, "unknown UI font", http.StatusBadRequest)
		return
	}
	if _, ok := fontStacks[fontMono]; fontMono != "" && !ok {
		http.Error(w, "unknown monospace font", http.StatusBadRequest)
		return
	}
	chosenSkin := r.FormValue("skin")
	if _, ok := skins[chosenSkin]; chosenSkin != "" && !ok {
		http.Error(w, "unknown skin", http.StatusBadRequest)
		return
	}

	user := app.currentUser(r)
	prevName, _ := effectiveSkin(app.config(), app.Auth.prefs(user))
	switchingSkin := skinName(chosenSkin) != prevName

	// Everything the settings form submits alongside the skin was rendered
	// against the skin the user was *on*. On a skin change the palette
	// describes the old look, so it's discarded in favour of the new skin's
	// own default unless the browser marks a later palette choice.
	if switchingSkin && r.FormValue("palette_explicit") != "1" {
		palette = resolveSkin(chosenSkin).Palette
	}

	if err := app.Auth.SetPrefs(user, palette, fontUI, fontMono, chosenSkin); err != nil {
		http.Error(w, "failed to save appearance", http.StatusInternalServerError)
		return
	}

	slog.Info("appearance updated", "by", user)
	http.Redirect(w, r, "/_/settings?saved=1", http.StatusSeeOther)
}

// handleSettingsExport snapshots the currently effective config (file
// values plus any env var overrides) into config.yaml. Lets someone who
// bootstrapped hmd via HMD_* env vars bake those values into the file in
// one action, instead of hand-copying each one. The settings page shows a
// confirmation dialog first — this bakes in the *current* values, which
// overwrites whatever is already in the file for those keys.
func (app *App) handleSettingsExport(w http.ResponseWriter, r *http.Request) {
	cfg := app.config()
	if err := SaveFileConfig(cfg.ConfigFile, cfg.toFileConfig()); err != nil {
		slog.Error("exporting config", "err", err)
		http.Error(w, "failed to export config", http.StatusInternalServerError)
		return
	}

	newCfg, err := LoadConfig()
	if err != nil {
		slog.Error("reloading config after export", "err", err)
		http.Error(w, "config exported but reload failed", http.StatusInternalServerError)
		return
	}
	app.SetConfig(newCfg)

	slog.Info("settings exported to config file", "by", app.currentUser(r))
	http.Redirect(w, r, "/_/admin?exported=1", http.StatusSeeOther)
}
