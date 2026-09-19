package web

import (
	"html"
	"html/template"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"hmd/internal/api"
	"hmd/internal/auth"
	"hmd/internal/config"
	"hmd/internal/presentation"
	"hmd/internal/search"
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
}

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
	UserGitAuthor    string             // current user's per-user git author override
	WikiConfigPath   string             // repository-relative .wiki.yaml path
	WikiLanding      string             // where "/" redirects to
	WikiSiteName     string             // title shared by every clone of the wiki
	Tokens           []TokenView        // current user's personal access tokens
	TokenNamespaces  []string           // selectable namespace names for new tokens
	NewToken         string             // freshly minted token value, shown exactly once
	TokenError       string             // token create/revoke validation error
	HasEnvOverrides  bool               // any field currently sourced from an env var — shows the "export to file" action
	ExportSecretVars []string           // secret environment variables excluded from export
	Users            []auth.UserSummary // every user, for the users tab
	AllScopes        []string           // "read", "write", "settings" — the scope checkbox options
	CurrentUser      string             // name of the logged-in user, so the users tab can block self-lockout
	UserError        string             // create/scope-update validation error
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

func buildSettingsData(cfg config.Config, prefs auth.UserRecord) SettingsData {
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

func exportSecretVars(cfg config.Config) []string {
	return nil
}

func (app *App) settingsData(r *http.Request) SettingsData {
	cfg := app.config()
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
		effectiveScopes := auth.EffectiveTokenScopes(prefs.Scopes, t.Scopes)
		isAdmin := auth.UserRecord{Scopes: effectiveScopes}.HasScope(auth.ScopeSettings)
		namespaceLabel := "All namespaces"
		if isAdmin {
			namespaceLabel = "Administrator"
		} else if len(t.Namespaces) > 0 {
			namespaceLabel = strings.Join(t.Namespaces, ", ") + " only"
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

func (app *App) namespaceManagementData(r *http.Request, name, errMsg string) NamespaceManagementData {
	user := app.currentUser(r)
	prefs := app.Auth.Prefs(user)
	data := NamespaceManagementData{
		CanWrite:     prefs.HasScope(auth.ScopeWrite),
		CanSettings:  prefs.HasScope(auth.ScopeSettings),
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
			}
			data.Form = entry
			data.TreeEditor = namespaceTreeEditor(app.apiClient().PageTitles(), entry.Name, entry.Index, entry.Tree)
			break
		}
	}
	return data
}

func namespaceTreeItems(titles map[string]string, namespace, index string, tree []string) []NamespaceTreeItem {
	entries := make([]search.BacklinkEntry, 0)
	for slug, title := range titles {
		if ns, rest := wiki.NamespaceFor(slug); ns == namespace && rest != "" {
			entries = append(entries, search.BacklinkEntry{Slug: slug, Title: title})
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
	entries := make([]search.BacklinkEntry, 0)
	for slug, title := range titles {
		if ns, rest := wiki.NamespaceFor(slug); ns == namespace && rest != "" {
			entries = append(entries, search.BacklinkEntry{Slug: slug, Title: title})
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
		data.Flash = "Namespace settings saved"
	}
	app.render(w, r, http.StatusOK, "namespaces", TemplateData{Authed: true, Title: "Namespaces", StatusMode: "settings", NamespaceManagement: &data})
}

func (app *App) handleNamespaceNewGet(w http.ResponseWriter, r *http.Request) {
	data := app.namespaceManagementData(r, "", "")
	data.Form = NamespaceListEntry{Template: wiki.DefaultNewPageTemplate, SlugPreset: presentation.SlugPresets[0].Key}
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
