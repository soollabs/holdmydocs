package web

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"hmd/internal/api"
	"hmd/internal/presentation"
	"hmd/internal/wiki"
)

var buildVersion = "dev"
var version = envOr("HMD_VERSION", buildVersion)

// App is the browser adapter: it renders HTML, owns templates and static
// assets, and delegates shared application state to API.
//
// API is set by the composition root. The lazy apiClient accessor lets unit
// tests construct a bare App without wiring the full dependency graph.
type App struct {
	API    *api.API
	Store  *Store
	Auth   *Auth
	Index  *Index
	Render *Renderer
	Tmpl   map[string]*template.Template
	OIDC   *OIDCAuth // nil when OIDC is disabled
}

func (app *App) apiClient() *api.API {
	if app.API == nil {
		app.API = api.New(app.Store, app.Index, app.Auth)
	}
	return app.API
}

func (app *App) config() Config { return app.apiClient().Config() }

// SetConfig stores a new configuration value atomically.
func (app *App) SetConfig(cfg Config) { app.apiClient().SetConfig(cfg) }

func (app *App) wikiConfig() WikiConfig { return app.apiClient().WikiConfig() }

// SetWikiConfig stores new repository-level settings atomically.
func (app *App) SetWikiConfig(cfg WikiConfig) { app.apiClient().SetWikiConfig(cfg) }

// Namespaces returns the current namespace registry.
func (app *App) Namespaces() wiki.NamespaceRegistry { return app.apiClient().Namespaces() }

// SetNamespaces stores the current namespace registry.
func (app *App) SetNamespaces(reg wiki.NamespaceRegistry) { app.apiClient().SetNamespaces(reg) }

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
	PreviousPage            *pageLink
	NextPage                *pageLink
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
	CSRFToken               string
	CSPNonce                string
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

	// Widget data — populated in app.render only when something on the page actually reads it (see
	// populateWidgetData).
	Calendar           CalendarMonth
	WritingStats       WritingStats
	PinnedPages        []BacklinkEntry
	PrevEntries        []PrevEntry
	NamespaceNav       []NamespaceNavEntry
	SidebarTreeNS      string          // namespace used to stage SidebarTreeEntries
	SidebarTreeEntries []BacklinkEntry // unfiltered entries; filtered during render
	SidebarTree        template.HTML
	NewPageEnabled     bool   // whether ctrl-j is available
	NewNamespace       string // which namespace ctrl-j targets
}

// NamespaceManagementData is deliberately smaller than SettingsData: the namespace directory and editor do
// not need the system configuration model.
type NamespaceTreeItem struct {
	Path    string
	Title   string
	Folder  bool
	IsIndex bool
}

