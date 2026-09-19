package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"hmd/internal/wiki"
)

// postMutation sends a JSON mutation body and returns the response. It mirrors
// the browser JavaScript calling the httpapi mutation surface.
func postMutation(t *testing.T, client *http.Client, url string, body any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshalling %s body: %v", url, err)
	}
	resp, err := client.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

// decodeMutation decodes a successful mutation result.
func decodeMutation(t *testing.T, resp *http.Response) pageMutation {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out pageMutation
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding mutation result: %v", err)
	}
	return out
}

func TestSavePageCreatesThenUpdates(t *testing.T) {
	env, client := newTestEnv(t, false)
	slug := testNS + "/mutation-page"

	created := decodeMutation(t, postMutation(t, client, env.server.URL+"/_/api/pages/"+slug, map[string]any{
		"title": "Mutation Page", "body": "first", "tags": []string{"one"},
	}))
	if created.Slug != slug || created.BlobHash == "" {
		t.Fatalf("create result = %+v, want slug and blob hash", created)
	}

	if _, _, err := env.store.Read(wiki.PageFile(slug)); err != nil {
		t.Fatalf("created page missing on disk: %v", err)
	}
	if !env.index.Exists(slug) {
		t.Error("created page should be indexed")
	}

	updated := decodeMutation(t, postMutation(t, client, env.server.URL+"/_/api/pages/"+slug, map[string]any{
		"title": "Mutation Page", "body": "second", "tags": []string{"one"}, "base_hash": created.BlobHash,
	}))
	if updated.BlobHash == created.BlobHash {
		t.Errorf("update should produce a new hash, got the same %q", updated.BlobHash)
	}
	content, _, err := env.store.Read(wiki.PageFile(slug))
	if err != nil {
		t.Fatalf("reading updated page: %v", err)
	}
	if body := wiki.ParsePage(slug, content).Body; body != "second" {
		t.Errorf("body = %q, want second", body)
	}
}

