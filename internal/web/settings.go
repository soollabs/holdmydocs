package web

import (
	"fmt"
	"html"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"hmd/internal/api"
	"hmd/internal/auth"
	"hmd/internal/config"
	"hmd/internal/i18n"
	"hmd/internal/presentation"
	"hmd/internal/wiki"
)

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
	FileCount    int                // files in the namespace being edited; deleting removes them too
	Section      string             // active editor section, one of namespaceSections
	Sections     []namespaceSection // editor pages, listed in the settings navigation
}

type FieldState struct {
	Value           string // current runtime value (or "set"/"not set" for secrets)
	Editable        bool   // can the user change this from the UI?
	RestartRequired bool   // does the change only take effect on restart?
	EnvVar          string // non-empty if an env var overrides this field
}

// SettingsData is passed to the settings template.
type SettingsData struct {
	Fields          map[string]FieldState
	ConfigPath      string
	Flash           string // success message after a save
	Error           string // validation error
	MaxUploadBytes  int64
	SyncPollMs      int
	SyncMode        string
	Palette         string
	PaletteNames    []string
	Palettes        map[string]themePreset // JSON-encoded into the settings page script
	FontUI          string
	FontMono        string
	FontsMono       []string // option groups for the font selects
	FontsSans       []string
	FontsSerif      []string
	FontStacks      map[string]string // JSON-encoded for the live font preview
	Skin            string
	SkinNames       []string
	SkinPalettes    map[string]string // skin -> default palette, JSON-encoded to move the selection on change
	HelpDrifted     bool
	UserGitAuthor   string             // current user's per-user git author override
	WikiConfigPath  string             // repository-relative .wiki.yaml path
	WikiLanding     string             // where "/" redirects to
	WikiSiteName    string             // title shared by every clone of the wiki
	Tokens          []TokenView        // current user's personal access tokens
	TokenNamespaces []string           // selectable namespace names for new tokens
	NewToken        string             // freshly minted token value, shown exactly once
	TokenError      string             // token create/revoke validation error
	HasEnvOverrides bool               // any field currently sourced from an env var — shows the "export to file" action
	Users           []auth.UserSummary // every user, for the users tab
	AllScopes       []string           // "read", "write", "settings" — the scope checkbox options
	CurrentUser     string             // name of the logged-in user, so the users tab can block self-lockout
	UserError       string             // create/scope-update validation error
	// Legacy attachments a migration would move, and the gone namespaces
	// whose old attachments it leaves.
	LegacyAttachments         []api.LegacyAttachmentNamespace
	LegacyAttachmentsMovable  bool
	LegacyAttachmentsOrphaned bool
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

// settingFields builds the settings-page state of every config.yaml setting,
// keyed by schema key (git.remote_url). Environment-only settings are never
// editable, and secrets only report whether they are set.
func settingFields(cfg config.Config) map[string]FieldState {
	fields := make(map[string]FieldState)
	for _, s := range config.Settings() {
		value := settingText(config.SettingValue(cfg, s.Key))
		if s.Secret {
			value = map[bool]string{true: i18n.T("web.settings.secret_set"), false: i18n.T("web.settings.secret_not_set")}[value != ""]
		}
		env := cfg.EnvOverrides[s.Key]
		if s.EnvOnly && os.Getenv(s.Env) != "" {
			env = s.Env
		}
		fields[s.Key] = FieldState{Value: value, Editable: env == "" && !s.EnvOnly, RestartRequired: s.Restart, EnvVar: env}
	}
	return fields
}

// pendingSetting is a saved setting waiting for a restart, as the restart
// banner lists it: a label and a link to its row on the settings page.
type pendingSetting struct {
	Label string
	Href  string
}

func pendingSettings(settings []config.Setting) []pendingSetting {
	var pending []pendingSetting
	for _, s := range settings {
		item := pendingSetting{Label: s.Title}
		if s.Group != "" {
			item.Label = s.Group + ": " + s.Title
		}
		for _, section := range config.Sections() {
			for _, group := range section.Groups {
				if slices.Contains(group.Rows, s.Key) {
					item.Href = "/_/settings/" + section.Key + "#" + s.Key
				}
			}
		}
		pending = append(pending, item)
	}
	return pending
}

// settingText renders a config value for an input: lists as comma-separated text.
func settingText(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case []string:
		return strings.Join(t, ", ")
	default:
		return fmt.Sprint(t)
	}
}

