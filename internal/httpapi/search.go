package httpapi

import (
	"context"
)

// AutocompleteResult is one page in the JSON search response.
type AutocompleteResult struct {
	Slug    string   `json:"slug"`
	Title   string   `json:"title"`
	Snippet string   `json:"snippet"`
	Tags    []string `json:"tags"`
}

// AttachmentResult is one attachment in the JSON search response. Excerpt is
// escaped text that the index may annotate with <mark> tags; it is a plain
// string because a JSON field is not a trusted-HTML context.
type AttachmentResult struct {
	OwnerSlug string  `json:"owner_slug"`
	Filename  string  `json:"filename"`
	URL       string  `json:"url"`
	Excerpt   string  `json:"excerpt"`
	Score     float64 `json:"score"`
}

// searchInput carries the query parameter shared by both search endpoints.
type searchInput struct {
	Query string `json:"q"`
}

// searchPages returns the page autocomplete results for the caller.
func (h *Handlers) searchPages(ctx context.Context, in searchInput) ([]AutocompleteResult, error) {
	hits, err := h.api.SearchPages(ctx, in.Query)
	if err != nil {
		return nil, err
	}
	results := make([]AutocompleteResult, 0, len(hits))
	for _, hit := range hits {
		results = append(results, AutocompleteResult{Slug: hit.Slug, Title: hit.Title, Snippet: hit.Snippet, Tags: hit.Tags})
	}
	return results, nil
}

// searchAttachments returns the attachment results for the caller.
func (h *Handlers) searchAttachments(ctx context.Context, in searchInput) ([]AttachmentResult, error) {
	hits, err := h.api.SearchAttachments(ctx, in.Query, 20)
	if err != nil {
		return nil, err
	}
	results := make([]AttachmentResult, 0, len(hits))
	for _, hit := range hits {
		results = append(results, AttachmentResult{
			OwnerSlug: hit.OwnerSlug, Filename: hit.Filename, URL: hit.URL,
			Excerpt: hit.Excerpt, Score: hit.Score,
		})
	}
	return results, nil
}
