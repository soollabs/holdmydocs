package wiki

import (
	"bytes"
	"strings"

	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/extension"
	"github.com/yuin/goldmark/v2/parser"
)

var sectionParser = parser.New(parser.WithExtensions(extension.GFMParser, extension.FootnoteParser))

// Section is the part of a Markdown body under one heading: the heading line
// through the line before the next heading of the same or a higher level.
// Start and End are byte offsets into the body.
type Section struct {
	Heading    string
	Level      int
	Start, End int
}

// Sections lists the body's top-level headings in order. Headings inside code
// blocks, quotes and lists are content, not section boundaries.
func Sections(body string) []Section {
	source := []byte(body)
	var sections []Section
	for node := range sectionParser.Parse(source).Children() {
		heading, ok := node.(*ast.Heading)
		if !ok || len(heading.Source()) == 0 {
			continue
		}
		lines := heading.Source()
		start := bytes.LastIndexByte(source[:lines[0].Start], '\n') + 1
		var text strings.Builder
		for i, line := range lines {
			if i > 0 {
				text.WriteByte(' ')
			}
			text.Write(bytes.TrimSpace(line.Bytes(source)))
		}
		sections = append(sections, Section{Heading: text.String(), Level: heading.Level, Start: start, End: len(body)})
	}
	for i := range sections {
		for _, next := range sections[i+1:] {
			if next.Level <= sections[i].Level {
				sections[i].End = next.Start
				break
			}
		}
	}
	return sections
}

// FindSection returns the first section whose heading text matches heading,
// ignoring case and surrounding space.
func FindSection(body, heading string) (Section, bool) {
	heading = strings.TrimSpace(heading)
	for _, section := range Sections(body) {
		if strings.EqualFold(section.Heading, heading) {
			return section, true
		}
	}
	return Section{}, false
}
