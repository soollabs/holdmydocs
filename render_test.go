package main

import (
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	tests := []struct {
		name            string
		exists          func(string) bool
		input           string
		wantContains    []string
		wantNotContains []string
	}{
		{
			name:         "GFM table",
			exists:       func(s string) bool { return false },
			input:        "| A | B |\n|---|---|\n| 1 | 2 |",
			wantContains: []string{"<table>"},
		},
		{
			name:            "mermaid block",
			exists:          func(s string) bool { return false },
			input:           "```mermaid\ngraph TD;\n```",
			wantContains:    []string{"<pre class=\"mermaid\">", "graph TD;"},
			wantNotContains: []string{"language-mermaid"},
		},
		{
			name:            "existing wiki link",
			exists:          func(s string) bool { return s == "alpha" },
			input:           "go to [[Alpha]]",
			wantContains:    []string{"class=\"wiki\"", "href=\"/page/alpha\"", "<span class=\"br\">[[</span>Alpha<span class=\"br\">]]</span>"},
			wantNotContains: []string{"class=\"missing"},
		},
		{
			name:         "missing wiki link",
			exists:       func(s string) bool { return s == "alpha" },
			input:        "go to [[Nowhere]]",
			wantContains: []string{"class=\"missing wiki\"", "href=\"/page/nowhere\"", "<span class=\"missing-suffix\">+</span>"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRenderer(tt.exists)
			output, err := r.Render(tt.input)
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
