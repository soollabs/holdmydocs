package web

import (
	"bytes"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"hmd/internal/api"
	"hmd/internal/store"
	"hmd/internal/wiki"
)

// TestStaticDirectoriesAreNotListed ensures static directories return 404.
func TestStaticDirectoriesAreNotListed(t *testing.T) {
	_, server, client := newTestAppFull(t)
	defer server.Close()

	for _, path := range []string{"/_/static/", "/_/static/fonts/"} {
		resp, err := client.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		closeTestBody(t, resp.Body)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, resp.StatusCode)
		}
	}
}

func TestMalformedPagePathsAreNotInternalErrors(t *testing.T) {
	_, server, client := newTestAppFull(t)
	defer server.Close()

	for _, path := range []string{"/notes/%00", "/notes/.git/config"} {
		resp, err := client.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		closeTestBody(t, resp.Body)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, resp.StatusCode)
		}
	}
}

func TestSecurityHeadersPresent(t *testing.T) {
	_, server, client := newTestAppFull(t)
	defer server.Close()

	resp, err := client.Get(server.URL + "/_/login")
	if err != nil {
		t.Fatalf("GET /_/login: %v", err)
	}
	closeTestBody(t, resp.Body)

	if got := resp.Header.Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", got)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := resp.Header.Get("Referrer-Policy"); got != "same-origin" {
		t.Errorf("Referrer-Policy = %q, want same-origin", got)
	}
}

// TestDoDispatchPerAction exercises each page action on the page's own URL.
func TestDoDispatchPerAction(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	authorName, authorEmail := app.gitAuthor("admin")
	page := wiki.Page{Slug: testNS + "/routing-target", Title: "Routing Target", Body: "hello"}
	if _, err := app.Store.Save(wiki.PageFile(page.Slug), page.Encode(), "seed", authorName, authorEmail); err != nil {
		t.Fatalf("seeding page: %v", err)
	}
	if err := app.Index.Update(page); err != nil {
		t.Fatalf("updating index: %v", err)
	}

	get := func(path string) *http.Response {
		resp, err := client.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		return resp
	}

	resp := get("/" + testNS + "/routing-target")
	body, _ := io.ReadAll(resp.Body)
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("hello")) {
		t.Errorf("view: status = %d, body = %s", resp.StatusCode, body)
	}

	resp = get("/" + testNS + "/routing-target?do=edit")
	body, _ = io.ReadAll(resp.Body)
	closeTestBody(t, resp.Body)
	basehashRe := regexp.MustCompile(`name="basehash" value="([0-9a-f]*)"`)
	m := basehashRe.FindStringSubmatch(string(body))
	if resp.StatusCode != http.StatusOK || m == nil {
		t.Fatalf("?do=edit: status = %d, body = %s", resp.StatusCode, body)
	}
	basehash := ""

	resp = get("/" + testNS + "/routing-target?do=history")
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("?do=history: status = %d", resp.StatusCode)
	}

	tagResp, err := postPageTags(t, client, server, testNS+"/routing-target", "a, b")
	if err != nil {
		t.Fatalf("POST tags: %v", err)
	}
	closeTestBody(t, tagResp.Body)
	if tagResp.StatusCode != http.StatusOK {
		t.Errorf("tags: status = %d", tagResp.StatusCode)
	}

	resp = get("/" + testNS + "/routing-target?do=edit")
	body, _ = io.ReadAll(resp.Body)
	closeTestBody(t, resp.Body)
	m = basehashRe.FindStringSubmatch(string(body))
	if m == nil {
		t.Fatalf("?do=edit (2nd fetch): no basehash found in body = %s", body)
	}
	basehash = m[1]

	saveResp, err := postPageSave(t, client, server, testNS+"/routing-target", url.Values{
		"title": {"Routing Target"}, "body": {"hello v2"}, "basehash": {basehash},
	})
	if err != nil {
		t.Fatalf("POST save: %v", err)
	}
	closeTestBody(t, saveResp.Body)
	if saveResp.StatusCode != http.StatusOK {
		t.Fatalf("save: status = %d, want 200", saveResp.StatusCode)
	}

	histResp := get("/" + testNS + "/routing-target?do=history")
	histBody, _ := io.ReadAll(histResp.Body)
	closeTestBody(t, histResp.Body)
	// history.html renders rev links as ?do=rev&hash=...; html/template escapes
	// the "&" to "&amp;" as any HTML attribute value would be, so unescape
	// before matching, same as a browser does before navigating.
	hashes := regexp.MustCompile(`\?do=rev&hash=([0-9a-f]+)`).FindAllStringSubmatch(html.UnescapeString(string(histBody)), -1)
	if len(hashes) < 2 {
		t.Fatalf("expected at least 2 revisions in history, got %d: %s", len(hashes), histBody)
	}
	hashA, hashB := hashes[0][1], hashes[1][1]

	resp = get("/" + testNS + "/routing-target?do=diff&a=" + hashA + "&b=" + hashB)
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("?do=diff: status = %d", resp.StatusCode)
	}

	resp = get("/" + testNS + "/routing-target?do=rev&hash=" + hashA)
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("?do=rev: status = %d", resp.StatusCode)
	}

	revertResp, err := postPageRevert(t, client, server, testNS+"/routing-target", hashA)
	if err != nil {
		t.Fatalf("POST revert: %v", err)
	}
	closeTestBody(t, revertResp.Body)
	if revertResp.StatusCode != http.StatusOK {
		t.Errorf("revert: status = %d, want 200", revertResp.StatusCode)
	}

	renameResp, err := postPageRename(t, client, server, testNS+"/routing-target", "Routing Target Renamed")
	if err != nil {
		t.Fatalf("POST rename: %v", err)
	}
	closeTestBody(t, renameResp.Body)
	if renameResp.StatusCode != http.StatusOK {
		t.Errorf("rename: status = %d, want 200", renameResp.StatusCode)
	}

	deleteResp, err := postJSON(t, client, server.URL+"/_/api/pages/delete/"+testNS+"/routing-target-renamed", map[string]any{"hidden": false})
	if err != nil {
		t.Fatalf("POST delete: %v", err)
	}
	closeTestBody(t, deleteResp.Body)
	if deleteResp.StatusCode != http.StatusOK {
		t.Errorf("delete: status = %d, want 200", deleteResp.StatusCode)
	}
	if app.Index.Exists(testNS + "/routing-target-renamed") {
		t.Error("delete: page still in search index")
	}
	resp = get("/" + testNS + "/routing-target-renamed")
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("view after delete: status = %d, want 404", resp.StatusCode)
	}
}

