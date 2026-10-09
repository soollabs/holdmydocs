package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"hmd/internal/api"
	"hmd/internal/wiki"
)

type mcpNamespaceIn struct {
	Name string `json:"name" jsonschema:"namespace name, e.g. notes; this is a name, not a page slug or path"`
}

type mcpNamespaceMeta struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Pages       int    `json:"pages"`
	Public      bool   `json:"public"`
}

type mcpNamespacesOut struct {
	Namespaces []mcpNamespaceMeta `json:"namespaces"`
}

type mcpNamespaceOut struct {
	Export      wiki.ExportConfig   `json:"export,omitempty"`
	Name        string              `json:"name"`
	Widgets     []string            `json:"widgets"`
	Public      bool                `json:"public"`
	Title       string              `json:"title,omitempty"`
	Description string              `json:"description,omitempty"`
	Skin        string              `json:"skin,omitempty"`
	Palette     string              `json:"palette,omitempty"`
	Index       string              `json:"index,omitempty"`
	Tree        []string            `json:"tree,omitempty"`
	New         *wiki.NewPageConfig `json:"new,omitempty"`
	Hash        string              `json:"hash"`
}

type mcpSaveNamespaceIn struct {
	Export      wiki.ExportConfig   `json:"export,omitempty" jsonschema:"static export URL, sitemap, allowed robots and external links; preserve from read_namespace when updating"`
	Name        string              `json:"name" jsonschema:"namespace name, e.g. notes; this is a name, not a page slug or path"`
	Widgets     []string            `json:"widgets,omitempty" jsonschema:"complete ordered string array of configured widget IDs; preserve from read_namespace when updating"`
	Public      bool                `json:"public" jsonschema:"whether anonymous users can read the namespace; preserve from read_namespace when updating"`
	Title       string              `json:"title,omitempty" jsonschema:"human-readable namespace title; preserve from read_namespace when updating"`
	Description string              `json:"description,omitempty" jsonschema:"brief namespace description, maximum 255 characters; preserve from read_namespace when updating"`
	Skin        string              `json:"skin,omitempty" jsonschema:"presentation skin name; preserve from read_namespace when updating"`
	Palette     string              `json:"palette,omitempty" jsonschema:"colour palette name; preserve from read_namespace when updating"`
	Index       string              `json:"index,omitempty" jsonschema:"one-segment page name used as the namespace index; preserve from read_namespace when updating"`
	Tree        []string            `json:"tree,omitempty" jsonschema:"complete ordered string array of page or folder paths for the tree; preserve from read_namespace when updating"`
	New         *wiki.NewPageConfig `json:"new,omitempty" jsonschema:"new-page template configuration; preserve from read_namespace when updating"`
	BaseHash    string              `json:"basehash,omitempty" jsonschema:"hash from read_namespace; omit only when creating a namespace configuration"`
}

func mcpNamespaceOutput(name string, cfg wiki.NamespaceConfig, hash string) mcpNamespaceOut {
	return mcpNamespaceOut{
		Export: cfg.Export,
		Name:   name, Widgets: cfg.Widgets, Public: cfg.Public, Title: cfg.Title, Description: cfg.Description,
		Skin: cfg.Skin, Palette: cfg.Palette, Index: cfg.Index, Tree: cfg.Tree, New: cfg.New, Hash: hash,
	}
}

func mcpNamespaceFromDetail(detail *api.NamespaceDetail) mcpNamespaceOut {
	return mcpNamespaceOutput(detail.Name, detail.Config, detail.Hash)
}

