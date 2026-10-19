package api

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	gitdiff "github.com/go-git/go-git/v5/utils/diff"
	"github.com/sergi/go-diff/diffmatchpatch"
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

// pageEditDiffContext is the number of unchanged lines shown around each
// change, as in git's default unified diff.
const pageEditDiffContext = 3

// pageEditDiffTimeout bounds the line diff of a large body. Past it the
// remaining text is reported as one delete and insert, which is still correct.
const pageEditDiffTimeout = time.Second

type pageDiffLine struct {
	op   byte
	text string
}

// PageEditDiff renders a unified diff of a body change with three lines of
// context around each change; empty when the two bodies are identical.
func PageEditDiff(old, new string) string {
	if old == new {
		return ""
	}
	var lines []pageDiffLine
	for _, change := range gitdiff.DoWithTimeout(old, new, pageEditDiffTimeout) {
		op := byte(' ')
		switch change.Type {
		case diffmatchpatch.DiffDelete:
			op = '-'
		case diffmatchpatch.DiffInsert:
			op = '+'
		}
		for line := range strings.SplitAfterSeq(change.Text, "\n") {
			if line != "" {
				lines = append(lines, pageDiffLine{op: op, text: line})
			}
		}
	}
	var out strings.Builder
	out.WriteString("--- body (before)\n+++ body (after)\n")
	oldLine, newLine := 1, 1
	for start := 0; start < len(lines); {
		first := nextPageDiffChange(lines, start)
		if first == len(lines) {
			break
		}
		// Changes whose contexts would touch or overlap share one hunk.
		last := first
		for {
			next := nextPageDiffChange(lines, last+1)
			if next == len(lines) || next-last-1 > 2*pageEditDiffContext {
				break
			}
			last = next
		}
		from := max(start, first-pageEditDiffContext)
		to := min(len(lines), last+pageEditDiffContext+1)
		// Lines skipped between hunks are unchanged, so both sides advance.
		oldLine += from - start
		newLine += from - start
		oldCount, newCount := 0, 0
		for _, line := range lines[from:to] {
			if line.op != '+' {
				oldCount++
			}
			if line.op != '-' {
				newCount++
			}
		}
		fmt.Fprintf(&out, "@@ -%s +%s @@\n", pageDiffRange(oldLine, oldCount), pageDiffRange(newLine, newCount))
		for _, line := range lines[from:to] {
			out.WriteByte(line.op)
			out.WriteString(line.text)
			if !strings.HasSuffix(line.text, "\n") {
				out.WriteString("\n\\ No newline at end of file\n")
			}
		}
		oldLine += oldCount
		newLine += newCount
		start = to
	}
	return out.String()
}

// nextPageDiffChange returns the index of the first added or removed line at
// or after from, or len(lines) when there is none.
func nextPageDiffChange(lines []pageDiffLine, from int) int {
	for i := from; i < len(lines); i++ {
		if lines[i].op != ' ' {
			return i
		}
	}
	return len(lines)
}

// pageDiffRange formats a hunk range as git does: a one-line range omits the
// count and an empty range names the line before it.
func pageDiffRange(start, count int) string {
	switch count {
	case 0:
		return fmt.Sprintf("%d,0", start-1)
	case 1:
		return fmt.Sprint(start)
	}
	return fmt.Sprintf("%d,%d", start, count)
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
