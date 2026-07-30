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
	"sync/atomic"
	texttemplate "text/template"
	"time"
)

// buildVersion is baked in at compile time via -ldflags "-X main.buildVersion=..."
// (see Dockerfile's VERSION build arg). HMD_VERSION overrides it at runtime
// if set. Shown on the login screen and sidebar footer.
var buildVersion = "dev"
var version = envOr("HMD_VERSION", buildVersion)

// journalNamespace is the one namespace name ctrl-j (and the palette's
// >new verb) is wired to — the successor to the old hardcoded daily/
// directory, now expressed as an ordinary namespace with a `new:` template
// instead of code-level special-casing.
const journalNamespace = "journal"

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
	SiteName            string
	Authed              bool
	Title               string
	Slug                string
	Content             template.HTML
	Body                string
	BaseHash            string
	Backlinks           []BacklinkEntry
	TagsInput           string
	PageTags            []TagChip
	AllTags             []TagCount
	TagName             string
	TagPages            []BacklinkEntry
	SyncState           string
	Error               string
	Query               string
	SearchResults       []SearchResult
	HistoryEntries      []HistoryEntry
	RevHash             string
	OldVersionDate      string
	TotalHistory        int
	MermaidNeeded       bool
	RevisionCount       int
	HeadShortHash       string
	HeadAuthor          string
	HeadWhen            string // relative, e.g. "3 hours ago"
	Username            string
	StatusMode          string // view|edit|search|log|conflict, drives the statusline mode block
	StatusContext       string // right-aligned context: revision count / word counts
	Version             string // shown on login intro
	RemoteHost          string // host of the git remote, for login intro (empty if none)
	OIDCEnabled         bool   // show the SSO button on the login page
	OIDCButtonText      string // SSO button label
	OIDCLocalLogin      bool   // show the password form alongside SSO
	OIDCIcon            bool   // show the icon (served at /auth/oidc/icon) on the SSO button
	SearchElapsed       string // search timing, e.g. "3ms"
	SearchPages         int    // total pages, for search stats
	SearchHits          int    // match count, for search stats
	SyncPollMs          int    // injected as a JS global for sync polling
	SyncMode            string
	BlobHash            string // current page blob hash, for client-side change detection
	ThemeStyle          template.CSS
	Skin                string // structural skin name; empty = default, only ever a known skinNames entry
	Settings            *SettingsData
	SetupHomePreview    template.HTML
	SetupHelpPreview    template.HTML
	NeedsSetup          bool
	NeedsHomeSetup      bool
	NeedsHelpSetup      bool
	HomeFileExists      bool
	HelpFileExists      bool
	HomeFilename        string
	RoutePrefix         string
	IsHidden            bool
	IsNamespaceIndex    bool
	CanWrite            bool
	CanSettings         bool
	NamespaceManagement *NamespaceManagementData
	Namespace           string // namespace index page: the namespace being listed
	NamespacePublic     bool
	RecentCommits       []LogEntry // sidebar LOG section: last commits for the current page
	HealthMissing       int
	HealthOrphans       int
	SyncAge             string // relative age of the last successful sync, e.g. "12 seconds ago"
	SyncLastUnix        int64  // raw timestamp for the client-side sync-age ticker
	SidebarWidgets      []*widget
	RailWidgets         []*widget
	PageHeadWidgets     []*widget
	PageFootWidgets     []*widget
	StatusVariant       string // skin.Status: full | write | quiet

	// Widget data — populated in app.render only when something on the page
	// actually reads it (see populateWidgetData).
	Calendar       CalendarMonth
	WritingStats   WritingStats
	PinnedPages    []BacklinkEntry
	PrevEntries    []PrevEntry
	NamespaceNav   []NamespaceNavEntry
	NewPageEnabled bool   // a namespace with a `new:` template is in reach — gates the ctrl-j shortcut and >new verb client-side
	NewNamespace   string // which one ctrl-j targets: this page's, else the journal fallback
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
	HomeFilename     string        // read-only display; restart required to change
	HomeFilenameEnv  string        // env var name if it overrides the file, else ""
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
		data.SyncPollMs = cfg.SyncPollMs
		data.SyncMode = cfg.SyncMode
		activeName, activeSkin := effectiveSkin(cfg, prefs)
		themePrefs := prefs
		themePrefs.Palette = effectivePalette(prefs, activeSkin)
		data.ThemeStyle = buildThemeStyle(themePrefs)
		data.Skin = activeName
		data.StatusVariant = activeSkin.Status
		// ctrl-j creates in the namespace you are standing in when it has a
		// `new:` block, falling back to the journal namespace — otherwise the
		// per-namespace "ctrl-j creates a page here" checkbox would be a lie
		// everywhere except journal.
		nsRegistry := app.Namespaces()
		target, _ := namespaceFor(data.Slug)
		if cfg, ok := nsRegistry[target]; !ok || cfg.New == nil {
			target = journalNamespace
		}
		if cfg, ok := nsRegistry[target]; ok && cfg.New != nil {
			data.NewPageEnabled = true
			data.NewNamespace = target
		}
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
		for i := len(data.NamespaceNav) - 1; i >= 0; i-- {
			if !tokenAllowsNamespace(r.Context(), data.NamespaceNav[i].Name) {
				data.NamespaceNav = append(data.NamespaceNav[:i], data.NamespaceNav[i+1:]...)
			}
		}
		data.PinnedPages = filterBacklinkEntries(r.Context(), data.PinnedPages)
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

