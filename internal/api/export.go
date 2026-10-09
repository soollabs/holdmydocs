package api

import (
	"context"
	"fmt"
	"io/fs"
	"strings"

	"hmd/internal/export"
	"hmd/internal/wiki"
)

// ExportOptions carries download overrides and the preview's script/style nonce.
type ExportOptions struct {
	BaseURL  string
	CSPNonce string
}

// ExportNamespace writes a static HTML export of one namespace to outDir,
// reading pages and attachments through the application store and delegating
// HTML rendering to the supplied presentation renderer. Assets names the
// browser asset tree copied alongside the HTML.
func (a *API) ExportNamespace(ctx context.Context, name, outDir string, assets fs.FS, assetsRoot string, renderer export.NamespaceRenderer, options ...ExportOptions) error {
	if err := a.RequireScope(ctx, ScopeSettings); err != nil {
		return err
	}
	if !AllowNamespace(ctx, name) {
		return Forbidden("namespace access denied")
	}
	cfg, ok := a.Namespaces()[name]
	if !ok {
		return InvalidInput(fmt.Sprintf("unknown namespace %q", name), nil)
	}
	if len(options) > 0 {
		cfg.Export.BaseURL = options[0].BaseURL
	}
	paths, err := a.store.List()
	if err != nil {
		return Unavailable("listing export pages", err)
	}
	pages := make([]wiki.Page, 0, len(paths))
	for _, path := range paths {
		slug := strings.TrimSuffix(path, ".md")
		if ns, _ := wiki.NamespaceFor(slug); ns != name {
			continue
		}
		content, _, err := a.store.Read(path)
		if err != nil {
			return Unavailable("reading export page", err)
		}
		pages = append(pages, wiki.ParsePage(slug, content))
	}
	request := export.NamespaceRequest{
		Context:    ctx,
		Pages:      pages,
		Namespace:  name,
		Config:     cfg,
		OutDir:     outDir,
		Assets:     assets,
		AssetsRoot: assetsRoot,
		Store:      a.store,
	}
	if len(options) > 0 {
		request.CSPNonce = options[0].CSPNonce
	}
	if err := export.Namespace(request, renderer); err != nil {
		return Unavailable("exporting namespace", err)
	}
	return nil
}
