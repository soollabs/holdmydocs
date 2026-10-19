package web

import (
	"bytes"
	"context"
	"html"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"hmd/internal/api"
	"hmd/internal/auth"
	"hmd/internal/config"
	"hmd/internal/httpmiddleware"
	"hmd/internal/i18n"
	"hmd/internal/oauthserver"
	"hmd/internal/wiki"
)

var version = "dev"

// App is the browser adapter: it renders HTML, owns templates and static
// assets, and delegates shared application state to API. Auth carries the
// transport identity primitives (sessions, login, OIDC, CSRF); every
// application read and write goes through API.
type App struct {
	previewMu sync.Mutex
	previews  map[string]*exportPreview
	API       *api.API
	Auth      *auth.Auth
	Render    *wiki.Renderer
	Tmpl      map[string]*template.Template
	OIDC      *auth.OIDC // nil when OIDC is disabled
	OAuth     *oauthserver.Service
}

func (app *App) apiClient() *api.API { return app.API }

func (app *App) config() config.Config { return app.apiClient().Config() }

// SetConfig stores a new configuration value atomically.
func (app *App) SetConfig(cfg config.Config) { app.apiClient().SetConfig(cfg) }

func (app *App) wikiConfig() wiki.WikiConfig { return app.apiClient().WikiConfig() }

// SetWikiConfig stores new repository-level settings atomically.
func (app *App) SetWikiConfig(cfg wiki.WikiConfig) { app.apiClient().SetWikiConfig(cfg) }

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
	ExportLinks             []wiki.ExportLink
	ExportTopbarLinks       []wiki.ExportLink
	ExportIcons             bool
	ExportMainBaseURL       string
	ExportPreview           bool
	PreviousPage            *pageLink
	NextPage                *pageLink
	SiteName                string
	AssetPath               string // static asset prefix; /_/static/ in the live app
	NamespaceHome           string // static namespace index link; empty uses the live route
	Static                  bool
	Authed                  bool
	Title                   string
	Description             string       // meta description on public and exported pages
	Preview                 *linkPreview // Open Graph tags; nil where the public URL is unknown
	Slug                    string
	Content                 template.HTML
	Body                    string
	BaseHash                string
	Backlinks               []wiki.BacklinkEntry
	TagsInput               string
	PageTags                []TagChip
	AllTags                 []wiki.TagCount
	TagName                 string
	TagPages                []wiki.BacklinkEntry
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
	HeadAuthor              string
	HeadWhen                string // relative, e.g. "3 hours ago"
	Username                string
	StatusMode              string // view|edit|search|log, drives the statusline mode block
	StatusContext           string // right-aligned context: revision count / word counts
	Version                 string // shown on login intro
	RemoteHost              string // host of the git remote, for login intro (empty if none)
	OIDCEnabled             bool   // show the SSO button on the login page
	OIDCButtonText          string // SSO button label
	OIDCLocalLogin          bool   // show the password form alongside SSO
	OIDCIcon                bool   // show the icon (served at /auth/oidc/icon) on the SSO button
	OAuthContinue           string // opaque, server-side OAuth login continuation handle
	OAuthEnabled            bool
	SettingsSection         string // highlights the current page in the shared settings navigation; empty hides it
	WikiWide                bool   // a screen outside any namespace (tags, search, hidden, health): the sidebar keeps only widgets that span the whole wiki
	SettingsPanel           string // URL-addressable account or system panel
	SettingsLabel           string // current section shown by the mobile selector
	SettingsHeading         string // page heading: the schema section title, or the lower-case label
	SettingsSubpage         string // namespace editor or export step in breadcrumbs
	SearchElapsed           string // search timing, e.g. "3ms"
	SearchHits              int    // match count, for search stats
	AttachmentHits          int    // attachment match count, for search stats
	SyncPollMs              int    // injected as a JS global for sync polling
	SyncMode                string
	BlobHash                string // current page blob hash, for client-side change detection
	IndexWarning            string // post-commit indexing note, e.g. after a save whose index refresh failed
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
	OAuthPrompt             *oauthserver.AuthorizationPrompt
	OAuthConnections        []oauthserver.Connection
	OAuthClients            []oauthserver.ClientSummary
	OAuthCreatedClient      *oauthserver.ProvisionedClient
	OAuthClientForm         url.Values
	OAuthClientRead         bool
	OAuthClientWrite        bool
	OAuthDisabledClients    int
	OAuthRedirect           string
	SetupSiteName           string // portable site-name default for first setup
	RoutePrefix             string
	IsHidden                bool
	IsNamespaceIndex        bool
	CanWrite                bool
	ReadOnly                bool
	CanSettings             bool
	CanViewSettings         bool             // settings pages are listed and shown; without CanSettings they are view-only
	PendingRestart          []pendingSetting // saved settings waiting for a restart; admins only
	PendingRestartKey       string           // dismissal key; changes when the pending settings do, so the banner returns
	LegacyAttachments       bool             // attachments in the old top-level layout wait to be moved or deleted; admins only
	HelpDrifted             bool             // .help.md differs from the built-in text; admins only
	SyncFailed              bool             // the last git sync failed; admins only
	IndexFailures           int              // pages the search index could not be updated for; admins only
	CanEdit                 bool
	NewPageHref             string
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

	// Widget data, populated in app.render only when something on the page actually reads it (see
	// populateWidgetData).
	Calendar           CalendarMonth
	WritingStats       WritingStats
	PinnedPages        []wiki.BacklinkEntry
	PrevEntries        []PrevEntry
	NamespaceNav       []NamespaceNavEntry
	SidebarTreeNS      string               // namespace used to stage SidebarTreeEntries
	SidebarTreeEntries []wiki.BacklinkEntry // unfiltered entries; filtered during render
	SidebarTree        template.HTML
	NewPageEnabled     bool   // whether ctrl-j is available
	NewNamespace       string // which namespace ctrl-j targets
}