func TestNewPageCanChooseFilename(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	resp, err := postPageSave(t, client, server, "ai/new", url.Values{
		"new_slug": {"ai/actual-name"},
		"title":    {"Actual name"},
		"body":     {"content"},
	})
	if err != nil {
		t.Fatalf("POST new page: %v", err)
	}
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save status = %d, want 200", resp.StatusCode)
	}
	if _, _, err := app.Store.Read(wiki.PageFile("ai/actual-name")); err != nil {
		t.Fatalf("reading chosen filename: %v", err)
	}
	if _, _, err := app.Store.Read(wiki.PageFile("ai/new")); err == nil {
		t.Fatal("placeholder filename should not remain")
	}
}

// TestEditMovesPageBetweenNamespaces ensures edits can move pages between namespaces.
func TestEditMovesPageBetweenNamespaces(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	nsCfg, err := wiki.NamespaceConfig{Index: store.DefaultIndexPage}.Encode()
	if err != nil {
		t.Fatalf("encoding namespace config: %v", err)
	}
	if _, err := app.Store.Save(wiki.NamespaceConfigPath("archive"), nsCfg, "Add archive", "test", "test@hmd.local"); err != nil {
		t.Fatalf("seeding archive namespace: %v", err)
	}
	reg, err := api.BuildNamespaceRegistry(app.config().RepoDir)
	if err != nil {
		t.Fatalf("api.BuildNamespaceRegistry: %v", err)
	}
	app.SetNamespaces(reg)

	hash, err := app.Store.Save(wiki.PageFile(testNS+"/mover"), wiki.Page{Slug: testNS + "/mover", Title: "Mover", Body: "body"}.Encode(), "Add mover", "test", "test@hmd.local")
	if err != nil {
		t.Fatalf("seeding page: %v", err)
	}
	if err := app.Index.Update(wiki.Page{Slug: testNS + "/mover", Title: "Mover", Body: "body"}); err != nil {
		t.Fatalf("indexing page: %v", err)
	}

	save := func(newSlug, basehash string) *http.Response {
		t.Helper()
		resp, err := postPageSave(t, client, server, testNS+"/mover", url.Values{
			"new_slug": {newSlug},
			"title":    {"Mover"},
			"body":     {"body"},
			"basehash": {basehash},
		})
		if err != nil {
			t.Fatalf("POST save: %v", err)
		}
		closeTestBody(t, resp.Body)
		return resp
	}

	if resp := save("nope/mover", hash); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("move to unknown namespace: status = %d, want 400", resp.StatusCode)
	}

	resp := save("archive/mover", hash)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("move: status = %d, want 200", resp.StatusCode)
	}
	if _, _, err := app.Store.Read(wiki.PageFile("archive/mover")); err != nil {
		t.Fatalf("reading moved page: %v", err)
	}
	if _, _, err := app.Store.Read(wiki.PageFile(testNS + "/mover")); err == nil {
		t.Fatal("page should not remain at its source path")
	}
	if app.Index.Exists(testNS + "/mover") {
		t.Error("source slug still in search index")
	}
	if !app.Index.Exists("archive/mover") {
		t.Error("new slug missing from search index")
	}
}

