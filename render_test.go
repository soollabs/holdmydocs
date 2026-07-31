package main

import (
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	tests := []struct {
		name            string
		resolve         func(title, ns string) (string, bool)
		input           string
		wantContains    []string
		wantNotContains []string
	}{
		{
			name:         "GFM table",
			resolve:      func(title, ns string) (string, bool) { return "", false },
			input:        "| A | B |\n|---|---|\n| 1 | 2 |",
			wantContains: []string{"<table>"},
		},
		{
			name:            "mermaid block",
			resolve:         func(title, ns string) (string, bool) { return "", false },
			input:           "```mermaid\ngraph TD;\n```",
			wantContains:    []string{"<pre class=\"mermaid\">", "graph TD;"},
			wantNotContains: []string{"language-mermaid"},
		},
		{
			name:            "existing wiki link",
			resolve:         func(title, ns string) (string, bool) { return "alpha", title == "Alpha" },
			input:           "go to [[Alpha]]",
			wantContains:    []string{"class=\"wiki\"", "href=\"/alpha\"", "<span class=\"br\">[[</span>Alpha<span class=\"br\">]]</span>"},
			wantNotContains: []string{"class=\"missing"},
		},
		{
			name:         "missing wiki link",
			resolve:      func(title, ns string) (string, bool) { return "alpha", title == "Alpha" },
			input:        "go to [[Nowhere]]",
			wantContains: []string{"class=\"missing wiki\"", "href=\"/nowhere\"", "<span class=\"missing-suffix\">+</span>"},
		},
		{
			name:            "wiki link title is escaped",
			resolve:         func(title, ns string) (string, bool) { return "", false },
			input:           `[[<img src=x onerror=alert(1)>]]`,
			wantContains:    []string{"&lt;img src=x onerror=alert(1)&gt;"},
			wantNotContains: []string{"<img src=x onerror=alert(1)>"},
		},
		{
			name:            "mermaid block content stays escaped",
			resolve:         func(title, ns string) (string, bool) { return "", false },
			input:           "```mermaid\n<script>alert(1)</script>\n```",
			wantContains:    []string{"&lt;script&gt;alert(1)&lt;/script&gt;"},
			wantNotContains: []string{"<script>alert(1)</script>"},
		},
		{
			name:            "raw script tag in page body is stripped",
			resolve:         func(title, ns string) (string, bool) { return "", false },
			input:           "hello\n\n<script>alert(document.cookie)</script>\n\nworld",
			wantNotContains: []string{"<script", "alert(document.cookie)"},
		},
		{
			name:            "raw event-handler attribute is stripped",
			resolve:         func(title, ns string) (string, bool) { return "", false },
			input:           `<img src="x" onerror="alert(1)">`,
			wantNotContains: []string{"onerror"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRenderer(tt.resolve)
			output, err := r.Render(tt.input, "")
			if err != nil {
				t.Fatalf("Render failed: %v", err)
			}

			html := string(output)
			for _, want := range tt.wantContains {
				if !strings.Contains(html, want) {
					t.Errorf("Output should contain %q, but got:\n%s", want, html)
				}
			}
			for _, notWant := range tt.wantNotContains {
				if strings.Contains(html, notWant) {
					t.Errorf("Output should NOT contain %q, but got:\n%s", notWant, html)
				}
			}
		})
	}
}
