package mcp

import (
	"context"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"hmd/internal/api"
)

type mcpAttachmentSearchIn struct {
	Query string `json:"query" jsonschema:"keyword or semantic query for extracted attachment text"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum 50 results, default 20"`
}

type mcpAttachmentSearchHit struct {
	OwnerSlug string  `json:"owner_slug"`
	Filename  string  `json:"filename"`
	URL       string  `json:"url"`
	Excerpt   string  `json:"excerpt" jsonschema:"escaped text containing optional <mark> tags"`
	Score     float64 `json:"score"`
}

type mcpAttachmentSearchOut struct {
	Hits []mcpAttachmentSearchHit `json:"hits"`
}

type mcpAttachmentUploadIn struct {
	Slug     string `json:"slug,omitempty" jsonschema:"canonical identifier of the page that owns the attachment, in namespace/page form; provide slug or path, not both"`
	Path     string `json:"path,omitempty" jsonschema:"alias for slug, identifying the owning page in namespace/page form; provide slug or path, not both"`
	Filename string `json:"filename" jsonschema:"original filename; HMD returns its canonical attachment URL; document formats supported by Apache Tika are indexed when document search is enabled"`
}

type mcpAttachmentUploadOut struct {
	UploadURL     string `json:"upload_url" jsonschema:"one-use multipart POST URL; upload the file as the file form field"`
	AttachmentURL string `json:"attachment_url" jsonschema:"URL of the uploaded attachment after a successful upload"`
	ExpiresAt     string `json:"expires_at"`
}

type mcpAttachmentReadIn struct {
	Slug     string `json:"slug,omitempty" jsonschema:"canonical identifier of the page that owns the attachment, in namespace/page form; provide slug or path, not both"`
	Path     string `json:"path,omitempty" jsonschema:"alias for slug, identifying the owning page in namespace/page form; provide slug or path, not both"`
	Filename string `json:"filename" jsonschema:"canonical attachment filename, without a path"`
}

type mcpAttachmentReadOut struct {
	Text string `json:"text"`
}

func (s *Server) registerAttachments(server *sdk.Server) {
	if s.options.DocumentsEnabled {
		sdk.AddTool(server, &sdk.Tool{
			Name:        "search_attachments",
			Description: "Search extracted attachment text using keyword and semantic ranking. Arguments: query (required string) and limit (optional integer, default 20, maximum 50). Available only when document indexing is enabled. Results are limited to accessible page-owned attachments; excerpts are escaped text with optional <mark> tags.",
		}, func(ctx context.Context, req *sdk.CallToolRequest, in mcpAttachmentSearchIn) (*sdk.CallToolResult, mcpAttachmentSearchOut, error) {
			if err := s.requireScope(ctx, api.ScopeRead); err != nil {
				return nil, mcpAttachmentSearchOut{}, err
			}
			hits, err := s.api.SearchAttachments(ctx, in.Query, in.Limit)
			if err != nil {
				return nil, mcpAttachmentSearchOut{}, err
			}
			out := mcpAttachmentSearchOut{Hits: make([]mcpAttachmentSearchHit, 0, len(hits))}
			for _, hit := range hits {
				out.Hits = append(out.Hits, mcpAttachmentSearchHit{
					OwnerSlug: hit.OwnerSlug, Filename: hit.Filename, URL: hit.URL,
					Excerpt: hit.Excerpt, Score: hit.Score,
				})
			}
			return nil, out, nil
		})
	}

	sdk.AddTool(server, &sdk.Tool{
		Name:        "upload_attachment",
		Description: "Create a one-use native multipart upload URL for a document or image owned by a page. Arguments: exactly one of slug or path (required page identifier in namespace/page form), plus filename (required original filename without a directory). Requires write access to that page. HMD canonicalises filename; use the returned attachment_url rather than constructing one. POST the file as the file form field. Apache Tika extracts and indexes supported formats when document search is enabled. After a successful upload, use save_page to add [filename](attachment_url) to the owning page, or ![alt text](attachment_url) for an image.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in mcpAttachmentUploadIn) (*sdk.CallToolResult, mcpAttachmentUploadOut, error) {
		if err := s.requireScope(ctx, api.ScopeWrite); err != nil {
			return nil, mcpAttachmentUploadOut{}, err
		}
		slug, err := mcpPageIdentifier(in.Slug, in.Path)
		if err != nil {
			return nil, mcpAttachmentUploadOut{}, err
		}
		if err := s.requireSlug(ctx, slug); err != nil {
			return nil, mcpAttachmentUploadOut{}, err
		}
		grant, err := s.api.IssueUploadCapability(ctx, slug, in.Filename)
		if err != nil {
			return nil, mcpAttachmentUploadOut{}, err
		}
		baseURL, _ := ctx.Value(mcpBaseURLKey{}).(string)
		return nil, mcpAttachmentUploadOut{
			UploadURL:     baseURL + "/_/api/attachment-uploads/" + grant.Token,
			AttachmentURL: "/_/attachments/" + grant.Slug + "/" + grant.Filename,
			ExpiresAt:     grant.Expires.UTC().Format(time.RFC3339),
		}, nil
	})

	sdk.AddTool(server, &sdk.Tool{
		Name:        "read_attachment",
		Description: "Read an attachment's cached extracted text. Arguments: exactly one of slug or path (required owning-page identifier in namespace/page form), plus filename (required canonical attachment filename without a directory). Requires read access to the owning page. Uploads made while document indexing is enabled have a Git-tracked extraction sidecar; source text files also get one. Use search_attachments first for large documents.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in mcpAttachmentReadIn) (*sdk.CallToolResult, mcpAttachmentReadOut, error) {
		if err := s.requireScope(ctx, api.ScopeRead); err != nil {
			return nil, mcpAttachmentReadOut{}, err
		}
		slug, err := mcpPageIdentifier(in.Slug, in.Path)
		if err != nil {
			return nil, mcpAttachmentReadOut{}, err
		}
		if err := s.requireSlug(ctx, slug); err != nil {
			return nil, mcpAttachmentReadOut{}, err
		}
		text, err := s.api.ReadAttachment(ctx, slug, in.Filename)
		if err != nil {
			return nil, mcpAttachmentReadOut{}, err
		}
		return nil, mcpAttachmentReadOut{Text: text}, nil
	})
}
