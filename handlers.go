package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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

// version is shown on the login screen intro line.
const version = "dev"

type App struct {
	cfg    atomic.Pointer[Config]
	Store  *Store
	Auth   *Auth
	Index  *Index
	Render *Renderer
	Tmpl   map[string]*template.Template
}

// config returns the current configuration value.
func (app *App) config() Config {
	return *app.cfg.Load()
}

// SetConfig stores a new configuration value atomically.
func (app *App) SetConfig(cfg Config) {
	app.cfg.Store(&cfg)
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
	SearchElapsed    string // search timing, e.g. "3ms"
	SearchPages      int    // total pages, for search stats
	SearchHits       int    // match count, for search stats
	Hostname         string // shell prompt host segment
	PathLabel        string // shell prompt path segment
	UserLabel        string // overrides Username in prompt when non-empty
	ShowTagsSidebar  bool
	SyncPollMs       int // injected as a JS global for sync polling
	SyncMode         string
	BlobHash         string // current page blob hash, for client-side change detection
	ThemeStyle       template.CSS
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
	Fields          map[string]FieldState
	NoConfigFile    bool
	ConfigPath      string
	Flash           string // success message after a save
	Error           string // validation error
	MaxUploadBytes  int64
	SyncPollMs      int
	ShowTagsSidebar bool
	SyncMode        string
	ThemeDark       map[string]string
	ThemeLight      map[string]string
	HelpDrifted     bool
	UserGitAuthor   string // current user's per-user git author override
	HomeFilename    string // read-only display; restart required to change
	HomeFilenameEnv string // env var name if it overrides the file, else ""
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
func buildSettingsData(cfg Config, fc fileConfig, configPath string, hasConfigFile bool) SettingsData {
	fields := make(map[string]FieldState)

	// envLocked returns the env var name if set, else "".
	envLocked := func(envKey string) string {
		if os.Getenv(envKey) != "" {
			return envKey
		}
		return ""
	}

	tokenFileLocked := os.Getenv("HMD_GIT_TOKEN_FILE") != "" || fc.GitTokenFile != ""

	// Helper: editable = hasConfigFile && no env var && not bootstrap-only
	mkField := func(value, envKey string, restartRequired, bootstrapOnly bool) FieldState {
		envVar := ""
		if !bootstrapOnly {
			envVar = envLocked(envKey)
		}
		editable := hasConfigFile && envVar == "" && !bootstrapOnly
		return FieldState{
			Value:           value,
			Editable:        editable,
			RestartRequired: restartRequired,
			EnvVar:          envVar,
			BootstrapOnly:   bootstrapOnly,
		}
	}

	fields["Bind"] = mkField(cfg.Bind, "HMD_BIND", true, false)
	fields["RepoDir"] = mkField(cfg.RepoDir, "HMD_REPO_DIR", true, false)
	fields["AppDir"] = mkField(cfg.AppDir, "HMD_APP_DIR", true, false)
	fields["RemoteURL"] = mkField(cfg.RemoteURL, "HMD_REMOTE_URL", false, false)
	fields["GitUser"] = mkField(cfg.GitUser, "HMD_GIT_USER", false, false)
	fields["GitAuthor"] = mkField(cfg.GitAuthor, "HMD_GIT_AUTHOR", false, false)

	// Token is special: masked, and locked if a token file is configured.
	tokenValue := "not set"
	if cfg.GitToken != "" {
		tokenValue = "set"
	}
	tokenEnvVar := envLocked("HMD_GIT_TOKEN")
	if tokenFileLocked && tokenEnvVar == "" {
		tokenEnvVar = "HMD_GIT_TOKEN_FILE"
	}
	tokenEditable := hasConfigFile && tokenEnvVar == ""
	fields["GitToken"] = FieldState{
		Value:           tokenValue,
		Editable:        tokenEditable,
		RestartRequired: false,
		EnvVar:          tokenEnvVar,
		BootstrapOnly:   false,
	}

	fields["SiteName"] = mkField(cfg.SiteName, "HMD_SITE_NAME", false, false)
	fields["AdminUser"] = mkField(cfg.AdminUser, "HMD_ADMIN_USER", false, true)
	fields["AdminPass"] = mkField(cfg.AdminPass, "HMD_ADMIN_PASSWORD", false, true)
	fields["Hostname"] = mkField(cfg.Hostname, "HMD_HOSTNAME", false, false)
	fields["PathLabel"] = mkField(cfg.PathLabel, "HMD_PATH_LABEL", false, false)
	fields["UserLabel"] = mkField(cfg.UserLabel, "HMD_USER_LABEL", false, false)

	fields["SyncMode"] = mkField(cfg.SyncMode, "HMD_SYNC_MODE", false, false)

	return SettingsData{
		Fields:          fields,
		NoConfigFile:    !hasConfigFile,
		ConfigPath:      configPath,
		MaxUploadBytes:  cfg.MaxUploadBytes,
		SyncPollMs:      cfg.SyncPollMs,
		ShowTagsSidebar: cfg.ShowTagsSidebar,
		SyncMode:        cfg.SyncMode,
		ThemeDark:       mergeTheme(defaultDark, cfg.ThemeDark),
		ThemeLight:      mergeTheme(defaultLight, cfg.ThemeLight),
		HomeFilename:    cfg.HomeFilename,
		HomeFilenameEnv: envLocked("HMD_HOME_FILENAME"),
	}
}

// render executes the named page template inside the shared layout.
// Every template set was parsed from base.html plus one content template.
func (app *App) render(w http.ResponseWriter, r *http.Request, status int, name string, data TemplateData) {
	data.SiteName = app.config().SiteName
	data.Version = version
	data.RemoteHost = remoteHost(app.config().RemoteURL)
	if data.RoutePrefix == "" {
		data.RoutePrefix = "/page"
	}
	if data.SyncState == "" {
		data.SyncState, _ = app.Store.SyncState()
	}
	if data.Authed && data.Username == "" {
		data.Username = app.currentUser(r)
	}
	if data.Authed && data.AllTags == nil {
		data.AllTags = app.Index.Tags()
	}
	if data.Authed && data.StatusMode == "" {
		data.StatusMode = "view"
	}
	if data.Authed {
		cfg := app.config()
		data.Hostname = cfg.Hostname
		data.PathLabel = cfg.PathLabel
		data.UserLabel = cfg.UserLabel
		data.ShowTagsSidebar = cfg.ShowTagsSidebar
		data.SyncPollMs = cfg.SyncPollMs
		data.SyncMode = cfg.SyncMode
		data.ThemeStyle = buildThemeStyle(cfg)
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
	buf.WriteTo(w)
}

func (app *App) currentUser(r *http.Request) string {
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
		raw = app.config().GitAuthor
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

func (app *App) Routes() http.Handler {
	mux := http.NewServeMux()

	// Root: redirect to home page (named by HMD_HOME_FILENAME, default README.md)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/page/"+app.config().HomeSlug(), http.StatusSeeOther)
	})

	// Setup endpoint: seeds the home file + .help.md, clears the setup flag
	mux.HandleFunc("POST /setup", app.handleSetup)

	// Static files. embed.FS carries no real mtime/ETag, so browsers have
	// nothing to conditionally revalidate against and can cache a stale
	// copy indefinitely across binary rebuilds — force revalidation instead.
	fsys, _ := fs.Sub(webFS, "web/static")
	staticHandler := http.StripPrefix("/static/", http.FileServerFS(fsys))
	mux.Handle("GET /static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		staticHandler.ServeHTTP(w, r)
	}))

	// Login handlers
	mux.HandleFunc("GET /login", app.handleLoginGet)
	mux.HandleFunc("POST /login", app.handleLoginPost)
	mux.HandleFunc("POST /logout", app.handleLogout)

	// Tags
	mux.HandleFunc("GET /tags", app.handleTagsIndex)
	mux.HandleFunc("GET /tags/{tag}", app.handleTagPages)

	// Settings
	mux.HandleFunc("GET /settings", app.handleSettingsGet)
	mux.HandleFunc("POST /settings", app.handleSettingsPost)
	mux.HandleFunc("POST /settings/author", app.handleSetAuthor)
	mux.HandleFunc("POST /settings/setup", app.handleRerunSetup)
	mux.HandleFunc("POST /settings/help/reset", app.handleResetHelp)

	// Search
	mux.HandleFunc("GET /search", app.handleSearch)

	// Page handlers
	mux.HandleFunc("GET /page/{slug}", app.handleViewPage)
	mux.HandleFunc("GET /page/{slug}/edit", app.handleEditPage)
	mux.HandleFunc("POST /page/{slug}/save", app.handleSavePage)
	mux.HandleFunc("GET /page/{slug}/history", app.handleHistory)
	mux.HandleFunc("GET /page/{slug}/rev/{hash}", app.handleViewRev)
	mux.HandleFunc("POST /page/{slug}/revert", app.handleRevert)

	// Hidden page handlers (dot-prefixed files, separate route namespace)
	mux.HandleFunc("GET /hidden", app.handleHiddenIndex)
	mux.HandleFunc("GET /hidden/{slug}", app.handleViewHidden)
	mux.HandleFunc("GET /hidden/{slug}/edit", app.handleEditHidden)
	mux.HandleFunc("POST /hidden/{slug}/save", app.handleSaveHidden)

	// API endpoints
	mux.HandleFunc("GET /api/search", app.handleSearchAPI)
	mux.HandleFunc("GET /api/sync", app.handleSyncAPI)
	mux.HandleFunc("POST /api/preview", app.handlePreview)
	mux.HandleFunc("POST /api/attachments/{slug}", app.handleUploadAttachment)
	mux.HandleFunc("GET /attachments/{slug}/{file}", app.handleServeAttachment)

	return mux
}

