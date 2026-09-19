package web

import (
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"hmd/internal/api"
	"hmd/internal/auth"
	"hmd/internal/search"
	"hmd/internal/wiki"
)

var tocToken = regexp.MustCompile(`<!-- hmd:toc(?::([a-z0-9,-]+))? -->`)

func (app *App) injectTOC(body string, indexSlug string, ns string) string {
	if !strings.Contains(body, "hmd:toc") {
		return body
	}
	titles := app.apiClient().PageTitles()
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
			for _, s := range app.apiClient().PagesForTags(tagSlugs) {
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

	if _, err := app.apiClient().ViewPage(r.Context(), slug); err == nil {
		http.Redirect(w, r, "/"+slug+"?do=edit", http.StatusSeeOther)
		return
	}

	templateSlug := wiki.NamespaceSlug(ns, nsCfg.New.Template)
	tplPage := wiki.Page{Slug: templateSlug, Title: slugRel}
	if view, err := app.apiClient().ViewHidden(r.Context(), templateSlug); err == nil {
		tplPage = wiki.Page{Slug: view.Slug, Title: view.Title, Tags: view.Tags, Body: view.Body}
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

	page := wiki.Page{Slug: view.Slug, Title: view.Title, Tags: view.Tags, Body: view.Body, Pin: view.Pin}

	if !authed {
		app.handlePublicPage(w, r, slug, page)
		return
	}

	ns, _ := wiki.NamespaceFor(slug)
	page.Body = app.injectTOC(page.Body, app.Namespaces().IndexSlug(ns), ns)
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
	backlinks := make([]search.BacklinkEntry, 0, len(backlinkSummaries))
	for _, backlink := range backlinkSummaries {
		backlinks = append(backlinks, search.BacklinkEntry{Slug: backlink.Slug, Title: backlink.Title})
	}

	var pageTags []TagChip
	for _, tag := range page.Tags {
		pageTags = append(pageTags, TagChip{Tag: tag, Slug: wiki.Slugify(tag)})
	}

	revisionCount := 0
	headAuthor, headWhen := "", ""
	var recentCommits []LogEntry
	if history, err := app.apiClient().PageHistory(r.Context(), slug); err == nil {
		revisionCount = len(history)
		if len(history) > 0 {
			headAuthor = history[0].Author
			headWhen = wiki.RelativeTime(history[0].When)
		}
		for _, c := range history[:min(3, len(history))] {
			recentCommits = append(recentCommits, LogEntry{
				Age:     wiki.RelativeTime(c.When),
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
		StatusContext: fmt.Sprintf("%d revision%s", revisionCount, wiki.Plural(revisionCount)),
		RecentCommits: recentCommits,
		IndexWarning:  indexPendingNote(r),
	})
}

// indexPendingNote reports a post-commit indexing warning carried on a
// navigation redirect. The commit succeeded; only the derived search index is

func indexPendingNote(r *http.Request) string {
	if r.URL.Query().Get("index") == "pending" {
		return "Saved. Search indexing is pending and will be reconciled automatically."
	}
	return ""
}

func (app *App) handlePublicPage(w http.ResponseWriter, r *http.Request, slug string, page wiki.Page) {
	ns := app.Namespaces()
	if !ns.IsPublic(slug) {
		app.notFound(w, r)
		return
	}

	pageNS, _ := wiki.NamespaceFor(slug)
	isPublicLink := func(s string) bool { return app.apiClient().PageExists(s) && ns.IsPublic(s) }
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
	var sidebarTreeEntries []search.BacklinkEntry
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
		ThemeStyle:         buildThemeStyle(auth.UserRecord{Palette: palette}),
		SidebarTreeNS:      sidebarTreeNS,
		SidebarTreeEntries: sidebarTreeEntries,
		// The outline widget builds its list from this page's own headings
		// client-side (toc.js) — no auth-only data or endpoint involved, so
		// it's safe for anonymous viewers same as the sidebar tree.
		RailWidgets: widgetsForSlot(slotRail, cfg.Widgets),
	})
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
		if app.apiClient().PageExists(indexSlug) {
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
		themeStyle = buildThemeStyle(auth.UserRecord{Palette: summary.Config.Palette})
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

func (app *App) handleEditPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")

	page := wiki.Page{Slug: slug, Title: slug}
	baseHash := ""

	switch view, err := app.apiClient().ViewPage(r.Context(), slug); {
	case err == nil:
		page = wiki.Page{Slug: view.Slug, Title: view.Title, Tags: view.Tags, Body: view.Body}
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

	pages := make([]search.BacklinkEntry, 0, len(summaries))
	for _, summary := range summaries {
		pages = append(pages, search.BacklinkEntry{Slug: summary.Slug, Title: summary.Title})
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
	if app.apiClient().DocumentsEnabled() && strings.TrimSpace(q) != "" {
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
		AttachmentSearchEnabled: app.apiClient().DocumentsEnabled(),
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

func (app *App) handleHealthReport(w http.ResponseWriter, r *http.Request) {
	titles := app.apiClient().PageTitles()
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
		StatusContext: fmt.Sprintf("%d missing · %d orphan%s", len(report.Missing), len(report.Orphans), wiki.Plural(len(report.Orphans))),
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

	page := wiki.Page{Slug: revision.Slug, Title: revision.Title, Tags: revision.Tags, Body: revision.Body}
	ns, _ := wiki.NamespaceFor(slug)
	page.Body = app.injectTOC(page.Body, app.Namespaces().IndexSlug(ns), ns)
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

func (app *App) handleHiddenIndex(w http.ResponseWriter, r *http.Request) {
	summaries, err := app.apiClient().HiddenPages(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pages := make([]search.BacklinkEntry, 0, len(summaries))
	for _, summary := range summaries {
		pages = append(pages, search.BacklinkEntry{Slug: summary.Slug, Title: summary.Title})
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

	page := wiki.Page{Slug: slug, Title: slug}
	baseHash := ""

	switch view, err := app.apiClient().ViewHidden(r.Context(), slug); {
	case err == nil:
		page = wiki.Page{Slug: view.Slug, Title: view.Title, Tags: view.Tags, Body: view.Body}
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
