package mcp

import (
	"context"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"hmd/internal/api"
)

type mcpRecentIn struct {
	Limit int `json:"limit,omitempty" jsonschema:"max commits to return, default 20"`
}

type mcpCommit struct {
	Hash    string   `json:"hash"`
	Message string   `json:"message"`
	Author  string   `json:"author"`
	When    string   `json:"when"`
	Files   []string `json:"files,omitempty"`
}

type mcpRecentOut struct {
	Commits []mcpCommit `json:"commits"`
}

func (s *Server) registerHistory(server *sdk.Server) {
	sdk.AddTool(server, &sdk.Tool{
		Name:        "recent_changes",
		Description: "List the newest accessible wiki commits first, with commit metadata and touched files. Argument: limit (optional positive integer, default 20). A commit is omitted if it includes files outside this caller's namespace access.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in mcpRecentIn) (*sdk.CallToolResult, mcpRecentOut, error) {
		if err := s.requireScope(ctx, api.ScopeRead); err != nil {
			return nil, mcpRecentOut{}, err
		}
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
				Files:   c.Files,
			})
		}
		return nil, out, nil
	})
}