func buildSettingsData(cfg config.Config, prefs auth.UserRecord) SettingsData {
	fields := settingFields(cfg)

	activeName, activeSkin := effectiveSkin(cfg, prefs)

	return SettingsData{
		Fields:          fields,
		ConfigPath:      cfg.ConfigFile,
		MaxUploadBytes:  cfg.MaxUploadBytes,
		SyncPollMs:      cfg.SyncPollMs,
		SyncMode:        cfg.SyncMode,
		Palette:         effectivePalette(prefs, activeSkin),
		PaletteNames:    themePresetNames,
		Palettes:        themePresets,
		FontUI:          prefs.FontUI,
		FontMono:        prefs.FontMono,
		FontsMono:       fontsMono,
		FontsSans:       fontsSans,
		FontsSerif:      fontsSerif,
		FontStacks:      fontStacks,
		Skin:            activeName,
		SkinNames:       skinNames,
		SkinPalettes:    skinPalettes(),
		HasEnvOverrides: len(cfg.EnvOverrides) > 0,
	}
}

func (app *App) settingsData(r *http.Request) SettingsData {
	// Show what was saved, including settings still waiting for a restart.
	cfg := app.apiClient().SavedConfig()
	user := app.currentUser(r)
	prefs := app.Auth.Prefs(user)

	sd := buildSettingsData(cfg, prefs)
	sd.HelpDrifted = app.apiClient().HelpDrifted()
	sd.UserGitAuthor = app.Auth.AuthorFor(user)
	wikiCfg := app.wikiConfig()
	sd.WikiConfigPath = wiki.ConfigFile
	sd.WikiLanding = wikiCfg.Landing
	sd.WikiSiteName = wikiCfg.SiteName
	sd.Users = app.Auth.Users()
	sd.AllScopes = []string{string(auth.ScopeRead), string(auth.ScopeWrite), string(auth.ScopeSettings)}
	sd.CurrentUser = user
	for _, summary := range app.apiClient().ListNamespaces(r.Context()) {
		sd.TokenNamespaces = append(sd.TokenNamespaces, summary.Name)
	}
	for _, t := range app.Auth.TokensFor(user) {
		expires := i18n.T("web.settings.expires_never")
		switch {
		case t.Expired():
			expires = i18n.T("web.settings.expires_expired")
		case !t.Expires.IsZero():
			expires = t.Expires.Format("2006-01-02")
		}
		effectiveScopes := auth.EffectiveTokenScopes(prefs.Scopes, t.Scopes)
		isAdmin := auth.UserRecord{Scopes: effectiveScopes}.HasScope(auth.ScopeSettings)
		namespaceLabel := i18n.T("web.settings.namespaces_all")
		if isAdmin {
			namespaceLabel = i18n.T("web.settings.namespaces_administrator")
		} else if len(t.Namespaces) > 0 {
			namespaceLabel = i18n.T("web.settings.namespaces_only", "Names", strings.Join(t.Namespaces, ", "))
		}
		scopeLabel := strings.Join(t.Scopes, ", ")
		sd.Tokens = append(sd.Tokens, TokenView{
			Name:           t.Name,
			Created:        wiki.RelativeTime(t.Created),
			Expires:        expires,
			Scopes:         append([]string(nil), t.Scopes...),
			Namespaces:     append([]string(nil), t.Namespaces...),
			ScopeLabel:     scopeLabel,
			NamespaceLabel: namespaceLabel,
		})
	}
	return sd
}