// PageURL is the browser URL of the page being rendered, hidden or not.
func (d TemplateData) PageURL() string {
	if d.RoutePrefix == wiki.HiddenRoutePrefix {
		return wiki.HiddenPageURL(d.Slug)
	}
	return wiki.PageURL(d.Slug)
}

type LogEntry struct {
	Age     string
	Message string
}

func (app *App) render(w http.ResponseWriter, r *http.Request, status int, name string, data TemplateData) {
	data.ReadOnly = app.config().ReadOnly
	data.OAuthEnabled = app.OAuth != nil
	switch name {
	case "tags", "search", "hidden":
		data.WikiWide = true
	case "settings", "admin", "namespaces", "connections", "oauth-clients":
		data.SettingsSection = name
	case "namespace-edit":
		data.SettingsSection = "namespaces"
		if data.NamespaceManagement != nil && data.NamespaceManagement.Form.Name != "" {
			data.SettingsSubpage = i18n.T("web.render.subpage_edit", "Name", data.NamespaceManagement.Form.Name)
			if s, ok := namespaceSectionByKey(data.NamespaceManagement.Section); ok && s.Key != "general" {
				data.SettingsSubpage = i18n.T("web.render.subpage_edit_section", "Name", data.NamespaceManagement.Form.Name, "Section", i18n.T(s.Label))
			}
		} else {
			data.SettingsSubpage = i18n.T("web.render.subpage_new_namespace")
		}
	case "export-options":
		data.SettingsSection = "namespaces"
		data.SettingsSubpage = data.Title
	}
	if name == "settings" || name == "admin" {
		data.SettingsPanel = r.PathValue("panel")
		if settingsPanels[data.SettingsPanel].Template != name {
			data.SettingsPanel = map[string]string{"settings": "appearance", "admin": "server"}[name]
		}
		if name == "settings" && data.Settings != nil && (data.Settings.NewToken != "" || data.Settings.TokenError != "") {
			data.SettingsPanel = "access-tokens"
		}
		if name == "admin" && data.Settings != nil && data.Settings.UserError != "" {
			data.SettingsPanel = "users"
		}
	}
	if data.SettingsSection != "" {
		key := data.SettingsSection
		if data.SettingsPanel != "" {
			key = data.SettingsPanel
		}
		data.SettingsLabel = i18n.T(settingsPanels[key].Label)
		if nm := data.NamespaceManagement; nm != nil && nm.Form.Name != "" && len(nm.Sections) > 0 {
			// The mobile section selector names the namespace page being edited.
			if s, ok := namespaceSectionByKey(nm.Section); ok {
				data.SettingsLabel = i18n.T("web.render.section_label", "Name", nm.Form.Name, "Section", i18n.T(s.Label))
			}
		}
		data.SettingsHeading = strings.ToLower(data.SettingsLabel)
		if section, ok := config.Layout(key); ok {
			data.SettingsHeading = section.Title
		}
		data.Title = data.SettingsLabel
		if data.SettingsSubpage != "" {
			data.Title = data.SettingsSubpage
		}
	}
	if data.SiteName == "" {
		data.SiteName = app.wikiConfig().SiteName
	}
	data.AssetPath = "/_/static/"
	data.Version = version
	data.RemoteHost = remoteHost(app.config().Git.RemoteURL)
	if data.SyncState == "" {
		data.SyncState, _, _ = app.apiClient().SyncState()
	}
	if data.Authed && data.Username == "" {
		data.Username = app.currentUser(r)
	}
	if cookie, err := r.Cookie("hmd_session"); err == nil {
		data.CSRFToken = app.Auth.CSRFToken(cookie.Value)
	}
	// Anonymous error pages deliberately remain byte-identical so private and
	// missing pages cannot become an existence oracle.
	if data.Authed || data.NamespacePublic || name == "login" {
		data.CSPNonce = httpmiddleware.CspNonce(r.Context())
	}
	// Use the current page's namespace as the default destination.
	if data.Namespace == "" {
		data.Namespace, _ = wiki.NamespaceFor(data.Slug)
	}
	if data.Authed {
		health, err := app.apiClient().HealthSummary(r.Context())
		if err != nil {
			slog.Error("reading health summary", "err", err)
		}
		data.HealthMissing = health.Missing
		data.HealthOrphans = health.Orphans
		// Default the tags widget to the current page's namespace tags, but
		// preserve an explicit whole-wiki list supplied by the handler (the
		// tags index).
		if data.AllTags == nil && data.WikiWide {
			data.AllTags = app.apiClient().Tags(r.Context())
		} else if data.AllTags == nil {
			ns, _ := wiki.NamespaceFor(data.Slug)
			data.AllTags = app.apiClient().NamespaceTags(r.Context(), ns)
		}
	}
	if data.Authed && data.StatusMode == "" {
		data.StatusMode = "view"
	}
	if data.Authed {
		_, _, data.SyncLastUnix = app.apiClient().SyncState()
		if ts := data.SyncLastUnix; ts > 0 {
			if d := time.Since(time.Unix(ts, 0)); d < time.Minute {
				data.SyncAge = i18n.T("js.age.seconds", "Count", int(d.Seconds()))
			} else {
				data.SyncAge = wiki.RelativeTime(time.Unix(ts, 0))
			}
		} else {
			data.SyncAge = "—"
		}
	}
	if data.Authed {
		cfg := app.config()
		prefs := app.Auth.Prefs(app.currentUser(r))
		data.CanWrite = app.apiClient().HasScope(r.Context(), api.ScopeWrite)
		data.CanSettings = app.apiClient().HasScope(r.Context(), api.ScopeSettings)
		data.CanViewSettings = app.apiClient().CanViewSettings(r.Context())
		if data.CanSettings {
			pending := app.apiClient().PendingRestart()
			data.PendingRestart = pendingSettings(pending)
			keys := make([]string, len(pending))
			for i, s := range pending {
				keys[i] = s.Key
			}
			data.PendingRestartKey = "restart:" + strings.Join(keys, ",")
			data.LegacyAttachments = app.apiClient().LegacyAttachments()
			data.HelpDrifted = app.apiClient().HelpDrifted()
			data.SyncFailed = data.SyncState == "failed"
			data.IndexFailures = app.apiClient().IndexFailures()
		}
		data.CanEdit = app.apiClient().CanViewEditor(r.Context()) && data.StatusMode == "view" && isPageSlug(data.Slug) && !data.IsNamespaceIndex
		if data.CanWrite {
			base := ""
			if data.IsNamespaceIndex {
				base = data.Namespace
			} else if isPageSlug(data.Slug) {
				base = data.Slug[:strings.LastIndex(data.Slug, "/")]
			} else if names := app.Namespaces().Names(); len(names) > 0 {
				base = names[0]
			}
			if base != "" {
				data.NewPageHref = wiki.PageURL(base+"/new") + "?do=edit"
			}
		}
		if data.StatusMode == "edit" {
			for _, name := range app.Namespaces().Names() {
				if auth.TokenAllowsSlug(r.Context(), wiki.NamespaceSlug(name, "")) {
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
		if cfg, ok := nsRegistry[target]; ok && cfg.New != nil && data.CanWrite {
			data.NewPageEnabled = true
			data.NewNamespace = target
		}
		if state := app.apiClient().SetupState(); state.Needed && data.CanSettings {
			if state.Wiki {
				data.NeedsWikiSetup = true
				data.SetupNamespace = api.DefaultSetupNamespace
				data.SetupNamespaces = app.Namespaces().Names()
				data.SetupSiteName = app.wikiConfig().SiteName
			}

			if state.Namespace {
				data.NeedsNamespaceSetup = true
				data.SetupNamespace = api.DefaultSetupNamespace
				homePreview, _ := app.Render.Render(api.DefaultHomeBody(), api.DefaultSetupNamespace)
				data.SetupHomePreview = template.HTML(homePreview)
			}

			if state.Help {
				data.NeedsHelpSetup = true
				data.HelpFileExists = state.HelpFileExists
				helpPreview, _ := app.Render.Render(api.DefaultHelpBody(), "")
				data.SetupHelpPreview = template.HTML(helpPreview)
			}

			data.NeedsSetup = true
		}

		nsCfg := app.Namespaces().Resolve(data.Slug)
		data.SidebarWidgets = widgetsForSlot(slotSidebar, nsCfg.Widgets)
		if data.WikiWide {
			data.SidebarWidgets = slices.DeleteFunc(widgetsForSlot(slotSidebar, wiki.BuiltinNamespaceWidgets), func(w *widget) bool { return !wikiWideWidgets[w.ID] })
		}
		data.RailWidgets = widgetsForSlot(slotRail, nsCfg.Widgets)
		data.PageHeadWidgets = widgetsForSlot(slotPageHead, nsCfg.Widgets)
		data.PageFootWidgets = widgetsForSlot(slotPageFoot, nsCfg.Widgets)

		app.populateWidgetData(r.Context(), &data, activeSkin)
		for i, v := range slices.Backward(data.NamespaceNav) {
			if !auth.TokenAllowsNamespace(r.Context(), v.Name) {
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
				return wiki.PageURL(wiki.NamespaceSlug(data.SidebarTreeNS, to))
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
	if user := auth.UserFromContext(r.Context()); user != "" {
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
	app.errorPage(w, r, http.StatusNotFound, i18n.T("web.render.not_found_title"), i18n.T("web.render.not_found_detail"))
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
	// Only a navigation gets the rendered page; a denied write comes from
	// fetch, which reads the status and never the body.
	if r.Method == http.MethodGet {
		app.errorPage(w, r, http.StatusForbidden, i18n.T("web.render.forbidden_title"), i18n.T("web.render.forbidden_detail"))
		return
	}
	http.Error(w, "403 Forbidden: namespace access denied", http.StatusForbidden)
}

func (app *App) requireTokenNamespace(w http.ResponseWriter, r *http.Request, namespace string) bool {
	if auth.TokenAllowsNamespace(r.Context(), namespace) {
		return true
	}
	app.tokenNamespaceDenied(w, r)
	return false
}

func (app *App) requireTokenSlug(w http.ResponseWriter, r *http.Request, slug string) bool {
	if auth.TokenAllowsSlug(r.Context(), slug) {
		return true
	}
	app.tokenNamespaceDenied(w, r)
	return false
}

func filterBacklinkEntries(ctx context.Context, entries []wiki.BacklinkEntry) []wiki.BacklinkEntry {
	filtered := make([]wiki.BacklinkEntry, 0, len(entries))
	for _, entry := range entries {
		if auth.TokenAllowsSlug(ctx, entry.Slug) {
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
	return config.ParseAuthor(raw, username)
}

func htmlEscape(s string) string {
	return html.EscapeString(s)
}
