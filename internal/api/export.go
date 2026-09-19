package api

import (
	"context"
	"fmt"
	"io/fs"
	"strings"

	"hmd/internal/export"
	"hmd/internal/wiki"
)

// ExportNamespace writes a static HTML export of one namespace to outDir,
// reading pages and attachments through the application store and delegating
// HTML rendering to the supplied presentation renderer. Assets names the
// browser asset tree copied alongside the HTML.
func (a *API) ExportNamespace(ctx context.Context, name, outDir string, assets fs.FS, assetsRoot string, renderer export.NamespaceRenderer) error {
	cfg, ok := a.Namespaces()[name]
	if !ok {
		return InvalidInput(fmt.Sprintf("unknown namespace %q", name), nil)
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
		Pages:      pages,
		Namespace:  name,
		Config:     cfg,
		OutDir:     outDir,
		Assets:     assets,
		AssetsRoot: assetsRoot,
		Store:      a.store,
	}
	if err := export.Namespace(request, renderer); err != nil {
		return Unavailable("exporting namespace", err)
	}
	return nil
}