func (app *App) renderSettings(w http.ResponseWriter, r *http.Request, sd SettingsData, tmpl string) {
	data := TemplateData{
		Authed:        true,
		Title:         i18n.T("web.settings.title"),
		StatusMode:    "settings",
		StatusContext: i18n.T("web.settings.status_context"),
		Settings:      &sd,
	}
	app.render(w, r, http.StatusOK, tmpl, data)
}

// settingsPanel describes one /_/settings/<name> page. Template is the page
// that renders it when served by handleSettingsPanelGet; sections with their
// own routes leave it empty.
type settingsPanel struct {
	Label, Template string
}

var settingsPanels = map[string]settingsPanel{
	"appearance":      {"web.settings.panel.appearance", "settings"},
	"git-author":      {"web.settings.panel.git_author", "settings"},
	"access-tokens":   {"web.settings.panel.access_tokens", "settings"},
	"server":          {"web.settings.panel.server", "admin"},
	"git":             {"web.settings.panel.git", "admin"},
	"mcp":             {"web.settings.panel.mcp", "admin"},
	"oidc":            {"web.settings.panel.oidc", "admin"},
	"document-search": {"web.settings.panel.document_search", "admin"},
	"wiki":            {"web.settings.panel.wiki", "admin"},
	"users":           {"web.settings.panel.users", "admin"},
	"advanced":        {"web.settings.panel.advanced", "admin"},
	"namespaces":      {"web.settings.panel.namespaces", ""},
	"connections":     {"web.settings.panel.connections", ""},
	"oauth-clients":   {"web.settings.panel.oauth_clients", ""},
}

func (app *App) handleSettingsPanelGet(w http.ResponseWriter, r *http.Request) {
	switch settingsPanels[r.PathValue("panel")].Template {
	case "admin":
		app.handleAdminGet(w, r)
	case "settings":
		app.handleAccountSettingsGet(w, r)
	default:
		app.notFound(w, r)
	}
}

func (app *App) handleAccountSettingsGet(w http.ResponseWriter, r *http.Request) {
	sd := app.settingsData(r)
	if q := r.URL.Query().Get("saved"); q == "1" {
		sd.Flash = i18n.T("web.settings.flash_saved")
	}
	app.renderSettings(w, r, sd, "settings")
}

func (app *App) handleAdminGet(w http.ResponseWriter, r *http.Request) {
	sd := app.settingsData(r)
	if q := r.URL.Query().Get("saved"); q == "1" {
		sd.Flash = i18n.T("web.settings.flash_saved")
	}
	if q := r.URL.Query().Get("exported"); q == "1" {
		sd.Flash = i18n.T("web.settings.flash_exported", "Path", sd.ConfigPath)
	}
	if q := r.URL.Query().Get("wiki-saved"); q == "1" {
		sd.Flash = i18n.T("web.settings.flash_wiki_saved", "Path", sd.WikiConfigPath)
	}
	if q := r.URL.Query().Get("attachments-moved"); q == "1" {
		sd.Flash = i18n.T("web.settings.flash_attachments_moved")
	}
	if q := r.URL.Query().Get("attachments-deleted"); q == "1" {
		sd.Flash = i18n.T("web.settings.flash_attachments_deleted")
	}
	legacy, err := app.apiClient().LegacyAttachmentStatus()
	if err != nil {
		slog.Warn("counting legacy attachments", "err", err)
	}
	sd.LegacyAttachments = legacy
	for _, n := range legacy {
		sd.LegacyAttachmentsMovable = sd.LegacyAttachmentsMovable || n.Exists
		sd.LegacyAttachmentsOrphaned = sd.LegacyAttachmentsOrphaned || !n.Exists
	}
	app.renderSettings(w, r, sd, "admin")
}

func (app *App) namespaceManagementData(r *http.Request, name, errMsg string) NamespaceManagementData {
	user := app.currentUser(r)
	data := NamespaceManagementData{
		CanWrite:     app.apiClient().HasScope(r.Context(), api.ScopeWrite),
		CanSettings:  app.apiClient().HasScope(r.Context(), api.ScopeSettings),
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
		if entry.Name == name && auth.TokenAllowsNamespace(r.Context(), entry.Name) {
			if detail, err := app.apiClient().ReadNamespace(r.Context(), entry.Name); err == nil {
				entry.Hash = detail.Hash
				data.FileCount = detail.Files
			}
			data.Form = entry
			data.TreeEditor = namespaceTreeEditor(app.apiClient().PageTitles(r.Context()), entry.Name, entry.Index, entry.Tree)
			break
		}
	}
	return data
}

