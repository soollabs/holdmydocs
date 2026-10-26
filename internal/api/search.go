package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"hmd/internal/search"
	"hmd/internal/wiki"
)

// PageHit is one authorised full-text page search result. Snippet is escaped
// text that may contain <mark> tags and is safe to render as trusted HTML only
// in a presentation adapter.
type PageHit struct {
	Slug    string
	Title   string
	Snippet string
	Tags    []string
}

// AttachmentHit is one authorised attachment search result.
type AttachmentHit struct {
	OwnerSlug string
	Filename  string
	URL       string
	Excerpt   string
	Score     float64
}

// SearchPages runs full-text page search and returns only the results the
// caller may read.
//
// The index returns a bounded candidate window (currently 20 page hits) and
// access filtering happens after retrieval, so a caller with restricted access
// may receive a short page even when further authorised matches exist.
// Authorisation-aware retrieval is intentionally out of scope for this API.
func (a *API) SearchPages(ctx context.Context, query string) ([]PageHit, error) {
	if err := a.RequireScope(ctx, ScopeRead); err != nil {
		return nil, err
	}
	if err := search.ValidateQuery(query); err != nil {
		return nil, InvalidInput(err.Error(), err)
	}
	hits, err := a.index.Search(query)
	if err != nil {
		return nil, Unavailable("page search failed", err)
	}
	results := make([]PageHit, 0, len(hits))
	for _, hit := range hits {
		if !AllowSlug(ctx, hit.Slug) {
			continue
		}
		results = append(results, PageHit{Slug: hit.Slug, Title: hit.Title, Snippet: hit.Snippet, Tags: hit.Tags})
	}
	return results, nil
}

// DefaultPageSearchResults and MaxPageSearchResults bound a PageSearch window,
// and MaxPageSearchOffset bounds how deep a caller may page.
const (
	DefaultPageSearchResults = 20
	MaxPageSearchResults     = 50
	MaxPageSearchOffset      = 1000
)

// PageSearchResult is one window of authorised page hits. More reports that
// the index holds further matches beyond the window, and Next is the offset of
// the following window; access filtering can leave a window short even then.
type PageSearchResult struct {
	Hits []PageHit
	More bool
	Next int
}

// PageSearch runs full-text page search within an optional namespace and
// returns the authorised hits among at most limit index results from offset
// (DefaultPageSearchResults when limit is zero). Access filtering follows
// retrieval, as in SearchPages.
func (a *API) PageSearch(ctx context.Context, query, namespace string, offset, limit int) (*PageSearchResult, error) {
	if err := a.RequireScope(ctx, ScopeRead); err != nil {
		return nil, err
	}
	if err := search.ValidateQuery(query); err != nil {
		return nil, InvalidInput(err.Error(), err)
	}
	if limit == 0 {
		limit = DefaultPageSearchResults
	}
	if limit < 0 || limit > MaxPageSearchResults {
		return nil, InvalidInput(fmt.Sprintf("limit must be between 1 and %d", MaxPageSearchResults), nil)
	}
	if offset < 0 || offset > MaxPageSearchOffset {
		return nil, InvalidInput(fmt.Sprintf("offset must be between 0 and %d", MaxPageSearchOffset), nil)
	}
	if err := a.checkSearchNamespace(ctx, namespace); err != nil {
		return nil, err
	}
	hits, more, err := a.index.SearchPages(query, namespace, offset, limit)
	if err != nil {
		return nil, Unavailable("page search failed", err)
	}
	result := &PageSearchResult{Hits: make([]PageHit, 0, len(hits)), More: more, Next: offset + len(hits)}
	for _, hit := range hits {
		if AllowSlug(ctx, hit.Slug) {
			result.Hits = append(result.Hits, PageHit{Slug: hit.Slug, Title: hit.Title, Snippet: hit.Snippet, Tags: hit.Tags})
		}
	}
	return result, nil
}

// checkSearchNamespace rejects an invalid or inaccessible namespace filter.
// An empty namespace searches everything the caller may read.
func (a *API) checkSearchNamespace(ctx context.Context, namespace string) error {
	if namespace == "" {
		return nil
	}
	if err := wiki.CheckNamespaceName(namespace); err != nil {
		return InvalidInput(err.Error(), err)
	}
	if !AllowNamespace(ctx, namespace) {
		return Forbidden("namespace access denied")
	}
	return nil
}

// DefaultAttachmentSearchResults and MaxAttachmentSearchResults bound an
// attachment search page.
const (
	DefaultAttachmentSearchResults = 20
	MaxAttachmentSearchResults     = 50
)

// attachmentSearchCandidates is the fixed retrieval window requested from the
// index before access filtering.
//
// The index fuses keyword and semantic candidates, then this operation drops
// any result the caller may not read. Post-filtering a bounded candidate window
// cannot guarantee a full authorised result page; the window is preserved
// rather than widened because authorisation-aware retrieval is out of scope for
// this API.
const attachmentSearchCandidates = 50

// SearchAttachments runs attachment full-text search, limited to attachments
// of pages in namespace when it is not empty. It normalises the query, clamps
// limit to [1, MaxAttachmentSearchResults], and maps disabled, busy and failed
// states onto explicit error categories.
func (a *API) SearchAttachments(ctx context.Context, query, namespace string, limit int) ([]AttachmentHit, error) {
	if err := a.RequireScope(ctx, ScopeRead); err != nil {
		return nil, err
	}
	if !a.index.DocumentsEnabled() {
		return nil, Unavailable("attachment search is not enabled", nil)
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, InvalidInput("query cannot be empty", nil)
	}
	if err := search.ValidateQuery(query); err != nil {
		return nil, InvalidInput(err.Error(), err)
	}
	if err := a.checkSearchNamespace(ctx, namespace); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = DefaultAttachmentSearchResults
	}
	if limit > MaxAttachmentSearchResults {
		limit = MaxAttachmentSearchResults
	}
	hits, err := a.index.SearchAttachments(ctx, query, namespace, attachmentSearchCandidates)
	if err != nil {
		if errors.Is(err, search.ErrBusy) {
			return nil, Busy("search is busy")
		}
		return nil, Unavailable("attachment search unavailable", err)
	}
	results := make([]AttachmentHit, 0, limit)
	for _, hit := range hits {
		if !AllowSlug(ctx, hit.OwnerSlug) {
			continue
		}
		results = append(results, AttachmentHit{
			OwnerSlug: hit.OwnerSlug, Filename: hit.Filename, URL: hit.URL,
			Excerpt: hit.Excerpt, Score: hit.Score,
		})
		if len(results) == limit {
			break
		}
	}
	return results, nil
}
