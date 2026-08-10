package main

import (
	"bytes"
	"context"
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
	"sync"
	"sync/atomic"
	texttemplate "text/template"
	"time"
)

// buildVersion is baked in at compile time via -ldflags "-X main.buildVersion=..."
// (see Dockerfile's VERSION build arg). HMD_VERSION overrides it at runtime
// if set. Shown on the login screen and sidebar footer.
var buildVersion = "dev"
var version = envOr("HMD_VERSION", buildVersion)

type App struct {
	cfg        atomic.Pointer[Config]
	wiki       atomic.Pointer[WikiConfig]
	namespaces atomic.Pointer[NamespaceRegistry]
	uploads    sync.Map
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

// wikiConfig returns the portable content settings. The fallback keeps small
// test apps and startup error paths usable before the first value is stored.
func (app *App) wikiConfig() WikiConfig {
	if cfg := app.wiki.Load(); cfg != nil {
		return *cfg
	}
	return defaultWikiConfig()
}

// SetWikiConfig stores new repository-level settings atomically.
func (app *App) SetWikiConfig(cfg WikiConfig) {
	cfg = cfg.normalised()
	app.wiki.Store(&cfg)
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

type AttachmentResult struct {
	OwnerSlug string        `json:"owner_slug"`
	Filename  string        `json:"filename"`
	URL       string        `json:"url"`
	Excerpt   template.HTML `json:"excerpt"`
	Score     float64       `json:"score"`
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
	SiteName                string
	AssetPath               string // static asset prefix; /_/static/ in the live app
	NamespaceHome           string // static namespace index link; empty uses the live route
	Static                  bool
	Authed                  bool
	Title                   string
	Slug                    string
	Content                 template.HTML
	Body                    string
	BaseHash                string
	Backlinks               []BacklinkEntry
	TagsInput               string
	PageTags                []TagChip
	AllTags                 []TagCount
	TagName                 string
	TagPages                []BacklinkEntry
	SyncState               string
	Error                   string
	Query                   string
	SearchResults           []SearchResult
	AttachmentResults       []AttachmentResult
	AttachmentSearchEnabled bool
	HistoryEntries          []HistoryEntry
	RevHash                 string
	OldVersionDate          string
	TotalHistory            int
	MermaidNeeded           bool
	RevisionCount           int
	HeadShortHash           string
	HeadAuthor              string
	HeadWhen                string // relative, e.g. "3 hours ago"
	Username                string
	StatusMode              string // view|edit|search|log|conflict, drives the statusline mode block
	StatusContext           string // right-aligned context: revision count / word counts
	Version                 string // shown on login intro
	RemoteHost              string // host of the git remote, for login intro (empty if none)
	OIDCEnabled             bool   // show the SSO button on the login page
	OIDCButtonText          string // SSO button label
	OIDCLocalLogin          bool   // show the password form alongside SSO
	OIDCIcon                bool   // show the icon (served at /auth/oidc/icon) on the SSO button
	SearchElapsed           string // search timing, e.g. "3ms"
	SearchHits              int    // match count, for search stats
	AttachmentHits          int    // attachment match count, for search stats
	SyncPollMs              int    // injected as a JS global for sync polling
	SyncMode                string
	BlobHash                string // current page blob hash, for client-side change detection
	ThemeStyle              template.CSS
	Skin                    string // structural skin name; empty = default, only ever a known skinNames entry
	Settings                *SettingsData
	SetupHomePreview        template.HTML
	SetupHelpPreview        template.HTML
	NeedsSetup              bool
	NeedsWikiSetup          bool
	NeedsNamespaceSetup     bool
	NeedsHelpSetup          bool
	HelpFileExists          bool
	SetupNamespace          string   // new-namespace name the setup form suggests
	SetupNamespaces         []string // existing namespaces offered as the landing choice
	SetupSiteName           string   // portable site-name default for first setup
	RoutePrefix             string
	IsHidden                bool
	IsNamespaceIndex        bool
	CanWrite                bool
	CanSettings             bool
	CanEdit                 bool
	NewPageBase             string
	NamespaceNames          []string // editor path field: namespaces a move may target
	NamespaceManagement     *NamespaceManagementData
	Namespace               string // namespace index page: the namespace being listed
	NamespacePublic         bool
	NamespaceTitle          string        // public top bar title; falls back to Namespace if empty
	PageTree                template.HTML // namespace index page: TagPages as a folder tree, see renderLiveTree
	RecentCommits           []LogEntry    // sidebar LOG section: last commits for the current page
	HealthMissing           int
	HealthOrphans           int
	SyncAge                 string // relative age of the last successful sync, e.g. "12 seconds ago"
	SyncLastUnix            int64  // raw timestamp for the client-side sync-age ticker
	SidebarWidgets          []*widget
	RailWidgets             []*widget
	PageHeadWidgets         []*widget
	PageFootWidgets         []*widget
	StatusVariant           string // skin.Status: full | write | quiet

	// Widget data — populated in app.render only when something on the page
	// actually reads it (see populateWidgetData).
	Calendar           CalendarMonth
	WritingStats       WritingStats
	PinnedPages        []BacklinkEntry
	PrevEntries        []PrevEntry
	NamespaceNav       []NamespaceNavEntry
	SidebarTreeNS      string          // namespace populateWidgetData staged SidebarTreeEntries for; "" means the tree widget has nothing to show
	SidebarTreeEntries []BacklinkEntry // unfiltered — render() applies filterBacklinkEntries before building SidebarTree, same as PinnedPages/NamespaceNav
	SidebarTree        template.HTML
	NewPageEnabled     bool   // a namespace with a `new:` template is in reach — gates the ctrl-j shortcut and >new verb client-side
	NewNamespace       string // which namespace ctrl-j targets
}

// NamespaceManagementData is deliberately smaller than SettingsData: the
// namespace directory and editor do not need the system configuration model.
type NamespaceManagementData struct {
	CanWrite     bool
	CanSettings  bool
	Rows         []NamespaceSummary
	Form         NamespaceListEntry
	WidgetGroups []widgetSlotGroup
	SlugPresets  []slugPresetView
	SkinNames    []string
	PaletteNames []string
	Palettes     map[string]themePreset // JSON-encoded for the published-view preview
	SkinPalettes map[string]string      // skin -> default palette for the preview
	Now          time.Time
	Error        string
	Flash        string
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
	HelpDrifted      bool
	UserGitAuthor    string        // current user's per-user git author override
	WikiConfigPath   string        // repository-relative .wiki.yaml path
	WikiLanding      string        // where "/" redirects to
	WikiSiteName     string        // title shared by every clone of the wiki
	Tokens           []TokenView   // current user's personal access tokens
	TokenNamespaces  []string      // selectable namespace names for new tokens
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
	Name           string
	Created        string
	Expires        string // "never", a date, or "expired"
	Scopes         []string
	Namespaces     []string
	ScopeLabel     string
	NamespaceLabel string
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
func buildSettingsData(cfg Config, prefs userRecord) SettingsData {
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
	tikaValue := "not set"
	if cfg.TikaURL != "" {
		tikaValue = "configured"
	}
	fields["TikaURL"] = FieldState{
		Value: tikaValue, Editable: false, RestartRequired: true,
		EnvVar: cfg.EnvOverrides["TikaURL"],
	}

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
	if data.SiteName == "" {
		data.SiteName = app.wikiConfig().SiteName
	}
	data.AssetPath = "/_/static/"
	data.Version = version
	data.RemoteHost = remoteHost(app.config().Git.RemoteURL)
	if data.SyncState == "" {
		data.SyncState, _ = app.Store.SyncState()
	}
	if data.Authed && data.Username == "" {
		data.Username = app.currentUser(r)
	}
	if data.Authed && data.AllTags == nil {
		ns, _ := namespaceFor(data.Slug)
		data.AllTags = app.Index.TagsInNamespace(ns)
	}
	// The namespace index sets this itself; everywhere else it's whichever
	// namespace the current page sits in, which is where the palette's
	// "create page" row files a new one.
	if data.Namespace == "" {
		data.Namespace, _ = namespaceFor(data.Slug)
	}
	if data.Authed {
		missing, orphans := app.Index.Health(app.Namespaces().IndexSlugs())
		missing, orphans = filterHealth(r.Context(), missing, orphans)
		data.HealthMissing = len(missing)
		data.HealthOrphans = len(orphans)
		data.AllTags = filterTagCounts(r.Context(), app.Index, data.AllTags)
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
		data.CanWrite = prefs.hasScope(scopeWrite)
		data.CanSettings = prefs.hasScope(scopeSettings)
		data.CanEdit = data.CanWrite && data.StatusMode == "view" && isPageSlug(data.Slug) && !data.IsNamespaceIndex
		if data.CanWrite {
			if data.IsNamespaceIndex {
				data.NewPageBase = data.Namespace
			} else if isPageSlug(data.Slug) {
				data.NewPageBase = data.Slug[:strings.LastIndex(data.Slug, "/")]
			} else if names := app.Namespaces().Names(); len(names) > 0 {
				data.NewPageBase = names[0]
			}
		}
		if data.StatusMode == "edit" {
			for _, name := range app.Namespaces().Names() {
				if tokenAllowsSlug(r.Context(), namespaceSlug(name, "")) {
					data.NamespaceNames = append(data.NamespaceNames, name)
				}
			}
		}
		data.SyncPollMs = cfg.SyncPollMs
		data.SyncMode = cfg.SyncMode
		activeName, activeSkin := effectiveSkin(cfg, prefs)
		themePrefs := prefs
		themePrefs.Palette = effectivePalette(prefs, activeSkin)
		data.ThemeStyle = buildThemeStyle(themePrefs)
		data.Skin = activeName
		data.StatusVariant = activeSkin.Status
		nsRegistry := app.Namespaces()
		target, _ := namespaceFor(data.Slug)
		if cfg, ok := nsRegistry[target]; ok && cfg.New != nil {
			data.NewPageEnabled = true
			data.NewNamespace = target
		}
		if app.Store.NeedsSetup.Load() || app.Store.ForceSetup.Load() {
			forced := app.Store.ForceSetup.Load()
			_, wikiExists, wikiErr := LoadWikiConfig(cfg.RepoDir)
			if wikiErr == nil && !wikiExists {
				data.NeedsWikiSetup = true
				data.SetupNamespace = defaultSetupNamespace
				data.SetupNamespaces = app.Namespaces().Names()
				data.SetupSiteName = app.wikiConfig().SiteName
			}

			if (!hasNamespace(cfg.RepoDir) || forced) && !data.NeedsWikiSetup {
				data.NeedsNamespaceSetup = true
				data.SetupNamespace = defaultSetupNamespace
				homePreview, _ := app.Render.Render(defaultHomeMD, defaultSetupNamespace)
				data.SetupHomePreview = template.HTML(homePreview)
			}

			_, helpErr := os.Stat(filepath.Join(cfg.RepoDir, ".help.md"))
			helpMissing := helpErr != nil
			if helpMissing || forced {
				data.NeedsHelpSetup = true
				data.HelpFileExists = !helpMissing
				helpPreview, _ := app.Render.Render(defaultHelpMD, "")
				data.SetupHelpPreview = template.HTML(helpPreview)
			}

			data.NeedsSetup = data.NeedsWikiSetup || data.NeedsNamespaceSetup || data.NeedsHelpSetup
		}

		nsCfg := app.Namespaces().Resolve(data.Slug)
		data.SidebarWidgets = widgetsForSlot(slotSidebar, nsCfg.Widgets)
		data.RailWidgets = widgetsForSlot(slotRail, nsCfg.Widgets)
		data.PageHeadWidgets = widgetsForSlot(slotPageHead, nsCfg.Widgets)
		data.PageFootWidgets = widgetsForSlot(slotPageFoot, nsCfg.Widgets)

		app.populateWidgetData(&data, activeSkin)
		for i := len(data.NamespaceNav) - 1; i >= 0; i-- {
			if !tokenAllowsNamespace(r.Context(), data.NamespaceNav[i].Name) {
				data.NamespaceNav = append(data.NamespaceNav[:i], data.NamespaceNav[i+1:]...)
			}
		}
		data.PinnedPages = filterBacklinkEntries(r.Context(), data.PinnedPages)
	}
	// Built for both authed and anonymous-public views: the tree is
	// server-rendered HTML with no auth-only API calls behind it, so a
	// public namespace can show it too (handlePublicPage/handleNamespaceIndex
	// stage SidebarTreeNS/SidebarTreeEntries directly since populateWidgetData
	// only runs when authed).
	if data.SidebarTreeNS != "" {
		entries := filterBacklinkEntries(r.Context(), data.SidebarTreeEntries)
		_, currentPath := namespaceFor(data.Slug)
		data.SidebarTree = renderLiveTree(buildPageTree(entries, data.SidebarTreeNS), data.SidebarTreeNS, currentPath)
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

// notFound renders a 404 inside the app chrome, so a bad URL leaves you with
// the sidebar and a way back instead of Go's bare text/plain line.
//
// The wording is deliberately identical for "no such page" and "you may not
// see this page": several call sites 404 precisely so a logged-out visitor
// can't tell a private page from a missing one, and a more helpful message
// would turn this into an existence oracle.
func (app *App) notFound(w http.ResponseWriter, r *http.Request) {
	app.errorPage(w, r, http.StatusNotFound, "Not found", "That page doesn't exist.")
}

// errorPage renders status inside the app chrome. app.render buffers and
// falls back to http.Error if the template itself fails, so a broken
// error.html can't loop.
func (app *App) errorPage(w http.ResponseWriter, r *http.Request, status int, title, detail string) {
	app.render(w, r, status, "error", TemplateData{
		Authed:        app.currentUser(r) != "",
		Title:         title,
		StatusContext: detail,
		StatusMode:    "view",
	})
}

func (app *App) tokenNamespaceDenied(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/_/api/") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		if _, err := w.Write([]byte(`{"error":"namespace access denied"}`)); err != nil {
			slog.Debug("writing namespace error response", "err", err)
		}
		return
	}
	// Only a navigation gets the rendered page; a denied write comes from
	// fetch, which reads the status and never the body.
	if r.Method == http.MethodGet {
		app.errorPage(w, r, http.StatusForbidden, "Forbidden", "You don't have access to that namespace.")
		return
	}
	http.Error(w, "403 Forbidden: namespace access denied", http.StatusForbidden)
}

func (app *App) requireTokenNamespace(w http.ResponseWriter, r *http.Request, namespace string) bool {
	if tokenAllowsNamespace(r.Context(), namespace) {
		return true
	}
	app.tokenNamespaceDenied(w, r)
	return false
}

func (app *App) requireTokenSlug(w http.ResponseWriter, r *http.Request, slug string) bool {
	if tokenAllowsSlug(r.Context(), slug) {
		return true
	}
	app.tokenNamespaceDenied(w, r)
	return false
}

func filterBacklinkEntries(ctx context.Context, entries []BacklinkEntry) []BacklinkEntry {
	filtered := make([]BacklinkEntry, 0, len(entries))
	for _, entry := range entries {
		if tokenAllowsSlug(ctx, entry.Slug) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func filterTagCounts(ctx context.Context, ix *Index, tags []TagCount) []TagCount {
	filtered := make([]TagCount, 0, len(tags))
	for _, tag := range tags {
		count := 0
		for _, slug := range ix.PagesForTag(tag.Slug) {
			if tokenAllowsSlug(ctx, slug) {
				count++
			}
		}
		if count > 0 {
			tag.Count = count
			filtered = append(filtered, tag)
		}
	}
	return filtered
}

func filterNamespaceSummaries(ctx context.Context, summaries []NamespaceSummary) []NamespaceSummary {
	filtered := make([]NamespaceSummary, 0, len(summaries))
	for _, summary := range summaries {
		if !tokenAllowsNamespace(ctx, summary.Name) {
			continue
		}
		summary.Pages = filterBacklinkEntries(ctx, summary.Pages)
		summary.Count = len(summary.Pages)
		filtered = append(filtered, summary)
	}
	return filtered
}

func filterHealth(ctx context.Context, missing map[string][]string, orphans []string) (map[string][]string, []string) {
	filteredMissing := make(map[string][]string, len(missing))
	for slug, sources := range missing {
		if !tokenAllowsSlug(ctx, slug) {
			continue
		}
		allowedSources := make([]string, 0, len(sources))
		for _, source := range sources {
			if tokenAllowsSlug(ctx, source) {
				allowedSources = append(allowedSources, source)
			}
		}
		if len(allowedSources) > 0 {
			filteredMissing[slug] = allowedSources
		}
	}
	filteredOrphans := make([]string, 0, len(orphans))
	for _, slug := range orphans {
		if tokenAllowsSlug(ctx, slug) {
			filteredOrphans = append(filteredOrphans, slug)
		}
	}
	return filteredMissing, filteredOrphans
}

// filterHealthNamespace scopes a health report to one namespace: a missing
// target or orphan belongs to the report if its own slug is in namespace.
// Callers only invoke this when a namespace filter was actually requested —
// namespaceFor("") == "" would otherwise make this a no-op filter to root
// pages rather than "show everything".
func filterHealthNamespace(missing map[string][]string, orphans []string, namespace string) (map[string][]string, []string) {
	filteredMissing := make(map[string][]string, len(missing))
	for slug, sources := range missing {
		if ns, _ := namespaceFor(slug); ns == namespace {
			filteredMissing[slug] = sources
		}
	}
	filteredOrphans := make([]string, 0, len(orphans))
	for _, slug := range orphans {
		if ns, _ := namespaceFor(slug); ns == namespace {
			filteredOrphans = append(filteredOrphans, slug)
		}
	}
	return filteredMissing, filteredOrphans
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
// pages in ns, the namespace of the page the token appears on — namespace
// content stays self-contained, so a token never reaches across into another
// namespace's pages. With no tag list, every page in ns is listed (excluding
// the home page). With a comma-separated tag list, only pages in ns matching
// ANY tag are included (OR). Results are sorted alphabetically by title;
// indexSlug (the namespace's own index page) is always excluded. The list is
// built as [[wiki-links]] so the existing wiki-link preprocessor renders the
// anchors.
func injectTOC(body string, ix *Index, indexSlug string, ns string) string {
	if !strings.Contains(body, "hmd:toc") {
		return body
	}
	titles := ix.Titles()
	inNS := func(slug string) bool {
		pageNS, _ := namespaceFor(slug)
		return pageNS == ns
	}
	return tocToken.ReplaceAllStringFunc(body, func(match string) string {
		tagList := ""
		if m := tocToken.FindStringSubmatch(match); m != nil {
			tagList = m[1]
		}
		var slugs []string
		if tagList == "" {
			for slug := range titles {
				if slug != indexSlug && inNS(slug) {
					slugs = append(slugs, slug)
				}
			}
		} else {
			tagSlugs := strings.Split(tagList, ",")
			for _, s := range ix.PagesForTags(tagSlugs) {
				if s != indexSlug && inNS(s) {
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

// landingPath is where "/" and a fresh login go: the portable landing
// slug, else the first namespace's index, else the namespace catalogue —
// which is the only useful destination on a wiki with nothing in it yet.
func (app *App) landingPath() string {
	if slug := app.wikiConfig().Landing; slug != "" {
		return "/" + slug
	}
	if names := app.Namespaces().Names(); len(names) > 0 {
		return "/" + names[0] + "/"
	}
	return "/_/namespaces"
}

// handleRoot redirects "/" to the landing path. An unauthenticated request
// never reaches here (Auth.Middleware always requires auth for "/").
func (app *App) handleRoot(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, app.landingPath(), http.StatusSeeOther)
}

func (app *App) Routes() http.Handler {
	mux := http.NewServeMux()

	// Root: redirect to the configured landing slug.
	mux.HandleFunc("GET /{$}", app.handleRoot)

	// Everything under /_/ is the app itself — the one reserved top-level
	// segment a namespace may never take. Content owns everything else.

	// Setup endpoint: seeds the first namespace + .help.md, clears the setup flag
	mux.HandleFunc("POST /_/setup", app.handleSetup)

	// New-page-from-template: ctrl-j and the palette's >new verb target a
	// namespace with a `new:` block.
	mux.HandleFunc("POST /_/new", app.handleNewPage)

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
	mux.HandleFunc("GET /_/namespaces", app.handleNamespacesGet)
	mux.HandleFunc("GET /_/namespaces/new", app.handleNamespaceNewGet)
	mux.HandleFunc("GET /_/namespaces/{name}/edit", app.handleNamespaceEditGet)
	mux.HandleFunc("GET /_/settings/namespaces/{name}/export", app.handleExportNamespace)
	mux.HandleFunc("POST /_/admin", app.handleSettingsPost)
	mux.HandleFunc("POST /_/settings/appearance", app.handleSettingsAppearance)
	mux.HandleFunc("POST /_/settings/export", app.handleSettingsExport)
	mux.HandleFunc("POST /_/settings/author", app.handleSetAuthor)
	mux.HandleFunc("POST /_/settings/tokens", app.handleCreateToken)
	mux.HandleFunc("POST /_/settings/tokens/revoke", app.handleRevokeToken)
	mux.HandleFunc("POST /_/settings/namespaces", app.handleSaveNamespace)
	mux.HandleFunc("POST /_/settings/namespaces/reset", app.handleResetNamespace)
	mux.HandleFunc("POST /_/settings/namespaces/delete", app.handleDeleteNamespace)
	mux.HandleFunc("POST /_/settings/namespaces/delete-all", app.handleDeleteNamespaceAll)
	mux.HandleFunc("POST /_/settings/users", app.handleCreateUser)
	mux.HandleFunc("POST /_/settings/users/scopes", app.handleSetUserScopes)
	mux.HandleFunc("POST /_/settings/setup", app.handleRerunSetup)
	mux.HandleFunc("POST /_/settings/wiki", app.handleSaveWikiConfig)
	mux.HandleFunc("POST /_/settings/help/reset", app.handleResetHelp)

	// Search
	mux.HandleFunc("GET /_/search", app.handleSearch)
	if app.Index.documents != nil {
		mux.HandleFunc("GET /_/search/attachments", app.handleAttachmentSearch)
	}
	mux.HandleFunc("GET /_/health-report", app.handleHealthReport)

	// Hidden page handlers (dot-prefixed files, an app-internal drafting
	// namespace — never addressable content, so it lives under /_/ too).
	// Actions are ?do= query params, same as ordinary pages.
	mux.HandleFunc("GET /_/hidden", app.handleHiddenIndex)
	mux.HandleFunc("GET /_/hidden/{path...}", app.handleHiddenGet)
	mux.HandleFunc("POST /_/hidden/{path...}", app.handleHiddenPost)

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
	if app.Index.documents != nil {
		mux.HandleFunc("GET /_/api/search/attachments", app.handleAttachmentSearchAPI)
	}
	mux.HandleFunc("GET /_/api/health", app.handleHealthAPI)
	mux.HandleFunc("GET /_/api/sync", app.handleSyncAPI)
	mux.HandleFunc("POST /_/api/sync/push-now", app.handleSyncPushNow)
	mux.HandleFunc("GET /_/api/preview/{slug...}", app.handleAPIPreview)
	mux.HandleFunc("POST /_/api/preview", app.handlePreview)
	mux.HandleFunc("POST /_/api/attachments/{slug...}", app.handleUploadAttachment)
	mux.HandleFunc("POST /_/api/attachment-uploads/{token}", app.handleCapabilityUpload)
	mux.HandleFunc("GET /_/attachments/{path...}", app.handleServeAttachment)

	// Content owns the root: one dispatcher for every page, GET and POST,
	// actions selected by ?do= rather than a path suffix. Go's ServeMux
	// prefers the more specific /_/... patterns above over this wildcard,
	// so /_/... never resolves here.
	mux.HandleFunc("GET /{path...}", app.handlePageGet)
	mux.HandleFunc("POST /{path...}", app.handlePagePost)

	return mux
}

// securityHeaders sets response headers that apply to every request
// regardless of route or auth state: clickjacking and MIME-sniffing
// protection always, HSTS whenever the request arrived over HTTPS (directly
// or via a terminating proxy).
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if isSecureRequest(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
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

	http.Redirect(w, r, app.landingPath(), http.StatusSeeOther)
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

// defaultSetupNamespace is the first-namespace name the setup form suggests.
// The field is editable — this is only the value someone who doesn't care
// gets by pressing the button.
const defaultSetupNamespace = "notes"

const newSetupNamespaceOption = "_new"

// handleSetup processes the setup form. Nothing is seeded without explicit
// consent: action=="add" writes the portable wiki config, any requested new
// namespace and .help.md; action=="skip" writes nothing. Either way
// NeedsSetup/ForceSetup are cleared.
func (app *App) handleSetup(w http.ResponseWriter, r *http.Request) {
	action := r.FormValue("action")
	cfg := app.config()
	authorName, authorEmail := app.gitAuthor(cfg.Git.User)

	if action == "add" {
		_, wikiExists, wikiErr := LoadWikiConfig(cfg.RepoDir)
		if wikiErr != nil {
			http.Error(w, "failed to read wiki config", http.StatusInternalServerError)
			return
		}
		if !wikiExists && r.FormValue("setup_wiki") != "on" {
			http.Error(w, "wiki setup is required", http.StatusBadRequest)
			return
		}
		if r.FormValue("setup_wiki") == "on" {
			selected := strings.TrimSpace(r.FormValue("default_namespace"))
			landing := ""
			if selected == newSetupNamespaceOption {
				name := strings.Trim(strings.TrimSpace(r.FormValue("new_namespace")), "/")
				if name == "" {
					name = defaultSetupNamespace
				}
				if !validNamespaceName(name) {
					http.Error(w, "invalid namespace name", http.StatusBadRequest)
					return
				}
				if _, exists := app.Namespaces()[name]; exists {
					http.Error(w, "namespace already exists; select it as the default instead", http.StatusBadRequest)
					return
				}
				if err := app.seedFirstNamespace(name, authorName, authorEmail); err != nil {
					slog.Error("seeding first namespace", "namespace", name, "err", err)
					http.Error(w, "failed to seed namespace", http.StatusInternalServerError)
					return
				}
				landing = name + "/"
			} else if _, ok := app.Namespaces()[selected]; ok {
				landing = selected + "/"
			} else {
				http.Error(w, "choose a detected namespace or create a new one", http.StatusBadRequest)
				return
			}
			wiki := WikiConfig{Landing: landing, SiteName: strings.TrimSpace(r.FormValue("site_name"))}
			if wiki.SiteName == "" {
				http.Error(w, "site name cannot be empty", http.StatusBadRequest)
				return
			}
			data, err := wiki.Encode()
			if err != nil {
				http.Error(w, "failed to encode wiki config", http.StatusInternalServerError)
				return
			}
			if _, err := app.Store.Save(wikiConfigFile, data, "Configure wiki settings", authorName, authorEmail); err != nil {
				http.Error(w, "failed to save wiki config", http.StatusInternalServerError)
				return
			}
			app.SetWikiConfig(wiki)
		}
		if r.FormValue("add_namespace") == "on" {
			name := strings.Trim(strings.TrimSpace(r.FormValue("namespace")), "/")
			if name == "" {
				name = defaultSetupNamespace
			}
			if !validNamespaceName(name) {
				http.Error(w, "invalid namespace name", http.StatusBadRequest)
				return
			}
			if err := app.seedFirstNamespace(name, authorName, authorEmail); err != nil {
				slog.Error("seeding first namespace", "namespace", name, "err", err)
				http.Error(w, "failed to seed namespace", http.StatusInternalServerError)
				return
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
	http.Redirect(w, r, refererPath(r, app.landingPath()), http.StatusSeeOther)
}

// seedFirstNamespace creates name with an index page and leaves a plain
// readme.md at the repo root for whoever browses the content repo on its git
// host. The caller decides the portable landing setting. Existing files are
// never overwritten: a repo that already has any of these keeps what it has.
func (app *App) seedFirstNamespace(name, authorName, authorEmail string) error {
	nsCfg := NamespaceConfig{Widgets: builtinWidgets, Index: defaultIndexPage}
	data, err := nsCfg.Encode()
	if err != nil {
		return fmt.Errorf("encoding namespace config: %w", err)
	}
	path := namespaceConfigPath(name)
	if _, err := app.Store.Save(path, data, "Configure namespace "+path, authorName, authorEmail); err != nil {
		return fmt.Errorf("saving namespace config: %w", err)
	}

	indexSlug := namespaceSlug(name, defaultIndexPage)
	if _, _, err := app.Store.Read(pageFile(indexSlug)); err != nil {
		content := Page{Slug: indexSlug, Title: name, Body: defaultHomeMD}.Encode()
		if _, err := app.Store.Save(pageFile(indexSlug), content, "Add "+indexSlug, authorName, authorEmail); err != nil {
			return fmt.Errorf("saving index page: %w", err)
		}
		if err := app.Index.Update(ParsePage(indexSlug, content)); err != nil {
			slog.Error("updating search index", "slug", indexSlug, "err", err)
		}
	}

	if _, _, err := app.Store.Read("readme.md"); err != nil {
		if _, err := app.Store.Save("readme.md", []byte(rootReadmeMD), "Add readme.md", authorName, authorEmail); err != nil {
			return fmt.Errorf("saving root readme: %w", err)
		}
	}

	app.refreshNamespaces()
	return nil
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

// newPageTemplateData is what a namespace's `new.slug`/entry-template
// text/template gets to work with: .Now as a plain time.Time (so
// `{{.Now.Format "2006-01-02"}}` works natively, no FuncMap needed), the
// acting user, and the target namespace name.
type newPageTemplateData struct {
	Now       time.Time
	User      string
	Namespace string
}

// renderNewPageText renders src (a slug pattern, or a template page's title
// or body) as text/template — not html/template: the output is markdown
// source, and page bodies are already trusted (the same WithUnsafe()
// discipline as everywhere else).
func renderNewPageText(src string, data newPageTemplateData) (string, error) {
	t, err := texttemplate.New("new").Parse(src)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// handleNewPage implements POST /_/new?ns=<namespace>: renders that
// namespace's `new.slug` template against today's date, creating the page
// from the namespace's `new.template` hidden page on first use and never
// overwriting an existing one. ctrl-j and the palette's >new verb use the
// current namespace when it declares a `new:` block.
func (app *App) handleNewPage(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("ns")
	if !app.requireTokenNamespace(w, r, ns) {
		return
	}
	nsCfg, ok := app.Namespaces()[ns]
	if !ok || nsCfg.New == nil {
		http.NotFound(w, r)
		return
	}

	username := app.currentUser(r)
	tmplData := newPageTemplateData{Now: time.Now(), User: username, Namespace: ns}

	slugRel, err := renderNewPageText(nsCfg.New.Slug, tmplData)
	if err != nil {
		http.Error(w, "invalid slug template", http.StatusInternalServerError)
		return
	}
	// The rendered slug is a trust boundary, like MCP input: validated
	// after rendering so a template can never write outside its own
	// namespace (no separators, no dot prefix, non-empty).
	if !validMCPPageSegment(slugRel) {
		http.Error(w, "invalid generated slug", http.StatusBadRequest)
		return
	}
	if !validMCPPageSegment(nsCfg.New.Template) {
		http.Error(w, "invalid template page", http.StatusBadRequest)
		return
	}
	slug := namespaceSlug(ns, slugRel)
	if !app.requireTokenSlug(w, r, slug) {
		return
	}

	if _, _, err := app.Store.Read(pageFile(slug)); err == nil {
		http.Redirect(w, r, "/"+slug+"?do=edit", http.StatusSeeOther)
		return
	}

	// A missing template page is not an error: the namespace's declared
	// template may have been deleted, or written into .namespace.yaml by
	// hand without creating it. Fall back to a bare page titled after the
	// slug rather than failing the one keystroke that creates pages here.
	templateSlug := namespaceSlug(ns, nsCfg.New.Template)
	tplPage := Page{Slug: templateSlug, Title: slugRel}
	if tplContent, _, err := app.Store.Read(hiddenFile(templateSlug)); err == nil {
		tplPage = ParsePage(templateSlug, tplContent)
	} else {
		slog.Warn("namespace template page missing, creating a bare page", "namespace", ns, "template", templateSlug)
	}

	title, err := renderNewPageText(tplPage.Title, tmplData)
	if err != nil {
		http.Error(w, "invalid title template", http.StatusInternalServerError)
		return
	}
	body, err := renderNewPageText(tplPage.Body, tmplData)
	if err != nil {
		http.Error(w, "invalid body template", http.StatusInternalServerError)
		return
	}
	// Tags go through the same substitution as the title and body: a template
	// where `{{.Now.Format "2006-01"}}` works in two of the three fields and
	// silently doesn't in the last one is just a trap.
	tags := make([]string, 0, len(tplPage.Tags))
	for _, tag := range tplPage.Tags {
		rendered, err := renderNewPageText(tag, tmplData)
		if err != nil {
			http.Error(w, "invalid tag template", http.StatusInternalServerError)
			return
		}
		tags = append(tags, rendered)
	}

	// Nothing is written to the store here: the rendered template is handed
	// straight to the edit template in this same response, same shape as
	// handleEditPage would build for a page that doesn't exist yet (like
	// following a missing wikilink). The page is only ever created by an
	// actual Save, so Cancel on this draft leaves no trace. The client
	// swaps this HTML in via history.pushState instead of navigating, so
	// there's no second request and nothing rides in the URL.
	app.render(w, r, http.StatusOK, "edit", TemplateData{
		Authed:     true,
		Title:      title,
		Slug:       slug,
		Body:       body,
		BaseHash:   "",
		TagsInput:  strings.Join(tags, ", "),
		StatusMode: "edit",
		IsHidden:   false,
	})
}

// handleViewPage serves a page's own URL with no ?do=. An unauthenticated
// request is only ever routed here for a page whose slug isn't under /_/
// (see Auth.Middleware); this handler makes the actual public-or-404 call by
// checking both existence and the owning namespace's public flag together,
// so a private page and a nonexistent page come out byte-identical — no
// existence oracle in a redirect-vs-404 split across two code paths.
func (app *App) handleViewPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	authed := app.currentUser(r) != ""

	content, blobHash, err := app.Store.Read(pageFile(slug))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if !authed {
				app.notFound(w, r)
				return
			}
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

	if !authed {
		app.handlePublicPage(w, r, slug, ParsePage(slug, content))
		return
	}

	page := ParsePage(slug, content)
	ns, _ := namespaceFor(slug)
	page.Body = injectTOC(page.Body, app.Index, app.Namespaces().IndexSlug(ns), ns)
	renderedBody, err := app.Render.Render(page.Body, ns)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	titles := app.Index.Titles()
	var backlinks []BacklinkEntry
	for _, bslug := range app.Index.Backlinks(slug) {
		if tokenAllowsSlug(r.Context(), bslug) {
			backlinks = append(backlinks, BacklinkEntry{Slug: bslug, Title: titles[bslug]})
		}
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

// handlePublicPage renders page for an anonymous viewer: content only, no
// widgets, no chrome that would call an authenticated endpoint. 404s
// (identically to a nonexistent slug — see handleViewPage) unless the
// page's namespace is public. hmd:toc is deliberately left unexpanded — a
// TOC would leak private page titles by construction — and wiki-links are
// rendered through Renderer.RenderPublic so a link to a private or
// nonexistent page unwraps to plain text instead of advertising it.
func (app *App) handlePublicPage(w http.ResponseWriter, r *http.Request, slug string, page Page) {
	ns := app.Namespaces()
	if !ns.IsPublic(slug) {
		app.notFound(w, r)
		return
	}

	pageNS, _ := namespaceFor(slug)
	isPublicLink := func(s string) bool { return app.Index.Exists(s) && ns.IsPublic(s) }
	renderedBody, err := app.Render.RenderPublic(page.Body, pageNS, isPublicLink)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	cfg := ns.Resolve(slug)
	skin := resolveSkin(cfg.Skin)
	palette := cfg.Palette
	if palette == "" {
		palette = skin.Palette
	}
	var sidebarTreeNS string
	var sidebarTreeEntries []BacklinkEntry
	if summary := namespaceSummaryFor(ns, app.Index.Titles(), pageNS); summary != nil {
		sidebarTreeNS = pageNS
		sidebarTreeEntries = summary.Pages
	}
	publishedTitle := namespaceDisplayTitle(pageNS, cfg)

	app.render(w, r, http.StatusOK, "page", TemplateData{
		Authed:             false,
		SiteName:           publishedTitle,
		Title:              page.Title,
		Slug:               slug,
		Content:            renderedBody,
		Namespace:          pageNS,
		NamespaceTitle:     publishedTitle,
		Skin:               skinName(cfg.Skin),
		ThemeStyle:         buildThemeStyle(userRecord{Palette: palette}),
		SidebarTreeNS:      sidebarTreeNS,
		SidebarTreeEntries: sidebarTreeEntries,
		// The outline widget builds its list from this page's own headings
		// client-side (toc.js) — no auth-only data or endpoint involved, so
		// it's safe for anonymous viewers same as the sidebar tree.
		RailWidgets: widgetsForSlot(slotRail, cfg.Widgets),
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

// isPageSlug reports whether slug can address a page at all: a namespace
// plus at least one segment inside it. A single segment names a namespace,
// so it is served by the namespace index or not at all.
func isPageSlug(slug string) bool {
	ns, rest := namespaceFor(slug)
	return ns != "" && rest != ""
}

// handlePageGet dispatches every read of a page's own URL. The action is
// selected by ?do= (edit/history/diff/rev), never by a path suffix — a page
// literally named "edit" is unambiguous, since "do" can never be part of
// the path. No do= at all means view. Any other value 404s rather than
// silently falling back to view, so a typoed ?do= doesn't look like success.
func (app *App) handlePageGet(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("path")
	if reservedPath(slug) {
		app.notFound(w, r)
		return
	}
	if !app.requireTokenSlug(w, r, slug) {
		return
	}
	r.SetPathValue("slug", slug)
	if r.URL.Query().Get("do") == "" {
		if name, ok := app.namespaceIndexName(slug); ok {
			app.handleNamespaceIndex(w, r, name)
			return
		}
	}
	if !isPageSlug(slug) {
		app.notFound(w, r)
		return
	}
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
		app.notFound(w, r)
	}
}

// namespaceIndexName reports whether path addresses a namespace's index: a
// single segment, trailing slash optional, that is present in the catalogue.
// A single segment can only ever be a namespace, since every page slug
// carries its namespace prefix.
func (app *App) namespaceIndexName(path string) (string, bool) {
	name := strings.TrimSuffix(path, "/")
	if name == "" || strings.Contains(name, "/") {
		return "", false
	}
	for _, entry := range namespaceSummaries(app.Namespaces(), app.Index.Titles()) {
		if entry.Name == name {
			return entry.Name, true
		}
	}
	return "", false
}

// handleNamespaceIndex lists the pages in one namespace — the browsable
// counterpart to the sidebar's namespace list. Anonymous visitors see it only
// for a public namespace, and get the same 404 as a private page otherwise.
func (app *App) handleNamespaceIndex(w http.ResponseWriter, r *http.Request, name string) {
	if !app.requireTokenNamespace(w, r, name) {
		return
	}
	authed := app.currentUser(r) != ""
	summary := namespaceSummaryFor(app.Namespaces(), app.Index.Titles(), name)
	if summary == nil || (!authed && !summary.Config.Public) {
		app.notFound(w, r)
		return
	}

	// A configured index page takes over the namespace root: hand off to the
	// normal page-view handler for it rather than duplicating its rendering
	// (auth, TOC, backlinks, ...) here. Falls back to the page list below if
	// the configured page doesn't exist.
	if summary.Config.Index != "" {
		indexSlug := namespaceSlug(name, summary.Config.Index)
		if _, ok := app.Index.Titles()[indexSlug]; ok {
			r.SetPathValue("slug", indexSlug)
			app.handleViewPage(w, r)
			return
		}
	}

	publishedTitle := namespaceDisplayTitle(name, summary.Config)
	var siteName, namespaceSkin string
	var themeStyle template.CSS
	if !authed {
		siteName = publishedTitle
		namespaceSkin = summary.Config.Skin
		themeStyle = buildThemeStyle(userRecord{Palette: summary.Config.Palette})
	}

	tagPages := filterBacklinkEntries(r.Context(), summary.Pages)
	app.render(w, r, http.StatusOK, "namespace", TemplateData{
		Authed:             authed,
		SiteName:           siteName,
		Title:              name,
		Slug:               name + "/",
		StatusMode:         "view",
		StatusContext:      fmt.Sprintf("%d pages", summary.Count),
		Namespace:          name,
		NamespacePublic:    summary.Config.Public,
		NamespaceTitle:     publishedTitle,
		Skin:               namespaceSkin,
		ThemeStyle:         themeStyle,
		IsNamespaceIndex:   true,
		TagPages:           tagPages,
		PageTree:           renderLiveTree(buildPageTree(tagPages, name), name, ""),
		SidebarTreeNS:      name,
		SidebarTreeEntries: summary.Pages,
	})
}

// handlePagePost dispatches every write to a page's own URL, selected by
// ?do=.
func (app *App) handlePagePost(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("path")
	if reservedPath(slug) {
		http.NotFound(w, r)
		return
	}
	if !app.requireTokenSlug(w, r, slug) {
		return
	}
	if !isPageSlug(slug) {
		http.NotFound(w, r)
		return
	}
	r.SetPathValue("slug", slug)
	switch r.URL.Query().Get("do") {
	case "save":
		app.handleSavePage(w, r)
	case "revert":
		app.handleRevert(w, r)
	case "rename":
		app.handleRenamePage(w, r)
	case "tags":
		app.handleSetTags(w, r)
	case "delete":
		app.handleDeletePage(w, r)
	default:
		http.NotFound(w, r)
	}
}

// handleDeletePage implements POST /{slug}?do=delete. Git history keeps the
// content recoverable, mirroring the MCP delete_page tool.
func (app *App) handleDeletePage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !app.requireTokenSlug(w, r, slug) {
		return
	}
	authorName, authorEmail := app.gitAuthor(app.currentUser(r))
	if err := app.Store.Remove(pageFile(slug), "Delete "+slug, authorName, authorEmail); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	app.Index.Remove(slug)
	slog.Info("deleted", "slug", slug, "by", app.currentUser(r))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleHiddenGet/Post mirror handlePageGet/Post for the /_/hidden/{path...}
// subtree: only view/edit/save make sense for a hidden page (no history,
// diff, revert or rename UI exists for it today).
func (app *App) handleHiddenGet(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("path")
	if !app.requireTokenSlug(w, r, slug) {
		return
	}
	r.SetPathValue("slug", slug)
	switch r.URL.Query().Get("do") {
	case "":
		app.handleViewHidden(w, r)
	case "edit":
		app.handleEditHidden(w, r)
	default:
		app.notFound(w, r)
	}
}

func (app *App) handleHiddenPost(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("path")
	if !app.requireTokenSlug(w, r, slug) {
		return
	}
	r.SetPathValue("slug", slug)
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
	if !app.requireTokenSlug(w, r, slug) {
		return
	}
	title := r.FormValue("title")
	body := r.FormValue("body")
	tagsInput := r.FormValue("tags")
	basehash := r.FormValue("basehash")
	hidden := r.FormValue("hidden") == "on"
	username := app.currentUser(r)
	targetSlug := slug
	if want := strings.TrimSpace(r.FormValue("new_slug")); want != "" && want != slug {
		targetSlug = want
		newNamespace, newPage := namespaceFor(targetSlug)
		if !validPagePath(newPage) {
			http.Error(w, "invalid filename", http.StatusBadRequest)
			return
		}
		// Crossing into another namespace files the page somewhere that
		// already exists; a typo in the path shouldn't conjure a namespace
		// directory. Staying put needs no such check — that's just a rename.
		if oldNamespace, _ := namespaceFor(slug); oldNamespace != newNamespace {
			if _, ok := app.Namespaces()[newNamespace]; !ok {
				http.Error(w, "unknown namespace", http.StatusBadRequest)
				return
			}
		}
		dest := pageFile(targetSlug)
		if hidden {
			dest = hiddenFile(targetSlug)
		}
		if _, _, err := app.Store.Read(dest); err == nil {
			http.Error(w, "a page with that filename already exists", http.StatusConflict)
			return
		}
		if !app.requireTokenSlug(w, r, targetSlug) {
			return
		}
	}

	newFile := pageFile(slug)
	newPrefix := ""
	if hidden {
		newFile = hiddenFile(targetSlug)
		newPrefix = "/_/hidden"
	}
	if !hidden {
		newFile = pageFile(targetSlug)
	}

	cfg := app.config()
	if cfg.SyncMode == "bidirectional" && cfg.Git.RemoteURL != "" {
		if _, err := app.Store.FetchAndFF(); err != nil {
			slog.Warn("save-time fetch", "slug", slug, "err", err)
		}
	}

	page := Page{Slug: targetSlug, Title: title, Tags: ParseTags(tagsInput), Body: body}
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
	blobHash, err := app.Store.SaveChecked(oldFile, newFile, basehash, page.Encode(), message, authorName, authorEmail)
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
		})
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	slog.Info("saved", "slug", slug, "file", newFile, "author", authorName, "message", message)

	if targetSlug != slug {
		app.Index.Remove(slug) // the page moved — drop the slug it left behind
	}
	if hidden {
		app.Index.Remove(slug)
	} else {
		if err := app.Index.UpdatePage(page, blobHash); err != nil {
			slog.Error("updating search index", "slug", page.Slug, "err", err)
		}
	}

	http.Redirect(w, r, newPrefix+"/"+targetSlug, http.StatusSeeOther)
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

	ns, _ := namespaceFor(r.URL.Query().Get("slug"))
	html, err := app.Render.Render(body, ns)
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
	if !app.requireTokenSlug(w, r, slug) {
		return
	}
	app.handleAttachmentUpload(w, r, slug, app.currentUser(r), "")
}

type uploadCapability struct {
	Slug     string
	Filename string
	User     string
	Expires  time.Time
}

func (app *App) handleCapabilityUpload(w http.ResponseWriter, r *http.Request) {
	value, ok := app.uploads.LoadAndDelete(r.PathValue("token"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	capability := value.(uploadCapability)
	if time.Now().After(capability.Expires) {
		http.Error(w, "upload URL expired", http.StatusGone)
		return
	}
	app.handleAttachmentUpload(w, r, capability.Slug, capability.User, capability.Filename)
}

func (app *App) handleAttachmentUpload(w http.ResponseWriter, r *http.Request, slug, username, expectedFilename string) {
	if !isPageSlug(slug) {
		http.Error(w, "invalid slug", http.StatusBadRequest)
		return
	}

	// Limit request body to the configured maximum
	maxBytes := app.config().MaxUploadBytes
	if maxBytes <= 0 {
		maxBytes = 10 * 1024 * 1024
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

	file, header, err := r.FormFile("file")
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "attachment exceeds maximum upload size", http.StatusRequestEntityTooLarge)
			return
		}
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

	ext = strings.ToLower(ext)

	filename = name + ext
	if expectedFilename != "" && filename != expectedFilename {
		http.Error(w, "filename does not match upload URL", http.StatusBadRequest)
		return
	}
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

	files := map[string][]byte{path: content}
	if app.Index.documents != nil || directTextAttachment(filename) {
		var tika *TikaClient
		if app.Index.documents != nil {
			tika = app.Index.documents.tika
		}
		text, extractErr := extractAttachmentText(r.Context(), tika, bytes.NewReader(content), filename)
		if extractErr == nil && text != "" {
			files[extractedAttachmentPath(path)] = encodeExtractedAttachment(attachmentBlobHash(content), text)
		}
	}

	// Save source and a successful extraction together.
	authorName, authorEmail := app.gitAuthor(username)
	_, err = app.Store.SaveAll(files, "Add attachment "+filename, authorName, authorEmail)
	if err != nil {
		http.Error(w, "error saving file", http.StatusInternalServerError)
		return
	}

	indexed := false
	var indexErr error
	var attachmentHash string
	if app.Index.documents != nil {
		file, hash, hashErr := app.Store.OpenAttachment(path)
		if hashErr != nil {
			indexErr = hashErr
		} else {
			attachmentHash = hash
			_ = file.Close()
			indexErr = app.Index.ReconcileAttachmentPath(path, hash)
		}
		indexed = indexErr == nil && app.Index.AttachmentIndexed(path, attachmentHash)
	}

	// Return JSON response. The Git commit is already durable even when the
	// disposable derived index cannot be rebuilt.
	w.Header().Set("Content-Type", "application/json")
	if indexErr != nil {
		slog.Warn("attachment indexing failed", "path", path, "err", indexErr)
		w.WriteHeader(http.StatusAccepted)
	} else if app.Index.documents != nil {
		w.WriteHeader(http.StatusCreated)
	}
	resp := map[string]interface{}{
		"url":     fmt.Sprintf("/_/attachments/%s/%s", slug, filename),
		"indexed": indexed,
	}
	if indexErr != nil {
		resp["index_error"] = indexErr.Error()
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Error("encoding attachment response", "err", err)
	}
}

// handleServeAttachment serves a page's attachment. Auth.Middleware lets an
// anonymous request through unconditionally (it can't know which namespace
// owns the attachment without a lookup of its own), so the public-or-404
// call is made here — identically whether the attachment is missing or the
// owning page's namespace just isn't public.
func (app *App) handleServeAttachment(w http.ResponseWriter, r *http.Request) {
	// {path...} is "{slug}/{file}"; slug itself may contain "/" for a
	// namespaced page, so only the last segment is ever the filename.
	path := r.PathValue("path")
	if strings.Contains(path, "/.hmd/") {
		http.NotFound(w, r)
		return
	}
	i := strings.LastIndex(path, "/")
	if i < 0 {
		http.NotFound(w, r)
		return
	}
	slug, file := path[:i], path[i+1:]
	if !app.requireTokenSlug(w, r, slug) {
		return
	}
	if !isPageSlug(slug) {
		http.NotFound(w, r)
		return
	}

	if app.currentUser(r) == "" && !app.Namespaces().IsPublic(slug) {
		http.NotFound(w, r)
		return
	}

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
	switch strings.ToLower(filepath.Ext(file)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
	default:
		w.Header().Set("Content-Disposition", "attachment")
	}
	http.ServeFile(w, r, repoPath)
}

func (app *App) handleTagsIndex(w http.ResponseWriter, r *http.Request) {
	app.render(w, r, http.StatusOK, "tags", TemplateData{
		Authed:  true,
		Title:   "Tags",
		AllTags: filterTagCounts(r.Context(), app.Index, app.Index.Tags()),
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
		if tokenAllowsSlug(r.Context(), slug) {
			pages = append(pages, BacklinkEntry{Slug: slug, Title: titles[slug]})
		}
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
		if !tokenAllowsSlug(r.Context(), hit.Slug) {
			continue
		}
		results = append(results, SearchResult{
			Slug:    hit.Slug,
			Title:   hit.Title,
			Snippet: template.HTML(hit.Snippet),
			Tags:    hit.Tags,
		})
	}
	var attachmentResults []AttachmentResult
	if app.Index.documents != nil && strings.TrimSpace(q) != "" {
		if hits, searchErr := app.Index.SearchAttachments(r.Context(), q, 50); searchErr != nil {
			slog.Warn("attachment search failed", "err", searchErr)
		} else {
			for _, hit := range hits {
				if !tokenAllowsSlug(r.Context(), hit.OwnerSlug) {
					continue
				}
				attachmentResults = append(attachmentResults, AttachmentResult{
					OwnerSlug: hit.OwnerSlug, Filename: hit.Filename, URL: hit.URL,
					Excerpt: template.HTML(hit.Excerpt), Score: hit.Score,
				})
				if len(attachmentResults) == 20 {
					break
				}
			}
		}
	}

	title := "Search"
	if q != "" {
		title = q
	}
	app.render(w, r, http.StatusOK, "search", TemplateData{
		Authed:                  true,
		Title:                   title,
		Query:                   q,
		SearchResults:           results,
		AttachmentResults:       attachmentResults,
		AttachmentSearchEnabled: app.Index.documents != nil,
		SearchHits:              len(results),
		AttachmentHits:          len(attachmentResults),
		SearchElapsed:           elapsed.String(),
		StatusMode:              "search",
	})
}

func (app *App) handleAttachmentSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.FormValue("q"))
	if q == "" {
		http.Error(w, "query cannot be empty", http.StatusBadRequest)
		return
	}
	hits, err := app.Index.SearchAttachments(r.Context(), q, 50)
	if err != nil {
		http.Error(w, "attachment search unavailable", http.StatusServiceUnavailable)
		return
	}
	results := make([]AttachmentResult, 0, 20)
	for _, hit := range hits {
		if !tokenAllowsSlug(r.Context(), hit.OwnerSlug) {
			continue
		}
		results = append(results, AttachmentResult{
			OwnerSlug: hit.OwnerSlug, Filename: hit.Filename, URL: hit.URL,
			Excerpt: template.HTML(hit.Excerpt), Score: hit.Score,
		})
		if len(results) == 20 {
			break
		}
	}
	app.render(w, r, http.StatusOK, "search", TemplateData{
		Authed: true, Title: q, Query: q, AttachmentResults: results,
		AttachmentSearchEnabled: true, StatusMode: "search",
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
		if !tokenAllowsSlug(r.Context(), hit.Slug) {
			continue
		}
		results = append(results, AutocompleteResult(hit))
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(results); err != nil {
		slog.Error("encoding search response", "err", err)
	}
}

func (app *App) handleAttachmentSearchAPI(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.FormValue("q"))
	if q == "" {
		http.Error(w, "query cannot be empty", http.StatusBadRequest)
		return
	}
	hits, err := app.Index.SearchAttachments(r.Context(), q, 50)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "attachment search unavailable"})
		return
	}
	results := make([]AttachmentResult, 0, 20)
	for _, hit := range hits {
		if !tokenAllowsSlug(r.Context(), hit.OwnerSlug) {
			continue
		}
		results = append(results, AttachmentResult{
			OwnerSlug: hit.OwnerSlug, Filename: hit.Filename, URL: hit.URL,
			Excerpt: template.HTML(hit.Excerpt), Score: hit.Score,
		})
		if len(results) == 20 {
			break
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(results); err != nil {
		slog.Error("encoding attachment search response", "err", err)
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
	if !app.requireTokenSlug(w, r, slug) {
		return
	}
	if !isPageSlug(slug) {
		http.NotFound(w, r)
		return
	}

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
	if !app.requireTokenSlug(w, r, slug) {
		return
	}
	newTitle := strings.TrimSpace(r.FormValue("title"))
	if newTitle == "" {
		http.Error(w, "missing title", http.StatusBadRequest)
		return
	}
	// A rename retitles a page in place: keep it in its namespace instead of
	// slugifying it out to the wiki root.
	renameNS, _ := namespaceFor(slug)
	if Slugify(newTitle) == "" {
		http.Error(w, "invalid title", http.StatusBadRequest)
		return
	}
	newSlug := namespaceSlug(renameNS, Slugify(newTitle))
	if !app.requireTokenSlug(w, r, newSlug) {
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
	for _, source := range sources {
		if !app.requireTokenSlug(w, r, source) {
			return
		}
	}

	page := ParsePage(slug, content)
	oldTitle := page.Title
	page.Title = newTitle
	page.Slug = newSlug
	authorName, authorEmail := app.gitAuthor(app.currentUser(r))
	message := fmt.Sprintf("Rename %s to %s", oldTitle, newTitle)
	newHash, err := app.Store.SaveChecked(pageFile(slug), pageFile(newSlug), hash, page.Encode(), message, authorName, authorEmail)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if newSlug != slug {
		app.Index.Remove(slug)
	}
	if err := app.Index.UpdatePage(page, newHash); err != nil {
		slog.Error("updating search index", "slug", page.Slug, "err", err)
	}

	// Rewrite [[wiki-links]] that resolved to the old slug.
	for _, src := range sources {
		srcContent, srcHash, err := app.Store.Read(pageFile(src))
		if err != nil {
			continue
		}
		srcPage := ParsePage(src, srcContent)
		srcNS, _ := namespaceFor(src)
		updated := wikiLinkRe.ReplaceAllStringFunc(srcPage.Body, func(m string) string {
			// Mirror Index.ResolveLink: the link pointed at the old page by
			// title, or — for casing that didn't match — by slug, namespace
			// first. Title alone isn't enough now that a slug can be namespaced.
			inner := m[2 : len(m)-2]
			if inner == oldTitle || namespaceSlug(srcNS, Slugify(inner)) == slug || Slugify(inner) == slug {
				return "[[" + newTitle + "]]"
			}
			return m
		})
		if updated == srcPage.Body {
			continue
		}
		srcPage.Body = updated
		if sourceHash, err := app.Store.SaveChecked(pageFile(src), pageFile(src), srcHash, srcPage.Encode(), "Update links after rename of "+oldTitle, authorName, authorEmail); err == nil {
			if err := app.Index.UpdatePage(srcPage, sourceHash); err != nil {
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
	if !app.requireTokenSlug(w, r, slug) {
		return
	}
	content, hash, err := app.Store.Read(pageFile(slug))
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	page := ParsePage(slug, content)
	page.Tags = ParseTags(r.FormValue("tags"))
	authorName, authorEmail := app.gitAuthor(app.currentUser(r))
	newHash, err := app.Store.SaveChecked(pageFile(slug), pageFile(slug), hash, page.Encode(), "Update tags for "+page.Title, authorName, authorEmail)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := app.Index.UpdatePage(page, newHash); err != nil {
		slog.Error("updating search index", "slug", page.Slug, "err", err)
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"ok": true, "slug": slug}); err != nil {
		slog.Error("encoding tags response", "err", err)
	}
}

func (app *App) handleHealthReport(w http.ResponseWriter, r *http.Request) {
	titles := app.Index.Titles()
	missing, orphans := app.Index.Health(app.Namespaces().IndexSlugs())
	missing, orphans = filterHealth(r.Context(), missing, orphans)

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

type HealthMissingEntry struct {
	Slug    string   `json:"slug"`
	Sources []string `json:"sources"`
}

type HealthReport struct {
	Missing []HealthMissingEntry `json:"missing"`
	Orphans []string             `json:"orphans"`
}

// handleHealthAPI is the read-only JSON form of handleHealthReport, scopable
// to one namespace with ?namespace=. Access-token namespace restrictions
// apply the same as the HTML report (filterHealth), on top of the requested
// scope.
func (app *App) handleHealthAPI(w http.ResponseWriter, r *http.Request) {
	namespace := r.URL.Query().Get("namespace")
	if namespace != "" {
		if !validNamespaceName(namespace) {
			http.Error(w, `{"error":"invalid namespace"}`, http.StatusBadRequest)
			return
		}
		if !app.requireTokenNamespace(w, r, namespace) {
			return
		}
	}

	missing, orphans := app.Index.Health(app.Namespaces().IndexSlugs())
	missing, orphans = filterHealth(r.Context(), missing, orphans)
	if namespace != "" {
		missing, orphans = filterHealthNamespace(missing, orphans, namespace)
	}

	report := HealthReport{Missing: []HealthMissingEntry{}, Orphans: orphans}
	if report.Orphans == nil {
		report.Orphans = []string{}
	}
	for slug, sources := range missing {
		report.Missing = append(report.Missing, HealthMissingEntry{Slug: slug, Sources: sources})
	}
	sort.Slice(report.Missing, func(i, j int) bool { return report.Missing[i].Slug < report.Missing[j].Slug })

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(report); err != nil {
		slog.Error("encoding health response", "err", err)
	}
}

func (app *App) handlePageDiff(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !app.requireTokenSlug(w, r, slug) {
		return
	}
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
	if !app.requireTokenSlug(w, r, slug) {
		return
	}

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
	if !app.requireTokenSlug(w, r, slug) {
		return
	}
	hash := r.URL.Query().Get("hash")

	// Get old version
	content, err := app.Store.FileAt(pageFile(slug), hash)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	page := ParsePage(slug, content)
	ns, _ := namespaceFor(slug)
	page.Body = injectTOC(page.Body, app.Index, app.Namespaces().IndexSlug(ns), ns)
	renderedBody, err := app.Render.Render(page.Body, ns)
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
	if !app.requireTokenSlug(w, r, slug) {
		return
	}
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
		slug := hiddenSlug(p)
		if !tokenAllowsSlug(r.Context(), slug) {
			continue
		}
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
	if !app.requireTokenSlug(w, r, slug) {
		return
	}
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
	hiddenNS, _ := namespaceFor(slug)
	renderedBody, err := app.Render.Render(page.Body, hiddenNS)
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
	if !app.requireTokenSlug(w, r, slug) {
		return
	}
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
	prefs := app.Auth.prefs(user)

	sd := buildSettingsData(cfg, prefs)
	sd.HelpDrifted = HelpDrifted(app.Store)
	sd.UserGitAuthor = app.Auth.AuthorFor(user)
	wiki := app.wikiConfig()
	sd.WikiConfigPath = wikiConfigFile
	sd.WikiLanding = wiki.Landing
	sd.WikiSiteName = wiki.SiteName
	sd.Users = app.Auth.Users()
	sd.AllScopes = []string{string(scopeRead), string(scopeWrite), string(scopeSettings)}
	sd.CurrentUser = user
	for _, summary := range namespaceSummaries(app.Namespaces(), app.Index.Titles()) {
		sd.TokenNamespaces = append(sd.TokenNamespaces, summary.Name)
	}
	for _, t := range app.Auth.TokensFor(user) {
		expires := "never"
		switch {
		case t.expired():
			expires = "expired"
		case !t.Expires.IsZero():
			expires = t.Expires.Format("2006-01-02")
		}
		effectiveScopes := effectiveTokenScopes(prefs.Scopes, t.Scopes)
		isAdmin := userRecord{Scopes: effectiveScopes}.hasScope(scopeSettings)
		namespaceLabel := "All namespaces"
		if isAdmin {
			namespaceLabel = "Administrator"
		} else if len(t.Namespaces) > 0 {
			namespaceLabel = strings.Join(t.Namespaces, ", ") + " only"
		}
		scopeLabel := "Inherited scopes"
		if t.Scopes != nil {
			scopeLabel = strings.Join(t.Scopes, ", ")
		}
		sd.Tokens = append(sd.Tokens, TokenView{
			Name:           t.Name,
			Created:        relativeTime(t.Created),
			Expires:        expires,
			Scopes:         append([]string(nil), t.Scopes...),
			Namespaces:     append([]string(nil), t.Namespaces...),
			ScopeLabel:     scopeLabel,
			NamespaceLabel: namespaceLabel,
		})
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
	if q := r.URL.Query().Get("wiki-saved"); q == "1" {
		sd.Flash = "Wiki settings saved to " + sd.WikiConfigPath
	}
	app.renderSettings(w, r, sd, "admin")
}

// handleCreateToken mints a PAT for the current user and re-renders the
// settings page with the value — the only time it is ever displayed.
func (app *App) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		sd := app.settingsData(r)
		sd.TokenError = "Could not read token form"
		app.renderSettings(w, r, sd, "settings")
		return
	}
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
	scopes := append([]string(nil), r.Form["scopes"]...)
	if len(scopes) == 0 {
		sd := app.settingsData(r)
		sd.TokenError = "Token needs at least one scope"
		app.renderSettings(w, r, sd, "settings")
		return
	}

	knownNamespaces := make(map[string]struct{})
	for _, summary := range namespaceSummaries(app.Namespaces(), app.Index.Titles()) {
		knownNamespaces[summary.Name] = struct{}{}
	}
	namespaces := make([]string, 0, len(r.Form["namespaces"]))
	for _, raw := range r.Form["namespaces"] {
		namespace := strings.TrimSpace(raw)
		if namespace == "" {
			sd := app.settingsData(r)
			sd.TokenError = "invalid namespace"
			app.renderSettings(w, r, sd, "settings")
			return
		}
		if _, ok := knownNamespaces[namespace]; !ok {
			sd := app.settingsData(r)
			sd.TokenError = fmt.Sprintf("unknown namespace %q", namespace)
			app.renderSettings(w, r, sd, "settings")
			return
		}
		namespaces = append(namespaces, namespace)
	}
	if len(namespaces) == 0 {
		namespaces = nil
	}
	user := app.currentUser(r)
	token, err := app.Auth.AddToken(user, label, expires, scopes, namespaces)
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

func (app *App) namespaceManagementData(r *http.Request, name, errMsg string) NamespaceManagementData {
	user := app.currentUser(r)
	prefs := app.Auth.prefs(user)
	data := NamespaceManagementData{
		CanWrite:     prefs.hasScope(scopeWrite),
		CanSettings:  prefs.hasScope(scopeSettings),
		Rows:         filterNamespaceSummaries(r.Context(), namespaceSummaries(app.Namespaces(), app.Index.Titles())),
		WidgetGroups: widgetSlotGroups(),
		SlugPresets:  slugPresetViews(user),
		SkinNames:    skinNames,
		PaletteNames: themePresetNames,
		Palettes:     themePresets,
		SkinPalettes: skinPalettes(),
		Now:          time.Now(),
		Error:        errMsg,
	}
	for _, entry := range namespaceListEntries(app.Namespaces(), user) {
		if entry.Name == name && tokenAllowsNamespace(r.Context(), entry.Name) {
			data.Form = entry
			break
		}
	}
	return data
}

func (app *App) renderNamespace(w http.ResponseWriter, r *http.Request, status int, templateName, name, errMsg string) {
	app.render(w, r, status, templateName, TemplateData{
		Authed:              true,
		Title:               "Namespaces",
		StatusMode:          "settings",
		NamespaceManagement: ptrNamespaceManagement(app.namespaceManagementData(r, name, errMsg)),
	})
}

func ptrNamespaceManagement(data NamespaceManagementData) *NamespaceManagementData { return &data }

func (app *App) handleNamespacesGet(w http.ResponseWriter, r *http.Request) {
	data := app.namespaceManagementData(r, "", "")
	if r.URL.Query().Get("saved") == "1" {
		data.Flash = "Namespace settings saved"
	}
	app.render(w, r, http.StatusOK, "namespaces", TemplateData{Authed: true, Title: "Namespaces", StatusMode: "settings", NamespaceManagement: &data})
}

func (app *App) handleNamespaceNewGet(w http.ResponseWriter, r *http.Request) {
	data := app.namespaceManagementData(r, "", "")
	data.Form = NamespaceListEntry{Template: defaultNewPageTemplate, SlugPreset: slugPresets[0].Key}
	app.render(w, r, http.StatusOK, "namespace-edit", TemplateData{Authed: true, Title: "New namespace", StatusMode: "settings", NamespaceManagement: &data})
}

func (app *App) handleNamespaceEditGet(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !app.requireTokenNamespace(w, r, name) {
		return
	}
	if !validNamespaceName(name) {
		app.notFound(w, r)
		return
	}
	data := app.namespaceManagementData(r, name, "")
	if data.Form.Name == "" {
		app.notFound(w, r)
		return
	}
	app.render(w, r, http.StatusOK, "namespace-edit", TemplateData{Authed: true, Title: name + " namespace", StatusMode: "settings", NamespaceManagement: &data})
}

// handleSaveNamespace implements POST /_/settings/namespaces: writes one
// namespace's .namespace.yaml from the system-configuration form, creating
// the namespace (i.e. its directory) on first save. Both the "new namespace"
// row and the per-namespace rows post here — an existing namespace is just a
// save whose name already exists.
func (app *App) handleSaveNamespace(w http.ResponseWriter, r *http.Request) {
	name := strings.Trim(strings.TrimSpace(r.FormValue("name")), "/")
	if !app.requireTokenNamespace(w, r, name) {
		return
	}
	fail := func(msg string) {
		app.renderNamespace(w, r, http.StatusOK, "namespace-edit", name, msg)
	}

	// A namespace name is a single directory segment, and must be one a
	// namespace may actually take.
	if !validNamespaceName(name) {
		fail(fmt.Sprintf("%q is not a valid namespace name: one path segment, not %q, not dot-prefixed", name, reservedNamespace))
		return
	}

	var ids []string
	for _, id := range strings.Split(r.FormValue("widgets"), ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := widgets[id]; !ok {
			fail(fmt.Sprintf("unknown widget %q", id))
			return
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		ids = builtinWidgets
	}

	// The template page name is a convention, not a question the form asks: it
	// arrives as a hidden field so a hand-written config naming something
	// other than defaultNewPageTemplate survives a save here.
	template := strings.TrimSpace(r.FormValue("template"))
	if template == "" {
		template = defaultNewPageTemplate
	}
	// It names a hidden page inside the namespace — the same one-segment shape
	// handleNewPage's rendered slug has to satisfy.
	if !validMCPPageSegment(template) {
		fail(fmt.Sprintf("%q is not a valid template page name: one segment, no slashes, no leading dot", template))
		return
	}
	// Whether this save is what brings the namespace into being, which decides
	// if it gets a template page seeded below.
	creating := !app.Namespaces()[name].Configured

	cfg := NamespaceConfig{Widgets: ids, Public: r.FormValue("public") == "on", Title: strings.TrimSpace(r.FormValue("title")), Description: r.FormValue("description"), Skin: strings.TrimSpace(r.FormValue("skin")), Palette: strings.TrimSpace(r.FormValue("palette")), Index: strings.TrimSpace(r.FormValue("index"))}
	if r.FormValue("new_enabled") == "on" {
		// The slug comes from the preset select; only "custom" falls through
		// to the raw pattern field.
		slug := slugPatternFor(r.FormValue("slug_preset"))
		if slug == "" {
			slug = strings.TrimSpace(r.FormValue("slug_custom"))
		}
		if slug == "" {
			fail("choose how new pages here are named, or give a custom pattern")
			return
		}
		// Reject a pattern that doesn't parse (or renders to something
		// unusable) here, at the form, rather than at ctrl-j time.
		rendered, err := renderNewPageText(slug, newPageTemplateData{Now: time.Now(), User: app.currentUser(r), Namespace: name})
		if err != nil {
			fail("slug pattern is not a valid template: " + err.Error())
			return
		}
		if !validMCPPageSegment(rendered) {
			fail(fmt.Sprintf("that pattern names a page %q, which isn't usable: no slashes, no leading dot, not empty", rendered))
			return
		}
		cfg.New = &NewPageConfig{Template: template, Slug: slug}
	}
	var err error
	cfg, err = normaliseNamespaceConfig(name, cfg, newPageTemplateData{Now: time.Now(), User: app.currentUser(r), Namespace: name})
	if err != nil {
		fail(err.Error())
		return
	}

	data, err := cfg.Encode()
	if err != nil {
		fail("encoding namespace config: " + err.Error())
		return
	}
	authorName, authorEmail := app.gitAuthor(app.currentUser(r))
	path := namespaceConfigPath(name)
	if _, err := app.Store.Save(path, data, "Configure namespace "+path, authorName, authorEmail); err != nil {
		slog.Error("saving namespace config", "namespace", name, "err", err)
		fail("failed to save namespace config: " + err.Error())
		return
	}

	// Every namespace born here gets a template page, and so does one that has
	// new pages switched on — the first so there's something to edit and read
	// before ctrl-j is ever involved, the second so the first ctrl-j doesn't
	// land in a namespace whose declared template doesn't exist. Never
	// overwrites, so this can't resurrect a template someone deleted.
	if creating || cfg.New != nil {
		if err := app.ensureNewPageTemplate(name, template, authorName, authorEmail); err != nil {
			slog.Warn("seeding namespace template page", "namespace", name, "err", err)
		}
	}

	app.refreshNamespaces()
	slog.Info("namespace configured", "namespace", name, "by", app.currentUser(r))
	http.Redirect(w, r, "/_/namespaces?saved=1", http.StatusSeeOther)
}

// handleResetNamespace removes only the namespace configuration. Pages and
// hidden files remain available, so reset can never be mistaken for deletion.
func (app *App) handleResetNamespace(w http.ResponseWriter, r *http.Request) {
	name := strings.Trim(strings.TrimSpace(r.FormValue("name")), "/")
	if !app.requireTokenNamespace(w, r, name) {
		return
	}
	if !validNamespaceName(name) {
		http.Error(w, "invalid namespace name", http.StatusBadRequest)
		return
	}
	authorName, authorEmail := app.gitAuthor(app.currentUser(r))
	path := namespaceConfigPath(name)
	if err := app.Store.Remove(path, "Reset namespace settings "+path, authorName, authorEmail); err != nil {
		app.renderNamespace(w, r, http.StatusOK, "namespace-edit", name, "failed to reset namespace settings: "+err.Error())
		return
	}
	app.refreshNamespaces()
	http.Redirect(w, r, "/_/namespaces?saved=1", http.StatusSeeOther)
}

// handleDeleteNamespace implements POST /_/settings/namespaces/delete. True
// deletion is only allowed for a configured namespace whose directory contains
// no pages or hidden files.
func (app *App) handleDeleteNamespace(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if !app.requireTokenNamespace(w, r, name) {
		return
	}
	authorName, authorEmail := app.gitAuthor(app.currentUser(r))
	path := namespaceConfigPath(name)
	if err := app.Store.DeleteNamespace(name, "Remove namespace config "+path, authorName, authorEmail); err != nil {
		if errors.Is(err, errInvalidNamespaceName) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		app.renderNamespace(w, r, http.StatusOK, "namespace-edit", name, err.Error())
		return
	}

	app.refreshNamespaces()
	slog.Info("namespace config removed", "namespace", name, "by", app.currentUser(r))
	http.Redirect(w, r, "/_/namespaces?saved=1", http.StatusSeeOther)
}

func (app *App) handleDeleteNamespaceAll(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if !app.requireTokenNamespace(w, r, name) {
		return
	}
	authorName, authorEmail := app.gitAuthor(app.currentUser(r))
	if err := app.Store.DeleteNamespaceAll(name, "Delete namespace "+name, authorName, authorEmail); err != nil {
		if errors.Is(err, errInvalidNamespaceName) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		app.renderNamespace(w, r, http.StatusOK, "namespace-edit", name, err.Error())
		return
	}
	for slug := range app.Index.Titles() {
		ns, _ := namespaceFor(slug)
		if ns == name {
			app.Index.Remove(slug)
		}
	}
	app.refreshNamespaces()
	slog.Info("namespace deleted with all files", "namespace", name, "by", app.currentUser(r))
	http.Redirect(w, r, "/_/namespaces?saved=1", http.StatusSeeOther)
}

// refreshNamespaces rebuilds the registry from disk immediately. pollFS does
// this on its own timer anyway; the settings handlers call it so the redirect
// they issue already reflects the save.
func (app *App) refreshNamespaces() {
	reg, err := BuildNamespaceRegistry(app.config().RepoDir)
	if err != nil {
		slog.Warn("rebuilding namespace registry", "err", err)
		return
	}
	app.SetNamespaces(reg)
}

// refreshWikiConfig reloads repository settings after a pull or direct edit.
func (app *App) refreshWikiConfig() {
	wiki, _, err := LoadWikiConfig(app.config().RepoDir)
	if err != nil {
		slog.Warn("reloading wiki config", "err", err)
		return
	}
	app.SetWikiConfig(wiki)
}

// ensureNewPageTemplate creates a namespace's new-page template as a hidden
// page if it doesn't exist yet, with a body that documents the template data
// available to it. A no-op when the page is already there — an existing
// template is never overwritten.
func (app *App) ensureNewPageTemplate(ns, template, authorName, authorEmail string) error {
	slug := namespaceSlug(ns, template)
	if _, _, err := app.Store.Read(hiddenFile(slug)); err == nil {
		return nil
	}
	page := Page{
		Slug:  slug,
		Title: `{{.Now.Format "Monday, 2 January 2006"}}`,
		Body:  newPageTemplateBody(ns),
	}
	if ns != "" {
		page.Tags = []string{ns}
	}
	_, err := app.Store.Save(hiddenFile(slug), page.Encode(), "Add new-page template "+slug, authorName, authorEmail)
	return err
}

// newPageTemplateFields is every field a template page can use — the whole of
// newPageTemplateData, which is deliberately small. Named without braces so
// the seeded body can list them as inert text and still show them working in
// the column beside.
var newPageTemplateFields = []string{
	`.Now.Format "2006-01-02"`,
	`.Now.Format "Monday, 2 January 2006"`,
	`.Now.Format "15:04"`,
	`.User`,
	`.Namespace`,
}

// newPageTemplateBody is the body every seeded template page starts with: it
// explains what a template page is and lists the available fields beside what
// each one produces. The left column names fields without braces, so it reads
// the same in the template's own editor as in a page created from it; the right
// column is live, so opening the template shows the syntax and opening a
// created page shows the values.
func newPageTemplateBody(ns string) string {
	where := "the root of the wiki"
	if ns != "" {
		where = "`" + ns + "/`"
	}

	var table strings.Builder
	table.WriteString("| field | what it puts on the page |\n| --- | --- |\n")
	for _, f := range newPageTemplateFields {
		table.WriteString("| `" + f + "` | {{" + f + "}} |\n")
	}

	return "This is the template page for " + where + ". Every page created here " +
		"starts as a copy of it, so whatever you leave in it — headings, a " +
		"checklist, tags — is what a new page begins with.\n\n" +
		"Wrap a field in double braces to have it filled in when the page is " +
		"created; the title of this page does exactly that. Title, tags and " +
		"body are all substituted, and these are the only fields there are:\n\n" +
		table.String() +
		"\nDates use Go's layout syntax: write out the reference time " +
		"`2006-01-02 15:04` in the shape you want it, so `02/01/2006` gives " +
		`{{.Now.Format "02/01/2006"}}` + " and `Jan 2` gives " +
		`{{.Now.Format "Jan 2"}}` + ".\n\n" +
		"This page is hidden — it never shows up in the page list, search, tags " +
		"or backlinks. Delete all of this and make it yours.\n"
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

// handleSaveWikiConfig writes the portable settings to the content repository
// rather than the instance's local config.yaml.
func (app *App) handleSaveWikiConfig(w http.ResponseWriter, r *http.Request) {
	wiki := WikiConfig{
		Landing:  strings.TrimSpace(r.FormValue("landing")),
		SiteName: strings.TrimSpace(r.FormValue("site_name")),
	}
	if wiki.SiteName == "" {
		http.Error(w, "site name cannot be empty", http.StatusBadRequest)
		return
	}
	if !validWikiLanding(wiki.Landing) {
		http.Error(w, "landing must be a namespace index such as notes/ or a page such as notes/inbox", http.StatusBadRequest)
		return
	}
	data, err := wiki.Encode()
	if err != nil {
		http.Error(w, "failed to encode wiki settings", http.StatusInternalServerError)
		return
	}
	authorName, authorEmail := app.gitAuthor(app.currentUser(r))
	if _, err := app.Store.Save(wikiConfigFile, data, "Configure wiki settings", authorName, authorEmail); err != nil {
		slog.Error("saving wiki config", "err", err)
		http.Error(w, "failed to save wiki settings", http.StatusInternalServerError)
		return
	}
	app.SetWikiConfig(wiki)
	slog.Info("wiki settings updated", "by", app.currentUser(r))
	http.Redirect(w, r, "/_/admin?wiki-saved=1", http.StatusSeeOther)
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
