// Package mcp adapts the shared application operations to the Model Context
// Protocol. It owns SDK types, the protocol gate, tool definitions and the
// mapping from application results to MCP output; every tool calls internal/api
// directly and no application operation is implemented here.
package mcp

import (
	"net/http"

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
	server := sdk.NewServer(&sdk.Implementation{Name: "hmd", Version: s.options.Version}, nil)
	s.registerPages(server)
	s.registerNamespaces(server)
	s.registerSearch(server)
	s.registerAttachments(server)
	s.registerHistory(server)
	return newMCPHTTPHandler(s.api.Config().BaseURL, func(*http.Request) *sdk.Server { return server })
}