// isSecureRequest reports whether the request arrived over TLS, either
// directly or (trusting the proxy) via X-Forwarded-Proto.
// trusts X-Forwarded-Proto unconditionally, no allowed-proxy list — fine here since a
// spoofed header only affects whether the cookie is marked Secure, add an allowlist if that changes.
func isSecureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (app *App) handleLoginGet(w http.ResponseWriter, r *http.Request) {
	app.render(w, r, http.StatusOK, "login", TemplateData{Title: "Login"})
}

func (app *App) handleLoginPost(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")

	token, ok := app.Auth.Login(username, r.FormValue("password"))
	if !ok {
		slog.Warn("login failed", "username", username, "remote", r.RemoteAddr)
		app.render(w, r, http.StatusUnauthorized, "login", TemplateData{
			Title: "Login",
			Error: "Invalid username or password",
		})
		return
	}

	slog.Info("login", "username", username, "remote", r.RemoteAddr)

	http.SetCookie(w, &http.Cookie{
		Name:     "hmd_session",
		Value:    token,
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		Path:     "/",
	})

	http.Redirect(w, r, "/page/"+app.config().HomeSlug(), http.StatusSeeOther)
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

	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// handleSetup processes the setup form. Nothing is seeded without explicit
// consent: action=="add" seeds whichever of the home file / .help.md the user
// ticked (offered when missing, or always when the modal was forced open
// via "re-run setup" — ticking an existing file overwrites it); action==
// "skip" seeds nothing. Either way NeedsSetup/ForceSetup are cleared.
func (app *App) handleSetup(w http.ResponseWriter, r *http.Request) {
	action := r.FormValue("action")
	cfg := app.config()
	authorName, authorEmail := app.gitAuthor(cfg.GitUser)

	if action == "add" {
		if r.FormValue("add_home") == "on" {
			homeSlug := cfg.HomeSlug()
			content := Page{Slug: homeSlug, Title: homeSlug, Body: defaultHomeMD}.Encode()
			if _, err := app.Store.Save(cfg.HomeFilename, content, "Add "+cfg.HomeFilename, authorName, authorEmail); err != nil {
				http.Error(w, "Failed to seed home page", http.StatusInternalServerError)
				return
			}
			app.Index.Update(ParsePage(homeSlug, content))
		}
		if r.FormValue("add_help") == "on" {
			content := Page{Slug: "help", Title: "Help", Tags: []string{"meta"}, Body: defaultHelpMD}.Encode()
			if _, err := app.Store.Save(".help.md", content, "Add .help.md", authorName, authorEmail); err != nil {
				http.Error(w, "Failed to seed help guide", http.StatusInternalServerError)
				return
			}
		}
	}

	app.Store.NeedsSetup.Store(false)
	app.Store.ForceSetup.Store(false)
	http.Redirect(w, r, refererPath(r, "/page/"+cfg.HomeSlug()), http.StatusSeeOther)
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

// handleRerunSetup reopens the setup modal on demand, showing both items
// even if the home file/.help.md already exist (unlike the automatic NeedsSetup
// flag, which only shows items that are actually missing). Nothing is
// written until the form is submitted — checking an existing file's box
// overwrites it.
func (app *App) handleRerunSetup(w http.ResponseWriter, r *http.Request) {
	app.Store.ForceSetup.Store(true)
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// handleSetAuthor stores the current user's git author override ("Name <email>",
// or empty to clear and fall back to the global default).
func (app *App) handleSetAuthor(w http.ResponseWriter, r *http.Request) {
	author := strings.TrimSpace(r.FormValue("git_author"))
	if err := app.Auth.SetAuthor(app.currentUser(r), author); err != nil {
		http.Error(w, "Failed to save git author", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// handleResetHelp overwrites .help.md with the built-in default, clearing
// the drift warning on the settings page. Destructive to any local edits.
func (app *App) handleResetHelp(w http.ResponseWriter, r *http.Request) {
	authorName, authorEmail := app.gitAuthor(app.currentUser(r))
	content := Page{Slug: "help", Title: "Help", Tags: []string{"meta"}, Body: defaultHelpMD}.Encode()
	if _, err := app.Store.Save(".help.md", content, "Reset .help.md to built-in", authorName, authorEmail); err != nil {
		http.Error(w, "Failed to reset .help.md", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
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
	if history, err := app.Store.History(pageFile(slug)); err == nil {
		revisionCount = len(history)
		if len(history) > 0 {
			headAuthor = history[0].Author
			headWhen = relativeTime(history[0].When)
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
	})
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
	title := r.FormValue("title")
	body := r.FormValue("body")
	tagsInput := r.FormValue("tags")
	basehash := r.FormValue("basehash")
	hidden := r.FormValue("hidden") == "on"
	username := app.currentUser(r)

	newFile := pageFile(slug)
	newPrefix := "/page"
	if hidden {
		newFile = hiddenFile(slug)
		newPrefix = "/hidden"
	}

	cfg := app.config()
	if cfg.SyncMode == "bidirectional" && cfg.RemoteURL != "" {
		if _, err := app.Store.FetchAndFF(); err != nil {
			slog.Warn("save-time fetch", "slug", slug, "err", err)
		}
	}

	page := Page{Slug: slug, Title: title, Tags: ParseTags(tagsInput), Body: body}

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
		oldPrefix := "/page"
		if oldFile == hiddenFile(slug) {
			oldPrefix = "/hidden"
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
		app.Index.Update(page)
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
	w.Write([]byte(html))
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
		http.Error(w, "No file uploaded", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Sanitise filename: base name only, slugify name part, keep extension
	filename := filepath.Base(header.Filename)
	ext := filepath.Ext(filename)
	name := filename[:len(filename)-len(ext)]
	name = Slugify(name)

	if name == "" {
		http.Error(w, "Invalid filename", http.StatusBadRequest)
		return
	}

	// Check extension whitelist (SVG excluded — can carry scripts that execute
	// when served as image/svg+xml)
	allowedExts := map[string]bool{
		".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
		".webp": true, ".pdf": true,
	}
	if !allowedExts[strings.ToLower(ext)] {
		http.Error(w, "File type not allowed", http.StatusBadRequest)
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
		http.Error(w, "Invalid slug", http.StatusBadRequest)
		return
	}

	// Read file content
	content, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "Error reading file", http.StatusInternalServerError)
		return
	}

	// Save via store
	authorName, authorEmail := app.gitAuthor(username)
	_, err = app.Store.Save(path, content, "Add attachment "+filename, authorName, authorEmail)
	if err != nil {
		http.Error(w, "Error saving file", http.StatusInternalServerError)
		return
	}

	// Return JSON response
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]string{"url": fmt.Sprintf("/attachments/%s/%s", slug, filename)}
	json.NewEncoder(w).Encode(resp)
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
		http.Error(w, "Not found", http.StatusNotFound)
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
		results = append(results, AutocompleteResult{
			Slug:    hit.Slug,
			Title:   hit.Title,
			Snippet: hit.Snippet,
			Tags:    hit.Tags,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
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

func (app *App) handleSyncAPI(w http.ResponseWriter, r *http.Request) {
	state, detail := app.Store.SyncState()
	resp := SyncStatus{
		State:           state,
		Detail:          detail,
		At:              time.Now().Format("15:04"),
		LastSuccessUnix: app.Store.LastSyncUnix(),
	}

	cfg := app.config()
	if cfg.SyncMode == "bidirectional" && cfg.RemoteURL != "" {
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
	json.NewEncoder(w).Encode(resp)
}

func (app *App) handleHistory(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")

	// Get page to get title
	content, _, err := app.Store.Read(pageFile(slug))
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
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
	hash := r.PathValue("hash")

	// Get old version
	content, err := app.Store.FileAt(pageFile(slug), hash)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
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
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	// Save as new commit
	authorName, authorEmail := app.gitAuthor(username)
	_, err = app.Store.Save(pageFile(slug), content, "Revert "+slug+" to "+hash[:8], authorName, authorEmail)
	if err != nil {
		http.Error(w, "Error reverting", http.StatusInternalServerError)
		return
	}
	slog.Info("reverted", "slug", slug, "to", hash[:8], "author", authorName)

	// Update index
	page := ParsePage(slug, content)
	app.Index.Update(page)

	// Redirect to page
	http.Redirect(w, r, "/page/"+slug, http.StatusSeeOther)
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
		RoutePrefix: "/hidden",
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
				RoutePrefix: "/hidden",
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
		RoutePrefix: "/hidden",
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
		RoutePrefix: "/hidden",
		IsHidden:    true,
	})
}

func (app *App) handleSaveHidden(w http.ResponseWriter, r *http.Request) {
	app.handleSave(w, r, hiddenFile(r.PathValue("slug")))
}

func (app *App) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	cfg := app.config()

	configPath := os.Getenv("HMD_CONFIG_FILE")
	hasConfigFile := configPath != ""

	var fc fileConfig
	if hasConfigFile {
		fc, _ = LoadFileConfig(configPath)
	}

	sd := buildSettingsData(cfg, fc, configPath, hasConfigFile)
	sd.HelpDrifted = HelpDrifted(app.Store)
	sd.UserGitAuthor = app.Auth.AuthorFor(app.currentUser(r))

	if q := r.URL.Query().Get("saved"); q == "1" {
		sd.Flash = "Settings saved"
	}

	app.render(w, r, http.StatusOK, "settings", TemplateData{
		Authed:        true,
		Title:         "Settings",
		StatusMode:    "settings",
		StatusContext: "config",
		Settings:      &sd,
	})
}

func (app *App) handleSettingsPost(w http.ResponseWriter, r *http.Request) {
	configPath := os.Getenv("HMD_CONFIG_FILE")
	if configPath == "" {
		http.Error(w, "No config file configured (set HMD_CONFIG_FILE)", http.StatusForbidden)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	bind := r.FormValue("bind")
	repoDir := r.FormValue("repo_dir")
	appDir := r.FormValue("app_dir")
	remoteURL := r.FormValue("remote_url")
	gitUser := r.FormValue("git_user")
	gitAuthor := r.FormValue("git_author")
	gitToken := r.FormValue("git_token")
	siteName := r.FormValue("site_name")
	hostname := r.FormValue("hostname")
	pathLabel := r.FormValue("path_label")
	userLabel := r.FormValue("user_label")
	maxUploadStr := r.FormValue("max_upload_bytes")
	syncPollStr := r.FormValue("sync_poll_ms")
	showTagsSidebar := r.FormValue("show_tags_sidebar") == "on"
	syncMode := r.FormValue("sync_mode")

	if bind == "" {
		http.Error(w, "Bind cannot be empty", http.StatusBadRequest)
		return
	}
	if repoDir == "" {
		http.Error(w, "Repo directory cannot be empty", http.StatusBadRequest)
		return
	}
	if appDir == "" {
		http.Error(w, "App directory cannot be empty", http.StatusBadRequest)
		return
	}
	if gitUser == "" {
		http.Error(w, "Git user cannot be empty", http.StatusBadRequest)
		return
	}
	if siteName == "" {
		http.Error(w, "Site name cannot be empty", http.StatusBadRequest)
		return
	}
	if hostname == "" {
		http.Error(w, "Hostname cannot be empty", http.StatusBadRequest)
		return
	}
	if pathLabel == "" {
		http.Error(w, "Path label cannot be empty", http.StatusBadRequest)
		return
	}
	if remoteURL != "" && !strings.HasPrefix(remoteURL, "https://") && !strings.HasPrefix(remoteURL, "git@") {
		http.Error(w, "Remote URL must be HTTPS or git@ SSH format", http.StatusBadRequest)
		return
	}
	if syncMode != "push" && syncMode != "bidirectional" {
		http.Error(w, "Sync mode must be push or bidirectional", http.StatusBadRequest)
		return
	}

	maxUploadBytes, err := strconv.ParseInt(maxUploadStr, 10, 64)
	if err != nil || maxUploadBytes < 1 {
		http.Error(w, "Invalid upload size", http.StatusBadRequest)
		return
	}
	syncPollMs, err := strconv.Atoi(syncPollStr)
	if err != nil || syncPollMs < 100 {
		http.Error(w, "Sync poll must be at least 100ms", http.StatusBadRequest)
		return
	}

	fc, err := LoadFileConfig(configPath)
	if err != nil {
		fc = fileConfig{}
	}

	fc.Bind = bind
	fc.RepoDir = repoDir
	fc.AppDir = appDir
	fc.RemoteURL = remoteURL
	fc.GitUser = gitUser
	fc.GitAuthor = gitAuthor
	fc.SiteName = siteName
	fc.Hostname = hostname
	fc.PathLabel = pathLabel
	fc.UserLabel = userLabel
	fc.MaxUploadBytes = int64Ptr(maxUploadBytes)
	fc.SyncPollMs = intPtr(syncPollMs)
	fc.ShowTagsSidebar = boolPtr(showTagsSidebar)
	fc.SyncMode = syncMode

	if gitToken != "" {
		fc.GitToken = gitToken
	}

	fc.ThemeDark = snapshotTheme(collectTheme(r.PostForm, "theme_dark_"), defaultDark)
	fc.ThemeLight = snapshotTheme(collectTheme(r.PostForm, "theme_light_"), defaultLight)

	if err := SaveFileConfig(configPath, fc); err != nil {
		slog.Error("saving config", "err", err)
		http.Error(w, "Failed to save config", http.StatusInternalServerError)
		return
	}

	newCfg, err := LoadConfig()
	if err != nil {
		slog.Error("reloading config after save", "err", err)
		http.Error(w, "Config saved but reload failed", http.StatusInternalServerError)
		return
	}
	app.SetConfig(newCfg)

	if err := app.Store.UpdateRemote(newCfg); err != nil {
		slog.Warn("updating store remote", "err", err)
	}

	slog.Info("settings updated", "by", app.currentUser(r))
	http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
}