type NamespaceManagementData struct {
	CanWrite     bool
	CanSettings  bool
	Rows         []api.NamespaceSummary
	Form         NamespaceListEntry
	WidgetGroups []widgetSlotGroup
	SlugPresets  []slugPresetView
	SkinNames    []string
	PaletteNames []string
	Palettes     map[string]themePreset // JSON-encoded for the published-view preview
	SkinPalettes map[string]string      // skin -> default palette for the preview
	Now          time.Time
	TreeEditor   template.HTML
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
	ExportSecretVars []string      // secret environment variables excluded from export
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

func remoteHost(raw string) string {
	if raw == "" {
		return ""
	}
	s := raw
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	} else if i := strings.Index(s, "@"); i >= 0 {
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

func buildSettingsData(cfg Config, prefs userRecord) SettingsData {
	fields := make(map[string]FieldState)

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
	fields["MaxUploadBytes"] = mkField(strconv.FormatInt(cfg.MaxUploadBytes, 10), "MaxUploadBytes", false, false)
	fields["SyncPollMs"] = mkField(strconv.Itoa(cfg.SyncPollMs), "SyncPollMs", false, false)
	fields["RepoDir"] = mkField(cfg.RepoDir, "RepoDir", true, false)
	appDirEnv := ""
	if os.Getenv("HMD_APP_DIR") != "" {
		appDirEnv = "HMD_APP_DIR"
	}
	fields["AppDir"] = FieldState{Value: cfg.AppDir, Editable: false, RestartRequired: true, EnvVar: appDirEnv, BootstrapOnly: true}
	fields["RemoteURL"] = mkField(cfg.Git.RemoteURL, "Git.RemoteURL", false, false)
	fields["GitUser"] = mkField(cfg.Git.User, "Git.User", false, false)
	fields["GitAuthor"] = mkField(cfg.Git.Author, "Git.Author", false, false)

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
	fields["DefaultBranch"] = mkField(cfg.DefaultBranch, "DefaultBranch", false, false)
	fields["Skin"] = mkField(cfg.Skin, "Skin", false, false)
	fields["Debug"] = mkField(strconv.FormatBool(cfg.Debug), "Debug", true, false)
	fields["BaseURL"] = mkField(cfg.BaseURL, "BaseURL", true, false)
	fields["TrustedProxies"] = mkField(strings.Join(cfg.TrustedProxies, ", "), "TrustedProxies", true, false)
	fields["GitTokenFile"] = mkField(cfg.Git.TokenFile, "Git.TokenFile", false, false)
	fields["MCPEnabled"] = mkField(strconv.FormatBool(cfg.MCP.Enabled), "MCP.Enabled", true, false)
	fields["DocumentModel"] = mkField(cfg.DocumentSearch.Model, "DocumentSearch.Model", true, false)
	fields["DocumentModelDir"] = mkField(cfg.DocumentSearch.ModelDir, "DocumentSearch.ModelDir", true, false)
	fields["DocumentIndexDir"] = mkField(cfg.DocumentSearch.IndexDir, "DocumentSearch.IndexDir", true, false)
	fields["OIDCIssuer"] = mkField(cfg.OIDC.Issuer, "OIDC.Issuer", true, false)
	fields["OIDCClientID"] = mkField(cfg.OIDC.ClientID, "OIDC.ClientID", true, false)
	oidcSecretValue := "not set"
	if cfg.OIDC.ClientSecret != "" {
		oidcSecretValue = "set"
	}
	fields["OIDCClientSecret"] = FieldState{Value: oidcSecretValue, Editable: cfg.EnvOverrides["OIDC.ClientSecret"] == "", RestartRequired: true, EnvVar: cfg.EnvOverrides["OIDC.ClientSecret"]}
	fields["OIDCClientSecretFile"] = mkField(cfg.OIDC.ClientSecretFile, "OIDC.ClientSecretFile", true, false)
	fields["OIDCLocalLogin"] = mkField(strconv.FormatBool(cfg.OIDC.LocalLogin), "OIDC.LocalLogin", true, false)
	fields["OIDCButtonText"] = mkField(cfg.OIDC.ButtonText, "OIDC.ButtonText", true, false)
	fields["OIDCIcon"] = mkField(cfg.OIDC.Icon, "OIDC.Icon", true, false)
	fields["OIDCDefaultScopes"] = mkField(strings.Join(cfg.OIDC.DefaultScopes, ", "), "OIDC.DefaultScopes", true, false)
	fields["OIDCAllowedSubjects"] = mkField(strings.Join(cfg.OIDC.AllowedSubjects, ", "), "OIDC.AllowedSubjects", true, false)
	fields["OIDCAllowedEmailDomains"] = mkField(strings.Join(cfg.OIDC.AllowedEmailDomains, ", "), "OIDC.AllowedEmailDomains", true, false)
	fields["OIDCAllowAnyAuthenticated"] = mkField(strconv.FormatBool(cfg.OIDC.AllowAnyAuthenticated), "OIDC.AllowAnyAuthenticated", true, false)
	fields["OIDCAllowInsecureLoopback"] = mkField(strconv.FormatBool(cfg.OIDC.AllowInsecureLoopback), "OIDC.AllowInsecureLoopback", true, false)
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

func exportSecretVars(cfg Config) []string {
	return nil
}

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
	if cookie, err := r.Cookie("hmd_session"); err == nil {
		data.CSRFToken = app.Auth.CSRFToken(cookie.Value)
	}
	// Anonymous error pages deliberately remain byte-identical so private and
	// missing pages cannot become an existence oracle.
	if data.Authed || data.NamespacePublic {
		data.CSPNonce = cspNonce(r.Context())
	}
	// Use the current page's namespace as the default destination.
	if data.Namespace == "" {
		data.Namespace, _ = wiki.NamespaceFor(data.Slug)
	}
	if data.Authed {
		health, err := app.apiClient().Health(r.Context(), "")
		if err != nil {
			slog.Error("reading health summary", "err", err)
		}
		data.HealthMissing = len(health.Missing)
		data.HealthOrphans = len(health.Orphans)
		// Default the tags widget to the current page's namespace tags, but
		// preserve an explicit whole-wiki list supplied by the handler (the
		// tags index).
		if data.AllTags == nil {
			ns, _ := wiki.NamespaceFor(data.Slug)
			data.AllTags = app.apiClient().NamespaceTags(r.Context(), ns)
		}
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
		prefs := app.Auth.Prefs(app.currentUser(r))
		data.CanWrite = prefs.HasScope(scopeWrite)
		data.CanSettings = prefs.HasScope(scopeSettings)
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
				if tokenAllowsSlug(r.Context(), wiki.NamespaceSlug(name, "")) {
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
		target, _ := wiki.NamespaceFor(data.Slug)
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

		app.populateWidgetData(r.Context(), &data, activeSkin)
		for i := len(data.NamespaceNav) - 1; i >= 0; i-- {
			if !tokenAllowsNamespace(r.Context(), data.NamespaceNav[i].Name) {
				data.NamespaceNav = append(data.NamespaceNav[:i], data.NamespaceNav[i+1:]...)
			}
		}
		data.PinnedPages = filterBacklinkEntries(r.Context(), data.PinnedPages)
	}
	// Public pages stage the tree before rendering because widget data is authenticated-only.
	if data.SidebarTreeNS != "" {
		entries := filterBacklinkEntries(r.Context(), data.SidebarTreeEntries)
		_, currentPath := wiki.NamespaceFor(data.Slug)
		cfg := app.Namespaces().Resolve(data.SidebarTreeNS)
		tree := buildPageTree(entries, data.SidebarTreeNS, cfg.Index, cfg.Tree)
		data.SidebarTree = renderLiveTree(tree, data.SidebarTreeNS, currentPath)
		if name == "page" && data.RevHash == "" && data.RoutePrefix == "" {
			data.PreviousPage, data.NextPage = pageNeighbours(orderedTreePages(tree), currentPath, func(to string) string {
				return "/" + data.SidebarTreeNS + "/" + to
			})
		}
	}

	// Load Mermaid only for pages that contain Mermaid content.
	if strings.Contains(string(data.Content), "class=\"mermaid\"") ||
		strings.Contains(data.Body, "mermaid") {
		data.MermaidNeeded = true
	}

	tmpl, ok := app.Tmpl[name]
	if !ok {
		http.Error(w, "template not found: "+name, http.StatusInternalServerError)
		return
	}

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
	if user := userFromContext(r.Context()); user != "" {
		return user
	}
	cookie, err := r.Cookie("hmd_session")
	if err != nil {
		return ""
	}
	user, _ := app.Auth.UserFor(cookie.Value)
	return user
}

func (app *App) notFound(w http.ResponseWriter, r *http.Request) {
	app.errorPage(w, r, http.StatusNotFound, "Not found", "That page doesn't exist.")
}

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

func (app *App) gitAuthor(username string) (name, email string) {
	raw := app.Auth.AuthorFor(username)
	if raw == "" {
		raw = app.config().Git.Author
	}
	return parseAuthor(raw, username)
}

var tocToken = regexp.MustCompile(`<!-- hmd:toc(?::([a-z0-9,-]+))? -->`)

func injectTOC(body string, ix *Index, indexSlug string, ns string) string {
	if !strings.Contains(body, "hmd:toc") {
		return body
	}
	titles := ix.Titles()
	inNS := func(slug string) bool {
		pageNS, _ := wiki.NamespaceFor(slug)
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

func (app *App) landingPath() string {
	if slug := app.wikiConfig().Landing; slug != "" {
		return "/" + slug
	}
	if names := app.Namespaces().Names(); len(names) > 0 {
		return "/" + names[0] + "/"
	}
	return "/_/namespaces"
}

func (app *App) handleRoot(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, app.landingPath(), http.StatusSeeOther)
}

func (app *App) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_/live", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /_/ready", app.handleReady)

	mux.HandleFunc("GET /{$}", app.handleRoot)

	mux.HandleFunc("POST /_/setup", app.handleSetup)

	mux.HandleFunc("POST /_/new", app.handleNewPage)

	fsys, _ := fs.Sub(webFS, "web/static")
	staticHandler := http.StripPrefix("/_/static/", http.FileServerFS(fsys))
	mux.Handle("GET /_/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// FileServer renders directory listings. Assets are public, but listing
		// embedded directories needlessly advertises every shipped file and can
		// expose one added by mistake in a later build.
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=3600")
		staticHandler.ServeHTTP(w, r)
	}))

	mux.HandleFunc("GET /_/login", app.handleLoginGet)
	mux.HandleFunc("POST /_/login", app.handleLoginPost)
	mux.HandleFunc("POST /_/logout", app.handleLogout)
	mux.HandleFunc("GET /_/auth/oidc/login", app.handleOIDCLogin)
	mux.HandleFunc("GET /_/auth/oidc/callback", app.handleOIDCCallback)
	mux.HandleFunc("GET /_/auth/oidc/icon", app.handleOIDCIcon)

	mux.HandleFunc("GET /_/tags", app.handleTagsIndex)
	mux.HandleFunc("GET /_/tags/{tag}", app.handleTagPages)

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

	mux.HandleFunc("GET /_/search", app.handleSearch)
	if app.Index.DocumentsEnabled() {
		mux.HandleFunc("GET /_/search/attachments", app.handleAttachmentSearch)
	}
	mux.HandleFunc("GET /_/health-report", app.handleHealthReport)

	mux.HandleFunc("GET /_/hidden", app.handleHiddenIndex)
	mux.HandleFunc("GET /_/hidden/{path...}", app.handleHiddenGet)
	mux.HandleFunc("POST /_/hidden/{path...}", app.handleHiddenPost)

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

	mux.HandleFunc("GET /_/api/search", app.handleSearchAPI)
	if app.Index.DocumentsEnabled() {
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

	// The wildcard handlers serve content after the reserved routes above.
	mux.HandleFunc("GET /{path...}", app.handlePageGet)
	mux.HandleFunc("POST /{path...}", app.handlePagePost)

	return mux
}

func (app *App) handleReady(w http.ResponseWriter, r *http.Request) {
	slog.Debug("readiness check listing repository")
	if _, err := app.Store.List(); err != nil || !app.Index.Ready() {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

type cspNonceKey struct{}

func cspNonce(ctx context.Context) string {
	nonce, _ := ctx.Value(cspNonceKey{}).(string)
	return nonce
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		nonce := base64.RawStdEncoding.EncodeToString(b)
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")

		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "accelerometer=(), camera=(), geolocation=(), microphone=(), payment=(), usb=()")
		h.Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; frame-ancestors 'none'; object-src 'none'; form-action 'self'; img-src 'self' data:; script-src 'self' 'nonce-"+nonce+"'; style-src 'self' 'nonce-"+nonce+"' 'unsafe-inline'; worker-src 'self' blob:")
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), cspNonceKey{}, nonce)))
	})
}

type gzipResponseWriter struct {
	http.ResponseWriter
	writer      *gzip.Writer
	wroteHeader bool
}

