package main

import (
	"bytes"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// TestSecurityHeadersPresent checks the clickjacking/MIME-sniffing headers
// land on every response, including ones auth denies before reaching a route.
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
	if got := resp.Header.Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", got)
	}
}

// TestDoDispatchPerAction exercises every ?do= action against a real page's
// own URL, replacing the old path-suffix routes.
func TestDoDispatchPerAction(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	authorName, authorEmail := app.gitAuthor("admin")
	page := Page{Slug: testNS + "/routing-target", Title: "Routing Target", Body: "hello"}
	if _, err := app.Store.Save(pageFile(page.Slug), page.Encode(), "seed", authorName, authorEmail); err != nil {
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

	// view (no do=)
	resp := get("/" + testNS + "/routing-target")
	body, _ := io.ReadAll(resp.Body)
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("hello")) {
		t.Errorf("view: status = %d, body = %s", resp.StatusCode, body)
	}

	// ?do=edit
	resp = get("/" + testNS + "/routing-target?do=edit")
	body, _ = io.ReadAll(resp.Body)
	closeTestBody(t, resp.Body)
	basehashRe := regexp.MustCompile(`name="basehash" value="([0-9a-f]*)"`)
	m := basehashRe.FindStringSubmatch(string(body))
	if resp.StatusCode != http.StatusOK || m == nil {
		t.Fatalf("?do=edit: status = %d, body = %s", resp.StatusCode, body)
	}
	basehash := m[1]

	// ?do=history
	resp = get("/" + testNS + "/routing-target?do=history")
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("?do=history: status = %d", resp.StatusCode)
	}

	// POST ?do=tags
	tagResp, err := client.PostForm(server.URL+"/"+testNS+"/routing-target?do=tags", url.Values{"tags": {"a, b"}})
	if err != nil {
		t.Fatalf("POST ?do=tags: %v", err)
	}
	closeTestBody(t, tagResp.Body)
	if tagResp.StatusCode != http.StatusOK {
		t.Errorf("?do=tags: status = %d", tagResp.StatusCode)
	}

	// ?do=tags just saved a new revision, so basehash is stale — re-fetch it.
	resp = get("/" + testNS + "/routing-target?do=edit")
	body, _ = io.ReadAll(resp.Body)
	closeTestBody(t, resp.Body)
	m = basehashRe.FindStringSubmatch(string(body))
	if m == nil {
		t.Fatalf("?do=edit (2nd fetch): no basehash found in body = %s", body)
	}
	basehash = m[1]

	// ?do=diff needs two real hashes: save once more, then diff head against itself's prior hash.
	saveResp, err := client.PostForm(server.URL+"/"+testNS+"/routing-target?do=save", url.Values{
		"title": {"Routing Target"}, "body": {"hello v2"}, "basehash": {basehash},
	})
	if err != nil {
		t.Fatalf("POST ?do=save: %v", err)
	}
	closeTestBody(t, saveResp.Body)
	if saveResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("?do=save: status = %d, want 303", saveResp.StatusCode)
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

	// ?do=rev&hash=
	resp = get("/" + testNS + "/routing-target?do=rev&hash=" + hashA)
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("?do=rev: status = %d", resp.StatusCode)
	}

	// POST ?do=revert
	revertResp, err := client.PostForm(server.URL+"/"+testNS+"/routing-target?do=revert", url.Values{"hash": {hashA}})
	if err != nil {
		t.Fatalf("POST ?do=revert: %v", err)
	}
	closeTestBody(t, revertResp.Body)
	if revertResp.StatusCode != http.StatusSeeOther {
		t.Errorf("?do=revert: status = %d, want 303", revertResp.StatusCode)
	}

	// POST ?do=rename
	renameResp, err := client.PostForm(server.URL+"/"+testNS+"/routing-target?do=rename", url.Values{"title": {"Routing Target Renamed"}})
	if err != nil {
		t.Fatalf("POST ?do=rename: %v", err)
	}
	closeTestBody(t, renameResp.Body)
	if renameResp.StatusCode != http.StatusOK {
		t.Errorf("?do=rename: status = %d, want 200", renameResp.StatusCode)
	}

	// POST ?do=delete, on the renamed slug.
	deleteResp, err := client.Post(server.URL+"/"+testNS+"/routing-target-renamed?do=delete", "", nil)
	if err != nil {
		t.Fatalf("POST ?do=delete: %v", err)
	}
	closeTestBody(t, deleteResp.Body)
	if deleteResp.StatusCode != http.StatusSeeOther {
		t.Errorf("?do=delete: status = %d, want 303", deleteResp.StatusCode)
	}
	if app.Index.Exists(testNS + "/routing-target-renamed") {
		t.Error("?do=delete: page still in search index")
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

	resp, err := client.PostForm(server.URL+"/ai/new?do=save", url.Values{
		"new_slug": {"ai/actual-name"},
		"title":    {"Actual name"},
		"body":     {"content"},
	})
	if err != nil {
		t.Fatalf("POST new page: %v", err)
	}
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ai/actual-name" {
		t.Fatalf("save redirect = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if _, _, err := app.Store.Read(pageFile("ai/actual-name")); err != nil {
		t.Fatalf("reading chosen filename: %v", err)
	}
	if _, _, err := app.Store.Read(pageFile("ai/new")); err == nil {
		t.Fatal("placeholder filename should not remain")
	}
}

// TestUnrecognisedDoValue404s asserts a typoed ?do= value doesn't silently
// fall back to view.
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

// TestPageNamedEditIsReachable checks a page literally titled "edit" is
// viewable at its own URL, since "do" can never be part of the path.
func TestPageNamedEditIsReachable(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	authorName, authorEmail := app.gitAuthor("admin")
	page := Page{Slug: testNS + "/edit", Title: "edit", Body: "a page named edit"}
	if _, err := app.Store.Save(pageFile(page.Slug), page.Encode(), "seed", authorName, authorEmail); err != nil {
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

// TestUnderscoreRouteNeverResolvesToContent checks a nonexistent /_/ route
// 404s via the mux, rather than being treated as a page slug "_/whatever"
// and rendered as the "create this page" prompt.
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

// hrefRe extracts href="..." attribute values from rendered HTML. Form
// actions are deliberately excluded — this is a GET-only check, and every
// action="..." in the app is a POST target.
var hrefRe = regexp.MustCompile(`href="([^"]*)"`)

// TestNoBrokenLinksSmoke walks a handful of authenticated pages, extracts
// every internal link, and asserts none of them 404 — a regression net for
// the URL rewrite in this step, not an exhaustive crawler. Pages are chosen
// to be pure content plus one static index (tags): settings/admin are
// excluded because they legitimately link to speculative targets (calendar
// preview days with no page yet) that 404 by design, not by breakage.
func TestNoBrokenLinksSmoke(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	authorName, authorEmail := app.gitAuthor("admin")
	page := Page{Slug: testNS + "/smoke-page", Title: "Smoke Page", Tags: []string{"smoke"}, Body: "links to [[notes]]"}
	if _, err := app.Store.Save(pageFile(page.Slug), page.Encode(), "seed", authorName, authorEmail); err != nil {
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
