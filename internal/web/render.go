package web

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"hmd/internal/api"
	"hmd/internal/auth"
	"hmd/internal/config"
	"hmd/internal/httpmiddleware"
	"hmd/internal/wiki"
)

var buildVersion = "dev"
var version = config.EnvOr("HMD_VERSION", buildVersion)

// App is the browser adapter: it renders HTML, owns templates and static
// assets, and delegates shared application state to API. Auth carries the
// transport identity primitives (sessions, login, OIDC, CSRF); every
// application read and write goes through API.
type App struct {
	API    *api.API
	Auth   *auth.Auth
	Render *wiki.Renderer
	Tmpl   map[string]*template.Template
	OIDC   *auth.OIDC // nil when OIDC is disabled
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
	PinnedPages        []wiki.BacklinkEntry
	PrevEntries        []PrevEntry
	NamespaceNav       []NamespaceNavEntry
	SidebarTreeNS      string               // namespace used to stage SidebarTreeEntries
	SidebarTreeEntries []wiki.BacklinkEntry // unfiltered entries; filtered during render
	SidebarTree        template.HTML
	NewPageEnabled     bool   // whether ctrl-j is available
	NewNamespace       string // which namespace ctrl-j targets
}

type LogEntry struct {
	Age     string
	Message string
}

func (app *App) render(w http.ResponseWriter, r *http.Request, status int, name string, data TemplateData) {
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
	if data.Authed || data.NamespacePublic {
		data.CSPNonce = httpmiddleware.CspNonce(r.Context())
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
		_, _, data.SyncLastUnix = app.apiClient().SyncState()
		if ts := data.SyncLastUnix; ts > 0 {
			if d := time.Since(time.Unix(ts, 0)); d < time.Minute {
				data.SyncAge = fmt.Sprintf("%ds ago", int(d.Seconds()))
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
		if cfg, ok := nsRegistry[target]; ok && cfg.New != nil {
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
		data.RailWidgets = widgetsForSlot(slotRail, nsCfg.Widgets)
		data.PageHeadWidgets = widgetsForSlot(slotPageHead, nsCfg.Widgets)
		data.PageFootWidgets = widgetsForSlot(slotPageFoot, nsCfg.Widgets)

		app.populateWidgetData(r.Context(), &data, activeSkin)
		for i := len(data.NamespaceNav) - 1; i >= 0; i-- {
			if !auth.TokenAllowsNamespace(r.Context(), data.NamespaceNav[i].Name) {
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
	// Only a navigation gets the rendered page; a denied write comes from
	// fetch, which reads the status and never the body.
	if r.Method == http.MethodGet {
		app.errorPage(w, r, http.StatusForbidden, "Forbidden", "You don't have access to that namespace.")
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
