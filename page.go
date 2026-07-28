package main

import (
	"regexp"
	"strings"
	"unicode"
)

type Page struct {
	Slug  string
	Title string
	Tags  []string
	Body  string
	Pin   bool // frontmatter `pin: true` — surfaced by the pinned widget
}

// pageFile maps a slug to its on-disk filename for regular pages.
func pageFile(slug string) string {
	return slug + ".md"
}

// hiddenFile maps a slug to its dot-prefixed filename for hidden pages.
func hiddenFile(slug string) string {
	return "." + slug + ".md"
}

func Slugify(title string) string {
	// Convert to lowercase
	slug := strings.ToLower(title)

	// Replace non-alphanumeric with dashes
	var result []rune
	for _, r := range slug {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			result = append(result, r)
		} else if unicode.IsSpace(r) || !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			// Replace any non-alphanumeric with a dash
			if len(result) > 0 && result[len(result)-1] != '-' {
				result = append(result, '-')
			}
		}
	}

	// Trim leading and trailing dashes
	s := strings.Trim(string(result), "-")
	// Collapse multiple dashes into single dash
	s = regexp.MustCompile(`-+`).ReplaceAllString(s, "-")
	return s
}

// ParseTags reads the value half of a frontmatter "tags:" line. Both the
// plain form (tags: a, b) and YAML's flow sequence (tags: [a, b]) are
// accepted — the brackets are conventional enough that leaving them in
// produced tags literally named "[a" and "b]".
func ParseTags(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	var tags []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		part = strings.Trim(part, `"'`)
		if part != "" {
			tags = append(tags, part)
		}
	}
	return tags
}

func ParsePage(slug string, raw []byte) Page {
	page := Page{Slug: slug, Title: slug}
	// Normalise CRLF so frontmatter parsing works regardless of line endings
	content := strings.ReplaceAll(string(raw), "\r\n", "\n")

	// Check if starts with frontmatter
	if !strings.HasPrefix(content, "---\n") {
		page.Body = strings.TrimRight(content, "\n")
		return page
	}

	// Find closing ---
	lines := strings.Split(content, "\n")
	closingIdx := -1
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			closingIdx = i
			break
		}
	}

	if closingIdx == -1 {
		// No closing delimiter, treat as body
		page.Body = strings.TrimRight(content, "\n")
		return page
	}

	// Extract title and tags from frontmatter
	for i := 1; i < closingIdx; i++ {
		line := lines[i]
		if strings.HasPrefix(line, "title:") {
			title := strings.TrimPrefix(line, "title:")
			page.Title = strings.TrimSpace(title)
		}
		if strings.HasPrefix(line, "tags:") {
			page.Tags = ParseTags(strings.TrimPrefix(line, "tags:"))
		}
		if strings.HasPrefix(line, "pin:") {
			page.Pin = strings.TrimSpace(strings.TrimPrefix(line, "pin:")) == "true"
		}
	}

	// Extract body (everything after closing --- and optional blank line)
	bodyStart := closingIdx + 1
	if bodyStart < len(lines) && lines[bodyStart] == "" {
		bodyStart++
	}
	page.Body = strings.TrimRight(strings.Join(lines[bodyStart:], "\n"), "\n")

	return page
}

func (p Page) Encode() []byte {
	// Browsers submit textarea content with CRLF; normalise so files are stored with LF
	body := strings.ReplaceAll(p.Body, "\r\n", "\n")
	result := "---\n"
	result += "title: " + p.Title + "\n"
	if len(p.Tags) > 0 {
		result += "tags: " + strings.Join(p.Tags, ", ") + "\n"
	}
	if p.Pin {
		result += "pin: true\n"
	}
	result += "---\n\n"
	result += body
	if !strings.HasSuffix(body, "\n") {
		result += "\n"
	}
	return []byte(result)
}

// wikiLinkOrCodeRe matches a fenced code block, an inline code span, or a
// [[wiki-link]] title. Code alternatives have no capturing group, so a
// [[...]] used as a markdown syntax example inside code is matched but not
// captured — WikiLinks and processWikiLinks (render.go) both treat an empty
// capture group as "this was code, not a real link".
var wikiLinkOrCodeRe = regexp.MustCompile("(?s)```.*?```|`[^`\n]*`|\\[\\[([^\\[\\]]+)\\]\\]")

func WikiLinks(body string) []string {
	matches := wikiLinkOrCodeRe.FindAllStringSubmatch(body, -1)

	seen := make(map[string]bool)
	var links []string

	for _, match := range matches {
		if len(match) > 1 && match[1] != "" {
			link := match[1]
			if !seen[link] {
				links = append(links, link)
				seen[link] = true
			}
		}
	}

	return links
}
