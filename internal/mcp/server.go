// Package mcp adapts the shared application operations to the Model Context
// Protocol. It owns SDK types, the protocol gate, tool definitions and the
// mapping from application results to MCP output; every tool calls internal/api
// directly and no application operation is implemented here.
package mcp

import (
	"net/http"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"hmd/internal/api"
)

// Options configures the MCP adapter.
type Options struct {
	// Version is the server build version reported to clients.
	Version string
	// DocumentsEnabled reports whether attachment document search is available,
	// which controls whether search_attachments is exposed.
	DocumentsEnabled bool
}

// Server exposes the wiki as MCP tools over a single streamable-HTTP handler.
type Server struct {
	api     *api.API
	options Options
}

// NewServer builds an MCP adapter over the shared application operations.
func NewServer(a *api.API, opts Options) *Server {
	return &Server{api: a, options: opts}
}

// Handler returns the streamable-HTTP handler that serves the MCP endpoint. The
// composition root mounts it at /_/mcp.
func (s *Server) Handler() http.Handler {
	server := sdk.NewServer(s.implementation(), &sdk.ServerOptions{
		SupportedProtocolVersions: []string{"2026-07-28", "2025-11-25", "2025-06-18", "2025-03-26"},
	})
	s.registerPages(server)
	s.registerNamespaces(server)
	s.registerSearch(server)
	s.registerAttachments(server)
	s.registerHistory(server)
	return newMCPHTTPHandler(s.api.Config().BaseURL, func(*http.Request) *sdk.Server { return server })
}

func (s *Server) implementation() *sdk.Implementation {
	info := &sdk.Implementation{
		Name: "hmd", Title: "HoldMyDocs", Version: s.options.Version,
		Description: "Search, read and manage pages and attachments in HoldMyDocs.",
	}
	if origin := strings.TrimRight(s.api.Config().BaseURL, "/"); origin != "" {
		info.Icons = []sdk.Icon{{
			Source:   origin + "/_/static/icon.svg",
			MIMEType: "image/svg+xml",
			Sizes:    []string{"any"},
		}}
	}
	return info
}
