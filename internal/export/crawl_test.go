package export

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hmd/internal/wiki"
)

func TestCrawlFiles(t *testing.T) {
	for _, sitemap := range []bool{true, false} {
		t.Run(map[bool]string{true: "sitemap", false: "no sitemap"}[sitemap], func(t *testing.T) {
			req := NamespaceRequest{OutDir: t.TempDir(), Config: wiki.NamespaceConfig{Index: "home", Export: wiki.ExportConfig{BaseURL: "https://docs.example.org/prefix/", Sitemap: &sitemap}}}
			pages := []wiki.Page{{Slug: "docs/home"}, {Slug: "docs/nested/café"}, {Slug: "docs/amp&ersand"}}
			if err := writeCrawlFiles(req, pages); err != nil {
				t.Fatal(err)
			}
			robots, err := os.ReadFile(filepath.Join(req.OutDir, "robots.txt"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(robots), "User-agent: *\nDisallow: /\n") {
				t.Fatalf("robots = %s", robots)
			}
			data, err := os.ReadFile(filepath.Join(req.OutDir, "sitemap.xml"))
			if !sitemap {
				if !os.IsNotExist(err) || strings.Contains(string(robots), "Sitemap:") {
					t.Fatal("disabled sitemap generated")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				URLs []struct {
					Loc string `xml:"loc"`
				} `xml:"url"`
			}
			if err := xml.Unmarshal(data, &doc); err != nil {
				t.Fatal(err)
			}
			if len(doc.URLs) != 3 || doc.URLs[0].Loc != "https://docs.example.org/prefix/" || doc.URLs[1].Loc != "https://docs.example.org/prefix/nested/caf%C3%A9/" {
				t.Fatalf("sitemap = %s", data)
			}
		})
	}
}

func TestMissingExportURLDoesNotWrite(t *testing.T) {
	out := filepath.Join(t.TempDir(), "output")
	err := Namespace(NamespaceRequest{OutDir: out, Namespace: "docs", Pages: []wiki.Page{{Slug: "docs/home"}}}, nil)
	if err == nil || !strings.Contains(err.Error(), "export base URL is required") {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("failed export created output")
	}
}

func TestCrawlFilesDisableExistingSitemap(t *testing.T) {
	req := NamespaceRequest{OutDir: t.TempDir(), Config: wiki.NamespaceConfig{Export: wiki.ExportConfig{BaseURL: "https://docs.example.org/"}}}
	pages := []wiki.Page{{Slug: "docs/home"}}
	if err := writeCrawlFiles(req, pages); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(req.OutDir, "sitemap.xml")); err != nil {
		t.Fatal(err)
	}
	disabled := false
	req.Config.Export.Sitemap = &disabled
	for range 2 {
		if err := writeCrawlFiles(req, pages); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(req.OutDir, "sitemap.xml")); !os.IsNotExist(err) {
			t.Fatalf("disabled sitemap remains: %v", err)
		}
		robots, err := os.ReadFile(filepath.Join(req.OutDir, "robots.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(robots), "Sitemap:") {
			t.Fatal("robots still advertises disabled sitemap")
		}
	}
}

func TestNamespaceRejectsCrawlPathCollisionsBeforeWriting(t *testing.T) {
	for _, sitemap := range []bool{true, false} {
		for _, rest := range []string{"robots.txt", "robots.txt/guide", "sitemap.xml", "sitemap.xml/guide", "Robots.txt", "SITEMAP.XML/guide"} {
			t.Run(fmt.Sprintf("%s/sitemap=%t", rest, sitemap), func(t *testing.T) {
				out := filepath.Join(t.TempDir(), "output")
				slug := "docs/" + rest
				if !wiki.ValidPageSlug(slug) {
					t.Fatalf("fixture is not a valid page slug: %s", slug)
				}
				err := Namespace(NamespaceRequest{
					OutDir: out, Namespace: "docs", Pages: []wiki.Page{{Slug: slug}},
					Config: wiki.NamespaceConfig{Export: wiki.ExportConfig{BaseURL: "https://docs.example.org/", Sitemap: &sitemap}},
				}, nil)
				if err == nil || !strings.Contains(err.Error(), "reserved static export path") || !strings.Contains(err.Error(), slug) {
					t.Fatalf("error = %v", err)
				}
				if _, err := os.Stat(out); !os.IsNotExist(err) {
					t.Fatal("rejected export created output")
				}
			})
		}
	}
}

func TestCrawlPagePathsAllowNestedNames(t *testing.T) {
	if err := validateCrawlPagePaths([]wiki.Page{{Slug: "docs/guide/robots.txt"}, {Slug: "docs/guide/sitemap.xml"}, {Slug: "docs/robots.txt-guide"}}); err != nil {
		t.Fatal(err)
	}
}
