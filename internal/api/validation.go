package api

import (
	"fmt"
	"unicode/utf8"
)

// Page input bounds, enforced by every page-mutation operation before the
// checked write so no adapter can bypass them.
const (
	MaxPageBodyBytes  = 1 << 20
	maxPageTitleRunes = 256
	maxPageTags       = 32
	maxPageTagRunes   = 64
)

func validRunes(s string, max int) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= max
}

// ValidatePageInput rejects a page title, tag set or body that exceeds the
// shared limits. It is the one semantic validation every page write shares.
func ValidatePageInput(title string, tags []string, body string) error {
	if !validRunes(title, maxPageTitleRunes) {
		return fmt.Errorf("title must be at most %d characters", maxPageTitleRunes)
	}
	if !utf8.ValidString(body) || len(body) > MaxPageBodyBytes {
		return fmt.Errorf("page body must be valid UTF-8 and at most %d bytes", MaxPageBodyBytes)
	}
	if len(tags) > maxPageTags {
		return fmt.Errorf("page must have at most %d tags", maxPageTags)
	}
	for _, tag := range tags {
		if !validRunes(tag, maxPageTagRunes) {
			return fmt.Errorf("tags must be valid UTF-8 and at most %d characters", maxPageTagRunes)
		}
	}
	return nil
}
