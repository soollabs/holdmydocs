package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"hmd/internal/api"
	"hmd/internal/wiki"
)

type mcpNamespaceIn struct {
	Name string `json:"name"`
}

type mcpNamespaceMeta struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Pages       int    `json:"pages"`
	Public      bool   `json:"public"`
}

type mcpNamespacesOut struct {
	Namespaces []mcpNamespaceMeta `json:"namespaces"`
}

type mcpNamespaceOut struct {
	Export      wiki.ExportConfig   `json:"export"`
	Name        string              `json:"name"`
	Widgets     []string            `json:"widgets"`
	Public      bool                `json:"public"`
	Title       string              `json:"title"`
	Description string              `json:"description"`
	Image       string              `json:"image"`
	Skin        string              `json:"skin"`
	Palette     string              `json:"palette"`
	Index       string              `json:"index"`
	Tree        []string            `json:"tree"`
	New         *wiki.NewPageConfig `json:"new,omitempty"`
	Hash        string              `json:"hash"`
}

type mcpSaveNamespaceIn struct {
	Name        string              `json:"name"`
	BaseHash    string              `json:"basehash,omitempty"`
	Public      *bool               `json:"public,omitempty"`
	Title       *string             `json:"title,omitempty"`
	Description *string             `json:"description,omitempty"`
	Image       *string             `json:"image,omitempty"`
	Skin        *string             `json:"skin,omitempty"`
	Palette     *string             `json:"palette,omitempty"`
	Index       *string             `json:"index,omitempty"`
	Widgets     *[]string           `json:"widgets,omitempty"`
	Tree        *[]string           `json:"tree,omitempty"`
	New         *wiki.NewPageConfig `json:"new,omitempty"`
	Export      *wiki.ExportConfig  `json:"export,omitempty"`
}

func mcpNamespaceOutput(name string, cfg wiki.NamespaceConfig, hash string) mcpNamespaceOut {
	return mcpNamespaceOut{
		Export: cfg.Export,
		Name:   name, Widgets: nonNil(cfg.Widgets), Public: cfg.Public, Title: cfg.Title, Description: cfg.Description, Image: cfg.Image,
		Skin: cfg.Skin, Palette: cfg.Palette, Index: cfg.Index, Tree: nonNil(cfg.Tree), New: cfg.New, Hash: hash,
	}
}

func mcpNamespaceFromDetail(detail *api.NamespaceDetail) mcpNamespaceOut {
	return mcpNamespaceOutput(detail.Name, detail.Config, detail.Hash)
}

// mergeNamespaceConfig applies the fields a save_namespace call sets onto cfg.
func mergeNamespaceConfig(cfg wiki.NamespaceConfig, in mcpSaveNamespaceIn) wiki.NamespaceConfig {
	set := func(field *string, value *string) {
		if value != nil {
			*field = *value
		}
	}
	set(&cfg.Title, in.Title)
	set(&cfg.Description, in.Description)
	set(&cfg.Image, in.Image)
	set(&cfg.Skin, in.Skin)
	set(&cfg.Palette, in.Palette)
	set(&cfg.Index, in.Index)
	if in.Public != nil {
		cfg.Public = *in.Public
	}
	if in.Widgets != nil {
		cfg.Widgets = *in.Widgets
	}
	if in.Tree != nil {
		cfg.Tree = *in.Tree
	}
	if in.New != nil {
		cfg.New = in.New
		if *in.New == (wiki.NewPageConfig{}) {
			cfg.New = nil
		}
	}
	if in.Export != nil {
		cfg.Export = *in.Export
	}
	return cfg
}

func (s *Server) registerNamespaces(server *sdk.Server) {
	addTool(s, server, "list_namespaces", func(ctx context.Context, req *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, mcpNamespacesOut, error) {
		out := mcpNamespacesOut{Namespaces: []mcpNamespaceMeta{}}
		for _, summary := range s.api.ListNamespaces(ctx) {
			out.Namespaces = append(out.Namespaces, mcpNamespaceMeta{Name: summary.Name, Description: summary.Config.Description, Pages: summary.Count, Public: summary.Config.Public})
		}
		return nil, out, nil
	})

	addTool(s, server, "read_namespace", func(ctx context.Context, req *sdk.CallToolRequest, in mcpNamespaceIn) (*sdk.CallToolResult, mcpNamespaceOut, error) {
		detail, err := s.api.ReadNamespace(ctx, in.Name)
		if err != nil {
			return nil, mcpNamespaceOut{}, err
		}
		return nil, mcpNamespaceFromDetail(detail), nil
	})

	addTool(s, server, "save_namespace", func(ctx context.Context, req *sdk.CallToolRequest, in mcpSaveNamespaceIn) (*sdk.CallToolResult, mcpNamespaceOut, error) {
		if err := wiki.CheckNamespaceName(in.Name); err != nil {
			return nil, mcpNamespaceOut{}, fmt.Errorf("invalid namespace %q: %w", in.Name, err)
		}
		if err := s.requireNamespace(ctx, in.Name); err != nil {
			return nil, mcpNamespaceOut{}, err
		}
		var cfg wiki.NamespaceConfig
		if in.BaseHash != "" {
			// Start from the revision the caller read; the checked save fails
			// unless it is still current.
			current, err := s.api.ReadNamespace(ctx, in.Name)
			if api.CategoryOf(err) == api.CategoryNotFound {
				return nil, mcpNamespaceOut{}, mcpConflict("the namespace no longer exists", "")
			}
			if err != nil {
				return nil, mcpNamespaceOut{}, err
			}
			if current.Hash != in.BaseHash {
				return nil, mcpNamespaceOut{}, mcpConflict("the namespace changed since basehash", current.Hash)
			}
			cfg = current.Config
		}
		detail, err := s.api.SaveNamespace(ctx, api.SaveNamespaceInput{
			Name:         in.Name,
			Config:       mergeNamespaceConfig(cfg, in),
			TemplateData: api.NewPageTemplateData{Now: time.Now(), User: s.user(ctx), Namespace: in.Name},
			BaseHash:     in.BaseHash,
		})
		if api.CategoryOf(err) == api.CategoryConflict {
			hash := ""
			if current, readErr := s.api.ReadNamespace(ctx, in.Name); readErr == nil {
				hash = current.Hash
			}
			if in.BaseHash == "" {
				return nil, mcpNamespaceOut{}, mcpConflict("the namespace already has settings; read them and pass their hash to update", hash)
			}
			return nil, mcpNamespaceOut{}, mcpConflict("the namespace changed since basehash", hash)
		}
		if err != nil {
			return nil, mcpNamespaceOut{}, err
		}
		slog.Info("mcp namespace configured", "namespace", in.Name, "author", s.user(ctx))
		return nil, mcpNamespaceFromDetail(detail), nil
	})
}
