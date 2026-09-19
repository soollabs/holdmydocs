package api

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// PageEdit is one exact-text replacement applied to a page body. OldText must
// occur exactly once when the edit is applied; NewText may be empty to delete.
type PageEdit struct {
	OldText string
	NewText string
}

// ApplyPageEdits applies ordered exact-text replacements to body. A missing or
// ambiguous match fails the whole call without a write, so an edit is all or
// nothing. The 128-replacement and body-size bounds match the page limits.
func ApplyPageEdits(body string, edits []PageEdit) (string, error) {
	if len(edits) == 0 {
		return "", fmt.Errorf("edits must contain at least one replacement")
	}
	if len(edits) > 128 {
		return "", fmt.Errorf("edits exceeds 128 replacements")
	}
	if len(body) > MaxPageBodyBytes {
		return "", fmt.Errorf("page body exceeds edit limit")
	}
	for i, edit := range edits {
		if edit.OldText == "" {
			return "", fmt.Errorf("edit %d oldText must not be empty", i+1)
		}
		if !utf8.ValidString(edit.OldText) || !utf8.ValidString(edit.NewText) {
			return "", fmt.Errorf("edit %d text must be valid UTF-8", i+1)
		}
		count := textOccurrenceCount(body, edit.OldText)
		if count == 0 {
			return "", fmt.Errorf("edit %d oldText was not found", i+1)
		}
		if count != 1 {
			return "", fmt.Errorf("edit %d oldText matched %d times; provide more context", i+1, count)
		}
		body = strings.Replace(body, edit.OldText, edit.NewText, 1)
		if len(body) > MaxPageBodyBytes {
			return "", fmt.Errorf("edited body exceeds %d bytes", MaxPageBodyBytes)
		}
	}
	return body, nil
}

// PageEditDiff renders a plain unified diff of a body change; empty when the
// two bodies are identical.
func PageEditDiff(old, new string) string {
	if old == new {
		return ""
	}
	var out strings.Builder
	out.WriteString("--- body (before)\n+++ body (after)\n")
	fmt.Fprintf(&out, "@@ -1,%d +1,%d @@\n", textLineCount(old), textLineCount(new))
	for _, line := range strings.SplitAfter(old, "\n") {
		out.WriteByte('-')
		out.WriteString(line)
		if !strings.HasSuffix(line, "\n") {
			out.WriteByte('\n')
		}
	}
	for _, line := range strings.SplitAfter(new, "\n") {
		out.WriteByte('+')
		out.WriteString(line)
		if !strings.HasSuffix(line, "\n") {
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func textLineCount(text string) int {
	if text == "" {
		return 0
	}
	count := strings.Count(text, "\n")
	if strings.HasSuffix(text, "\n") {
		return count
	}
	return count + 1
}

func textOccurrenceCount(text, needle string) int {
	count := 0
	for start := 0; ; {
		index := strings.Index(text[start:], needle)
		if index < 0 {
			return count
		}
		count++
		start += index + 1
	}
}