func namespaceTreeItems(titles map[string]string, namespace, index string, tree []string) []NamespaceTreeItem {
	entries := make([]wiki.BacklinkEntry, 0)
	for slug, title := range titles {
		if ns, rest := wiki.NamespaceFor(slug); ns == namespace && rest != "" {
			entries = append(entries, wiki.BacklinkEntry{Slug: slug, Title: title})
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
	entries := make([]wiki.BacklinkEntry, 0)
	for slug, title := range titles {
		if ns, rest := wiki.NamespaceFor(slug); ns == namespace && rest != "" {
			entries = append(entries, wiki.BacklinkEntry{Slug: slug, Title: title})
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

func (app *App) handleNamespacesGet(w http.ResponseWriter, r *http.Request) {
	data := app.namespaceManagementData(r, "", "")
	if r.URL.Query().Get("saved") == "1" {
		data.Flash = i18n.T("web.settings.flash_namespace_saved")
	}
	app.render(w, r, http.StatusOK, "namespaces", TemplateData{Authed: true, Title: "Namespaces", StatusMode: "settings", NamespaceManagement: &data})
}

func (app *App) handleNamespaceNewGet(w http.ResponseWriter, r *http.Request) {
	data := app.namespaceManagementData(r, "", "")
	data.Form = NamespaceListEntry{Template: wiki.DefaultNewPageTemplate, SlugPreset: presentation.SlugPresets[0].Key}
	data.Section = "general"
	app.render(w, r, http.StatusOK, "namespace-edit", TemplateData{Authed: true, Title: i18n.T("web.settings.title_new_namespace"), StatusMode: "settings", NamespaceManagement: &data})
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
	section, ok := namespaceSectionByKey(r.PathValue("section"))
	if !ok || r.PathValue("section") == "general" {
		app.notFound(w, r)
		return
	}
	data := app.namespaceManagementData(r, name, "")
	if data.Form.Name == "" {
		app.notFound(w, r)
		return
	}
	data.Section = section.Key
	data.Sections = localizedNamespaceSections()
	if r.URL.Query().Get("saved") == "1" {
		data.Flash = i18n.T("web.settings.flash_namespace_saved")
	}
	app.render(w, r, http.StatusOK, "namespace-edit", TemplateData{Authed: true, Title: i18n.T("web.settings.namespace_title", "Name", name), StatusMode: "settings", NamespaceManagement: &data})
}

// namespaceSection is one page of the namespace editor. The editor keeps a
// single form, so saving any section posts the whole namespace configuration.
type namespaceSection struct {
	Key, Label, Path string // Path follows /_/settings/namespaces/{name}/edit
}

var namespaceSections = []namespaceSection{
	{"general", "web.settings.namespace_section.general", ""},
	{"published", "web.settings.namespace_section.published", "/published"},
	{"static-site", "web.settings.namespace_section.static_site", "/static-site"},
	{"widgets", "web.settings.namespace_section.widgets", "/widgets"},
}

// namespaceSectionByKey resolves an editor section; an empty key is general.
func namespaceSectionByKey(key string) (namespaceSection, bool) {
	if key == "" {
		key = "general"
	}
	for _, s := range namespaceSections {
		if s.Key == key {
			return s, true
		}
	}
	return namespaceSection{}, false
}

// localizedNamespaceSections returns the editor pages with their labels translated for this request.
func localizedNamespaceSections() []namespaceSection {
	sections := make([]namespaceSection, len(namespaceSections))
	for i, s := range namespaceSections {
		s.Label = i18n.T(s.Label)
		sections[i] = s
	}
	return sections
}