// TestSavePageConflictCarriesCurrentRevision checks the 409 shape the browser
// uses to keep the user's draft.
func TestSavePageConflictCarriesCurrentRevision(t *testing.T) {
	env, client := newTestEnv(t, false)
	slug := testNS + "/conflict-page"

	created := decodeMutation(t, postMutation(t, client, env.server.URL+"/_/api/pages/"+slug, map[string]any{
		"title": "Conflict Page", "body": "live body",
	}))

	resp := postMutation(t, client, env.server.URL+"/_/api/pages/"+slug, map[string]any{
		"title": "Conflict Page", "body": "my draft", "base_hash": "deadbeef",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale save status = %d, want 409", resp.StatusCode)
	}
	var conflict conflictResponse
	if err := json.NewDecoder(resp.Body).Decode(&conflict); err != nil {
		t.Fatalf("decoding conflict JSON: %v", err)
	}
	if conflict.Conflict.CurrentHash != created.BlobHash {
		t.Errorf("current_hash = %q, want %q", conflict.Conflict.CurrentHash, created.BlobHash)
	}
	if conflict.Conflict.Body != "live body" {
		t.Errorf("conflict body = %q, want the live revision", conflict.Conflict.Body)
	}
}

func TestSavePageMoveAndHiddenToggle(t *testing.T) {
	env, client := newTestEnv(t, false)

	moved := decodeMutation(t, postMutation(t, client, env.server.URL+"/_/api/pages/"+testNS+"/mover", map[string]any{
		"new_slug": testNS + "/moved", "title": "Moved", "body": "body",
	}))
	if moved.Slug != testNS+"/moved" {
		t.Fatalf("move result slug = %q, want %s/moved", moved.Slug, testNS)
	}
	if _, _, err := env.store.Read(wiki.PageFile(testNS + "/moved")); err != nil {
		t.Fatalf("moved page missing: %v", err)
	}

	hidden := decodeMutation(t, postMutation(t, client, env.server.URL+"/_/api/pages/"+testNS+"/help-page", map[string]any{
		"title": "Help Page", "body": "hidden body", "hidden": true,
	}))
	if hidden.Slug != testNS+"/help-page" {
		t.Fatalf("hidden save slug = %q, want %s/help-page", hidden.Slug, testNS)
	}
	if _, _, err := env.store.Read(wiki.HiddenFile(testNS + "/help-page")); err != nil {
		t.Fatalf("hidden page missing: %v", err)
	}
}

func TestDeletePageRemovesIt(t *testing.T) {
	env, client := newTestEnv(t, false)
	slug := testNS + "/deletable"
	env.seedPage(t, wiki.Page{Slug: slug, Title: "Deletable", Body: "body"})

	resp := postMutation(t, client, env.server.URL+"/_/api/pages/delete/"+slug, map[string]any{"hidden": false})
	decodeMutation(t, resp)

	if _, _, err := env.store.Read(wiki.PageFile(slug)); err == nil {
		t.Error("deleted page still on disk")
	}
	if env.index.Exists(slug) {
		t.Error("deleted page still indexed")
	}
}

func TestRenameRepairsBacklinks(t *testing.T) {
	env, client := newTestEnv(t, false)
	env.seedPage(t, wiki.Page{Slug: testNS + "/old-name", Title: "Old Name", Body: "target"})
	env.seedPage(t, wiki.Page{Slug: testNS + "/source", Title: "Source", Body: "see [[Old Name]]"})

	resp := postMutation(t, client, env.server.URL+"/_/api/pages/rename/"+testNS+"/old-name", map[string]any{"title": "New Name"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rename status = %d, want 200", resp.StatusCode)
	}
	var result pageRenameResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding rename result: %v", err)
	}
	if result.Slug != testNS+"/new-name" {
		t.Errorf("rename slug = %q, want %s/new-name", result.Slug, testNS)
	}
	content, _, err := env.store.Read(wiki.PageFile(testNS + "/source"))
	if err != nil {
		t.Fatalf("reading source: %v", err)
	}
	if body := wiki.ParsePage(testNS+"/source", content).Body; !strings.Contains(body, "[[New Name]]") {
		t.Errorf("backlink not repaired: %q", body)
	}
}

func TestSetTagsAndRevert(t *testing.T) {
	env, client := newTestEnv(t, false)
	slug := testNS + "/tag-and-revert"

	decodeMutation(t, postMutation(t, client, env.server.URL+"/_/api/pages/"+slug, map[string]any{
		"title": "Tag And Revert", "body": "version one",
	}))
	history, err := env.api.PageHistory(context.Background(), slug)
	if err != nil || len(history) == 0 {
		t.Fatalf("reading history: %v (%d revisions)", err, len(history))
	}
	firstCommit := history[0].Hash

	tags := decodeMutation(t, postMutation(t, client, env.server.URL+"/_/api/pages/tags/"+slug, map[string]any{"tags": []string{"alpha", "beta"}}))
	content, _, _ := env.store.Read(wiki.PageFile(slug))
	if got := wiki.ParsePage(slug, content).Tags; len(got) != 2 || got[0] != "alpha" {
		t.Errorf("tags = %v, want [alpha beta]", got)
	}

	decodeMutation(t, postMutation(t, client, env.server.URL+"/_/api/pages/"+slug, map[string]any{
		"title": "Tag And Revert", "body": "version two", "base_hash": tags.BlobHash,
	}))

	revert := decodeMutation(t, postMutation(t, client, env.server.URL+"/_/api/pages/revert/"+slug, map[string]any{"hash": firstCommit}))
	if revert.Slug != slug {
		t.Errorf("revert slug = %q, want %s", revert.Slug, slug)
	}
	content, _, _ = env.store.Read(wiki.PageFile(slug))
	if body := wiki.ParsePage(slug, content).Body; body != "version one" {
		t.Errorf("reverted body = %q, want version one", body)
	}
}