func (s *Server) registerNamespaces(server *sdk.Server) {
	sdk.AddTool(server, &sdk.Tool{
		Name:        "list_namespaces",
		Description: "List namespaces accessible to this caller. Arguments: none. Returns each namespace's name, description, page count and public/private status. Pass a returned name to read_namespace for full settings.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, _ any) (*sdk.CallToolResult, mcpNamespacesOut, error) {
		if err := s.requireScope(ctx, api.ScopeRead); err != nil {
			return nil, mcpNamespacesOut{}, err
		}
		out := mcpNamespacesOut{Namespaces: []mcpNamespaceMeta{}}
		for _, summary := range s.api.ListNamespaces(ctx) {
			out.Namespaces = append(out.Namespaces, mcpNamespaceMeta{Name: summary.Name, Description: summary.Config.Description, Pages: summary.Count, Public: summary.Config.Public})
		}
		return nil, out, nil
	})

	sdk.AddTool(server, &sdk.Tool{
		Name:        "read_namespace",
		Description: "Read one namespace's full settings and current hash. Argument: name (required namespace name, for example notes; not a page slug or path). Requires settings access. Pass the returned hash as basehash to save_namespace when updating.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in mcpNamespaceIn) (*sdk.CallToolResult, mcpNamespaceOut, error) {
		if err := s.requireScope(ctx, api.ScopeSettings); err != nil {
			return nil, mcpNamespaceOut{}, err
		}
		detail, err := s.api.ReadNamespace(ctx, in.Name)
		if err != nil {
			return nil, mcpNamespaceOut{}, err
		}
		return nil, mcpNamespaceFromDetail(detail), nil
	})

	sdk.AddTool(server, &sdk.Tool{
		Name: "save_namespace",
		Description: "Create or replace namespace settings (each save is a git commit). Arguments: name (required string), public (required boolean), widgets (optional complete string array), title, description, skin, palette and index (optional strings), tree (optional complete string array), new (optional object with template and slug naming-pattern strings), export (optional object with base_url, sitemap, robots_allow and links), and basehash (required for updates; omit only on create). " +
			"Requires settings access. For updates, first call read_namespace and preserve every setting you do not intend to change; omitted optional fields are reset. Allowed widgets: pages, namespaces, pinned, tags, log, health, calendar, writing-stats, page-meta, backlinks, prev-entries. Allowed skins: phosphor, newsprint, journal, soft, bare. Allowed palettes: phosphor, catppuccin, dracula, everforest, gruvbox, monokai, nord, one dark, rosé pine, solarized, tokyo night. Index must be a one-segment page name. On conflict re-read and retry. Creating or updating settings does not delete or change pages.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in mcpSaveNamespaceIn) (*sdk.CallToolResult, mcpNamespaceOut, error) {
		if err := s.requireScope(ctx, api.ScopeSettings); err != nil {
			return nil, mcpNamespaceOut{}, err
		}
		if !wiki.ValidNamespaceName(in.Name) {
			return nil, mcpNamespaceOut{}, fmt.Errorf("invalid namespace %q", in.Name)
		}
		if err := s.requireNamespace(ctx, in.Name); err != nil {
			return nil, mcpNamespaceOut{}, err
		}
		cfg := wiki.NamespaceConfig{
			Export:  in.Export,
			Widgets: in.Widgets, Public: in.Public, Title: in.Title, Description: in.Description, Skin: in.Skin,
			Palette: in.Palette, Index: in.Index, Tree: in.Tree, New: in.New,
		}
		detail, err := s.api.SaveNamespace(ctx, api.SaveNamespaceInput{
			Name:         in.Name,
			Config:       cfg,
			TemplateData: api.NewPageTemplateData{Now: time.Now(), User: s.user(ctx), Namespace: in.Name},
			BaseHash:     in.BaseHash,
		})
		if err != nil {
			if api.CategoryOf(err) == api.CategoryConflict {
				current, readErr := s.api.ReadNamespace(ctx, in.Name)
				if readErr != nil {
					return nil, mcpNamespaceOut{}, readErr
				}
				payload, _ := json.Marshal(map[string]any{"error": "conflict: the namespace changed since basehash (or already exists)", "namespace": mcpNamespaceFromDetail(current)})
				return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: string(payload)}}}, mcpNamespaceOut{}, nil
			}
			return nil, mcpNamespaceOut{}, err
		}
		slog.Info("mcp namespace configured", "namespace", in.Name, "author", s.user(ctx))
		return nil, mcpNamespaceFromDetail(detail), nil
	})
}
