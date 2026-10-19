package mcp

import (
	"context"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpRecentIn struct {
	Limit int `json:"limit,omitempty"`
}

type mcpCommit struct {
	Hash    string   `json:"hash"`
	Message string   `json:"message"`
	Author  string   `json:"author"`
	When    string   `json:"when"`
	Files   []string `json:"files"`
}

type mcpRecentOut struct {
	Commits []mcpCommit `json:"commits"`
}

func (s *Server) registerHistory(server *sdk.Server) {
	addTool(s, server, "recent_changes", func(ctx context.Context, req *sdk.CallToolRequest, in mcpRecentIn) (*sdk.CallToolResult, mcpRecentOut, error) {
		commits, err := s.api.RecentChanges(ctx, in.Limit)
		if err != nil {
			return nil, mcpRecentOut{}, err
		}
		out := mcpRecentOut{Commits: make([]mcpCommit, 0, len(commits))}
		for _, c := range commits {
			out.Commits = append(out.Commits, mcpCommit{
				Hash:    c.Hash,
				Message: c.Message,
				Author:  c.Author,
				When:    c.When.Format(time.RFC3339),
				Files:   nonNil(c.Files),
			})
		}
		return nil, out, nil
	})
}
