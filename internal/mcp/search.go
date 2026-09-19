package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"hmd/internal/api"
)

type mcpSearchIn struct {
	Query string `json:"query" jsonschema:"full-text query for page titles, bodies, and tags"`
}

type mcpSearchHit struct {
	Slug    string   `json:"slug"`
	Title   string   `json:"title"`
	Snippet string   `json:"snippet,omitempty"`
	Tags    []string `json:"tags,omitempty"`
}

type mcpSearchOut struct {
	Hits []mcpSearchHit `json:"hits"`
}

func (s *Server) registerSearch(server *sdk.Server) {
	sdk.AddTool(server, &sdk.Tool{
		Name:        "search",
		Description: "Full-text search accessible page titles, bodies, and tags. Argument: query (required full-text query string). Results include escaped snippets with optional <mark> tags; use read_page for the complete page.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in mcpSearchIn) (*sdk.CallToolResult, mcpSearchOut, error) {
		if err := s.requireScope(ctx, api.ScopeRead); err != nil {
			return nil, mcpSearchOut{}, err
		}
		hits, err := s.api.SearchPages(ctx, in.Query)
		if err != nil {
			return nil, mcpSearchOut{}, err
		}
		out := mcpSearchOut{Hits: make([]mcpSearchHit, 0, len(hits))}
		for _, h := range hits {
			out.Hits = append(out.Hits, mcpSearchHit{Slug: h.Slug, Title: h.Title, Snippet: h.Snippet, Tags: h.Tags})
		}
		return nil, out, nil
	})
}
