package web

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"hmd/internal/wiki"
)

func TestLiveReadingEnhancements(t *testing.T) {
	app, server, authed := newTestAppFull(t)
	defer server.Close()
	if err := writeNamespaceConfig(t, app, "docs", "public: true\nindex: home\ntree:\n- home\n- guides\n- last\n"); err != nil {
		t.Fatal(err)
	}
	for _, page := range []wiki.Page{
		{Slug: "docs/home", Title: "Home", Body: "# Home"},
		{Slug: "docs/guides/start", Title: "Start", Body: "# Start\n\n```sh\necho hello\n```\n\n```mermaid\ngraph LR\nA-->B\n```"},
		{Slug: "docs/last", Title: "Last", Body: "Last page"},
		{Slug: "private/secret", Title: "Secret", Body: "private"},
	} {
		seedPage(t, app, page)
	}
	for _, tc := range []struct {
		name   string
		client *http.Client
	}{{"authenticated", authed}, {"anonymous", noAuthClient()}} {
		t.Run(tc.name, func(t *testing.T) {
			for _, page := range []struct{ path, prev, next string }{
				{"/docs/home", "", "/docs/guides/start"},
				{"/docs/guides/start", "/docs/home", "/docs/last"},
				{"/docs/last", "/docs/guides/start", ""},
			} {
				resp, err := tc.client.Get(server.URL + page.path)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(resp.Body)
				closeTestBody(t, resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("%s: status %d", page.path, resp.StatusCode)
				}
				s := string(body)
				if !strings.Contains(s, `src="`+staticURL("page.js")+`"`) {
					t.Errorf("%s: missing shared reader script", page.path)
				}
				for rel, href := range map[string]string{"prev": page.prev, "next": page.next} {
					if href == "" {
						if strings.Contains(s, `rel="`+rel+`"`) {
							t.Errorf("%s: unexpected %s", page.path, rel)
						}
					} else if !strings.Contains(s, `href="`+href+`" rel="`+rel+`"`) {
						t.Errorf("%s: missing %s link to %s", page.path, rel, href)
					}
				}
				if page.path == "/docs/guides/start" {
					if !strings.Contains(s, "<h1>Start</h1>") {
						t.Error("author heading removed")
					}
					if !strings.Contains(s, `src="`+staticURL("mermaid.min.js")+`"`) {
						t.Error("missing Mermaid dependency")
					}
				}
				if strings.Contains(s, "search-index.js") || strings.Contains(s, "export.js") {
					t.Error("live view loaded export-only search")
				}
				if tc.name == "anonymous" && strings.Contains(s, "/private/secret") {
					t.Error("private page leaked")
				}
			}
		})
	}
}

func TestPageNeighboursSkipsDirectoriesAndMissingPages(t *testing.T) {
	entries := []wiki.BacklinkEntry{{Slug: "docs/a", Title: "A"}, {Slug: "docs/folder/b", Title: "B"}, {Slug: "docs/c", Title: "C"}}
	tree := buildPageTree(entries, "docs", "a", []string{"folder", "c"})
	pages := orderedTreePages(tree)
	href := func(to string) string { return "/docs/" + to }
	previous, next := pageNeighbours(pages, "folder/b", href)
	if previous == nil || previous.Href != "/docs/a" || next == nil || next.Href != "/docs/c" {
		t.Fatalf("wrong neighbours: %+v %+v", previous, next)
	}
	previous, next = pageNeighbours(pages, "missing", href)
	if previous != nil || next != nil {
		t.Fatal("nonexistent page received navigation")
	}
}