func (w *gzipResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	contentType := w.Header().Get("Content-Type")
	if status >= 200 && status != http.StatusNoContent && status != http.StatusNotModified &&
		w.Header().Get("Content-Encoding") == "" &&
		(strings.HasPrefix(contentType, "text/") || strings.Contains(contentType, "json") || strings.Contains(contentType, "javascript")) {
		w.Header().Del("Content-Length")
		w.Header().Set("Content-Encoding", "gzip")
		w.writer = gzip.NewWriter(w.ResponseWriter)
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *gzipResponseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", http.DetectContentType(p))
		}
		w.WriteHeader(http.StatusOK)
	}
	if w.writer != nil {
		return w.writer.Write(p)
	}
	return w.ResponseWriter.Write(p)
}

func acceptsGzip(header string) bool {
	for value := range strings.SplitSeq(header, ",") {
		parts := strings.Split(strings.TrimSpace(value), ";")
		if parts[0] != "gzip" && parts[0] != "*" {
			continue
		}
		quality := 1.0
		for _, param := range parts[1:] {
			if raw, ok := strings.CutPrefix(strings.TrimSpace(param), "q="); ok {
				quality, _ = strconv.ParseFloat(raw, 64)
			}
		}
		return quality > 0
	}
	return false
}

func compression(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Accept-Encoding")
		if r.Method == http.MethodHead || r.Header.Get("Range") != "" || r.URL.Path == "/_/mcp" || !acceptsGzip(r.Header.Get("Accept-Encoding")) {
			next.ServeHTTP(w, r)
			return
		}
		compressed := &gzipResponseWriter{ResponseWriter: w}
		next.ServeHTTP(compressed, r)
		if compressed.writer != nil {
			_ = compressed.writer.Close()
		}
	})
}

func (app *App) isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if !app.trustedProxy(r.RemoteAddr) {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]), "https")
}

func (app *App) secureCookie(r *http.Request) bool {
	if app.isSecureRequest(r) {
		return true
	}
	if !app.apiClient().HasConfig() {
		return false
	}
	base, err := url.Parse(app.externalBaseURL())
	return err == nil && strings.EqualFold(base.Scheme, "https")
}

