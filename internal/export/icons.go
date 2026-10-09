package export

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hmd/internal/wiki"
)

const iconSource = "https://raw.githubusercontent.com/FortAwesome/Font-Awesome/6.7.2/svgs/"
const maxIconBytes = 512 << 10

// bundleIcons downloads only the icons used by navigation. The fixed source,
// disabled redirects and SVG allow-list keep configuration from becoming an
// arbitrary network fetch or an active document on the preview origin.
func bundleIcons(ctx context.Context, links []wiki.ExportLink, outDir string, transport http.RoundTripper) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	client := &http.Client{
		Transport: transport, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	var css strings.Builder
	seen := map[string]bool{}
	for _, link := range links {
		if !link.IconOnly || link.Icon == "" || seen[link.Icon] {
			continue
		}
		seen[link.Icon] = true
		classes := strings.Fields(link.Icon)
		style, name := strings.TrimPrefix(classes[0], "fa-"), strings.TrimPrefix(classes[1], "fa-")
		filename := style + "-" + name + ".svg"
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, iconSource+style+"/"+name+".svg", nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("downloading icon %q: %w", link.Icon, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxIconBytes+1))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("downloading icon %q: HTTP %d", link.Icon, resp.StatusCode)
		}
		if readErr != nil {
			return fmt.Errorf("reading icon %q: %w", link.Icon, readErr)
		}
		if len(data) > maxIconBytes {
			return fmt.Errorf("icon %q exceeds the download limit", link.Icon)
		}
		if err := validateIconSVG(data); err != nil {
			return fmt.Errorf("invalid icon %q: %w", link.Icon, err)
		}
		dir := filepath.Join(outDir, "export-icons")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, filename), data, 0o644); err != nil {
			return err
		}
		fmt.Fprintf(&css, ".%s.%s::before{content:\"\";display:inline-block;width:1em;height:1em;vertical-align:-.125em;background-color:currentColor;-webkit-mask:url(\"export-icons/%s\") center/contain no-repeat;mask:url(\"export-icons/%s\") center/contain no-repeat}\n", classes[0], classes[1], filename, filename)
	}
	if css.Len() == 0 {
		return nil
	}
	if err := os.WriteFile(filepath.Join(outDir, "export-icons.css"), []byte(css.String()), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, "export-icons", "NOTICE.txt"), []byte("Font Awesome Free 6.7.2 icons by Fonticons, Inc.\nhttps://fontawesome.com\nIcons licensed under CC BY 4.0: https://creativecommons.org/licenses/by/4.0/\nSource: https://github.com/FortAwesome/Font-Awesome/tree/6.7.2/svgs\nSVGs are unmodified; original licence notices are retained.\n"), 0o644)
}

func validateIconSVG(data []byte) error {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	depth, roots, paths := 0, 0, 0
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		switch node := token.(type) {
		case xml.StartElement:
			if node.Name.Space != "http://www.w3.org/2000/svg" {
				return fmt.Errorf("unexpected SVG namespace")
			}
			if depth == 0 {
				roots++
				if node.Name.Local != "svg" {
					return fmt.Errorf("expected SVG root")
				}
			} else if node.Name.Local != "path" {
				return fmt.Errorf("unexpected SVG element")
			}
			if node.Name.Local == "path" {
				paths++
			}
			for _, attr := range node.Attr {
				if attr.Name.Space != "" {
					return fmt.Errorf("unexpected SVG attribute namespace")
				}
				switch attr.Name.Local {
				case "xmlns", "viewBox", "d", "fill", "width", "height":
					if strings.Contains(strings.ToLower(attr.Value), "url(") {
						return fmt.Errorf("SVG references are not permitted")
					}
				default:
					return fmt.Errorf("unexpected SVG attribute")
				}
			}
			depth++
		case xml.EndElement:
			depth--
		case xml.Directive, xml.ProcInst:
			return fmt.Errorf("SVG directives are not permitted")
		case xml.CharData:
			if strings.TrimSpace(string(node)) != "" {
				return fmt.Errorf("unexpected SVG text")
			}
		}
	}
	if roots != 1 || depth != 0 || paths == 0 {
		return fmt.Errorf("incomplete SVG")
	}
	return nil
}
