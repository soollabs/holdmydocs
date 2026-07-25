package main

import (
	"regexp"
	"strings"
	"unicode"
)

type Page struct {
	Slug     string
	Title    string
	Tags     []string
	Body     string
	Public   bool // frontmatter `public: true` — served unauthenticated under /garden/
	Pin      bool // frontmatter `pin: true` — surfaced by the pinned widget
	Unread   bool // frontmatter `unread: true` — surfaced by the inbox widget
	Source   string
	Author   string
	ReadTime string
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

func ParseTags(s string) []string {
	var tags []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
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
		if strings.HasPrefix(line, "public:") {
			page.Public = strings.TrimSpace(strings.TrimPrefix(line, "public:")) == "true"
		}
		if strings.HasPrefix(line, "pin:") {
			page.Pin = strings.TrimSpace(strings.TrimPrefix(line, "pin:")) == "true"
		}
		if strings.HasPrefix(line, "unread:") {
			page.Unread = strings.TrimSpace(strings.TrimPrefix(line, "unread:")) == "true"
		}
		if strings.HasPrefix(line, "source:") {
			page.Source = strings.TrimSpace(strings.TrimPrefix(line, "source:"))
		}
		if strings.HasPrefix(line, "author:") {
			page.Author = strings.TrimSpace(strings.TrimPrefix(line, "author:"))
		}
		if strings.HasPrefix(line, "read_time:") {
			page.ReadTime = strings.TrimSpace(strings.TrimPrefix(line, "read_time:"))
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
	if p.Public {
		result += "public: true\n"
	}
	if p.Pin {
		result += "pin: true\n"
	}
	if p.Unread {
		result += "unread: true\n"
	}
	if p.Source != "" {
		result += "source: " + p.Source + "\n"
	}
	if p.Author != "" {
		result += "author: " + p.Author + "\n"
	}
	if p.ReadTime != "" {
		result += "read_time: " + p.ReadTime + "\n"
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
