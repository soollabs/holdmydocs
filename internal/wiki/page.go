package wiki

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
func PageFile(slug string) string {
	return slug + ".md"
}

// hiddenFile maps a slug to a filename whose basename is dot-prefixed.
func HiddenFile(slug string) string {
	i := strings.LastIndexByte(slug, '/')
	if i == -1 {
		return "." + slug + ".md"
	}
	return slug[:i+1] + "." + slug[i+1:] + ".md"
}

func HiddenSlug(path string) string {
	path = strings.TrimSuffix(path, ".md")
	i := strings.LastIndexByte(path, '/')
	if i == -1 {
		return strings.TrimPrefix(path, ".")
	}
	return path[:i+1] + strings.TrimPrefix(path[i+1:], ".")
}

func Slugify(title string) string {
	slug := strings.ToLower(title)

	var result []rune
	for _, r := range slug {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			result = append(result, r)
		} else if unicode.IsSpace(r) || !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			if len(result) > 0 && result[len(result)-1] != '-' {
				result = append(result, '-')
			}
		}
	}

	s := strings.Trim(string(result), "-")

	s = regexp.MustCompile(`-+`).ReplaceAllString(s, "-")
	return s
}

// ParseTags reads the value half of a frontmatter "tags:" line.
func ParseTags(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	var tags []string
	for part := range strings.SplitSeq(s, ",") {
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

	content := strings.ReplaceAll(string(raw), "\r\n", "\n")

	if !strings.HasPrefix(content, "---\n") {
		page.Body = strings.TrimRight(content, "\n")
		return page
	}

	lines := strings.Split(content, "\n")
	closingIdx := -1
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			closingIdx = i
			break
		}
	}

	if closingIdx == -1 {
		page.Body = strings.TrimRight(content, "\n")
		return page
	}

	for i := 1; i < closingIdx; i++ {
		line := lines[i]
		if after, ok := strings.CutPrefix(line, "title:"); ok {
			title := after
			page.Title = strings.TrimSpace(title)
		}
		if after, ok := strings.CutPrefix(line, "tags:"); ok {
			page.Tags = ParseTags(after)
		}
		if after, ok := strings.CutPrefix(line, "pin:"); ok {
			page.Pin = strings.TrimSpace(after) == "true"
		}
	}

	bodyStart := closingIdx + 1
	if bodyStart < len(lines) && lines[bodyStart] == "" {
		bodyStart++
	}
	page.Body = strings.TrimRight(strings.Join(lines[bodyStart:], "\n"), "\n")

	return page
}

func (p Page) Encode() []byte {
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

// wikiLinkOrCodeRe matches a fenced code block, an inline code span, or a [[wiki-link]] title.
var WikiLinkOrCodeRE = regexp.MustCompile("(?s)```.*?```|`[^`\n]*`|\\[\\[([^\\[\\]]+)\\]\\]")

func WikiLinks(body string) []string {
	matches := WikiLinkOrCodeRE.FindAllStringSubmatch(body, -1)

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
