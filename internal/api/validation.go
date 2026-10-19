package api

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Page input bounds, enforced by every page-mutation operation before the
// checked write so no adapter can bypass them.
const (
	MaxPageBodyBytes  = 1 << 20
	MaxPageTitleRunes = 256
	MaxPageTags       = 32
	MaxPageTagRunes   = 64
	maxGitAuthorRunes = 256
)

func validRunes(s string, max int) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= max
}

// ValidatePageInput rejects a page title, tag set or body that exceeds the
// shared limits. It is the one semantic validation every page write shares.
func ValidatePageInput(title string, tags []string, body string) error {
	if !validRunes(title, MaxPageTitleRunes) {
		return fmt.Errorf("title must be at most %d characters", MaxPageTitleRunes)
	}
	if !utf8.ValidString(body) || len(body) > MaxPageBodyBytes {
		return fmt.Errorf("page body must be valid UTF-8 and at most %d bytes", MaxPageBodyBytes)
	}
	if len(tags) > MaxPageTags {
		return fmt.Errorf("page must have at most %d tags", MaxPageTags)
	}
	for _, tag := range tags {
		if !validRunes(tag, MaxPageTagRunes) {
			return fmt.Errorf("tags must be valid UTF-8 and at most %d characters", MaxPageTagRunes)
		}
	}
	return nil
}

// ValidateGitAuthor rejects a git author that is not valid UTF-8, exceeds the
// rune limit, or spans more than one line. It is shared by the author-setting
// and server-settings operations.
func ValidateGitAuthor(author string) error {
	if !validRunes(author, maxGitAuthorRunes) || strings.ContainsAny(author, "\r\n") {
		return fmt.Errorf("git author must be valid UTF-8, at most %d characters, and one line", maxGitAuthorRunes)
	}
	return nil
}