func tokenNamespaceDenied(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/_/api/") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		if _, err := w.Write([]byte(`{"error":"namespace access denied"}`)); err != nil {
			slog.Debug("writing namespace error response", "err", err)
		}
		return
	}
	http.Error(w, "403 Forbidden: namespace access denied", http.StatusForbidden)
}

func (app *App) requireTokenNamespace(w http.ResponseWriter, r *http.Request, namespace string) bool {
	if tokenAllowsNamespace(r.Context(), namespace) {
		return true
	}
	tokenNamespaceDenied(w, r)
	return false
}

func (app *App) requireTokenSlug(w http.ResponseWriter, r *http.Request, slug string) bool {
	if tokenAllowsSlug(r.Context(), slug) {
		return true
	}
	tokenNamespaceDenied(w, r)
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

// handleRoot redirects "/" to Config.LandingSlug() — the configured landing
// slug if set, else the home page. An unauthenticated request never reaches
// here (Auth.Middleware always requires auth for "/").
func (app *App) handleRoot(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/"+app.config().LandingSlug(), http.StatusSeeOther)
}

func (app *App) Routes() http.Handler {
	mux := http.NewServeMux()

	// Root: redirect to the configured landing slug.
	mux.HandleFunc("GET /{$}", app.handleRoot)

	// Everything under /_/ is the app itself — the one reserved top-level
	// segment a namespace may never take. Content owns everything else.

	// Setup endpoint: seeds the home file + .help.md, clears the setup flag
	mux.HandleFunc("POST /_/setup", app.handleSetup)

	// New-page-from-template: ctrl-j and the palette's >new verb both call
	// this with ns=journal; generic over any namespace with a `new:` block.
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
	mux.HandleFunc("POST /_/admin", app.handleSettingsPost)
	mux.HandleFunc("POST /_/settings/appearance", app.handleSettingsAppearance)
	mux.HandleFunc("POST /_/settings/export", app.handleSettingsExport)
	mux.HandleFunc("POST /_/settings/author", app.handleSetAuthor)
	mux.HandleFunc("POST /_/settings/tokens", app.handleCreateToken)
	mux.HandleFunc("POST /_/settings/tokens/revoke", app.handleRevokeToken)
	mux.HandleFunc("POST /_/settings/namespaces", app.handleSaveNamespace)
	mux.HandleFunc("POST /_/settings/namespaces/reset", app.handleResetNamespace)
	mux.HandleFunc("POST /_/settings/namespaces/delete", app.handleDeleteNamespace)
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
	mux.HandleFunc("GET /_/api/preview/{slug...}", app.handleAPIPreview)
	mux.HandleFunc("POST /_/api/preview", app.handlePreview)
	mux.HandleFunc("POST /_/api/attachments/{slug...}", app.handleUploadAttachment)
	mux.HandleFunc("GET /_/attachments/{path...}", app.handleServeAttachment)

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
// overwriting an existing one. ctrl-j and the palette's >new verb call this
// with ns=journal; the endpoint itself is generic over any namespace that
// declares a `new:` block.
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

	newPage := Page{Slug: slug, Title: title, Tags: tags, Body: body}
	authorName, authorEmail := app.gitAuthor(username)
	if _, err := app.Store.Save(pageFile(slug), newPage.Encode(), "Create "+slug, authorName, authorEmail); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := app.Index.Update(newPage); err != nil {
		slog.Error("updating search index", "slug", slug, "err", err)
	}

	http.Redirect(w, r, "/"+slug+"?do=edit", http.StatusSeeOther)
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
				http.NotFound(w, r)
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
	page.Body = injectTOC(page.Body, app.Index, app.config().HomeSlug())
	renderedBody, err := app.Render.Render(page.Body)
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
		http.NotFound(w, r)
		return
	}

	isPublicLink := func(s string) bool { return app.Index.Exists(s) && ns.IsPublic(s) }
	renderedBody, err := app.Render.RenderPublic(page.Body, isPublicLink)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	app.render(w, r, http.StatusOK, "page", TemplateData{
		Authed:  false,
		Title:   page.Title,
		Slug:    slug,
		Content: renderedBody,
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
	slug := r.PathValue("path")
	if reservedPath(slug) {
		http.NotFound(w, r)
		return
	}
	if !app.requireTokenSlug(w, r, slug) {
		return
	}
	r.SetPathValue("slug", slug)
	switch r.URL.Query().Get("do") {
	case "":
		if name, ok := app.namespaceIndexName(slug); ok {
			app.handleNamespaceIndex(w, r, name)
			return
		}
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

// namespaceIndexName reports whether path addresses a namespace's index: an
// exact one-segment name with a trailing slash that is present in the
// catalogue. Bare paths remain root-page URLs.
func (app *App) namespaceIndexName(path string) (string, bool) {
	name, slashed := strings.CutSuffix(path, "/")
	if !slashed || name == "" || strings.Contains(name, "/") {
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
	var summary *NamespaceSummary
	for _, entry := range namespaceSummaries(app.Namespaces(), app.Index.Titles()) {
		if entry.Name == name {
			entryCopy := entry
			summary = &entryCopy
			break
		}
	}
	if summary == nil || (!authed && !summary.Config.Public) {
		http.NotFound(w, r)
		return
	}

	app.render(w, r, http.StatusOK, "namespace", TemplateData{
		Authed:           authed,
		Title:            name,
		Slug:             name + "/",
		StatusMode:       "view",
		StatusContext:    fmt.Sprintf("%d pages", summary.Count),
		Namespace:        name,
		NamespacePublic:  summary.Config.Public,
		IsNamespaceIndex: true,
		TagPages:         filterBacklinkEntries(r.Context(), summary.Pages),
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
	default:
		http.NotFound(w, r)
	}
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
		http.NotFound(w, r)
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

	page := Page{Slug: slug, Title: title, Tags: ParseTags(tagsInput), Body: body}
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
	if !app.requireTokenSlug(w, r, slug) {
		return
	}
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

// handleServeAttachment serves a page's attachment. Auth.Middleware lets an
// anonymous request through unconditionally (it can't know which namespace
// owns the attachment without a lookup of its own), so the public-or-404
// call is made here — identically whether the attachment is missing or the
// owning page's namespace just isn't public.
func (app *App) handleServeAttachment(w http.ResponseWriter, r *http.Request) {
	// {path...} is "{slug}/{file}"; slug itself may contain "/" for a
	// namespaced page, so only the last segment is ever the filename.
	path := r.PathValue("path")
	i := strings.LastIndex(path, "/")
	if i < 0 {
		http.NotFound(w, r)
		return
	}
	slug, file := path[:i], path[i+1:]
	if !app.requireTokenSlug(w, r, slug) {
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

	pageCount := 0
	for slug := range app.Index.Titles() {
		if tokenAllowsSlug(r.Context(), slug) {
			pageCount++
		}
	}
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
	newSlug := Slugify(newTitle)
	if newSlug == "" {
		http.Error(w, "invalid title", http.StatusBadRequest)
		return
	}
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
		slug := strings.TrimPrefix(p, ".")
		slug = slug[:len(slug)-3] // strip .md
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
			continue
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
		http.NotFound(w, r)
		return
	}
	data := app.namespaceManagementData(r, name, "")
	if data.Form.Name == "" {
		http.NotFound(w, r)
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

	// "" is the root namespace, always present — any other name is a single
	// directory segment, and must be one a namespace may actually take.
	if name != "" && (strings.Contains(name, "/") || !validNamespaceName(name)) {
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

	cfg := NamespaceConfig{Widgets: ids, Public: r.FormValue("public") == "on"}
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
	if name != "" && !validNamespaceName(name) {
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
// deletion is only allowed for a configured namespace whose directory and
// hidden namespace storage contain no other content.
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
