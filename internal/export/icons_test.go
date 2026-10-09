package export

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hmd/internal/wiki"
)

type iconTransport func(*http.Request) (*http.Response, error)

func (f iconTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const testIconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512"><!-- Font Awesome licence notice --><path d="M0 0h512v512z"/></svg>`

func TestBundleIcons(t *testing.T) {
	out := t.TempDir()
	calls := 0
	transport := iconTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != iconSource+"brands/github.svg" || r.Header.Get("Authorization") != "" {
			t.Fatalf("unexpected icon request: %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(testIconSVG)), Header: http.Header{}}, nil
	})
	link := wiki.ExportLink{Icon: "fa-brands fa-github", IconOnly: true}
	if err := bundleIcons(context.Background(), []wiki.ExportLink{link, link, {Icon: "fa-solid fa-heart"}}, out, transport); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("expected one unique icon download, got %d", calls)
	}
	svg, err := os.ReadFile(filepath.Join(out, "export-icons", "brands-github.svg"))
	if err != nil || string(svg) != testIconSVG {
		t.Fatalf("original SVG and licence notice not retained: %v", err)
	}
	css, err := os.ReadFile(filepath.Join(out, "export-icons.css"))
	if err != nil || !strings.Contains(string(css), ".fa-brands.fa-github::before") || !strings.Contains(string(css), `url("export-icons/brands-github.svg")`) || strings.Contains(string(css), "https:") {
		t.Fatalf("expected local icon stylesheet: %s, %v", css, err)
	}
	notice, err := os.ReadFile(filepath.Join(out, "export-icons", "NOTICE.txt"))
	if err != nil || !strings.Contains(string(notice), "CC BY 4.0") {
		t.Fatalf("missing icon attribution: %v", err)
	}
}

func TestBundleIconsNoIcons(t *testing.T) {
	out := t.TempDir()
	transport := iconTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("text-only export made an icon request")
		return nil, nil
	})
	if err := bundleIcons(context.Background(), []wiki.ExportLink{{Icon: "fa-solid fa-heart"}}, out, transport); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "export-icons.css")); !os.IsNotExist(err) {
		t.Fatal("text-only export wrote icon assets")
	}
}

func TestBundleIconsFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		err    error
	}{
		{"missing", 404, "", nil},
		{"redirect", 302, "", nil},
		{"oversized", 200, strings.Repeat("x", maxIconBytes+1), nil},
		{"active", 200, `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`, nil},
		{"unavailable", 0, "", errors.New("offline")},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			transport := iconTransport(func(*http.Request) (*http.Response, error) {
				calls++
				if test.err != nil {
					return nil, test.err
				}
				return &http.Response{StatusCode: test.status, Header: http.Header{"Location": {"http://127.0.0.1/private"}}, Body: io.NopCloser(strings.NewReader(test.body))}, test.err
			})
			out := t.TempDir()
			err := bundleIcons(context.Background(), []wiki.ExportLink{{Icon: "fa-solid fa-heart", IconOnly: true}}, out, transport)
			if err == nil || calls != 1 {
				t.Fatalf("expected a failure without redirect fetch: calls=%d, err=%v", calls, err)
			}
			if _, err := os.Stat(filepath.Join(out, "export-icons.css")); !os.IsNotExist(err) {
				t.Fatal("failed download produced an icon stylesheet")
			}
		})
	}
}

func TestValidateIconSVG(t *testing.T) {
	for _, data := range []string{
		`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"><path d="M0 0"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><foreignObject/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><path fill="url(https://example.org)" d="M0 0"/></svg>`,
		`<!DOCTYPE svg><svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0"/></svg>`,
		testIconSVG + testIconSVG, `<svg/>`, `<html/>`,
	} {
		if err := validateIconSVG([]byte(data)); err == nil {
			t.Errorf("unsafe or malformed SVG accepted: %s", data)
		}
	}
}
