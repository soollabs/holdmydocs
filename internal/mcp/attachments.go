package mcp

import (
	"context"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"hmd/internal/wiki"
)

type mcpAttachmentSearchIn struct {
	Query     string `json:"query"`
	Namespace string `json:"namespace,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type mcpAttachmentSearchHit struct {
	OwnerSlug string  `json:"owner_slug"`
	Filename  string  `json:"filename"`
	URL       string  `json:"url"`
	Excerpt   string  `json:"excerpt"`
	Score     float64 `json:"score"`
}

type mcpAttachmentSearchOut struct {
	Hits []mcpAttachmentSearchHit `json:"hits"`
}

type mcpAttachmentUploadIn struct {
	Slug     string `json:"slug"`
	Filename string `json:"filename"`
}

type mcpAttachmentUploadOut struct {
	UploadURL     string `json:"upload_url"`
	AttachmentURL string `json:"attachment_url"`
	ExpiresAt     string `json:"expires_at"`
}

type mcpAttachmentReadIn struct {
	Slug     string `json:"slug"`
	Filename string `json:"filename"`
	Offset   int    `json:"offset,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

type mcpAttachmentReadOut struct {
	Text string `json:"text"`
	textWindow
}

func (s *Server) registerAttachments(server *sdk.Server) {
	if s.options.DocumentsEnabled {
		addTool(s, server, "search_attachments", func(ctx context.Context, req *sdk.CallToolRequest, in mcpAttachmentSearchIn) (*sdk.CallToolResult, mcpAttachmentSearchOut, error) {
			hits, err := s.api.SearchAttachments(ctx, in.Query, in.Namespace, in.Limit)
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

	addTool(s, server, "upload_attachment", func(ctx context.Context, req *sdk.CallToolRequest, in mcpAttachmentUploadIn) (*sdk.CallToolResult, mcpAttachmentUploadOut, error) {
		slug, err := mcpPageSlug(in.Slug)
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
			AttachmentURL: wiki.AttachmentURL(grant.Slug, grant.Filename),
			ExpiresAt:     grant.Expires.UTC().Format(time.RFC3339),
		}, nil
	})

	addTool(s, server, "read_attachment", func(ctx context.Context, req *sdk.CallToolRequest, in mcpAttachmentReadIn) (*sdk.CallToolResult, mcpAttachmentReadOut, error) {
		slug, err := mcpPageSlug(in.Slug)
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
		out := mcpAttachmentReadOut{}
		out.Text, out.textWindow, err = windowText(text, in.Offset, in.Limit)
		if err != nil {
			return nil, mcpAttachmentReadOut{}, err
		}
		return nil, out, nil
	})
}
