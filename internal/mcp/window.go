package mcp

import "fmt"

// Long texts are returned in windows of characters so a large page or document
// cannot overflow a client's tool-result limit. The window size and its bounds
// are declared on read_page in schema/tools.json; read_attachment matches it.
var defaultTextWindowChars = toolDefault("read_page", "limit")

// textWindow describes the part of a longer text a result carries.
type textWindow struct {
	Offset     int  `json:"offset"`
	NextOffset int  `json:"next_offset"`
	TotalChars int  `json:"total_chars"`
	Truncated  bool `json:"truncated"`
}

// windowText returns limit characters of text from offset, or the default
// window when limit is zero. An offset past the end of the text is an error.
func windowText(text string, offset, limit int) (string, textWindow, error) {
	if limit <= 0 {
		limit = defaultTextWindowChars
	}
	runes := []rune(text)
	if offset > len(runes) {
		return "", textWindow{}, fmt.Errorf("offset %d is past the end of the text, which has %d characters", offset, len(runes))
	}
	end := min(offset+limit, len(runes))
	return string(runes[offset:end]), textWindow{
		Offset: offset, NextOffset: end, TotalChars: len(runes), Truncated: end < len(runes),
	}, nil
}
