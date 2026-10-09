package export

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"hmd/internal/store"
	"hmd/internal/wiki"
)

// NamespaceRequest describes one static namespace export. Pages is the full
// page set; Namespace selects the pages to export. Assets and AssetsRoot name
// the browser asset tree to copy alongside the HTML; when Assets is nil no
// assets are copied.
type NamespaceRequest struct {
	Pages         []wiki.Page
	Namespace     string
	Config        wiki.NamespaceConfig
	OutDir        string
	Title         string
	Assets        fs.FS
	AssetsRoot    string
	Store         *store.Store
	CSPNonce      string // populated only for an authenticated browser preview
	Context       context.Context
	IconTransport http.RoundTripper // nil uses the standard HTTP transport
}

// SearchEntry is one entry in the exported client-side search index.
type SearchEntry struct {
	Title string `json:"title"`
	Href  string `json:"href"`
	Text  string `json:"text"`
}

// NamespaceRenderer renders the HTML pages of a namespace export. The web
// package implements it, so template, theme and widget dependencies stay
// explicit at the caller. The request's Pages are already filtered to the
// namespace.
type NamespaceRenderer interface {
	RenderNamespace(request NamespaceRequest) ([]SearchEntry, error)
}

// Namespace coordinates a static namespace export: it filters the pages, checks
// the file budget, copies the browser assets and namespace attachments, and
// writes the client-side search index, delegating HTML rendering to the
// supplied renderer.
func Namespace(request NamespaceRequest, renderer NamespaceRenderer) error {
	settings, err := wiki.NormaliseExportConfig(request.Config.Export, true)
	if err != nil {
		return err
	}
	request.Config.Export = settings
	nsPages := make([]wiki.Page, 0, len(request.Pages))
	for _, page := range request.Pages {
		if ns, _ := wiki.NamespaceFor(page.Slug); ns == request.Namespace {
			nsPages = append(nsPages, page)
		}
	}
	if len(nsPages) == 0 {
		return fmt.Errorf("no pages found in namespace %q", request.Namespace)
	}
	if len(nsPages) > maxFiles {
		return fmt.Errorf("export exceeds %d files", maxFiles)
	}
	if err := validateCrawlPagePaths(nsPages); err != nil {
		return err
	}
	if request.Config.Index != "" {
		found := false
		for _, page := range nsPages {
			if page.Slug == request.Namespace+"/"+request.Config.Index {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("namespace index %q does not exist", request.Config.Index)
		}
	}
	if err := os.MkdirAll(request.OutDir, 0o755); err != nil {
		return fmt.Errorf("creating output dir: %w", err)
	}
	if request.Assets != nil {
		if err := CopyAssets(request.Assets, request.AssetsRoot, request.OutDir); err != nil {
			return err
		}
	}
	request.Pages = nsPages
	ctx := request.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if err := bundleIcons(ctx, settings.Links, request.OutDir, request.IconTransport); err != nil {
		return err
	}
	entries, err := renderer.RenderNamespace(request)
	if err != nil {
		return err
	}
	searchJSON, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("encoding search index: %w", err)
	}
	// A script, rather than fetched JSON, also works when opened via file://.
	if err := os.WriteFile(filepath.Join(request.OutDir, "search-index.js"), append([]byte("window.HMDSearchIndex = "), append(searchJSON, ';')...), 0o644); err != nil {
		return fmt.Errorf("writing search index: %w", err)
	}
	files, bytes := len(nsPages), int64(0)
	for _, page := range nsPages {
		bytes += int64(len(page.Body))
	}
	if err := CopyAttachments(request.Store, request.OutDir, request.Namespace, &files, &bytes); err != nil {
		return err
	}
	if err := writeCrawlFiles(request, nsPages); err != nil {
		return err
	}
	slog.Info("exported namespace", "namespace", request.Namespace, "pages", len(nsPages), "dir", request.OutDir)
	return nil
}
