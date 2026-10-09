package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"hmd/internal/api"
	"hmd/internal/presentation"
	"hmd/internal/wiki"
)

// namespaceSaveInput is the JSON body of the namespace editor. It carries the
// submitted settings and the base hash for a checked write; an empty BaseHash
// creates the namespace.
type namespaceSaveInput struct {
	Export      wiki.ExportConfig `json:"export"`
	Name        string            `json:"name"`
	Template    string            `json:"template"`
	BaseHash    string            `json:"base_hash"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Index       string            `json:"index"`
	Tree        []string          `json:"tree"`
	Public      bool              `json:"public"`
	Skin        string            `json:"skin"`
	Palette     string            `json:"palette"`
	NewEnabled  bool              `json:"new_enabled"`
	SlugPreset  string            `json:"slug_preset"`
	SlugCustom  string            `json:"slug_custom"`
	Widgets     []string          `json:"widgets"`
}

// namespaceNameInput names one namespace for a reset or delete.
type namespaceNameInput struct {
	Name string `json:"name"`
}

// namespaceSaved is the JSON result of a committed namespace write.
type namespaceSaved struct {
	Name string `json:"name"`
	Hash string `json:"hash"`
}

// namespaceConflictConfig is the current committed namespace configuration in a
// rejected checked write. The browser uses it to show what it collided with
// while preserving the user's submitted settings.
type namespaceConflictConfig struct {
	Export      wiki.ExportConfig     `json:"export"`
	Widgets     []string              `json:"widgets,omitempty"`
	Public      bool                  `json:"public"`
	Title       string                `json:"title,omitempty"`
	Description string                `json:"description,omitempty"`
	Skin        string                `json:"skin,omitempty"`
	Palette     string                `json:"palette,omitempty"`
	Index       string                `json:"index,omitempty"`
	Tree        []string              `json:"tree,omitempty"`
	New         *namespaceConflictNew `json:"new,omitempty"`
}

// namespaceConflictNew is the current committed quick-create state.
type namespaceConflictNew struct {
	Enabled  bool   `json:"enabled"`
	Template string `json:"template,omitempty"`
	Slug     string `json:"slug,omitempty"`
}

// namespaceConflict is the current committed namespace a checked write
// collided with.
type namespaceConflict struct {
	BaseHash    string                  `json:"base_hash"`
	CurrentHash string                  `json:"current_hash"`
	Name        string                  `json:"name"`
	Config      namespaceConflictConfig `json:"config"`
}

// namespaceConflictResponse is the 409 JSON body for a rejected namespace write.
type namespaceConflictResponse struct {
	Error    string            `json:"error"`
	Conflict namespaceConflict `json:"conflict"`
}

// saveNamespace handles create and update for a namespace configuration. It is
// concrete rather than generic because a conflict returns the current settings,
// not just an error message.
func (h *Handlers) saveNamespace(w http.ResponseWriter, r *http.Request) {
	var in namespaceSaveInput
	if err := decodeInput(w, r, &in); err != nil {
		writeAPIError(w, err)
		return
	}
	name := strings.Trim(strings.TrimSpace(in.Name), "/")
	if !wiki.ValidNamespaceName(name) {
		writeAPIError(w, api.InvalidInput("invalid namespace name", nil))
		return
	}

	cfg := wiki.NamespaceConfig{
		Export:      in.Export,
		Widgets:     in.Widgets,
		Public:      in.Public,
		Title:       in.Title,
		Description: in.Description,
		Skin:        in.Skin,
		Palette:     in.Palette,
		Index:       in.Index,
		Tree:        in.Tree,
	}
	if in.NewEnabled {
		slug := presentation.SlugPatternFor(in.SlugPreset)
		if slug == "" {
			slug = strings.TrimSpace(in.SlugCustom)
		}
		if slug == "" {
			writeAPIError(w, api.InvalidInput("choose how new pages here are named, or give a custom pattern", nil))
			return
		}
		template := strings.TrimSpace(in.Template)
		if template == "" {
			template = wiki.DefaultNewPageTemplate
		}
		cfg.New = &wiki.NewPageConfig{Template: template, Slug: slug}
	}

	// Whether this save brings the namespace into being decides if it gets a
	// template page seeded; seeding never overwrites an existing template.
	creating := !h.api.Namespaces()[name].Configured
	templateName := strings.TrimSpace(in.Template)
	if templateName == "" {
		templateName = wiki.DefaultNewPageTemplate
	}
	username, _ := api.Username(r.Context())

	detail, err := h.api.SaveNamespace(r.Context(), api.SaveNamespaceInput{
		Name:   name,
		Config: cfg,
		TemplateData: api.NewPageTemplateData{
			Now: time.Now(), User: username, Namespace: name,
		},
		BaseHash:     in.BaseHash,
		SeedTemplate: creating || cfg.New != nil,
		TemplateName: templateName,
	})
	if err != nil {
		if api.CategoryOf(err) == api.CategoryConflict {
			h.writeNamespaceConflict(w, r, name, in.BaseHash)
			return
		}
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, namespaceSaved{Name: detail.Name, Hash: detail.Hash})
}

// writeNamespaceConflict reads the current committed namespace and returns it
// with the caller's stale base hash, so the browser can keep the submitted
// settings and offer to save again.
func (h *Handlers) writeNamespaceConflict(w http.ResponseWriter, r *http.Request, name, baseHash string) {
	conflict := namespaceConflict{BaseHash: baseHash, Name: name}
	if detail, err := h.api.ReadNamespace(r.Context(), name); err == nil {
		conflict.Name = detail.Name
		conflict.CurrentHash = detail.Hash
		conflict.Config = namespaceConflictConfig{
			Export:      detail.Config.Export,
			Widgets:     detail.Config.Widgets,
			Public:      detail.Config.Public,
			Title:       detail.Config.Title,
			Description: detail.Config.Description,
			Skin:        detail.Config.Skin,
			Palette:     detail.Config.Palette,
			Index:       detail.Config.Index,
			Tree:        detail.Config.Tree,
		}
		if detail.Config.New != nil {
			conflict.Config.New = &namespaceConflictNew{
				Enabled:  true,
				Template: detail.Config.New.Template,
				Slug:     detail.Config.New.Slug,
			}
		}
	}
	writeJSON(w, http.StatusConflict, namespaceConflictResponse{
		Error:    "the namespace changed since base hash, or already exists",
		Conflict: conflict,
	})
}

// resetNamespace removes a namespace's configuration file.
func (h *Handlers) resetNamespace(ctx context.Context, in namespaceNameInput) (okResult, error) {
	if err := h.api.ResetNamespace(ctx, strings.Trim(strings.TrimSpace(in.Name), "/")); err != nil {
		return okResult{}, err
	}
	return okResult{OK: true}, nil
}

// deleteNamespace removes a configured namespace's configuration only.
func (h *Handlers) deleteNamespace(ctx context.Context, in namespaceNameInput) (okResult, error) {
	if err := h.api.DeleteNamespace(ctx, strings.TrimSpace(in.Name)); err != nil {
		return okResult{}, err
	}
	return okResult{OK: true}, nil
}

// deleteNamespaceAll removes a configured namespace and every file beneath it.
func (h *Handlers) deleteNamespaceAll(ctx context.Context, in namespaceNameInput) (okResult, error) {
	if err := h.api.DeleteNamespaceAll(ctx, strings.TrimSpace(in.Name)); err != nil {
		return okResult{}, err
	}
	return okResult{OK: true}, nil
}

// registerNamespaces mounts the namespace mutation endpoints.
func (h *Handlers) registerNamespaces(mux *http.ServeMux) {
	mux.HandleFunc("POST /_/api/namespaces", h.saveNamespace)
	h.registerJSON(mux, "POST /_/api/namespaces/reset", h.resetNamespace)
	h.registerJSON(mux, "POST /_/api/namespaces/delete", h.deleteNamespace)
	h.registerJSON(mux, "POST /_/api/namespaces/delete-all", h.deleteNamespaceAll)
}
