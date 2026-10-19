package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpSearchIn struct {
	Query     string `json:"query"`
	Namespace string `json:"namespace,omitempty"`
	Offset    int    `json:"offset,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type mcpSearchHit struct {
	Slug    string   `json:"slug"`
	Title   string   `json:"title"`
	Snippet string   `json:"snippet"`
	Tags    []string `json:"tags"`
}

type mcpSearchOut struct {
	Hits       []mcpSearchHit `json:"hits"`
	More       bool           `json:"more"`
	NextOffset int            `json:"next_offset"`
}

func (s *Server) registerSearch(server *sdk.Server) {
	addTool(s, server, "search", func(ctx context.Context, req *sdk.CallToolRequest, in mcpSearchIn) (*sdk.CallToolResult, mcpSearchOut, error) {
		result, err := s.api.PageSearch(ctx, in.Query, in.Namespace, in.Offset, in.Limit)
		if err != nil {
			return nil, mcpSearchOut{}, err
		}
		out := mcpSearchOut{Hits: make([]mcpSearchHit, 0, len(result.Hits)), More: result.More, NextOffset: result.Next}
		for _, h := range result.Hits {
			out.Hits = append(out.Hits, mcpSearchHit{Slug: h.Slug, Title: h.Title, Snippet: h.Snippet, Tags: nonNil(h.Tags)})
		}
		return nil, out, nil
	})
}
