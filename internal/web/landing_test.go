package web

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestLandingRoute checks "/" redirects to the landing path: the configured
// Landing slug, which setup points at the first namespace — independent of
// skin, since landing is a portable wiki setting rather than a per-skin enum.
func TestLandingRoute(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	noRedirectClient := &http.Client{
		Jar: client.Jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	get := func() string {
		resp, err := noRedirectClient.Get(server.URL + "/")
		if err != nil {
			t.Fatalf("GET /: %v", err)
		}
		closeTestBody(t, resp.Body)
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("status = %d, want 303", resp.StatusCode)
		}
		return resp.Header.Get("Location")
	}

	if loc := get(); loc != "/"+testNS+"/" {
		t.Errorf("default Location = %q, want /%s/", loc, testNS)
	}

	app.SetWikiConfig(WikiConfig{Landing: "notes/2026-07-24"})

	if loc := get(); loc != "/notes/2026-07-24" {
		t.Errorf("configured Landing Location = %q, want /notes/2026-07-24", loc)
	}
}

// TestNewPageJSGlobals checks window.hmdNewEnabled reflects whether a
// namespace with a `new:` template is in reach, independent of skin.
func TestNewPageJSGlobals(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	get := func() string {
		resp, err := client.Get(server.URL + "/" + testHome)
		if err != nil {
			t.Fatalf("GET /%s: %v", testHome, err)
		}
		defer closeTestBody(t, resp.Body)
		body, _ := io.ReadAll(resp.Body)
		return string(body)
	}

	if body := get(); !strings.Contains(body, "hmdNewEnabled =  false") {
		t.Errorf("expected hmdNewEnabled false with no new: template, body: %s", body)
	}

	if err := writeNamespaceConfig(t, app, testNS, "new:\n  template: entry\n  slug: '{{.Now.Format \"2006-01-02\"}}'\n"); err != nil {
		t.Fatalf("writing namespace config: %v", err)
	}

	setSkin := func(name string) {
		resp, err := client.PostForm(server.URL+"/_/settings/appearance", url.Values{"skin": {name}})
		if err != nil {
			t.Fatalf("setting skin %s: %v", name, err)
		}
		closeTestBody(t, resp.Body)
	}
	for _, s := range skinNames {
		setSkin(s)
		if body := get(); !strings.Contains(body, "hmdNewEnabled =  true") {
			t.Errorf("skin=%s: expected hmdNewEnabled true once the namespace has a new: template, body: %s", s, body)
		}
	}
}

// TestNewNamespaceFollowsPage checks ctrl-j targets the namespace of the page
// being viewed when it has its own `new:` block.
func TestNewNamespaceFollowsPage(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	newBlock := "new:\n  template: template\n  slug: '{{.Now.Format \"2006-01-02\"}}'\n"
	for _, ns := range []string{"blog"} {
		if err := writeNamespaceConfig(t, app, ns, newBlock); err != nil {
			t.Fatalf("writing %s config: %v", ns, err)
		}
	}
	if _, err := app.Store.Save("blog/post.md", Page{Slug: "blog/post", Title: "Post", Body: "hi"}.Encode(), "add", "t", "t@e"); err != nil {
		t.Fatalf("saving blog page: %v", err)
	}
	if _, err := app.Store.Save("blog/folder/post.md", Page{Slug: "blog/folder/post", Title: "Nested post", Body: "hi"}.Encode(), "add", "t", "t@e"); err != nil {
		t.Fatalf("saving nested blog page: %v", err)
	}

	pageBody := func(slug string) string {
		resp, err := client.Get(server.URL + "/" + slug)
		if err != nil {
			t.Fatalf("GET /%s: %v", slug, err)
		}
		defer closeTestBody(t, resp.Body)
		body, _ := io.ReadAll(resp.Body)
		return string(body)
	}
	nsGlobal := func(slug string) string {
		body := pageBody(slug)
		for line := range strings.SplitSeq(body, "\n") {
			if strings.Contains(line, "hmdNewNamespace") {
				return strings.TrimSpace(line)
			}
		}
		return ""
	}

	if got := nsGlobal("blog/post"); !strings.Contains(got, `"blog"`) {
		t.Errorf("on a blog page, ctrl-j target = %q, want blog", got)
	}
	if got := nsGlobal(testHome); !strings.Contains(got, `""`) {
		t.Errorf("in a namespace with no new: block, ctrl-j target = %q, want none", got)
	}
	if body := pageBody("blog/folder/post"); !strings.Contains(body, `href="/blog/folder/new?do=edit"`) {
		t.Error("New does not create beside the current nested page")
	}

	resp, err := client.Get(server.URL + "/_/static/app.js")
	if err != nil {
		t.Fatalf("GET app.js: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	asset, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(asset), "new page in the ${window.hmdNewNamespace || 'current'} namespace") {
		t.Errorf("new verb description does not name its target namespace")
	}
}