func (app *App) trustedProxy(remote string) bool {
	if !app.apiClient().HasConfig() {
		return false
	}
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, cidr := range app.config().TrustedProxies {
		_, network, err := net.ParseCIDR(cidr)
		if err == nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

func (app *App) externalBaseURL() string { return app.config().BaseURL }

func (app *App) requestOrigin(r *http.Request) string {
	base := app.externalBaseURL()
	if base != "" {
		return base
	}
	scheme := "http"
	if app.isSecureRequest(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (app *App) requestSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions && !strings.HasPrefix(r.URL.Path, "/_/api/attachments/") && !strings.HasPrefix(r.URL.Path, "/_/api/attachment-uploads/") && !strings.HasPrefix(r.URL.Path, "/_/mcp") {
			if r.ContentLength > maxFormBytes {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
		}
		if app.isSecureRequest(r) {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions || strings.HasPrefix(r.URL.Path, "/_/api/attachment-uploads/") {
			next.ServeHTTP(w, r)
			return
		}
		if _, bearer := tokenPrincipalFromContext(r.Context()); bearer {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/_/login" {
			origin := r.Header.Get("Origin")
			if origin != "" && origin != app.requestOrigin(r) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if origin == "" && r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		cookie, err := r.Cookie("hmd_session")
		if err != nil || cookie.Value == "" {
			next.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		if origin == "" || origin != app.requestOrigin(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		token := r.Header.Get("X-CSRF-Token")
		if token == "" {
			if err := r.ParseForm(); err != nil {
				var maxBytesErr *http.MaxBytesError
				if errors.As(err, &maxBytesErr) {
					http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
					return
				}
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			token = r.Form.Get("csrf_token")
		}
		expected := app.Auth.CSRFToken(cookie.Value)
		if expected == "" || subtle.ConstantTimeCompare([]byte(token), []byte(expected)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func parseRequestForm(w http.ResponseWriter, r *http.Request) bool {
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

func (app *App) loginData(errMsg string) TemplateData {
	cfg := app.config()
	return TemplateData{
		Title:          "Login",
		Error:          errMsg,
		OIDCEnabled:    cfg.OIDC.Issuer != "",
		OIDCButtonText: cfg.OIDC.ButtonText,
		OIDCLocalLogin: cfg.OIDC.Issuer == "" || cfg.OIDC.LocalLogin,
		OIDCIcon:       app.OIDC != nil && app.OIDC.Icon() != nil,
	}
}

func (app *App) handleLoginGet(w http.ResponseWriter, r *http.Request) {
	slog.Debug("rendering login page")
	w.Header().Set("Cache-Control", "no-store")
	app.render(w, r, http.StatusOK, "login", app.loginData(""))
}

func (app *App) handleLoginPost(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	username := r.FormValue("username")

	token, ok := app.Auth.LoginLimited(r.RemoteAddr, username, r.FormValue("password"))
	if !ok {
		slog.Warn("login failed", "username", username)
		app.render(w, r, http.StatusUnauthorized, "login", app.loginData("Invalid username or password"))
		return
	}

	slog.Info("login", "username", username)

	cookie := &http.Cookie{
		Name:     "hmd_session",
		Value:    token,
		HttpOnly: true,
		Secure:   app.secureCookie(r),
		SameSite: http.SameSiteLaxMode,
		Path:     "/",
	}
	if r.FormValue("remember") != "" {
		cookie.MaxAge = 30 * 24 * 60 * 60
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
		Secure:   app.secureCookie(r),
		Path:     "/",
	})

	http.Redirect(w, r, "/_/login", http.StatusSeeOther)
}

const defaultSetupNamespace = "notes"

const newSetupNamespaceOption = "_new"

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
				if !wiki.ValidNamespaceName(name) {
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
			if !wiki.ValidNamespaceName(name) {
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

func (app *App) seedFirstNamespace(name, authorName, authorEmail string) error {
	nsCfg := wiki.NamespaceConfig{Widgets: wiki.BuiltinNamespaceWidgets, Index: defaultIndexPage}
	data, err := nsCfg.Encode()
	if err != nil {
		return fmt.Errorf("encoding namespace config: %w", err)
	}
	path := wiki.NamespaceConfigPath(name)
	if _, err := app.Store.Save(path, data, "Configure namespace "+path, authorName, authorEmail); err != nil {
		return fmt.Errorf("saving namespace config: %w", err)
	}

	indexSlug := wiki.NamespaceSlug(name, defaultIndexPage)
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

func (app *App) handleRerunSetup(w http.ResponseWriter, r *http.Request) {
	app.Store.ForceSetup.Store(true)
	http.Redirect(w, r, "/_/admin", http.StatusSeeOther)
}

func (app *App) handleSetAuthor(w http.ResponseWriter, r *http.Request) {
	author := strings.TrimSpace(r.FormValue("git_author"))
	if err := validateGitAuthor(author); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := app.Auth.SetAuthor(app.currentUser(r), author); err != nil {
		http.Error(w, "failed to save git author", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/_/settings", http.StatusSeeOther)
}

func (app *App) handleResetHelp(w http.ResponseWriter, r *http.Request) {
	authorName, authorEmail := app.gitAuthor(app.currentUser(r))
	content := Page{Slug: "help", Title: "Help", Tags: []string{"meta"}, Body: defaultHelpMD}.Encode()
	if _, err := app.Store.Save(".help.md", content, "Reset .help.md to built-in", authorName, authorEmail); err != nil {
		http.Error(w, "failed to reset .help.md", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/_/admin", http.StatusSeeOther)
}

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
	tmplData := api.NewPageTemplateData{Now: time.Now(), User: username, Namespace: ns}

	slugRel, err := api.RenderNewPageText(nsCfg.New.Slug, tmplData)
	if err != nil {
		http.Error(w, "invalid slug template", http.StatusInternalServerError)
		return
	}
	// The rendered slug is a trust boundary, like MCP input: validated
	// after rendering so a template can never write outside its own
	// namespace (no separators, no dot prefix, non-empty).
	if !wiki.ValidPageSegment(slugRel) {
		http.Error(w, "invalid generated slug", http.StatusBadRequest)
		return
	}
	if !wiki.ValidPageSegment(nsCfg.New.Template) {
		http.Error(w, "invalid template page", http.StatusBadRequest)
		return
	}
	slug := wiki.NamespaceSlug(ns, slugRel)
	if !app.requireTokenSlug(w, r, slug) {
		return
	}

	if _, _, err := app.Store.Read(pageFile(slug)); err == nil {
		http.Redirect(w, r, "/"+slug+"?do=edit", http.StatusSeeOther)
		return
	}

	templateSlug := wiki.NamespaceSlug(ns, nsCfg.New.Template)
	tplPage := Page{Slug: templateSlug, Title: slugRel}
	if tplContent, _, err := app.Store.Read(hiddenFile(templateSlug)); err == nil {
		tplPage = ParsePage(templateSlug, tplContent)
	} else {
		slog.Warn("namespace template page missing, creating a bare page", "namespace", ns, "template", templateSlug)
	}

	title, err := api.RenderNewPageText(tplPage.Title, tmplData)
	if err != nil {
		http.Error(w, "invalid title template", http.StatusInternalServerError)
		return
	}
	body, err := api.RenderNewPageText(tplPage.Body, tmplData)
	if err != nil {
		http.Error(w, "invalid body template", http.StatusInternalServerError)
		return
	}
	tags := make([]string, 0, len(tplPage.Tags))
	for _, tag := range tplPage.Tags {
		rendered, err := api.RenderNewPageText(tag, tmplData)
		if err != nil {
			http.Error(w, "invalid tag template", http.StatusInternalServerError)
			return
		}
		tags = append(tags, rendered)
	}

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

func (app *App) handleViewPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	authed := app.currentUser(r) != ""

	view, err := app.apiClient().ViewPage(r.Context(), slug)
	if err != nil {
		switch api.CategoryOf(err) {
		case api.CategoryNotFound:
			if !authed {
				app.notFound(w, r)
				return
			}
			app.render(w, r, http.StatusNotFound, "create", TemplateData{
				Authed: true,
				Title:  "Page not found",
				Slug:   slug,
			})
		case api.CategoryForbidden:
			app.errorPage(w, r, http.StatusForbidden, "Forbidden", "You don't have access to that namespace.")
		default:
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}

	page := Page{Slug: view.Slug, Title: view.Title, Tags: view.Tags, Body: view.Body, Pin: view.Pin}

	if !authed {
		app.handlePublicPage(w, r, slug, page)
		return
	}

	ns, _ := wiki.NamespaceFor(slug)
	page.Body = injectTOC(page.Body, app.Index, app.Namespaces().IndexSlug(ns), ns)
	renderedBody, err := app.Render.Render(page.Body, ns)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	backlinkSummaries, err := app.apiClient().Backlinks(r.Context(), slug)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	backlinks := make([]BacklinkEntry, 0, len(backlinkSummaries))
	for _, backlink := range backlinkSummaries {
		backlinks = append(backlinks, BacklinkEntry{Slug: backlink.Slug, Title: backlink.Title})
	}

	var pageTags []TagChip
	for _, tag := range page.Tags {
		pageTags = append(pageTags, TagChip{Tag: tag, Slug: Slugify(tag)})
	}

	revisionCount := 0
	headAuthor, headWhen := "", ""
	var recentCommits []LogEntry
	if history, err := app.apiClient().PageHistory(r.Context(), slug); err == nil {
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
		BlobHash:      view.Hash,
		StatusContext: fmt.Sprintf("%d revision%s", revisionCount, plural(revisionCount)),
		RecentCommits: recentCommits,
	})
}

func (app *App) handlePublicPage(w http.ResponseWriter, r *http.Request, slug string, page Page) {
	ns := app.Namespaces()
	if !ns.IsPublic(slug) {
		app.notFound(w, r)
		return
	}

	pageNS, _ := wiki.NamespaceFor(slug)
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
	if summary := app.apiClient().NamespaceSummary(r.Context(), pageNS); summary != nil {
		sidebarTreeNS = pageNS
		sidebarTreeEntries = summary.Pages
	}
	publishedTitle := wiki.NamespaceDisplayTitle(pageNS, cfg)

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

func reservedPath(path string) bool {
	return path == "_" || strings.HasPrefix(path, "_/")
}

func isPageSlug(slug string) bool {
	return wiki.ValidPageSlug(slug)
}

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

func (app *App) namespaceIndexName(path string) (string, bool) {
	name := strings.TrimSuffix(path, "/")
	if name == "" || strings.Contains(name, "/") {
		return "", false
	}
	for _, entry := range app.apiClient().NamespaceSummaries() {
		if entry.Name == name {
			return entry.Name, true
		}
	}
	return "", false
}

func (app *App) handleNamespaceIndex(w http.ResponseWriter, r *http.Request, name string) {
	if !app.requireTokenNamespace(w, r, name) {
		return
	}
	authed := app.currentUser(r) != ""
	summary := app.apiClient().NamespaceSummary(r.Context(), name)
	if summary == nil || (!authed && !summary.Config.Public) {
		app.notFound(w, r)
		return
	}

	// A configured index page takes over the namespace root: hand off to the
	// normal page-view handler for it rather than duplicating its rendering
	// (auth, TOC, backlinks, ...) here. Falls back to the page list below if
	// the configured page doesn't exist.
	if summary.Config.Index != "" {
		indexSlug := wiki.NamespaceSlug(name, summary.Config.Index)
		if _, ok := app.Index.Titles()[indexSlug]; ok {
			r.SetPathValue("slug", indexSlug)
			app.handleViewPage(w, r)
			return
		}
	}

	publishedTitle := wiki.NamespaceDisplayTitle(name, summary.Config)
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
		PageTree:           renderLiveTree(buildPageTree(tagPages, name, summary.Config.Index, summary.Config.Tree), name, ""),
		SidebarTreeNS:      name,
		SidebarTreeEntries: summary.Pages,
	})
}

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

	switch view, err := app.apiClient().ViewPage(r.Context(), slug); {
	case err == nil:
		page = Page{Slug: view.Slug, Title: view.Title, Tags: view.Tags, Body: view.Body}
		baseHash = view.Hash
	case errors.Is(err, os.ErrNotExist):
		// Missing page: start a blank edit form.
	default:
		http.Error(w, "not found", http.StatusNotFound)
		return
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

func (app *App) handleSave(w http.ResponseWriter, r *http.Request, oldFile string) {
	if !parseRequestForm(w, r) {
		return
	}
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
		newNamespace, newPage := wiki.NamespaceFor(targetSlug)
		if !wiki.ValidPagePath(newPage) {
			http.Error(w, "invalid filename", http.StatusBadRequest)
			return
		}
		// Crossing into another namespace files the page somewhere that
		// already exists; a typo in the path shouldn't conjure a namespace
		// directory. Staying within the namespace is a rename.
		if oldNamespace, _ := wiki.NamespaceFor(slug); oldNamespace != newNamespace {
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

	tags := ParseTags(tagsInput)
	if err := validatePageInput(title, tags, body); err != nil {
		status := http.StatusBadRequest
		if len(body) > maxPageBodyBytes {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, err.Error(), status)
		return
	}
	page := Page{Slug: targetSlug, Title: title, Tags: tags, Body: body}
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
		app.Index.Remove(slug)
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
	var body string
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		if !parseRequestForm(w, r) {
			return
		}
		body = r.FormValue("body")
	} else {
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPageBodyBytes))
		if err != nil {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		body = string(b)
	}
	if len(body) > maxPageBodyBytes {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}

	ns, _ := wiki.NamespaceFor(r.URL.Query().Get("slug"))
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

func (app *App) handleCapabilityUpload(w http.ResponseWriter, r *http.Request) {
	capability, ok := app.apiClient().TakeUploadCapability(r.PathValue("token"))
	if !ok {
		http.NotFound(w, r)
		return
	}
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

	maxBytes := app.config().MaxUploadBytes
	if maxBytes <= 0 {
		maxBytes = 10 * 1024 * 1024
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+(1<<20))
	reader, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "no file uploaded", http.StatusBadRequest)
		return
	}
	var file *os.File
	filenameInput := ""
	for {
		part, partErr := reader.NextPart()
		if errors.Is(partErr, io.EOF) {
			break
		}
		if partErr != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(partErr, &maxBytesErr) {
				http.Error(w, "attachment exceeds maximum upload size", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "invalid upload", http.StatusBadRequest)
			return
		}
		if part.FormName() != "file" || part.FileName() == "" || file != nil {
			if err := part.Close(); err != nil {
				http.Error(w, "error reading upload", http.StatusInternalServerError)
				return
			}
			continue
		}
		file, err = os.CreateTemp("", "hmd-upload-*")
		if err != nil {
			http.Error(w, "error preparing upload", http.StatusInternalServerError)
			return
		}
		filenameInput = part.FileName()
		copied, copyErr := io.Copy(file, io.LimitReader(part, maxBytes+1))
		closeErr := part.Close()
		if copyErr != nil {
			_ = file.Close()
			_ = os.Remove(file.Name())
			http.Error(w, "error reading file", http.StatusInternalServerError)
			return
		}
		if closeErr != nil {
			_ = file.Close()
			_ = os.Remove(file.Name())
			http.Error(w, "error reading file", http.StatusInternalServerError)
			return
		}
		if copied > maxBytes {
			_ = file.Close()
			_ = os.Remove(file.Name())
			http.Error(w, "attachment exceeds maximum upload size", http.StatusRequestEntityTooLarge)
			return
		}
	}
	if file == nil {
		http.Error(w, "no file uploaded", http.StatusBadRequest)
		return
	}
	defer func() {
		if err := file.Close(); err != nil {
			slog.Warn("closing uploaded file", "err", err)
		}
		if err := os.Remove(file.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("removing upload temporary file", "err", err)
		}
	}()

	filename := filepath.Base(filenameInput)
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

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		http.Error(w, "error reading file", http.StatusInternalServerError)
		return
	}
	content, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "error reading file", http.StatusInternalServerError)
		return
	}

	files := map[string][]byte{path: content}
	if extracted, ok := app.Index.ExtractAttachment(r.Context(), file, filename, content); ok {
		files[extractedAttachmentPath(path)] = extracted
	}

	authorName, authorEmail := app.gitAuthor(username)
	_, err = app.Store.SaveAll(files, "Add attachment "+filename, authorName, authorEmail)
	if err != nil {
		http.Error(w, "error saving file", http.StatusInternalServerError)
		return
	}

	indexed := false
	var indexErr error
	var attachmentHash string
	if app.Index.DocumentsEnabled() {
		file, hash, hashErr := app.Store.OpenAttachment(path)
		if hashErr != nil {
			indexErr = hashErr
		} else {
			attachmentHash = hash
			if err := file.Close(); err != nil {
				slog.Warn("closing indexed attachment", "path", path, "err", err)
			}
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
	} else if app.Index.DocumentsEnabled() {
		w.WriteHeader(http.StatusCreated)
	}
	resp := map[string]any{
		"url":     fmt.Sprintf("/_/attachments/%s/%s", slug, filename),
		"indexed": indexed,
	}
	if indexErr != nil {
		resp["index_error"] = "attachment indexing pending"
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Error("encoding attachment response", "err", err)
	}
}

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

	attachment, _, err := app.Store.OpenAttachment("attachments/" + slug + "/" + file)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() {
		if err := attachment.Close(); err != nil {
			slog.Warn("closing served attachment", "path", path, "err", err)
		}
	}()

	w.Header().Set("X-Content-Type-Options", "nosniff")
	switch strings.ToLower(filepath.Ext(file)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
	default:
		w.Header().Set("Content-Disposition", "attachment")
	}
	http.ServeContent(w, r, file, time.Time{}, attachment)
}

func (app *App) handleTagsIndex(w http.ResponseWriter, r *http.Request) {
	app.render(w, r, http.StatusOK, "tags", TemplateData{
		Authed:  true,
		Title:   "Tags",
		AllTags: app.apiClient().Tags(r.Context()),
	})
}

func (app *App) handleTagPages(w http.ResponseWriter, r *http.Request) {
	tagSlug := r.PathValue("tag")
	name, summaries := app.apiClient().TagPages(r.Context(), tagSlug)

	pages := make([]BacklinkEntry, 0, len(summaries))
	for _, summary := range summaries {
		pages = append(pages, BacklinkEntry{Slug: summary.Slug, Title: summary.Title})
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
	hits, err := app.apiClient().SearchPages(r.Context(), q)
	if err != nil {
		if api.CategoryOf(err) == api.CategoryInvalidInput {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
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
	attachmentResults := make([]AttachmentResult, 0)
	if app.Index.DocumentsEnabled() && strings.TrimSpace(q) != "" {
		attachmentHits, searchErr := app.apiClient().SearchAttachments(r.Context(), q, 20)
		if searchErr != nil {
			if api.CategoryOf(searchErr) == api.CategoryBusy {
				w.Header().Set("Retry-After", "1")
				http.Error(w, "search is busy", http.StatusTooManyRequests)
				return
			}
			slog.Warn("attachment search failed", "err", searchErr)
		} else {
			for _, hit := range attachmentHits {
				attachmentResults = append(attachmentResults, AttachmentResult{
					OwnerSlug: hit.OwnerSlug, Filename: hit.Filename, URL: hit.URL,
					Excerpt: template.HTML(hit.Excerpt), Score: hit.Score,
				})
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
		AttachmentSearchEnabled: app.Index.DocumentsEnabled(),
		SearchHits:              len(results),
		AttachmentHits:          len(attachmentResults),
		SearchElapsed:           elapsed.String(),
		StatusMode:              "search",
	})
}

func (app *App) handleAttachmentSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.FormValue("q"))
	hits, err := app.apiClient().SearchAttachments(r.Context(), q, 20)
	if err != nil {
		switch api.CategoryOf(err) {
		case api.CategoryInvalidInput:
			http.Error(w, err.Error(), http.StatusBadRequest)
		case api.CategoryBusy:
			w.Header().Set("Retry-After", "1")
			http.Error(w, "search is busy", http.StatusTooManyRequests)
		default:
			http.Error(w, "attachment search unavailable", http.StatusServiceUnavailable)
		}
		return
	}
	results := make([]AttachmentResult, 0, len(hits))
	for _, hit := range hits {
		results = append(results, AttachmentResult{
			OwnerSlug: hit.OwnerSlug, Filename: hit.Filename, URL: hit.URL,
			Excerpt: template.HTML(hit.Excerpt), Score: hit.Score,
		})
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
	hits, err := app.apiClient().SearchPages(r.Context(), q)
	if err != nil {
		if api.CategoryOf(err) == api.CategoryInvalidInput {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	results := make([]AutocompleteResult, 0, len(hits))
	for _, hit := range hits {
		results = append(results, AutocompleteResult{Slug: hit.Slug, Title: hit.Title, Snippet: hit.Snippet, Tags: hit.Tags})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(results); err != nil {
		slog.Error("encoding search response", "err", err)
	}
}

func (app *App) handleAttachmentSearchAPI(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.FormValue("q"))
	hits, err := app.apiClient().SearchAttachments(r.Context(), q, 20)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		switch api.CategoryOf(err) {
		case api.CategoryInvalidInput:
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		case api.CategoryBusy:
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "search is busy"})
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "attachment search unavailable"})
		}
		return
	}
	results := make([]AttachmentResult, 0, len(hits))
	for _, hit := range hits {
		results = append(results, AttachmentResult{
			OwnerSlug: hit.OwnerSlug, Filename: hit.Filename, URL: hit.URL,
			Excerpt: template.HTML(hit.Excerpt), Score: hit.Score,
		})
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

	view, err := app.apiClient().ViewPage(r.Context(), slug)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	snippet := extractSnippet(view.Body, 40)

	resp := map[string]any{
		"title":   view.Title,
		"snippet": snippet,
		"tags":    view.Tags,
		"age":     "just now",
	}

	if history, err := app.apiClient().PageHistory(r.Context(), slug); err == nil && len(history) > 0 {
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

	resp := map[string]any{
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
	renameNS, _ := wiki.NamespaceFor(slug)
	if Slugify(newTitle) == "" {
		http.Error(w, "invalid title", http.StatusBadRequest)
		return
	}
	newSlug := wiki.NamespaceSlug(renameNS, Slugify(newTitle))
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

	for _, src := range sources {
		srcContent, srcHash, err := app.Store.Read(pageFile(src))
		if err != nil {
			continue
		}
		srcPage := ParsePage(src, srcContent)
		srcNS, _ := wiki.NamespaceFor(src)
		updated := wikiLinkRe.ReplaceAllStringFunc(srcPage.Body, func(m string) string {
			// Mirror Index.ResolveLink: the link pointed at the old page by
			// title, or — for casing that didn't match — by slug, namespace
			// first. Title alone isn't enough now that a slug can be namespaced.
			inner := m[2 : len(m)-2]
			if inner == oldTitle || wiki.NamespaceSlug(srcNS, Slugify(inner)) == slug || Slugify(inner) == slug {
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
	report, err := app.apiClient().Health(r.Context(), "")
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var b strings.Builder
	if len(report.Missing) > 0 {
		fmt.Fprintf(&b, `<section><h2>Missing pages (%d)</h2><p>Wiki-linked but not yet created:</p><ul>`, len(report.Missing))
		for _, entry := range report.Missing {
			fmt.Fprintf(&b, `<li><a href="/%s?do=edit" class="missing">%s</a> — linked from `, entry.Slug, htmlEscape(entry.Slug))
			for i, src := range entry.Sources {
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

	if len(report.Orphans) > 0 {
		fmt.Fprintf(&b, `<section><h2>Orphaned pages (%d)</h2><p>Pages with no incoming links:</p><ul>`, len(report.Orphans))
		for _, o := range report.Orphans {
			title := titles[o]
			if title == "" {
				title = o
			}
			fmt.Fprintf(&b, `<li><a href="/%s">%s</a></li>`, o, htmlEscape(title))
		}
		b.WriteString(`</ul></section>`)
	}

	if len(report.Missing) == 0 && len(report.Orphans) == 0 {
		b.WriteString(`<p>✓ Your wiki is healthy!</p>`)
	}

	app.render(w, r, http.StatusOK, "page", TemplateData{
		Authed:        true,
		Title:         "wiki health",
		Slug:          "health-report",
		Content:       template.HTML(b.String()),
		StatusContext: fmt.Sprintf("%d missing · %d orphan%s", len(report.Missing), len(report.Orphans), plural(len(report.Orphans))),
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

func (app *App) handleHealthAPI(w http.ResponseWriter, r *http.Request) {
	namespace := r.URL.Query().Get("namespace")
	report, err := app.apiClient().Health(r.Context(), namespace)
	if err != nil {
		switch api.CategoryOf(err) {
		case api.CategoryInvalidInput:
			http.Error(w, `{"error":"invalid namespace"}`, http.StatusBadRequest)
		case api.CategoryForbidden:
			app.tokenNamespaceDenied(w, r)
		default:
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}

	envelope := HealthReport{Missing: make([]HealthMissingEntry, 0, len(report.Missing)), Orphans: report.Orphans}
	for _, entry := range report.Missing {
		envelope.Missing = append(envelope.Missing, HealthMissingEntry{Slug: entry.Slug, Sources: entry.Sources})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(envelope); err != nil {
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

	diff, err := app.apiClient().PageDiff(r.Context(), slug, hashA, hashB)
	if err != nil {
		if api.CategoryOf(err) == api.CategoryForbidden {
			app.tokenNamespaceDenied(w, r)
			return
		}
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

	view, err := app.apiClient().ViewPage(r.Context(), slug)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	history, err := app.apiClient().PageHistory(r.Context(), slug)
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
		Title:          view.Title,
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

	revision, err := app.apiClient().RevisionPage(r.Context(), slug, hash)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	page := Page{Slug: revision.Slug, Title: revision.Title, Tags: revision.Tags, Body: revision.Body}
	ns, _ := wiki.NamespaceFor(slug)
	page.Body = injectTOC(page.Body, app.Index, app.Namespaces().IndexSlug(ns), ns)
	renderedBody, err := app.Render.Render(page.Body, ns)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	commitTime := ""
	if !revision.When.IsZero() {
		commitTime = revision.When.Format("2006-01-02 15:04")
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

	content, err := app.Store.FileAt(pageFile(slug), hash)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	authorName, authorEmail := app.gitAuthor(username)
	_, err = app.Store.Save(pageFile(slug), content, "Revert "+slug+" to "+hash[:8], authorName, authorEmail)
	if err != nil {
		http.Error(w, "error reverting", http.StatusInternalServerError)
		return
	}
	slog.Info("reverted", "slug", slug, "to", hash[:8], "author", authorName)

	page := ParsePage(slug, content)
	if err := app.Index.Update(page); err != nil {
		slog.Error("updating search index", "slug", page.Slug, "err", err)
	}

	http.Redirect(w, r, "/"+slug, http.StatusSeeOther)
}

func (app *App) handleHiddenIndex(w http.ResponseWriter, r *http.Request) {
	summaries, err := app.apiClient().HiddenPages(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pages := make([]BacklinkEntry, 0, len(summaries))
	for _, summary := range summaries {
		pages = append(pages, BacklinkEntry{Slug: summary.Slug, Title: summary.Title})
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

	view, err := app.apiClient().ViewHidden(r.Context(), slug)
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

	hiddenNS, _ := wiki.NamespaceFor(slug)
	renderedBody, err := app.Render.Render(view.Body, hiddenNS)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	app.render(w, r, http.StatusOK, "page", TemplateData{
		Authed:      true,
		Title:       view.Title,
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

	page := Page{Slug: slug, Title: slug}
	baseHash := ""

	switch view, err := app.apiClient().ViewHidden(r.Context(), slug); {
	case err == nil:
		page = Page{Slug: view.Slug, Title: view.Title, Tags: view.Tags, Body: view.Body}
		baseHash = view.Hash
	case errors.Is(err, os.ErrNotExist):
		// Missing hidden page: start a blank edit form.
	default:
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
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

func (app *App) settingsData(r *http.Request) SettingsData {
	cfg := app.config()
	user := app.currentUser(r)
	prefs := app.Auth.Prefs(user)

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
	for _, summary := range app.apiClient().NamespaceSummaries() {
		sd.TokenNamespaces = append(sd.TokenNamespaces, summary.Name)
	}
	for _, t := range app.Auth.TokensFor(user) {
		expires := "never"
		switch {
		case t.Expired():
			expires = "expired"
		case !t.Expires.IsZero():
			expires = t.Expires.Format("2006-01-02")
		}
		effectiveScopes := effectiveTokenScopes(prefs.Scopes, t.Scopes)
		isAdmin := userRecord{Scopes: effectiveScopes}.HasScope(scopeSettings)
		namespaceLabel := "All namespaces"
		if isAdmin {
			namespaceLabel = "Administrator"
		} else if len(t.Namespaces) > 0 {
			namespaceLabel = strings.Join(t.Namespaces, ", ") + " only"
		}
		scopeLabel := strings.Join(t.Scopes, ", ")
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
	for _, summary := range app.apiClient().NamespaceSummaries() {
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
	prefs := app.Auth.Prefs(user)
	data := NamespaceManagementData{
		CanWrite:     prefs.HasScope(scopeWrite),
		CanSettings:  prefs.HasScope(scopeSettings),
		Rows:         app.apiClient().ListNamespaces(r.Context()),
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
			data.TreeEditor = namespaceTreeEditor(app.Index.Titles(), entry.Name, entry.Index, entry.Tree)
			break
		}
	}
	return data
}

func namespaceTreeItems(titles map[string]string, namespace, index string, tree []string) []NamespaceTreeItem {
	entries := make([]BacklinkEntry, 0)
	for slug, title := range titles {
		if ns, rest := wiki.NamespaceFor(slug); ns == namespace && rest != "" {
			entries = append(entries, BacklinkEntry{Slug: slug, Title: title})
		}
	}
	root := buildPageTree(entries, namespace, index, tree)
	items := make([]NamespaceTreeItem, 0, len(entries))
	var walk func([]*navNode)
	walk = func(nodes []*navNode) {
		for _, node := range nodes {
			item := NamespaceTreeItem{Path: node.Path, Title: node.Title, Folder: !node.IsPage, IsIndex: node.Path == index}
			if item.Title == "" {
				item.Title = node.Name
			}
			items = append(items, item)
			walk(node.Children)
		}
	}
	walk(root.Children)
	return items
}

func namespaceTreeEditor(titles map[string]string, namespace, index string, tree []string) template.HTML {
	entries := make([]BacklinkEntry, 0)
	for slug, title := range titles {
		if ns, rest := wiki.NamespaceFor(slug); ns == namespace && rest != "" {
			entries = append(entries, BacklinkEntry{Slug: slug, Title: title})
		}
	}
	root := buildPageTree(entries, namespace, index, tree)
	var b strings.Builder
	var writeNodes func([]*navNode, string)
	writeNodes = func(nodes []*navNode, parent string) {
		b.WriteString(`<ul class="tree-order-list" data-parent="`)
		b.WriteString(html.EscapeString(parent))
		b.WriteString(`">`)
		for _, node := range nodes {
			path := html.EscapeString(node.Path)
			title := node.Title
			if title == "" {
				title = node.Name
			}
			classes := "tree-order-item"
			if node.Path == index {
				classes += " fixed"
			}
			row := func() {
				if len(node.Children) > 0 {
					b.WriteString(`<span class="tree-order-toggle" aria-hidden="true">▸</span>`)
				} else {
					b.WriteString(`<span class="tree-order-toggle-placeholder" aria-hidden="true"></span>`)
				}
				b.WriteString(`<span class="tree-order-handle" aria-hidden="true">⠿</span><span class="tree-order-title">`)
				b.WriteString(html.EscapeString(title))
				b.WriteString(`</span><code>`)
				b.WriteString(path)
				b.WriteString(`</code>`)
				if node.Path == index {
					b.WriteString(`<span class="tree-order-index">index</span>`)
				}
			}
			if len(node.Children) > 0 {
				b.WriteString(`<li class="tree-order-node"><details><summary class="`)
				b.WriteString(classes)
				b.WriteString(`" data-path="`)
				b.WriteString(path)
				b.WriteString(`"`)
				if node.Path != index {
					b.WriteString(` draggable="true"`)
				}
				b.WriteString(`>`)
				row()
				b.WriteString(`</summary>`)
				writeNodes(node.Children, node.Path)
				b.WriteString(`</details></li>`)
				continue
			}
			b.WriteString(`<li class="tree-order-node `)
			b.WriteString(classes)
			b.WriteString(`" data-path="`)
			b.WriteString(path)
			b.WriteString(`"`)
			if node.Path != index {
				b.WriteString(` draggable="true"`)
			}
			b.WriteString(`>`)
			row()
			b.WriteString(`</li>`)
		}
		b.WriteString(`</ul>`)
	}
	writeNodes(root.Children, "")
	return template.HTML(b.String())
}

func (app *App) renderNamespace(w http.ResponseWriter, r *http.Request, status int, templateName, name, errMsg string) {
	app.render(w, r, status, templateName, TemplateData{
		Authed:              true,
		Title:               "Namespaces",
		StatusMode:          "settings",
		NamespaceManagement: new(app.namespaceManagementData(r, name, errMsg)),
	})
}

func (app *App) handleNamespacesGet(w http.ResponseWriter, r *http.Request) {
	data := app.namespaceManagementData(r, "", "")
	if r.URL.Query().Get("saved") == "1" {
		data.Flash = "Namespace settings saved"
	}
	app.render(w, r, http.StatusOK, "namespaces", TemplateData{Authed: true, Title: "Namespaces", StatusMode: "settings", NamespaceManagement: &data})
}

func (app *App) handleNamespaceNewGet(w http.ResponseWriter, r *http.Request) {
	data := app.namespaceManagementData(r, "", "")
	data.Form = NamespaceListEntry{Template: wiki.DefaultNewPageTemplate, SlugPreset: slugPresets[0].Key}
	app.render(w, r, http.StatusOK, "namespace-edit", TemplateData{Authed: true, Title: "New namespace", StatusMode: "settings", NamespaceManagement: &data})
}

func (app *App) handleNamespaceEditGet(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !app.requireTokenNamespace(w, r, name) {
		return
	}
	if !wiki.ValidNamespaceName(name) {
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
	if !wiki.ValidNamespaceName(name) {
		fail(fmt.Sprintf("%q is not a valid namespace name: one path segment, not %q, not dot-prefixed", name, wiki.ReservedNamespace))
		return
	}

	var ids []string
	for id := range strings.SplitSeq(r.FormValue("widgets"), ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if !presentation.ValidWidget(id) {
			fail(fmt.Sprintf("unknown widget %q", id))
			return
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		ids = wiki.BuiltinNamespaceWidgets
	}

	// The template page name is a convention, not a question the form asks: it
	// arrives as a hidden field so a hand-written config naming something
	// other than wiki.DefaultNewPageTemplate survives a save here.
	template := strings.TrimSpace(r.FormValue("template"))
	if template == "" {
		template = wiki.DefaultNewPageTemplate
	}
	// It names a hidden page inside the namespace — the same one-segment shape
	// handleNewPage's rendered slug has to satisfy.
	if !wiki.ValidPageSegment(template) {
		fail(fmt.Sprintf("%q is not a valid template page name: one segment, no slashes, no leading dot", template))
		return
	}
	// Whether this save is what brings the namespace into being, which decides
	// if it gets a template page seeded below.
	creating := !app.Namespaces()[name].Configured

	title := strings.TrimSpace(r.FormValue("title"))
	if !validRunes(title, wiki.MaxNamespaceTitleRunes) {
		fail(fmt.Sprintf("namespace title must be at most %d characters", wiki.MaxNamespaceTitleRunes))
		return
	}
	var tree []string
	for path := range strings.SplitSeq(r.FormValue("tree"), ",") {
		if path = strings.TrimSpace(path); path != "" {
			tree = append(tree, path)
		}
	}
	cfg := wiki.NamespaceConfig{Widgets: ids, Public: r.FormValue("public") == "on", Title: title, Description: r.FormValue("description"), Skin: strings.TrimSpace(r.FormValue("skin")), Palette: strings.TrimSpace(r.FormValue("palette")), Index: strings.TrimSpace(r.FormValue("index")), Tree: tree}
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
		rendered, err := api.RenderNewPageText(slug, api.NewPageTemplateData{Now: time.Now(), User: app.currentUser(r), Namespace: name})
		if err != nil {
			fail("slug pattern is not a valid template: " + err.Error())
			return
		}
		if !wiki.ValidPageSegment(rendered) {
			fail(fmt.Sprintf("that pattern names a page %q, which isn't usable: no slashes, no leading dot, not empty", rendered))
			return
		}
		cfg.New = &wiki.NewPageConfig{Template: template, Slug: slug}
	}
	var err error
	cfg, err = api.NormaliseNamespaceConfig(name, cfg, api.NewPageTemplateData{Now: time.Now(), User: app.currentUser(r), Namespace: name})
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
	path := wiki.NamespaceConfigPath(name)
	if _, err := app.Store.Save(path, data, "Configure namespace "+path, authorName, authorEmail); err != nil {
		slog.Error("saving namespace config", "namespace", name, "err", err)
		fail("failed to save namespace config: " + err.Error())
		return
	}

	// Seed the template when creating or enabling new pages, without overwriting it.
	if creating || cfg.New != nil {
		if err := app.ensureNewPageTemplate(name, template, authorName, authorEmail); err != nil {
			slog.Warn("seeding namespace template page", "namespace", name, "err", err)
		}
	}

	app.refreshNamespaces()
	slog.Info("namespace configured", "namespace", name, "by", app.currentUser(r))
	http.Redirect(w, r, "/_/namespaces?saved=1", http.StatusSeeOther)
}

func (app *App) handleResetNamespace(w http.ResponseWriter, r *http.Request) {
	name := strings.Trim(strings.TrimSpace(r.FormValue("name")), "/")
	if !app.requireTokenNamespace(w, r, name) {
		return
	}
	if !wiki.ValidNamespaceName(name) {
		http.Error(w, "invalid namespace name", http.StatusBadRequest)
		return
	}
	authorName, authorEmail := app.gitAuthor(app.currentUser(r))
	path := wiki.NamespaceConfigPath(name)
	if err := app.Store.Remove(path, "Reset namespace settings "+path, authorName, authorEmail); err != nil {
		app.renderNamespace(w, r, http.StatusOK, "namespace-edit", name, "failed to reset namespace settings: "+err.Error())
		return
	}
	app.refreshNamespaces()
	http.Redirect(w, r, "/_/namespaces?saved=1", http.StatusSeeOther)
}

func (app *App) handleDeleteNamespace(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if !app.requireTokenNamespace(w, r, name) {
		return
	}
	authorName, authorEmail := app.gitAuthor(app.currentUser(r))
	path := wiki.NamespaceConfigPath(name)
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
		ns, _ := wiki.NamespaceFor(slug)
		if ns == name {
			app.Index.Remove(slug)
		}
	}
	app.refreshNamespaces()
	slog.Info("namespace deleted with all files", "namespace", name, "by", app.currentUser(r))
	http.Redirect(w, r, "/_/namespaces?saved=1", http.StatusSeeOther)
}

func (app *App) refreshNamespaces() {
	if err := app.apiClient().RefreshNamespaces(); err != nil {
		slog.Warn("rebuilding namespace registry", "err", err)
	}
}

func (app *App) ensureNewPageTemplate(ns, template, authorName, authorEmail string) error {
	slug := wiki.NamespaceSlug(ns, template)
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

var newPageTemplateFields = []string{
	`.Now.Format "2006-01-02"`,
	`.Now.Format "Monday, 2 January 2006"`,
	`.Now.Format "15:04"`,
	`.User`,
	`.Namespace`,
}

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
	if err := app.Auth.AddUserWithScopes(name, password, scopes); err != nil {
		fail(err.Error())
		return
	}
	slog.Info("user created", "user", name, "by", app.currentUser(r))
	http.Redirect(w, r, "/_/admin?saved=1", http.StatusSeeOther)
}

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
	list := func(name string) []string {
		return strings.FieldsFunc(r.FormValue(name), func(r rune) bool { return r == ',' || r == '\n' })
	}

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
	if err := validateGitAuthor(gitAuthor); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
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

	// Environment variables are authoritative. Do not let a crafted form change
	// their on-disk fallback values (and, especially, do not turn an omitted
	// disabled checkbox into false).
	set := func(path string, apply func()) {
		if app.config().EnvOverrides[path] == "" {
			apply()
		}
	}
	set("Bind", func() { fc.Bind = bind })
	set("RepoDir", func() { fc.RepoDir = repoDir })
	set("Git.RemoteURL", func() { fc.Git.RemoteURL = remoteURL })
	set("Git.User", func() { fc.Git.User = gitUser })
	set("Git.Author", func() { fc.Git.Author = gitAuthor })
	set("MaxUploadBytes", func() { fc.MaxUploadBytes = new(maxUploadBytes) })
	set("SyncPollMs", func() { fc.SyncPollMs = new(syncPollMs) })
	set("SyncMode", func() { fc.SyncMode = syncMode })
	set("DefaultBranch", func() { fc.DefaultBranch = r.FormValue("default_branch") })
	set("Skin", func() { fc.Skin = r.FormValue("skin") })
	set("Debug", func() { fc.Debug = r.FormValue("debug") == "on" })
	set("BaseURL", func() { fc.BaseURL = r.FormValue("base_url") })
	set("TrustedProxies", func() { fc.TrustedProxies = list("trusted_proxies") })
	set("Git.TokenFile", func() { fc.Git.TokenFile = r.FormValue("git_token_file") })
	set("MCP.Enabled", func() { fc.MCP.Enabled = r.FormValue("mcp_enabled") == "on" })
	set("DocumentSearch.Model", func() { fc.DocumentSearch.Model = r.FormValue("document_model") })
	set("DocumentSearch.ModelDir", func() { fc.DocumentSearch.ModelDir = r.FormValue("document_model_dir") })
	set("DocumentSearch.IndexDir", func() { fc.DocumentSearch.IndexDir = r.FormValue("document_index_dir") })
	set("OIDC.Issuer", func() { fc.OIDC.Issuer = r.FormValue("oidc_issuer") })
	set("OIDC.ClientID", func() { fc.OIDC.ClientID = r.FormValue("oidc_client_id") })
	set("OIDC.ClientSecretFile", func() { fc.OIDC.ClientSecretFile = r.FormValue("oidc_client_secret_file") })
	set("OIDC.ClientSecret", func() {
		if secret := r.FormValue("oidc_client_secret"); secret != "" {
			fc.OIDC.ClientSecret = secret
		}
	})
	set("OIDC.LocalLogin", func() { localLogin := r.FormValue("oidc_local_login") == "on"; fc.OIDC.LocalLogin = &localLogin })
	set("OIDC.ButtonText", func() { fc.OIDC.ButtonText = r.FormValue("oidc_button_text") })
	set("OIDC.Icon", func() { fc.OIDC.Icon = r.FormValue("oidc_icon") })
	set("OIDC.DefaultScopes", func() { fc.OIDC.DefaultScopes = list("oidc_default_scopes") })
	set("OIDC.AllowedSubjects", func() { fc.OIDC.AllowedSubjects = list("oidc_allowed_subjects") })
	set("OIDC.AllowedEmailDomains", func() { fc.OIDC.AllowedEmailDomains = list("oidc_allowed_email_domains") })
	set("OIDC.AllowAnyAuthenticated", func() { fc.OIDC.AllowAnyAuthenticated = r.FormValue("oidc_allow_any_authenticated") == "on" })
	set("OIDC.AllowInsecureLoopback", func() { fc.OIDC.AllowInsecureLoopback = r.FormValue("oidc_allow_insecure_loopback") == "on" })

	set("Git.Token", func() {
		if gitToken != "" && r.FormValue("store_git_token") == "on" {
			fc.Git.Token = gitToken
		}
	})

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

	if err := app.Store.UpdateRemote(storeOptions(newCfg)); err != nil {
		slog.Warn("updating store remote", "err", err)
	}

	slog.Info("settings updated", "by", app.currentUser(r))
	http.Redirect(w, r, "/_/admin?saved=1", http.StatusSeeOther)
}

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
	prevName, _ := effectiveSkin(app.config(), app.Auth.Prefs(user))
	switchingSkin := skinName(chosenSkin) != prevName

	// A skin change discards the submitted palette unless the browser supplied a later palette choice.
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

func (app *App) handleSettingsExport(w http.ResponseWriter, r *http.Request) {
	cfg := app.config()
	if err := SaveFileConfig(cfg.ConfigFile, cfg.ToFileConfig()); err != nil {
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
