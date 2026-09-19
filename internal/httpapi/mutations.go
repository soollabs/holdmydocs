package httpapi

import (
	"context"
	"net/http"
	"strings"

	"hmd/internal/api"
)

// pageSaveInput is the JSON body for creating, updating or moving a page. An
// empty BaseHash creates; a non-empty one replaces the revision it names.
// NewSlug, when set and different, makes the write a checked move. Hidden and
// FromHidden name the hidden tree so a normal/hidden toggle is a real path
// move.
type pageSaveInput struct {
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	Tags       []string `json:"tags"`
	Pin        *bool    `json:"pin"`
	BaseHash   string   `json:"base_hash"`
	NewSlug    string   `json:"new_slug"`
	Hidden     bool     `json:"hidden"`
	FromHidden bool     `json:"from_hidden"`
}

// pageDeleteInput names the tree the deleted page lives in.
type pageDeleteInput struct {
	Hidden bool `json:"hidden"`
}

// pageRenameInput carries the new title for an in-place retitle.
type pageRenameInput struct {
	Title string `json:"title"`
}

// pageTagsInput replaces a page's tag set.
type pageTagsInput struct {
	Tags []string `json:"tags"`
}

// pageRevertInput names the revision to restore.
type pageRevertInput struct {
	Hash string `json:"hash"`
}

// pageMutation is the JSON result of a committed page write. A committed write
// is a success even when IndexWarning is set: only the derived index failed to
// refresh and will be reconciled later.
type pageMutation struct {
	Slug         string `json:"slug"`
	BlobHash     string `json:"blob_hash"`
	IndexWarning string `json:"index_warning,omitempty"`
}

// pageRenameResult reports a retitle and its backlink repair. LinkFailures
// lists backlink sources that could not be rewritten.
type pageRenameResult struct {
	Slug         string   `json:"slug"`
	BlobHash     string   `json:"blob_hash"`
	IndexWarning string   `json:"index_warning,omitempty"`
	LinkFailures []string `json:"link_failures,omitempty"`
}

// conflictPage is the current committed revision an optimistic write collided
// with. The browser uses it to keep the user's unsaved input and offer to
// overwrite, so it carries the live page fields, not just hashes.
type conflictPage struct {
	BaseHash    string   `json:"base_hash"`
	CurrentHash string   `json:"current_hash"`
	Slug        string   `json:"slug"`
	Title       string   `json:"title"`
	Body        string   `json:"body"`
	Tags        []string `json:"tags"`
	Pin         bool     `json:"pin"`
}

// conflictResponse is the 409 JSON body for a rejected optimistic write.
type conflictResponse struct {
	Error    string       `json:"error"`
	Conflict conflictPage `json:"conflict"`
}

// savePage handles create, update and move for both the normal and hidden
// trees. Path value slug is authoritative; the body never restates it. It is
// concrete rather than generic because a conflict returns a richer payload
// than writeAPIError.
func (h *Handlers) savePage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !api.AllowSlug(r.Context(), slug) {
		writeNamespaceDenied(w)
		return
	}
	var in pageSaveInput
	if err := decodeInput(w, r, &in); err != nil {
		writeAPIError(w, err)
		return
	}
	targetSlug := slug
	if want := strings.TrimSpace(in.NewSlug); want != "" && want != slug {
		targetSlug = want
	}

	var (
		mutation *api.Mutation
		err      error
	)
	switch {
	case targetSlug != slug:
		mutation, err = h.api.MovePage(r.Context(), api.MovePageInput{
			FromSlug: slug, ToSlug: targetSlug, FromHidden: in.FromHidden, ToHidden: in.Hidden,
			Title: in.Title, Tags: in.Tags, Body: in.Body, BaseHash: in.BaseHash,
		})
	case in.BaseHash == "":
		mutation, err = h.api.SavePage(r.Context(), api.SavePageInput{
			Slug: slug, Title: in.Title, Tags: in.Tags, Body: in.Body,
			Pin: in.Pin != nil && *in.Pin, Hidden: in.Hidden,
		})
	default:
		title, tags, body, pin := in.Title, in.Tags, in.Body, in.Pin
		mutation, err = h.api.UpdatePage(r.Context(), api.UpdatePageInput{
			Slug: slug, BaseHash: in.BaseHash, FromHidden: in.FromHidden, ToHidden: in.Hidden,
			Title: &title, Tags: &tags, Body: &body, Pin: pin,
		})
	}
	if err != nil {
		if api.CategoryOf(err) == api.CategoryConflict {
			h.writeSaveConflict(w, r, in, slug)
			return
		}
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pageMutation{
		Slug: mutation.Slug, BlobHash: mutation.BlobHash, IndexWarning: mutation.IndexWarning,
	})
}

// writeSaveConflict reads the current committed revision and returns it with
// the caller's stale base hash, so the browser can keep the unsaved draft.
func (h *Handlers) writeSaveConflict(w http.ResponseWriter, r *http.Request, in pageSaveInput, slug string) {
	conflict := conflictPage{BaseHash: in.BaseHash, Slug: slug}
	if view, err := h.readPage(r, slug, in.FromHidden); err == nil {
		conflict.CurrentHash = view.Hash
		conflict.Slug = view.Slug
		conflict.Title = view.Title
		conflict.Body = view.Body
		conflict.Tags = view.Tags
		conflict.Pin = view.Pin
	}
	writeJSON(w, http.StatusConflict, conflictResponse{
		Error:    "the page changed since base hash",
		Conflict: conflict,
	})
}

// readPage reads one page from the normal or hidden tree.
func (h *Handlers) readPage(r *http.Request, slug string, hidden bool) (*api.PageView, error) {
	if hidden {
		return h.api.ViewHidden(r.Context(), slug)
	}
	return h.api.ViewPage(r.Context(), slug)
}

// deletePage removes a page from the normal or hidden tree.
func (h *Handlers) deletePage(ctx context.Context, slug string, in pageDeleteInput) (pageMutation, error) {
	mutation, err := h.api.DeletePage(ctx, slug, in.Hidden)
	if err != nil {
		return pageMutation{}, err
	}
	return pageMutation{Slug: mutation.Slug}, nil
}

// renamePage retitles a page and repairs backlinks.
func (h *Handlers) renamePage(ctx context.Context, slug string, in pageRenameInput) (pageRenameResult, error) {
	result, err := h.api.RenamePage(ctx, api.RenamePageInput{Slug: slug, NewTitle: in.Title})
	if err != nil {
		return pageRenameResult{}, err
	}
	return pageRenameResult{
		Slug: result.Slug, BlobHash: result.BlobHash,
		IndexWarning: result.IndexWarning, LinkFailures: result.FailedLinks,
	}, nil
}

// setPageTags replaces a page's tags.
func (h *Handlers) setPageTags(ctx context.Context, slug string, in pageTagsInput) (pageMutation, error) {
	mutation, err := h.api.SetPageTags(ctx, slug, in.Tags)
	if err != nil {
		return pageMutation{}, err
	}
	return pageMutation{Slug: mutation.Slug, BlobHash: mutation.BlobHash, IndexWarning: mutation.IndexWarning}, nil
}

// revertPage restores one committed revision as a new commit.
func (h *Handlers) revertPage(ctx context.Context, slug string, in pageRevertInput) (pageMutation, error) {
	mutation, err := h.api.RevertPage(ctx, slug, in.Hash)
	if err != nil {
		return pageMutation{}, err
	}
	return pageMutation{Slug: mutation.Slug, BlobHash: mutation.BlobHash, IndexWarning: mutation.IndexWarning}, nil
}
