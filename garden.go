package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"html"
	"html/template"
	"log/slog"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// gardenData is separate from TemplateData, so public templates cannot
// receive internal state.
type gardenData struct {
	GardenTitle string
	Title       string
	Content     template.HTML
	Tags        []string
	Updated     string
	Entries     []gardenEntry
}

type gardenEntry struct {
	Slug    string
	Title   string
	Snippet string
	Updated string
	when    time.Time
}

// publicPages returns every page opted in via `public: true` frontmatter,
// keyed by slug.
// O(pages) frontmatter parse per garden request; cache keyed on repo HEAD if it ever matters
func (app *App) publicPages() (map[string]Page, error) {
	paths, err := app.Store.List()
	if err != nil {
		return nil, err
	}
	pages := make(map[string]Page)
	for _, path := range paths {
		content, _, err := app.Store.Read(path)
		if err != nil {
			continue
		}
		slug := strings.TrimSuffix(path, ".md")
		page := ParsePage(slug, content)
		if page.Public {
			pages[slug] = page
		}
	}
	return pages, nil
}

// renderGarden renders a page body for the public namespace. Wiki-links to
// public pages point at /garden/{slug}; links to private or missing pages
// unwrap to plain text so no private slug is advertised. Attachment URLs are
// rewritten onto the public attachment route, which only serves attachments
// of public pages. hmd:toc tokens are NOT expanded — a TOC would leak
// private page titles.
func (app *App) renderGarden(body string, public map[string]Page) (template.HTML, error) {
	body = wikiLinkOrCodeRe.ReplaceAllStringFunc(body, func(match string) string {
		if !strings.HasPrefix(match, "[[") {
			return match // fenced/inline code — not a real link
		}
		title := match[2 : len(match)-2]
		slug := Slugify(title)
		if _, ok := public[slug]; ok {
			return fmt.Sprintf(`<a href="/garden/%s">%s</a>`, slug, html.EscapeString(title))
		}
		return html.EscapeString(title)
	})
	rendered, err := app.Render.Render(body)
	if err != nil {
		return "", err
	}
	// Catches both markdown-generated src="..." and raw HTML attributes.
	out := strings.ReplaceAll(string(rendered), `"/attachments/`, `"/garden/attachments/`)
	return template.HTML(out), nil
}

// gardenEntries builds the sorted (newest first) index entries for public.
func (app *App) gardenEntries(public map[string]Page) []gardenEntry {
	entries := make([]gardenEntry, 0, len(public))
	for slug, page := range public {
		// Snippets are plain text: unwrap [[wiki-link]] markup.
		snippet := strings.NewReplacer("[[", "", "]]", "").Replace(extractSnippet(page.Body, 40))
		e := gardenEntry{Slug: slug, Title: page.Title, Snippet: snippet}
		if h, err := app.Store.History(pageFile(slug)); err == nil && len(h) > 0 {
			e.when = h[0].When
			e.Updated = h[0].When.Format("2006-01-02")
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].when.After(entries[j].when) })
	return entries
}

// renderGardenTemplate executes one of the templates defined in garden.html,
// buffering first so a template error becomes a clean 500.
func (app *App) renderGardenTemplate(w http.ResponseWriter, name string, data gardenData) {
	var buf bytes.Buffer
	if err := app.Tmpl["garden"].ExecuteTemplate(&buf, name, data); err != nil {
		slog.Error("rendering garden template", "name", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := buf.WriteTo(w); err != nil {
		slog.Error("writing garden template", "name", name, "err", err)
	}
}

func (app *App) handleGardenIndex(w http.ResponseWriter, r *http.Request) {
	if !app.config().Garden.Enabled {
		http.NotFound(w, r)
		return
	}
	public, err := app.publicPages()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	title := app.config().Garden.Title
	app.renderGardenTemplate(w, "garden-index", gardenData{
		GardenTitle: title,
		Title:       title,
		Entries:     app.gardenEntries(public),
	})
}

func (app *App) handleGardenPage(w http.ResponseWriter, r *http.Request) {
	if !app.config().Garden.Enabled {
		http.NotFound(w, r)
		return
	}
	slug := r.PathValue("slug")
	public, err := app.publicPages()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Private and nonexistent must be byte-identical — no existence oracle.
	page, ok := public[slug]
	if !ok {
		http.NotFound(w, r)
		return
	}
	content, err := app.renderGarden(page.Body, public)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	updated := ""
	if h, err := app.Store.History(pageFile(slug)); err == nil && len(h) > 0 {
		updated = h[0].When.Format("2006-01-02")
	}
	app.renderGardenTemplate(w, "garden-page", gardenData{
		GardenTitle: app.config().Garden.Title,
		Title:       page.Title,
		Content:     content,
		Tags:        page.Tags,
		Updated:     updated,
	})
}

// handleGardenAttachment serves attachments only for public pages. Private
// pages return 404, as do nonexistent files.
func (app *App) handleGardenAttachment(w http.ResponseWriter, r *http.Request) {
	if !app.config().Garden.Enabled {
		http.NotFound(w, r)
		return
	}
	slug := r.PathValue("slug")
	public, err := app.publicPages()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if _, ok := public[slug]; !ok {
		http.NotFound(w, r)
		return
	}

	// Same path-traversal discipline as handleServeAttachment.
	cleanPath := filepath.Join("attachments", slug, r.PathValue("file"))
	repoPath := filepath.Join(app.config().RepoDir, cleanPath)
	absRepo := filepath.Join(app.config().RepoDir, "attachments")
	absPath, _ := filepath.Abs(repoPath)
	absRepoAbs, _ := filepath.Abs(absRepo)
	if !strings.HasPrefix(absPath, absRepoAbs+string(filepath.Separator)) {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, repoPath)
}

// Hand-written RSS 2.0 — no feed dependency needed for this little XML.
type rssFeed struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title       string    `xml:"title"`
	Link        string    `xml:"link"`
	Description string    `xml:"description"`
	Items       []rssItem `xml:"item"`
}

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	PubDate     string `xml:"pubDate,omitempty"`
	Description string `xml:"description"`
}

func (app *App) handleGardenFeed(w http.ResponseWriter, r *http.Request) {
	cfg := app.config()
	if !cfg.Garden.Enabled {
		http.NotFound(w, r)
		return
	}
	public, err := app.publicPages()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	base := strings.TrimRight(cfg.OIDC.BaseURL, "/")
	feed := rssFeed{
		Version: "2.0",
		Channel: rssChannel{
			Title:       cfg.Garden.Title,
			Link:        base + "/garden",
			Description: cfg.Garden.Title,
		},
	}
	for _, e := range app.gardenEntries(public) {
		item := rssItem{
			Title:       e.Title,
			Link:        base + "/garden/" + e.Slug,
			GUID:        base + "/garden/" + e.Slug,
			Description: e.Snippet,
		}
		if !e.when.IsZero() {
			item.PubDate = e.when.Format(time.RFC1123Z)
		}
		feed.Channel.Items = append(feed.Channel.Items, item)
	}

	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	if _, err := w.Write([]byte(xml.Header)); err != nil {
		slog.Error("writing garden feed header", "err", err)
		return
	}
	if err := xml.NewEncoder(w).Encode(feed); err != nil {
		slog.Error("encoding garden feed", "err", err)
	}
}