// TestUnrecognisedDoValue404s asserts a typoed ?do= value doesn't silently fall back to view.
func TestUnrecognisedDoValue404s(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	resp, err := client.Get(server.URL + "/" + testHome + "?do=bogus")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("?do=bogus: status = %d, want 404", resp.StatusCode)
	}
}

// TestNotFoundRendersInAppChrome verifies 404 responses use app chrome and do not reveal private-page existence.
func TestNotFoundRendersInAppChrome(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	resp, err := client.Get(server.URL + "/" + testNS + "/?do=edit")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(string(body), "app-topbar") {
		t.Error("404 body has no app chrome")
	}

	seedPage(t, app, wiki.Page{Slug: testNS + "/secret", Title: "Secret", Body: "shh"})
	anon := noAuthClient()
	get := func(path string) string {
		t.Helper()
		resp, err := anon.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		b, _ := io.ReadAll(resp.Body)
		closeTestBody(t, resp.Body)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s: status = %d, want 404", path, resp.StatusCode)
		}
		return string(b)
	}
	if private, missing := get("/"+testNS+"/secret"), get("/"+testNS+"/no-such-page"); private != missing {
		t.Error("private and missing pages produce different 404s — existence oracle")
	}
}

// TestPageNamedEditIsReachable checks a page literally titled "edit" is viewable at its own URL, since "do"
// can never be part of the path.
func TestPageNamedEditIsReachable(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	authorName, authorEmail := app.gitAuthor("admin")
	page := wiki.Page{Slug: testNS + "/edit", Title: "edit", Body: "a page named edit"}
	if _, err := app.Store.Save(wiki.PageFile(page.Slug), page.Encode(), "seed", authorName, authorEmail); err != nil {
		t.Fatalf("seeding page: %v", err)
	}
	if err := app.Index.Update(page); err != nil {
		t.Fatalf("updating index: %v", err)
	}

	resp, err := client.Get(server.URL + "/" + testNS + "/edit")
	if err != nil {
		t.Fatalf("GET /%s/edit: %v", testNS, err)
	}
	defer closeTestBody(t, resp.Body)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("a page named edit")) {
		t.Errorf("GET /%s/edit: status = %d, body = %s", testNS, resp.StatusCode, body)
	}
}

// TestUnderscoreRouteNeverResolvesToContent checks a nonexistent /_/ route 404s via the mux, rather than
// being treated as a page slug "_/whatever" and rendered as the "create this page" prompt.
func TestUnderscoreRouteNeverResolvesToContent(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	resp, err := client.Get(server.URL + "/_/nonexistent-app-route")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	if bytes.Contains(body, []byte("Create this page")) {
		t.Errorf("a nonexistent /_/ route must not render the page-creation prompt: %s", body)
	}
}

var hrefRe = regexp.MustCompile(`href="([^"]*)"`)

// TestNoBrokenLinksSmoke ensures representative authenticated pages contain no broken links.
func TestNoBrokenLinksSmoke(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	authorName, authorEmail := app.gitAuthor("admin")
	page := wiki.Page{Slug: testNS + "/smoke-page", Title: "Smoke Page", Tags: []string{"smoke"}, Body: "links to [[notes]]"}
	if _, err := app.Store.Save(wiki.PageFile(page.Slug), page.Encode(), "seed", authorName, authorEmail); err != nil {
		t.Fatalf("seeding page: %v", err)
	}
	if err := app.Index.Update(page); err != nil {
		t.Fatalf("updating index: %v", err)
	}

	pages := []string{"/" + testHome, "/" + testNS + "/smoke-page", "/_/tags"}

	seen := make(map[string]bool)
	for _, p := range pages {
		resp, err := client.Get(server.URL + p)
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		body, _ := io.ReadAll(resp.Body)
		closeTestBody(t, resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: status = %d", p, resp.StatusCode)
		}

		for _, m := range hrefRe.FindAllStringSubmatch(string(body), -1) {
			href := html.UnescapeString(m[1])
			if href == "" || href == "#" || strings.HasPrefix(href, "#") ||
				strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") ||
				strings.HasPrefix(href, "mailto:") || strings.HasPrefix(href, "data:") ||
				strings.HasPrefix(href, "javascript:") {
				continue
			}
			if seen[href] {
				continue
			}
			seen[href] = true

			checkResp, err := client.Get(server.URL + href)
			if err != nil {
				t.Errorf("GET %s (linked from %s): %v", href, p, err)
				continue
			}
			closeTestBody(t, checkResp.Body)
			if checkResp.StatusCode == http.StatusNotFound {
				t.Errorf("GET %s (linked from %s): 404", href, p)
			}
		}
	}
}
