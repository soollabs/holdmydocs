package main

import (
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"My Page Name", "my-page-name"},
		{"Hello,  World!", "hello-world"},
		{"already-a-slug", "already-a-slug"},
		{"  spaces  ", "spaces"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := Slugify(tt.input)
			if got != tt.expected {
				t.Errorf("Slugify(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestParseEncodeRoundTrip(t *testing.T) {
	tests := []struct {
		name     string
		slug     string
		rawInput string
	}{
		{
			name:     "with frontmatter",
			slug:     "test",
			rawInput: "---\ntitle: Test Page\n---\nThis is the body.",
		},
		{
			name:     "without frontmatter",
			slug:     "my-page",
			rawInput: "Just some markdown body.",
		},
		{
			name:     "with tags",
			slug:     "tagged",
			rawInput: "---\ntitle: Tagged Page\ntags: go, wiki\n---\nBody with tags.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed := ParsePage(tt.slug, []byte(tt.rawInput))
			encoded := parsed.Encode()
			reparsed := ParsePage(tt.slug, encoded)

			if parsed.Slug != reparsed.Slug {
				t.Errorf("Slug mismatch: %q != %q", parsed.Slug, reparsed.Slug)
			}
			if parsed.Title != reparsed.Title {
				t.Errorf("Title mismatch: %q != %q", parsed.Title, reparsed.Title)
			}
			if parsed.Body != reparsed.Body {
				t.Errorf("Body mismatch:\n%q\n!=\n%q", parsed.Body, reparsed.Body)
			}
		})
	}

	if got := ParsePage("tagged", []byte(tests[len(tests)-1].rawInput)); len(got.Tags) != 2 || got.Tags[0] != "go" || got.Tags[1] != "wiki" {
		t.Errorf("Tags = %v, want [go wiki]", got.Tags)
	}

	t.Run("widget frontmatter keys round-trip", func(t *testing.T) {
		p := Page{
			Slug: "clip", Title: "Clip", Body: "Body.",
			Pin: true, Unread: true, Source: "https://example.com/a", Author: "Jane", ReadTime: "4 min",
		}
		reparsed := ParsePage("clip", p.Encode())
		if !reparsed.Pin || !reparsed.Unread || reparsed.Source != p.Source || reparsed.Author != p.Author || reparsed.ReadTime != p.ReadTime {
			t.Errorf("round-trip mismatch: got %+v, want Pin/Unread=true Source=%q Author=%q ReadTime=%q",
				reparsed, p.Source, p.Author, p.ReadTime)
		}
	})

	t.Run("widget frontmatter keys omitted when unset", func(t *testing.T) {
		p := Page{Slug: "plain", Title: "Plain", Body: "Body."}
		encoded := string(p.Encode())
		for _, key := range []string{"pin:", "unread:", "source:", "author:", "read_time:"} {
			if strings.Contains(encoded, key) {
				t.Errorf("Encode() with unset %s should omit it, got:\n%s", key, encoded)
			}
		}
	})

	t.Run("encode omits tags line when empty", func(t *testing.T) {
		p := Page{Slug: "no-tags", Title: "No Tags", Body: "Body."}
		encoded := p.Encode()
		if strings.Contains(string(encoded), "tags:") {
			t.Errorf("Encode() with no tags should omit the tags line, got:\n%s", encoded)
		}
	})

	t.Run("encode includes tags line when present", func(t *testing.T) {
		p := Page{Slug: "has-tags", Title: "Has Tags", Tags: []string{"go", "wiki"}, Body: "Body."}
		encoded := p.Encode()
		if !strings.Contains(string(encoded), "tags: go, wiki\n") {
			t.Errorf("Encode() should include 'tags: go, wiki', got:\n%s", encoded)
		}
	})

	// Test fallback to slug when no title in frontmatter
	t.Run("no frontmatter title falls back to slug", func(t *testing.T) {
		parsed := ParsePage("my-page", []byte("Just body"))
		if parsed.Title != "my-page" {
			t.Errorf("Title = %q, want %q", parsed.Title, "my-page")
		}
	})

	// CRLF line endings must not break frontmatter parsing
	t.Run("CRLF frontmatter parses", func(t *testing.T) {
		parsed := ParsePage("crlf", []byte("---\r\ntitle: CRLF Page\r\ntags: a, b\r\n---\r\n\r\nBody text"))
		if parsed.Title != "CRLF Page" {
			t.Errorf("Title = %q, want %q", parsed.Title, "CRLF Page")
		}
		if len(parsed.Tags) != 2 {
			t.Errorf("Tags = %v, want 2 tags", parsed.Tags)
		}
		if parsed.Body != "Body text" {
			t.Errorf("Body = %q, want %q", parsed.Body, "Body text")
		}
	})

	// Encode must store LF even when the body arrives with CRLF (browser form submission)
	t.Run("Encode normalises CRLF body", func(t *testing.T) {
		encoded := Page{Slug: "p", Title: "p", Body: "line one\r\nline two"}.Encode()
		if strings.Contains(string(encoded), "\r") {
			t.Errorf("Encode() output contains CR: %q", encoded)
		}
	})
}

func TestParseTags(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"foo, bar, baz", []string{"foo", "bar", "baz"}},
		{"foo,bar", []string{"foo", "bar"}},
		{"  spaced  , tags  ", []string{"spaced", "tags"}},
		{"foo,, bar", []string{"foo", "bar"}},
		{"", nil},
		{"   ", nil},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := ParseTags(tt.input)
			if len(got) != len(tt.expected) {
				t.Fatalf("ParseTags(%q) = %v, want %v", tt.input, got, tt.expected)
			}
			for i := range got {
				if got[i] != tt.expected[i] {
					t.Errorf("ParseTags(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.expected[i])
				}
			}
		})
	}
}

func TestWikiLinks(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{
			"see [[Alpha]] and [[Beta Two]] and [[Alpha]] again",
			[]string{"Alpha", "Beta Two"},
		},
		{
			"no links here",
			[]string{},
		},
		{
			"[[Single]]",
			[]string{"Single"},
		},
	}

	for _, tt := range tests {
		t.Run(strings.TrimPrefix(tt.input, " "), func(t *testing.T) {
			got := WikiLinks(tt.input)
			if len(got) != len(tt.expected) {
				t.Fatalf("WikiLinks length: got %d, want %d", len(got), len(tt.expected))
			}
			for i, link := range got {
				if link != tt.expected[i] {
					t.Errorf("Link %d: got %q, want %q", i, link, tt.expected[i])
				}
			}
		})
	}
}
